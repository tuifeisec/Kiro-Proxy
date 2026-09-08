# Kiro 动态模型路由设计讨论稿

> 状态：讨论中，未实施。
> 目标：Kiro 侧保持一个稳定的逻辑模型选择；代理只在一个主会话或独立 agent 分支首次进入时，根据结构化上下文选择实际模型，后续保持稳定。
> 依据：已对照 `xuexi/plugins/agnets-workflow` 的任务级多模型编排、状态持久化和验收闭环设计；本文只迁移适合代理运行时的原则，不直接复用其工作流控制器。

## 1. 背景与目标

当前 `kiro-proxy` 已具备模型组、候选模型、熔断、并发控制、超时、重试与降级能力，但一段 Kiro 对话通常从开始到结束使用同一个主模型或模型组。

希望达到的效果是：

```text
Kiro 主会话的逻辑模型：保持不变
主会话的实际模型：首次选定后保持不变，保留缓存
独立子任务/agent 分支：按任务角色选择另一实际模型
子任务结果：作为工具结果回到主会话的原实际模型
```

例如：

- 简短问答使用低延迟、低成本模型；
- 读取代码、调用命令或编辑文件等可独立子任务，可由 coding/agent 模型承担；
- 图片请求使用视觉模型；
- 长上下文或高推理强度任务使用长上下文或强推理模型；
- 同一实际模型配置内的重试仍保留；是否允许主会话故障时换成员冷启动，单独作为产品决策。

## 2. 非目标

本轮不计划：

- 修改 Kiro 客户端或要求用户在 Kiro 中频繁切换模型；
- 用完整的工作流/DAG 替换实时请求路由；
- 改变现有模型组、上游配置、授权或统计行为；
- 在一次流式输出中途切换实际上游模型。

`xuexi` 项目中“路由理由、显式状态、checkpoint、审计和关闭条件”的思想可借鉴到策略控制面；实时对话的高频路径应采用轻量的规则与会话状态，不宜逐请求构建完整 DAG。

## 3. 从 xuexi 借鉴什么

`xuexi` 的 `agnets-workflow` 解决的是复杂开发任务的多模型/多角色编排：按任务节点的风险、依赖和验收要求选择执行者，并持久化 `routing_reason`、状态、checkpoint、租约和复核记录。

它不是实时模型代理。因此应拆成两个边界清晰的平面：

```text
数据平面：Kiro 请求 -> 实时策略决策 -> Forwarder -> 上游模型
控制平面：策略配置 -> 发布/停用 -> 观测 -> 审计 -> 回滚
```

| xuexi 的思想 | 放到 proxy 中的对应位置 | 首版处理方式 |
| --- | --- | --- |
| `routing_reason` | `RoutePlan.RouteReason` | 每轮必填并记录。 |
| 主控与不同角色的执行者 | Kiro 主会话与独立 agent 分支 | 主会话固定实际模型；只对新分支按角色选模。 |
| 节点状态 | `ConversationRuntimeState.Phase` | 只记录会话阶段与工具连续性。 |
| checkpoint | 会话策略快照/请求日志 | 记录实际选择和切换原因，不保存完整请求正文。 |
| 租约与 claim | 策略版本与会话亲和 | 防止同一工具链在连续回合中被随意切换。 |
| DAG 与依赖 | 策略发布流程 | 仅用于配置审核、发布、回滚和界面编排。 |
| 总审与关闭门禁 | 策略启用门禁 | 以指标阈值、人工确认和一键停用代替每请求审核。 |

不迁移的部分：每个请求创建任务节点、请求级 SQLite 事务、请求级心跳/租约、以及由工作流引擎执行候选重试。这些做法会增加热路径延迟和复杂度，并会与现有 Forwarder 的职责重叠。

`xuexi` 的关键模式是“主控不丢上下文，独立工作交给角色化执行者，结果回交主控”，不是“主控在每个步骤换脑”。这正好符合 Kiro 的缓存约束。

## 4. 当前链路

当前主要调用链：

