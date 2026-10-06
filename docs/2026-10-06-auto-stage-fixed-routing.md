# Auto 五阶段固定路由

> 状态：前端已收敛为五阶段唯一入口，后端旧配置结构、选模与元数据分支已清理，相关 Go 单元、Mock 和阶段并发测试通过，独立只读复审无 blocker。用户继续禁止生成发布包、部署和重启；当前源码尚未应用到运行程序。真实阶段与费用验收尚未完成。此前的任务角色方案只保留在 [历史设计记录](2026-10-06-auto-task-routing.md)。

## 目标与边界

Kiro 继续负责工作流编排。代理只根据生成请求到达时已经获得的结构化阶段选择目标，在正常工具续轮保持模型与调用渠道身份，并记录缓存用量及费用估算。五个阶段目标独立配置，允许选择五个不同模型，也允许重复；配置引用具体启用模型的 ID，不引用轮询模型组。

仓库实施阶段不部署、不写真实数据库配置、不修改已安装的 Kiro。后续用户已明确授权备份并应用本机代理和 Kiro 桥接、重启两者；该授权不自动扩展为远端发布或修改授权服务。五阶段模型仍由用户自行配置，不主动调用收费模型，也不改变缓存键、历史转换、Responses continuation 或 classifier 的独立路由。实际缓存命中和实际费用收益必须与上游计费记录对账，不能以本地 Mock 代替真实效果。

## 配置契约

继续使用设置键 `auto_routing_config`，当前界面写入：

- `mode`：唯一合法值为 `stage_fixed`，用户无需选择模式。完全没有保存值时得到未启用的阶段配置；已有 JSON 缺少或留空 `mode`、或显式 `task_rules` 时明确返回“旧配置需重新配置五阶段”的错误，不再执行旧规则，也不静默改用默认模型。
- `stage_models`：对象，仅包含 `run`、`plan`、`implement`、`review`、`finalize`，值为实际模型配置 ID。

配置结构和规范化输出只包含 `mode`、`enabled`、`default_model_id` 和 `stage_models`。已有 JSON 的阶段映射必须是包含全部五个字符串键的对象。启用时，五个阶段目标和 `default_model_id` 都必须配置且为启用的实际模型；允许重复引用。`default_model_id` 表示阶段未提供或未匹配时的固定目标。删除角色字段、字符阈值、任务规则结构、执行优先级及对应元数据分支。

明确 `stage_fixed` 的已有配置可能带有此前序列化的七个退役字段。解析边界只识别并丢弃这些键，不保存其值、恢复其规则或把它们转换为阶段映射；重新保存后规范化 JSON 不再包含旧键。未知键、错误 JSON 和活动字段类型错误仍明确失败。非阶段记录不会因已保存某些阶段草稿而自动转为启用的阶段策略。

旧记录不能自动映射为五阶段模型。设置 GET 保持返回数据库原始 Auto JSON，供前端识别历史记录；不能在读取时把旧模式标签规范化成 `stage_fixed` 或写数据库。界面提供关闭的阶段草稿并提示旧配置不能用于新版 Auto，用户填写并保存后才替换原记录。新版后端遇到未替换旧记录时，新 Auto 任务明确失败；手动模型和独立 classifier 路由不受影响。有效阶段工具锁仍沿用首次目标，不被随后配置变化改写。设置 PUT 在解析和所有目标校验通过后统一写入，拒绝旧模式时也不部分写入其他设置。

## 阶段输入

阶段只能来自当前生成请求中可关联、在转发前到达的结构化输入。`/agents/activity` 是异步观察，不能决定首轮目标；不从提示词语义、`vibe`、`wf-coder`、最新 Activity 或相邻会话推断阶段。

最新日志中的 86 条可解析生成请求没有阶段字段；已安装 Kiro 内部有 workflow session 的 `workflowId/workflowNodeId`，但没有送进生成请求。本次定义以下同步桥接接收契约，真实生产发送端仍须单独接通，不能以测试注入宣称已完成真实 Kiro 接入。

