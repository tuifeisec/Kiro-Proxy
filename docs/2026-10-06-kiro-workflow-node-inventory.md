# Kiro 工作流节点盘点

> 采集时间：2026-10-06 23:58（Asia/Shanghai）。本次只收集定义与运行证据，尚未裁决 Auto 分类，也没有修改模型配置。

## 范围与统计

清单覆盖当前已安装的 Kiro 1.1.294 内置 recipe、本机保存的当前工作流定义，以及 `kiro_traffic.log` 和 `kiro_traffic.log.1` 的运行证据。生成工作流允许自定义节点名称，规划节点还可调用 `update_workflow` 改写剩余流程。因此，下面是本次证据范围内的全量清单，不代表所有未来节点，也不包含已被覆盖的历史定义版本。

| 来源 | 数量 | 说明 |
|---|---:|---|
| 本机保存的工作流定义 | 53 份 | 本机会话中的匿名工作流定义，全部 JSON 可解析 |
| 本机定义中的显式节点 | 127 条 / 49 个不同 ID | 105 条 step、22 条 repeat；递归计入嵌套节点 |
| 当前内置 recipe | 7 套 | 30 条显式节点 / 28 个不同 ID |
| 合并去重后的节点名 | **70 个** | **47 个 step、22 个 repeat/parallel、1 个 watch** |
| 日志实际活动可见的节点名 | 7 个 | `run`、`restart-and-verify`、`plan`、`implement`、`review`、`finalize`、`investigate` |

机器清单见 [2026-10-06-kiro-workflow-node-inventory.json](2026-10-06-kiro-workflow-node-inventory.json)，保留每份定义的结构路径、行号、类型、Agent、父节点及状态证据；本机定义的 ID、标题、来源文件已匿名化，`createdAt` 置为 `null`（已去除精确创建时间）；不保存原始业务源码、完整任务提示或凭据。

## 读取清单时的口径

- `step` 是由 Agent 执行的任务节点；其职责以所属工作流的任务定义为准。
- `repeat` 和 `parallel` 是循环、并行容器，调度子节点，本身不作为一次模型任务。
- `watch` 使用专用 handler；内置 `wait` 监视 GitHub PR 状态，不调用模型。
- `sequence` 是顺序容器。本机执行状态还包含以 `workflowId` 为标识的隐式根；它不是固定阶段名，也不加入下面 70 个显式 ID。
- “本机记录数”和“内置记录数”是节点在定义中出现的次数，不是模型调用、工具调用或循环次数。
- 日志中的 `workflowName` 可能是运行标签；用 `workflowId` 与定义对应，不能只根据显示名称合并工作流。
- 表中的用途是对定义提示与结构的归纳，不是 Auto 阶段映射。

## 全部 70 个节点名