```text
POST runtime.<region>.kiro.dev/（具备 conversationState.currentMessage.userInputMessage）
  -> proxy/interceptor.go: ParseKiroRequest
  -> converter.ParsedConversation
  -> router.Resolve / resolveClassifierRoute
  -> proxy.Forwarder.Forward
  -> 模型组候选选择、健康检查、并发/超时/熔断、重试
  -> 上游模型
```

根路径上的其他请求不会进入该链路。特别是：

```text
POST runtime.<region>.kiro.dev/agents/activity
  -> kiro-passthrough
  -> 记录请求体并直通 Kiro
```

`/agents/activity` 当前不调用 `ParseKiroRequest`，不进入 `Router` 或 `Forwarder`，也不会影响正在执行的主请求。

关键文件：

| 职责 | 文件 |
| --- | --- |
| Kiro 请求解析与拦截 | `proxy/interceptor.go` |
| 上游转发与候选执行 | `proxy/forwarder.go`、`proxy/forwarder_request.go` |
| 基础路由 | `router/router.go` |
| Kiro 请求结构 | `converter/types.go`、`converter/kiro_parse.go` |
| 模型与模型组存储 | `store/model.go`、`store/model_group.go` |
| 使用量记录 | `proxy/request_usage.go` |

当前 `conversationModels` 只保存 `conversationId -> 最近主模型`，主要服务于 classifier 跟随主模型，并不表达会话当前所处阶段、工具状态或策略决策。

已有的 `Forwarder` 还会在一个已选模型组内部完成候选排序、会话亲和、图片能力过滤、并发、首字节超时、熔断和失败转移。策略层负责选择目标，并把严格锁定约束传给 Forwarder；它不复制组内调度，但 Forwarder 必须在严格缓存模式下遵守“不跨实际模型配置”的候选边界。

### 代码可行性核对

下面的结论来自当前源码与已有回归测试，不是对未来实现的假设：

| 能力 | 当前证据 | 对本方案的结论 |
| --- | --- | --- |
| 主会话与 agent 分支识别 | `ParseKiroRequest` 已解析 `agentMode`、`agentContinuationId`；`vibe` 使用裸 `conversationId`，非 `vibe` 使用长度前缀分支键。 | 可以按现有 `ContinuationStateKey` 区分主会话和独立分支。 |
| 分支工具续接 | `test/cache_regression_test.go` 覆盖 `context-gatherer` 首轮、工具结果续轮和严格前缀 continuation。 | 分支内部维持同一实际模型时，转换与续接基础已具备。 |
| 工具结果回主会话 | 转换器将历史 `toolUses` 变为 `function_call`、将 `toolResults` 变为 `function_call_output`，已有 append 与 cache read 日志证据。 | 工具结果回到同一实际主模型，与稳定前缀缓存相容。 |
| 按角色选模型 | Router 当前仅依据 `modelId`/classifier 解析路由，未按 `agentMode`、工具或能力标签选择目标组。 | 可实现，但必须新增 `PolicyResolver`；这不是现有功能。 |
| 实际模型亲和 | `modelAffinityStore` 的键是 `groupID + conversationId`；成功后写入，失败时清除，随后 Forwarder 可尝试同组或后备组其它成员。 | 这只是在组内优先复用，不是主会话实际模型锁，不能单独保证缓存不被冷启动。 |
| activity 的子任务信号 | `/agents/activity` 只透传；抓包有 `subAgentComplete`，但没有与全部主请求建立完备协议关联。 | 可作为异步观察补强；首版不能依赖它决定正在执行的请求，也不应把“结果必然回主会话”写成已证实事实。 |

因此，**主会话固定实际模型、分支首次选模、工具结果回原主模型**在协议解析和转换层面可以落地；要达到“缓存不因策略切换而丢失”，还必须新增独立的实际模型锁，并限制现有 Forwarder 的跨成员故障转移范围。

## 5. 可用请求信号

`ParsedConversation` 已可提供的主要信号：

```text
modelId
conversationId
agentContinuationId
agentMode
agentTaskType
chatTriggerType
origin
content
history
tools
toolResults
images
reasoningEffort
maxTokens
temperature
topP
```

