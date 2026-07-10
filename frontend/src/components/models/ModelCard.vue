<script setup>
import { computed } from 'vue'
import IconPlay from '../icons/IconPlay.vue'
import IconEdit from '../icons/IconEdit.vue'
import IconTrash from '../icons/IconTrash.vue'

const props = defineProps({
  model: { type: Object, required: true },
  testResult: { type: Object, default: null },
  testing: { type: Boolean, default: false },
  refreshingCodex: { type: Boolean, default: false }
})

const emit = defineEmits(['edit', 'delete', 'test', 'refresh-codex'])

const displayName = computed(() => props.model.name || props.model.model_id)
const maskedKey = computed(() => {
  const key = props.model.api_key || ''
  if (key.length <= 8) return '••••••••'
  return key.slice(0, 4) + '••••' + key.slice(-4)
})
const truncatedUrl = computed(() => {
  const url = props.model.base_url || ''
  return url.length > 40 ? url.slice(0, 40) + '...' : url
})
const truncatedProxy = computed(() => {
  const url = props.model.proxy_url || ''
  return url.length > 40 ? url.slice(0, 40) + '...' : url
})
const providerLabel = computed(() => {
  if (props.model.provider === 'anthropic') return 'Anthropic'
  if (props.model.provider === 'gemini') return 'Gemini'
  if (props.model.provider === 'codex') return 'Codex'
  return 'OpenAI'
})
const codexUsage = computed(() => {
  if (!props.model.codex_usage_json) return null
  try {
    return JSON.parse(props.model.codex_usage_json)
  } catch {
    return null
  }
})
const codexRateLimit = computed(() => codexUsage.value?.rate_limit || null)
const codexWindows = computed(() => {
  const rate = codexRateLimit.value
  if (!rate) return []
  return [
    { label: '5小时', window: rate.primary_window },
    { label: '本周', window: rate.secondary_window },
  ].filter(item => item.window)
})

function formatReset(seconds) {
  const value = Number(seconds || 0)
  if (value <= 0) return '即将重置'
  const days = Math.floor(value / 86400)
  const hours = Math.floor((value % 86400) / 3600)
  const minutes = Math.floor((value % 3600) / 60)
  if (days > 0) return `${days}天${hours}小时后`
  if (hours > 0) return `${hours}小时${minutes}分钟后`
  return `${minutes}分钟后`
}

function remainingPercent(row) {
  const used = Number(row?.used_percent || 0)
  return Math.max(0, 100 - used)
}
</script>

