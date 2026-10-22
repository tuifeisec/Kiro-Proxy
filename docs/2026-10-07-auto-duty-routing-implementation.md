# Auto 职责固定路由实施契约

> 2026-10-22 更新：无补丁 raw Auto、路径规则身份、原生 continuation 锁与启动行为由[当前契约](2026-10-20-activity-driven-auto-routing.md)补充并优先适用。三元组规则继续兼容；本文后续测试禁令和安装桥接说明仅描述当时任务，不约束本次已授权修复。

> 2026-10-07。用户已授权多 agent 开发，当前工作区 main；本轮只改源码、文档与 Mock 用例，不运行测试、不编译、不部署/重启、不改本机数据库或 Kiro 安装文件。本文将模型职责布局的未决接口收敛为实施契约，实际完成状态以交付记录为准。

## 配置与迁移

新设置继续使用 auto_routing_config，JSON version=2、mode=duty_fixed；layout=standard 对应 18 位，layout=expanded 对应 20 位。enabled 与 default_model_id 保留。duty_models 是职责键到固定目标的对象，每个目标包含 model_id（实际配置 UUID）和 reasoning_effort（明确字符串；空表示固定使用上游默认，不采用客户端等级）。node_bindings 与 node_overrides 均为数组。

standard 职责键：orchestration、routine_support、investigation、requirements、design、implementation_plan、implementation、iterative_execution、experiment、design_review、code_review、semantic_review、review_synthesis、review_verdict、validation、delivery_audit、operations、pr_lifecycle。

expanded 删除 routine_support/pr_lifecycle，新增 preparation/finalization/pr_submit/pr_feedback，其余保留，共 20 位。启用时默认目标与所有当前布局目标都必须引用启用的模型或现有模型分组；禁用时允许目标暂未填写。参数必须满足本地 provider 表达能力，不能声称上游一定支持所有等级。

node_bindings 每项包含 definition_id、definition_revision、node_path、duty；node_overrides 每项包含同一稳定身份三字段，以及 model_id、reasoning_effort。稳定身份三元组严格精确匹配；禁止裸 nodeId 通配、显示名称匹配和迭代编号配置键。绑定职责须在当前布局中。重复身份拒绝，不把冲突靠数组顺序静默裁决。

保留 stage_fixed 只读兼容入口，使尚未迁移的现有五槽配置仍可运行；不恢复旧角色/字符阈值/提示词分类策略。新界面必须显示旧配置与迁移预览，用户确认保存时才写 v2：run→orchestration、plan→implementation_plan、implement→implementation、review→code_review、finalize→routine_support（expanded 为 finalization）。其余职责留空，由用户选择或明确点击批量填充；不能自动将旧 plan 复制给调查/需求/设计。

首次 stage_fixed→duty_fixed 保存时，服务端将完整原始值备份到 auto_routing_config_stage_fixed_backup，与新配置在同一 SetSettings 事务写入。客户端不得覆盖此保留备份键。校验或写入失败不改变原值；GET 不写回或自动迁移。回滚旧版需恢复备份原值，不把 v2 JSON交给旧版解析器。

## 私有元数据 v2

仍使用 X-Kiro-Proxy-Workflow 私有头，解析器接受严格 v1 兼容头和 v2 头，按 conversationId、agentMode、agentContinuationId 校验请求绑定，拒绝重复字段、未知字段、错误类型和超限值。v2 字段：version、nodeId、definitionId、definitionRevision、nodePath、taskType、executionId、workflowId、conversationId、agentMode、agentContinuationId。身份/职责暂无法取得时允许空字符串，并明确记录 unknown；不能把缺失节点伪装成 run。

ParsedConversation 新增 WorkflowNodeID、WorkflowDefinitionID、WorkflowDefinitionRevision、WorkflowNodePath、WorkflowTaskType、WorkflowExecutionID、WorkflowMetadataVersion。现有 WorkflowStage 字段仅保留原始 v1 兼容含义，不再兼任新职责。