Kiro 的 `/agents/activity` 日志还包含：

```text
sessionId
executionId
turnStart / turnEnd
toolUse / toolResult
sessionMetadata
usageSummary
sessionEvent
operationType
contextUsage
status
```

其中 `sessionId`、`executionId`、`toolUse`、`toolResult` 与 `contextUsage` 可补强会话阶段识别。现阶段 activity 请求主要透传，尚未参与实时路由决策。

### 已核对的流量结论

本轮只读核查了 `kiro-proxy.log`、`kiro_traffic.log`、`kiro_traffic.log.1` 与当前解析代码。以下结论应作为策略设计前提：

| 结论 | 证据与影响 |
| --- | --- |
| Kiro 传入的是逻辑模型配置 UUID。 | 日志中的主请求 `modelId` 为 UUID，Router 也以分组 UUID、模型 UUID 为优先匹配。策略应保留该 UUID，不改写 Kiro 协议字段。 |
| 正式会话请求在样本中连续发生，且同会话会携带大量历史和工具结果。 | 已有请求体达到约 416 KiB，缓存审计显示连续回合的前缀追加。因此跨实际模型的切换不是零成本。 |
| 工具活动是高频的异步批量事件。 | `kiro_traffic.log.1` 中 163 个 activity HTTP 请求承载 485 个事件，包含 167 个 `toolUse` 与 167 个 `toolResult`；同一 HTTP 请求可含 1 至 14 个事件。必须按 payload item 处理，而不是按 HTTP 请求计数。 |
| activity 的关联键是 `(sessionId, executionId, toolCallId)`。 | 同一 execution 下 `toolUse.toolCallId` 与 `toolResult.toolCallId` 可关联；这套键与主请求字段之间只有样本层面的 `sessionId == conversationId` 观察，尚未形成协议约束。 |
| activity 到达存在批量和先后竞态。 | 因此它只能为后续请求维护 observe-only 运行态，不能决定同一轮主请求的路由。 |
| 现有样本无法证明图片路由的命中率。 | 主请求 detail 默认最多保留 64 KiB，样本中未见可确认的非空 `images[].source.bytes`；这不代表真实流量没有图片。vision 规则进入 enforce 前需要专门采样验证。 |
| 当前日志样本没有命中 `classifier=true`。 | classifier 解析与路由能力已存在，但“是否频繁出现、与主请求的相对顺序”仍需通过新的结构化指标确认。 |

后续观测记录只保存路由所需的脱敏元数据和长度/计数，不把完整 prompt、工具参数或工具结果写入策略审计表。

### 当前模型配置就绪度

以下是本轮对 `kiro-proxy.db` 的只读快照，不含任何凭据字段：

| 项目 | 当前状态 | 对首版策略的影响 |
| --- | --- | --- |
| 启用模型 | 6 个；包含 OpenAI Responses 与 Anthropic Messages 两类上游。 | 跨模型组切换必须重新执行每个候选的协议转换。 |
| 模型组 | `Claude-oPUS` 仅 1 个 Anthropic 成员；`guomo-plus` 有 3 个成员，包含 2 个明确不支持图片的成员和 1 个支持图片的成员。 | 已有分组用于故障转移，尚未表达 `vision`、`coding/agent`、`快速`、`长上下文` 等能力目标。 |
| 图片能力 | 只有一个成员配置为 `supported`，其余为 `auto` 或 `unsupported`。 | 当前 Forwarder 已能在组内过滤图片候选；全局策略还没有一个明确的“vision 目标组”。 |
| classifier | `classifier_model_id=__disabled__`。 | 当前没有可直接启用的 classifier 动态选模分支。 |
| Responses continuation | 所有当前模型的 `enable_continuation=0`。 | 当前运行时未使用 Responses continuation；设计仍应保留其未来约束，但首版没有既存 continuation 数据需要迁移。 |
| 能力标签 | 数据表没有 coding/tool、速度、成本、长上下文适配、强推理等策略标签。 | 仅凭 provider、模型名称或现有排序推断能力会产生不可解释的路由，首版不应这样做。 |

