# OpenAI Responses 工具正则兼容修复

## 故障证据

2026-10-02 02:13 的 Kiro 请求实际发往 OpenAI `/v1/responses`。上游 SSE 返回 `response.failed`、内层 HTTP 400、`invalid_function_parameters`，指出 `run_workflow` 的 `parameters` 中表达式 `^[^\p{Cc}\p{Zl}\p{Zp}"]*$` 不满足 `regex` 格式。请求在提交首个有效事件前失败，现有流式重试发送一次相同请求后仍失败。重试提示是错误处理结果，不是 schema 被拒绝的原因。

现有工具转换保留 Kiro `toolSpecification.inputSchema.json` 的参数 schema，Responses 转换没有改写 `pattern`。日志正文有截断，尚不能从失败记录还原完整 `run_workflow` schema。Kiro 扩展 `kiroAgent` 1.1.237 的打包源码 `extensions/kiro.kiro-agent/dist/extension.js` 第 1775 行定义了完全相同的 Unicode 正则和 `runLabel` 字符串约束，第 15054 行将该约束用于 `run_workflow` 的可选 `runLabel` 参数。这确认了客户端工具定义的来源；仍未确认该次请求在客户端具体经过哪条 JSON Schema 序列化路径。

该表达式在 JavaScript Unicode 模式中合法；Python 标准 `re` 编译会因 `\p` 报错，与上游的拒绝一致。此结果不能证明上游具体使用哪一个正则库。

## 授权与范围

用户已明确允许本轮修复扩展到 OpenAI `/responses`。仅对现有路由已选中 Responses 转换、Kiro 协议且 `provider=openai`、实际出站 URL 路径为 `/responses` 或其带前缀路径的请求应用。普通请求和显式续接都使用相同规则。Codex provider、Chat Completions、Anthropic、Gemini、本地兼容入口的原始透传和其他实际 URL 沿用原工具 schema。本轮不修改协议路由或 SSE 重试行为。

## 转换契约

仅在工具名为 `run_workflow`，且 schema 节点的 `pattern` 精确等于已确认表达式时进行替换。保留字符串校验，使用显式字符范围表示同一排除集合：`Cc` 为 U+0000–U+001F 与 U+007F–U+009F，`Zl` 为 U+2028，`Zp` 为 U+2029，双引号也继续排除。保留原来的 `^`、`$` 和重复次数，不扩大到其他 Unicode 类别或未知表达式。

只遍历 JSON Schema 子节点；描述、默认值、枚举值中的同名内容不改。保持工具数量、顺序、描述、引用路径及其他约束。解析使用 `json.Number` 保留数字值；修改时复制缓存工具列表，不污染共享定义。出站工具哈希、prompt cache key 和审计摘要使用规范化后的定义。

最终设置 `store` 字段时维持原有对象键递归排序，仅在实际 OpenAI Responses 路径通过 `Decoder.UseNumber()` 精确保留数字，再修改 `store` 的值；其他实际 URL 保持旧数字处理。复审和出站回归测试发现，原实现通过默认 `interface{}` 数字类型重编码整个请求，会将 schema 中 `9007199254740993` 舍入为 `9007199254740992`；初版 `json.RawMessage` 处理虽然保留精度，却改变了原有嵌套对象键序，专项缓存复审后改为同时保留精度与原有排序。

OpenAI Responses 的本地工具转换缓存使用独立 `openai-responses` 分区，按保留数字精度的原始工具摘要取值，源数组顺序进入缓存身份。该分区缓存规范化结果及其精确 provider hash，转换与审计复用同一条目；实际出站工具仍按现有名称排序。provider hash 保留既有字段、顺序、分隔符和截断方式，仅改为保留数字精度的 JSON 规范化。全局 `hashTools`、Chat、Codex 和其他 provider 的哈希及缓存流程保持现状；不统一失效续接 scope，续接请求每轮仍发送当前工具定义。

## 验证与交付

- 本地 JavaScript 将原表达式（Unicode 模式）与替换表达式逐一比较全部 1,112,064 个 Unicode 标量，结果无差异；Python 标准正则接受替换表达式，按 Unicode 类别逐一验证也无差异。
- 回归测试覆盖脱敏错误 fixture、嵌套 schema、引用路径、注释数据、数值保真、缓存隔离、幂等、普通与续接路径及实际 provider/URL 边界；Mock 上游捕获实际请求并验证正则可解析。
- 缓存专项修正后重新通过 `go test ./converter ./proxy -count=1`、`go build ./...`、本轮 Go 文件的 `gofmt -d` 与 `git diff --check`。普通与续接的最终出站测试证明大整数仍为 `9007199254740993`，工具审计哈希与 `prompt_cache_key` 一致。
- 相关缓存与续接集成测试通过：`go test ./test -run 'TestPrefixAuditAttributesAppendAndRewrite|TestOpenAICachePrefixCurveWithMockProvider|TestResponsesContinuationSkipsPreviousAssistantMessage|TestResponsesSessionsAreIsolatedByContinuationStateKey|TestContextCompactionResetsThenRebuildsContinuation' -count=1`。
- 当前修正代码的并发检测通过：`go test -race ./converter ./proxy -run 'TestToolCache|TestOpenAIResponses|TestResponsesCacheContract|TestSetResponsesStore' -count=1`。
- 忽略目录 `bin/` 中此前生成的 `kiro-proxy-responses-regex-20261002-local.exe` 早于缓存专项修正，不能作为本次审查通过的部署产物。当前仅完成源码编译检查，未替换、重启或运行服务，未推送 GitHub。
- 真实上游是否接受修复后的整个请求需要部署后的用户复现验证。本轮未新增上游探针授权；本地测试不消耗真实模型用量。
- 部署、正式发布、授权服务登记和 GitHub 推送须遵守用户提供的 AGENTS.md 第 8 条，代码修复授权不自动扩大为这些操作。

