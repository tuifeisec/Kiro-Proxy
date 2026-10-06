# Auto 节点模型设计建议（旧版，已替代）

> 本稿已由 [Auto 节点模型细分设计建议](2026-10-07-auto-node-model-granular-design.md) 替代。下面保留的是历史讨论：五组上限、调查/设计/计划合并，以及按 `wf-planner` 统一归为 plan 的建议均已撤回，不应作为后续实施依据。请以新稿的可扩展职责类型、稳定节点覆盖与明确创建声明为准。

> 日期：2026-10-07。状态：建议稿，尚未实施。本轮不修改运行配置或代码。

## 推荐设计

保留 run / plan / implement / review / finalize 五个用户可配置模型组。节点在启动前明确归属，代理按组引用一个实际模型配置。同组和同一执行内保持目标稳定。五个组可以引用相同模型，不要求五个不同模型，也不按字符阈值、提示词语义或每轮 Agent 名称动态选模。

当前代码把原始 nodeId 直接当阶段，只有五个同名值能命中。应先分开原始节点身份与选模组，再补明确归类；仅在界面选好五个模型不足以接通不同工作流。

## 首轮模型候选

| 组 | 界面含义 | 首轮建议 |
|---|---|---|
| run | 主控 / 准备 | 保留当前 DeepSeek 配置 |
| plan | 调查 / 设计 / 计划 | 保留当前 GPT 配置 |
| implement | 实施 / 综合执行 | 保留当前 DeepSeek 配置；质量不足时调整整组固定目标 |
| review | 审查 / 验证 | 保留当前 GPT 配置 |
| finalize | 收尾 / 发布 | 可与 run 共用模型，作为待比较的候选；实际成本和质量仍需确认 |

当前实际配置：run、implement 为 cn:deepseek-v4.1-flash；plan、review、finalize、default 为 gpt-6.1-sol。GPT 的界面名称是 gpt-6-astra1，不能据显示名称认定实际调用 Astra。以上是尊重现有选择的首轮候选，不是已经证明最低费用的组合。

未知节点先保留当前已配置的固定默认目标，明确显示未归类，不悄悄改成便宜模型。模型组引用具体启用配置，不引用轮询组。

## 全部 47 个执行节点的建议归类

以下是拟定约定，不是当前已实现的行为；原始证据见 [节点盘点](2026-10-06-kiro-workflow-node-inventory.md)。五组不要求每项任务依次执行五段，节点内读文件、测试、提交等动作不自动触发换模。

| 组 | 节点名 |
|---|---|
| run（主控、准备与运维） | `baseline`、`prepare`、`protect-baseline`、`restart-and-verify`、`run`、`setup` |
| plan（调查、设计与计划） | `audit-plan`、`design`、`design-draft`、`design-fix`、`implementation-plan`、`investigate`、`native-design`、`plan`、`requirements` |
| implement（实施与综合执行） | `code`、`experiment`、`implement`、`iterate`、`memory-code`、`native-code`、`native-code-verify`、`native-implementation`、`repair`、`respond`、`work` |
| review（审查、验证与裁决） | `aggregate`、`code-aggregate`、`design-file-gate`、`design-recheck`、`design-review`、`fidelity-review`、`final-audit`、`implementation-review`、`independent-review`、`logic-review`、`memory-review`、`native-review`、`native-semantic-review`、`review`、`review-fable`、`review-gpt`、`review-opus`、`validate` |
| finalize（整理、清理与发布） | `cleanup`、`finalize`、`submit` |

覆盖核对：6 + 9 + 11 + 18 + 3 = 47 个 ID，无重复、无漏项。run 为有条件条目，见下节。22 个 repeat/parallel 容器和 wait/watch 不单独绑定模型。

review 包含 validate、final-audit、design-file-gate，以及 aggregate、code-aggregate：关键验收与裁决不交给纯整理模型。finalize 只涵盖收尾、指定清理与 PR 提交；原有副作用授权仍由工作流执行层负责。

## 同名 run 的明确声明

本机 29 个合成 run 分别由 planner 7 份、coder 19 份、reviewer 3 份执行。因此普通主聊天与不同合成工作流不能仅凭 nodeId=run 混为同一职责。

| 已知创建入口 | 原始节点 | 创建时明确声明的组 |
|---|---|---|
| 普通主聊天 | 无 workflow 节点 / run | run |
| agent://wf-planner | run | plan |
| agent://wf-coder | run | implement |
| agent://semantic_reviewer | run | review |

这些是建议在已知工厂中设定的静态契约，不是根据每轮 agentMode 猜任务，也不恢复模型角色设置。其他自定义 Agent 或任意同名 run 必须明确声明或人工归类。实施前还需核对实际工厂与会话创建位置，尚未证明它们已有可直接使用的分类字段。