代理专用请求头 `X-Kiro-Proxy-Workflow` 接受一个不超过 4096 bytes 的 v1 JSON 对象，例如：

```json
{"version":1,"stage":"plan","workflowId":"workflow-id","conversationId":"request-conversation-id","agentMode":"vibe","agentContinuationId":""}
```

`stage` 必须是 1–128 bytes 的安全节点标识，去首尾空白、转小写后只允许 `[a-z0-9._:/-]`；`workflowId` 可选，其余五项必需。`conversationId` 必须与当前生成正文中的非空值相等，`agentMode` 与 `agentContinuationId` 必须与当轮正文匹配（允许匹配空值）。客户端必须用实际请求构造出的 conversationId 绑定，不能把本地 step sessionId 当作它。拒绝重复/未知 JSON 键、错误类型/版本、重复 HTTP 头、显式空头及关联不匹配；返回 400，不回退选模。错误说明不包含原始头。手动模型与 classifier 不应用此阶段信息。

解析后的会话保存阶段值、来源 `request_header` 和是否提供。缺失与已提供但未知分别记录为 `stage_unavailable` 和 `stage_unknown:<stage>`，均使用固定目标。已知阶段为 `stage:<stage>`。私有头在入口移除，capture/passthrough 也不向上游发送，不参与正文转换、签名字段、缓存键或 continuation 键。阶段发送端还须核实实际 workflow node ID 的映射和 SDK/raw 两条传输路径。

### runtime 原生 Responses 通路

已安装客户端在带 `streamAttemptConsumer` 的 Agent 生成路径使用 runtime `/v1/responses`；代理原行为是 passthrough。为接通阶段，只拦截该精确路径的 POST、顶层 `model=auto` 且携带私有头的候选请求。无头请求、手动模型、其他域名/路径和 capture 继续原行为。候选头或正文关联错误返回 400，不放行官方作为隐式回退。

桥接必须在 raw 正文 `metadata` 同步补两项仅供本地路由的值：`kiro:proxy_continuation_id`（string，允许空）和 `kiro:proxy_current_tool_results`（非负 integer，当轮原始 `conversationState.currentMessage.userInputMessage.userInputMessageContext.toolResults` 数量）。与既有 `kiro:conversation_id`、`kiro:agent_mode` 一起校验当轮关联。工具锁依据这个明确的当轮边界，不根据历史中的任意 `function_call_output` 推断；count 不得大于实际 raw input 中工具输出数量。图片按 raw input 中结构化 `input_image` 检查。代理转发前移除上述两个私有 metadata，不能把私有路由字段送给上游。

该通路仅接受启用的 `stage_fixed` 及其有效续轮锁。原生 raw 最小支持范围是 `provider=openai` 且最终端点为 `/responses`；Anthropic/Gemini/Codex 明确报错，不偷偷改协议或改目标。OpenAI 的 chat 端点可按现有规则推导同渠道 `/responses`，上游不支持时原样返回错误。非空 `previous_response_id` 或顶层 `conversation` 暂不支持，必须完整 input，不能删掉服务端引用后继续请求。

原生 raw 不执行历史转换、压缩、缓存键生成或 Responses continuation。只替换目标 `model`、移除两项私有 metadata，并在请求未指定 effort 时应用模型默认 effort；其余 JSON 值、数值精度、数组顺序、未知扩展与已有 cache key 保留。不改用户提供的 effort，不静默降档。响应状态、头和正文/SSE 字节直接交还客户端；本地旁路观察用量，不包装成 Kiro 二进制事件、不追加合成响应事件。读到 EOF、断流或提前关闭都只结算一次并释放单模型并发额度。

已经正常结算的记录不因随后取消而改变。尚未结算时，已观察到的上游失败优先；否则当前请求取消或超时须明确在 `request_usage` 保存 `ctx.Err()`，不能因 Read/Close 的先后变为成功。客户端取消不增加 stats 的上游错误数量，已观察到的上游失败仍计错误；请求记录的未完成结果与上游错误统计口径不同。

