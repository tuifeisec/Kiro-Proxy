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
  run: '工作流或 Agent 的通用入口。已有定义中可由 wf-planner、wf-coder、semantic_reviewer 或 vibe 使用；名称本身不能区分主控、规划、编码或审查任务，需按定义或明确 task_type 确认。',
  prepare: '创建产物目录并记录基线等机械准备。',
  'protect-baseline': '机械保护基线并保留已有改动。',
  setup: '准备本次产物目录、输入或基线；具体操作依所属定义。',
  'startup-check': '启动检查方向节点（名称参考）；不同工作流中的具体对象和范围，以完整定义或任务声明确认。',
  'design-file-gate': '只读核验设计文件存在、类型与非空条件。',
  cleanup: '审查通过后清理所属工作流创建的指定临时协作文件。',
  finalize: '汇总收尾情况和交付结果；可写范围取决于所属定义。',
  baseline: '只读核对既有输入与源码，建立前置基线认识。',
  investigate: '只读调查业务源码并整理证据报告；自定义范围以所属定义为准。',
  'investigate-wasm': 'WASM方向调查节点（名称参考）；具体对象和边界待工作流定义确认。',
  requirements: '整理任务需求与约束。',
  design: '建立或修订技术设计、保真规格；具体执行者以所属定义为准。',
  'design-draft': '检查代码并编写或修订技术设计。',
  'design-fix': '修订既有设计。',
  'native-design': '设计原生能力优先的改造。',
  plan: '调查约束、整理信息并形成实施计划；具体内容依所属定义。',
  'implementation-plan': '编写针对实施的计划。',
  'audit-plan': '只读调查并形成具体实施计划。',
  code: '按所属工作流约束直接修改目标源码。',
  implement: '实施计划、修改代码并执行该任务要求的验证。',
  repair: '实施已调查并定位的问题修复。',
  'memory-code': '直接排查并修复 memory 相关故障。',
  'native-code': '直接实施原生能力优先的源码改动。',
  'native-code-verify': '实施已证明等价的原生 API 替换并记录验证。',
  'native-implementation': '实施原生能力优先的改造。',
  work: '按目标自主推进工作，并以具体证据核对完成状态。',
  iterate: '建立或推进 checklist，并按 recipe 处理未完成项。',
  experiment: '提出并应用实验、运行 benchmark，再按结果保留或回滚。',
  'design-review': '对需求与设计进行独立审查。',
  'design-recheck': '独立复核设计修订。',
  review: '执行独立代码或语义审查。',
  'review-fable': '独立代码审查，不读取另一份报告。',
  'review-gpt': '独立代码审查，不读取另一份报告。',
  'review-opus': '独立语义审查，不读取另一份审查文件。',
  'implementation-review': '独立审查实施改动。',
  'independent-review': '对真实代码增量执行独立审查。',
  'fidelity-review': '独立审查实现是否满足保真要求。',
  'logic-review': '独立审查实现与原有算法、行为的等价性。',
  'memory-review': '独立审查 memory 故障修复。',
  'native-review': '独立审查原生能力优先的实施。',
  'native-semantic-review': '审查 native 数据获取和等价替换的语义。',
  aggregate: '合并多份审查并整理最终 issues-only 报告。',
  'code-aggregate': '合并代码审查、核对计划完成度并输出 verdict。',
  validate: '执行验收、端到端或 build/test 检查并报告；该节点不修代码。',
  'final-audit': '只读交付稽核，核对产物与验证证据。',
  'restart-and-verify': '按所属定义执行本地服务重启及运行验证；属于运维步骤。',
  submit: '发布或复用 PR 并记录 PR 身份。',
  respond: '处理 PR 反馈；PR 合并或关闭后收尾。',
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

// Identity entries cite observed task logs; they do not claim a complete workflow definition.
const AUTO_NODE_IDENTITY_USAGE = Object.freeze({
  '["content://862b0f8a6ce5c2a6d503a1d10a05dbf444fe1e26d0ea77c0d97b9789da33dce0","0","startup-check"]': {
    usage: '在已观察任务中准备项目启动、核对启动步骤和服务健康状态，整理可复核启动结果。',
    evidenceLabel: '关联任务日志参考（未取得完整工作流定义）',
    known: true,
  },
  '["content://d544cb57bd704a816bf07c592ed03e5dfbecac76dccb8436666f1b344f850655","0","investigate-wasm"]': {
    usage: '在已观察任务中调查 WASM 版本相关实现与调用逻辑，整理源码分析结果。',
    evidenceLabel: '关联任务日志参考（未取得完整工作流定义）',
    known: true,
  },
  '["content://0891b432898a3eed11221d7c03ef8206a5f56f38eeecbf85ad884815b1b696d6","0","investigate-wasm"]': {
    usage: '在已观察的“WASM 配置只读调查”任务中梳理相关配置与实现，具体节点职责和可写范围仍以工作流定义为准。',
    evidenceLabel: '关联工作流标题与会话任务日志参考（未取得完整节点定义）',
    known: true,
  },
})