## 缓存专项复审

用户要求进一步核对缓存后，查阅了以下官方文档：

- [Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)：缓存复用依赖完整渲染前缀一致，工具名称、描述、schema 和顺序均影响前缀；历史应追加，动态内容应置于稳定前缀之后。稳定 `prompt_cache_key` 本身不能保证命中。文档区分早期模型的路由作用和 GPT-5.6 及后续模型的自动路由、按 key 隔离计量；兼容上游是否完全实现这些规则尚未验证。
- [Prompt cache diagnostics](https://developers.openai.com/api/docs/guides/prompt-caching/diagnostics)：`tools_changed`、`input_changed` 和 `prompt_cache_key_changed` 等可解释缓存复用差异；实际复用应读取 `usage.input_tokens_details.cached_tokens`，本地哈希相同不等于实际命中。
- [Conversation state](https://developers.openai.com/api/docs/guides/conversation-state)：`previous_response_id` 传递前次上下文；`store` 控制响应保存。续接状态命中与 Prompt Cache 命中需要分别核验。

专项审查发现，初版 `setResponsesStore` 使用 `json.RawMessage` 时只对顶层对象排序，改变了旧实现对 `input` 项及所有工具 schema 对象递归排序的序列化行为；这也影响没有匹配正则的请求。旧续接状态通过字节比较输入前缀，字段顺序变化可能使正常追加回退为全量发送。为保留已有缓存前缀布局，采用 `Decoder.UseNumber()` 保留数字精度，并维持旧实现对对象键递归排序、数组顺序不变的流程。

正则替换仍会改变对应工具定义和工具哈希；首次切换可能需要重新建立这部分缓存关联，不能承诺复用原 schema 的全部缓存。验收目标是修复后重复请求保持确定性，并保持其他稳定消息及工具定义的布局。真实命中率仍须在获授权的部署后通过上游 usage 对照验证。

精确数字与旧本地工具缓存的组合还发现了取值冲突：旧 `ToolsHash` 会将 `9007199254740992` 与 `9007199254740993` 合并，命中时返回先到请求的 schema。仅验证出站与审计哈希一致无法发现这个错误，因为两者可能都读取了同一错误条目。因此 Responses 需独立的精确缓存分区，不能直接将精确摘要放入旧 `openai` 分区；旧条目也可能占用这个摘要。真实已知 `runLabel` 的 1/100 不触发大数问题，该处理是为了让本轮承诺的其余 schema 数值保真在热缓存中仍成立。

`1`、`1.0` 与 `1e0` 按当前数字词法保真契约分别保留，不新增十进制归一算法。因此旧布局保持指对象键排序、数组顺序及未发生数值表示修正的内容；精确数值或词法表示不同的 schema 使用不同身份。专用分区首次使用有一次本地转换 miss；普通小整数且不匹配正则的请求应保持原有上游工具哈希与 `prompt_cache_key`，正则替换或原先被舍入的数值则需要对应的新缓存关联。

### 专项回归证据

| 检查对象 | 本地验证结果 |
| --- | --- |
| 不匹配正则、使用常见小整数的请求 | 普通及续接请求与旧序列化布局一致，上游工具哈希和 `prompt_cache_key` 保持原值。 |
| 源对象字段顺序变化、工具源数组重排 | 未触发 128 项截断且名称互不相同的 fixture，重复冷转换后的出站工具定义、哈希与 key 保持一致；既有按名称排序规则未变。 |
| 旧版本保存的 input 与新版正常历史追加 | 实际 `ApplyResponsesContinuation` 仍可匹配旧前缀并启用 `previous_response_id`。 |
| 旧 `ToolsHash` 碰撞的大整数 A/B | 使用真实 `ParseKiroRequest` 复现旧摘要碰撞；专用分区分别返回各自的精确 schema，A→B、B→A 均不串用。 |
| 工具约束从大整数 A 变为 B 的实际续接 | 在旧摘要相同的情况下，第二轮使用 B 的精确工具定义及新的 key；`ApplyResponsesContinuation` 仍能续接，增量 body 保留当前 schema 和 key。 |
| 相同 raw 工具重复读取 | 局部 `ToolCache` 首次 miss、再次 hit，条目数不增加，schema 与 provider hash 不漂移。 |
| 共享缓存并发读取 | 16 路请求的工具及 key 保持稳定，相关 race 检测通过。 |
| 配置 Responses 但实际 URL 为 `/custom` | 保持旧数字处理及请求布局，本次精确保真没有扩展到该路径。 |

这些结果验证的是本地转换缓存、出站前缀与续接契约。Mock 上游没有真实 KV 缓存，不能据此计算实际命中率；真实兼容上游的 `cached_tokens` 仍是部署后的验收项。

### 最终独立复审

新的只读复审者在前级阻断项修正后独立核对当前 diff、调用链、测试断言及上述官方文档，结论为 PASS，未发现新的 blocker。复审发现并发测试的原始 `RawMessage` 快照只复制切片头，不能可靠检测底层字节被原地改动；该非阻断测试问题也已修正为逐项深复制，定向 race 测试通过。

最后两项补强的验证分别为 `go test -race ./proxy -run '^TestResponsesCacheContractPreservesPreciseToolsAcrossAppliedContinuation$' -count=1` 和 `go test -race ./proxy -run '^TestResponsesCacheContractConcurrentSharedToolsRemainImmutable$' -count=1`。它们只调整测试断言，生产代码与前述普通测试、集成测试、race 和编译检查的版本一致。未进行真实上游请求，未部署或推送。
