// Typesafe 配置仅用于独立 classifier；前端选择值不会写入模型 ID。
export const TYPESAFE_CLASSIFIER_CHOICE = '__typesafe__'
export const TYPESAFE_SETTINGS_KEY = 'typesafe_classifier_config'
export const TYPESAFE_KEY_MASK = '****'

export function defaultTypesafeConfig() {
  return {
    enabled: false,
    base_url: 'https://api.typesafe.ai',
    api_key: '',
    model: 'jev-1.13.0',
    timeout_seconds: 10,
    input_price_per_million: 0.042,
    proxy_url: '',
  }
}

export function parseTypesafeConfig(settings) {
  const raw = settings?.[TYPESAFE_SETTINGS_KEY]
  if (raw === undefined || raw === null || raw === '') return defaultTypesafeConfig()
  const parsed = typeof raw === 'string' ? JSON.parse(raw) : raw
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('Typesafe classifier 配置格式无效')
  }
  const config = { ...defaultTypesafeConfig(), ...parsed }
  if (typeof config.enabled !== 'boolean') throw new Error('Typesafe classifier enabled 必须是布尔值')
  for (const key of ['base_url', 'api_key', 'model', 'proxy_url']) {
    if (typeof config[key] !== 'string') throw new Error(`Typesafe classifier ${key} 必须是字符串`)
  }
  // API 正常只返回掩码；表单与主选择保存均不保留读回的明文。
  config.api_key = config.api_key ? TYPESAFE_KEY_MASK : ''
  config.model = config.model.trim() || defaultTypesafeConfig().model
  return config
}

export function effectiveClassifierChoice(legacyModelId, typesafeConfig) {
  return typesafeConfig.enabled ? TYPESAFE_CLASSIFIER_CHOICE : legacyModelId
}
