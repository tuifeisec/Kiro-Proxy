# OpenAI / Claude SDK 兼容实施计划

> 更新时间：2026-07-22
> 实际接入与验收见 [sdk-integration-guide.md](sdk-integration-guide.md)，架构依据见 [sdk-integration-architecture.md](sdk-integration-architecture.md)。

## 1. 目标

让以下最新版官方 SDK 通过 Kiro Proxy 稳定调用已配置模型：

- OpenAI Node SDK：Responses、Chat Completions。
- Claude TypeScript SDK：Messages、Count Tokens。

同时保持：

- Kiro MITM 原链路不变。
- OpenAI、Anthropic、Gemini、Codex 共享现有 Forwarder。
- SDK 新增未知 JSON 字段保持语义和值精度，messages/input/tools 数组顺序不变。
- 上游密钥不暴露给 SDK 客户端。

## 2. 支持范围

### 第一阶段

| 优先级 | 路由 | 状态/动作 |
| --- | --- | --- |
| P0 | `POST /v1/responses` | 保留现有实现并补契约测试 |
| P0 | `POST /v1/chat/completions` | 保留现有实现并补契约测试 |
| P0 | `POST /v1/messages` | 保留现有实现并补 Header/错误契约 |
| P0 | `GET /v1/models` | 新增 SDK 格式模型列表 |
| P1 | `POST /v1/messages/count_tokens` | 新增 Claude Token Count |

### 后续按真实需求增加

- `GET /v1/responses/{id}`
- `DELETE /v1/responses/{id}`
- `POST /v1/responses/{id}/cancel`
- Responses input items
- OpenAI background/WebSocket mode
- Claude Message Batches
- OpenAI 与 Claude 跨协议转换

不一次性实现所有官方 API，只覆盖项目能够正确转发、观测和解释的能力。

## 3. 阶段 0：固定当前行为

在 `test/sdk-compat/` 建立独立 Node 测试项目：

```text
test/sdk-compat/
  package.json
  package-lock.json
  openai.contract.test.mjs
  anthropic.contract.test.mjs
  fixtures/
```

依赖：

- `openai`
- `@anthropic-ai/sdk`

工作项：

1. 锁定测试使用的 SDK 版本。
2. 使用本地 mock upstream，避免测试消耗真实额度。
3. 记录请求 Path、Header、Body 和取消状态。
4. 记录 SDK 对响应、流和错误的实际解析结果。
5. 先让现有基础 create/stream 场景通过。
6. 断言 SDK 语义直通不改变 input/messages/tools 的数组顺序、未知字段和值精度。
7. 为 Kiro 转换链保留 static system、动态尾部、EnvironmentContext 和 tools 稳定排序回归用例。
8. 为 Kiro Responses continuation 增加严格前缀命中、前缀变化全量发送、成功状态保存和 4xx 清理回退用例。
9. 从 `jilu/kiro_traffic.log` 提取最小化 fixture，覆盖 context-gatherer 首轮和工具续轮；fixture 只保留结构和必要 marker，不复制完整规则、文件树或凭证。

验证命令建议统一为：

```powershell
npm --prefix test/sdk-compat test
go test ./...
go test -tags dev ./...
```

## 4. 阶段 1：基础网关契约

### 4.0 修正 Kiro 跨轮规范化和 continuation 状态（已完成）

这是抓包验证后新增的 P0，先于 SDK 网关扩展实施。

涉及文件：

- `converter/types.go`
- `converter/kiro_parse.go`
- `converter/cache_optimize.go`
- `converter/kiro_to_openai.go`
- `converter/kiro_to_responses.go`
- `converter/kiro_to_anthropic.go`
- `converter/kiro_to_gemini.go`
- `proxy/forwarder.go`
- `proxy/forwarder_stream.go`
- `store/responses_session.go`

工作项：

1. 在 `KiroRequest` 保留顶层 `AgentMode`。
2. 在 `ParsedConversation` 保留 `AgentMode`、`AgentContinuationID`、`PromptAffinityKey` 和 `ContinuationStateKey`。
3. context-gatherer 的 bootstrap 在首轮 current 和后续 `history[0]` 都保持一个原始 user item；不要清空 current 后只留下 system。
4. P0 移除 current-only EnvironmentContext 重排，恢复 current/history 的同构表示。
5. `PromptAffinityKey` 先保持 `conversationId`；普通 `vibe` 的 `ContinuationStateKey` 使用 `conversationId`，context/subagent 使用 `conversationId + agentMode + agentContinuationId`。
6. Responses session 查询、成功保存和失败删除全部使用 `ContinuationStateKey`。
7. 四种上游会话 Header 暂不跟随 SQLite key 改动，先用目标中转站 fixture 明确它们是路由亲和还是状态会话。
8. 将 `<session_start_snapshot>` 和 EnvironmentContext 的结构化细分延后为 P1；实现时 current/history 必须共用同一个纯函数。

