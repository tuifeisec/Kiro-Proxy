<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useDailyStats } from '../../composables/useStats.js'
import IconRefresh from '../icons/IconRefresh.vue'
import { ALL_STATS_START_DATE, statsDaysAgo } from '../../utils/date.js'
import { formatTokenRate, formatTokens, formatUsd } from '../../utils/format.js'

const props = defineProps({
  refreshKey: { type: Number, default: 0 },
  days: { type: Number, default: 0 },
})
const emit = defineEmits(['summary', 'range-change'])
const { rows, loading, error, range, load } = useDailyStats()
const validationError = ref('')
const isAllRange = computed(() => props.days < 0)

const startDate = ref('')
const endDate = ref('')
let lastAutoStart = ''
let lastAutoEnd = ''
let refreshInFlight = false
let refreshPending = false
let refreshTimer = null
let disposed = false

function applyDaysRange() {
  startDate.value = isAllRange.value ? ALL_STATS_START_DATE : statsDaysAgo(props.days)
  endDate.value = statsDaysAgo(0)
  lastAutoStart = startDate.value
  lastAutoEnd = endDate.value
}

function refreshTimerTick() {
  if (document.hidden) return
  const nextStart = isAllRange.value ? ALL_STATS_START_DATE : statsDaysAgo(props.days)
  const nextEnd = statsDaysAgo(0)
  if (startDate.value === lastAutoStart && endDate.value === lastAutoEnd) {
    startDate.value = nextStart
    endDate.value = nextEnd
    lastAutoStart = nextStart
    lastAutoEnd = nextEnd
  }
  void refresh()
}

function handleVisibilityChange() {
  if (!document.hidden) refreshTimerTick()
}

applyDaysRange()

async function refresh() {
  if (refreshInFlight) {
    refreshPending = true
    return
  }
  if (startDate.value && endDate.value && startDate.value > endDate.value) {
    validationError.value = '开始日期不能晚于结束日期'
    return
  }
  validationError.value = ''
  refreshInFlight = true
  try {
    await load(startDate.value, endDate.value)
    if (!error.value) {
      emit('range-change', {
        all: isAllRange.value,
        start: range.value.start || startDate.value,
        end: range.value.end || endDate.value,
      })
    }
  } finally {
    refreshInFlight = false
    if (!disposed && refreshPending) {
      refreshPending = false
      void refresh()
    }
  }
}

onMounted(() => {
  refresh()
  refreshTimer = setInterval(refreshTimerTick, 5000)
  document.addEventListener('visibilitychange', handleVisibilityChange)
})
watch(() => props.refreshKey, refresh)
watch(() => props.days, async () => {
  applyDaysRange()
  await refresh()
})
onBeforeUnmount(() => {
  disposed = true
  if (refreshTimer) clearInterval(refreshTimer)
  refreshTimer = null
  document.removeEventListener('visibilitychange', handleVisibilityChange)
})

function statNum(row, ...keys) {
  for (const key of keys) {
    const value = Number(row?.[key] ?? 0)
    if (value) return value
  }
  return 0
}

function cacheHitTokens(row) {
  return statNum(row, 'cache_read_tokens', 'cache_read')
}

function cacheWriteTokens(row) {
  return statNum(row, 'cache_write_tokens', 'cache_write')
}

const byModel = computed(() => {
  const map = new Map()
  for (const r of rows.value) {
    const cur = map.get(r.model_id) || {
      model_id: r.model_id,
      requests: 0,
      errors: 0,
      input_tokens: 0,
      output_tokens: 0,
      cache_write_tokens: 0,
      cache_hit_tokens: 0,
      cost_usd: 0,
      generation_speed_sum: 0,
      generation_speed_samples: 0,
    }
    cur.requests += r.requests
    cur.errors += r.errors
    cur.input_tokens += r.input_tokens
    cur.output_tokens += r.output_tokens
    cur.cache_write_tokens += cacheWriteTokens(r)
    cur.cache_hit_tokens += cacheHitTokens(r)
    cur.cost_usd += Number(r.cost_usd || 0)
    cur.generation_speed_sum += Number(r.generation_speed_sum ?? 0)
    cur.generation_speed_samples += Number(r.generation_speed_samples ?? 0)
    map.set(r.model_id, cur)
  }
  return Array.from(map.values()).sort((a, b) => (b.input_tokens + b.output_tokens) - (a.input_tokens + a.output_tokens))
})

const summary = computed(() => byModel.value.reduce((total, row) => {
  total.total_requests += Number(row.requests || 0)
  total.total_errors += Number(row.errors || 0)
  total.input_tokens += Number(row.input_tokens || 0)
  total.output_tokens += Number(row.output_tokens || 0)
  total.cache_read_tokens += Number(row.cache_hit_tokens || 0)
  total.cache_write_tokens += Number(row.cache_write_tokens || 0)
  total.cost_usd += Number(row.cost_usd || 0)
  return total
}, {
  total_requests: 0,
  total_errors: 0,
  input_tokens: 0,
  output_tokens: 0,
  cache_read_tokens: 0,
  cache_write_tokens: 0,
  cost_usd: 0,
}))

const speedSummary = computed(() => byModel.value.reduce((total, row) => {
  total.sum += row.generation_speed_sum
  total.samples += row.generation_speed_samples
  return total
}, { sum: 0, samples: 0 }))

function averageTokenRate(row) {
  return row.generation_speed_samples > 0
    ? row.generation_speed_sum / row.generation_speed_samples
    : null
}