日志实值已核对：`kiro_traffic.log.1` 的 `/agents/activity` 中有五个同名 nodeId，另当前日志有 `investigate`。客户端桥用明确的 workflow session nodeId 原样归一化，同名白名单映射；未知阶段固定目标。Activity 的这些证据只证明节点命名，不参与当轮路由。

## 路由与执行域

1. 当前消息有 `toolResults` 且现有 `ContinuationStateKey` 锁有效：先核对并复用原目标，阶段变化不重选；随后保存的新配置或关闭策略不改写有效锁。
2. 无有效续轮锁时，配置解析或校验错误明确失败，包括尚未替换的旧配置。
3. 无有效续轮锁且策略未启用：SDK 通路沿用原兼容路由；原生 raw Auto 明确要求启用有效阶段配置。
4. 可重选的新任务：按当前配置的阶段目标选择；阶段未提供或不匹配时使用固定目标。所有目标都经过已有、可核实的能力检查；图片输入不兼容时明确失败。

当前锁仅保证连续工具回合；同一聊天中不含当前工具结果的新任务重新读取最新配置。保留锁的现有过期、重启和手动改选语义，不把锁扩大为整个 conversation 固定。阶段模式记录锁定目标的脱敏调用身份，续轮发现相同配置 ID 的实际出站型号、provider、地址、端点、账号身份或服务档位发生变化时，明确失败，不能静默替换渠道。不存在旧策略选模或旧策略工具锁分支。

已经开始的阶段工具链沿用首次选择理由，日志为 `conversation_lock:stage:<原阶段>`；保存新配置或关闭策略在下一个新任务生效，不改写有效阶段锁。调用身份还包括单模型代理、端点改写开关、continuation/OpenCode 会话布局及模型默认 effort。价格、显示名称和备注不冻结；Codex 优先以 account ID/email 标识账户，正常 access token 刷新不会换身份。

同一配置的原有重试保持，新增阶段路由不引入跨模型或跨账号的自动备用切换。代理保证选中配置及出站身份；第三方渠道可能进行别名映射和内部调度，不承诺物理模型或缓存机器固定。

## 能力、推理与缓存

阶段模式不根据字数或推理档位改选目标。模型默认 effort 不能作为支持档位集合，字符数不能作为精确 token 容量。请求、分组、模型默认 effort 的优先关系保持原语义；已有转换器和真实上游错误继续可见，不静默降低 effort 或忽略输入。

保留当前稳定工具排序、系统动态尾部后移和历史追加转换。不新增 `stage` 或 `iteration` 到 `prompt_cache_key`、`ContinuationStateKey` 或 `previous_response_id` 的身份，不跨模型复用 continuation。先用已有 `prefix_audit` 定位实际前缀变化，再考虑单独的供应商缓存优化；本次不删改缓存键。

Auto 虚拟模型的候选元数据在新模式中来自五阶段及固定目标，不能继续从非活动角色目标生成。图片能力、上下文和推理展示不得伪造已证实能力；未知能力保持未知，沿用兼容展示时须说明不能作为上游能力保证。

阶段 Auto 仅当所有活动目标都明确支持图片时声明 `IMAGE`；容量取所有活动目标已知值的最小值，任一目标未设置时省略对应 token limit（Kiro 消费端使用可选链读取）。不生成由默认 effort 推断的支持档位 schema。具体模型、分组和关闭阶段策略时的普通默认路由行为保持原样；删除基于旧角色/规则聚合 Auto 能力的分支。

已安装 Kiro 对缺失输入容量使用内部 1,000,000 token 预算，这不代表目标模型具备该容量。使用前应在具体模型配置填写上游确认的 `context_window` 和 `max_tokens`，让 Auto 提供活动目标的已知最小值。容量不足仍由现有检查或真实上游明确报错；代理不推测容量，也不因此改选其他阶段模型。