因此，当前配置下不存在可以稳妥直接切入 `enforce` 的跨组能力规则。第一项工作应是建立“能力 -> 目标模型组”的显式配置；在它完成前，动态策略可以计算和记录建议，但没有可靠的目标组可执行。

## 6. 目标架构

建议在现有 Router 与 Forwarder 之间增加策略层：

```text
ParsedConversation
  -> RoutingContext
  -> Router（解析逻辑模型与基础模型组）
  -> PolicyResolver（选择策略目标）
  -> RoutePlan（实际模型组、原因、亲和约束）
  -> Forwarder（沿用候选、熔断、并发、超时、重试）
```

职责边界：

| 组件 | 责任 |
| --- | --- |
| `Router` | 保留现有 `modelId` 到逻辑模型/模型组的映射能力。 |
| `RoutingContext` | 统一请求、会话、工具、图片、上下文长度和活动事件信号。 |
| `PolicyResolver` | 对未锁定执行域决定首次目标；对已锁定执行域只校验硬能力并输出锁定计划，不直接调用上游。 |
| `RoutePlan` | 记录本轮计划：目标组、原因、锁定模式、实际模型约束、策略版本。 |
| `Forwarder` | 在目标组内执行现有候选选择、熔断、并发和重试；严格模式下候选范围受实际模型约束。 |
| `ConversationRuntimeState` | 保存必要会话状态，避免把阶段判断分散在拦截器中。 |

## 7. 逻辑模型与实际模型分离

Kiro 的 `modelId` 应视为逻辑模型标识，而不强制等于真实上游模型：

```text
logicalModelId = Kiro 请求中的 modelId
actualModelGroup = PolicyResolver 本轮选择的模型组
actualModel = Forwarder 在组内最终执行的候选模型
```

这保留了 Kiro 对会话与协议的稳定预期，同时使代理可根据任务阶段切换能力和成本。

建议在日志与用量记录中同时保存三者，避免“用户选的模型”和“实际调用的模型”混淆。

### 主会话与子任务分支

动态选模应按执行域发生：

```text
主会话（vibe / 同一 ContinuationStateKey）
  -> 主实际模型固定
  -> Kiro 工具调用
  -> 工具结果回传
  -> 仍回到同一主实际模型

独立 agent 分支（新的 ContinuationStateKey）
  -> 根据角色、工具和能力选择 worker 实际模型
  -> 分支结果通过 Kiro 的工具/消息链回交主会话
```

当前解析已为非 `vibe` 的 agent mode 构造独立 continuation key，因此这是一条可承载的协议边界。`/agents/activity` 中虽出现子任务事件，但仅作为旁路观察；首版要以主生成请求里的 `agentMode`、`agentContinuationId` 和 continuation key 识别分支，避免依赖 activity 的到达顺序。

## 8. RoutingContext 与运行态

建议的 `RoutingContext` 字段分层：

```text
请求层：logicalModelId、content、history、tools、toolResults、images、reasoningEffort
会话层：conversationId、agentContinuationId、sessionId、executionId
运行态：当前阶段、上次实际模型组、工具调用状态、最近失败原因、contextUsage
系统层：时间、策略版本、模型组能力、健康与拥塞摘要
```

会话状态必须复用现有 continuation 语义，而不是自行拼接 `conversationId + agentContinuationId`：

```text
agentMode 为空或 vibe：ContinuationStateKey = conversationId
其他 agentMode：ContinuationStateKey =
  "branch|" + length(conversationId):conversationId +
  length(agentMode):agentMode +
  length(agentContinuationId):agentContinuationId
```

这与现有 Responses continuation 的会话隔离保持一致。Responses 状态还会额外按实际模型配置 ID 与 reasoning effort 分桶；切换实际模型或推理档位会使 continuation 失效并回到完整输入。状态只保留实时决策必需的信息，例如：

```text
phase: chat | planning | tool_call | tool_result | synthesis | fallback
lastActualModelGroup
lastActualModel
toolAffinityGroup
contextUsage
recentFailure
updatedAt
```