普通子 Agent 继承父组；进入拥有独立声明的工作流节点时按节点声明选择组。work、iterate、experiment、respond 等混合任务按整个节点的目标固定模型，不根据中途工具动作切换。
## 归属、传输与模型绑定

采用工作流定义身份与节点位置的明确绑定表。已有内置配方按已核实定义绑定；已有自定义节点人工确定归属；新节点明确声明，或显示为未归类。不能以运行标签、全局名字模糊匹配、异步 activity 或提示词猜测代替明确声明。

客户端同步传递原始 nodeId、稳定定义身份、运行 workflowId 及明确模型组。后端仅用组查五个模型目标；日志保留原始节点、归属组、归类来源、目标配置与脱敏渠道身份。阶段元数据留在私有传输中，不写进 system prompt、工具描述或缓存键。

当前私有协议 v1 严格拒绝未知字段，stage 同时承担原始节点与选模阶段。实施时须同步更新发送端与解析端或升级协议版本。Kiro 的 workflow JSON schema 扩展方式还需核实，不能直接塞入未知属性。

未知节点使用当前明确配置的固定默认目标并记录 unknown；前端列出待归类节点，不把所有未知伪装为 run。

建议以明确的节点执行实例作为绑定单位，首次选择后覆盖到该执行结束，包括多轮模型调用、工具链和必要重试。进入新节点才选择其组，配置修改对新执行生效；普通聊天以当前任务为单位，不冻结整个聊天。

当前实现仅在当前工具续轮读取旧锁，不足以宣称冻结整个节点。实施前须核实节点、循环迭代身份和结束信号。this.executionId 虽在调用 metadata 中出现，其生命周期尚未证实，不能直接据此扩大锁。发现目标资源或渠道身份改变时保留明确错误，不静默切换模型或账号，也不偷偷降低请求 effort。

## 多模型审查的取舍

普通任务使用固定 review 目标。内置 feature-pipeline 与 semantic-review-multi-model 明确声明两路不同模型；统一到一个 review 目标后变为同一模型在独立上下文中审查两次，不能继续称为两种模型交叉审查。

第一版可以保持五组统一绑定，明确披露此效果；常规任务不应仅为增加调用次数而选择多模型审查配方。若需要保留异构审查，允许这些明确配方有固定的节点目标覆盖，两路目标分别稳定，不能轮询。此例外是后续可选项，本轮未替用户启用。

## 前缀与费用

模型、渠道稳定是缓存可复用的条件之一。不同 Agent 的职责、system prompt 与工具可能不同，不能为了统一前缀删除行为契约，也不能保证同模型跨职责复用缓存。优先稳定同节点多轮请求的静态前缀、工具顺序与历史追加方式，不将节点名、循环编号写进缓存键主动分裂缓存。

先接通明确归属并保留当前两模型组合，再用实际质量和费用证据调整某一组。不要同时改模型、渠道、effort 和提示词，以免无法判断收益来自哪里。

已核对本地价格配置，单位 USD / 百万 token：

| 实际模型 | 普通输入 | 输出 | 缓存读取输入 |
|---|---:|---:|---:|
| cn:deepseek-v4.1-flash | 5 | 30 | 0.5 |
| gpt-6.1-sol | 5 | 30 | 0.5 |

两者恰好与界面 openai 预设一致，不能据此认定真实渠道价格。它们只影响本地估算，不改变实际收费或缓存率；先核对上游费率，不能仅按模型名称判断更便宜。

主要指标是完成同类任务的实际上游总费用，同时核对质量、完成率、耗时、模型轮次、输入量、缓存读取量、输出量与重试。按请求条数的命中率和按输入 token 的缓存比例分开；缓存率上升不保证总费用下降。prefix_audit 用于已经覆盖的转换通路，其他通路的等价观测还需核实。

## 建议实施顺序与本轮状态

1. 修订节点身份和明确归属契约，落实已有节点绑定和合成 run 的工厂声明。
2. 同步接通私有元数据、后端选模与原始节点/归属组日志。
3. 核实节点执行生命周期，补足执行实例内的模型和渠道绑定。
4. 前端保留五个模型选择，增加节点归属表和未归类清单；不恢复角色、字符阈值或语义分类器。
5. 由用户运行真实任务，按日志核对主流程、子 Agent、工作流、循环与未知节点，再按真实费率比较完整任务费用。

本轮仅检查既有清单、代码和安全投影后的运行配置，形成此建议稿。未修改 Auto 代码、数据库或 Kiro 安装文件，未编译、运行测试、重启、提交或推送。以上归类、协议与完整节点绑定均尚待实施。
