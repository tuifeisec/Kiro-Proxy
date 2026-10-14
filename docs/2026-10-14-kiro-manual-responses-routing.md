# Kiro 原生 Responses 手动模型转发

## 问题与当前契约

Kiro 模型列表使用本地模型配置或模型组的 ID。此前 runtime `/v1/responses` 只接管带阶段头的 Auto 请求，手动选择的本地 ID 被透传到 Kiro 官方，因而出现 `InvalidModelError`。本契约取代旧阶段设计中“手动模型始终 passthrough”的边界。

在 forward 模式下，该路径的 POST 若选择已启用且可解析的本地模型或模型组，必须进入本地转发，不要求 `X-Kiro-Proxy-Workflow`。匹配顺序沿用普通 Kiro 路由的配置 ID、组名称、模型显示名及上游 ModelID；未知标识不使用首模型兜底，仍按原行为透传。Auto 继续只在带阶段头时进入严格阶段路由，无头 Auto 保持透传。capture 和非 POST 请求保持透传。

已配置但当前不可用的本地模型或模型组（包括停用配置、空组和成员均停用的组）也必须留在本地处理，无可用路由时返回 503，不能当作未知原厂标识透传。

手动请求不校验或应用 Auto 阶段头，不绑定 Auto 推理等级；有可用会话、Agent 和 continuation 元数据时，清除相同执行域的旧 Auto 锁。手动解析允许缺少阶段桥接专用 metadata。所有已读取的 Responses POST 在出站前移除本地 `kiro:proxy_*` 字段，私有阶段头始终在入口移除。

capture 不使用生成协议校验来拒绝请求：正文可解析时只清除本地字段，正文无法解析时保留原字节透传；不解析模型或改变 Auto 锁。既有请求读取失败及大小限制继续有效。

## 转发边界

使用配置的上游 URL、凭证和 ModelID，并沿用用量统计、取消和并发释放。`provider=openai` 配置 `/responses` 时保留原始响应/SSE；显式配置 `/chat/completions` 时，在发送前将当前支持的 Responses 文本和函数工具请求转换为 Chat Completions，并将响应转换回 Responses。协议由配置决定，不在失败后改协议重放。模型组沿用现有候选调度，仅选兼容成员。单模型不兼容、组无兼容成员或上游失败必须明确返回错误，不回退 Kiro 官方。

Chat 适配的本轮验收覆盖文本与函数工具链路。既有 `input_file` 附件转换仍有缺口，其 `filename`、`file_data` 不会被当前转换器保留；本轮未修复或验收该附件能力，不能据此宣称所有 Responses 内容均兼容。

本地选中模型的请求在路由消费完毕后移除 metadata 中的 `kiro:*` 字段；这些字段属于 Kiro 的会话及本地桥接上下文。清理后为空的 metadata 连同字段移除，其他调用方自定义 metadata 保留原值和数值精度。capture 及原厂透传仍只清理既有代理私有字段，不改变 Kiro 原厂会话字段。

本地上游出口同时移除 Kiro 专有的顶层 `origin`、`profileArn` 和 `reasoning.type`；仅含 type 的 reasoning 清理后整体移除，标准 `reasoning.effort`、`summary` 及其他扩展保留。已安装 Kiro 的 `sJu`/`aJu` 构造器把这三项直接放入 HTTP 正文，不能将其当成标准 Responses 字段转发。capture 和原厂透传保留这些原厂上下文，不以通用字段白名单删减其余业务字段。

流失败继续原样返回客户端，不能把 HTTP 200 当成生成成功。请求用量日志保留失败事件及已知的错误 code/type、参数名或 incomplete 原因，默认不保存可能回显提示词或凭证的上游错误 message。

Chat 响应转换复用现有适配器，并保证各工具的参数事件不交错：首个工具的参数增量实时输出，其他工具先收集参数，结束时逐个连续输出 added、参数和 done。文本仍实时输出，不禁止并行工具调用。此顺序满足当前 Kiro 只将参数增量关联到最近一次 added 的消费行为；请求取消仍关闭上游并释放并发占用。