运行态应设置过期时间，并在会话结束事件后尽早清理，防止状态无限增长和旧会话干扰新会话。

### 三个键与一个锁

这四项不能合并，也不能互相替代：

| 名称 | 当前值/建议值 | 职责 | 策略实现要求 |
| --- | --- | --- | --- |
| `PromptAffinityKey` | 当前为 `conversationId` | 传给 Provider 的 `prompt_cache_key`、`user` 和部分上游会话亲和。 | 保持当前语义；不要为分支动态改键，否则会改变已有稳定前缀的亲和域。 |
| `ContinuationStateKey` | 主 `vibe` 为 `conversationId`；非 `vibe` 为带长度前缀的三字段键。 | Responses `previous_response_id` 的 SQLite 隔离。 | 复用现有生成逻辑；不能用 `agentContinuationId` 替换主会话键。 |
| 组内 `modelAffinityStore` | 当前为 `groupID + conversationId`。 | 在一个模型组内优先选回上次成功成员。 | 可继续服务普通调度；它不表示跨组策略锁，也不以 agent 分支键隔离。 |
| `ConversationRouteLock` | 建议键为 `ContinuationStateKey`，值含实际模型配置 ID。 | 确保一个执行域不会因策略而切换实际模型。 | 新增并在候选选择前生效；分支锁与主会话锁独立。 |

特别注意：`PromptAffinityKey` 可以在主会话与 `context-gatherer` 分支之间相同，而 `ContinuationStateKey` 必须隔离。这是现有抓包和回归测试已经证明的边界，不应为了“统一缓存键”而破坏它。

## 9. 首版策略规则

首版采用“确定性优先级规则 + 能力过滤”，便于解释、观测和回滚。下表仅用于**未取得有效 `ConversationRouteLock` 的执行域首次选模**；已锁定主会话或分支直接走锁定实际模型，规则命中只记入审计。

| 优先级 | 条件 | 策略目标 |
| --- | --- | --- |
| 1 | classifier 请求 | classifier 低延迟模型组 |
| 2 | 当前消息存在非空图片 bytes，且 vision 采样已验证 | vision 模型组 |
| 3 | 新分支首个请求已携带 `toolResults` | 工具兼容模型组；已有锁时只记录，不重选 |
| 4 | 当前工具 schema 命中明确的文件、命令、读写工具 | coding/agent 模型组 |
| 5 | `reasoningEffort` 为 `high`/`xhigh`，或复杂规划特征明显 | 强推理模型组 |
| 6 | 上下文长度或 `contextUsage` 达到阈值 | 长上下文模型组 |
| 7 | 短文本、无工具、无图片、低推理 | 快速经济模型组 |
| 8 | 其他情况 | 逻辑模型对应的默认模型组 |
| 9 | 目标组不可用、超时、429 或上游异常 | 保持现有组内失败处理；必要时进入明确的 fallback 阶段 |

规则必须输出可观测的理由代码，例如：

```text
classifier
vision_required
tool_continuation
coding_tool
high_reasoning
long_context
fast_chat
default
upstream_failure
```

首版不要直接依赖内容语义分类来决定高风险路由。可先使用主请求内的明确字段、当前工具 schema、当前 `toolResults`、实际图片 bytes、推理强度和结构化长度/计数；后续再评估是否引入 classifier 或 activity 补强状态。

## 10. 工具调用与会话连续性

工具链是动态切换中最重要的约束：

1. 同一次上游流式响应期间，实际模型必须固定。
2. 主模型提出工具调用后，工具结果回传的下一轮必须复用同一实际模型配置，而不只是同一模型组。
3. 只有新建的独立 agent 分支才可按角色切换到另一个模型；分支模型必须支持该分支的工具协议与序列化格式。
4. 需保留原始 `modelId`、`conversationId`、`agentContinuationId` 与工具调用标识，禁止重写导致 Kiro 无法关联响应。
5. 工具循环结束后的总结回合可切换至总结能力更强的模型组，但应作为后续策略，不进入首版。
6. 跨实际模型组或模型配置切换会丢失该模型的 Prompt Cache、模型亲和与 Responses continuation；首版应把这类切换视为明确代价，并在 RoutePlan 中记录 `continuationImpact=cold_start`。
7. 若当前会话存在可用的 Responses continuation 或未完成工具链，策略应优先锁定已成功的实际模型组；只有能力硬约束或现有组不可用时才打破锁定。

