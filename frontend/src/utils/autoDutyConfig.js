export const AUTO_ROUTING_SETTINGS_KEY = 'auto_routing_config'

export const AUTO_DUTY_OPTIONS = [
  { key: 'orchestration', label: '主控协调', nodes: ['run'] },
  { key: 'routine_support', label: '机械准备与交付整理', nodes: ['prepare', 'protect-baseline', 'setup', 'design-file-gate', 'cleanup', 'finalize'] },
  { key: 'investigation', label: '调查取证', nodes: ['baseline', 'investigate'] },
  { key: 'requirements', label: '需求与约束', nodes: ['requirements'] },
  { key: 'design', label: '技术设计', nodes: ['design', 'design-draft', 'design-fix', 'native-design'] },
  { key: 'implementation_plan', label: '实施计划', nodes: ['plan', 'implementation-plan', 'audit-plan'] },
  { key: 'implementation', label: '代码实施与修复', nodes: ['code', 'implement', 'repair', 'memory-code', 'native-code', 'native-code-verify', 'native-implementation'] },
  { key: 'iterative_execution', label: '持续目标与清单执行', nodes: ['work', 'iterate'] },
  { key: 'experiment', label: '实验研究', nodes: ['experiment'] },
  { key: 'design_review', label: '设计审查', nodes: ['design-review', 'design-recheck'] },
  { key: 'code_review', label: '代码审查', nodes: ['review', 'review-fable', 'review-gpt', 'review-opus', 'implementation-review', 'independent-review'] },
  { key: 'semantic_review', label: '专项语义审查', nodes: ['fidelity-review', 'logic-review', 'memory-review', 'native-review', 'native-semantic-review'] },
  { key: 'review_synthesis', label: '审查问题汇总', nodes: ['aggregate'] },
  { key: 'review_verdict', label: '审查裁决', nodes: ['code-aggregate'] },
  { key: 'validation', label: '实际验证', nodes: ['validate'] },
  { key: 'delivery_audit', label: '交付稽核', nodes: ['final-audit'] },
  { key: 'operations', label: '运行环境运维', nodes: ['restart-and-verify'] },
  { key: 'pr_lifecycle', label: 'PR 提交与反馈处理', nodes: ['submit', 'respond'] },
]

export const AUTO_DUTY_GROUPS = {
  routine_support: { preparation: '机械准备', finalization: '交付整理' },
  pr_lifecycle: { pr_submit: 'PR 提交', pr_feedback: 'PR 反馈处理' },
}

export const AUTO_DUTY_EXPANDED = AUTO_DUTY_OPTIONS.flatMap((duty) => {
  const split = AUTO_DUTY_GROUPS[duty.key]
  if (!split) return [duty]
  return Object.entries(split).map(([key, label]) => ({
    key,
    label,
    nodes: key === 'preparation'
      ? ['prepare', 'protect-baseline', 'setup', 'design-file-gate', 'cleanup']
      : key === 'finalization'
        ? ['finalize']
        : key === 'pr_submit'
          ? ['submit']
          : ['respond'],
  }))
})

export const AUTO_DUTY_LAYOUTS = {
  standard: AUTO_DUTY_OPTIONS,
  expanded: AUTO_DUTY_EXPANDED,
}

export const AUTO_DUTY_LAYOUT_LABELS = {
  standard: '标准布局（18 个职责）',
  expanded: '细分布局（20 个职责）',
}

const DUTY_KEYS = Object.fromEntries(
  Object.entries(AUTO_DUTY_LAYOUTS).map(([layout, duties]) => [layout, duties.map(({ key }) => key)]),
)
const ROOT_FIELDS = ['version', 'mode', 'enabled', 'layout', 'default_model_id', 'duty_models', 'node_bindings', 'node_overrides']
const LEGACY_FIELDS = ['mode', 'enabled', 'default_model_id', 'stage_models', 'vision_model_id', 'reasoning_model_id', 'long_context_model_id', 'fast_model_id', 'fast_max_chars', 'long_context_min_chars', 'task_rules']
const STAGE_KEYS = ['run', 'plan', 'implement', 'review', 'finalize']