职责来源只能是精确内置 recipe 与定义修订/节点路径声明、用户明确稳定节点绑定、或工作流/子 Agent 创建时的显式 taskType。新增工具声明参数由创建者明确填写；不读取运行提示词猜职责，不把 wf-planner 等 Agent 名称当分类。已有工作流定义缺少来源时允许内容身份；但合成 agent://run 的静态 definition 无法区分职责，必须有显式声明或继承来源。

桥接必须在可信创建入口记录定义来源/修订和职责声明，并在真实 executeStep 的完整 nodePath、iteration、branch、step session 边界保留这些信息。长期路径仅剔除运行 workflowId 的开头帧与经定义树证实的 synthetic iteration 帧，保留真实结构和分支。禁止给 Kiro workflow schema 插入未经支持的属性；私有声明应附在可证实支持的输入/会话元数据或记录旁路。

路径规范化必须核对完整定义树：只剔除开头的运行 workflowId，并且只在 repeat 进入其子节点的实际位置消费 synthetic iter-N 帧；真实节点 ID 即使叫 iter-0、iter-1 或与 workflowId 同名也保留。依据 sequence.steps、repeat.steps、parallel.branches 逐层精确核对，最终必须是 metadata 所指的真实 step；不能靠全局正则删除名字。真实路径段用 encodeURIComponent 无碰撞编码，空 ID 段使用单独的 % 标记；内置 ASCII 名称保持原样。缺定义或结构无法证明时留空稳定路径，不能生成一个可能覆盖其它节点的假身份。

工作流创建成功后、创建接口返回前，在 Kiro 已有 workflowBaseDir/workflowId 目录写入独立的 kiro-proxy-routing-v2.json，复用 stateFs 的临时文件写入与 rename 范式并读回核验；只保存版本、工作流身份、可信定义来源及明确声明/继承的路由上下文，不保存提示或凭据。恢复时精确读取并校验版本与 workflowId；已存在的记录损坏或读写失败必须明确报错。历史工作流没有记录时，非合成定义可使用完整定义内容身份；合成 agent://run 不猜职责，显示 unknown。写入失败保留已经创建的工作流原件，不能把未持久化声明当作成功返回。

agent:// 的明确声明属于创建时的单个 run step；单节点内容修订后仍保留该声明，但使用当前完整定义内容与声明重新计算 definitionRevision，不能沿用旧节点覆盖。无独立声明的单 run 继续继承父完整路由。结构改成多节点、容器或不同节点 ID 时，原单节点声明不能自动分配给新节点，改用内容身份并显示 unknown，等待明确绑定。内置 recipe 修订后的内容也不能盲目沿用原版职责表。

workflow 的 executionId 使用同一 step session 的稳定实例标识；单次 driveExecution 的 this.executionId 会在续执行变化，不能拿来承诺整节点锁。普通主聊天在已核实的主执行入口声明 orchestration，执行实例可按该用户任务的主 Execution 标识。普通子 Agent 默认继承父私有路由上下文和 Auto，显式声明独立职责/节点时才新建绑定；并行子任务复制上下文，不能修改共享对象。

step 会话创建、冷加载和已驻留会话的继续执行三个入口都必须从当前可信 step metadata 与恢复后的来源记录补齐私有路由。ACP session/load 先打开持久化聊天后再继续工作流，同样使用真实 step sessionId，不能因会话已经存在而跳过职责恢复，或退到单次 driveExecution 标识。

如果无法证明某入口的节点实例稳定，发送空 executionId；服务端只保持现有工具续轮范围，并公开 lock_scope=tool_continuation，不能伪称整节点已锁。主控/工作流入口已证明稳定实例时使用 execution 范围。不得为未知节点补猜测身份。

## 路由、参数与观察

v2 策略优先级：稳定节点模型覆盖→稳定节点职责绑定→创建时明确 taskType→固定默认目标。旧 v1 元数据用于 legacy stage_fixed 兼容；duty_fixed 收到 v1 时记录 metadata_v1 并按固定默认目标处理，直到桥接升级，不按原始节点名猜职责。