### 缓存优先的会话锁定

“每轮都按条件换模型”与“保留上游缓存”天然存在取舍。当前日志已经出现 `gpt-5.6-sol` 的大规模 `cache_read`，所以首版默认策略必须是缓存优先，而不是每轮重新挑选成本最低或能力最强的模型。

**缓存完整性是本方案的硬约束：同一 ContinuationStateKey 内，一旦成功选定实际模型配置，策略路由不得把后续正常请求切到另一个实际上游。** 条件变化只影响新会话或新分支的首次选模；对已锁定会话只记录命中结果，不改变实际目标。

现有 OpenAI 缓存亲和键至少包含实际上游 API、实际模型、静态 system 和工具 schema；Responses continuation 还按实际模型配置 ID 与 reasoning effort 隔离。即使两个配置显示为相同模型名，也不应假设跨上游地址或跨凭据可共享提供方缓存。

建议新增独立于组内 affinity 的 `ConversationRouteLock`：

```text
key: 现有 ContinuationStateKey
value: logicalModelId + selectedModelGroup + actualModelConfigId + policyVersion + lockMode
ttl: 跟随会话活动时间，并短于或等于现有组内 affinity 的有效期
```

路由顺序改为：

1. 先检查 `ConversationRouteLock`。
2. 已锁定会话直接复用锁定目标，记录 `routeReason=conversation_lock`；图片、工具、高推理等条件只记录为策略观察结果。
3. 新会话、新 agent 分支或用户显式更换逻辑模型时，才根据硬能力约束选择首个实际目标并写入锁定。
4. classifier 是独立请求链，可使用其专属模型，但不修改主会话锁定。
5. 锁定目标故障、熔断或能力不兼容时，按 `lockMode` 执行：`strict` 只允许同一实际模型配置重试，最终将失败返回客户端；`availability` 允许明确记录的冷启动切换。首版推荐 `strict`。

现有模型组的候选故障转移与该硬约束存在直接取舍：组内切到另一个成员意味着新成员冷启动。严格缓存模式落地时，Forwarder 需要识别已锁定会话，并禁止跨实际模型配置的候选切换；同一模型配置的重试仍可保留。

因此，动态路由首版的含义应是“每个会话或分支在首次可判断时选定实际模型，后续保持稳定”，而不是“同一对话的每一轮自由切换模型”。工具循环、长上下文、推理强度和图片等变化默认只记录观察结果，不触发主会话的实际模型切换。新分支也只有在它的首个生成请求到达时选模；activity 中的 `subAgentComplete` 只用于随后核对该分支的结束与结果路径。

### 工具结果回主模型的缓存语义

工具结果本身不会要求主模型换路由。当前 Kiro -> Responses 转换会将历史 `toolUses` 写为 `function_call`，将对应 `toolResults` 写为 `function_call_output`；后续回合是在原有 history 后追加这些项目。

只要以下条件保持不变，上游可继续命中已有稳定前缀：

```text
主会话 ContinuationStateKey
实际模型与上游 API
静态 system 前缀
工具 schema 的规范化结果
历史消息与工具调用顺序
```

现有日志已经出现工具结果数量从 58 增至 59、输入关系为 append，且后续获得约 100,864 token 的 cache read。这说明“工具结果回同一主模型”的路径与缓存复用相容。缓存仍受提供方 TTL、工具 schema 变化、system 前缀漂移和上游实际模型变化影响，所以策略审计应持续记录 cache read/write 与前缀漂移原因。

## 11. 可观测性与审计字段

每一轮至少记录：

```text
requestId
conversationId
agentContinuationId
logicalModelId
defaultModelGroup
selectedModelGroup
actualModel
policyVersion
routeReason
phase
toolAffinityApplied
contextUsage
cacheImpact
previousSelectedModelGroup
previousActualModel
candidateAttempts
fallbackReason
latency
resultStatus
```