| nodeId | 类型 | 定义中的 Agent | 本机 / 内置记录数 | 日志实际出现 | 已核对用途 |
|---|---|---|---:|---|---|
| `adaptive-htf-loop` | repeat | — | 1 / 0 | 未见 | 循环调度子节点：code、review。 |
| `aggregate` | step | `wf-coder` | 0 / 1 | 未见 | 合并两份审查为最终 issues-only 报告。 |
| `audit-plan` | step | `wf-planner` | 1 / 0 | 未见 | 只读调查并形成具体实施计划。 |
| `baseline` | step | `wf-coder` | 1 / 0 | 未见 | 只读核对既有输入与源码，建立前置基线认识。 |
| `build-loop` | repeat | — | 1 / 0 | 未见 | 循环调度子节点：code、review。 |
| `cleanup` | step | `wf-coder` | 1 / 0 | 未见 | 审查通过后清理该工作流创建的指定临时协作文件。 |
| `code` | step | `wf-coder` | 7 / 0 | 未见 | 直接修改目标源码，按该工作流的约束实施。 |
| `code-aggregate` | step | `wf-review-aggregator` | 0 / 1 | 未见 | 合并两份审查，核对计划完成度并输出 verdict。 |
| `code-loop` | repeat | — | 0 / 1 | 未见 | 实现与代码审查循环，最多 3 次。 |
| `code-reviews` | parallel | — | 0 / 1 | 未见 | 并行运行两份相互独立的代码审查。 |
| `compact-review-loop` | repeat | — | 1 / 0 | 未见 | 循环调度子节点：implement、review。 |
| `conversion-loop` | repeat | — | 1 / 0 | 未见 | 循环调度子节点：code、review。 |
| `design` | step | `wf-design`, `wf-planner` | 3 / 0 | 未见 | 建立或修订技术设计、保真规格；本地定义可由 designer 或 planner 执行。 |
| `design-draft` | step | `wf-design` | 0 / 1 | 未见 | 检查代码并编写、修订技术设计。 |
| `design-file-gate` | step | `wf-coder` | 1 / 0 | 未见 | 只读核验设计文件存在、类型与非空条件。 |
| `design-fix` | step | `wf-design` | 1 / 0 | 未见 | 修订既有设计。 |
| `design-loop` | repeat | — | 4 / 1 | 未见 | 设计与设计审查循环，最多 3 次。 |
| `design-recheck` | step | `wf-design-reviewer` | 1 / 0 | 未见 | 独立复核设计修订。 |
| `design-review` | step | `wf-design-reviewer` | 4 / 1 | 未见 | 对需求与设计进行独立审查。 |
| `equivalence-review-loop` | repeat | — | 1 / 0 | 未见 | 循环调度子节点：native-code-verify、native-semantic-review。 |
| `experiment` | step | `wf-auto-researcher` | 0 / 1 | 未见 | 提出并应用实验，运行 benchmark，保留更优结果或回滚实验。 |
| `experiments` | repeat | — | 0 / 1 | 未见 | 重复实验，最多 1000 次，超限暂停。 |
| `fidelity-review` | step | `semantic_reviewer` | 1 / 0 | 未见 | 独立审查实现是否满足保真要求。 |
| `final-audit` | step | `wf-coder` | 1 / 0 | 未见 | 只读交付稽核，核对产物与验证证据。 |
| `finalize` | step | `wf-coder` | 3 / 0 | 是 | 收尾核对与交付报告；具体可写范围依工作流定义。 |
| `fix-review-loop` | repeat | — | 2 / 0 | 未见 | 循环调度子节点：implement、review、code。 |
| `goal-loop` | repeat | — | 0 / 1 | 未见 | 推进目标直到 success，最多 200 次。 |
| `hotspot-loop` | repeat | — | 1 / 0 | 未见 | 循环调度子节点：code、review。 |
| `implement` | step | `wf-coder` | 6 / 1 | 是 | 实施计划、修改代码及执行该任务要求的验证。 |
| `implement-review-loop` | repeat | — | 1 / 0 | 未见 | 循环调度子节点：native-implementation、implementation-review。 |
| `implementation-loop` | repeat | — | 4 / 0 | 未见 | 循环调度子节点：implement、independent-review、fidelity-review、logic-review。 |
| `implementation-plan` | step | `wf-planner` | 1 / 0 | 未见 | 编写针对实施的计划。 |
| `implementation-review` | step | `semantic_reviewer` | 1 / 0 | 未见 | 独立审查实施改动。 |
| `independent-review` | step | `semantic_reviewer` | 1 / 0 | 未见 | 对真实增量执行独立审查。 |
| `investigate` | step | `wf-planner` | 6 / 1 | 是 | 只读调查业务源码并写证据报告；自定义调查节点另看其定义。 |
| `iterate` | step | `wf-coder` | 0 / 1 | 未见 | 建立或推进 checklist，实施未完成项、测试和提交。 |
| `logic-review` | step | `semantic_reviewer` | 2 / 0 | 未见 | 独立审查实现与原有算法、行为的等价性。 |
| `memory-code` | step | `wf-coder` | 1 / 0 | 未见 | 直接排查并修复内存相关故障。 |
| `memory-fix-loop` | repeat | — | 1 / 0 | 未见 | 循环调度子节点：memory-code、memory-review。 |
| `memory-review` | step | `semantic_reviewer` | 1 / 0 | 未见 | 独立审查内存故障修复。 |
| `native-code` | step | `wf-coder` | 1 / 0 | 未见 | 直接实施原生能力优先的源码改动。 |
| `native-code-verify` | step | `wf-coder` | 1 / 0 | 未见 | 实施已证明等价的原生 API 替换，并记录验证。 |
| `native-design` | step | `wf-design` | 1 / 0 | 未见 | 设计原生能力优先的改造。 |
| `native-implementation` | step | `wf-coder` | 1 / 0 | 未见 | 实施原生能力优先的改造。 |
| `native-implementation-loop` | repeat | — | 1 / 0 | 未见 | 循环调度子节点：native-code、native-review。 |
| `native-review` | step | `semantic_reviewer` | 1 / 0 | 未见 | 独立审查原生能力优先的实施。 |
| `native-semantic-review` | step | `semantic_reviewer` | 1 / 0 | 未见 | 独立审查原生数据获取与等价替换。 |
| `pine-fix-loop` | repeat | — | 1 / 0 | 未见 | 循环调度子节点：code、review。 |
| `plan` | step | `wf-planner` | 7 / 1 | 是 | 调查、整理约束并形成实施计划。 |
| `pr-loop` | repeat | — | 0 / 1 | 未见 | 等待并处理 PR 反馈，最多 200 次。 |
| `prepare` | step | `wf-coder` | 1 / 0 | 未见 | 创建产物目录并记录基线等机械准备。 |
| `protect-baseline` | step | `wf-coder` | 1 / 0 | 未见 | 机械保护基线，保留已有改动。 |
| `ralph-loop` | repeat | — | 0 / 1 | 未见 | 按 checklist 迭代直到 complete=true，最多 200 次。 |
| `repair` | step | `wf-coder` | 1 / 0 | 未见 | 实施已经调查的问题修复。 |
| `repair-review-loop` | repeat | — | 2 / 0 | 未见 | 循环调度子节点：repair、review、code。 |
| `requirements` | step | `wf-design` | 3 / 1 | 未见 | 整理需求与约束。 |
| `respond` | step | `wf-pr-responder` | 0 / 1 | 未见 | 处理 PR 反馈；PR 合并或关闭后收尾。 |
| `restart-and-verify` | step | `wf-coder` | 1 / 0 | 是 | 执行已授权的本地服务重启与运行验证，属于运维任务。 |
| `review` | step | `semantic_reviewer` | 10 / 0 | 是 | 独立代码或语义审查。 |
| `review-fable` | step | `semantic_reviewer` | 0 / 1 | 未见 | 独立代码审查，不读取另一份报告。 |
| `review-gpt` | step | `semantic_reviewer` | 0 / 2 | 未见 | 独立代码审查，不读取另一份报告。 |
| `review-opus` | step | `semantic_reviewer` | 0 / 1 | 未见 | 独立语义审查，不读取另一审查文件。 |
| `reviews` | parallel | — | 0 / 1 | 未见 | 并行运行两份独立审查。 |
| `run` | step | `wf-planner`, `wf-coder`, `semantic_reviewer` | 29 / 0 | 是 | agent:// 合成单步任务的通用节点；本机既有 planner、coder，也有 reviewer，不能单凭名称确定职责。 |
| `setup` | step | `wf-coder` | 2 / 2 | 未见 | 准备本次产物目录与既有输入、基线。 |
| `submit` | step | `wf-pr-submitter` | 0 / 1 | 未见 | 发布或复用 PR 并记录 PR 身份。 |
| `validate` | step | `wf-coder` | 0 / 1 | 未见 | 执行验收检查、端到端检查及 build/test；只报告，不修代码。 |
| `validate-gate` | repeat | — | 0 / 1 | 未见 | 最多一次验证；verdict 非 PASS 时中止。 |
| `wait` | watch | — | 0 / 1 | 未见 | github-pr handler 轮询 PR 状态，不调用模型。 |
| `work` | step | `wf-coder` | 0 / 1 | 未见 | 按任务自主推进，凭具体证据确认目标完成。 |

