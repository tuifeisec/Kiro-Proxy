<div align="center">

<img src="frontend/public/favicon.svg" width="72" alt="Kiro Proxy 图标">

# Kiro Proxy

**在 Kiro 中使用你配置的模型，集中管理路由、用量与请求。**

连接 OpenAI、Anthropic、Gemini 与 Codex，在本地面板中完成配置和排查。

**[下载 Windows 版](https://github.com/tuifeisec/Kiro-Proxy/releases/download/v2.0.2/kiro-proxy-v2.0.2-windows-amd64-unsigned.zip)** · **[更新说明](https://github.com/tuifeisec/Kiro-Proxy/releases/latest)** · **[使用指南](docs/windows-guide.md)** · **[技术文档](docs/architecture.md)**

<sub>v2.0.2 · Windows x64 · 无需软件激活</sub>

</div>

## 用它做什么

Kiro Proxy 是面向 Kiro IDE 的本地模型代理与桌面管理工具。添加上游模型后，由代理转换请求协议并转发；你可以在同一个面板里管理模型、查看用量，定位请求问题。

- **接入自己的模型**：配置 OpenAI Chat Completions / Responses、Anthropic Messages、Gemini，或通过 Codex OAuth 接入。
- **管理模型与路由**：维护模型和分组，配置 Kiro Auto 的默认目标、职责目标与节点规则。
- **查看每次请求**：查看 Token、缓存读写、耗时和按配置价格估算的费用；通过流量检查器排查请求。
- **配置 Kiro 代理**：安装本地 CA 证书、写入代理设置、重启 Kiro，也可以还原原始代理。
- **复用本地 API**：为支持自定义地址的客户端提供 OpenAI Chat / Responses 和 Anthropic Messages 兼容入口。

上游服务仍需要有效的 API Key 或 OAuth 凭据；可用模型及其能力取决于你接入的服务。

## 下载

| 平台 | 当前版本 | 下载与要求 |
| :--- | :--- | :--- |
| **Windows x64** | **v2.0.2 · 已发布** | [下载 ZIP](https://github.com/tuifeisec/Kiro-Proxy/releases/download/v2.0.2/kiro-proxy-v2.0.2-windows-amd64-unsigned.zip) · 解压运行 · 需要 [WebView2 Runtime](https://developer.microsoft.com/microsoft-edge/webview2/) |
| macOS · Apple Silicon | 待发布 | 暂无安装包 |
| macOS · Intel | 待发布 | 暂无安装包 |

Windows 包尚未进行 Authenticode 签名，系统可能显示安全提示。仓库目前为私有仓库，下载时需要登录有访问权限的 GitHub 账号。

[全部版本](https://github.com/tuifeisec/Kiro-Proxy/releases) · [SHA256 校验文件](https://github.com/tuifeisec/Kiro-Proxy/releases/download/v2.0.2/kiro-proxy-v2.0.2-windows-amd64-unsigned.zip.sha256) · [构建清单](https://github.com/tuifeisec/Kiro-Proxy/releases/download/v2.0.2/windows-amd64-manifest.json)

## 开始使用

1. **解压并启动。** 将 ZIP 完整解压到可写目录，运行 `kiro-proxy.exe`。若桌面窗口无法打开，先确认已安装 WebView2 Runtime。
2. **添加模型。** 在“模型配置”中填写对应 Provider、上游地址、模型 ID 和凭据，保存后使用“测试”验证连接。OpenAI 兼容服务请按实际能力选择 `/chat/completions` 或 `/responses`。
3. **连接 Kiro。** 在首页点击“安装 CA 证书”，按系统提示完成信任。先保存 Kiro 中未完成的工作，再点击“一键配置 Kiro 代理”；该操作会写入配置并自动重启 Kiro。
4. **发起对话并检查结果。** 在 Kiro 中选择已配置的模型，发送请求，再回到面板查看请求和用量。需要使用 Auto 时，先配置有效的 Auto 策略及 default 目标。

关闭桌面窗口后，代理会继续在后台运行；要停止服务，请在系统托盘菜单中选择“退出”。停止使用代理前，先保存 Kiro 中的工作，再点击“还原原始代理”（会自动重启 Kiro），避免 Kiro 继续连接已停止的本地端口。

详细步骤、更新备份和常见问题见 [Windows 使用指南](docs/windows-guide.md)。

## 支持的接入方式

| 接入方向 | 协议或方式 |
| :--- | :--- |
| Kiro → 上游服务 | OpenAI Chat Completions、OpenAI Responses、Anthropic Messages、Gemini、Codex OAuth |
| 其他客户端 → 本地 API | `/v1/chat/completions`、`/v1/responses`、`/v1/messages` |

默认地址：

```text
Kiro HTTP/HTTPS 代理  http://127.0.0.1:18081
管理面板与本地 API   http://127.0.0.1:18082
```

本地 API 的模型名称与路由需要匹配已有配置，不同 Provider 的协议能力有区别。接入示例见 [SDK 使用指南](docs/sdk-integration-guide.md)。

## 使用边界

- **证书与代理。** Kiro 的 HTTPS 请求会经本地代理解密和转发，需要信任本程序生成的 CA。请妥善保管 CA 私钥和上游凭据；不要将数据库、证书或流量日志提交到仓库。
- **本地访问。** 默认仅允许回环地址访问；局域网访问需要在面板中明确配置，并按提示重启程序。
- **Auto 路由。** 同一原生 continuation 内保持模型选择稳定；节点信息是异步观察，不能保证每个节点首轮都按职责精准选模。缺少可靠观察时使用已配置的 default。详见 [Auto 路由契约](docs/2026-10-20-activity-driven-auto-routing.md)。
- **费用统计。** 面板中的费用和 Credits 按本地配置估算，不等同于 Kiro 或上游服务的官方账单。

## 文档与开发

| 文档 | 内容 |
| :--- | :--- |
| [Windows 使用指南](docs/windows-guide.md) | 安装、首次配置、更新与故障排查 |
| [当前架构](docs/architecture.md) | 模块职责、服务边界与有效特性契约 |
| [配置与开发参考](docs/reference.md) | 配置字段、命令参数、模型路由、API 与源码构建 |
| [SDK 使用指南](docs/sdk-integration-guide.md) | 本地兼容入口与客户端接入 |
| [v2.0.2 更新记录](docs/releases/v2.0.2.md) | 本次变化、下载和构建验证范围 |

源码使用 Go 与 Vue 3。开发和桌面构建步骤见[配置与开发参考](docs/reference.md#启动方式)。
