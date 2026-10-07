<script setup>
import { computed, ref, watch } from 'vue'
import { getSettings } from '../../api/settings.js'
import {
  AUTO_DUTY_LAYOUTS,
  AUTO_DUTY_LAYOUT_LABELS,
  AUTO_ROUTING_SETTINGS_KEY,
  autoTargetDefaultEffort,
  autoTargetGroupCandidates,
  parseAutoDutyConfig,
  resolveAutoTarget,
} from '../../utils/autoDutyConfig.js'

const props = defineProps({
  models: { type: Array, default: () => [] },
  groups: { type: Array, default: () => [] },
  refreshToken: { type: Number, default: 0 },
})

const emit = defineEmits(['configure'])
const summary = ref(null)
const legacyConfig = ref(false)
const readError = ref('')
const loading = ref(false)
let readSequence = 0

const duties = computed(() => summary.value ? AUTO_DUTY_LAYOUTS[summary.value.layout] || [] : [])
const configuredDutyCount = computed(() => duties.value.filter((duty) => summary.value?.duty_models?.[duty.key]?.model_id).length)
const targetIds = computed(() => {
  if (!summary.value) return []
  return [...new Set([
    summary.value.default_model_id,
    ...duties.value.map((duty) => summary.value.duty_models?.[duty.key]?.model_id),
    ...summary.value.node_overrides.map((item) => item.model_id),
  ].filter(Boolean))]
})
const targetRates = computed(() => targetIds.value.map((id) => {
  const target = resolveAutoTarget(id, props.models, props.groups)
  if (target.kind === 'collision') return `${id}（模型/分组 ID 冲突）`
  if (target.kind === 'missing') return `${id}（配置不存在）`
  if (target.kind === 'model') return modelRate(target.model)
  const candidates = autoTargetGroupCandidates(id, props.models, props.groups)
  const prices = candidates.length ? candidates.map((model) => modelRate(model, true)).join('、') : '无已启用候选成员'
  return `分组：${target.group.name || id} · ${id}（成员价格：${prices}）`
}))
const dutyTargetSummary = computed(() => duties.value.map((duty) => {
  const target = summary.value?.duty_models?.[duty.key]
  if (!target?.model_id) return `${duty.label}：未配置`
  return `${duty.label}：${targetIdentity(target.model_id)} / ${target.reasoning_effort || '上游默认'}`
}).join('；'))
const defaultTargetSummary = computed(() => {
  const id = summary.value?.default_model_id
  if (!id) return '默认目标：未配置'
  return `默认目标：${targetIdentity(id)} / ${autoTargetDefaultEffort(id, props.models, props.groups) || '上游默认'}`
})
const routingLabel = computed(() => {
  if (legacyConfig.value) return '旧 Auto 配置待迁移'
  if (readError.value) return 'Auto 配置读取失败'
  if (!summary.value) return 'Auto 职责路由待配置'
  const layout = AUTO_DUTY_LAYOUT_LABELS[summary.value.layout] || '未知布局'
  return `${layout} · ${summary.value.enabled ? '已启用' : '未启用'}`
})

function formatPrice(value) {
  const num = Number(value || 0)
  if (!Number.isFinite(num) || num === 0) return '$0'
  if (num < 0.001) return `$${num.toFixed(5).replace(/0+$/, '')}`
  return `$${num.toFixed(4).replace(/0+$/, '').replace(/\.$/, '')}`
}

function modelRate(model, includeConfigID = false) {
  const identity = includeConfigID
    ? `${model.name || model.model_id || model.id} · ${model.model_id || model.id} · ${model.id}`
    : model.model_id || model.id
  return `${identity} · ${model.provider || 'openai'}：${formatPrice(model.input_price)} / ${formatPrice(model.output_price)}`
}

function targetIdentity(id) {
  const target = resolveAutoTarget(id, props.models, props.groups)
  if (target.kind === 'model') return `模型：${target.model.model_id || id} · ${id} · ${target.model.provider || 'openai'}`
  if (target.kind === 'group') return `分组：${target.group.name || id} · ${id}`
  if (target.kind === 'collision') return `${id}（模型/分组 ID 冲突）`
  return `${id}（配置不存在）`
}

async function loadSummary() {
  const sequence = ++readSequence
  loading.value = true
  readError.value = ''
  summary.value = null
  legacyConfig.value = false
  try {
    const settings = await getSettings()
    const next = parseAutoDutyConfig(settings?.[AUTO_ROUTING_SETTINGS_KEY])
    if (sequence === readSequence) {
      summary.value = next.config
      legacyConfig.value = next.legacy
    }
  } catch (error) {
    if (sequence === readSequence) readError.value = error?.message || 'Auto 职责配置读取失败。'
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
          <span>职责固定路由</span>
        </div>
      </div>

      <div class="min-w-0 px-2 text-[12px] leading-5">
        <div v-if="loading" class="text-ink-4">正在读取职责路由...</div>
        <div v-else-if="readError" class="truncate text-danger" role="alert" :title="readError">{{ readError }}</div>
        <template v-else-if="legacyConfig">
          <div class="font-medium text-warning-fg">{{ routingLabel }}</div>
          <div class="mt-1 truncate text-[11px] text-ink-3">旧值保留，迁移预览待确认</div>
        </template>
        <template v-else-if="summary">
          <div class="font-medium text-ink-2">{{ routingLabel }}</div>
          <div class="mt-1 truncate text-[11px] text-ink-3" :title="defaultTargetSummary">{{ defaultTargetSummary }}</div>
          <div class="mt-1 truncate text-[11px] text-ink-4" :title="dutyTargetSummary">{{ configuredDutyCount }}/{{ duties.length }} 个职责已配置</div>
        </template>
      </div>

      <div class="min-w-0 px-2 text-[11px] leading-4">
        <div class="font-medium text-ink-3">按实际模型计价，分组列出候选</div>
        <div v-if="loading" class="mt-1 text-ink-4">读取中...</div>
        <div v-else-if="readError" class="mt-1 truncate text-danger" :title="readError">读取失败</div>
        <div v-else-if="legacyConfig" class="mt-1 text-ink-4">迁移后显示职责目标</div>
        <div v-else-if="summary && targetRates.length" class="mt-1 truncate text-ink-2" :title="targetRates.join('；')">{{ targetRates.join('；') }}</div>
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
          aria-label="配置 Auto 职责路由"
          title="配置 Auto 职责路由"
          @click="emit('configure')"
        >配置</button>
      </div>
    </div>
  </div>
</template>
