export const AUTO_ROUTING_SETTINGS_KEY = 'auto_routing_config'

export const AUTO_STAGE_OPTIONS = [
  { key: 'run', label: '运行（run）' },
  { key: 'plan', label: '计划（plan）' },
  { key: 'implement', label: '实现（implement）' },
  { key: 'review', label: '审查（review）' },
  { key: 'finalize', label: '收尾（finalize）' }
]

const LEGACY_FIELDS = [
  'vision_model_id',
  'reasoning_model_id',
  'long_context_model_id',
  'fast_model_id',
  'fast_max_chars',
  'long_context_min_chars',
  'task_rules'
]
const STAGE_KEYS = AUTO_STAGE_OPTIONS.map(({ key }) => key)

export function emptyAutoStageConfig() {
  return {
    mode: 'stage_fixed',
    enabled: false,
    default_model_id: '',
    stage_models: Object.fromEntries(STAGE_KEYS.map((key) => [key, '']))
  }
}

function parseError(message) {
  return new Error(`Auto 模型配置${message}，当前值已保留且不能覆盖。`)
}

function parseStageModels(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw parseError('中的 stage_models 必须是包含五个阶段的对象')
  }
  const keys = Object.keys(value)
  if (keys.length !== STAGE_KEYS.length || STAGE_KEYS.some((key) => !Object.prototype.hasOwnProperty.call(value, key))) {
    throw parseError('中的 stage_models 必须且只能包含全部五个阶段')
  }
  const stageModels = {}
  for (const key of STAGE_KEYS) {
    if (typeof value[key] !== 'string') {
      throw parseError(`中的 stage_models.${key} 必须是字符串`)
    }
    stageModels[key] = value[key].trim()
  }
  return stageModels
}

export function parseAutoStageConfig(raw) {
  if (raw === undefined || raw === null || (typeof raw === 'string' && raw.trim() === '')) {
    return { config: emptyAutoStageConfig(), legacy: false }
  }
  if (typeof raw !== 'string') {
    throw parseError('不是有效 JSON')
  }

  let parsed
  try {
    parsed = JSON.parse(raw)
  } catch (error) {
    throw new Error(`Auto 模型配置不是有效 JSON：${error.message}`)
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw parseError('必须是 JSON 对象')
  }

  const allowedFields = new Set(['mode', 'enabled', 'default_model_id', 'stage_models', ...LEGACY_FIELDS])
  const unknownField = Object.keys(parsed).find((key) => !allowedFields.has(key))
  if (unknownField) {
    throw parseError(`包含未知字段 ${unknownField}`)
  }
  if (parsed.mode !== undefined && parsed.mode !== '' && parsed.mode !== 'task_rules' && parsed.mode !== 'stage_fixed') {
    throw parseError('中的 mode 无效')
  }
  if (parsed.enabled !== undefined && typeof parsed.enabled !== 'boolean') {
    throw parseError('中的 enabled 必须是布尔值')
  }
  if (parsed.default_model_id !== undefined && typeof parsed.default_model_id !== 'string') {
    throw parseError('中的 default_model_id 必须是字符串')
  }

  const legacy = parsed.mode !== 'stage_fixed'
  if (!legacy && parsed.stage_models === undefined) {
    throw parseError('缺少 stage_models')
  }
  const stageModels = parsed.stage_models === undefined
    ? emptyAutoStageConfig().stage_models
    : parseStageModels(parsed.stage_models)
  const defaultModelID = (parsed.default_model_id || '').trim()
  const config = {
    mode: 'stage_fixed',
    enabled: legacy ? false : (parsed.enabled ?? false),
    default_model_id: defaultModelID,
    stage_models: stageModels
  }
  return { config, legacy }
}