watch(summary, (value) => {
  emit('summary', {
    ...value,
    total_tokens: value.input_tokens + value.output_tokens,
  })
}, { immediate: true })

function cacheHitRate(row) {
  const totalIn = row.input_tokens || 0
  const cached = Math.min(row.cache_hit_tokens ?? cacheHitTokens(row), totalIn)
  if (totalIn === 0) return 0
  return (cached / totalIn) * 100
}

</script>

<template>
  <div class="ui-card">
    <div class="flex flex-wrap items-center justify-between gap-2 border-b border-line-2 px-3 py-2">
      <div class="min-w-0">
        <h3 class="text-[14px] font-semibold text-ink">用量明细</h3>
        <p class="mt-0.5 text-[12px] text-ink-3">按模型聚合 Token、费用（USD）和缓存命中</p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <template v-if="!isAllRange">
          <input v-model="startDate" type="date" class="h-8 rounded-lg border border-line bg-surface px-2 text-[12px] text-ink-2" />
          <span class="text-[12px] text-ink-4">至</span>
          <input v-model="endDate" type="date" class="h-8 rounded-lg border border-line bg-surface px-2 text-[12px] text-ink-2" />
        </template>
        <span v-else class="inline-flex h-8 items-center rounded-lg border border-line bg-surface-2 px-3 text-[12px] text-ink-3">全部历史数据</span>
        <button
          class="inline-flex h-8 items-center gap-1.5 rounded-lg border border-line bg-surface px-3 text-[12px] font-medium text-ink-2 hover:bg-hover disabled:opacity-60"
          :disabled="loading"
          @click="refresh"
        >
          <IconRefresh class="h-3.5 w-3.5" :class="{ 'animate-spin': loading }" />
          {{ loading ? '加载中' : '刷新' }}
        </button>
      </div>
    </div>

    <div v-if="validationError || error" class="mx-4 mt-3 rounded-lg border border-danger-tint-line bg-danger-tint px-3 py-2 text-[12px] text-danger">{{ validationError || error }}</div>

    <div v-if="rows.length === 0 && !loading" class="py-12 text-center text-[13px] text-ink-3">该时间段无数据</div>

    <div v-else class="overflow-x-auto px-3 pb-3 pt-2">
      <table class="usage-table w-full table-auto text-[12px]">
        <thead>
          <tr class="border-b border-line-2 text-left text-ink-3">
            <th class="model-column font-medium">模型</th>
            <th class="compact-column text-right font-medium">请求数</th>
            <th class="compact-column text-right font-medium">错误</th>
            <th class="compact-column text-right font-medium">输入 Token</th>
            <th class="compact-column text-right font-medium">输出 Token</th>
            <th class="compact-column text-right font-medium">缓存命中</th>
            <th class="compact-column text-right font-medium">费用（USD）</th>
            <th class="compact-column text-right font-medium">平均 tok/s</th>
            <th class="compact-column text-right font-medium">命中率</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="m in byModel" :key="m.model_id" class="border-b border-line-2 last:border-b-0 hover:bg-hover">
            <td class="model-column font-medium text-ink" :title="m.model_id"><span class="model-name">{{ m.model_id }}</span></td>
            <td class="compact-column text-right tabular-nums text-ink-2">{{ m.requests }}</td>
            <td class="compact-column text-right tabular-nums" :class="m.errors > 0 ? 'text-danger' : 'text-ink-4'">{{ m.errors }}</td>
            <td class="compact-column text-right tabular-nums text-ink-2">{{ formatTokens(m.input_tokens) }}</td>
            <td class="compact-column text-right tabular-nums text-ink-2">{{ formatTokens(m.output_tokens) }}</td>
            <td class="compact-column text-right tabular-nums text-success">{{ formatTokens(m.cache_hit_tokens) }}</td>
            <td class="compact-column text-right tabular-nums text-warning">{{ formatUsd(m.cost_usd) }}</td>
            <td class="compact-column text-right tabular-nums text-ink-2">{{ formatTokenRate(averageTokenRate(m)) }}</td>
            <td class="compact-column text-right tabular-nums">
              <div class="flex items-center justify-end gap-1">
                <span class="cache-rate-value text-ink-2">{{ cacheHitRate(m).toFixed(1) }}%</span>
                <span class="cache-rate-bar h-1.5 w-10 overflow-hidden rounded-full bg-hover-strong"><span class="block h-full rounded-full bg-success" :style="{ width: `${Math.min(100, cacheHitRate(m))}%` }"></span></span>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div class="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 px-3 pb-3 text-[11px] text-ink-4">
      <span>范围：{{ range.start }} ~ {{ range.end }}</span>
      <span :title="'按当前范围内有效的单次输出速度进行算术平均；没有有效记录时显示 —'">全范围平均 {{ formatTokenRate(speedSummary.samples > 0 ? speedSummary.sum / speedSummary.samples : null) }} tok/s</span>
    </div>
  </div>
</template>

<style scoped>
.usage-table th,
.usage-table td {
  padding: 6px 4px;
}

.usage-table .compact-column {
  width: 1%;
  white-space: nowrap;
}

.usage-table .model-column {
  max-width: 0;
}

.usage-table .model-name {
  display: block;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cache-rate-value {
  flex: 0 0 auto;
  white-space: nowrap;
}

.cache-rate-bar {
  flex: 0 1 2.5rem;
  min-width: 0;
}
</style>
