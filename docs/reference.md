# 配置与开发参考

本文说明 Kiro Proxy 的运行结构、配置字段和开发方式。首次使用请看[项目首页](../README.md)与 [Windows 使用指南](windows-guide.md)；模块边界以[当前架构](architecture.md)为准。

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
| api/ | Web UI 使用的模型、统计、设置、应用版本和测试接口 |
| frontend/ | Vue 3 + Vite 管理面板源码 |
| build/windows/ | Windows .rc 和 .ico 资源源文件 |
| test/ | 本地兼容入口和缓存回归测试 |
| docs/ | 项目已有的 SDK 接入、架构和计划文档 |

## 启动方式

### 开发版后端

开发版使用非 desktop 入口，会保留控制台窗口；开发版和发行版均无需软件激活。

~~~powershell
npm --prefix frontend install
npm --prefix frontend run build
go build -o kiro-proxy-dev.exe .
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
go build -tags desktop -ldflags="-H=windowsgui" -o kiro-proxy-dev.exe .
.\kiro-proxy-dev.exe
~~~

正式桌面构建使用仓库中的脚本：

~~~bat
build.bat
~~~

脚本会构建 frontend/、用 windres 生成 resource_windows.syso，再执行 go build -tags desktop。版本号可由参数传入，未传参数时交互输入，并通过 -X kiro-proxy/buildinfo.AppVersion=... 写入二进制。

### 跨平台构建与发布

Windows 上运行 `build.bat 2.0.2`；macOS 上运行 `bash build-mac.sh 2.0.2`。构建脚本要求平台原生工具链；Windows 脚本还需要 PowerShell 7、MinGW-w64 GCC、windres 和 objdump。GitHub Actions 工作流执行回归、编译并上传 workflow artifacts，正式 Release 资产需要另行核验和上传。

当前 v2.0.2 仅发布 Windows x64 包，由本机原生编译并验证；macOS 包待补齐。构建脚本存在不等于对应平台已有可下载产物。

Mac 版要求 macOS 12 Monterey 或更新系统。分发包未经过 Apple Developer ID 签名和公证，Windows 包未经过 Authenticode 签名，系统可能显示安全提示。私有仓库的 Release 下载需要 GitHub 仓库访问权限。

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

Windows 下程序写入 http.proxy 和 http.proxyStrictSSL=false，并把原有代理保存到同目录的 settings.json.proxy-backup 文件。一键配置与还原原始代理都会自动重启 Kiro，点击前应先保存工作；UI 也提供单独重启 Kiro 的操作。

首次启动时 runner.go 会自动生成：

~~~text
certs/ca.crt
certs/ca.key
~~~

使用 HTTPS MITM 前，需要在 Web UI 中安装该 CA 证书并重启 Kiro。

安装 CA 只更新操作系统的证书信任，不写入用户级、机器级或登录会话级的 `NODE_EXTRA_CA_CERTS`。管理面板配置或重启使用本程序代理的 Kiro 时，将校验后的 CA 绝对路径传给该 Kiro 进程；代理归属按实际监听地址和端口判断，支持 `localhost` 与 IPv4 回环地址，以及 LAN 模式下仍可访问服务的本机 IPv4 地址。还原原始代理时不额外注入本程序 CA。代理进程自身和其他程序的环境保持不变。证书读取、程序定位、旧进程退出或启动失败时，接口返回实际错误；未确认旧 Kiro 已退出时取消启动，不报告重启成功。

Kiro 的一键配置和重启保留代理 CA 注入与进程验证，不再预检、安装或启用 JavaScript 阶段桥接。Auto 使用原生请求 continuation 维持同一轮模型稳定，activity 在本地停发前提供异步节点观察。首轮没有节点证据时使用已配置 default，具体边界见[无补丁 Auto 契约](2026-10-20-activity-driven-auto-routing.md)。

旧版本可能遗留指向已移动或删除文件的用户级 `NODE_EXTRA_CA_CERTS`，导致其他 Node 命令提示 `Ignoring extra certs`。清理前先记录原值，只删除已经核实属于本程序的失效值，不覆盖其他软件设置的证书路径。已运行的终端和 IDE 保留原环境，需要重新启动相应宿主才能使用清理后的环境。

## Kiro 请求处理

router.DetectProtocol 根据 Kiro 域名和路径识别请求：

- 旧 AWS 域名：识别 generateAssistantResponse、ListAvailableModels 和 getUsageLimits。
- 新版 runtime.*.kiro.dev：根路径会读取请求体确认是否为对话请求。
- management.*.kiro.dev：处理模型列表和用量限制。
- Kiro runtime 的 `POST /agents/activity` 在本地丢弃并返回空 `204`，转发和抓包模式均不向官方发送活动正文；不新增模型请求统计。详见[活动停发契约](2026-10-17-kiro-activity-suppression.md)。
- 其他目标请求按 kiro-passthrough 原样放行。

代理有两种模式：

| 模式 | 行为 |
| --- | --- |
| forward | 解析 Kiro 请求，转换协议并发送到模型上游 |
| capture | 记录原始请求和响应，然后放行到 Kiro 原始服务器，不做转换 |