expanded 中，明确 routine_support 必须结合已声明节点职责的拆分表解析到 preparation/finalization；pr_lifecycle 类似拆为 pr_submit/pr_feedback。该表只细化已声明类别，不识别未知 taskType。未知布局职责、配置缺失、稳定身份缺失等均有明确选择来源和 unknown 原因。

Route 新增 AutoDuty、AutoEffort、AutoEffortFixed、AutoExecutionID、AutoLockScope、AutoSelectionSource、AutoUnknownReason；新增字段不改变手动模型或 classifier。固定目标快照覆盖有效 effort，客户端冲突值仅用于日志说明。proxy Preflight 新增 AutoReasoningEffort *string，route.AutoEffortFixed 时设置它；resolveReasoningEffort 优先读取该固定值，包括空值。Raw Responses 必须同步覆盖/移除客户端 reasoning.effort，保留 reasoning 的其他字段。

有稳定 executionId 时，锁以该 workflow 执行实例关联，同一实例每轮都复用目标，不以是否有工具结果为条件；普通继承子 Agent 同步复用父 executionId。缺实例时沿用按会话/分支的工具续轮锁，并标清范围。新实例重读配置。进行中的目标资源身份/有效参数改变或目标失效时明确报错，不换渠道、轮询或降档。锁不等于 Responses 续链或缓存保证；进程重启无法保证内存锁延续，必须如实标记这一边界。

execution 范围锁不按工具续轮的一小时空闲期限清除，当前进程内保留到显式忘记或进程结束；新非空执行实例不能复用旧工具锁。缺执行实例的工具续轮只有在没有明确新节点/职责边界时沿用快照，提供不同稳定节点或不同明确 taskType 时重新按新边界选择。改职责映射只影响新实例，旧实例使用首次目标与有效等级；改旧目标本身的资源身份或使目标失效仍报错。工具续轮复用快照前先判断明确边界，不能因不相关目标造成新配置无效、停用策略或切换模式而中断旧链；新边界与新执行实例仍须通过当前配置校验。

Router.GetAutoObservations() 提供当前代理进程内的有界安全快照；API GET /api/auto-routing/observations 返回数组。每项包含 definition_id、definition_revision、node_path、node_id、task_type、selected_duty、selection_source、unknown_reason、model_config_id、outbound_model、effective_effort、channel_identity、lock_scope、count、last_seen。只保留路由元数据，不存提示正文/凭据，不承诺跨进程持久化。缺少稳定身份时界面禁止保存节点绑定或覆盖。

前端展示职责目标、实际模型与等级、模型绑定的多个节点；支持18/20布局、固定默认目标、稳定节点绑定/覆盖、已观察节点与未归类清单、配置保存后 Auto 摘要刷新。未知版本/字段不能被归一化后覆盖；保留错误与原始配置。手动请求、classifier、必要提示词和缓存键不因新路由元数据变化。

内嵌桥接清单必须与最终源码补丁同步。安装源码允许原版、当前版及清单列出的精确旧桥接 hash；旧桥接先在内存逆补丁还原，并核验完整原版 hash 后再应用新补丁，不写中间原版文件。真正替换前备份当前安装的完整旧版本，并按预检时的完整 hash 再次核对；其它修改或 Kiro 版本仍明确拒绝。本轮只维护该源码与清单，不执行安装。

## 验证与分工

### 现有模型分组接入（2026-10-07 补充）

用户要求 Auto 可以选择“模型 → 分组”里已经建立的整个 GPT 等分组。默认目标、职责目标、稳定节点覆盖共用现有目标选择，保留 v2 的 model_id/default_model_id 字段，允许其引用分组 UUID；模型与分组 ID 冲突时明确拒绝，不能根据表顺序选择。旧单模型配置无需迁移。界面必须区分模型与分组并展示分组名称；停用、无有效成员和不存在的目标不允许保存为非空目标。

