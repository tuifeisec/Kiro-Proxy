<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { getModels } from '../../api/models.js'
import { getSettings, updateSettings } from '../../api/settings.js'
import { useToast } from '../../composables/useToast.js'

const SETTINGS_KEY = 'auto_routing_config'
const FIELD_OPTIONS = [
  { value: 'agent_task_type', label: '任务类型' },
  { value: 'agent_mode', label: 'Agent 模式' },
  { value: 'chat_trigger_type', label: '触发类型' }
]
const ROLE_OPTIONS = [
  { key: 'default_model_id', label: '默认模型', hint: '其他规则未命中时使用；启用策略后必选。' },
  { key: 'vision_model_id', label: '图片模型', hint: '图片请求必须使用此项且模型图片能力为“支持”；未配置时图片请求返回错误。' },
  { key: 'reasoning_model_id', label: '高推理模型', hint: '收到 high、xhigh 或 max 推理要求时使用；未配置则跳过该规则，继续匹配；最终无命中使用默认模型。' },
  { key: 'long_context_model_id', label: '长上下文模型', hint: '总字符数达到长上下文阈值时使用；未配置则跳过该规则，继续匹配；最终无命中使用默认模型。' },
  { key: 'fast_model_id', label: '快速模型', hint: '无工具、无图片、非高推理的短文本请求可使用；未配置则跳过该规则，继续匹配；最终无命中使用默认模型。' }
]

const emptyConfig = () => ({
  enabled: false,
  default_model_id: '',
  vision_model_id: '',
  reasoning_model_id: '',
  long_context_model_id: '',
  fast_model_id: '',
  fast_max_chars: 500,
  long_context_min_chars: 32000,
  task_rules: []
})

const models = ref([])
const config = ref(emptyConfig())
const snapshot = ref(null)
const configError = ref('')
const loadError = ref('')
const saveError = ref('')
const loading = ref(false)
const saving = ref(false)
const { showToast } = useToast()
const emit = defineEmits(['busy', 'dirty', 'saved', 'cancel'])

const enabledModels = computed(() => models.value.filter((model) => model.enabled === true))
const enabledModelIds = computed(() => new Set(enabledModels.value.map((model) => model.id)))
const formErrors = computed(() => {
  const errors = []
  const current = config.value
  if (!Number.isInteger(current.fast_max_chars) || current.fast_max_chars < 1) {
    errors.push('快速任务字符阈值必须是大于 0 的整数。')
  }
  if (!Number.isInteger(current.long_context_min_chars) || current.long_context_min_chars < 1) {
    errors.push('长上下文字符阈值必须是大于 0 的整数。')
  }
  if (Number.isInteger(current.fast_max_chars) && Number.isInteger(current.long_context_min_chars) && current.fast_max_chars >= current.long_context_min_chars) {
    errors.push('快速任务字符上限必须小于长上下文字符起点。')
  }
  if (current.default_model_id && !enabledModelIds.value.has(current.default_model_id)) {
    errors.push('默认模型必须是已启用的模型，或清空该项。')
  }
  if (current.enabled && !enabledModelIds.value.has(current.default_model_id)) {
    errors.push('启用策略时，默认模型必须是已启用的模型。')
  }

  for (const role of ROLE_OPTIONS.slice(1)) {
    const modelId = current[role.key]
    if (modelId && !isModelAllowed(role.key, modelId)) {
      errors.push(`${role.label}必须选择符合条件的已启用模型，或清空该项。`)
    }
  }
  current.task_rules.forEach((rule, index) => {
    if (!FIELD_OPTIONS.some((field) => field.value === rule.field)) {
      errors.push(`第 ${index + 1} 条任务规则的字段无效。`)
    }
    if (!String(rule.value || '').trim()) {
      errors.push(`第 ${index + 1} 条任务规则的匹配值不能为空。`)
    }
    if (!enabledModelIds.value.has(rule.model_id)) {
      errors.push(`第 ${index + 1} 条任务规则必须选择已启用的模型。`)
    }
  })
  return errors
})

const canSave = computed(() => !loading.value && !saving.value && !loadError.value && !configError.value && formErrors.value.length === 0)
const dirty = computed(() => snapshot.value !== null && JSON.stringify(config.value) !== snapshot.value)

watch(saving, (value) => emit('busy', value), { immediate: true })
watch(dirty, (value) => emit('dirty', value), { immediate: true })

function modelName(model) {
  return `${model.name || model.model_id || model.id} · ${model.model_id || model.id}`
}

function isModelAllowed(roleKey, modelId) {
  const model = enabledModels.value.find((item) => item.id === modelId)
  if (!model) return false
  return roleKey !== 'vision_model_id' || model.image_input_mode === 'supported'
}