## 内置工作流的完整结构

以下为 recipe 原始定义。固定模型与 effort 是 recipe 声明值，不代表这些尚未运行的节点已经按它们出站。源码为当前安装的 `extension.js`，SHA256 为 `7639b043c1838c9e226d0c0aeee8f289c444f0f3b4e89c856af279d01df2f1cb`；注册表位于字符偏移 5985485。JSON 清单保留每个节点的字符偏移，30 个偏移均已读回核对 ID。

### `bundled://autoresearch`

| 节点 | 类型 | 父节点 | Agent | recipe 固定模型 / effort | 职责 |
|---|---|---|---|---|---|
| `experiments` | repeat | 根 | — | 未指定 | 重复实验，最多 1000 次，超限暂停。 |
| `experiment` | step | `experiments` | `wf-auto-researcher` | 未指定 | 提出并应用实验，运行 benchmark，保留更优结果或回滚实验。 |

### `bundled://feature-pipeline`

| 节点 | 类型 | 父节点 | Agent | recipe 固定模型 / effort | 职责 |
|---|---|---|---|---|---|
| `setup` | step | 根 | `wf-coder` | 未指定 / low | 创建本次运行的产物目录。 |
| `requirements` | step | 根 | `wf-design` | 未指定 | 编写需求文档，不做设计或实现。 |
| `design-loop` | repeat | 根 | — | 未指定 | 设计与设计审查循环，最多 3 次。 |
| `design-draft` | step | `design-loop` | `wf-design` | 未指定 | 检查代码并编写、修订技术设计。 |
| `design-review` | step | `design-loop` | `wf-design-reviewer` | 未指定 | 对照需求和真实代码审查设计，输出 verdict。 |
| `plan` | step | 根 | `wf-planner` | 未指定 | 基于获批设计编写有序实施计划。 |
| `code-loop` | repeat | 根 | — | 未指定 | 实现与代码审查循环，最多 3 次。 |
| `implement` | step | `code-loop` | `wf-coder` | 未指定 | 执行计划、相关测试、更新完成情况并提交。 |
| `code-reviews` | parallel | `code-loop` | — | 未指定 | 并行运行两份相互独立的代码审查。 |
| `review-fable` | step | `code-reviews` | `semantic_reviewer` | `claude-fable-5` / xhigh | 独立代码审查，不读取另一份报告。 |
| `review-gpt` | step | `code-reviews` | `semantic_reviewer` | `gpt-5.6-sol` / xhigh | 独立代码审查，不读取另一份报告。 |
| `code-aggregate` | step | `code-loop` | `wf-review-aggregator` | 未指定 | 合并两份审查，核对计划完成度并输出 verdict。 |
| `validate-gate` | repeat | 根 | — | 未指定 | 最多一次验证；verdict 非 PASS 时中止。 |
| `validate` | step | `validate-gate` | `wf-coder` | 未指定 | 执行验收检查、端到端检查及 build/test；只报告，不修代码。 |

