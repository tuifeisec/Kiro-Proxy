# OpenAI / Claude SDK 接入与测试指南

> 更新时间：2026-07-22
> 本文是实际操作入口。架构依据见 [sdk-integration-architecture.md](sdk-integration-architecture.md)，当前总架构见 [architecture.md](architecture.md)。

## 1. 推荐接入方式

把 Kiro Proxy 做成“缓存感知的 SDK 兼容网关”：

```text
业务程序中的 OpenAI / Claude 官方 SDK
                  |
                  | HTTP JSON / SSE
                  v
       Kiro Proxy :18082 /v1/*
                  |
                  | 现有 Router + Forwarder
                  v
       OpenAI / Anthropic / Codex 上游
```

- 官方 SDK 只放在业务客户端和 `test/sdk-compat/` 契约测试中。
- Go 运行时继续使用现有 `Forwarder`，保留原始 JSON 和 SSE 透传。
- SDK 请求使用管理/API 端口 `18082`，不使用 Kiro MITM 端口 `18081`。
- OpenAI SDK 只调用 OpenAI/Codex 路由，Claude SDK 只调用 Anthropic 路由。
- 第一阶段不做 OpenAI 与 Claude 的跨协议转换。

SDK 负责提供稳定的客户端调用方式；缓存收益来自稳定前缀、缓存字段和会话状态，不来自安装 SDK 这个动作本身。

## 2. 当前能力与接入前缺口

### 已有能力

| 能力 | 当前实现 |
| --- | --- |
| OpenAI Responses | `POST /v1/responses` |
| OpenAI Chat Completions | `POST /v1/chat/completions` |
| Claude Messages | `POST /v1/messages` |
| 模型选择 | `Router.ResolveExact` 按启用模型的 `Name`、`ModelID` 精确匹配 |
| 请求处理 | 替换上游 `model`，其他 JSON 字段通常保留；当前会重新编码整个顶层对象 |
| 流式响应 | 原始 SSE 透传并旁路统计 usage |
| 缓存统计 | 保存 cache read/write Token，可在请求用量页和 API 中查看 |
| Kiro 跨轮同构 | context-gatherer bootstrap 和带 EnvironmentContext 的 user 在 current/history 中保持同一结构 |
| Kiro continuation 隔离 | 主 `vibe` 与 context/subagent 使用不同的 SQLite 状态键 |

### 正式接入前应补齐

| 优先级 | 缺口 | 影响 |
| --- | --- | --- |
| P0 | 官方 SDK 契约测试 | SDK 升级后没有自动兼容性判断 |
| P0 | 本地 API Token | LAN 模式下缺少独立的客户端凭证 |
| P0 | OpenAI/Claude 功能 Header 白名单 | 新 beta、幂等键、请求 ID 等可能丢失 |
| P0 | 厂商错误响应格式 | 官方 SDK 不能稳定识别本地错误类型 |
| P0 | 无损 model 替换 | 当前 `map[string]interface{}` 重编码可能改变大整数精度和顶层字节表示 |
| P0 | 稳定 SDK 会话 ID | 当前所有 SDK 请求都使用固定的 `local-compat`，会混淆不同会话 |
| P1 | `GET /v1/models` | `openai.models.list()` 还不可用 |
| P1 | `POST /v1/messages/count_tokens` | `anthropic.messages.countTokens()` 还不可用 |
| P1 | 新 usage 字段覆盖 | 新模型新增的 cache/usage 字段可能漏记 |

结论：三个 create 接口现在可以做基础联调，但完成 P0 后再作为稳定 SDK 接口对外使用。

### 项目现有缓存标准

SDK 接入必须以现有 Kiro 缓存链为基线，但 SDK 入口不会自动继承全部转换优化：

| 机制 | Kiro MITM | SDK `/v1/*` |
| --- | --- | --- |
| 拆分 system 动态尾部 | 代理自动处理 | 调用方负责 |
| 保持 EnvironmentContext 的 current/history 同构 | 代理保留原始 user content | 调用方负责保持消息结构稳定 |
| tools canonical hash、稳定排序和 10 分钟转换缓存 | 代理自动处理 | 不进入 converter，调用方保持顺序稳定 |
| `prompt_cache_key`、`user` | 由 Kiro `conversationId` 自动生成 | 客户端原样传入 |
| Anthropic system/tool/top-level breakpoint | 代理转换时自动生成 | 客户端原样传入 `cache_control` |
| Responses 自动 continuation | 模型开启后由代理管理 | 客户端管理 `previous_response_id` |
| cache usage | 代理读取上游字段 | 代理读取上游字段 |

