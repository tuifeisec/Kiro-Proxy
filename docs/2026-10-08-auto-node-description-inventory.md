# Auto 节点用途说明清单

> 状态：当前清单含 49 个名称参考与 3 个关联任务日志支持的精确身份用途摘要。本清单不参与路由，也不创建节点规则。动态节点目录另按真实日志展示工作流标题与可核对上下文，见 [节点目录契约](2026-10-08-auto-node-catalog-design.md)。

## 口径与证据边界

前 47 项来自 [2026-10-06 Kiro 工作流节点盘点](2026-10-06-kiro-workflow-node-inventory.md) 的 step 名称表，以及其机器清单 [JSON](2026-10-06-kiro-workflow-node-inventory.json)。该盘点覆盖当时读取的 53 份本机定义和 7 套已安装内置 recipe；“本机 / 内置”数字表示定义出现次数，不表示模型请求或成功执行次数。用途是对当时定义结构、任务描述和名称的摘要。`investigate-wasm` 和 `startup-check` 是后续日志中观察到的名称，旧盘点没有计入定义次数。

本清单共 49 个名称参考，其中原盘点的 47 项和新日志补入的 2 项。名称参考不是稳定节点身份。相同 `node_id` 可出现在不同工作流、定义修订和节点路径中，具体用途以完整工作流定义身份为准。名称参考不证明实际路由、模型选择或当前节点仍在运行。缺少 `node_id` 时，只有完整 `node_path` 整串与已知名称键完全相同，才可显示名称参考；不截取路径叶段。已有 `node_id` 时，不用未知或不一致的路径名替代它。新运行证据须有 `definition_id`、`definition_revision`、`node_path` 的精确身份及可核对日志来源，才能加入身份级用途表；不能从名称、路径叶段或 Agent 名称补造身份映射。请求携带的 `task_type` 只解释职责参考；现有协议不能区分其来自显式声明、内置映射或父级继承，也不代表路由实际命中。`selected_duty` 不作为节点用途依据。

旧盘点的日志窗口列在下文，截止范围仅是该次盘点的 `kiro_traffic.log` 与 `.1`，不能视为当前完整节点清单。对于任意未列出或未来出现的节点，说明保持“用途待确认”。

## 49 个名称参考项

“日志”列中的“有”表示出现在 2026-10-06 旧盘点活动表，“新日志”表示出现在 2026-10-08 取证快照；“未见”只代表旧盘点窗口未见。来源行和新快照边界见后文。