工具续轮中连续的多个 `function_call` 合并为同一条 Chat assistant 消息的 `tool_calls`，随后保留各工具结果及其 call ID，结果顺序可以与调用顺序不同。流和 JSON 均把 Chat 的 `length`、`content_filter` 分别转换为 Responses `incomplete` 的 `max_output_tokens`、`content_filter`，保留部分内容及实际 usage，工具项也标记为 incomplete；本地 raw 用量及模型组不能记为成功或建立成功亲和性。只有 `[DONE]` 或非法数据加 `[DONE]` 不能生成空的成功响应。

请求仍须携带完整数组 input；非空 `previous_response_id` 和顶层 `conversation` 引用保持不支持，不能删除引用后继续发送。不增加其他厂商的协议转换或服务端会话迁移。

## 验收

使用临时数据库和本地 HTTP Mock 验证：手动模型与模型组无头转发、ID 替换、配置鉴权、私有字段移除、SSE 保留与用量统计；手动请求清锁且不应用阶段策略；未知模型、无头 Auto、capture 与非 POST 边界；不支持的目标明确失败且官方 Mock 零命中。继续运行已有 raw Responses 和 Auto 路由回归，不调用真实收费模型。

构建候选不等于部署；本轮不替换运行程序、不重启 Kiro、不修改真实数据库。

## 本轮验证记录

以下检查退出码均为 0，全部使用临时数据库和 HTTP Mock：

```powershell
F:\Go\bin\go.exe test ./converter ./router -skip '^TestAutoGroupDefaultsMetadataAndFallbackProviderValidation$' -count=1
F:\Go\bin\go.exe test ./proxy -run 'TestManualRawResponses|TestStage|TestDuty|TestFixedAuto|TestForwardRawResponses|TestRawResponses|TestAutoGroupRaw|TestAutoFailover|TestClassifier' -skip '^TestDutyAutoGroupRecordsActualMemberAfterCapacityDispatch$' -count=1
```

更宽范围检查实际发现两条既有失败，未修改相关能力或测试来掩盖结果：

- `TestAutoGroupDefaultsMetadataAndFallbackProviderValidation` 期待 Anthropic 拒绝 `none`，但当前转换器将其映射到 `disabled`，校验返回 nil；测试、校验器和转换器相对本轮开始均未改变。
- `TestDutyAutoGroupRecordsActualMemberAfterCapacityDispatch` 创建测试组时设置 `FirstByteTimeoutMS=1000`，被现有 store 的 10000–300000 限制拒绝；测试和 store 均未修改。失败发生在进入路由前。

本轮修正了旧测试中“已配置手动模型应透传”的预期：透传清锁场景改用未配置的原厂模型标识；已配置模型转发由新增 Mock 覆盖。独立初审发现空组误透传，已将配置存在性与路由可用性分开，并验证空组、停用模型、成员均停用的组返回 503，无兼容成员的组返回 502，均不命中官方 Mock。

后续独立复审发现前置校验会拒绝原本可抓包透传的非法 JSON 和非字符串 model，已把 capture 与 forward 的协议校验边界分开，保留抓包只观察的语义，并补充对应 Mock 回归。

22:02 的实际日志能确认本地模型 ID 和数组 input 前缀，但正文截断，无法核实全部 metadata 及服务端引用。

后续只读取证在 `~/.kiro/logs/20261014T140245348/kiro.log` 找到 22:41 的真实错误 `InternalServerError: Unsupported parameter: metadata`，与本地记录的三次 HTTP 200 流失败对应。此前 raw sender 仍把 `kiro:conversation_id` 和 `kiro:agent_mode` 发送给兼容上游，普通模型测试没有这些字段。另一条 DeepSeek 请求明确配置 `/chat/completions`，实际却请求 `/responses` 并返回 404。两种问题分别通过本地元数据清理和配置驱动的协议适配修正；Mock 验证与真实 Kiro 复测需分别报告。