### `bundled://goal`

| 节点 | 类型 | 父节点 | Agent | recipe 固定模型 / effort | 职责 |
|---|---|---|---|---|---|
| `goal-loop` | repeat | 根 | — | 未指定 | 推进目标直到 success，最多 200 次。 |
| `work` | step | `goal-loop` | `wf-coder` | 未指定 | 按任务自主推进，凭具体证据确认目标完成。 |

### `bundled://investigate`

| 节点 | 类型 | 父节点 | Agent | recipe 固定模型 / effort | 职责 |
|---|---|---|---|---|---|
| `investigate` | step | 根 | `wf-planner` | 未指定 | 只读调查源码，输出证据报告和建议，不实施修复。 |

### `bundled://publish-pr`

| 节点 | 类型 | 父节点 | Agent | recipe 固定模型 / effort | 职责 |
|---|---|---|---|---|---|
| `submit` | step | 根 | `wf-pr-submitter` | 未指定 | 发布或复用 PR 并记录 PR 身份。 |
| `pr-loop` | repeat | 根 | — | 未指定 | 等待并处理 PR 反馈，最多 200 次。 |
| `wait` | watch | `pr-loop` | — | 未指定 | github-pr handler 轮询 PR 状态，不调用模型。 |
| `respond` | step | `pr-loop` | `wf-pr-responder` | 未指定 | 处理 PR 反馈；PR 合并或关闭后收尾。 |

### `bundled://ralph`

| 节点 | 类型 | 父节点 | Agent | recipe 固定模型 / effort | 职责 |
|---|---|---|---|---|---|
| `ralph-loop` | repeat | 根 | — | 未指定 | 按 checklist 迭代直到 complete=true，最多 200 次。 |
| `iterate` | step | `ralph-loop` | `wf-coder` | 未指定 | 建立或推进 checklist，实施未完成项、测试和提交。 |

### `bundled://semantic-review-multi-model`