分组目标复用现有成员优先级、并发限制、熔断、亲和、尝试上限和后备分组策略，不能把组 ID 替换为首成员 ID。单模型继续保持原有模型与渠道锁。分组执行锁固定逻辑分组及有效等级，组内成员可以按现有调度策略变化；因此选择分组并不承诺整节点始终使用同一渠道。未发生故障或拥塞时沿用现有亲和策略；切换成员须记录实际出站目标。修改职责映射只影响新实例；进行中分组的成员、渠道、请求参数或调度配置变化时明确报错，不静默改写已锁定目标。

职责及节点覆盖的 reasoning_effort 仍是固定值，空字符串表示固定上游默认。默认分组目标使用组级 reasoning_effort，组未设置时使用全部有效候选一致的默认等级，否则为空；不使用首成员推导。固定等级必须能由全部有效候选（包括后备组）的 Provider 表达。前端选项取其等级交集，服务端独立校验。

普通 Kiro 请求和原始 Responses 请求都必须接通整个分组。原始 Responses 仅尝试 OpenAI /responses 兼容候选，保持原始协议与 body 字段，并为每个候选分别改写目标和固定等级；无兼容候选时明确报错，不改走 Chat 协议。调度前只能记录逻辑目标，Auto 日志及观察中的实际 model_config_id/outbound_model/channel_identity 在实际候选确定后记录，同时包含 target_group_id/target_group_name。不得把首候选记录成已经调用的模型。

原始 Responses 的分组发送只在获得响应前按现有 HTTP 状态、网络、容量和健康策略选择成员；不能预读 SSE 判断 response.failed 或缺终态后跨模型重放。200 的失败事件、空流、读取错误和取消保持原始响应交给客户端，只在单次旁路观察中结算。返回最后非 2xx 时也保留完整正文及响应头，不能复用会截断错误正文的日志读取函数。RawResponsesHasImages 必须参与候选图片能力判断；raw input 不走 SDK 的历史图片投影。响应、统计、健康终态和 lease 各只结算一次。

本次维护只修改源码、文档与回归用例，不运行用户未授权的测试、不修改真实配置或 Kiro 安装、不启动或重启程序。验证结果必须区分静态检查、编译和用户运行证据。

### 用户运行日志核对（2026-10-07）

用户自行运行已编译的 desktop,dev 版本。14:16 的桥接安装记录报告完整候选 SHA256 为 8384475aad60356ba18dc0be15b75af37efcfd506987797a5dcad127540e77be，与源码候选一致。14:17–14:24 的实际生成包含 44 条 duty_fixed / v2 路由：主控 orchestration 4 条，调查 investigation 40 条，分别使用 gpt-6.1-sol 与 cn:deepseek-v4.1-flash；有效等级均为 high，lock_scope 均为 execution，unknown_reason 均为空。调查使用一个稳定 step session 实例；主控包含两个用户执行实例。

这一时间段的 44 条前缀审计未报告 prefix_changed=true。已完成响应的上游输入 token / cache_read token 汇总：调查 4,101,541 / 3,954,176（96.41%），主控 129,636 / 89,600（69.12%）。这些是响应 token 口径，不能等同于渠道后台的请求命中率或实际账单节省。仅主控和调查已取得运行证据，其余职责、稳定节点覆盖与工作流冷恢复尚未由本次日志验收。连续调查调用不能仅凭次数判定为重复重试，须核对每轮工具推进与终态。

只读复核补充：44 轮的上游响应均为 200，该窗口没有 warn/error；调查请求里的累计 tool_results/tool_blocks 从 2 增长到 72，其后续前缀审计均为 append，支持工具续轮持续推进的判断。43/44 轮 cache_read 非零。日志未出现重试提示，但缺少统计中的 RetryCount/WireAttemptCount，不能凭这一点承诺完全没有传输重试。88 条请求/响应 detail 的截断是日志 64 KiB 详情上限，不能当成模型输出截断；另有 4 次工具描述裁剪需要与该既有转换行为区分。

### 分组候选构建记录