模式通过 PUT /api/proxy-mode 切换，值为 forward 或 capture。

活动上报停发在两种模式下均生效；该路径不记录活动正文，抓包模式也不放行到官方服务。

## 模型路由

### Auto 与本地估算积分

转发模式下，管理面板的 Auto 配置使用固定职责或兼容阶段策略，明确指定 default、各职责目标和节点规则。raw Responses 的 Auto 必须启用有效策略；策略未启用、配置无效或目标不可用时返回明确错误，不放行官方，也不静默改用其它候选。无需创建名为 Auto 的分组。无补丁请求按原生 continuation 保持同轮模型稳定；节点观察、职责与路径规则的选择边界见下方“[Kiro Auto 职责路由](#kiro-auto-职责路由)”。

请求标识仍是 `auto`。规则只作用于 Kiro 的 Auto 主生成请求，不覆盖手动模型或独立 Classifier。旧 SDK 入口保留策略关闭时的兼容默认候选行为，该行为不适用于 raw Responses。Kiro 官方 Auto 的内部算法未公开；历史方案见 [Auto 官方说明与任务路由复核](2026-10-06-auto-task-routing.md)，当前行为以[无补丁 Auto 契约](2026-10-20-activity-driven-auto-routing.md)为准。

价格配置以 `USD / 百万 Token` 为单位。本地估算积分为包含缓存读写和本地倍率的配置费用除以 `0.04 USD/Credit`；它不等于 Kiro 官方账单。管理界面 Credits 四舍五入显示两位小数，内部计算和累计保留原始精度；请求明细的 USD 费用分项显示六位小数。Kiro 用量接口返回本地自然月的累计积分，保留小数精度。旧库升级时在事务中备份并一次性转换历史积分；升级后不能混用旧版程序写入同一数据库。

计算公式、日志证据、迁移与验证边界见 [Auto 路由与本地估算积分](2026-10-06-auto-credits.md)。

### Typesafe 意图分类

首页高级设置使用统一 Classifier 下拉框：选择 `Typesafe classifier · jev-1.13.0` 后，通过同行的“设置”按钮配置 API Key 和模型参数，再点击“保存设置”启用。基础地址默认为 `https://api.typesafe.ai`，下拉标签随配置模型更新。Key 读回只显示 `****`，保留该值表示不修改，清空表示删除。选择其他 Classifier 选项并保存后关闭 Typesafe，执行所选方式；仅在转发模式调用，抓包模式仍放行官方。选择与参数保存规则见 [Classifier 统一模型选择](2026-10-06-classifier-selection.md)。

分类结果按 Typesafe 返回的三类概率传回 Kiro，Auto 继续按上述规则选模。分类费用单独统计，默认输入价为 `$0.042 / 百万 Token`，输出免费，再按 USD ÷ 0.04 换算本地估算 Credits。协议、配置、失败行为与验证边界见 [Typesafe 接入 Kiro 意图分类](2026-10-06-typesafe-classifier.md)。

模型配置保存在 SQLite 的 models 表中。Kiro 模型列表使用配置 UUID 作为稳定的 modelId、Name 作为显示名；路由同时支持模型或分组标识，并兼容 Name 和上游 ModelID。`auto` 使用上述专门的本地选择规则。

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
/inspector        流量抓包检查器
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

本地兼容入口默认只允许回环地址访问；把 proxy_access_mode 设置为 lan 后才允许局域网访问。入口根据请求 API 和模型 Provider 校验路由类型，无需软件激活或访问授权服务器。

## 数据和日志

Windows 桌面版不切换工作目录。配置、数据库、日志和证书的默认相对路径以进程工作目录为基准；以自定义参数启动时，请记录实际路径。运行数据与构建产物的区别如下：

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

## Kiro Auto 职责路由

转发模式下，Kiro runtime 的 raw `model=auto` 请求进入本地固定路由，不再因缺少桥接头而放给官方。正常启动不修改 Kiro JavaScript，不依赖桥接结构匹配。

原生 `kiro:agent_continuation_id` 与会话、模式共同绑定一轮模型选择，同一 continuation 内保持模型稳定；手动选模清除对应分支的 Auto 锁。activity 每 3 秒异步批量上报，代理在本地返回空 204，仅提取有界节点观察，按同流 seq 排除乱序与重放，子流不覆盖父流。

已有且无冲突的节点观察可供新 continuation 初选；首轮缺少观察时使用用户已配置的 default，并记录默认原因。activity 不是当前请求的同步节点身份，不能保证每个节点首轮都精准按职责选模，也不会为了等 activity 固定阻塞请求。

节点规则支持完整 definition 三元组或明确的路径规则（definition_id 与 definition_revision 同时为空）。三元组精确匹配优先；缺少 definition 时，路径唯一命中才使用，歧义则落 default。路径按原始分段编码，保留 repeat 的 iter-N；配置了职责目标仍需明确的节点绑定，不按裸节点名猜职责。

详见[无补丁 Auto 与 activity 观察契约](2026-10-20-activity-driven-auto-routing.md)。存量桥接工具只保留兼容诊断与严格校验，不参与正常启动。
