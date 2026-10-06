# Auto 模型职责布局：18 个常用目标，最多细分为 20 个

> 日期：2026-10-07。状态：18/20 位布局已完成源码开发，独立静态复审无剩余阻塞项；本轮未运行测试、编译或部署。按用户最新要求，将常用职责配置位控制在 10～20 个；推荐先使用 18 个。本文更新此前五模型示例与 29 类初始职责菜单建议，节点盘点仍作为事实依据。配置、迁移、协议与执行锁以 [实施契约](2026-10-07-auto-duty-routing-implementation.md) 为准。

## 配置单位与数量

界面以固定模型目标组织配置，每个职责位选择一个完整、启用的模型配置，固定实际 model ID、有效 reasoning effort、接口、渠道及相关请求参数。每位可以绑定多个明确的职责字段或节点条目，不根据提示词、工具动作或每轮 Agent 名称推断换模。

18 个职责位可以选择 18 个不同模型，也可以让部分职责引用同一个配置。五种思考等级是目标的参数，可以分布在 10～20 个目标上，不是仅有五个路由桶。用户实际模型数尚未逐项核实，下面不为具体 model ID 宣称能力或价格排名。

同一有效目标应重复引用已有 modelConfigId，避免无意复制等价配置。新职责路由同时固定配置 UUID 与有效 reasoning effort，覆盖客户端的冲突等级；同一配置被不同职责引用但设置了不同 effort 时，仍属于不同的有效目标。

## 推荐的 18 个职责位

下面的原始节点名用于展示已盘点的覆盖范围；实际绑定使用稳定工作流定义身份与节点路径，不能把裸 nodeId 当作全局规则。职责位仍独立，多个节点引用同一目标不会合并其执行、会话或权限。

| 序号 | 职责位 | 配置键建议 | 本次已知节点 ID |
|---|---|---|---|
| 1 | 主控协调 | `orchestration` | `run` |
| 2 | 机械准备与交付整理 | `routine_support` | `prepare`、`protect-baseline`、`setup`、`design-file-gate`、`cleanup`、`finalize` |
| 3 | 调查取证 | `investigation` | `baseline`、`investigate` |
| 4 | 需求与约束 | `requirements` | `requirements` |
| 5 | 技术设计 | `design` | `design`、`design-draft`、`design-fix`、`native-design` |
| 6 | 实施计划 | `implementation_plan` | `plan`、`implementation-plan`、`audit-plan` |
| 7 | 代码实施与修复 | `implementation` | `code`、`implement`、`repair`、`memory-code`、`native-code`、`native-code-verify`、`native-implementation` |
| 8 | 持续目标与清单执行 | `iterative_execution` | `work`、`iterate` |
| 9 | 实验研究 | `experiment` | `experiment` |
| 10 | 设计审查 | `design_review` | `design-review`、`design-recheck` |
| 11 | 代码审查 | `code_review` | `review`、`review-fable`、`review-gpt`、`review-opus`、`implementation-review`、`independent-review` |
| 12 | 专项语义审查 | `semantic_review` | `fidelity-review`、`logic-review`、`memory-review`、`native-review`、`native-semantic-review` |
| 13 | 审查问题汇总 | `review_synthesis` | `aggregate` |
| 14 | 审查裁决 | `review_verdict` | `code-aggregate` |
| 15 | 实际验证 | `validation` | `validate` |
| 16 | 交付稽核 | `delivery_audit` | `final-audit` |
| 17 | 运行环境运维 | `operations` | `restart-and-verify` |
| 18 | PR 提交与反馈处理 | `pr_lifecycle` | `submit`、`respond` |

覆盖核对：47 个不同 step ID 各列一次；不是 47 个节点实例或全部未来节点。repeat、parallel、sequence、watch 不独立选模。事实来源见 [节点盘点](2026-10-06-kiro-workflow-node-inventory.md) 与 [机器清单](2026-10-06-kiro-workflow-node-inventory.json)。

## 归组依据与重要边界