前端、后端、转发按非重叠范围完成实施及静态检查，修复独立预审发现的 raw HTTP 健康重复结算、取消期间错误响应 lease 遗留、内部重试耗尽吞掉最后错误正文、半开探测取消后未释放，以及观察条目缺少逻辑组身份的问题。新增 Mock 用例源码覆盖这些触发条件，但没有执行。

主代理按用户“修改代码后编译根目录 dev”的指令，备份旧 dev-next 和 frontend/dist 后重建前端，并使用 desktop,dev / CGO_ENABLED=1 / Windows amd64 编译 D:\proxy\kiro-proxy-dev-next.exe；前端和 Go 构建退出码均为 0。二进制为 AMD64、GUI subsystem，内嵌 Models-C1FFvabz.js 与最新前端产物逐字节一致。产物 SHA256：1454d7650e3fddbeb34cd81c9ec78c7b2e6300e4c59059bbb76460f736dfae4f。运行中的 kiro-proxy-dev.exe 未覆盖或停止，没有启动候选程序、运行测试、写真实配置、安装桥接或重启 Kiro。分组真实调用及其它职责由用户运行后继续核对日志；编译成功不代表这些行为已通过运行验收。

修复后由新的独立 Astra/high 只读复审者检查冻结源码和新增 Mock 源码，结论为未发现阻塞交付的问题；上述五项预审问题均有修复代码与对应回归断言。复审未执行 Mock 或真实请求，不替代用户接下来的运行验收。本次没有提交或推送 GitHub。

### Auto 设置中的职责与节点说明弹窗

用户要求在设置里查看不同节点的说明。标准 18 位和细分 20 位的每个职责项均提供“说明”按钮，弹窗显示职责用途、主要输入与产物、选择模型时关注的能力，以及该职责列出的节点用途。已观察和未归类节点均提供“节点说明”，展示当前实际选中的职责、明确声明及节点身份；没有可信职责时显示用途尚未确认，不能仅按裸 node_id 自动归类。节点名称用途表只作为参考说明，不成为模型路由规则。

说明为只读，不写入配置、不触发保存或改变模型选择，也不标记表单为已修改。弹窗支持关闭按钮、遮罩点击及 Escape，使用键盘焦点限制并在关闭后恢复入口焦点。说明弹窗的按键不得触发外层 Auto 设置的 Escape 关闭或丢弃修改确认；20 位拆分职责须有独立说明。内容复用当前界面样式，不引入 UI 依赖或功能开关。

说明实现覆盖两个布局共 22 个独立职责键、47 个关联节点名。初轮独立复审发现未知节点名可能命中对象继承属性，以及已确认职责与名称参考缺失提示冲突；修复后两个查询仅接受自有键，名称参考缺失不再否定观察记录中的实际职责。helper 的语法检查、Vue SFC 静态解析和修后前端构建均通过。按用户要求没有执行测试、界面交互或真实模型请求；原生弹窗在目标 WebView 中的焦点、Escape 和遮罩行为仍由用户运行验收。

### 自定义节点规则管理（2026-10-08）

保留现有 18/20 职责配置，额外将 node_overrides 展示为可新增、编辑、删除的“自定义节点规则”。规则直接指定实际模型或现有模型分组及固定思考等级，不要求节点属于内置职责目录，不新增配置模式或改变 v2 JSON。已保存的节点覆盖原样保留；node_bindings 的职责绑定行为保持现有契约。

已观察和未归类节点均可将完整稳定身份带入规则编辑；用户也可手工录入尚未观察到的节点，但必须填写真实的 definition_id、definition_revision、node_path，不以裸节点名跨工作流匹配。规则编辑显示用途与字段提示，校验目标、等级、完整身份以及重复身份；编辑时只排除被编辑的原规则，不能静默覆盖另一规则。编辑窗口确认后只更新 Auto 设置草稿，仍由外层“保存”提交。取消编辑不修改草稿，编辑窗口关闭与键盘事件不关闭外层设置。