export function emptyAutoDutyConfig(layout = 'standard') {
  return {
    version: 2,
    mode: 'duty_fixed',
    enabled: false,
    layout,
    default_model_id: '',
    duty_models: Object.fromEntries(DUTY_KEYS[layout].map((key) => [key, { model_id: '', reasoning_effort: '' }])),
    node_bindings: [],
    node_overrides: [],
  }
}

function configError(message) {
  return new Error(`Auto 模型配置${message}；原始值已保留，当前不能保存覆盖。`)
}

function rejectDuplicateJsonKeys(source) {
  let index = 0
  const skipWhitespace = () => {
    while (index < source.length && /\s/.test(source[index])) index += 1
  }
  const readString = () => {
    const start = index
    index += 1
    while (index < source.length) {
      if (source[index] === '\\') index += 2
      else if (source[index++] === '"') return JSON.parse(source.slice(start, index))
    }
    return ''
  }
  const readValue = () => {
    skipWhitespace()
    if (source[index] === '"') {
      readString()
      return
    }
    if (source[index] === '{') {
      index += 1
      skipWhitespace()
      if (source[index] === '}') {
        index += 1
        return
      }
      const keys = new Set()
      while (index < source.length) {
        skipWhitespace()
        if (source[index] !== '"') return
        const key = readString()
        if (keys.has(key)) {
          const error = configError(`包含重复 JSON 字段 ${key}`)
          error.code = 'DUPLICATE_JSON_KEY'
          throw error
        }
        keys.add(key)
        skipWhitespace()
        if (source[index] !== ':') return
        index += 1
        readValue()
        skipWhitespace()
        if (source[index] === '}') {
          index += 1
          return
        }
        if (source[index] !== ',') return
        index += 1
      }
      return
    }
    if (source[index] === '[') {
      index += 1
      skipWhitespace()
      if (source[index] === ']') {
        index += 1
        return
      }
      while (index < source.length) {
        readValue()
        skipWhitespace()
        if (source[index] === ']') {
          index += 1
          return
        }
        if (source[index] !== ',') return
        index += 1
      }
      return
    }
    while (index < source.length && !/[\s,\]}]/.test(source[index])) index += 1
  }
  readValue()
}

function requireExactFields(value, expected, context) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw configError(`中的 ${context} 必须是对象`)
  const keys = Object.keys(value)
  const unknown = keys.find((key) => !expected.includes(key))
  if (unknown) throw configError(`中的 ${context} 包含未知字段 ${unknown}`)
  const missing = expected.find((key) => !Object.prototype.hasOwnProperty.call(value, key))
  if (missing) throw configError(`中的 ${context} 缺少字段 ${missing}`)
}

function identityKey(item) {
  return JSON.stringify([item.definition_id, item.definition_revision, item.node_path])
}

function parseNodeArray(value, kind, dutyKeys) {
  if (!Array.isArray(value)) throw configError(`中的 ${kind} 必须是数组`)
  const seen = new Set()
  return value.map((item, index) => {
    const fields = kind === 'node_bindings'
      ? ['definition_id', 'definition_revision', 'node_path', 'duty']
      : ['definition_id', 'definition_revision', 'node_path', 'model_id', 'reasoning_effort']
    requireExactFields(item, fields, `${kind}[${index}]`)
    for (const field of ['definition_id', 'definition_revision', 'node_path']) {
      if (typeof item[field] !== 'string' || !item[field].trim() || item[field] !== item[field].trim()) {
        throw configError(`中的 ${kind}[${index}].${field} 必须是非空、无首尾空格的字符串`)
      }
    }
    const identity = identityKey(item)
    if (seen.has(identity)) throw configError(`中的 ${kind} 含重复节点身份`)
    seen.add(identity)
    if (kind === 'node_bindings') {
      if (typeof item.duty !== 'string' || !dutyKeys.includes(item.duty)) throw configError(`中的 ${kind}[${index}].duty 不属于当前布局`)
      return { definition_id: item.definition_id, definition_revision: item.definition_revision, node_path: item.node_path, duty: item.duty }
    }
    if (typeof item.model_id !== 'string' || typeof item.reasoning_effort !== 'string') {
      throw configError(`中的 ${kind}[${index}] 的 model_id 和 reasoning_effort 必须是字符串`)
    }
    return { definition_id: item.definition_id, definition_revision: item.definition_revision, node_path: item.node_path, model_id: item.model_id, reasoning_effort: item.reasoning_effort }
  })
}