Kiro 转换后的稳定前缀顺序是：static system 或首条 developer 消息、历史对话、稳定 tools；当前 tool result、system 动态尾部和原始 current user content 放在后面。`<EnvironmentContext>` 不再从 current 中单独拆出。

`jilu/kiro_traffic.log` 和修复前源码共同暴露的三个例外已经纳入回归测试：context-gatherer 的首轮/续轮角色、EnvironmentContext 的 current/history 表示，以及 Responses continuation 的 agent 分支状态隔离。完整证据和字段统计见 [sdk-integration-architecture.md](sdk-integration-architecture.md#原始-kiro-抓包对照)。

SDK 入口的目标是 JSON 语义直通和原始 SSE 直通。当前 `replaceModelInBody` 会重新编码顶层 JSON；正式接入应使用 `map[string]json.RawMessage` 或等价结构只替换 `model`，保留未知字段的原始值和 messages/input/tools 数组顺序。SDK 客户端及契约测试应主动构造与 Kiro 转换链相同的稳定布局。

## 3. 模型配置

先启动 Kiro Proxy，打开 `http://127.0.0.1:18082`，在“模型配置”中新增模型。

### OpenAI Responses

```text
Provider: openai
Name: OPENAI_ROUTE_NAME
ModelID: OPENAI_UPSTREAM_MODEL
APIKey: OPENAI_UPSTREAM_KEY
BaseURL: https://api.openai.com
Endpoint: /v1/responses
Enabled: true
```

### OpenAI Chat Completions

```text
Provider: openai
Name: OPENAI_CHAT_ROUTE_NAME
ModelID: OPENAI_UPSTREAM_MODEL
APIKey: OPENAI_UPSTREAM_KEY
BaseURL: https://api.openai.com
Endpoint: /v1/chat/completions
Enabled: true
```

### Claude Messages

```text
Provider: anthropic
Name: CLAUDE_ROUTE_NAME
ModelID: CLAUDE_UPSTREAM_MODEL
APIKey: ANTHROPIC_UPSTREAM_KEY
BaseURL: https://api.anthropic.com
Endpoint: /v1/messages
Enabled: true
```

`Name` 是 SDK 客户端看到的稳定路由名，`ModelID` 是真正发给上游的模型 ID。SDK 请求的 `model` 应优先填写 `Name`。同一个 `ModelID` 配置多个渠道时，每个渠道必须使用不同的 `Name`。

上游密钥只保存在模型配置中。客户端 SDK 的 `apiKey` 是访问本地网关的凭证，两者不要共用。

## 4. 客户端安装与初始化

2026-07-22 查询到的 npm 最新版本：

```powershell
npm install openai@6.48.0 @anthropic-ai/sdk@0.112.5
```

新项目可以使用 `@latest` 安装，再提交 `package-lock.json` 固定实际测试版本：

```powershell
npm install openai@latest @anthropic-ai/sdk@latest
```

### OpenAI SDK

OpenAI SDK 的 `baseURL` 必须带 `/v1`：

```js
import OpenAI from "openai";

const openai = new OpenAI({
  apiKey: process.env.KIRO_PROXY_TOKEN ?? "local-proxy",
  baseURL: "http://127.0.0.1:18082/v1",
});

const response = await openai.responses.create({
  model: process.env.OPENAI_MODEL ?? "OPENAI_ROUTE_NAME",
  input: "只回复 SDK route ok",
});

console.log(response.output_text);
```

Chat Completions 使用同一个客户端：

```js
const completion = await openai.chat.completions.create({
  model: process.env.OPENAI_CHAT_MODEL ?? "OPENAI_CHAT_ROUTE_NAME",
  messages: [{ role: "user", content: "只回复 chat route ok" }],
});

console.log(completion.choices[0]?.message?.content);
```

### Claude SDK

Claude SDK 的 `baseURL` 不带 `/v1`，SDK 会自行请求 `/v1/messages`：

```js
import Anthropic from "@anthropic-ai/sdk";

const anthropic = new Anthropic({
  apiKey: process.env.KIRO_PROXY_TOKEN ?? "local-proxy",
  baseURL: "http://127.0.0.1:18082",
});

const message = await anthropic.messages.create({
  model: process.env.CLAUDE_MODEL ?? "CLAUDE_ROUTE_NAME",
  max_tokens: 64,
  messages: [{ role: "user", content: "只回复 SDK route ok" }],
});

console.log(message.content);
```

当前本机模式无需软件激活，默认只允许回环地址访问。示例中的 `local-proxy` 是 SDK 所需的占位 key；上游 key 始终由 Kiro Proxy 注入。LAN 访问须按现有设置明确启用；本文关于未来本地 API Token 的规划不代表已交付能力。

## 5. 代码接入顺序

### 第一步：建立 SDK 契约测试

在 `test/sdk-compat/` 创建独立 Node 测试项目，并锁定两个 SDK 版本：

```text
test/sdk-compat/
  package.json
  package-lock.json
  openai.contract.test.mjs
  anthropic.contract.test.mjs
  helpers/
  fixtures/
```

测试先连接本地 mock upstream，记录 Path、Header、Body、SSE 和取消信号。基线至少覆盖：

- Responses 普通响应和流式响应。
- Chat Completions 普通响应和流式响应。
- Messages 普通响应和流式响应。
- 模型不存在、上游 429、上游 500。
- 客户端中止流式请求。

### 第二步：补兼容层基础契约

按职责拆分，不把协议兼容逻辑放进 `converter/`：

```text
proxy/local_compat.go             公共编排
proxy/local_compat_openai.go      OpenAI 路由、错误、models
proxy/local_compat_anthropic.go   Claude 路由、错误、count_tokens
proxy/local_compat_headers.go     入站 Header 白名单
proxy/local_compat_stream.go      SSE 透传
proxy/local_compat_usage.go       usage/cache 统计
```

需要实现：

1. 本地 API Token 与上游 API Key 分离。
2. OpenAI 传递 `OpenAI-Beta`、`Idempotency-Key`、`x-client-request-id`。
3. Claude 传递 `anthropic-version`、`anthropic-beta`、`anthropic-user-profile-id`。
4. `Authorization`、`x-api-key` 始终由模型配置覆盖，不从客户端透传。
5. 分别返回 OpenAI 和 Claude 的标准错误结构。
6. 增加 `GET /v1/models` 和 `POST /v1/messages/count_tokens`。
7. 只从原始请求提取会话和缓存元数据，不重排 SDK messages、input、system 或 tools。
8. 用 `json.RawMessage` 级别的顶层改写替换 `model`，不得把未知数字字段解码为 `float64`。

### 第三步：增加缓存感知会话键

当前 `proxy/local_compat.go` 调用 `sendWithRetry` 时固定传入 `local-compat`。应改为每个 SDK 会话独立的稳定值，建议优先级：

```text
x-kiro-session-id 请求头
  -> prompt_cache_key（OpenAI）
  -> user（OpenAI，兼容已有调用方）
  -> 空值，不发送缓存关联 Header
```

不要使用一个全局固定值，也不要把不断变化的 `previous_response_id` 当成会话 ID。Claude 没有对应的标准会话字段，持续对话由调用方显式传 `x-kiro-session-id`。Codex 的 `x-client-request-id` 在没有稳定 ID 时可以继续使用随机请求 ID，但不能把随机值当作跨轮会话键。

该会话 ID 只用于中转站缓存关联 Header 和 trace，不替代请求体中的缓存字段。

### 第三步之前：修正 Kiro 缓存基线

SDK 会话键只处理 `/v1/*` 客户端入口。Kiro MITM 链路还需先完成：

1. 保留原始 `agentMode`、`agentContinuationId`。
2. context-gatherer bootstrap 在 current 和 `history[0]` 都保持原始 user 角色，不再按 sub-agent marker 提升为 system。
3. 首版停止 current-only EnvironmentContext 重排；后续若拆分，current/history 必须共用一个纯规范化函数。
4. 分开定义 `PromptAffinityKey` 和 `ContinuationStateKey`，前者先保持 `conversationId`。
5. Responses SQLite 查询、保存和删除使用 `ContinuationStateKey`：普通 `vibe` 使用 `conversationId`，context/subagent 使用三字段组合。
6. 四种非标准会话 Header 在确认实际中转站语义后选择 affinity 或 continuation key，不先假定与 SQLite 相同。

不要把普通 `vibe` 的缓存键直接改为 `agentContinuationId`，否则每次新任务会重新分配缓存域。

### 第四步：补 usage 解析

保留原始响应，不修改事件，只旁路读取：

- OpenAI `usage.input_tokens_details.cached_tokens`。
- Chat Completions `usage.prompt_tokens_details.cached_tokens`。
- Claude `usage.cache_read_input_tokens`。
- Claude `usage.cache_creation_input_tokens` 和 `cache_creation` 明细。
- 流式累计 usage 取最大值，不重复相加。
- OpenAI Chat、Responses 和 Gemini 没有返回 cache write 时保持 0，不根据输入 Token 推算。

## 6. 接入后的测试顺序

### 6.1 静态检查和回归测试

```powershell
go test ./...
go test -tags dev ./...
npm --prefix frontend run build
npm --prefix test/sdk-compat test
```

普通构建与 `dev` 标签构建均无需软件激活。SDK 契约测试不得依赖授权服务器或真实上游额度。

Kiro 缓存回归测试还应固定验证：

- `Here is the last user message:` 动态尾部仍位于历史之后。
- `<EnvironmentContext>` 保持在原始 current user content 中，不做 current-only 拆分。
- context-gatherer bootstrap 在首轮 current 和第二轮 `history[0]` 都生成同一个 user item。
- 带 EnvironmentContext 的 current 在下一轮进入 history 后，转换出的 provider item 逐项一致。
- 普通 `vibe`、context-gatherer 和不同 subagent continuation 生成预期的 `ContinuationStateKey`，且 SQLite 状态互不覆盖。
- `PromptAffinityKey` 的变化单独测试，不与 continuation key 的断言混在一起。
- tools 的语义等价 JSON 产生相同 hash，并按名称稳定排序。
- SDK 语义直通不改变客户端提交的 input/messages/tools 数组顺序，未知大整数保持精度。
- Responses continuation 只在严格前缀增长时发送 delta，前缀变化时发送全量请求。
- 续传状态只在成功完成后保存，可识别的 4xx 会清理状态并回退全量。

### 6.2 真实上游冒烟测试

按以下顺序逐项执行，每项只发送最小请求：

1. OpenAI Responses 非流式。
2. OpenAI Responses 流式。
3. OpenAI Chat Completions 非流式和流式。
4. Claude Messages 非流式。
5. Claude Messages 流式。
6. OpenAI function tool call。
7. Claude tool use。
8. structured output、thinking、客户端取消和 429 错误。

验收条件：

- SDK 返回对象类型正确，流事件顺序完整。
- 管理端请求用量中 `protocol` 分别为 `responses`、`chat`、`messages`。
- 上游收到真正的 `ModelID`，客户端只使用配置的 `Name`。
- 客户端本地 Token 不出现在上游请求和流量日志中。
- Kiro MITM 原调用链没有变化。

## 7. 缓存测试

缓存测试分成两组，不能混为一个指标。

### 7.1 Prompt cache 命中测试

目标：验证相同静态前缀在第二次和后续请求中出现 cache read Token。

测试规则：

1. 同一模型、同一上游渠道连续请求 3 次。
2. 先确认该上游模型支持 prompt caching，并记录其最小可缓存长度。
3. developer/system、tools、消息顺序和静态内容保持逐字节一致。
4. 只修改最后一个用户问题。
5. OpenAI 三次使用相同 `prompt_cache_key` 和 `x-kiro-session-id`。
6. Claude 在稳定 system 或 tool block 上使用 `cache_control: { type: "ephemeral" }`，三次使用相同 `x-kiro-session-id`。
7. 静态前缀必须达到当前模型要求的最小可缓存长度；测试时建议使用明显高于阈值的固定文本。
8. tools 存在时使用同一份已稳定排序的常量数组，不在每轮动态拼装或改变 JSON schema。
9. Claude 有 tools 时只在最后一个稳定 tool 上设置 breakpoint，并保留顶层 `cache_control`，与 Kiro 转换链一致。

OpenAI 请求核心字段：

```js
await openai.responses.create({
  model: "OPENAI_ROUTE_NAME",
  input: [
    { role: "developer", content: STABLE_LONG_PREFIX },
    { role: "user", content: changingQuestion },
  ],
  prompt_cache_key: "sdk-cache-case-001",
  store: true,
}, {
  headers: { "x-kiro-session-id": "sdk-cache-case-001" },
});
```

Claude 请求核心字段：

```js
await anthropic.messages.create({
  model: "CLAUDE_ROUTE_NAME",
  max_tokens: 64,
  cache_control: { type: "ephemeral" },
  system: [{
    type: "text",
    text: STABLE_LONG_PREFIX,
    cache_control: { type: "ephemeral" },
  }],
  messages: [{ role: "user", content: changingQuestion }],
}, {
  headers: { "x-kiro-session-id": "sdk-cache-case-001" },
});
```

预期结果：

- 第一次主要建立缓存，cache read 可以为 0。
- 第二、三次 `cache_read_tokens` 应大于 0。
- Claude 第一次通常可观察到 `cache_write_tokens`，后续可观察到 `cache_read_tokens`。
- OpenAI Chat/Responses 当前项目标准只记录实际上游返回的 cache read；没有 cache write 字段时保持 0，不能用 write 为 0 判定缓存失效。

### 7.2 Responses continuation 测试

目标：验证 `previous_response_id` 只发送新增输入，减少重复请求体。

```js
const first = await openai.responses.create({
  model: "OPENAI_ROUTE_NAME",
  input: "第一轮问题",
  store: true,
});

const second = await openai.responses.create({
  model: "OPENAI_ROUTE_NAME",
  previous_response_id: first.id,
  input: "第二轮只发送增量问题",
  store: true,
});
```

验收时比较请求日志中的 body 大小，并确认第二次请求保留 `previous_response_id`。该测试验证状态复用和请求减量，不以 cache hit 百分比作为通过条件。

模型配置中的“启用 continuation”当前只作用于 Kiro MITM 转换链。SDK 入口由 SDK 客户端显式发送 `previous_response_id`，两者不要重复管理。

Kiro 自动 continuation 的额外验收条件是：只有前次完整 input 是本次 input 的逐项字节前缀时才发送 delta；只有明确完成且无错误的流才保存状态；上游明确拒绝续传时清理旧状态并回退全量。SQLite 状态超过 7 天会在启动时清理。

### 7.3 查看统计

管理界面的“请求用量”页可查看每次请求的输入、输出、缓存读取和缓存写入。也可以读取结构化 API：

```powershell
curl.exe "http://127.0.0.1:18082/api/request-usage?start=YYYY-MM-DD&end=YYYY-MM-DD&limit=100"
```

当前界面的命中率计算为：

```text
min(cache_read_tokens, input_tokens) / input_tokens * 100%
```

同一天混入旧请求会稀释汇总结果，因此验收应优先查看三次测试请求的单条记录，再计算这组请求的命中率。

## 8. 完成判定

满足以下条件后，SDK 接入才算完成：

- 最新锁定版本的 OpenAI 和 Claude SDK 契约测试全部通过。
- 三个现有 create 路由的普通响应、SSE、工具调用和错误均可被 SDK 正常解析。
- `GET /v1/models` 和 Claude Count Tokens 可用。
- LAN 模式强制本地 Token，上游 key 不离开代理进程。
- 不同 SDK 会话使用不同稳定会话 ID，不再共享 `local-compat`。
- Prompt cache 测试第二、三次出现真实 cache read Token。
- Responses continuation 的第二次请求只包含增量输入。
- `go test ./...`、`go test -tags dev ./...`、前端构建和 SDK 契约测试全部通过。
- Kiro MITM、Classifier、路由和现有缓存优化回归通过。

## 9. 实施范围建议

第一批实现只做：

```text
契约测试基线
  -> 本地 API Token
  -> Header 白名单
  -> 厂商错误格式
  -> 稳定 SDK 会话 ID
  -> models / count_tokens
  -> usage 与缓存验收
```

WebSocket、background Responses、Message Batches 和 OpenAI/Claude 跨协议转换留到基础契约稳定后再评估。