清单显示规则目标、等级及本进程是否观察到对应身份。“本进程尚未观察”不等于节点不存在；观察记录只代表最近调用，不宣称节点仍在运行。身份缺失的请求只能展示和说明缺失原因，不能生成猜测身份。未命中自定义规则时继续按现有职责绑定与声明规则选模；这些规则也未命中时才使用固定默认目标。删除自定义规则恢复同一优先级链路，不保证已归类节点退到默认。添加、编辑或删除规则影响新的执行实例，进行中实例继续沿用已有快照。

观察记录已命中 node_override 且 unknown_reason 为空时归入已匹配记录，即使 selected_duty 为空；显示“自定义节点规则”，不伪造职责声明，也不将成功覆盖标成路由未知。职责说明仍展示真实 selected_duty 与节点参考的区别。

观察接口读取失败时，规则清单显示“观察状态未知（读取失败）”并保留原始错误；只有成功取得观察记录后才判断本进程已观察或尚未观察，不把取证失败当作未观察。

本轮实现仅涉及 AutoRoutingSettings.vue 与新增 AutoNodeRuleEditor.vue，保存仍使用原有 v2 的五字段 node_overrides，不修改真实配置、路由算法或已安装桥接。初轮静态复审发现观察读取失败被标为未观察，以及 JavaScript trim 未覆盖后端所拒绝的边界 U+0085；修复后保留错误并显示未知状态，身份首尾补拒绝 U+0085，不裁剪输入，也不拒绝内部合法字符。两个 Vue SFC 静态解析及修后前端构建通过，根目录 dev 构建通过并核对 desktop,dev、Windows amd64、GUI 子系统及最新 Models-CVFgmQRa.js 完整嵌入。产物为 D:\proxy\kiro-proxy-dev-custom-nodes.exe，SHA256 为 C96DE01BA14127DB71C4C87512F601DD028CE6A12525B5BE08DB1FB488A307C3。没有执行测试、Mock、界面交互、真实模型请求、部署或重启；实际新增、编辑、删除、保存及新执行规则命中仍由用户运行验收。

修后由新的独立 Sol/xhigh 只读复审者检查冻结源码、契约和候选产物，结论为未发现 blocker；两处初轮问题已修复，规则 CRUD、原职责保全、目标与等级校验、五字段保存及弹窗静态事件路径符合契约。该结论不替代用户的实际运行验收。本轮源码保留在当前 main 工作区，没有新增 Git 提交或推送。

Mock 用例覆盖18/20布局、严格校验、旧配置迁移事务保全、节点优先级、未知回退、实例/工具锁、effective effort 冲突、raw body、手动与 classifier 不受影响、桥接并行隔离与 v1/v2绑定；本轮按用户限制仅编写/审阅，不执行测试。允许无请求的源码语法/格式静态检查；无实际运行证据时不称测试通过。

后端执行者拥有 store/auto_routing*.go、新职责目录/观察文件、router/auto*.go 与 router/router.go、api/settings.go、api/server.go、新观察 API 与相关 API Mock；桥接执行者拥有 tools/kiro_stage_bridge.mjs、其新私有辅助模块和 test/kiro_stage_bridge_test.mjs；前端执行者拥有 AutoRoutingSettings.vue、autoStageConfig.js、AutoModelRow.vue、Models.vue、新职责配置辅助代码。主代理拥有本文、converter/workflow_stage*.go 与 ParsedConversation 新字段、proxy 请求/出站等级覆盖、Auto 日志与集成 Mock，负责最后集成。不得跨范围回滚或覆盖他人改动，范围需要调整时先向主代理报告。

### 节点快捷规则与用途说明（2026-10-08）

本节记录首轮实现。用户后续反馈“日志节点没有归结清楚、说明不足且很多节点无法快捷添加”，对应的新行为以 [Auto 节点目录与快捷配置契约](2026-10-08-auto-node-catalog-design.md) 为准：完整身份归并、历史日志来源、独立规则快捷更新、明确改为职责绑定、缺身份请求的职责/默认配置入口。以下关于已有覆盖禁止快捷更新与只显示本进程观察的限制已由该契约替代；路由身份和优先级仍保持。

