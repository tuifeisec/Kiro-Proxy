// 思考档位的唯一前端定义，必须与后端 converter/reasoning_effort_fit.go 保持一致。
// 档位按强度从弱到强排列；模型有效档位 = 已声明的支持档位，否则使用 provider 默认表。

export const REASONING_EFFORT_ORDER = ['none', 'low', 'medium', 'high', 'xhigh', 'max']

export const REASONING_EFFORT_LABELS = {
  none: '关闭',
  low: '低',
  medium: '中',
  high: '高',
  xhigh: '极高',
  max: '最大',
}

const OPENAI_EFFORTS = ['none', 'low', 'medium', 'high', 'xhigh', 'max']

const PROVIDER_REASONING_EFFORTS = {
  openai: OPENAI_EFFORTS,
  codex: OPENAI_EFFORTS,
  anthropic: OPENAI_EFFORTS,
  gemini: OPENAI_EFFORTS,
}

// 思考形态覆盖：空=自动；仅 anthropic 与 gemini 可显式选择。
export const PROVIDER_THINKING_STYLES = {
  anthropic: [
    { value: '', label: '自动' },
    { value: 'adaptive', label: 'adaptive' },
    { value: 'budget', label: 'budget' },
  ],
  gemini: [
    { value: '', label: '自动' },
    { value: 'level', label: 'level' },
    { value: 'budget', label: 'budget' },
  ],
}

// 与后端一致：未知 provider 按 OpenAI 兼容格式转发，共用 OpenAI 表。
export function providerReasoningEfforts(provider) {
  const table = PROVIDER_REASONING_EFFORTS[String(provider || '').trim().toLowerCase()] || OPENAI_EFFORTS
  return [...table]
}

export function sortReasoningEfforts(values = []) {
  const selected = new Set((Array.isArray(values) ? values : []).map((value) => String(value || '').trim().toLowerCase()))
  return REASONING_EFFORT_ORDER.filter((effort) => selected.has(effort))
}

// 模型有效档位；声明值中 provider 无法表达的档位不计入（后端保存时会拒绝）。
export function modelReasoningEfforts(model) {
  const providerTable = providerReasoningEfforts(model?.provider)
  const declared = sortReasoningEfforts(model?.supported_reasoning_efforts).filter((effort) => providerTable.includes(effort))
  return declared.length ? declared : providerTable
}

// 默认档位与 Auto 档位下拉选项：空值（不指定）+ 模型有效档位。
export function modelEffortOptions(model) {
  return ['', ...modelReasoningEfforts(model)]
}

export function describeReasoningEffort(effort) {
  return REASONING_EFFORT_LABELS[effort] || effort || '自动'
}