const AUTO_ROUTE_REASON_DESCRIPTIONS = Object.freeze({
  task_type_unavailable: '客户端没有提供可用于职责选择的 task_type。',
  stable_identity_unavailable: '请求未提供可匹配规则的节点路径身份；使用已配置的默认目标，不表示实际选模结果未知。',
  node_path_ambiguous: '同一路径匹配到多个节点规则，无法唯一确定规则；使用已配置的默认目标。',
  metadata_v1: '请求只携带 v1 私有元数据；duty_fixed 按固定默认目标处理。',
  duty_target_missing: '已解析出职责，但该职责尚未配置固定目标。',
  expanded_routine_support_node_unknown: 'expanded 布局无法根据该节点名把 routine_support 细分为 preparation 或 finalization。',
  expanded_pr_lifecycle_node_unknown: 'expanded 布局无法根据该节点名把 pr_lifecycle 细分为 pr_submit 或 pr_feedback。',
  task_type_outside_layout: '客户端声明的 task_type 不属于当前职责布局。',
  legacy_node_identity_unavailable: '旧版阶段路由缺少可用的节点身份。',
  legacy_stage_unavailable: '旧版阶段路由没有提供阶段信息。',
  legacy_node_unknown: '旧版阶段路由提供了节点身份，但名称无法映射到已知阶段。',
})

function readOwnString(record, key) {
  if (record === null || typeof record !== 'object' || !Object.hasOwn(record, key)) return ''
  const value = record[key]
  return typeof value === 'string' ? value : ''
}

function readCompleteStableIdentity(record) {
  const definitionId = readOwnString(record, 'definition_id')
  const definitionRevision = readOwnString(record, 'definition_revision')
  const nodePath = readOwnString(record, 'node_path')
  if (
    definitionId === '' ||
    definitionRevision === '' ||
    nodePath === ''
  ) {
    return null
  }
  return JSON.stringify([definitionId, definitionRevision, nodePath])
}

export function getAutoNodeDescription(record) {
  const nodeId = readOwnString(record, 'node_id')
  const nodePath = readOwnString(record, 'node_path')
  const displayNodeId = nodeId || nodePath
  const identityKey = readCompleteStableIdentity(record)
  if (identityKey && Object.hasOwn(AUTO_NODE_IDENTITY_USAGE, identityKey)) {
    const identityUsage = AUTO_NODE_IDENTITY_USAGE[identityKey]
    return {
      nodeId: displayNodeId,
      usage: identityUsage.usage,
      evidenceLabel: identityUsage.evidenceLabel,
      known: identityUsage.known,
    }
  }

  const taskType = readOwnString(record, 'task_type')
    .replace(/^[\u0009-\u000D\u0020\u0085\u00A0\u1680\u2000-\u200A\u2028\u2029\u202F\u205F\u3000]+|[\u0009-\u000D\u0020\u0085\u00A0\u1680\u2000-\u200A\u2028\u2029\u202F\u205F\u3000]+$/g, '')
    .toLowerCase()
  if (taskType !== '' && Object.hasOwn(AUTO_DUTY_DESCRIPTIONS, taskType)) {
    return {
      nodeId: displayNodeId,
      usage: AUTO_DUTY_DESCRIPTIONS[taskType].purpose,
      evidenceLabel: '请求携带的 task_type 职责参考；声明、映射或继承来源未区分，不代表实际路由。',
      known: true,
    }
  }

  const reference = nodeId
    ? getAutoNodeReference(nodeId)
    : getAutoNodeReference(nodePath)
  if (reference) {
    return {
      nodeId: displayNodeId,
      usage: reference.usage,
      evidenceLabel: nodeId
        ? identityKey
          ? '按 node_id 名称参考；完整稳定身份未命中定义用途，名称不证明实际路由。'
          : '按 node_id 名称参考；名称不证明工作流定义或实际路由。'
        : '按完整 node_path 字符串精确匹配名称参考；没有截取路径叶段，也不证明工作流定义身份或实际路由。',
      known: true,
    }
  }

  const workflowName = readOwnString(record, 'workflow_name')
  if (workflowName) {
    return {
      nodeId: displayNodeId,
      usage: `已观察工作流“${workflowName}”中的${displayNodeId ? `“${displayNodeId}”节点` : '请求上下文'}；独立职责和可写范围待节点定义确认。`,
      evidenceLabel: '同会话、workflowId 与 nodeId 精确关联的日志标题；标题不是节点职责定义。',
      known: true,
    }
  }

  return {
    nodeId: displayNodeId,
    usage: '用途待确认：未取得工作流定义或明确 task_type 的用途依据。请核对 node_id、task_type、definition_id、definition_revision 和 node_path；节点名称本身不能证明实际职责。',
    evidenceLabel: identityKey
      ? '完整稳定身份未命中已证实条目，且没有可用的明确 task_type 或 node_id 名称参考。'
      : '没有可用的完整稳定身份、明确 task_type 或 node_id 名称参考。',
    known: false,
  }
}

export function describeAutoRouteReason(reason) {
  if (typeof reason !== 'string' || reason.trim() === '') return ''
  return Object.hasOwn(AUTO_ROUTE_REASON_DESCRIPTIONS, reason)
    ? AUTO_ROUTE_REASON_DESCRIPTIONS[reason]
    : '未识别的路由原因，需查看原始码'
}