保持现有职责和路由优先级。已观察且具有完整稳定身份的节点提供一个紧凑的规则目标选择器和“快捷添加”：选择已有职责时写入 node_bindings，持续沿用该职责当前配置的模型或分组及思考等级；选择单模型或分组时写入 node_overrides，复用该目标配置的思考等级，并在添加前展示目标与等级。快捷添加只更新 Auto 草稿，外层保存后提交，不再要求重复确认完整身份。身份仍按完整三元组精确匹配，长身份默认收起且可展开核对；高级编辑保留用于手工录入身份、编辑独立规则或单独指定等级。

观察列表另提供批量入口：勾选多个节点后，一次选择职责或独立目标并批量添加，避免逐条重复选择。同一完整身份的历史观察共享勾选状态、只添加一次。身份不完整及已有独立覆盖的节点不可勾选；批量添加先校验全部候选，任一不符合条件时整批不更新草稿，并展示原因。

快捷添加不得隐式覆盖已有独立规则；已有规则提供编辑入口。职责绑定可通过明确的快捷更新改为另一职责。已有独立规则优先于职责绑定，界面应说明该优先级。目标不可用、等级不兼容、身份不完整或字段非法时不能快捷添加，不替用户推测目标，不改真实配置或进行中实例的快照。引用已有职责的绑定与复制模型目标的独立覆盖必须在界面上明确区分。

观察清单、保存的规则和高级编辑显示节点用途及说明依据。用途说明覆盖已有职责目录节点、日志发现的额外节点以及客户端明确声明的任务类型；能读取到工作流定义的节点按定义解释，只有名称参考的内容标为参考，来源不足的节点明确显示“用途待确认”并保留可核对元数据。路由异常原因提供中文解释并保留原始原因码，“未归类”不等同于违规。说明目录不参与自动归类、不自动创建绑定，也不以名称代替稳定身份。完整节点清单及证据边界另存文档；未观察、未取得定义的任意未来节点不宣称已掌握用途。

实际实现涉及 AutoRoutingSettings.vue、AutoNodeRuleEditor.vue、autoNodeDescriptions.js 和新增的 [节点用途清单](2026-10-08-auto-node-description-inventory.md)。名称参考整理为 49 项；startup-check 和当前 investigate-wasm 定义另有按完整身份关联的任务日志用途说明，另一份同名 investigate-wasm 定义因缺少独立任务文本而保持名称参考。run 不再被一概解释为主控入口。记录用途与实际 selected_duty 分开展示，路由原因保留中文解释及原始码。

主代理完成 Vue SFC 静态解析、git diff --check、前端构建和 desktop/dev 构建；只读检查确认 Windows amd64、PE GUI 子系统 2，25 个最新前端文件全部完整嵌入，包含 Models-CCQ2XGiR.js。根目录产物 D:\proxy\kiro-proxy-dev-custom-nodes.exe，大小 34878700 字节，SHA256 为 79180FCD6E117BC1D66D29408BF72E13BFB47228E18E1314A5A680A6B158CA55。源码及旧 dev/dist 覆盖前已保存恢复副本；本轮没有运行测试、Mock、程序、实际界面或模型请求，没有修改真实配置、安装桥接、重启或提交推送。快捷添加、批量操作、外层保存和新执行路由仍由用户运行验收。

最终由未参与写入的 Sol/xhigh 只读复审者核对冻结源码与清单，未发现 P1/P2 blocker；49 项名称集合一致、身份说明的证据边界、快捷与批量草稿操作、覆盖优先级、原生弹窗静态路径均符合本节契约。该结论不代替用户的界面和实际路由验收。

## 源码交付记录（2026-10-07）

状态：前端、后端与 Kiro 桥接的源码开发已完成。三个执行者按不重叠范围实施，主代理完成协议、转发参数、日志与集成；多轮独立静态复审发现的阻塞项均已修复，最后新的只读复审者对修复后的冻结源码给出无 blocker 结论。本次交付的是当前 main 工作区源码，不代表运行程序已经更新。