| `node_id` | 用途参考 | 本机 / 内置定义出现数 | 日志 |
|---|---|---:|---|
| `aggregate` | 合并多份审查并整理最终 issues-only 报告。 | 0 / 1 | 未见 |
| `audit-plan` | 只读调查并形成具体实施计划。 | 1 / 0 | 未见 |
| `baseline` | 只读核对既有输入与源码，建立前置基线认识。 | 1 / 0 | 未见 |
| `cleanup` | 审查通过后清理所属工作流创建的指定临时协作文件。 | 1 / 0 | 未见 |
| `code` | 按所属工作流约束直接修改目标源码。 | 7 / 0 | 未见 |
| `code-aggregate` | 合并代码审查、核对计划完成度并输出 verdict。 | 0 / 1 | 未见 |
| `design` | 建立或修订技术设计、保真规格；具体执行者以所属定义为准。 | 3 / 0 | 未见 |
| `design-draft` | 检查代码并编写或修订技术设计。 | 0 / 1 | 未见 |
| `design-file-gate` | 只读核验设计文件存在、类型与非空条件。 | 1 / 0 | 未见 |
| `design-fix` | 修订既有设计。 | 1 / 0 | 未见 |
| `design-recheck` | 独立复核设计修订。 | 1 / 0 | 未见 |
| `design-review` | 对需求与设计进行独立审查。 | 4 / 1 | 未见 |
| `experiment` | 提出并应用实验、运行 benchmark，再按结果保留或回滚。 | 0 / 1 | 未见 |
| `fidelity-review` | 独立审查实现是否满足保真要求。 | 1 / 0 | 未见 |
| `final-audit` | 只读核对交付产物和验证证据。 | 1 / 0 | 未见 |
| `finalize` | 汇总收尾情况和交付结果；可写范围取决于所属定义。 | 3 / 0 | 有 |
| `implement` | 实施计划、修改代码并执行该任务要求的验证。 | 6 / 1 | 有 |
| `implementation-plan` | 编写针对实施的计划。 | 1 / 0 | 未见 |
| `implementation-review` | 独立审查实施改动。 | 1 / 0 | 未见 |
| `independent-review` | 对真实代码增量执行独立审查。 | 1 / 0 | 未见 |
| `investigate` | 只读调查业务源码并整理证据报告；自定义范围以所属定义为准。 | 6 / 1 | 有 |
| `investigate-wasm` | WASM方向调查节点（名称参考）；具体对象和边界待工作流定义确认。 | 旧盘点未列 | 新日志 |
| `iterate` | 建立或推进 checklist，并按 recipe 处理未完成项。 | 0 / 1 | 未见 |
| `logic-review` | 独立审查实现与原有算法、行为的等价性。 | 2 / 0 | 未见 |
| `memory-code` | 直接排查并修复 memory 相关故障。 | 1 / 0 | 未见 |
| `memory-review` | 独立审查 memory 故障修复。 | 1 / 0 | 未见 |
| `native-code` | 实施 native 能力优先的源码改动。 | 1 / 0 | 未见 |
| `native-code-verify` | 实施已证明等价的原生 API 替换并记录验证。 | 1 / 0 | 未见 |
| `native-design` | 设计 native 能力优先的改造。 | 1 / 0 | 未见 |
| `native-implementation` | 实施 native 能力优先的改造。 | 1 / 0 | 未见 |
| `native-review` | 独立审查原生能力优先的实施。 | 1 / 0 | 未见 |
| `native-semantic-review` | 审查 native 数据获取和等价替换的语义。 | 1 / 0 | 未见 |
| `plan` | 调查约束、整理信息并形成实施计划；具体内容依所属定义。 | 7 / 1 | 有 |
| `prepare` | 创建产物目录并记录基线等机械准备。 | 1 / 0 | 未见 |
| `protect-baseline` | 机械保护基线并保留已有改动。 | 1 / 0 | 未见 |
| `repair` | 实施已调查并定位的问题修复。 | 1 / 0 | 未见 |
| `requirements` | 整理任务需求与约束。 | 3 / 1 | 未见 |
| `respond` | 处理 PR 反馈；PR 合并或关闭后收尾。 | 0 / 1 | 未见 |
| `restart-and-verify` | 按所属定义执行本地服务重启及运行验证；属于运维步骤。 | 1 / 0 | 有 |
| `review` | 执行独立代码或语义审查。 | 10 / 0 | 有 |
| `review-fable` | 独立代码审查，不读取另一份报告。 | 0 / 1 | 未见 |
| `review-gpt` | 独立代码审查，不读取另一份报告。 | 0 / 2 | 未见 |
| `review-opus` | 独立语义审查，不读取另一份审查文件。 | 0 / 1 | 未见 |
| `run` | 工作流或 Agent 的通用入口。`wf-planner`、`wf-coder`、`semantic_reviewer` 与 `vibe` 等上下文都可能使用；名称本身不能区分主控、规划、编码或审查职责，需按定义或明确声明确认。 | 29 / 0 | 有 |
| `setup` | 准备本次产物目录、输入或基线；具体操作依所属定义。 | 2 / 2 | 未见 |
| `startup-check` | 启动检查方向节点（名称参考）；不同工作流中的具体对象和范围，以完整定义或任务声明确认。 | 旧盘点未列 | 新日志 |
| `submit` | 发布或复用 PR 并记录 PR 身份。 | 0 / 1 | 未见 |
| `validate` | 执行验收、端到端或 build/test 检查并报告；该节点不修代码。 | 0 / 1 | 未见 |
| `work` | 按目标自主推进工作，并以具体证据核对完成状态。 | 0 / 1 | 未见 |

除 `run` 外，同名步骤在不同定义中的输入、可写范围和产物也可能不同。表格仅用于缺少更强证据时给出名称参考；其文字不成为模型路由规则。

## 旧盘点的实际日志节点

证据来源为旧盘点记录的 `kiro_traffic.log`、`kiro_traffic.log.1`。表中计数按 `messageId` 去重，缺失时按 `toolCallId`，合并 text、toolUse、toolResult 活动事件；不是模型请求数或执行次数。原盘点说明两份日志外层 JSON 共 11,376 行，非空 detail 中有 2,355 条未能完整解析；日志活动完整性受此限制。

| 工作流标签 | `node_id` | 活动 Agent | 唯一活动事件 | 来源行 |
|---|---|---|---:|---|
| `name-group-021` | `run` | `wf-coder` | 15 | `kiro_traffic.log.1:1007–1110` |
| `name-group-022` | `plan` | `wf-planner` | 186 | `kiro_traffic.log.1:2203–3154` |
| `name-group-022` | `implement` | `wf-coder` | 141 | `kiro_traffic.log.1:3173–4053` |
| `name-group-022` | `review` | `semantic_reviewer` | 91 | `kiro_traffic.log.1:4082–4609` |
| `name-group-022` | `finalize` | `wf-coder` | 50 | `kiro_traffic.log.1:4641–4920` |
| `name-group-023` | `investigate` | `wf-planner` | 232 | `kiro_traffic.log:3564–4084` |
| `name-group-024` | `run` | `wf-coder` | 29 | `kiro_traffic.log.1:639–739` |
| `name-group-025` | `run` | `wf-coder` | 30 | `kiro_traffic.log.1:27–131` |
| `name-group-026` | `run` | `wf-coder` | 51 | `kiro_traffic.log.1:260–518` |
| `name-group-027` | `run` | `wf-coder` | 21 | `kiro_traffic.log.1:835–913` |
| `name-group-028` | `restart-and-verify` | `wf-coder` | 88 | `kiro_traffic.log:1384–1847` |
| `name-group-029` | `run` | `vibe` | 175 | `kiro_traffic.log:2526–2832` |
| `name-group-029` | `run` | `wf-planner` | 4 | `kiro_traffic.log:2504–2519` |
| `name-group-030` | `run` | `wf-planner` | 206 | `kiro_traffic.log:2985–3423` |