验证：

- 抓包 fixture 的第一轮完整 Responses input 是第二轮 input 的严格前缀。
- 替换现有只比较公共前两项的“PrefixStability”断言，逐项比较第一轮全部 input。
- 第一轮和第二轮的 bootstrap 都是同一个 user item，内容和消息边界完全一致。
- 普通带 EnvironmentContext 的 user 消息从 current 进入 history 后，OpenAI、Responses、Anthropic、Gemini 的规范化结果不变。
- context-gatherer 的 continuation 状态不覆盖前后的 `vibe` 状态。
- 新 `vibe` 任务更换 `agentContinuationId` 后仍沿用 conversation scope。
- Prompt affinity 与 continuation state 分别断言，不使用一个通用 scope 掩盖两种语义。
- 抓包没有 Provider cache Token，因此测试只验证前缀与状态隔离；真实命中率仍由上游 smoke test 的 cache read 字段验收。

### 4.1 本地 API 鉴权

涉及文件：

- `store/setting.go`
- `proxy/access_mode.go`
- `proxy/local_compat.go`
- `frontend/src/components/dashboard/AdvancedSettings.vue`

工作项：

1. 增加 `local_api_token_hash` 设置。
2. 仅本机模式允许固定占位 key。
3. 局域网模式强制校验本地 Token。
4. Token 与 `store.Model.APIKey` 完全分离。
5. 管理界面提供生成、轮换和脱敏显示。

验证：

- loopback 使用占位 key 成功。
- LAN 无 Token 返回 401。
- LAN 正确 Token 成功。
- 客户端 Token 不出现在上游请求和日志中。

### 4.2 无损 model 替换

涉及文件：

- `proxy/forwarder_request.go`
- `proxy/local_compat.go`
- `test/sdk-compat/`

工作项：

1. 将 `replaceModelInBody` 的顶层值改为 `json.RawMessage` 或等价 JSON AST。
2. 只替换 `model`，不把未知数字解码为 `float64`。
3. 保持 messages、input、tools 等数组顺序。
4. 对未知嵌套字段保持语义和值精度。

验证：超过 JavaScript 安全整数范围的未知整数、未知嵌套对象和 tools 数组经过代理后保持不变，只有 `model` 被替换。

### 4.3 协议 Header 白名单

涉及文件：

- `proxy/local_compat_headers.go`（新增）
- `proxy/local_compat.go`
- `proxy/forwarder.go`

工作项：

1. 过滤客户端 Header。
2. OpenAI 保留 `OpenAI-Beta`、`Idempotency-Key`、`x-client-request-id`。
3. Claude 保留 `anthropic-version`、`anthropic-beta`、`anthropic-user-profile-id`。
4. 上游鉴权仍由模型配置生成。
5. 重试时复用同一组功能 Header 和幂等键。
6. 接受本地扩展 `x-kiro-session-id`，消费后生成现有会话关联 Header，不把它当作上游厂商 Header 直接透传。

验证：mock upstream 收到功能 Header，但收不到客户端 Authorization。

### 4.4 厂商错误结构

涉及文件：

- `proxy/local_compat_openai.go`（新增）
- `proxy/local_compat_anthropic.go`（新增）
- `proxy/local_compat.go`

覆盖错误：

- JSON 解析失败。
- 缺少 model。
- 模型不存在。
- Provider 与入口协议不匹配。
- 本地鉴权失败。
- 上游连接失败。
- 上游 4xx、429、5xx。

验证：官方 SDK 能解析 status、错误类型、message 和 request ID。

### 4.5 模型列表

涉及文件：

- `proxy/server.go`
- `proxy/local_compat_openai.go`
- `router/router.go`

规则：

- 只返回启用模型。
- 对外 ID 使用稳定、唯一的 `Name`，空 Name 才使用 ModelID。
- 不返回 BaseURL、API Key、Refresh Token 和内部数据库 ID。
- Provider 信息可放入扩展字段，但不改变 OpenAI 基础结构。

验证：OpenAI SDK 的 `models.list()` 和常见模型选择器可读取。

### 4.6 SDK 会话 ID

涉及文件：

- `proxy/local_compat.go`
- `proxy/forwarder.go`
- `proxy/local_compat_headers.go`（新增）

工作项：

1. 移除所有 SDK 请求共用的固定 `local-compat` 会话 ID。
2. 支持显式 `x-kiro-session-id`。
3. OpenAI 可从 `prompt_cache_key`、`user` 提取稳定会话 ID。
4. 没有稳定标识时保持空值，不发送缓存关联 Header，避免跨会话串联。
5. 不使用不断变化的 `previous_response_id` 作为会话 ID。

验证：不同会话不会共享缓存关联 Header；同一会话重试时保持相同 ID。