建议增加两类视图：

- 会话时间线：每轮请求、策略决策、实际模型、工具事件、失败与切换原因；
- 策略效果：按路由原因统计时延、成功率、429/超时率、成本、人工切回默认模型的比例。

任何策略异常都应能通过 `requestId` 追溯到原始逻辑模型、所选模型组、候选尝试与失败原因。

## 12. 启用模式：影子决策不是硬性前提

影子决策的目的不是“必须先积累历史数据”，而是在真实流量上验证策略命中和策略副作用，同时保持真实转发结果不变。它可直接使用实时请求产生数据。由于 activity 与主请求存在批量和顺序竞态，activity 状态只允许在 `observe` 模式使用，首版路由决策以主请求字段为准。

建议将每条策略配置显式设置为一种模式：

| 模式 | 行为 | 适合场景 |
| --- | --- | --- |
| `disabled` | 只走默认模型组，不计算策略。 | 尚未配置能力标签或临时停用。 |
| `observe` | 计算并记录建议 `RoutePlan`，真实请求仍走默认模型组。 | 新增复杂规则、工具连续性、语义分类、上下文阈值。 |
| `enforce` | 真实请求按 `RoutePlan` 进入目标模型组。 | 条件清晰、协议兼容性明确、模型组已准备好。 |

在严格缓存模式下，`enforce` 只作用于新会话、新 agent 分支或用户显式换模型后的首次请求；已经取得 `ConversationRouteLock` 的会话继续走锁定模型。

因此，首版可选择以下两条路线：

### 路线 1：先观测，再逐项启用

先用 `observe` 验证全部规则，再将稳定规则切到 `enforce`。

适合：现有模型组能力标签尚未整理、不了解真实 Kiro 工具回合形态、或准备让策略覆盖范围较广。

优点是风险低；代价是需要一个观测周期，且观测期不产生真实的切换收益。

### 路线 2：首版直接启用有限规则

直接将边界明确的规则设为 `enforce`，其余保持 `disabled` 或 `observe`。

首批候选只建议包含：

```text
已配置且可用的 classifier -> classifier 模型组
已完成专门图片采样验证的非空图片请求 -> vision 模型组
已确认协议和工具兼容性的明确 coding/agent 工具 -> coding/agent 模型组
```

前提是这些目标组中的模型协议、工具能力和故障处理已确认一致。涉及 `toolResults`、长上下文、高推理或语义判断的规则仍先采用 `observe`。

优点是立即获得动态切换价值；代价是需要先完成能力标签和目标模型组的准确配置。

### 推荐选择

如果你已明确哪些上游模型可稳定处理 classifier 和 coding 工具，推荐路线 2：先直接启用这两类确定性规则。vision 规则先补一组非空图片主请求样本后再启用；工具连续性和长上下文先做观测。

如果目前模型能力、工具协议兼容性和成本数据尚未整理，推荐路线 1。当前数据库快照属于这一情况：classifier 已禁用，且没有能力目标组，所以首版应先做 `disabled/observe` 与能力配置，不应直接启用跨组切换。无论选择哪条路线，策略都应支持单条规则停用和全局切回默认模型组。

## 13. 分阶段落地

### 阶段 A：策略基础与可选观测

新增 `RoutingContext`、`PolicyResolver`、`RoutePlan`、`ConversationRouteLock` 和 `disabled/observe/enforce` 模式。主请求决策和 activity 观察状态必须分开：前者同步执行，后者只更新带 TTL 的 `(sessionId, executionId)` 缓存。`observe` 模式每轮仅记录：

```text
当前默认路由
策略建议路由
建议理由
能力约束
```

验收：日志可完整说明策略会如何选择；若使用 `observe`，现有 Kiro 请求行为保持不变。

### 阶段 B：有限启用

在能力配置完成后，只启用边界明确且能力已核对的分支：classifier、已验证非空图片或 coding/agent 工具。短文本快速模型仅在成本和体验指标已确认后加入。工具结果连续性、长上下文与推理切换仍可保持 `observe`。

