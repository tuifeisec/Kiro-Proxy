# Kiro Proxy

Kiro Proxy 是当前项目中的本地代理服务和管理面板。程序监听一个 MITM 代理端口，识别 Kiro IDE 的请求，按数据库中的模型配置转换并转发到上游，再把上游响应转换回 Kiro 使用的 AWS Event Stream。

项目还在同一个 Web 服务上提供 OpenAI 和 Anthropic 的本地兼容入口。模型配置、请求统计、缓存统计、流量日志和 Kiro 代理设置都由 Vue 前端调用 Go API 完成。

## 实际运行结构

~~~text
main.go / main_desktop.go
        |
        +-- runner.go
        |     +-- rules.LoadConfig
        |     +-- store.Open(SQLite)
        |     +-- proxy.NewServer
        |
        +-- proxy.Server
              +-- MITM 代理: cfg.Proxy.Port
              +-- Web UI/API: cfg.Proxy.Port + 1
              +-- frontend/dist (go:embed)
~~~

默认端口来自 rules.DefaultConfig()：

~~~text
MITM 代理: 18081
Web UI/API: 18082
~~~

如果 config.yaml 不存在或解析失败，程序会使用默认配置；-port 参数可以覆盖代理端口，Web UI 端口会跟随变成代理端口加一。

## 目录职责

| 目录/文件 | 当前职责 |
| --- | --- |
| main.go | 非桌面构建入口，启动后端并等待信号 |
| main_desktop.go | desktop 构建入口，启动 WebView 窗口和系统托盘 |
| runner.go | 配置、CA、SQLite、服务器和前端嵌入资源的组装 |
| proxy/ | goproxy MITM、拦截、转发、重试、日志、模式和本地兼容 API |
| converter/ | Kiro 请求解析、OpenAI/Responses/Anthropic/Gemini 转换和流式帧转换 |
| router/ | 根据 Kiro 模型名解析数据库模型配置 |
| store/ | SQLite schema、模型配置、请求记录、统计和 Responses 状态 |
| api/ | Web UI 使用的模型、统计、设置、授权、更新和测试接口 |
| frontend/ | Vue 3 + Vite 管理面板源码 |
| build/windows/ | Windows .rc 和 .ico 资源源文件 |
| test/ | 本地授权策略和缓存回归测试 |
| docs/ | 项目已有的 SDK 接入、架构和计划文档 |

## 启动方式

### 开发版后端

开发版使用非 desktop 入口，会保留控制台窗口；dev 标签只切换本地开发授权策略。

~~~powershell
npm --prefix frontend install
npm --prefix frontend run build
go build -tags dev -o kiro-proxy-dev.exe .
.\kiro-proxy-dev.exe
~~~

也可以直接运行：

~~~powershell
go run . -config config.yaml -port 18081 -db kiro-proxy.db
~~~

可用参数：

~~~text
-config     配置文件路径，默认 config.yaml
-port       覆盖代理端口，默认使用配置文件或 18081
-db         SQLite 路径，默认 kiro-proxy.db
~~~

### Windows 桌面版

桌面版使用 desktop 标签，启动 WebView 窗口和系统托盘；-no-window 可以只启动后端服务。

~~~powershell
go build -tags "desktop dev" -ldflags="-H=windowsgui" -o kiro-proxy-dev.exe .
.\kiro-proxy-dev.exe
~~~

正式桌面构建使用仓库中的脚本：

~~~bat
build.bat
~~~

脚本会构建 frontend/、用 windres 生成 resource_windows.syso，再执行 go build -tags desktop。版本号由脚本交互输入，并通过 -X kiro-proxy/license.AppVersion=... 写入二进制。

## 配置文件

rules/config.go 定义的配置字段如下，配置文件不是必须的：

~~~yaml
proxy:
  port: 18081
  verbose: true
  log_file: kiro_traffic.log
  log_detail: summary
  log_max_body: 65536
~~~

log_detail 只有 full 会使用完整详情，其余值都会归一化为 summary。log_max_body 控制日志详情最大字节数。

## Kiro 代理设置

Web UI 的 Kiro 代理设置会修改当前用户的 Kiro 配置：

~~~text
%APPDATA%\Kiro\User\settings.json
~~~

Windows 下程序写入 http.proxy 和 http.proxyStrictSSL=false，并把原有代理保存到同目录的 .proxy-backup 文件。UI 也提供恢复代理和重启 Kiro 操作。

首次启动时 runner.go 会自动生成：

~~~text
certs/ca.crt
certs/ca.key
~~~

使用 HTTPS MITM 前，需要在 Web UI 中安装该 CA 证书并重启 Kiro。

## Kiro 请求处理

router.DetectProtocol 根据 Kiro 域名和路径识别请求：

