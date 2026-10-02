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

- 普通 API 渠道的推理请求、模型连通性测试和上游模型列表同步使用对应 Provider 的官方 Go SDK 格式兼容标识；模型列表的每一页及同主机重定向、推理请求的重试及协议回退均保留该标识，不能使用 Go 默认的 `Go-http-client/1.1`。不发送本项目名称作为 User-Agent。移除 Codex 请求中原有的固定项目安装标记，独立探测及空会话转发的请求 ID 改为 UUID。
- Kiro 转发使用请求体 `conversationState.conversationId`，仅开启兼容的模型在 `x-opencode-session` 中发送；辅助分类请求也使用实际对话 ID，移除共享的 `kiro-classifier` 常量。
- 本地兼容接口的单模型与模型组路径均传递实际会话 ID，移除所有会话共用的 `local-compat` 常量。请求重试与协议回退沿用同一值。
- 同步发送原有 `session_id`、`conversation_id`、`x-session-id`、`session-id` 兼容头，支持 Codex 等客户端的原生 `session_id` 输入。
- 浏览器跨域预检允许这些会话头。
- 请求内容继续由真实编程客户端提供；不改写用户意图、不添加伪造的编程任务。模型连通性测试仍只是连通性探测，不能证明真实 Agent 流量被上游接受。
- 开启兼容后，每次模型连通性测试分配独立 UUID 并作为 `x-opencode-session` 发送；它是一段独立的单轮探测，不与其他测试或 Kiro 对话共用会话 ID。关闭兼容时不发送此头。

## 排查后端显示 Go 默认 UA

`Go-http-client/2.0` 是 Go HTTP/2 的缺省标识。生产 transport 的真实 TLS/HTTP/2 接收端测试验证：显式 SDK UA 会保留；只有删除 UA 头的对照请求才出现该缺省值。后端页面上的 UA 必须与同一条请求关联，不能仅根据协议或相近 Token 数认定本地漏头。

经过上游重试发送器的请求会产生 `upstream_identity` 流量日志，记录本地 `trace_id`、应用尝试次数、原始目标主机与路径，以及响应中的 `X-Request-Id`（或 `Request-Id`）、最终响应主机与路径、响应协议和状态。日志消息直接显示实际观察到的 UA 和最终响应 request ID，便于在流量页面搜索。请求跟随重定向时，原始目标与最终响应目标分别记录；最终响应 request ID 只属于最后一跳。

每条诊断是一次应用尝试的整个 `Client.Do` 返回响应头或失败时的快照，可能包含重定向、协议回退及 transport 内部重试。`encoded_user_agents` 只记录 UA 编码回调实际提供的值，`ua_header_observed=false` 表示快照时未观察到 UA 编码；不使用预期 SDK 值补齐证据。`negotiated_protocols` 记录连接回调提供的 TLS 协商协议（无 ALPN 时为空字符串），`write_results` 记录已经观察到的写入回调结果（`true` 为该回调没有错误，`false` 为该回调有错误）。空数组表示尚未观察到对应回调，不能推断失败或完成。

三类回调可能并发、交错或在响应返回后到达，三组列表分别按各自回调到达顺序记录，不能按下标一一对应，也不能据此判断某一物理发送或整个调用的写入结果。为保持原有流式响应时序，诊断不等待后续 trace 回调。编码观测只能证明本地编码器提供的头值，不能证明中转站或下一跳收到的值。

该诊断不修改请求头、正文、重试或路由规则，不记录 Authorization、Cookie、URL 查询参数、请求正文或响应正文。它仅覆盖调用上游重试发送器的 Kiro 单模型推理和本地单模型兼容请求；绕过该发送器的 Kiro／本地模型组直发、模型列表和独立模型测试不在这条诊断日志范围内。旧版日志无法补录历史出站 UA；本次测试通过也不等于已确认或修复外部后端显示异常的根因。

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

## 2026-09-08 实际对话 MissingSessionID 排查

用户反馈实际对话返回 `Error from provider (Console Go): Request is missing x-opencode-session`。只读核对发现：运行中的程序为提交 `d92867e` 的干净构建；失败模型的 `enable_opencode_session` 已在请求前开启；Kiro 解析日志中有非空真实会话 ID；失败请求由该模型转发到中转站的 `/v1/responses`，上游返回 HTTP 400，未发生传输回退或模型切换。用户确认中转站使用 new-api。

现有日志没有记录实际发送的会话请求头，所以这些事实还不能单独证明中转站在哪一步丢失了该头。源码核对未发现模型路由副本、工具结果回传、请求重试会删除它。

新增 `TestKiroSessionReachesHTTPUpstreamThroughRouter`，使用原始 Kiro JSON 工具结果回传请求，经过临时数据库、路由重载、单模型及模型组、完整 `Forward` 和本地 HTTP 服务接收端，验证开关 `false → true → false`、同会话复用、新会话隔离及 OpenAI SDK User-Agent。测试通过，说明这条本地链路能够把正确会话头发到第一跳。

新增集成测试与既有会话、重试及本地兼容测试一并执行 `go test -race ./proxy -run 'Test(KiroSessionReachesHTTPUpstreamThroughRouter|KiroClassifierAndRetryKeepRealSession|OpenCodeSessionIsExplicitPerModel|LocalCompatForwardsDistinctStableSessions)$' -count=1`，通过。本轮未修改生产数据库、运行程序或中转站配置，也未发送真实上游对话；实际链路仍需核查 new-api 渠道的请求头透传并用真实对话验收。

### 直连模型的测试按钮修复

随后用户新增 `https://opencode.ai/zen/go/v1` 直连模型并报告相同错误，最终确认此次来自模型配置的“测试”按钮。该模型已开启兼容，但 `api/test_model.go` 独立构造探测请求，原先没有设置 `x-opencode-session`。这与此前的 new-api 实际对话是两个不同入口，不能用其中一次的结果替代另一次验收。

修复使开启兼容的独立测试生成并发送一个 UUID，关闭时仍不发送；Provider SDK User-Agent 保持既定规则。测试服务模拟缺头即返回 HTTP 400 `MissingSessionID`，覆盖 Chat Completions 和 Responses 两个端点、开关切换、错误原文保留、独立测试 ID 不重复及各 Provider 标识。相关 API 和代理会话测试共同运行 `go test -race` 通过。真实 OpenCode 直连尚未重新测试，需要构建并运行包含本次修改的程序。

### new-api 渠道动态透传配置

对于经过 new-api 的模型，在实际连接 Console Go 的渠道“自定义请求头”配置中合并以下条目，保留其他已有配置：

```json
{
  "x-opencode-session": "{client_header:x-opencode-session}"
}
```

该占位符按每次客户端请求复制会话头，不应替换成固定会话 ID，也无需开启所有请求头的通配透传。准确字段为 `x-opencode-session`，末尾有 `n`。直连 OpenCode 不涉及这层配置。

语法依据为 [new-api 的 processHeaderOverride 和 DoApiRequest 实现](https://github.com/QuantumNous/new-api/blob/main/relay/channel/api_request.go)：`{client_header:<name>}` 必须是整个配置值；渠道 API 的 `header_override` 字段为承载该 JSON 的字符串。该实现会跳过 new-api 自身渠道测试中的客户端头占位符，因此应通过实际客户端请求验证透传。本文未修改实际渠道配置，当前安装版本是否支持此语法、上游最终是否收到头，仍需在渠道和真实对话中确认。