| 节点 | 类型 | 父节点 | Agent | recipe 固定模型 / effort | 职责 |
|---|---|---|---|---|---|
| `setup` | step | 根 | `wf-coder` | 未指定 / low | 创建本次审查报告目录。 |
| `reviews` | parallel | 根 | — | 未指定 | 并行运行两份独立审查。 |
| `review-opus` | step | `reviews` | `semantic_reviewer` | `claude-opus-5` / xhigh | 独立语义审查，不读取另一审查文件。 |
| `review-gpt` | step | `reviews` | `semantic_reviewer` | `gpt-5.6-sol` / xhigh | 独立语义审查，不读取另一审查文件。 |
| `aggregate` | step | 根 | `wf-coder` | `claude-opus-5` / xhigh | 合并两份审查为最终 issues-only 报告。 |

## 本机保存工作流的结构

将同名且结构相同的定义合并展示，具体 53 份定义的路径与节点位置仍逐份保留在 JSON。括号表示容器的子节点，箭头表示定义中的顺序；不表示所有节点已经成功执行。

| 定义名称 | 保存份数 | 结构 |
|---|---:|---|
| `investigate` | 5 | `investigate` |
| `name-group-001` | 1 | `restart-and-verify` |
| `agent-wf-planner` | 7 | `run` |
| `name-group-002` | 1 | `plan` → `fix-review-loop`(`implement` → `review`) → `finalize` |
| `name-group-003` | 1 | `fix-review-loop`(`code` → `review`) |
| `name-group-004` | 1 | `conversion-loop`(`code` → `review`) |
| `agent-wf-coder` | 19 | `run` |
| `name-group-005` | 1 | `memory-fix-loop`(`memory-code` → `memory-review`) |
| `name-group-006` | 1 | `audit-plan` → `equivalence-review-loop`(`native-code-verify` → `native-semantic-review`) |
| `name-group-007` | 1 | `setup` → `design-fix` → `design-recheck` → `plan` → `implementation-loop`(`implement` → `independent-review`) → `finalize` |
| `name-group-008` | 1 | `investigate` → `repair-review-loop`(`repair` → `review`) |
| `name-group-009` | 1 | `prepare` → `requirements` → `design-loop`(`design` → `design-review`) → `plan` → `implementation-loop`(`implement` → `fidelity-review`) → `finalize` |
| `name-group-010` | 1 | `plan` → `build-loop`(`code` → `review`) |
| `name-group-011` | 1 | `pine-fix-loop`(`code` → `review`) |
| `name-group-012` | 1 | `baseline` → `design-loop`(`design` → `design-file-gate` → `design-review`) → `plan` → `implementation-loop`(`implement` → `logic-review`) → `final-audit` |
| `agent-semantic_reviewer` | 3 | `run` |
| `name-group-014` | 1 | `setup` → `requirements` → `design-loop`(`design` → `design-review`) → `plan` → `implementation-loop`(`implement` → `logic-review`) |
| `name-group-015` | 1 | `plan` → `compact-review-loop`(`implement` → `review`) → `cleanup` |
| `name-group-016` | 1 | `adaptive-htf-loop`(`code` → `review`) |
| `name-group-017` | 1 | `protect-baseline` → `requirements` → `design-loop`(`native-design` → `design-review`) → `implementation-plan` → `implement-review-loop`(`native-implementation` → `implementation-review`) |
| `name-group-018` | 1 | `hotspot-loop`(`code` → `review`) |
| `name-group-019` | 1 | `native-implementation-loop`(`native-code` → `native-review`) |
| `name-group-020` | 1 | `repair-review-loop`(`code` → `review`) |

## 日志中的真实活动

两份日志外层 JSON 共 11,376 行，全部可解析；其中 3,614 条 detail 非空，1,259 条能还原为完整 JSON，2,355 条未能完整解析（字符串未闭合 1,161、属性前缀解析类 1,178、其他 16）。这些错误不一概归因为截断。另对 18 条失败的真实 `/agents/activity` 详情恢复了 147 次重复 workflow 元数据观察，未发现额外节点名；不把这些重复观察加入事件次数。

以下事件按 messageId 去重，缺失时用 toolCallId；text/toolUse/toolResult 合计，**不是模型请求数或失败重试次数**。无法完整解析的 detail 仍构成日志活动完整性限制。