- 旧 AWS 域名：识别 generateAssistantResponse、ListAvailableModels 和 getUsageLimits。
- 新版 runtime.*.kiro.dev：根路径会读取请求体确认是否为对话请求。
- management.*.kiro.dev：处理模型列表和用量限制。
- 其他目标请求按 kiro-passthrough 原样放行。

代理有两种模式：

| 模式 | 行为 |
| --- | --- |
| forward | 解析 Kiro 请求，转换协议并发送到模型上游 |
| capture | 记录原始请求和响应，然后放行到 Kiro 原始服务器，不做转换 |

模式通过 PUT /api/proxy-mode 切换，值为 forward 或 capture。

## 模型路由

模型配置保存在 SQLite 的 models 表中。Kiro 请求里的 modelId 首先按配置的 Name 精确匹配，再兼容匹配 ModelID；因此 Name 是 Kiro 下拉框显示名称，也是路由键。

主要字段：

| 字段 | 当前含义 |
| --- | --- |
| name | Kiro 可见名称和首选路由键 |
| model_id | 发给上游 JSON 的 model 值 |
| provider | openai、anthropic、gemini 或 codex |
| base_url | 上游基础地址 |
| endpoint | 上游端点路径 |
| api_key | OpenAI/Anthropic/Gemini Key；Codex 使用 OAuth 字段 |
| proxy_url | 该模型单独使用的上游代理，可为空 |
| disable_auto_suffix | 是否停用端点改写和 Gemini query 追加 |
| reasoning_effort | 默认 low、medium、high 或 OpenAI/Codex 的 xhigh |
| enable_continuation | Responses 是否启用 previous_response_id 状态 |

协议选择来自 provider 和 endpoint：

- OpenAI + /chat/completions：Kiro 转 OpenAI Chat Completions。
- OpenAI + /responses：Kiro 转 OpenAI Responses；classifier 请求会强制走 Chat Completions。
- Anthropic：转 /messages，并发送 Anthropic 缓存相关 Header。
- Gemini：转 streamGenerateContent，默认会处理 {model} 和 alt=sse。
- Codex：使用 /codex/responses 和 OAuth access token 刷新流程。

上游服务只实现 Chat Completions 时，OpenAI 模型应配置 /chat/completions；provider=openai 只决定本地转换方向，不代表上游一定实现 /responses。

## Web UI 和 API

Web UI 由 frontend/dist 嵌入 Go 二进制，后端路由在 proxy/server.go 和 api/server.go 注册。

页面路由：

~~~text
/                 仪表盘
/models           模型配置
/usage            用量明细
/request-usage    请求统计、筛选和分页
/inspector        流量抓包检查器（需要对应日志权限）
~~~

主要 HTTP 接口：

~~~text
GET/POST/PUT/DELETE /api/models
GET                 /api/stats
GET                 /api/request-usage
GET/PUT             /api/settings
POST                /api/test-model
GET/PUT             /api/proxy-mode
GET/PUT             /api/proxy-access-mode
GET/POST/DELETE     /api/kiro-proxy
POST                /api/kiro-restart
GET                 /api/logs
GET                 /ws
~~~

默认本地兼容入口：

~~~text
POST http://127.0.0.1:18082/v1/chat/completions
POST http://127.0.0.1:18082/v1/responses
POST http://127.0.0.1:18082/v1/messages
~~~

本地兼容入口默认只允许回环地址访问；把 proxy_access_mode 设置为 lan 后才允许局域网访问。入口还会执行授权检查，并根据请求 API 和模型 Provider 校验路由类型。

## 数据和日志

默认运行目录中会产生：

~~~text
kiro-proxy.db       SQLite 数据库
kiro-proxy.log      程序运行日志
kiro_traffic.log    流量日志
certs/              MITM CA 证书和私钥
frontend/dist/      构建后的前端资源（被嵌入二进制）
~~~

SQLite 主要表：models、settings、daily_stats、request_usage、responses_sessions。请求统计记录输入/输出 Token、缓存读写、费用、转换耗时、TLS/连接耗时、重试次数和连接复用状态。

Responses continuation 只在模型启用后使用；程序会比较上一轮完整 input 的严格前缀，前缀不一致时回退全量请求。过期的 Responses 状态会在数据库启动迁移时清理。

## 测试和检查

~~~powershell
go test ./...
git status --short
~~~

模型页面的“测试”按钮调用 POST /api/test-model，按模型 Provider 发送最小流式请求，并返回首字时间、耗时和粗略吞吐信息。

## 本地文件安全

不要提交以下文件：

- kiro-proxy.db 及 SQLite WAL 文件；
- config.yaml；
- certs/ca.key；
- API Key、Refresh Token、OAuth 数据；
- kiro-proxy.log 和 kiro_traffic.log；
- 构建生成的 .exe、.syso 和前端 dist/。

.gitignore 已按上述本地运行产物配置，同时保留 build/windows/kiro-proxy.rc 和 build/windows/kiro-proxy.ico 这两个 Windows 资源源文件。
