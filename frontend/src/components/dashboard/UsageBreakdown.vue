<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { useDailyStats } from '../../composables/useStats.js'
import IconRefresh from '../icons/IconRefresh.vue'
import { ALL_STATS_START_DATE, daysAgo } from '../../utils/date.js'

const props = defineProps({
  refreshKey: { type: Number, default: 0 },
  days: { type: Number, default: -1 },
})
const emit = defineEmits(['summary', 'range-change'])
const { rows, loading, error, range, load } = useDailyStats()
const validationError = ref('')
const isAllRange = computed(() => props.days < 0)

const startDate = ref('')
const endDate = ref('')

function applyDaysRange() {
  startDate.value = isAllRange.value ? ALL_STATS_START_DATE : daysAgo(props.days)
  endDate.value = daysAgo(0)
}

applyDaysRange()

async function refresh() {
  if (startDate.value && endDate.value && startDate.value > endDate.value) {
    validationError.value = '开始日期不能晚于结束日期'
    return
  }
  validationError.value = ''
  await load(startDate.value, endDate.value)
  if (!error.value) {
    emit('range-change', {
      all: isAllRange.value,
      start: range.value.start || startDate.value,
      end: range.value.end || endDate.value,
    })
  }
}

onMounted(refresh)
watch(() => props.refreshKey, refresh)
watch(() => props.days, async () => {
  applyDaysRange()
  await refresh()
})

function formatNum(n) {
  const value = Number(n || 0)
  if (value >= 1_000_000) return (value / 1_000_000).toFixed(2).replace(/\.00$/, '') + 'M'
  if (value >= 1_000) return (value / 1_000).toFixed(1).replace(/\.0$/, '') + 'K'
  return String(value)
}

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
      credits: 0,
    }
    cur.requests += r.requests
    cur.errors += r.errors
    cur.input_tokens += r.input_tokens
    cur.output_tokens += r.output_tokens
    cur.cache_write_tokens += cacheWriteTokens(r)
    cur.cache_hit_tokens += cacheHitTokens(r)
    cur.credits += Number(r.credits || 0)
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
  total.credits += Number(row.credits || 0)
  return total
}, {
  total_requests: 0,
  total_errors: 0,
  input_tokens: 0,
  output_tokens: 0,
  cache_read_tokens: 0,
  cache_write_tokens: 0,
  credits: 0,
}))

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

function formatCredits(n) {
  const value = Number(n || 0)
  if (value >= 1) return '$' + value.toFixed(2)
  return '$' + value.toFixed(4)
}
</script>

<template>
  <div class="ui-card">
    <div class="flex flex-wrap items-center justify-between gap-2 border-b border-[#edf0f5] px-3 py-2">
      <div class="min-w-0">
        <h3 class="text-[14px] font-semibold text-[#0f172a]">用量明细</h3>
        <p class="mt-0.5 text-[12px] text-[#64748b]">按模型聚合 token、Credits 和缓存命中</p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <template v-if="!isAllRange">
          <input v-model="startDate" type="date" class="h-8 rounded-lg border border-[#e2e8f0] bg-white px-2 text-[12px] text-[#475569]" />
          <span class="text-[12px] text-[#94a3b8]">至</span>
          <input v-model="endDate" type="date" class="h-8 rounded-lg border border-[#e2e8f0] bg-white px-2 text-[12px] text-[#475569]" />
        </template>
        <span v-else class="inline-flex h-8 items-center rounded-lg border border-[#e2e8f0] bg-[#f8fafc] px-3 text-[12px] text-[#64748b]">全部历史数据</span>
        <button
          class="inline-flex h-8 items-center gap-1.5 rounded-lg border border-[#e2e8f0] bg-white px-3 text-[12px] font-medium text-[#475569] hover:bg-[#f8fafc] disabled:opacity-60"
          :disabled="loading"
          @click="refresh"
        >
          <IconRefresh class="h-3.5 w-3.5" :class="{ 'animate-spin': loading }" />
          {{ loading ? '加载中' : '刷新' }}
        </button>
      </div>
    </div>

    <div v-if="validationError || error" class="mx-4 mt-3 rounded-lg border border-[#fecaca] bg-[#fef2f2] px-3 py-2 text-[12px] text-[#dc2626]">{{ validationError || error }}</div>

    <div v-if="rows.length === 0 && !loading" class="py-12 text-center text-[13px] text-[#64748b]">该时间段无数据</div>

    <div v-else class="overflow-x-auto px-3 pb-3 pt-2">
      <table class="w-full min-w-[720px] text-[12px]">
        <thead>
          <tr class="border-b border-[#edf0f5] text-left text-[#64748b]">
            <th class="py-1.5 pr-3 font-medium">模型</th>
            <th class="py-1.5 pr-3 text-right font-medium">请求数</th>
            <th class="py-1.5 pr-3 text-right font-medium">错误</th>
            <th class="py-1.5 pr-3 text-right font-medium">输入 Token</th>
            <th class="py-1.5 pr-3 text-right font-medium">输出 Token</th>
            <th class="py-1.5 pr-3 text-right font-medium">缓存命中</th>
            <th class="py-1.5 pr-3 text-right font-medium">Credits</th>
            <th class="py-1.5 text-right font-medium">命中率</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="m in byModel" :key="m.model_id" class="border-b border-[#f1f5f9] last:border-b-0 hover:bg-[#fbfdff]">
            <td class="py-1.5 pr-3 font-medium text-[#0f172a]">{{ m.model_id }}</td>
            <td class="py-1.5 pr-3 text-right tabular-nums text-[#334155]">{{ m.requests }}</td>
            <td class="py-1.5 pr-3 text-right tabular-nums" :class="m.errors > 0 ? 'text-[#dc2626]' : 'text-[#94a3b8]'">{{ m.errors }}</td>
            <td class="py-1.5 pr-3 text-right tabular-nums text-[#334155]">{{ formatNum(m.input_tokens) }}</td>
            <td class="py-1.5 pr-3 text-right tabular-nums text-[#334155]">{{ formatNum(m.output_tokens) }}</td>
            <td class="py-1.5 pr-3 text-right tabular-nums text-[#059669]">{{ formatNum(m.cache_hit_tokens) }}</td>
            <td class="py-1.5 pr-3 text-right tabular-nums text-[#d97706]">{{ formatCredits(m.credits) }}</td>
            <td class="py-1.5 text-right tabular-nums">
              <div class="flex items-center justify-end gap-2">
                <span class="text-[#334155]">{{ cacheHitRate(m).toFixed(1) }}%</span>
                <span class="h-1.5 w-10 overflow-hidden rounded-full bg-[#e2e8f0]"><span class="block h-full rounded-full bg-[#22c55e]" :style="{ width: `${Math.min(100, cacheHitRate(m))}%` }"></span></span>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
      <div class="mt-2 text-[11px] text-[#94a3b8]">范围：{{ range.start }} ~ {{ range.end }}</div>
    </div>
  </div>
</template>