| 范围 | 已完成行为 |
|---|---|
| 管理界面 | 18/20 职责布局、实际模型与固定等级、旧五槽迁移预览、明确批量填充、节点绑定/覆盖、观察与未知原因、保存后摘要刷新 |
| 配置与 API | 严格 duty_fixed v2、启用目标校验、禁用空目标草稿、legacy 只读兼容、原始旧配置首次事务备份、进程内观察接口 |
| 路由与转发 | 节点覆盖优先、明确职责选择、执行与工具续轮快照、旧资源失效报错、SDK/raw 固定有效 effort、结构化路由日志 |
| Kiro 桥接 | 明确创建声明、子 Agent 继承、可信 recipe/定义来源、定义树稳定路径、sidecar 创建与恢复、创建/冷加载/驻留 step 三入口、真实 step sessionId |
| 后续安装源码 | 精确原版与已知旧桥接 hash 校验、内存逆补丁还原、最终清单同步；本轮没有执行安装 |

独立复审重点修复了真实 iter-N 节点路径碰撞、工具续轮受无关配置失效影响、旧主控声明兼容、禁用空覆盖草稿前后端校验差异、驻留会话跳过职责恢复，以及仍有引用的三个 Go helper 被误删。修复后的 helper 与 HEAD 原实现一致，当前调用点均有定义；没有通过新增推测分类或静默换模处理这些问题。

实际执行的静态检查包括相关 Go 源码的 gofmt、JS helper/桥接/用例的 node --check、三个 Vue 文件的 SFC/script/template 解析、git diff --check、源码符号与调用链核对。最终 31 个桥接锚点逐项唯一，清单与源码一致；完整候选仅在内存作语法解析，新候选和实际旧桥接均能逆补丁还原为逐字节一致的原版。没有执行候选代码或 Mock，也没有写出 bundle 文件。Git 的 LF/CRLF 提示不代表已通过编译或类型检查。

新增或更新的回归源码覆盖配置/迁移事务、节点优先级、未知声明、执行/工具锁、固定 effort、私有头原子校验、路径碰撞、sidecar 恢复、子 Agent 与驻留 step 恢复，以及禁用空覆盖草稿的读取、编辑与重载。前端用例位于 test/auto_duty_frontend_test.mjs；桥接真实源码抽取用例位于 test/kiro_stage_bridge_test.mjs，后者需要 KIRO_STAGE_TEST_BUNDLE 指向已核实的精确原版才能覆盖相应分支。本轮只编写和审阅这些用例，不能称其测试通过。

最终关键 SHA256：

```text
router/auto_policy.go
6c461e5b87c3636408933947a67db98132cf9d72b0e1eff151b7b39054a1b638
tools/kiro_stage_bridge.mjs
89b08c68acb1e73f97e15a60d44ca46413a798ab5300c384ade7602799e15b9e
proxy/kiro_stage_bridge_manifest.json
c4d4d01fb2e5c012a1d59dcaa08074781bc1621ef96676b9b32a2fdea629d401
内存候选 bundle
8384475aad60356ba18dc0be15b75af37efcfd506987797a5dcad127540e77be
```

仍未运行验证：Go 编译与测试、UI 交互、真实数据库迁移、Kiro 创建/恢复及 sidecar、实际转发、上游缓存和费用。上述源码开发与静态复审阶段没有改变真实配置、安装版本或运行程序，没有部署、重启、提交或推送。当前分支与工作区中的既有改动继续保留，.zcodeignore 未修改。后续真实效果必须在用户运行后结合路由日志与上游账单核对，静态复审不能替代这一证据。

源码交付后，用户明确授权将本次修复提交到 GitHub，并要求中文描述。后续 Git 提交按文档、后端路由、Kiro 桥接和前端设置分段整理；该授权只增加提交和推送，不增加运行测试、编译、部署或重启的权限。