### 4.7 Usage 补齐与文件拆分

涉及文件：

- `proxy/local_compat_usage.go`（新增）
- `proxy/local_compat_stream.go`（新增）
- `proxy/local_compat.go`

工作项：

1. 保留现有累计 usage 取最大值策略。
2. 保持 OpenAI Chat/Responses cache write 为上游真实值；上游不返回时记录 0。
3. 只在有官方 schema 或真实 fixture 时增加新的 Claude usage 位置。
4. usage 解析只观察，不修改 SSE。
5. 将入口编排、流和统计从单文件中拆开。

## 5. 阶段 2：官方 SDK 常用功能

### Claude Count Tokens

新增 `POST /v1/messages/count_tokens`，沿用：

- 本地鉴权。
- `Router.ResolveExact`。
- Anthropic 模型和上游密钥。
- Header 白名单。

响应原样返回，统计中将其标记为 `messages_count_tokens`，不计作生成输出。

### OpenAI Responses

验证：

- 非流式和流式文本。
- Function tool call/output。
- Structured output。
- `previous_response_id`。
- 客户端取消。
- 429 和流中错误。

### Claude Messages

验证：

- 非流式和流式文本。
- `tool_use`、`input_json_delta`、`tool_result`。
- adaptive thinking、signature。
- Structured output。
- 客户端取消。
- 429 和流中错误。

## 6. 契约测试矩阵

| 协议 | 场景 | 验收点 |
| --- | --- | --- |
| Responses | 非流式文本 | SDK 返回 `output_text` |
| Responses | 流式文本 | delta 顺序及 completed 事件完整 |
| Responses | Function tool | call ID、name、arguments 完整 |
| Responses | Structured output | SDK 正确解析目标结构 |
| Chat | 流式工具调用 | choices、tool_calls、finish_reason 完整 |
| Messages | 非流式文本 | content blocks、stop_reason 完整 |
| Messages | 流式文本 | message/content block 事件顺序完整 |
| Messages | Tool use | tool use、增量 JSON、result 完整 |
| Messages | Adaptive thinking | thinking、signature、文本块完整 |
| Messages | Count tokens | SDK 返回 input token 数 |
| 两种协议 | 错误 | SDK 解析状态、类型、message、request ID |
| 两种协议 | 客户端取消 | 上游 Context 结束，统计状态明确 |
| 两种协议 | 缓存 | cache read/write Token 与上游一致，不合成缺失字段 |

## 7. 完成标准

1. 最新 OpenAI Node SDK 可调用 Responses 和 Chat Completions。
2. 最新 Claude TypeScript SDK 可调用 Messages 和 Count Tokens。
3. 普通响应、SSE、工具调用和错误均由 SDK 正常解析。
4. 未识别的新 JSON 字段不会被兼容入口主动删除。
5. 局域网访问强制使用独立本地 Token。
6. 上游 API Key 不出现在客户端响应、流量日志和请求统计中。
7. Kiro MITM 调用链、Classifier、缓存优化和 Event Stream 行为保持不变。
8. OpenAI、Anthropic、Gemini、Codex 继续共享 Forwarder 和统计体系。
9. SDK 请求不再共用固定 `local-compat` 会话 ID。
10. 普通构建与 `dev` 构建的 Go 测试均通过。
11. SDK 契约测试全部通过。

## 8. 推荐执行顺序

```text
SDK 契约测试基线
  -> Kiro 跨轮规范化与 continuation 状态隔离
  -> 本地 API Token
  -> 无损 model 替换
  -> Header 白名单
  -> 厂商错误结构
  -> 稳定 SDK 会话 ID
  -> GET /v1/models
  -> usage 补齐和文件拆分
  -> Claude count_tokens
  -> 工具、thinking、structured output
  -> 后续扩展端点
```

第一批改动集中在本地鉴权、无损 model 替换、协议 Header、错误结构和模型列表。完成后再通过官方 SDK测试推动其他功能，不先引入运行时厂商 SDK，也不先做跨协议转换。

## 9. 官方参考

- [OpenAI SDK](https://developers.openai.com/api/docs/libraries)
- [OpenAI Responses 迁移指南](https://developers.openai.com/api/docs/guides/migrate-to-responses)
- [OpenAI Responses API Reference](https://developers.openai.com/api/reference/resources/responses/methods/create)
- [OpenAI GPT-5.6 指南](https://developers.openai.com/api/docs/guides/latest-model.md)
- [Claude TypeScript SDK](https://platform.claude.com/docs/en/api/sdks/typescript)
- [Claude Messages API](https://platform.claude.com/docs/en/api/messages)
- [Claude Streaming](https://platform.claude.com/docs/en/build-with-claude/streaming)
- [Claude Tool Use](https://platform.claude.com/docs/en/agents-and-tools/tool-use/overview)