## 界面和观测

### 客户端桥接候选与启用

`tools/kiro_stage_bridge.mjs` 仅检查原始 bundle 并生成工作区内的候选；不会安装、覆盖 Kiro 或重启进程。当前支持已检查的 Kiro extension `1.1.294`，原 bundle SHA256 `6acca01dfa75f7a301618f59736c996b481843445838bf618499487194d35fbb`。生成时严格核对全文件 hash、每个唯一锚点和完整 JS 语法；其他版本或已修改文件明确失败，不能猜测应用。候选修改 session services→Execution 顶层 `kiroProxyWorkflow` call option→SDK 和 raw 两条请求构造链；不依赖默认关闭的测试头开关。

候选只在进程环境 `KIRO_PROXY_STAGE_BRIDGE=1` 时发送阶段信息；普通启动维持原行为。代理的一键配置/重启启动计划仅在实际使用本代理 CA、非 capture、有效且已启用 `stage_fixed` 时置 `1`，其他情况置 `0`。该环境开关不会安装桥，也不表示安装已完成。真正应用到已安装 Kiro 前须用户另行授权；备份原文件、确认 Kiro 已退出、重新校验原 hash，然后原子替换。Kiro 升级后须重新核对，不自动复用旧候选。尚未进行真实 IDE 运行验收。

Auto 弹窗标题为“Auto 模型配置”，说明五阶段独立配置。无保存配置时，表单和列表摘要采用 `stage_fixed`，保持 `enabled=false`，不自动写入设置。历史旧记录显示“旧配置待替换”，提示新版 Auto 不再支持旧配置，填写并保存后替换，启用后按阶段路由；不能声称旧记录仍会在新版后端按角色规则运行。

弹窗直接显示 `run / plan / implement / review / finalize` 五个模型选择和“阶段未提供或未匹配的固定目标”；允许多个阶段使用同一模型配置。移除选模模式控件、模型角色、字符阈值、任务匹配规则及相关操作和校验。说明连续工具回合保持首次选定的模型与渠道。只通过用户保存替换历史记录，不自动启用或写入设置。

Auto 列表摘要区分“五阶段固定模型”的待配置、未启用和已启用状态，显示已配置阶段数量，并提供五个阶段与具体模型的完整映射；固定目标未设置时使用“未配置固定目标”。旧记录显示“五阶段待配置（旧配置待替换）”，目标名称标为草稿，不能把草稿当作已生效配置，也不能用其目标价格代表旧记录真实路由；费用提示以请求记录为准。

复用现有路由日志、`request_usage` 和 `prefix_audit`。路由日志补充模式、阶段、来源、阶段状态、选中配置、实际出站 model 和脱敏渠道身份；不记录密钥或完整提示词。现有分支键和请求用量记录继续可关联。费用由实际选中配置估算，真实收益需与上游同时间窗的账单对账。

## 验证与交付

使用临时数据库、本地 HTTP Mock 和纯规则测试，覆盖五阶段独立映射、重复模型、未提供与未知阶段、历史记录识别、非活动旧引用失效、图片不换模型、工具续轮阶段变化保持、新任务应用新配置、渠道身份变化、classifier/手动请求隔离及实际候选计费。前端验证五阶段表单、历史记录更新提示、精简保存载荷和错误处理；用户叫停构建期间仅做源码解析与内存 Mock，不截图。

真实阶段生产通路未接通时，交付明确区分“配置与代理实现”与“实际客户端接入”。允许先提供可审查的桥接源码或工具，但修改外部安装必须另获明确授权。

## 配置与费用核验步骤