已安装 Kiro 的 `dist/extension.js` 中 `sJu(t)` 构造器及其原始备份均表明：原生 metadata 只可能包含 `kiro:agent_mode`、`kiro:conversation_id`、`kiro:root_conversation_id`、`kiro:agent_continuation_id`；Auto 桥接另附加两个 `kiro:proxy_*` 字段。因此清理该命名空间可完整移除现场请求里的 metadata，同时保留调用方自定义字段。

## 兼容修复验证与调试候选

本次修复后的以下检查退出码为 0，仍仅使用临时数据库和本地 HTTP Mock：

```powershell
F:\Go\bin\go.exe test ./converter ./router -skip '^TestAutoGroupDefaultsMetadataAndFallbackProviderValidation$' -count=1
F:\Go\bin\go.exe test ./proxy -run 'TestManualRawResponses|TestStage|TestDuty|TestFixedAuto|TestForwardRawResponses|TestRawResponses|TestPrepareRawResponsesChatBody|TestAdaptRawChatResponse|TestAutoGroupRaw|TestAutoFailover|TestClassifier|TestOpenAICompatDerivesBothNativeEndpoints|TestResponsesToChatFallback|TestConvertChatCompletionToResponses|TestCopyChatStreamAsResponses|TestAggregateResponsesSSE|TestFallbackMessages' -skip '^TestDutyAutoGroupRecordsActualMemberAfterCapacityDispatch$' -count=1
```

新增覆盖包括拒绝 metadata 的原生上游、保留自定义 metadata 及大数、Chat-only 单模型和模型组的真实转发链路、文本和工具续轮转换、交错工具参数回放、惰性读取与取消、HTTP 200 错误 JSON 和失败 SSE、用量及并发释放、流失败不重放和错误日志脱敏。原 Auto 阶段测试曾硬编码所有模型都应请求 `/responses`，本轮将其断言改为按测试中实际配置的端点验证；其混合 Chat/Responses Mock 无需改动。

独立复审提出的多工具续轮和截断状态问题已补修。测试进一步覆盖双工具反序结果、多个完整工具轮、单工具回归；`length` 与 `content_filter` 在 JSON/SSE、单模型/模型组的全部八种组合；以及裸 `[DONE]`、非法 JSON、未知对象、只有 role 的 chunk 后接 `[DONE]` 均不能记为成功。补修后的上述 proxy 统一回归退出码为 0，包耗时 2.998 秒。

后续复审核实 `origin`、`profileArn`、`reasoning.type` 同样直接进入 Kiro HTTP 正文，旧 Mock 没有携带这些字段，不能验证完整出口边界。已在本地上游清理处补齐这三项，并增强实际 handler 的请求 fixture 与严格 Mock，覆盖模型/组转发及 capture 保留原厂上下文。新增直接准备测试同时核对无 metadata、仅有 reasoning.type、自定义 metadata、未知扩展及大数保真。该补修后的上述 proxy 统一回归退出码为 0，包耗时 3.028 秒。

最终调试候选为根目录 `kiro-proxy-dev-responses-compat-v3.exe`：38,024,034 字节，SHA256 `EA994C5B93BC2B3A5F54E146E1F12FEBC1A12F08EA5255F1C84300F802F35A0E`。构建使用 `desktop,dev`、Windows amd64、CGO=1、GUI subsystem=2，未使用 `-s -w`，保留调试符号。此前无版本后缀和 `v2` 候选均不含全部复审补修，不作为最终交付。候选未启动或替换运行程序，真实 Kiro 及第三方上游请求仍需复测；上述两个既有测试问题仍显式排除，不能表述为全量测试通过。

补修后由新的只读独立复审者核对代码、测试覆盖与 v3 产物，本轮限定目标无交付 blocker；复审独立检查了候选身份及 `.debug_info` 等调试段，未重复运行统一回归。主代理另以 `go tool nm` 确认 `main.main`、`prepareRawResponsesBody` 和 `stripRawUpstreamKiroContext` 符号实际保留。
