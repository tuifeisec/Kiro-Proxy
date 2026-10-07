const makeDutyDescription = (purpose, inputs, outputs, modelFocus) => ({
  purpose,
  inputs,
  outputs,
  modelFocus,
})

// Each layout has its own entry, including the four duties introduced by expanded.
export const AUTO_DUTY_DESCRIPTIONS = {
  orchestration: makeDutyDescription(
    '承接主任务，理解约束，安排工作流或子任务，并汇总各步骤结果。',
    ['用户目标与限制', '当前对话和可用工具', '各步骤返回的结果'],
    ['任务拆分与执行顺序', '工作流推进和子任务协调', '面向用户的最终答复'],
    ['长上下文理解', '任务分解与计划调整', '工具编排和结果整合'],
  ),
  routine_support: makeDutyDescription(
    '处理任务周边的准备、环境整理和交付收尾；标准布局将这两类工作合并为一个职责。',
    ['任务要求与操作边界', '项目或运行环境状态', '已有产物和执行记录'],
    ['可继续工作的准备状态', '整理后的交付材料和状态说明'],
    ['按清单执行', '识别操作边界', '准确记录状态变化'],
  ),
  preparation: makeDutyDescription(
    '为后续工作准备环境与输入，并按要求保护现有基线。',
    ['任务约束', '项目、文件和运行环境现状', '需要保留的已有状态'],
    ['可用的工作环境和准备材料', '基线保护及准备结果记录'],
    ['细致检查', '遵循步骤', '避免遗漏既有状态'],
  ),
  finalization: makeDutyDescription(
    '汇总已完成内容，整理产物并说明交付状态。',
    ['实现与验证结果', '待交付产物', '尚未完成或待确认事项'],
    ['清晰的交付摘要', '整理后的产物与未解决事项'],
    ['准确汇总', '核对完整性', '清楚区分已完成与未验证'],
  ),
  investigation: makeDutyDescription(
    '围绕问题收集证据、核对事实并定位可能原因。',
    ['问题描述和复现条件', '源码、配置、日志或运行证据'],
    ['可核对的事实与证据', '原因判断及其置信边界'],
    ['多源证据分析', '因果推理', '区分事实、假设和未知'],
  ),
  requirements: makeDutyDescription(
    '把用户目标和已有约束整理为明确、可验收的需求。',
    ['用户请求', '业务契约和已有行为', '环境、安全及兼容性约束'],
    ['需求、范围与非目标', '验收条件和待确认歧义'],
    ['准确理解意图', '发现冲突与歧义', '写出可验证条件'],
  ),
  design: makeDutyDescription(
    '根据需求和仓库证据形成技术方案；标准布局把初稿、修订及 native 设计节点合并。',
    ['已确认需求与验收条件', '架构、调用关系和实现约束', '调查证据'],
    ['技术方案、接口和关键取舍', '待解决风险与验证方式'],
    ['跨组件推理', '比较实现取舍', '保持方案与约束一致'],
  ),
  design_review: makeDutyDescription(
    '检查技术方案的完整性、可行性、边界和风险。',
    ['需求与验收条件', '设计方案和已有实现证据'],
    ['设计问题、缺口和风险', '是否满足条件的审查意见'],
    ['发现遗漏和冲突', '推演边界情形', '给出可核对依据'],
  ),
  implementation_plan: makeDutyDescription(
    '将已确认方案拆成有序实施步骤，并审查计划覆盖情况。',
    ['需求、设计和仓库状态', '依赖关系与实施约束'],
    ['文件或模块级步骤', '步骤依赖、验收和验证安排'],
    ['任务拆分', '依赖排序', '检查计划覆盖与可执行性'],
  ),
  implementation: makeDutyDescription(
    '按确认的需求和方案实施代码修改，并处理实现中的修复与核对。',
    ['需求、设计与实施计划', '当前源码和相关验证结果'],
    ['代码修改与必要的修复', '变更说明和局部验证证据'],
    ['代码理解和生成', '跨文件一致性', '按证据修复问题'],
  ),
  iterative_execution: makeDutyDescription(
    '围绕持续目标或清单逐项推进，记录进展并更新后续动作。',
    ['持续目标、待办清单和当前状态', '上一轮执行结果'],
    ['已完成项、阻塞项和下一步', '更新后的执行状态'],
    ['跨轮次保持上下文', '追踪清单状态', '根据结果调整步骤'],
  ),
  experiment: makeDutyDescription(
    '围绕假设进行受控比较或试验，整理可复核结果。',
    ['待检验假设', '实验条件、输入和评价指标'],
    ['观察结果、条件和局限', '对假设的支持或反证'],
    ['控制变量', '分析结果', '保留复现条件'],
  ),
  code_review: makeDutyDescription(
    '审查代码变更的正确性、回归风险、安全边界和维护性；各 review 变体仍以实际任务上下文为准。',
    ['变更 diff', '需求、设计和相关调用链', '验证结果'],
    ['按严重性排序的问题及证据', '未覆盖风险和审查结论'],
    ['细读代码', '追踪数据与调用路径', '优先报告可复现问题'],
  ),
  semantic_review: makeDutyDescription(
    '检查实现或产物是否保留要求的语义、逻辑、记忆行为或 native 语义。',
    ['需求与原始材料', '实现、状态或相关语义约束'],
    ['偏差、逻辑问题和证据', '需要修正的语义要求'],
    ['逐项对照', '追踪语义变化', '识别隐含状态影响'],
  ),
  review_synthesis: makeDutyDescription(
    '整合多份审查意见，合并重复问题并保留分歧和证据。',
    ['多个审查结果及各自证据'],
    ['去重后的问题清单', '分歧、优先级和待核实项'],
    ['归并相同问题', '保留来源与严重性', '避免丢失少数意见'],
  ),
  review_verdict: makeDutyDescription(
    '基于汇总的审查证据形成最终代码审查裁决。',
    ['审查问题汇总', '问题严重性和验证证据'],
    ['明确的审查裁决', '阻塞项及其依据'],
    ['权衡证据和影响', '区分阻塞与建议', '结论可追溯'],
  ),
  validation: makeDutyDescription(
    '执行或核对与改动相称的验证，并如实记录结果。',
    ['变更范围和验收条件', '可用的检查命令或运行证据'],
    ['实际执行的验证及结果', '失败原文与未覆盖项'],
    ['选择匹配范围的检查', '准确解读失败', '区分静态检查和运行验证'],
  ),
  delivery_audit: makeDutyDescription(
    '核对交付内容、变更范围、验证证据和未解决事项是否完整。',
    ['最终差异与交付产物', '验证记录和验收条件'],
    ['交付完整性检查', '缺项、未验证和待处理事项'],
    ['系统化核对', '检查范围漂移', '准确报告限制'],
  ),
  operations: makeDutyDescription(
    '按已明确的运维任务检查运行状态，并核对操作后的结果。',
    ['运行环境与服务状态', '已授权的运维步骤和预期状态'],
    ['操作或检查结果', '服务健康状态与异常证据'],
    ['遵循运维步骤', '读取运行证据', '判断健康状态'],
  ),
  pr_lifecycle: makeDutyDescription(
    '处理 PR 提交和反馈；标准布局将提交与反馈处理合并为一个职责。',
    ['待提交的变更或已有 PR', '审查反馈与协作约束'],
    ['PR 状态与提交信息', '反馈处理结果和未解决意见'],
    ['整理变更上下文', '跟踪反馈状态', '准确汇总协作结果'],
  ),
  pr_submit: makeDutyDescription(
    '整理 PR 描述与变更上下文，并完成该流程声明的提交步骤。',
    ['待提交变更、验证结果和仓库状态', 'PR 描述要求'],
    ['PR 提交信息与可追踪状态', '尚未完成的提交事项'],
    ['准确概括变更', '关联验证证据', '核对提交状态'],
  ),
  pr_feedback: makeDutyDescription(
    '理解并处理 PR 反馈，跟踪修复、答复和仍需决定的问题。',
    ['审查意见及其上下文', '相关代码和验证证据'],
    ['反馈处理结果', '修复和验证记录', '未解决的分歧'],
    ['解释审查意图', '定位代码影响', '逐项跟踪反馈'],
  ),
}