function optionsFor(roleKey, currentId) {
  const candidates = roleKey === 'vision_model_id'
    ? enabledModels.value.filter((model) => model.image_input_mode === 'supported')
    : enabledModels.value
  const options = candidates.map((model) => ({ id: model.id, label: modelName(model), disabled: false }))
  if (currentId && !options.some((option) => option.id === currentId)) {
    const existing = models.value.find((model) => model.id === currentId)
    options.unshift({
      id: currentId,
      label: existing
        ? `${modelName(existing)}（已停用或不满足图片能力，保留现有值）`
        : `${currentId}（模型不存在，保留现有值）`,
      disabled: true
    })
  }
  return options
}

function parseConfig(raw) {
  if (raw === undefined || raw === null || raw === '') return emptyConfig()
  let parsed
  try {
    parsed = JSON.parse(raw)
  } catch (error) {
    throw new Error(`自动任务选模配置不是有效 JSON：${error.message}`)
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('自动任务选模配置必须是 JSON 对象，当前值已保留且不能覆盖。')
  }
  if (parsed.task_rules !== undefined && !Array.isArray(parsed.task_rules)) {
    throw new Error('自动任务选模配置中的 task_rules 不是数组，当前值已保留且不能覆盖。')
  }
  const next = { ...emptyConfig(), ...parsed }
  if (typeof next.enabled !== 'boolean') {
    throw new Error('自动任务选模配置中的 enabled 必须是布尔值，当前值已保留且不能覆盖。')
  }
  for (const role of ROLE_OPTIONS) {
    if (typeof next[role.key] !== 'string') {
      throw new Error(`自动任务选模配置中的 ${role.key} 必须是字符串，当前值已保留且不能覆盖。`)
    }
  }
  if (!Number.isInteger(next.fast_max_chars) || !Number.isInteger(next.long_context_min_chars)) {
    throw new Error('自动任务选模配置中的字符阈值必须是整数，当前值已保留且不能覆盖。')
  }
  if (!next.task_rules.every((rule) => rule && typeof rule === 'object' && typeof rule.field === 'string' && typeof rule.value === 'string' && typeof rule.model_id === 'string')) {
    throw new Error('自动任务选模配置中的任务规则格式不完整，当前值已保留且不能覆盖。')
  }
  return next
}

async function load() {
  loading.value = true
  snapshot.value = null
  try {
    const [modelsData, settings] = await Promise.all([getModels(), getSettings()])
    models.value = Array.isArray(modelsData) ? modelsData : []
    try {
      config.value = parseConfig(settings?.[SETTINGS_KEY])
      snapshot.value = JSON.stringify(config.value)
    } catch (error) {
      configError.value = error.message || '自动任务选模配置无法解析。'
      showToast(configError.value, { tone: 'error' })
    }
  } catch (error) {
    loadError.value = error?.message || '设置读取失败，无法安全保存。'
    console.error('加载自动任务选模设置失败', error)
    showToast(loadError.value || '加载自动任务选模设置失败', { tone: 'error' })
  } finally {
    loading.value = false
  }
}

function addRule() {
  config.value.task_rules.push({ field: 'agent_task_type', value: '', model_id: '' })
}

function moveRule(index, direction) {
  const target = index + direction
  if (target < 0 || target >= config.value.task_rules.length) return
  const rules = [...config.value.task_rules]
  ;[rules[index], rules[target]] = [rules[target], rules[index]]
  config.value.task_rules = rules
}

function removeRule(index) {
  config.value.task_rules.splice(index, 1)
}

