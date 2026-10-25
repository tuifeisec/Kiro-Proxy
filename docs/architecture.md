# 当前架构

更新时间：2026-10-25。本文是目录、服务边界和发布方式的入口；协议细节以所链接的特性契约为准。

## 运行结构

Kiro Proxy 由一个 Go 程序提供 MITM 代理、管理 API、SDK 兼容入口和内嵌 Vue 管理面板。默认代理端口为 18081，管理/API 端口为代理端口加一。desktop 构建额外启动 WebView 和系统托盘。

```text
main.go / main_desktop.go
  -> runner.go（配置、CA、SQLite、前端资源）
  -> proxy.Server
       -> Kiro MITM / 协议识别 / Auto 与手动路由
       -> /v1/responses、/v1/chat/completions、/v1/messages
       -> api.Server / frontend/dist / WebSocket 日志
       -> Forwarder -> OpenAI / Anthropic / Gemini / Codex
```

## 模块边界

| 路径 | 职责 |
| --- | --- |
| runner.go、main*.go、desktop_*.go | 服务生命周期、平台窗口、托盘、单实例与数据目录 |
| proxy/ | MITM、原始 Responses、SDK 入口、连接池、流式转发、重试、模型组、日志和 Kiro 设置 |
| converter/ | Kiro 与各 Provider 的请求、响应和事件流转换 |
| router/ | 模型匹配、Auto 职责与节点路径选择 |
| store/ | SQLite schema、配置、凭据、请求统计与 continuation 状态 |
| api/ | 模型、分组、统计、设置、测试和应用构建版本接口 |
| frontend/ | 唯一随主程序发布的 Vue 管理面板，抓包页面为 /inspector |
| codex/、typesafe/、upstreamproxy/、ua/、billing/ | OAuth、分类器、上游连接、User-Agent 与费用计算 |
| buildinfo/ | 应用版本和可执行文件构建身份 |
| build/windows/ | Windows 图标与版本资源源文件 |
| test/、各包 *_test.go | Mock 上游与回归契约 |
| tools/ | 仍需保留的诊断、证书和历史桥接工具 |

沿用原代码结构规范：每个文件只做一件事，文件名即职责说明；单文件不超过 300 行，超过就拆。存量文件的结构治理不属于本次清理。结构体与导出函数使用 PascalCase，内部函数使用 camelCase，文件使用 snake_case，测试文件使用 xxx_test.go 并尽量与被测实现同目录。

转换层返回 (result, error)，不 panic；代理层记录错误并返回相应 HTTP 错误，不中断服务；流式转换器错误写入 pipe 的 error 交由调用方处理。日志不得泄露模型密钥、OAuth 凭据或提示正文。新增能力按影响补充稳定的 Mock 回归与必要真实验证。

## 取消软件激活

自本次版本起，软件不要求激活、机器登记、二进制哈希白名单或授权服务器可用。Kiro 转发、SDK 兼容接口、Typesafe 分类、管理界面与流量日志不再调用软件授权服务。删除机器码采集、授权后台及广告。开发版和发行版采用同一使用策略，不使用授权开关或伪造授权结果。

上游 API Key、Codex OAuth、系统 CA 信任与本地访问边界继续生效：取消软件激活不改变这些协议和系统要求。SDK 入口默认仅回环访问；显式 LAN 设置继续按现有访问契约工作。

应用版本由 buildinfo.AppVersion 写入，/api/app-version 返回版本和构建身份。更新下载入口指向 GitHub Releases；程序启动不再向原授权服务器请求广告或版本信息。私有仓库的下载需要有仓库访问权限的 GitHub 账号。

## 当前特性契约

- [无补丁 Auto 与 activity 观察](2026-10-20-activity-driven-auto-routing.md)：同一原生 continuation 保持模型稳定。activity 为异步观察，不保证首轮职责身份；缺少可靠身份时使用已配置 default，禁止用固定等待或猜测节点弥补。
- [活动停发](2026-10-17-kiro-activity-suppression.md)：forward/capture 两模式均本地消费活动正文并返回空 204。
- [Auto 职责细分观察日志](2026-10-22-auto-routing-diagnostics.md)。
- [SDK 兼容架构](sdk-integration-architecture.md)及[接入指南](sdk-integration-guide.md)。
- [Auto 积分](2026-10-06-auto-credits.md)、[Classifier 选择](2026-10-06-classifier-selection.md)和[Typesafe](2026-10-06-typesafe-classifier.md)。

保留文档中的历史测试记录只描述当时结果。涉及旧授权、旧目录或已撤回桥接设计的历史段落不构成当前行为；当前边界以本文、当前特性契约和 README 为准。独立特性契约不会因日期较早而被删。

## 构建与发布

GitHub Actions 工作流定义了 Windows x64、macOS arm64 和 macOS Intel 构建矩阵，安装锁文件依赖、构建前端、运行回归、原生编译桌面程序，并生成带 SHA256 的分发包。工作流上传 workflow artifacts，不自动发布 Release；正式发布需核验目标平台的原生构建、无窗口启动及 API/前端资源后，再上传同一提交的资产。

当前 v2.0.2 已发布 Windows x64 包，由本机原生编译并验证；macOS 包待补齐。实际可下载平台以 [Release](https://github.com/tuifeisec/Kiro-Proxy/releases/latest) 为准。首次使用见 [Windows 使用指南](windows-guide.md)，配置和开发细节见[技术参考](reference.md)。

Mac 包没有 Apple Developer ID 签名和公证；构建所用 ad-hoc 签名只确保包结构完整，不代表 Apple 认可的开发者身份。Windows 包没有 Authenticode 签名。下载说明须保留此限制；CI 的无窗口启动不等于人工桌面交互验收。

## 清理与恢复

移除未接入主程序的 .vben-template/、旧独立 frontend-inspector/、授权服务 go/ 和 license/，以及已被当前契约取代的早期总规划和架构提案。业务代码、测试、有效特性文档、Windows 资源和依赖锁文件保留。历史版本可从本次提交的父提交恢复；不重写 Git 历史。

仓库清理先在 GitHub 完成，随后按用户授权将源码修改同步到本机，并完成 Windows 原生构建。源码同步与打包不清理开发机上的历史备份、运行数据库、证书或已存在程序。