- 主控协调消费任务目标、约束、当前状态并做分工决策。普通主聊天应由明确创建入口声明该职责；表中 run 仅有条件归入主控。合成 run 若实际负责调查、实现或审查，应绑定对应职责位。
- 机械准备与交付整理按确定步骤操作或组织已验证结果，不承担最终正确性裁决。prepare/setup/protect-baseline/design-file-gate/cleanup/finalize 共用目标是能力需求上的起始候选，日志尚未证明它们具有相同前缀。具体 finalize 若还承担实质稽核，必须按该定义重绑交付稽核目标。
- 调查、需求、设计、计划分别保留配置位。它们消费的业务背景、约束和证据关联紧密，允许共用一个能力合适的目标，但可独立选择不同模型。audit-plan 仍是调查与计划的混合节点，整节点按其明确目标绑定；不能声称内部已能分别选模。
- 代码实施与修复消费源码、明确方案、改动和验证反馈。专业 memory/native 节点可先共用这一目标；模型能力或上下文要求确实不同的定义可以覆盖，也可以重新分配已有职责位。
- work/iterate 是多轮目标或清单执行，常包含实施、验证与状态推进；experiment 还需要提出假设、benchmark 比较及保留/回滚决策，因此单独配置。单个混合 step 内保持固定目标，需要不同模型时必须先真实拆分节点。
- 设计审查、代码审查和专项语义审查分别消费设计/需求、真实改动、保真或等价性证据。专项审查的几个 ID 也不一定具有相同输入前缀；本布局按主要能力需求复用，特殊定义允许固定覆盖。
- aggregate 仅合并审查问题，code-aggregate 还做完成度与 verdict 裁决，final-audit 核对实际交付产物与验证证据。三者分别配置，避免把整理能力等同于裁决或稽核能力。
- validate 按定义执行实际验收、测试和构建；restart-and-verify 处理运行环境重启和运维核验，分别配置。模型选择不扩大执行权限与副作用授权。
- submit 与 respond 共用 PR 生命周期目标是初始取舍：前者提交/复用 PR，后者处理反馈，可能包括代码修改与验证。该目标必须满足反馈修复的能力要求，不能将 respond 当作纯回复撰写降档。两者是否共享上下文前缀尚未证实。
- 多模型审查的 A/B 分支保留节点级固定覆盖。覆盖按 recipe 与稳定节点身份区分；配置的实际不同模型应如实显示。普通子 Agent 继承父有效目标，进入独立声明的新节点才重新选择。

## 使用 20 个目标时的两处拆分

在上述 18 位基础上增加两个配置位，不新增自动分类层：

1. 将机械准备与交付整理拆为“机械准备”和“交付整理”。前者包含 prepare/protect-baseline/setup/design-file-gate/cleanup；后者为 finalize。可分别选择擅长可靠工具操作和长上下文结果整理的模型。
2. 将 PR 提交与反馈处理拆为“PR 提交”和“PR 反馈处理”。submit 可选择满足提交与材料组织要求的目标；respond 固定使用能完成反馈分析、代码修复和验证的目标。

这样得到 20 位。若实际只有 10～15 个合适模型，可保留 18 个可理解职责，让部分位明确引用同一个目标；职责配置数量仍在用户要求范围内。初始建议不要求每个思考等级都被使用，也不要求每个职责使用不同基础模型。

## 上下文协调与缓存

判断是否适合共用目标，应分别检查能力要求和请求上下文：

- 能力方面看具体定义的成功标准、因果分析、代码改动、审查独立性和裁决要求；思考等级名称不是通用能力排名，也不扩大上下文窗口。
- 上下文方面看模型实际可见的稳定提示前缀、工具定义及顺序、项目证据、历史追加方式与会话/分支身份。相同 Agent 名称、节点名称或工具集合不足以证明缓存可以复用。
- 交接时持续传递目标、约束、已验证事实、设计决策、未解决项与必要产物；模型没有跨节点共享的隐式记忆。独立审查保持独立会话和证据边界。
- 同一目标不会自动合并会话。现有 Responses 续链范围包含配置 ID、有效 effort、渠道、静态提示/工具及会话分支，切换范围后不能复用相同 previous_response_id；这是续链约束，不能等同于上游缓存必然失效。
- 私有路由元数据不加入提示词；不主动把职责名、节点名、迭代编号或每次执行 ID 加入缓存键分裂缓存。