const AUTO_NODE_USAGE = {
  run: '主控入口，协调用户任务、工作流步骤和最终答复。',
  prepare: '准备后续步骤所需的工作环境、输入或材料。',
  'protect-baseline': '在修改前识别并保护已有基线或用户状态。',
  setup: '完成任务要求的环境或项目初始化准备。',
  'design-file-gate': '检查设计类文件是否满足进入后续步骤的条件。',
  cleanup: '整理任务过程产生的临时状态或交付材料。',
  finalize: '汇总完成情况、交付结果和仍未解决的事项。',
  baseline: '先记录问题相关的现状和对照证据，供调查比较。',
  investigate: '围绕问题继续收集证据并分析可能原因。',
  requirements: '明确任务需求、限制和可验收条件。',
  design: '形成技术设计和关键实现取舍。',
  'design-draft': '产出设计的初稿，供后续评审或修订。',
  'design-fix': '根据反馈修订已有设计，关注修订项是否闭合。',
  'native-design': '处理名称标记为 native 的设计路径；具体边界以工作流定义为准。',
  plan: '拆分实施步骤并安排先后顺序。',
  'implementation-plan': '形成面向实现的步骤、依赖和验收安排。',
  'audit-plan': '检查计划是否覆盖需求、依赖和验证步骤。',
  code: '实施代码修改并说明相应变更。',
  implement: '执行实现步骤，产出代码或其他实现结果。',
  repair: '针对已定位的问题实施修复。',
  'memory-code': '处理名称标记为 memory 的代码实现路径。',
  'native-code': '处理名称标记为 native 的代码实现路径。',
  'native-code-verify': '核对 native 代码实现结果及其对应要求。',
  'native-implementation': '处理名称标记为 native 的实现步骤。',
  work: '围绕持续目标推进当前待办并记录结果。',
  iterate: '依据上一轮结果继续下一轮目标或清单执行。',
  experiment: '执行围绕指定假设或变量的实验步骤。',
  'design-review': '审查设计的完整性、可行性和风险。',
  'design-recheck': '在设计修订后重新核对先前问题和验收条件。',
  review: '执行代码审查并报告有证据的问题。',
  'review-fable': '名称为 review-fable 的审查变体入口；变体名标识节点路径，不指定模型。',
  'review-gpt': '名称为 review-gpt 的审查变体入口；变体名标识节点路径，不指定模型。',
  'review-opus': '名称为 review-opus 的审查变体入口；变体名标识节点路径，不指定模型。',
  'implementation-review': '从实现是否满足需求与方案的角度审查变更。',
  'independent-review': '以独立审查路径检查变更并单独记录问题。',
  'fidelity-review': '对照原始要求或材料检查内容保真度。',
  'logic-review': '检查推理、条件和步骤之间的逻辑一致性。',
  'memory-review': '检查涉及 memory 的实现或状态语义。',
  'native-review': '审查名称标记为 native 的实现路径。',
  'native-semantic-review': '检查 native 路径中的语义是否符合要求。',
  aggregate: '归并多份审查意见，整理问题清单并保留证据。',
  'code-aggregate': '汇总代码审查结果并形成审查裁决所需材料。',
  validate: '按验收条件执行或核对实际验证步骤。',
  'final-audit': '在交付前核对产物、差异、验证和未解决事项。',
  'restart-and-verify': '按运维步骤检查重启后的运行状态与健康结果。',
  submit: '处理 PR 提交流程并记录提交后的状态。',
  respond: '逐项处理 PR 反馈并记录答复、修复或待决事项。',
}

export function getAutoDutyDescription(dutyKey) {
  return Object.hasOwn(AUTO_DUTY_DESCRIPTIONS, dutyKey)
    ? AUTO_DUTY_DESCRIPTIONS[dutyKey]
    : null
}

export function getAutoNodeReference(nodeId) {
  const key = String(nodeId ?? '')
  return Object.hasOwn(AUTO_NODE_USAGE, key)
    ? { nodeId: key, usage: AUTO_NODE_USAGE[key] }
    : null
}