function parseV2(parsed) {
  requireExactFields(parsed, ROOT_FIELDS, 'v2 配置')
  if (parsed.version !== 2 || parsed.mode !== 'duty_fixed') throw configError('的 version 或 mode 不受支持')
  if (typeof parsed.enabled !== 'boolean') throw configError('中的 enabled 必须是布尔值')
  if (!Object.prototype.hasOwnProperty.call(DUTY_KEYS, parsed.layout)) throw configError('中的 layout 必须是 standard 或 expanded')
  if (typeof parsed.default_model_id !== 'string') throw configError('中的 default_model_id 必须是字符串')
  if (!parsed.duty_models || typeof parsed.duty_models !== 'object' || Array.isArray(parsed.duty_models)) throw configError('中的 duty_models 必须是对象')
  const unknownDuty = Object.keys(parsed.duty_models).find((key) => !DUTY_KEYS[parsed.layout].includes(key))
  if (unknownDuty) throw configError(`中的 duty_models 包含当前布局未知职责 ${unknownDuty}`)
  const dutyModels = Object.fromEntries(DUTY_KEYS[parsed.layout].map((key) => [key, { model_id: '', reasoning_effort: '' }]))
  for (const key of Object.keys(parsed.duty_models)) {
    const target = parsed.duty_models[key]
    requireExactFields(target, ['model_id', 'reasoning_effort'], `duty_models.${key}`)
    if (typeof target.model_id !== 'string' || typeof target.reasoning_effort !== 'string') {
      throw configError(`中的 duty_models.${key} 的 model_id 和 reasoning_effort 必须是字符串`)
    }
    dutyModels[key] = { model_id: target.model_id, reasoning_effort: target.reasoning_effort }
  }
  return {
    config: {
      version: 2,
      mode: 'duty_fixed',
      enabled: parsed.enabled,
      layout: parsed.layout,
      default_model_id: parsed.default_model_id,
      duty_models: dutyModels,
      node_bindings: parseNodeArray(parsed.node_bindings, 'node_bindings', DUTY_KEYS[parsed.layout]),
      node_overrides: parseNodeArray(parsed.node_overrides, 'node_overrides', DUTY_KEYS[parsed.layout]),
    },
    legacy: false,
  }
}

function parseLegacy(parsed, raw) {
  const unknown = Object.keys(parsed).find((key) => !LEGACY_FIELDS.includes(key))
  if (unknown) throw configError(`包含未知字段 ${unknown}`)
  if (parsed.mode !== undefined && !['', 'task_rules', 'stage_fixed'].includes(parsed.mode)) throw configError('中的旧 mode 无效')
  if (parsed.enabled !== undefined && typeof parsed.enabled !== 'boolean') throw configError('中的 enabled 必须是布尔值')
  if (parsed.default_model_id !== undefined && typeof parsed.default_model_id !== 'string') throw configError('中的 default_model_id 必须是字符串')
  const stageModels = Object.fromEntries(STAGE_KEYS.map((key) => [key, '']))
  if (parsed.stage_models !== undefined) {
    requireExactFields(parsed.stage_models, STAGE_KEYS, 'stage_models')
    for (const key of STAGE_KEYS) {
      if (typeof parsed.stage_models[key] !== 'string') throw configError(`中的 stage_models.${key} 必须是字符串`)
      stageModels[key] = parsed.stage_models[key]
    }
  } else if (parsed.mode === 'stage_fixed') {
    throw configError('缺少 stage_models')
  }
  return {
    config: null,
    legacy: true,
    legacyData: { enabled: parsed.enabled === true, default_model_id: parsed.default_model_id || '', stage_models: stageModels, mode: parsed.mode || 'task_rules', raw },
  }
}