验收：选定场景的实际模型、日志、用量记录和失败处理可闭环追踪。

### 阶段 C：工具与会话状态

引入 `ConversationRuntimeState`，先验证主会话和分支锁在多工具回合中持续复用；coding/agent 仅作为新分支首选候选。上下文阈值路由仍只作用于未锁定执行域。

验收：跨多个工具回合使用同一实际模型配置；`strict` 模式不得出现跨成员候选；`availability` 模式的每次冷启动必须带有明确审计记录，且不破坏 Kiro 工具调用关联。

### 阶段 D：策略配置与界面

将规则、阈值、模型组映射和启用范围配置化，并在主前端展示策略命中、实际模型和会话时间线。

验收：策略变更可审计、可快速停用，并且不需要修改 Kiro 侧配置。

## 14. 与 xuexi 项目的借鉴边界

适合借鉴：

- 每个路由结论都有明确、机器可读的 `routing_reason`；
- 状态驱动的阶段表达与可核对的状态迁移；
- 配置变更、策略发布、停用和回滚的工作流可视化；
- 策略版本、操作记录和异常恢复的审计证据；
- 高成本/高风险决策与低成本/可验证任务分流的思想。

不建议直接复用到热路径：

- 每个聊天请求都创建完整任务图；
- 高频请求同步写入大量步骤状态；
- 让工作流引擎承担模型候选、熔断和流式转发。

实时路由应保持为内存中的轻量决策；持久化工作流应服务于策略管理、观测和运维动作。`xuexi` 的“模型选择理由必须可解释”是最应直接落实到 `RoutePlan` 和 Request Usage 的部分。

## 15. 待确认的产品决策

以下选择会影响首版范围，建议沟通后再进入实施：

1. 用户是否需要在 UI 中看到“本轮实际模型”，还是只向管理员展示？
2. 策略是否允许覆盖用户主动选择的模型，还是仅在同一逻辑模型的候选组内切换？
3. 首批准备建立哪些能力模型组：快速、coding/agent、vision、长上下文、强推理、classifier？
4. 首版选“全部先 observe”，还是“对 classifier/已核对 coding 工具直接 enforce，其余 observe”？
5. 当前模型组能力信息是否足够准确；若不足，能力标签由配置维护还是从模型元数据同步？
6. 会话状态首版是否仅内存保存；服务重启后回到默认路由，还是需要持久化恢复？
7. 是否先采集一组含非空图片 bytes 的主请求，验证图片字段、实际转换体和 vision 上游兼容性后再启用 vision 规则？
8. 缓存完整性优先时，锁定模型发生故障应直接返回失败，还是允许组内备用成员冷启动接管？这两种行为不能同时成立。

## 16. 推荐结论

推荐先实现 `RoutingContext -> PolicyResolver -> RoutePlan` 的纯决策链路和三种启用模式。结合当前真实流量与数据库配置，首版顺序应是：

1. 为模型或模型组补充显式能力标签，并建立 `vision`、`coding/agent`、`快速`、`长上下文`、`强推理` 等目标组映射；不以模型名称或 provider 猜测能力。
2. 对主请求字段做 `observe`，并将 activity 仅接为异步会话观察缓存。
3. 用非空图片、工具调用和工具结果的真实样本核对协议转换、缓存命中、锁定复用和候选切换记录。
4. 只有目标组、样本与会话锁定策略都齐备的规则才转为 `enforce`。

能力配置完成后，再按模型组准备程度选择：

- 模型能力和实际流量样本已核对：classifier、coding/agent 工具直接 `enforce`；图片规则在非空图片样本验证后 `enforce`；
- 模型能力或跨轮连续性仍不清楚：对应规则先 `observe`；
- 工具结果、长上下文、高推理、activity 驱动状态和内容语义判断：首版保持 `observe`。

这条路线复用现有候选、熔断、并发、超时与失败处理机制；唯一必须收紧的路径是严格缓存模式下的跨实际模型候选切换。它同时把 xuexi 的可解释路由和状态闭环放入正确的控制平面。