<template>
  <div class="bg-white border border-[#e5e7eb] rounded-lg p-4 flex flex-col gap-3 hover:border-[#d1d5db] transition-colors">
    <div class="flex items-start justify-between">
      <div class="flex-1 min-w-0">
        <h3 class="text-sm font-medium text-[#111827] truncate">{{ displayName }}</h3>
        <p class="text-xs text-[#6b7280] mt-0.5 font-mono">{{ model.model_id }}</p>
      </div>
      <span class="shrink-0 ml-2 px-2 py-0.5 text-[10px] font-medium rounded bg-[#ecfdf5] text-[#10b981] border border-[#10b981]/20">
        {{ providerLabel }}
      </span>
    </div>

    <div class="flex flex-col gap-1.5 text-xs">
      <div class="flex items-center gap-2">
        <span class="text-[#6b7280] w-12 shrink-0">HOST</span>
        <span class="text-[#111827] truncate">{{ truncatedUrl }}</span>
      </div>
      <div class="flex items-center gap-2">
        <span class="text-[#6b7280] w-12 shrink-0">KEY</span>
        <span class="text-[#111827] font-mono text-[11px]">{{ maskedKey }}</span>
      </div>
      <div class="flex items-center gap-2">
        <span class="text-[#6b7280] w-12 shrink-0">端点</span>
        <span class="text-[#111827]">{{ model.endpoint }}</span>
      </div>
      <div class="flex items-center gap-2">
        <span class="text-[#6b7280] w-12 shrink-0">后缀</span>
        <span class="text-[#111827]">{{ model.disable_auto_suffix ? '不自动添加' : '自动处理' }}</span>
      </div>
      <div class="flex items-center gap-2">
        <span class="text-[#6b7280] w-12 shrink-0">代理</span>
        <span class="text-[#111827] truncate">{{ truncatedProxy || '直连' }}</span>
      </div>
      <div class="flex items-center gap-2">
        <span class="text-[#6b7280] w-12 shrink-0">倍率</span>
        <span class="text-[#111827] tabular-nums">{{ model.rate_multiplier || 1 }}</span>
      </div>
      <div class="flex items-center gap-2">
        <span class="text-[#6b7280] w-12 shrink-0">价格</span>
        <span class="text-[#111827] tabular-nums">in {{ model.input_price }} / out {{ model.output_price }} / cache {{ model.cache_input_price }} / write {{ model.cache_output_price }}</span>
      </div>
      <template v-if="model.provider === 'codex'">
        <div class="flex items-center gap-2">
          <span class="text-[#6b7280] w-12 shrink-0">账号</span>
          <span class="text-[#111827] truncate">{{ model.codex_email || '-' }}</span>
        </div>
        <div class="flex items-center gap-2">
          <span class="text-[#6b7280] w-12 shrink-0">类型</span>
          <span class="text-[#111827] truncate">{{ model.codex_plan_type || '-' }}</span>
        </div>
        <div v-if="codexWindows.length" class="flex flex-col gap-1.5 mt-1">
          <div v-for="item in codexWindows" :key="item.label" class="rounded border border-[#e5e7eb] bg-[#f9fafb] px-2 py-1.5">
            <div class="flex items-center justify-between text-[11px] mb-1">
              <span class="text-[#374151] font-medium">{{ item.label }}限制</span>
              <span class="text-[#6b7280]">剩余 {{ remainingPercent(item.window) }}%</span>
            </div>
            <div class="h-1.5 bg-[#e5e7eb] rounded-full overflow-hidden">
              <div class="h-full bg-[#10b981]" :style="{ width: `${remainingPercent(item.window)}%` }"></div>
            </div>
            <div class="flex items-center justify-between text-[10px] text-[#6b7280] mt-1">
              <span>已用 {{ item.window.used_percent }}%</span>
              <span>{{ formatReset(item.window.reset_after_seconds) }}</span>
            </div>
          </div>
        </div>
      </template>
    </div>

    <div v-if="testResult"
      class="text-xs px-2 py-1.5 rounded border"
      :class="testResult.ok
        ? 'bg-[#ecfdf5] border-[#10b981]/20 text-[#10b981]'
        : 'bg-[#fef2f2] border-[#ef4444]/20 text-[#ef4444]'">
      <template v-if="testResult.ok">
        {{ Math.round(testResult.tokens_per_sec) }} t/s | 首字 {{ (testResult.ttfb_ms / 1000).toFixed(1) }}s
      </template>
      <template v-else>
        {{ testResult.error }}
      </template>
    </div>

    <div class="flex items-center gap-3 pt-2 border-t border-[#e5e7eb]">
      <button class="flex items-center gap-1 text-xs text-[#6b7280] hover:text-[#10b981] disabled:opacity-50"
        :disabled="testing" @click="emit('test')">
        <IconPlay class="w-3 h-3" />
        {{ testing ? '测试中...' : '测试' }}
      </button>
      <button v-if="model.provider === 'codex'"
        class="flex items-center gap-1 text-xs text-[#6b7280] hover:text-[#10b981] disabled:opacity-50"
        :disabled="refreshingCodex" @click="emit('refresh-codex')">
        {{ refreshingCodex ? '刷新中...' : '刷新信息' }}
      </button>
      <button class="flex items-center gap-1 text-xs text-[#6b7280] hover:text-[#111827]" @click="emit('edit')">
        <IconEdit class="w-3 h-3" />
        编辑
      </button>
      <button class="flex items-center gap-1 text-xs text-[#6b7280] hover:text-[#ef4444]" @click="emit('delete')">
        <IconTrash class="w-3 h-3" />
        删除
      </button>
    </div>
  </div>
</template>
