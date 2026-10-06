<script setup>
import { computed, ref, watch } from 'vue'
import { getSettings } from '../../api/settings.js'

const SETTINGS_KEY = 'auto_routing_config'

const props = defineProps({
  models: { type: Array, default: () => [] },
  refreshToken: { type: Number, default: 0 },
})

const emit = defineEmits(['configure'])
const summary = ref(null)
const readError = ref('')
const loading = ref(false)
let readSequence = 0

const modelById = computed(() => new Map(props.models.map((model) => [model.id, model])))
const targetIds = computed(() => {
  if (!summary.value?.enabled) return []
  return [...new Set([
    summary.value.default_model_id,
    summary.value.vision_model_id,
    summary.value.reasoning_model_id,
    summary.value.long_context_model_id,
    summary.value.fast_model_id,
    ...summary.value.task_rules.map((rule) => rule.model_id),
  ].filter(Boolean))]
})
const targetRates = computed(() => targetIds.value.map((id) => {
  const model = modelById.value.get(id)
  if (!model) return `${id}（模型不存在）`
  const name = model.name || model.model_id || id
  return `${name}：${formatPrice(model.input_price)} / ${formatPrice(model.output_price)}`
}))
const defaultTargetName = computed(() => {
  if (summary.value && !summary.value.enabled) return '按当前默认候选路由'
  const id = summary.value?.default_model_id
  if (!id) return '未配置默认模型'
  const model = modelById.value.get(id)
  return model ? (model.name || model.model_id || id) : `${id}（模型不存在）`
})
const routingLabel = computed(() => summary.value?.enabled ? '按任务选模' : '兼容默认路由')

function emptyConfig() {
  return {
    enabled: false,
    default_model_id: '',
    vision_model_id: '',
    reasoning_model_id: '',
    long_context_model_id: '',
    fast_model_id: '',
    fast_max_chars: 500,
    long_context_min_chars: 32000,
    task_rules: [],
  }
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
    throw new Error('自动任务选模配置必须是 JSON 对象。')
  }
  if (parsed.task_rules !== undefined && !Array.isArray(parsed.task_rules)) {
    throw new Error('自动任务选模配置中的 task_rules 不是数组。')
  }
  const next = { ...emptyConfig(), ...parsed }
  if (typeof next.enabled !== 'boolean') {
    throw new Error('自动任务选模配置中的 enabled 必须是布尔值。')
  }
  for (const key of ['default_model_id', 'vision_model_id', 'reasoning_model_id', 'long_context_model_id', 'fast_model_id']) {
    if (typeof next[key] !== 'string') throw new Error(`自动任务选模配置中的 ${key} 必须是字符串。`)
  }
  if (!Number.isInteger(next.fast_max_chars) || !Number.isInteger(next.long_context_min_chars)) {
    throw new Error('自动任务选模配置中的字符阈值必须是整数。')
  }
  if (!next.task_rules.every((rule) => rule && typeof rule === 'object' && typeof rule.field === 'string' && typeof rule.value === 'string' && typeof rule.model_id === 'string')) {
    throw new Error('自动任务选模配置中的任务规则格式不完整。')
  }
  return next
}

function formatPrice(value) {
  const num = Number(value || 0)
  if (!Number.isFinite(num) || num === 0) return '$0'
  if (num < 0.001) return `$${num.toFixed(5).replace(/0+$/, '')}`
  return `$${num.toFixed(4).replace(/0+$/, '').replace(/\.$/, '')}`
}

async function loadSummary() {
  const sequence = ++readSequence
  loading.value = true
  readError.value = ''
  summary.value = null
  try {
    const settings = await getSettings()
    const next = parseConfig(settings?.[SETTINGS_KEY])
    if (sequence === readSequence) summary.value = next
  } catch (error) {
    if (sequence === readSequence) readError.value = error?.message || '自动任务选模配置读取失败。'
  } finally {
    if (sequence === readSequence) loading.value = false
  }
}

watch(() => props.refreshToken, loadSummary, { immediate: true })
</script>

<template>
  <div data-auto-model-row class="border-b border-line-2 bg-surface">
    <div class="grid min-w-[1153px] grid-cols-[48px_205px_400px_225px_133px_142px] min-h-[76px] items-center">
      <div class="flex justify-center px-2">
        <span class="rounded-full border border-primary-tint-line bg-primary-tint px-1.5 py-1 text-[9px] font-bold tracking-wide text-primary-fg">AUTO</span>
      </div>

      <div class="min-w-0 px-3">
        <div class="text-[14px] font-semibold leading-5 text-ink">Auto</div>
        <div class="mt-1 flex items-center gap-1.5 text-[11px] leading-4 text-ink-3">
          <span class="font-mono text-ink-2">auto</span>
          <span>内置默认配置</span>
        </div>
      </div>

      <div class="min-w-0 px-2 text-[12px] leading-5">
        <div v-if="loading" class="text-ink-4">正在读取路由设置...</div>
        <div v-else-if="readError" class="truncate text-danger" role="alert" :title="readError">{{ readError }}</div>
        <template v-else-if="summary">
          <div class="font-medium text-ink-2">{{ routingLabel }}</div>
          <div class="mt-1 truncate text-[11px] text-ink-3" :title="`${summary.enabled ? '默认：' : ''}${defaultTargetName}`">{{ summary.enabled ? '默认：' : '' }}{{ defaultTargetName }}</div>
          <div class="mt-1 truncate text-[11px] text-ink-4" :title="`${summary.task_rules.length} 条任务规则${summary.enabled ? '' : '（未启用）'}`">
            {{ summary.task_rules.length }} 条任务规则<span v-if="!summary.enabled">（未启用）</span>
          </div>
        </template>
      </div>

      <div class="min-w-0 px-2 text-[11px] leading-4">
        <div class="font-medium text-ink-3">按实际选中模型计价</div>
        <div v-if="loading" class="mt-1 text-ink-4">读取中...</div>
        <div v-else-if="readError" class="mt-1 truncate text-danger" :title="readError">读取失败</div>
        <div v-else-if="summary && !summary.enabled" class="mt-1 text-ink-4">由兼容默认路由决定</div>
        <div v-else-if="targetRates.length" class="mt-1 truncate text-ink-2" :title="targetRates.join('；')">{{ targetRates.join('；') }}</div>
        <div v-else class="mt-1 text-ink-4">未配置目标模型</div>
        <div class="mt-0.5 text-[10px] text-ink-4">USD / 1M tokens，输入 / 输出</div>
      </div>

      <div class="px-3">
        <div class="inline-flex rounded-[5px] bg-surface-2 px-2 py-[3px] text-[11px] font-semibold leading-4 text-ink-2">内置入口</div>
        <div class="mt-1 text-[11px] leading-4 text-ink-4">不计入模型统计</div>
      </div>

      <div class="flex items-center justify-start px-1">
        <button
          type="button"
          class="inline-flex h-[30px] w-[70px] items-center justify-center ui-control bg-surface px-1 text-[12px] font-medium leading-none text-primary-fg hover:border-primary-tint-line hover:bg-primary-tint"
          aria-label="配置 Auto"
          title="配置 Auto"
          @click="emit('configure')"
        >配置</button>
      </div>
    </div>
  </div>
</template>