async function save() {
  if (!canSave.value) return
  saving.value = true
  saveError.value = ''
  try {
    const payload = {
      ...config.value,
      task_rules: config.value.task_rules.map((rule) => ({ ...rule, value: rule.value.trim() }))
    }
    await updateSettings({ [SETTINGS_KEY]: JSON.stringify(payload) })
    config.value = payload
    snapshot.value = JSON.stringify(payload)
    showToast('自动任务选模设置已保存', { tone: 'success' })
    emit('saved')
  } catch (error) {
    console.error('保存自动任务选模设置失败', error)
    saveError.value = error?.message || '保存自动任务选模设置失败'
    showToast(saveError.value, { tone: 'error' })
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <section class="flex min-h-0 flex-1 flex-col">
    <div class="min-h-0 flex-1 overflow-y-auto p-4">
      <p class="mb-3 text-[12px] leading-5 text-ink-4">本地规则策略仅作用于 Kiro 主生成请求中的 Auto，capture 模式不适用；真实运行模式由代理控制页管理。</p>
      <div v-if="configError" class="mb-3 rounded-md border border-danger-tint-line bg-danger-tint px-3 py-2 text-[12px] leading-5 text-danger" role="alert">
        {{ configError }} 请先修复原始配置；当前不会允许保存覆盖。
      </div>
      <div v-if="loadError" class="mb-3 rounded-md border border-danger-tint-line bg-danger-tint px-3 py-2 text-[12px] leading-5 text-danger" role="alert">
        {{ loadError }}
      </div>
      <p v-if="loading" class="py-2 text-[12px] text-ink-4">正在加载设置...</p>
      <div v-else-if="!configError && !loadError" class="space-y-3">
       <fieldset class="min-w-0 border-0 p-0" :disabled="saving">
      <div class="flex items-center justify-between gap-4 rounded-lg border border-line-2 bg-surface-2 px-3 py-2.5">
        <div>
          <div class="text-[12px] font-medium text-ink">启用按任务选模</div>
          <p class="mt-0.5 text-[11px] leading-4 text-ink-4">同一聊天的新任务重新选模；连续工具回合保持已选模型，主会话与分支分别记忆。单次执行失败不会切换到其他模型。</p>
        </div>
        <label class="relative inline-flex shrink-0 cursor-pointer items-center">
          <input v-model="config.enabled" type="checkbox" aria-label="启用按任务选模" class="peer sr-only" />
          <span class="h-5 w-9 rounded-full bg-hover-strong after:absolute after:left-[2px] after:top-[2px] after:h-4 after:w-4 after:rounded-full after:border after:border-line-strong after:bg-surface after:transition-transform after:content-[''] peer-checked:bg-primary peer-checked:after:translate-x-full peer-checked:after:border-white"></span>
        </label>
      </div>

      <section class="rounded-lg border border-line-2 p-3">
        <h4 class="mb-2 text-[12px] font-semibold text-ink">模型角色</h4>
        <div class="grid grid-cols-1 gap-2.5 sm:grid-cols-2">
          <div v-for="role in ROLE_OPTIONS" :key="role.key">
            <label :for="`auto-routing-${role.key}`" class="mb-1 block text-[12px] font-medium text-ink-2">{{ role.label }}<span v-if="role.key === 'default_model_id' && config.enabled" class="ml-1 text-danger">必选</span></label>
            <select
              :id="`auto-routing-${role.key}`"
              v-model="config[role.key]"
              class="ui-control-h ui-control w-full px-2.5 text-[12px] text-ink outline-none transition focus:border-primary-tint-line-strong focus:ring-2 focus:ring-primary-ring"
            >
              <option value="">{{ role.key === 'default_model_id' ? '请选择默认模型' : role.key === 'vision_model_id' ? '未配置（图片请求会报错）' : '未配置（跳过该规则，继续匹配）' }}</option>
              <option v-for="option in optionsFor(role.key, config[role.key])" :key="option.id" :value="option.id" :disabled="option.disabled">
                {{ option.label }}
              </option>
            </select>
            <p class="mt-1 text-[11px] leading-4 text-ink-4">{{ role.hint }}</p>
          </div>
        </div>
        <p v-if="enabledModels.length === 0" class="mt-2 text-[11px] text-warning-fg">当前没有已启用模型可供选择。</p>
        <p class="mt-2 text-[11px] leading-4 text-ink-4">角色表示你明确声明的能力，不会根据模型名称推断。图片目标必须在模型配置中标为“支持”。</p>
      </section>

      <section class="rounded-lg border border-line-2 p-3">
        <h4 class="mb-2 text-[12px] font-semibold text-ink">字符阈值</h4>
        <div class="grid grid-cols-1 gap-2.5 sm:grid-cols-2">
          <label class="text-[12px] font-medium text-ink-2">快速任务上限（字符）
            <input v-model.number="config.fast_max_chars" type="number" min="1" step="1" class="ui-control-h ui-control mt-1 w-full px-2.5 text-[12px] text-ink outline-none focus:border-primary-tint-line-strong focus:ring-2 focus:ring-primary-ring" />
          </label>
          <label class="text-[12px] font-medium text-ink-2">长上下文起点（字符）
            <input v-model.number="config.long_context_min_chars" type="number" min="1" step="1" class="ui-control-h ui-control mt-1 w-full px-2.5 text-[12px] text-ink outline-none focus:border-primary-tint-line-strong focus:ring-2 focus:ring-primary-ring" />
          </label>
        </div>
        <p class="mt-1.5 text-[11px] leading-4 text-ink-4">字符数是本地估算条件，不等于 Token 数；快速任务上限须小于长上下文起点。</p>
      </section>

      <section class="rounded-lg border border-line-2 p-3">
        <div class="mb-2 flex flex-wrap items-center justify-between gap-2">
          <div>
            <h4 class="text-[12px] font-semibold text-ink">任务字段规则</h4>
            <p class="mt-0.5 text-[11px] leading-4 text-ink-4">按顺序匹配，首条命中生效；值去首尾空白后忽略大小写精确匹配。例：spec、context-gatherer。</p>
          </div>
          <button type="button" class="h-7 rounded-md border border-line bg-surface px-2.5 text-[11px] font-medium text-ink-2 hover:bg-hover" @click="addRule">＋ 添加规则</button>
        </div>
        <div v-if="config.task_rules.length" class="space-y-2">
          <div v-for="(rule, index) in config.task_rules" :key="index" class="grid grid-cols-1 items-start gap-2 rounded-md bg-surface-2 p-2 sm:grid-cols-[minmax(110px,0.8fr)_minmax(120px,1fr)_minmax(150px,1.4fr)_auto]">
            <select v-model="rule.field" class="ui-control-h ui-control px-2 text-[11px] text-ink outline-none focus:border-primary-tint-line-strong" :aria-label="`第 ${index + 1} 条规则字段`">
              <option v-for="field in FIELD_OPTIONS" :key="field.value" :value="field.value">{{ field.label }}</option>
            </select>
            <input v-model="rule.value" type="text" class="ui-control-h ui-control px-2 text-[11px] text-ink outline-none focus:border-primary-tint-line-strong" :aria-label="`第 ${index + 1} 条规则匹配值`" placeholder="例如 spec" />
            <select v-model="rule.model_id" class="ui-control-h ui-control px-2 text-[11px] text-ink outline-none focus:border-primary-tint-line-strong" :aria-label="`第 ${index + 1} 条规则模型`">
              <option value="">选择已启用模型</option>
              <option v-for="option in optionsFor('task_rule', rule.model_id)" :key="option.id" :value="option.id" :disabled="option.disabled">{{ option.label }}</option>
            </select>
            <div class="flex items-center justify-end gap-1">
              <button type="button" class="h-7 w-7 rounded border border-line bg-surface text-[12px] text-ink-2 hover:bg-hover disabled:opacity-40" :disabled="index === 0" :aria-label="`第 ${index + 1} 条规则上移`" @click="moveRule(index, -1)">↑</button>
              <button type="button" class="h-7 w-7 rounded border border-line bg-surface text-[12px] text-ink-2 hover:bg-hover disabled:opacity-40" :disabled="index === config.task_rules.length - 1" :aria-label="`第 ${index + 1} 条规则下移`" @click="moveRule(index, 1)">↓</button>
              <button type="button" class="h-7 rounded border border-danger-tint-line bg-surface px-2 text-[11px] text-danger hover:bg-danger-tint" :aria-label="`删除第 ${index + 1} 条规则`" @click="removeRule(index)">删除</button>
            </div>
          </div>
        </div>
        <p v-else class="rounded-md border border-dashed border-line px-3 py-3 text-center text-[11px] text-ink-4">尚无任务规则；可以只使用模型角色和默认模型。</p>
        <p class="mt-2 text-[11px] leading-4 text-ink-4">请求中声明的 tools 多数只是可用工具，不代表当前任务一定是编码。</p>
        <p class="mt-1 text-[11px] leading-4 text-ink-4">选择顺序：图片模型 → 首条命中任务规则 → 高推理 → 长上下文 → 快速模型 → 默认模型。</p>
      </section>

      <ul v-if="formErrors.length" class="space-y-1 rounded-md border border-warning-tint-line bg-warning-tint px-3 py-2 text-[11px] leading-4 text-warning-fg">
        <li v-for="error in formErrors" :key="error">{{ error }}</li>
      </ul>
       </fieldset>
      </div>
      <div v-if="saveError" class="mt-3 rounded-md border border-danger-tint-line bg-danger-tint px-3 py-2 text-[12px] leading-5 text-danger" role="alert">{{ saveError }}</div>
    </div>
    <footer class="flex shrink-0 items-center justify-end gap-2 border-t border-line-2 px-4 py-3">
      <button type="button" class="h-9 rounded-md border border-line bg-surface px-5 text-xs text-ink-2 shadow-sm hover:bg-hover disabled:cursor-not-allowed disabled:opacity-50" :disabled="saving" @click="emit('cancel')">取消</button>
      <button type="button" class="h-9 rounded-md bg-primary px-5 text-xs font-medium text-white shadow-sm hover:bg-primary-hover disabled:cursor-not-allowed disabled:opacity-50" :disabled="!canSave" @click="save">
        {{ saving ? '保存中...' : '保存选模设置' }}
      </button>
    </footer>
  </section>
</template>
