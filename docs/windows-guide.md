# Windows 使用指南

适用版本：v2.0.2，Windows x64。下载入口见[项目首页](../README.md#下载)。

## 安装与启动

1. 下载 `kiro-proxy-v2.0.2-windows-amd64-unsigned.zip`，完整解压到你有写入权限的目录，不要直接在压缩包中启动。
2. 确认系统已安装 [Microsoft Edge WebView2 Runtime](https://developer.microsoft.com/microsoft-edge/webview2/)。桌面窗口通过它显示管理面板。
3. 运行 `kiro-proxy.exe`。本版无需软件激活、机器码登记或授权服务器。

此包没有 Windows Authenticode 签名。若系统显示安全提示，请先确认下载来源，并按下文校验文件；校验和用于核对文件完整性，不代表开发者签名。

仓库目前为私有仓库，下载需要登录有仓库访问权限的 GitHub 账号。GitHub 自动生成的 “Source code” 压缩包是源码，不是可直接运行的 Windows 发行包。

## 添加上游模型

打开“模型配置”，填写显示名称、Provider、上游基础地址、模型 ID 和凭据，保存后点击“测试”。

| Provider | 凭据与配置要点 |
| :--- | :--- |
| OpenAI | API Key；按上游实际支持选择 `/chat/completions` 或 `/responses` |
| Anthropic | API Key；使用 Messages 接口 |
| Gemini | API Key；按模型与上游要求配置地址 |
| Codex | OAuth 凭据；按页面流程配置 |

选择 OpenAI Provider 不代表上游服务一定支持 Responses。测试失败时，先检查地址、端点、模型 ID、凭据与上游网络连接。更多字段见[模型路由参考](reference.md#模型路由)。

## 连接 Kiro

1. 回到首页，点击“安装 CA 证书”，按系统提示完成证书安装。首次启动时程序会生成本地 CA；安装成功后，面板应显示“已安装并受信任”。
2. 先保存 Kiro 中未完成的工作，再点击“一键配置 Kiro 代理”。该操作会写入代理配置并自动重启 Kiro。Windows 下修改的是 `%APPDATA%\Kiro\User\settings.json`，原代理配置保存在同目录的 `settings.json.proxy-backup` 文件中。
3. 等待 Kiro 重启完成。后续需要手动重启时，按钮可能随当前状态显示为“重启 Kiro”或“更新并重启 Kiro”等；点击前同样先保存工作。
4. 在 Kiro 中选择已配置的模型，发送请求，再到管理面板检查结果。

首次配置建议先用明确指定的模型验证连通性。使用 Auto 前，需要配置有效策略和 default 目标；Auto 的职责与节点观察边界见[当前契约](2026-10-20-activity-driven-auto-routing.md)。

CA 安装和代理配置是两个独立步骤。代理会处理经它转发的 HTTPS 内容；请保管好 `certs/ca.key`，不要共享或上传到仓库。证书注入与进程环境的详细行为见[技术参考](reference.md#kiro-代理设置)。

## 后台运行与退出

关闭桌面窗口会隐藏到后台，代理继续运行。系统托盘菜单提供“打开窗口”“隐藏窗口”和“退出”。选择“退出”才会停止代理。

不再使用时，先保存 Kiro 中的工作，再在首页点击“还原原始代理”（会自动重启 Kiro），完成后从托盘退出 Kiro Proxy。如果只停止代理而没有还原配置，Kiro 可能继续连接已经停止的端口。

## 更新与备份

Windows 桌面版默认以进程工作目录保存相对路径数据，并未固定到 AppData。通常从解压目录启动时，数据会在该目录；快捷方式或命令行可以改变工作目录，请以实际启动方式为准。

更新前退出 Kiro Proxy，并备份实际使用的：

- `kiro-proxy.db`，以及存在的 `kiro-proxy.db-wal`、`kiro-proxy.db-shm`；
- `certs/`，包含本地 CA 证书和私钥；
- 自定义的 `config.yaml`。

解压新版，核对校验值后再替换程序，保留原数据目录和启动工作目录。旧数据库可能在启动时迁移；需要回退时，应同时恢复升级前的数据库备份。已迁移的数据库不要再由旧版程序写入。

## 常见问题

| 现象 | 检查方法 |
| :--- | :--- |
| 桌面窗口无法打开 | 确认 WebView2 Runtime 已安装；程序运行时可尝试浏览器打开 `http://127.0.0.1:18082`，默认端口未修改时适用 |
| Kiro 无法连接 | 确认代理正在运行、CA 已受信任，并在代理配置后重启过 Kiro；检查端口和上游模型测试结果 |
| 关闭窗口后仍在运行 | 这是后台运行行为；从系统托盘选择“退出” |
| 下载返回 404 或无权限 | 使用有仓库访问权限的 GitHub 账号登录；在 Release 中选择 Windows ZIP |
| 使用 Auto 时返回策略错误 | 检查策略是否有效、default 与职责目标是否可用；详细原因见请求日志 |

默认代理端口为 `18081`，管理/API 端口为 `18082`。自定义 `-port` 后，管理/API 端口随之变为代理端口加一。

## 校验下载文件

在下载目录用 PowerShell 执行：

```powershell
Get-FileHash -Algorithm SHA256 .\kiro-proxy-v2.0.2-windows-amd64-unsigned.zip
```

将结果与同一 Release 的 `.sha256` 文件比较。v2.0.2 的预期 SHA256：

```text
bf96d3ba192185823f5c1c89ac55af537c845a90bc64ada2e9fc5774418c3b39
```