1. 在具体模型配置中创建并启用目标，填入固定 provider、上游型号、地址、端点、账号、价格和已确认的容量。允许两个阶段引用同一个模型配置；五个不同模型不是缓存或费用改善的必要条件。
2. 打开 Auto 配置，分别填写 `run`、`plan`、`implement`、`review`、`finalize` 及固定目标，再启用并保存。界面只有五阶段配置；已有旧记录在保存成功时替换，不需要选择模式或设置模型角色。
3. 经授权安装与当前 Kiro 版本匹配的桥接候选。通过代理的“一键配置/重启 Kiro”启动且满足 CA、forward 模式和有效阶段配置条件时，桥接环境开关才启用。普通启动或升级后的未验证 bundle 不视为已经接通。无头 runtime `/v1/responses` 仍按原行为 passthrough，不能据 Auto 设置本身断言该通路已被代理选模。
4. 以 `auto-routing` 日志核对 `auto_mode=stage_fixed`、当前 `workflow_stage`、实际 `selected_stage`、`selected_model_config_id`、`outbound_model` 和 `channel_identity`；续轮理由应为 `conversation_lock:stage:<首轮阶段>`。当前日志还出现 `investigate`，其明确走固定目标。
5. 保持每阶段模型配置、渠道与前缀布局稳定，收集同类任务的完整基线窗口和调整后窗口。结合 `request_usage` 与 `prefix_audit` 检查请求量、输入总量、缓存读取量、输出量、失败与重试；以实际上游账单比较完成同类任务的总费用，并同时记录质量、完成率和耗时。按请求条数算的命中率与按输入 token 算的缓存比例不可混用。

费用估算使用实际所选模型配置的价格和倍率；本地统计不证明渠道按相同价格结算。多个模型各自积累缓存，切换阶段不会共享其缓存池；阶段稳定只减少不必要的切换和布局变化，不承诺命中率或节省比例。

## 首次五阶段实现的历史验证记录

- `go test ./router ./converter ./store ./api`：通过。
- `go test ./proxy -count=1`：通过；覆盖现有代理回归和新增 SDK/raw 阶段集成。
- `go test -race ./router ./proxy -run 'StageFixed|StageRaw|StageBridge|StageAuto|StageRouting|ForwardRawResponses|RawResponsesUsageObserver' -count=1`：通过。
- 修复取消统计后重新执行 `go test ./proxy -count=1`：通过；补充确定次序测试后，`go test -race ./proxy -run 'TestForwardRawResponses|TestRawResponses' -count=1`：通过，包含取消、deadline、Read/Close 两种顺序、上游失败优先和正常结算后取消。
- 设置 `KIRO_STAGE_TEST_BUNDLE` 指向只读的当前安装 bundle 后，`node --test test/kiro_stage_bridge_test.mjs`：4 项通过。执行真实 session services 构造、Execution options、LangChain options 分离、SDK command、raw body adapter 和 raw HTTP transport 的提取代码，外部依赖为 Mock；并校验完整候选语法和原文件 hash。
- 上一轮 `npm --prefix frontend run build -- --outDir <工作区内新的验证目录>`：通过。前端产物在 `build/auto-stage-validation-a37c2d9950c042a3863a0e55c47d5f94/`；该产物对应本轮默认入口、标题和摘要修正前的源码，不代表最新前端已构建验证。
- 使用 Go `-overlay` 将上述已验证资源映射到 embed 输入，`go build -tags desktop -ldflags='-H=windowsgui'`：通过。包含取消统计修复的桌面候选为 `build/kiro-proxy-auto-stage-reviewed-candidate.exe`，SHA256 `38ce93a56ec37de384206abf569558803b340ac95073f8aba1a1f4ed9d2a3444`。未覆盖当前 `frontend/dist` 或正在使用的代理程序。
- `git diff --check`：通过。
- 独立复审：Astra/high 首轮核对完整契约和实现，发现取消统计的顺序差异；修复并补足确定次序测试后，由未参与写入的 Sol/xhigh 复核修复、文件范围和候选 hash，结论无 blocker。复审角色保持只读；完整测试与 race 的实际执行证据由主代理核验。

