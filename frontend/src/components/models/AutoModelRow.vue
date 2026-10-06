<script setup>
import { computed, ref, watch } from 'vue'
import { getSettings } from '../../api/settings.js'
import { AUTO_ROUTING_SETTINGS_KEY, AUTO_STAGE_OPTIONS, parseAutoStageConfig } from '../../utils/autoStageConfig.js'

const STAGE_KEYS = AUTO_STAGE_OPTIONS.map((stage) => stage.key)

const props = defineProps({
  models: { type: Array, default: () => [] },
  refreshToken: { type: Number, default: 0 },
})

const emit = defineEmits(['configure'])
const summary = ref(null)
const legacyConfig = ref(false)
const readError = ref('')
const loading = ref(false)
let readSequence = 0

const modelById = computed(() => new Map(props.models.map((model) => [model.id, model])))
const targetIds = computed(() => {
  if (legacyConfig.value || !summary.value?.enabled) return []
  return [...new Set([
    summary.value.default_model_id,
    ...STAGE_KEYS.map((key) => summary.value.stage_models[key]),
  ].filter(Boolean))]
})
const targetRates = computed(() => targetIds.value.map((id) => {
  const model = modelById.value.get(id)
  if (!model) return `${id}（模型不存在）`
  const name = model.name || model.model_id || id
  return `${name}：${formatPrice(model.input_price)} / ${formatPrice(model.output_price)}`
}))
const configuredStageCount = computed(() => STAGE_KEYS.filter((key) => summary.value?.stage_models[key]).length)
const stageTargetSummary = computed(() => STAGE_KEYS.map((key) => `${key}：${targetName(summary.value?.stage_models[key])}`).join('；'))
const defaultTargetName = computed(() => {
  const id = summary.value?.default_model_id
  if (!id) return '未配置固定目标'
  return targetName(id)
})
const defaultTargetLabel = computed(() => legacyConfig.value ? '待保存固定目标：' : '固定目标：')
const routingLabel = computed(() => {
  if (legacyConfig.value) return '五阶段待配置（旧配置待替换）'
  if (summary.value?.enabled) return '五阶段固定模型（已启用）'
  return configuredStageCount.value === STAGE_KEYS.length && summary.value?.default_model_id
    ? '五阶段固定模型（未启用）'
    : '五阶段固定模型（待配置，未启用）'
})

function targetName(id) {
  if (!id) return '未配置'
  const model = modelById.value.get(id)
  return model ? (model.name || model.model_id || id) : `${id}（模型不存在）`
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
  legacyConfig.value = false
  try {
    const settings = await getSettings()
    const next = parseAutoStageConfig(settings?.[AUTO_ROUTING_SETTINGS_KEY])
    if (sequence === readSequence) {
      summary.value = next.config
      legacyConfig.value = next.legacy
    }
  } catch (error) {
    if (sequence === readSequence) readError.value = error?.message || 'Auto 模型配置读取失败。'
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
          <span>内置模型路由</span>
        </div>
      </div>

      <div class="min-w-0 px-2 text-[12px] leading-5">
        <div v-if="loading" class="text-ink-4">正在读取路由设置...</div>
        <div v-else-if="readError" class="truncate text-danger" role="alert" :title="readError">{{ readError }}</div>
        <template v-else-if="summary">
          <div class="font-medium text-ink-2">{{ routingLabel }}</div>
          <div class="mt-1 truncate text-[11px] text-ink-3" :title="`${defaultTargetLabel}${defaultTargetName}`">{{ defaultTargetLabel }}{{ defaultTargetName }}</div>
          <div class="mt-1 truncate text-[11px] text-ink-4" :title="stageTargetSummary">
            {{ configuredStageCount }}/5 个阶段已配置<span v-if="legacyConfig">（待保存）</span><span v-else-if="!summary.enabled">（未启用）</span>
          </div>
        </template>
      </div>

      <div class="min-w-0 px-2 text-[11px] leading-4">
        <div class="font-medium text-ink-3">按实际选中模型计价</div>
        <div v-if="loading" class="mt-1 text-ink-4">读取中...</div>
        <div v-else-if="readError" class="mt-1 truncate text-danger" :title="readError">读取失败</div>
        <div v-else-if="legacyConfig" class="mt-1 text-ink-4">新版 Auto 需先配置五阶段模型</div>
        <div v-else-if="summary && !summary.enabled" class="mt-1 text-ink-4">由默认路由决定</div>
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
