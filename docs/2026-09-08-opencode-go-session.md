# OpenCode Go 客户端标识与会话传递

本次按 [OpenCode Go 官方要求](https://opencode.ai/docs/go/#where-can-i-use-it) 添加可选的上游兼容功能。官方说明这些信息用于路由与提示词缓存优化；它们不保证某次请求命中缓存或获得固定幅度的提速。

## 在模型配置中启用

编辑需要此兼容功能的模型，在“模型能力参数”中开启“OpenCode 会话兼容”并保存。字段为 `enable_opencode_session`，新模型和升级前已有模型默认关闭。开关按模型独立保存，模型组使用当前实际候选模型的设置；修改保存后随路由重载生效。

- 关闭：不额外发送 `x-opencode-session`。
- 开启：有真实会话 ID 时发送 `x-opencode-session`。
- 开关不改变 User-Agent；客户端标识仅由 Provider 决定。
- 不按域名或 Provider 自动启用；官方 OpenCode 地址和自定义中转地址都由用户按模型自行选择。
- 无需手填固定会话 ID，也不会因上游不支持此头而自动重试。会话 ID 由客户端每段对话提供，代理在转发时复用。

## 实际修复

- 普通 API 渠道使用对应 Provider 的官方 Go SDK 格式兼容标识，模型连通性测试遵循相同规则；不发送本项目名称作为 User-Agent。移除 Codex 请求中原有的固定项目安装标记，独立探测及空会话转发的请求 ID 改为 UUID。
- Kiro 转发使用请求体 `conversationState.conversationId`，仅开启兼容的模型在 `x-opencode-session` 中发送；辅助分类请求也使用实际对话 ID，移除共享的 `kiro-classifier` 常量。
- 本地兼容接口的单模型与模型组路径均传递实际会话 ID，移除所有会话共用的 `local-compat` 常量。请求重试与协议回退沿用同一值。
- 同步发送原有 `session_id`、`conversation_id`、`x-session-id`、`session-id` 兼容头，支持 Codex 等客户端的原生 `session_id` 输入。
- 浏览器跨域预检允许这些会话头。
- 请求内容继续由真实编程客户端提供；不改写用户意图、不添加伪造的编程任务。模型连通性测试仍只是连通性探测，不能证明真实 Agent 流量被上游接受。

## Provider 对应标识

按用户明确选择，使用官方 SDK 的标识格式，不使用 Codex CLI/Claude Code CLI 标识替代普通 OpenAI/Anthropic API 渠道。固定版本仅用于兼容请求头，不表示本程序引入或实际执行了这些 SDK；没有新增 SDK 依赖或运行时版本查询。

| Provider | User-Agent |
| --- | --- |
| `openai` | `OpenAI/Go 3.56.0` |
| `anthropic` | `Anthropic/Go 1.71.0` |
| `gemini` | `google-genai-sdk/1.71.0 gl-go/<实际 Go runtime.Version()>` |
| `codex` | 保留现有 Codex OAuth 协议的 `Codex Desktop/260202.0859 (darwin; arm64)` |

OpenAI/Anthropic 格式来自官方 [OpenAI Go 请求构造](https://github.com/openai/openai-go/blob/v3.56.0/internal/requestconfig/requestconfig.go) 和 [Anthropic Go 请求构造](https://github.com/anthropics/anthropic-sdk-go/blob/v1.71.0/internal/requestconfig/requestconfig.go)；版本分别来自 [OpenAI v3.56.0](https://github.com/openai/openai-go/blob/v3.56.0/internal/version.go) 和 [Anthropic v1.71.0](https://github.com/anthropics/anthropic-sdk-go/blob/v1.71.0/internal/version.go)。Gemini 格式与版本来自 [Google Gen AI Go v1.71.0](https://github.com/googleapis/go-genai/blob/v1.71.0/api_client.go#L224-L275)。空 Provider 或未知 Provider 沿用 OpenAI 兼容协议的默认标识。

OpenCode Go 页面要求专属编程 Agent 标识；用户指定的 SDK 标识不等于满足这项要求。此开关提供的是会话头兼容，真实接入是否被上游接受仍需单独验证。

## 客户端接入

Kiro 的会话 ID 由现有请求体提供。通过本地 `/v1/chat/completions`、`/v1/responses`、`/v1/messages` 接入时，按下列优先级选择第一个非空标识：

1. `x-opencode-session`
2. `x-conversation-id`
3. `x-session-id`
4. `session_id`
5. `conversation_id` 请求头
6. `session-id`
7. 请求体 `conversation_id`

建议客户端新建对话时生成 UUID，后续多轮、工具调用、同会话辅助请求复用它；新对话使用新 UUID。例如：

```http
x-opencode-session: 7e71a66d-cf34-4a4a-9d9e-ddcd68baf015
```

对于开启此兼容的模型，缺少真实标识时不发送稳定会话头，不能宣称已满足 OpenCode 的会话要求。未开启的模型不受此要求约束。`prompt_cache_key` 和 system/tools 前缀散列只保留作代理内部缓存亲和回退，不作为上游对话身份；不同对话可以共享相同提示词前缀。

## 验证与生效范围

本轮验证覆盖开关关闭、开启、再次关闭，模型配置持久化与旧库默认值，三种本地协议的单模型/模型组转发，客户端会话复用及模型探测的 User-Agent。授权依赖使用 Mock，上游使用 httptest 或 Mock transport，数据库使用临时文件。

- `go test ./store ./api` 通过。
- `go test -race ./proxy ./store ./api ./router ./ua -count=1` 通过。
- 前端 `npm.cmd run build` 通过。
- `git diff --check` 通过。

SDK 标识调整后的检查：`go test ./ua ./api ./proxy`、`go test -race ./ua ./api ./proxy -count=1` 和前端构建通过。空会话备用 ID 改为 UUID 后，重新运行 `go test -race ./proxy -count=1` 通过。上游 Mock 断言覆盖各 Provider、非空/空会话、会话开关开启/关闭、请求头无固定项目标记，以及 Codex OAuth 标识保留。

SDK 标识及空会话 UUID 修复经过新的独立只读终验，通过，无阻塞项。

本次没有真实调用 OpenCode、部署或重启现有程序。代码需要构建并运行新版本后，界面开关及数据库迁移才可用；实际缓存命中率、首内容延迟的变化仍需真实编程会话验证。