本次 Mock 还核对失败/缺终止 raw SSE 的状态与正文逐字节保留、零重放、单次 stats/用量结算与 lease 释放；五阶段/固定目标/当前工具结果边界、手动模型清锁、模式关闭或切换、图片能力和未知容量的 wire 展示均有相应测试。以上结果证明仓库行为与候选可构建，不证明真实 IDE 已运行成功、上游缓存命中提高或实际费用下降。

### 上一轮前端入口修正验证（用户叫停编译后）

本轮修改 `AutoRoutingSettings.vue`、`AutoModelRow.vue` 和父弹窗 `Models.vue`。只使用现有依赖解析 SFC、JavaScript 和模板 AST，没有执行 `compileScript`、`compileTemplate`、Vite 或 Go 构建，也没有启动开发服务器、截图或修改真实数据库。

Node 内存 Mock 执行组件中的实际脚本，使用 Vue 的 `ref`、`computed` 和 `watch`，替换设置读取与写入边界。验证通过：不存在配置的新默认、已有无模式配置的兼容语义、全部五个模型字段、五阶段分支与旧表单分离、启用必填校验、阶段复用同一模型、非活动旧模型引用与任务规则原值保留、模式切换、停用或不存在模型选项、保存失败保留未保存状态，以及读取或解析失败阻止保存。列表摘要验证待配置/未启用/已启用状态、五阶段完整映射、目标去重和不存在模型提示。`git diff --check` 通过。

未参与写入的 Sol/xhigh 对照本轮修改前快照核对四个文件，独立复审无 blocker。列表完整阶段映射在悬停 `title` 提示中，正文显示配置数量；触屏与键盘的可发现性未验证。本轮源码未进行构建或浏览器视觉验收，旧候选仍对应修正前前端；现用页面不会因源码修改而更新。

### 五阶段唯一配置入口修正

用户进一步明确旧任务策略用不上后，取消模式选择及旧策略表单。设置组件和摘要复用 `frontend/src/utils/autoStageConfig.js`，前端读取与写入都只产生四字段阶段配置；历史字段只作为已知字段识别。旧记录投影为关闭的阶段草稿，保留明确的固定目标与已存阶段映射，显示待替换状态，保存之前不改变数据库或运行路由。旧记录本身与旧角色不会被猜测成新的阶段映射。

该轮修改前快照位于 `build/auto-stage-only-before-5f3c8c87f6c2477b9fcaa9d67501be00/`。当时只收敛前端，后端旧记录解析与执行代码尚未清理，也未调用真实模型或写真实设置；后端清理属于本次后续实施。

验证通过：共享模块及列表的精简字段、历史草稿状态、忽略非活动字段、完整阶段映射和错误保护；组件真实脚本配合 Vue 状态与 API Mock 的无自动写入、启用时六项必填、重复模型、严格四字段保存载荷、成功保存才替换历史状态、保存失败保留快照与旧记录标记、读取或解析失败阻止覆盖、停用或缺失模型选项。仅打开旧记录时不触发未保存修改确认，用户编辑后才记为 dirty。三个 Vue 文件及共享模块的 JS、SFC 和模板 AST 解析通过，`git diff --check` 通过。未参与写入的 Sol/xhigh 独立复审无 blocker；其建议的提示文案已改为“保存后替换为五阶段配置；启用后按阶段路由”，避免将关闭的草稿描述为生效。编译、安装和重启保持暂停，尚无构建或浏览器视觉验收。

### 五阶段后端退役清理验证

本轮删除 `AutoRoutingModeTaskRules`、`AutoTaskRule`、角色字段、字符阈值、任务规则校验及实际选模分支。`AutoRoutingConfig` 与规范化 JSON 只保留四字段；阶段锁移除旧模式分流，所有续轮统一核对原模型与渠道身份。Auto 能力摘要只聚合五阶段及固定目标，不再读取旧角色或规则目标。

七个退役键名仅在设置解析边界用于识别并丢弃明确阶段配置中的历史附带字段；旧模式记录明确报 `legacy` 错误。设置 GET 返回原始 Auto JSON，让界面提供待替换草稿；PUT 只有解析与目标校验全部通过才统一写入，保存结果不含退役字段。读取不迁移、不写数据库。