| 日志 workflowName | nodeId | 活动 Agent / agentMode | 唯一事件数 | 来源行 |
|---|---|---|---:|---|
| `name-group-021` | `run` | `wf-coder` | 15 | kiro_traffic.log.1:1007–1110 |
| `name-group-022` | `plan` | `wf-planner` | 186 | kiro_traffic.log.1:2203–3154 |
| `name-group-022` | `implement` | `wf-coder` | 141 | kiro_traffic.log.1:3173–4053 |
| `name-group-022` | `review` | `semantic_reviewer` | 91 | kiro_traffic.log.1:4082–4609 |
| `name-group-022` | `finalize` | `wf-coder` | 50 | kiro_traffic.log.1:4641–4920 |
| `name-group-023` | `investigate` | `wf-planner` | 232 | kiro_traffic.log:3564–4084 |
| `name-group-024` | `run` | `wf-coder` | 29 | kiro_traffic.log.1:639–739 |
| `name-group-025` | `run` | `wf-coder` | 30 | kiro_traffic.log.1:27–131 |
| `name-group-026` | `run` | `wf-coder` | 51 | kiro_traffic.log.1:260–518 |
| `name-group-027` | `run` | `wf-coder` | 21 | kiro_traffic.log.1:835–913 |
| `name-group-028` | `restart-and-verify` | `wf-coder` | 88 | kiro_traffic.log:1384–1847 |
| `name-group-029` | `run` | `vibe` | 175 | kiro_traffic.log:2526–2832 |
| `name-group-029` | `run` | `wf-planner` | 4 | kiro_traffic.log:2504–2519 |
| `name-group-030` | `run` | `wf-planner` | 206 | kiro_traffic.log:2985–3423 |

日志侧的 7 个节点 ID 都在本机定义的 49 个 ID 中。Auto 路由记录另有 recognized/run 7 次、unknown/investigate 31 次及 stage_unavailable 30 次；这组计数独立于上面的活动事件。`kiro-proxy.log` 当前为空。

## 会影响后续 Auto 设计的事实

1. **`run` 不是单一职责。** 29 份 `agent://` 合成工作流均使用 `run`：7 份为 `wf-planner`，19 份为 `wf-coder`，3 份为 `semantic_reviewer`。普通主聊天的桥接入口也标记 run。把所有 run 当成同一工作类型会混合调查、实现、审查和主流程编排。
2. **节点名和 Agent 都不能独自证明阶段。** `wf-coder` 不仅实现代码，也执行 setup、design-file-gate、validate、final-audit、finalize、aggregate 等准备、核验、汇总任务；本机 design 还由不同 Agent 执行。这里记录 Agent 是取证，不代表恢复前端模型角色配置。
3. **同一节点可能包含多类工作。** goal/work、ralph/iterate、autoresearch/experiment、publish-pr/respond 的单步任务可以涵盖分析、实施、检查或反馈处理。仅匹配名称不会让它们自动经过五个阶段。
4. **多模型审查有独立的契约。** feature-pipeline 明确设置 review-fable 与 review-gpt 不同模型；semantic-review-multi-model 明确设置 review-opus 与 review-gpt 不同模型。Auto 控制域会优先保留 Auto；若以后把两者统一归入单一 review 目标，需要决定是否接受失去原 recipe 的模型多样性。
5. **现有五阶段只按同名值匹配。** 桥接当前将 nodeId 原样归一化送达；后端只识别 run/plan/implement/review/finalize，其他名走默认目标。因此不仅 investigate，code/native-code/design-review/validate 等也需要讨论明确归类。
6. **当前清单不是永久枚举。** workflow schema 的 id 为字符串，支持动态生成与重构。后续方案必须考虑新节点如何获得明确分类，不能只补当前这些名字就宣称全部未来工作流接通。

## 验证和未解决项

- 已读取并解析全部 53 份本机保存定义，递归展开嵌套节点；读取对应状态区分定义存在与实际启动。
- 已核对全部 7 个内置 recipe 注册项与 30 个节点的源码字符位置；合并 49 与 28 个 ID，交集 7 个，得到 70 个。
- 已解析两份日志外层结构、提取实际 workflow 活动；对失败活动详情做有界元数据恢复，排除 schema、提示词示例和纯名称提及。
- 在用户 `.kiro` 和已确认工作区的 `.kiro/workflows` 目录范围内未发现额外独立工作流文件；未访问云端 registry 或读取无关业务源码。
- 本轮没有修改 Auto 代码、模型数据库、Kiro 安装文件，也没有编译、运行测试、重启、提交或推送。
- 尚未决定节点与 Auto 配置组的映射、通用 run 的区分契约，以及多模型审查与单阶段固定模型的取舍；缓存率与实际费用也未验收。