本轮沿用已完成的两份流量日志取证：共 588 条 prefix_audit，其中 546 条 input_relation=append；可精确关联到 incoming conversation ID 的 170 条均记录 agentMode=vibe，其系统与工具指纹并非处处相同。审计会话与带 nodeId 的 activity session/execution 未建立可靠 ID 链，因此无法据日志证明某两个职责共享前缀或缓存。prefix_audit.model 是 statsModelID 显示标签，不应视为已核实的实际上游 model ID。kiro-proxy.log 当前为空。

缓存与费用的收益必须按上游实际账单、完整同类任务质量、完成率、耗时和重试评估。本建议是模型配置布局，尚未做 18～20 个实际模型的能力或费用比较。

## 当前源码状态与交付边界

管理界面已提供 standard 的 18 个职责与 expanded 的 20 个职责，每位独立选择启用的实际模型与固定等级。后端使用 version=2、mode=duty_fixed；按稳定节点覆盖、节点职责绑定、明确 taskType、固定默认目标的顺序选择。已观察节点和未归类原因可在界面查看，稳定身份不完整时不能创建长期绑定。

Kiro 桥接源码已贯通普通主会话、子 Agent、工作流 step、SDK 与 raw 请求；普通子 Agent 继承父 Auto 路由，明确声明独立职责时重新选模。内置 recipe 只按精确内容身份与定义路径使用职责表，其它节点保留 unknown，交由明确声明或节点绑定处理。定义路径按真实树结构校验，不能把名字相似的节点自动归类。

新职责路由在标准转发与 Raw Responses 中固定有效 effort，包括空值代表的上游默认；保留 Raw Responses 的其它 reasoning 字段。同一 execution 保持首次目标快照，无 executionId 的工具续轮在没有明确新节点/职责边界时沿用快照；不相关职责配置修改不应中断旧链。旧目标本身停用、删除或改变渠道身份时明确报错。

旧 stage_fixed 仅保留读取和运行兼容。界面先显示五槽迁移预览，用户保存时才迁移并由服务端事务备份原始配置；调查、需求、设计等新职责不会由旧 plan 静默填满。第五个“超级高”等级仍以实际 provider 参数表达能力为准，不能仅凭名称宣称所有上游都支持。

源码开发与静态复审阶段只修改源码、文档与 Mock 用例，没有运行测试或 Mock，没有编译、生成 bundle 文件、部署、重启、提交或推送，没有改变真实数据库、Kiro 安装文件或真实 sidecar。随后用户已另行授权中文 GitHub 提交，测试、编译和部署限制仍保留。当前运行程序仍是现有版本；真实 Kiro 流程、缓存命中率及费用收益尚未运行验证。最终检查记录见 [实施契约](2026-10-07-auto-duty-routing-implementation.md)。

## 新界面的配置顺序

1. 先选择标准 18 位或细分 20 位布局，再选择启用的实际模型作为固定默认目标。默认目标的有效等级取该模型配置；具体职责与节点覆盖的等级各自明确配置，空值代表上游默认。
2. 若读取的是旧五槽配置，先查看迁移预览，再补齐新职责。启用前所有当前布局职责都须引用有效模型。只有用户点击“用默认目标填充未配置职责”才批量填写，之后仍可逐项调整。
3. 按能力和上下文要求分配职责。同一模型适合多个职责时直接重复引用已有配置 UUID；需要不同等级时在相应职责位明确选择，避免仅通过模型名称区分。
4. 查看已观察节点及未归类原因。完整稳定身份可绑定到已有职责；特殊审查分支可直接设置节点模型覆盖。缺少定义身份、修订或路径时，先补创建声明或桥接来源，再建立长期规则。
5. 保存后查看 Auto 行的职责数量、实际模型和等级摘要。已有执行继续使用首次快照，新执行读取保存后的配置。未来真实验证应使用完整同类任务，再按上游缓存与账单、质量、重试和耗时比较收益。