原 `proxy/auto_task_test.go` 改为 `proxy/auto_stage_scope_test.go`。旧规则用例被阶段用例替换，保留选中目标计费、当轮工具续轮、历史工具与新任务边界、分支锁、手动改选及 classifier 隔离验证。对应配置、API 和 router 测试改为唯一阶段模式，并覆盖旧记录拒绝、附带旧键丢弃、未知字段失败、图片不换目标、锁期间配置失效和渠道身份改变。

主代理在执行者结束写入后统一执行：

- `go test ./store ./router ./api ./converter ./proxy -count=1`：全部通过，使用临时数据库和本地 HTTP Mock。
- `go test -race ./router ./proxy -run 'Stage|ResolveAuto|AutoModel|TestTypesafeClassifierMapsChoiceUsageAndAccounting' -count=1`：全部通过，覆盖相关阶段锁与代理通路。
- 独立复审后补回关闭策略时的 SDK/raw 有效续轮锁、下一新任务边界，以及续轮新增图片不兼容时的原锁检查；`go test -race ./router -run 'TestStageFixed(ToolLockRejectsNewUnsupportedImage|LockSurvivesDisabledConfigUntilNextTask)' -count=1`：通过。生产源码未再改动，未重复无关测试。
- 全项目 Go 源搜索旧配置类型、字段和 selector/helper：无匹配；生产代码的退役键名仅保留在 store 解析边界。
- Go 文件格式检查与 `git diff --check`：通过。两处前端旧记录提示的 Vue SFC、JavaScript 和模板 AST 解析通过，未运行 Vite 构建。

实现过程第一次 router 格式检查及并行 API 编译曾遇到 `auto_policy.go:155` 的多余闭括号；执行者已修复，随后统一测试通过。该错误不作为环境问题或留存失败处理。

未参与写入的 Astra/high 对照快照和当前源码完成独立只读复审，无 blocker。复审发现两处旧测试删除后的覆盖缩减，主代理补回并验证后由同一只读复审者确认缺口已补齐，原结论保持有效。

本轮修改前快照位于 `build/auto-stage-backend-before-94b4119b0e5d4dc895f273f889308418/`；补测前已复审的测试文件另保存在该目录的 `router/auto_stage_test-reviewed-before-coverage.go`。本轮没有打包、替换程序、安装桥接、重启、写真实设置或调用收费模型。此前候选与现用程序均不包含本轮清理，真实客户端阶段接入、上游缓存命中率和实际费用收益仍未验收。

## 本机应用预检（2026-10-06）

用户已明确授权备份、替换本机代理、安装 Kiro 桥接和重启。本机现用代理版本为 `2.1.8`，`/api/license-info` 返回 `authorized=true`、`version_valid=true`、`tampered=false`；最新版本记录的 `expected_sha256` 与现用代理文件 hash 一致。授权服务源码要求版本存在且文件 hash 匹配，才允许转发。直接将不同 hash 的候选按旧版本替换会失去授权；此前仅用于构建验证的候选使用源码缺省 `AppVersion=dev`，不能作为已登记的生产版本直接切换。

实际仅完成原文件备份和候选临时复制；原子替换调用失败，Kiro 主 bundle 与代理主文件均未改，未重启。备份位于 `build/stage-install-20261006-92854b03a86d46978347b762550465d9/`。用户随后要求暂不编译并指出前端配置项遗漏，本轮前端源码修正与复审已完成，编译、安装、重启及发布保持暂停。

新版本尚未登记或发布，不能使用 `dev` 策略、清空远端 hash 或覆盖旧版本 hash 作为隐式绕过。远端发布或登记仍须另获明确授权；当前不执行该流程，不宣称本机阶段路由已经生效。前端本轮修改前快照在 `build/auto-frontend-before-fix-0f24eabda0404540a05d39372efe7e3d/`。