export function parseAutoDutyConfig(raw) {
  if (raw === undefined || raw === null || (typeof raw === 'string' && raw.trim() === '')) {
    return { config: emptyAutoDutyConfig(), legacy: false, raw: raw ?? '' }
  }
  if (typeof raw !== 'string') throw configError('不是 JSON 字符串')
  let parsed
  try {
    rejectDuplicateJsonKeys(raw)
    parsed = JSON.parse(raw)
  } catch (error) {
    if (error?.code === 'DUPLICATE_JSON_KEY') throw error
    throw new Error(`Auto 模型配置不是有效 JSON：${error.message}；原始值已保留，当前不能保存覆盖。`)
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw configError('必须是 JSON 对象')
  if (Object.prototype.hasOwnProperty.call(parsed, 'version')) {
    if (parsed.version !== 2) throw configError(`的 version ${String(parsed.version)} 不受支持`)
    return { ...parseV2(parsed), raw }
  }
  return { ...parseLegacy(parsed, raw), raw }
}

export function migrateAutoDutyConfig(legacy, layout = 'standard') {
  const config = emptyAutoDutyConfig(layout)
  config.enabled = legacy.enabled === true
  config.default_model_id = legacy.default_model_id || ''
  const finalDuty = layout === 'expanded' ? 'finalization' : 'routine_support'
  const mappings = {
    run: 'orchestration',
    plan: 'implementation_plan',
    implement: 'implementation',
    review: 'code_review',
    finalize: finalDuty,
  }
  for (const [stage, duty] of Object.entries(mappings)) {
    const modelId = legacy.stage_models?.[stage] || ''
    if (modelId) config.duty_models[duty].model_id = modelId
  }
  return config
}

export function providerEffortOptions(provider) {
  const options = {
    openai: ['', 'none', 'low', 'medium', 'high', 'xhigh', 'max'],
    codex: ['', 'none', 'low', 'medium', 'high', 'xhigh', 'max'],
    anthropic: ['', 'none', 'low', 'medium', 'high'],
    gemini: ['', 'low', 'medium', 'high'],
  }
  return options[String(provider || '').toLowerCase()] || ['']
}

export function resolveAutoTarget(targetId, models = [], groups = []) {
  const model = (Array.isArray(models) ? models : []).find((item) => item.id === targetId)
  const group = (Array.isArray(groups) ? groups : []).find((item) => item.id === targetId)
  if (model && group) return { kind: 'collision', model, group }
  if (model) return { kind: 'model', model }
  if (group) return { kind: 'group', group }
  return { kind: 'missing' }
}

export function autoTargetGroupCandidates(targetId, models = [], groups = []) {
  const target = resolveAutoTarget(targetId, models, groups)
  if (target.kind !== 'group' || target.group.enabled !== true) return []

  const modelById = new Map((Array.isArray(models) ? models : []).map((model) => [model.id, model]))
  const groupById = new Map((Array.isArray(groups) ? groups : []).map((group) => [group.id, group]))
  const candidates = []
  const seenModels = new Set()
  const seenGroups = new Set()
  const collect = (group) => {
    if (group?.enabled !== true || seenGroups.has(group.id)) return
    seenGroups.add(group.id)
    const members = [...(group.members || [])].sort((left, right) => Number(left.priority || 0) - Number(right.priority || 0))
    for (const member of members) {
      const model = modelById.get(member.model_id)
      if (model?.enabled === true && !seenModels.has(model.id)) {
        candidates.push(model)
        seenModels.add(model.id)
      }
    }
    const fallback = groupById.get(group.fallback_group_id)
    if (fallback) collect(fallback)
  }
  collect(target.group)
  return candidates
}

export function autoTargetEffortOptions(targetId, models = [], groups = []) {
  const target = resolveAutoTarget(targetId, models, groups)
  if (target.kind === 'model') return providerEffortOptions(target.model.provider)
  if (target.kind !== 'group') return ['']

  const candidates = autoTargetGroupCandidates(targetId, models, groups)
  if (!candidates.length) return ['']
  const optionsByCandidate = candidates.map((model) => new Set(providerEffortOptions(model.provider)))
  return providerEffortOptions(candidates[0].provider).filter((effort) => (
    optionsByCandidate.every((options) => options.has(effort))
  ))
}

export function autoTargetDefaultEffort(targetId, models = [], groups = []) {
  const target = resolveAutoTarget(targetId, models, groups)
  if (target.kind === 'model') return String(target.model.reasoning_effort || '').trim().toLowerCase()
  if (target.kind !== 'group') return ''
  const configuredEffort = String(target.group.reasoning_effort || '').trim()
  if (configuredEffort) return configuredEffort.toLowerCase()

  const candidates = autoTargetGroupCandidates(targetId, models, groups)
  if (!candidates.length) return ''
  const effort = String(candidates[0].reasoning_effort || '').trim().toLowerCase()
  return candidates.every((model) => String(model.reasoning_effort || '').trim().toLowerCase() === effort)
    ? effort
    : ''
}

export function isAutoTargetSelectable(targetId, models = [], groups = []) {
  const target = resolveAutoTarget(targetId, models, groups)
  if (target.kind === 'model') return target.model.enabled === true
  if (target.kind === 'group') return target.group.enabled === true && autoTargetGroupCandidates(targetId, models, groups).length > 0
  return false
}

export function validateAutoDutyConfig(config, models, groups = []) {
  const errors = []
  const dutyKeys = DUTY_KEYS[config.layout] || []
  if (config.node_bindings.some((item) => !dutyKeys.includes(item.duty))) errors.push('节点职责绑定不属于当前布局，需先明确迁移或删除。')

  const validateTarget = (label, id, configuredEffort, required, useDefaultEffort = false) => {
    if (!id) {
      if (required) errors.push(`启用 Auto 路由时，${label}必须选择已启用的模型或有效分组。`)
      return
    }
    const resolved = resolveAutoTarget(id, models, groups)
    if (resolved.kind === 'collision') {
      errors.push(`${label}的 ID 同时属于模型配置和分组，不能作为目标。`)
      return
    }
    if (resolved.kind === 'missing') {
      errors.push(`${label}必须引用已启用的模型配置或分组；当前配置不存在，已保留原值。`)
      return
    }

    let candidates
    if (resolved.kind === 'model') {
      if (resolved.model.enabled !== true) {
        errors.push(`${label}必须引用已启用的模型配置；当前配置已停用，已保留原值。`)
        return
      }
      candidates = [resolved.model]
    } else {
      if (resolved.group.enabled !== true) {
        errors.push(`${label}必须引用已启用的分组；当前分组已停用，已保留原值。`)
        return
      }
      candidates = autoTargetGroupCandidates(id, models, groups)
      if (!candidates.length) {
        errors.push(`${label}必须引用至少包含一个已启用候选模型的分组；当前分组没有有效候选，已保留原值。`)
        return
      }
    }

    const effort = useDefaultEffort ? autoTargetDefaultEffort(id, models, groups) : configuredEffort
    const allowed = autoTargetEffortOptions(id, models, groups)
    if (effort && !allowed.includes(String(effort).trim().toLowerCase())) {
      const providers = [...new Set(candidates.map((model) => model.provider || '未知'))].join('/')
      errors.push(`${label}的思考等级不受本地 ${providers} Provider 表达。`)
    }
  }

  validateTarget('默认固定目标', config.default_model_id, '', config.enabled, true)
  const targets = []
  for (const duty of AUTO_DUTY_LAYOUTS[config.layout] || []) {
    targets.push({ label: `${duty.label}目标`, target: config.duty_models[duty.key] })
  }
  for (const { label, target } of targets) {
    const id = target?.model_id || ''
    validateTarget(label, id, target?.reasoning_effort || '', config.enabled)
  }
  for (const [index, item] of config.node_overrides.entries()) {
    validateTarget(`节点覆盖 ${index + 1}`, item.model_id || '', item.reasoning_effort || '', config.enabled)
  }
  return errors
}

export function nodeIdentityComplete(observation) {
  return ['definition_id', 'definition_revision', 'node_path'].every((field) => (
    typeof observation?.[field] === 'string' && observation[field].trim() !== '' && observation[field] === observation[field].trim()
  ))
}

export function applyDutyForLayout(config, layout, resolutions = {}) {
  const next = emptyAutoDutyConfig(layout)
  next.enabled = config.enabled
  next.default_model_id = config.default_model_id
  next.node_bindings = config.node_bindings.map((item, index) => {
    const split = AUTO_DUTY_GROUPS[item.duty]
    if (layout === 'expanded' && config.layout === 'standard' && split) {
      const chosenDuty = resolutions[`binding:${index}`]
      return chosenDuty && Object.hasOwn(split, chosenDuty) ? { ...item, duty: chosenDuty } : { ...item }
    }
    if (layout === 'standard' && config.layout === 'expanded') {
      const combined = Object.entries(AUTO_DUTY_GROUPS).find(([, children]) => Object.hasOwn(children, item.duty))?.[0]
      return combined ? { ...item, duty: combined } : { ...item }
    }
    return { ...item }
  })
  next.node_overrides = config.node_overrides.map((item) => ({ ...item }))
  for (const duty of AUTO_DUTY_LAYOUTS[layout]) {
    if (config.duty_models[duty.key]) next.duty_models[duty.key] = { ...config.duty_models[duty.key] }
  }
  for (const [combined, children] of Object.entries(AUTO_DUTY_GROUPS)) {
    const childKeys = Object.keys(children)
    if (layout === 'expanded' && config.layout === 'standard') {
      const source = config.duty_models[combined] || { model_id: '', reasoning_effort: '' }
      for (const childKey of childKeys) next.duty_models[childKey] = { ...source }
      continue
    }
    if (layout === 'standard' && config.layout === 'expanded') {
      const [first, second] = childKeys
      const firstTarget = config.duty_models[first] || { model_id: '', reasoning_effort: '' }
      const secondTarget = config.duty_models[second] || { model_id: '', reasoning_effort: '' }
      const choice = resolutions[combined]
      if (JSON.stringify(firstTarget) === JSON.stringify(secondTarget)) next.duty_models[combined] = { ...firstTarget }
      else if (!firstTarget.model_id) next.duty_models[combined] = { ...secondTarget }
      else if (!secondTarget.model_id) next.duty_models[combined] = { ...firstTarget }
      else if (choice === first) next.duty_models[combined] = { ...firstTarget }
      else if (choice === second) next.duty_models[combined] = { ...secondTarget }
      else if (choice === 'block') next.duty_models[combined] = { model_id: '', reasoning_effort: '' }
      else next.duty_models[combined] = { model_id: '', reasoning_effort: '' }
      if (!firstTarget.model_id && !secondTarget.model_id) next.duty_models[combined] = { model_id: '', reasoning_effort: '' }
    }
  }
  return next
}

export function layoutMergeConflicts(config) {
  if (config.layout !== 'expanded') return []
  return Object.entries(AUTO_DUTY_GROUPS).flatMap(([combined, children]) => {
    const [first, second] = Object.keys(children)
    const firstTarget = config.duty_models[first] || { model_id: '', reasoning_effort: '' }
    const secondTarget = config.duty_models[second] || { model_id: '', reasoning_effort: '' }
    if (!firstTarget.model_id || !secondTarget.model_id || JSON.stringify(firstTarget) === JSON.stringify(secondTarget)) return []
    return [{ combined, children, first, second, firstTarget, secondTarget }]
  })
}

export function observationIdentity(observation) {
  return identityKey(observation)
}

// 同一节点可以保留多次配置下的观察；编辑键用节点身份，渲染键用完整记录。
export function observationRecordKey(observation) {
  return JSON.stringify([
    observation.definition_id, observation.definition_revision, observation.node_path,
    observation.node_id, observation.task_type, observation.selected_duty,
    observation.selection_source, observation.unknown_reason, observation.model_config_id,
    observation.outbound_model, observation.target_group_id, observation.target_group_name,
    observation.effective_effort, observation.channel_identity,
    observation.lock_scope,
  ])
}