这些是 2026-10-06 旧盘点窗口中可见的节点名，不是截至 2026-10-08 的完整运行清单；工作流标签和活动 Agent 也不能替代完整稳定身份。该旧证据中 `run` 明确横跨多个 Agent 与任务上下文，因此用途参考只解释为通用入口。

## 2026-10-08 最新日志快照

取证快照覆盖的当前日志最新时间为 `02:17:53`，归档日志 `.1` 的末条时间为 `01:39:22`；两份日志仍会增长，这些时刻只界定本次读取快照，不代表日志已封存。取证共统计 226 条 `auto-routing` 记录：`node_id` 非空 125 条、为空 101 条；非空值只有 `investigate`、`investigate-wasm`、`startup-check`。`workflow_stage=run` 的 7 条和 `workflow_stage=investigate` 的 31 条是旧兼容字段，不等于 `node_id`，不用于节点名称或身份统计。

| `node_id` / 身份 | 路由记录与来源行 | 任务证据 | 用途说明及边界 |
|---|---|---|---|
| `startup-check`；`content://862b0f8a6ce5c2a6d503a1d10a05dbf444fe1e26d0ea77c0d97b9789da33dce0` / revision `0` / path `startup-check` | 21 条；`kiro_traffic.log.1:5930–6367` | `task_type` 为空，`unknown_reason=task_type_unavailable`，选择来源为 `default`。关联活动行：`.1:5936`、`.1:6309`、`.1:6433`，内容涉及项目启动授权、启动步骤、服务健康状态和启动命令记录。 | 精确身份用途参考：“在已观察任务中准备项目启动、核对启动步骤和服务健康状态，整理可复核启动结果。”证据标签为“关联任务日志参考（未取得完整工作流定义）”。不能据此断言其他工作流中同名节点执行相同任务。 |
| `investigate-wasm`；身份 A：`content://d544cb57bd704a816bf07c592ed03e5dfbecac76dccb8436666f1b344f850655` / revision `0` / path `investigate-wasm` | 28 条；`kiro_traffic.log:54–579` | `task_type` 为空，`unknown_reason=task_type_unavailable`。关联活动行：`kiro_traffic.log:70`，并使用 `file_search`、`read_file`、`grep_search`、`execute_pwsh` 等源码工具。 | 精确身份用途参考：“在已观察任务中调查 WASM 版本相关实现与调用逻辑，整理源码分析结果。”证据标签为“关联任务日志参考（未取得完整工作流定义）”。任务目标明确要求调查 WASM；任务文字不证明实际实现存在。 |
| `investigate-wasm`；另一身份：`content://0891b432898a3eed11221d7c03ef8206a5f56f38eeecbf85ad884815b1b696d6` / revision `0` / path `investigate-wasm` | 36 条；`kiro_traffic.log.1:7008–7725` | 后续补查 `interceptor.detail.payload`：`.1:7024` 有同会话原始请求，`.1:7029` 起携带精确 workflowId 和 nodeId，标题为“WASM 配置只读调查”。 | 单独加入身份用途参考，注明只取得关联标题与会话日志，未取得完整节点定义；不复用当前 WASM 节点的用途。日志没有字面“WASM B”别名。 |
| `investigate`；`bundled://investigate` / revision `6acca01dfa75f7a301618f59736c996b481843445838bf618499487194d35fbb` / path `investigate` | 40 条，计数来自本次取证快照；本次取证未记录行区间。 | `task_type=investigation`。 | 保留请求职责参考，并由 [旧盘点的内置 recipe 定义](2026-10-06-kiro-workflow-node-inventory.md#L145) 支持“只读调查源码、输出证据报告和建议，不实施修复”。该身份未列入三个日志专用映射。 |

现有三个身份级任务日志参考（`startup-check` 与两份不同定义的 `investigate-wasm`）。另一 WASM 身份最初未取得独立任务文本，后续补查 interceptor 活动后补齐上述来源；这纠正了初轮取证范围不足，不代表其节点定义已经取得。完整稳定身份精确匹配仅限定到对应 identity，不能将同名节点的用途传播到其他定义或修订。

## 动态日志说明

静态名称表无法列举用户以后创建的所有节点。节点目录从当前流量日志及 `.1` 的 Auto 路由记录提取真实身份，从同会话、精确工作流与节点的活动提取标题。完整身份决定能否建立独立规则，名称说明缺失不再阻止添加。缺少身份的 Auto 路由记录可定位现有职责或默认目标，界面明确其作用范围。没有同会话 Auto 路由证据的活动另列为活动线索，仅展示说明和来源，不提供独立绑定或目标配置入口，也不据此断言没有模型调用。

工作流标题不是完整职责描述；关联会话原始请求节选只在展开详情中以普通文本显示，帮助用户判断节点用途，不自动分类或选模。未取得对应文本、标题或职责依据时显示用途待确认，不能凭名称猜出权限、任务完成程度或输出结果。
