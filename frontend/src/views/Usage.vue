<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useDailyStats } from '../composables/useStats.js'
import { useSmoothNumber } from '../composables/useSmoothNumber.js'
import { getLogsByDate } from '../api/logs.js'
import IconUsage from '../components/icons/IconUsage.vue'
import IconDashboard from '../components/icons/IconDashboard.vue'
import IconWifi from '../components/icons/IconWifi.vue'
import IconArrowRight from '../components/icons/IconArrowRight.vue'
import IconRefresh from '../components/icons/IconRefresh.vue'
import { statsDaysAgo } from '../utils/date.js'
import { formatCredits, formatTokens as formatNum } from '../utils/format.js'

const { rows, loading, error, range, load } = useDailyStats()

const activeRange = ref('7d')
const startDate = ref(statsDaysAgo(6))
const endDate = ref(statsDaysAgo(0))
const selectedDate = ref('')
const selectedDateLogs = ref([])
const logsLoading = ref(false)
const logsError = ref('')
const validationError = ref('')
let refreshInFlight = false
let refreshPending = false
let refreshTimer = null
let disposed = false
let selectSeq = 0

function refreshTimerTick() {
  if (document.hidden) return
  if (activeRange.value !== 'custom') {
    const option = quickRanges.find((item) => item.key === activeRange.value)
    if (option) {
      startDate.value = statsDaysAgo(option.days)
      endDate.value = statsDaysAgo(0)
    }
  }
  void refresh()
}

function handleVisibilityChange() {
  if (!document.hidden) refreshTimerTick()
}

const quickRanges = [
  { key: 'today', label: '今天', days: 0 },
  { key: '7d', label: '最近 7 天', days: 6 },
  { key: '30d', label: '最近 30 天', days: 29 },
]

function statNum(row, ...keys) {
  for (const key of keys) {
    const value = Number(row?.[key] ?? 0)
    if (value) return value
  }
  return 0
}

function cacheRead(row) {
  return statNum(row, 'cache_read_tokens', 'cache_read')
}

function cacheWrite(row) {
  return statNum(row, 'cache_write_tokens', 'cache_write')
}

function resetSelectedDate() {
  selectedDate.value = ''
  selectedDateLogs.value = []
  logsError.value = ''
}

function applyQuickRange(option) {
  validationError.value = ''
  activeRange.value = option.key
  startDate.value = statsDaysAgo(option.days)
  endDate.value = statsDaysAgo(0)
  resetSelectedDate()
  refresh()
}

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
    // 刷新成功后选中日期可能已不在新范围内（如自定义范围被改变、该日期数据消失），清理脱节的选择，与列表保持一致。
    if (!error.value && selectedDate.value
      && !rows.value.some((row) => row.date === selectedDate.value)) {
      resetSelectedDate()
    }
  } finally {
    refreshInFlight = false
    if (!disposed && refreshPending) {
      refreshPending = false
      void refresh()
    }
  }
}

async function selectDate(date) {
  if (selectedDate.value === date) {
    resetSelectedDate()
    return
  }

  const seq = ++selectSeq
  selectedDate.value = date
  selectedDateLogs.value = []
  logsError.value = ''
  logsLoading.value = true
  try {
    const data = await getLogsByDate(date, 200)
    // 过期响应直接丢弃，避免旧请求覆盖最后点击日期的数据。
    if (seq !== selectSeq) return
    selectedDateLogs.value = data.items || []
  } catch (e) {
    if (seq !== selectSeq) return
    logsError.value = e.message || String(e)
  } finally {
    // 过期请求不复位 loading，由最新请求负责。
    if (seq === selectSeq) logsLoading.value = false
  }
}

const totals = computed(() => rows.value.reduce((acc, row) => {
  acc.requests += Number(row.requests || 0)
  acc.errors += Number(row.errors || 0)
  acc.input += Number(row.input_tokens || 0)
  acc.output += Number(row.output_tokens || 0)
  acc.cacheRead += cacheRead(row)
  acc.cacheWrite += cacheWrite(row)
  acc.credits += Number(row.credits || 0)
  return acc
}, { requests: 0, errors: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0, credits: 0 }))

const totalTokens = computed(() => totals.value.input + totals.value.output)
const cacheHitRate = computed(() => totals.value.input ? (Math.min(totals.value.cacheRead, totals.value.input) / totals.value.input) * 100 : 0)
const errorRate = computed(() => totals.value.requests ? (totals.value.errors / totals.value.requests) * 100 : 0)

// 核心汇总指标平滑；snapFirstChange 让冷启动首个真实数据直接落位，表格行内明细保持原样不做平滑。
function smoothStat(source) {
  return useSmoothNumber(source, { snapFirstChange: true })
}

const smoothRequests = smoothStat(() => totals.value.requests)
const smoothTotalTokens = smoothStat(totalTokens)
const smoothCacheHitRate = smoothStat(cacheHitRate)
const smoothCredits = smoothStat(() => totals.value.credits)

const summaryCards = computed(() => [
  { label: '总请求数', value: Math.round(smoothRequests.value).toLocaleString(), sub: `错误 ${totals.value.errors} · 错误率 ${errorRate.value.toFixed(2)}%`, icon: IconUsage, tone: 'blue' },
  { label: '总 Token', value: formatNum(Math.round(smoothTotalTokens.value)), sub: `输入 ${formatNum(totals.value.input)} · 输出 ${formatNum(totals.value.output)}`, icon: IconDashboard, tone: 'green' },
  { label: '缓存命中', value: smoothCacheHitRate.value ? smoothCacheHitRate.value.toFixed(1) + '%' : '—', sub: `命中 ${formatNum(totals.value.cacheRead)} · 写入 ${formatNum(totals.value.cacheWrite)}`, icon: IconWifi, tone: 'purple' },
  { label: 'Credits', value: formatCredits(smoothCredits.value), sub: '按模型价格累计', icon: IconArrowRight, tone: 'amber' },
])

const toneMap = {
  blue: 'bg-[#eff6ff] text-[#2563eb] border-[#bfdbfe]',
  green: 'bg-[#ecfdf5] text-[#059669] border-[#a7f3d0]',
  purple: 'bg-[#f5f3ff] text-[#7c3aed] border-[#ddd6fe]',
  amber: 'bg-[#fff7ed] text-[#d97706] border-[#fed7aa]',
}

const dailyRows = computed(() => {
  const map = new Map()
  for (const row of rows.value) {
    const date = row.date || '—'
    const cur = map.get(date) || { date, requests: 0, errors: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0, credits: 0, models: new Set() }
    cur.requests += Number(row.requests || 0)
    cur.errors += Number(row.errors || 0)
    cur.input += Number(row.input_tokens || 0)
    cur.output += Number(row.output_tokens || 0)
    cur.cacheRead += cacheRead(row)
    cur.cacheWrite += cacheWrite(row)
    cur.credits += Number(row.credits || 0)
    if (row.model_id) cur.models.add(row.model_id)
    map.set(date, cur)
  }
  return Array.from(map.values()).sort((a, b) => String(b.date).localeCompare(String(a.date)))
})

const modelRows = computed(() => {
  const map = new Map()
  for (const row of rows.value) {
    const model = row.model_id || 'unknown'
    const cur = map.get(model) || { model, requests: 0, errors: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0, credits: 0, days: new Set() }
    cur.requests += Number(row.requests || 0)
    cur.errors += Number(row.errors || 0)
    cur.input += Number(row.input_tokens || 0)
    cur.output += Number(row.output_tokens || 0)
    cur.cacheRead += cacheRead(row)
    cur.cacheWrite += cacheWrite(row)
    cur.credits += Number(row.credits || 0)
    if (row.date) cur.days.add(row.date)
    map.set(model, cur)
  }
  return Array.from(map.values()).sort((a, b) => (b.input + b.output) - (a.input + a.output))
})

const selectedDateRows = computed(() => rows.value
  .filter((row) => row.date === selectedDate.value)
  .map((row) => ({
    model: row.model_id || 'unknown',
    requests: Number(row.requests || 0),
    errors: Number(row.errors || 0),
    input: Number(row.input_tokens || 0),
    output: Number(row.output_tokens || 0),
    cacheRead: cacheRead(row),
    cacheWrite: cacheWrite(row),
    credits: Number(row.credits || 0),
  }))
  .sort((a, b) => (b.input + b.output) - (a.input + a.output)))

function rowCacheHitRate(row) {
  return row.input ? (Math.min(row.cacheRead, row.input) / row.input) * 100 : 0
}

function logTime(entry) {
  return entry.time || (entry.full_time ? entry.full_time.slice(11, 23) : '—')
}

function logLevelClass(level) {
  if (level === 'error') return 'bg-[#fef2f2] text-[#dc2626] ring-[#fecaca]'
  if (level === 'warn') return 'bg-[#fff7ed] text-[#d97706] ring-[#fed7aa]'
  if (level === 'debug') return 'bg-[#f8fafc] text-[#64748b] ring-[#e2e8f0]'
  return 'bg-[#eff6ff] text-[#2563eb] ring-[#bfdbfe]'
}

function logMetaLine(entry) {
  const parts = []
  if (entry.model) parts.push(`model=${entry.model}`)
  if (entry.status) parts.push(`status=${entry.status}`)
  if (entry.latency_ms) parts.push(`${entry.latency_ms}ms`)
  const tokens = Number(entry.input_tokens || 0) + Number(entry.output_tokens || 0)
  if (tokens) parts.push(`tokens=${formatNum(tokens)}`)
  if (entry.cache_read_tokens) parts.push(`cache=${formatNum(entry.cache_read_tokens)}`)
  if (entry.credits) parts.push(formatCredits(entry.credits))
  return parts.join(' · ')
}

function logTextPreview(value, max = 240) {
  if (!value) return ''
  const text = String(value)
  if (text.length <= max) return text
  return `${text.slice(0, max)}…（${formatNum(text.length)} 字符）`
}

function closeSelectedDate() {
  resetSelectedDate()
}

onMounted(() => {
  refresh()
  refreshTimer = setInterval(refreshTimerTick, 5000)
  document.addEventListener('visibilitychange', handleVisibilityChange)
})

onBeforeUnmount(() => {
  disposed = true
  if (refreshTimer) clearInterval(refreshTimer)
  refreshTimer = null
  document.removeEventListener('visibilitychange', handleVisibilityChange)
})
</script>

<template>
  <div class="ui-page-shell h-full overflow-y-auto">
    <div class="ui-page-inner space-y-3">
      <header class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 class="text-[22px] font-semibold tracking-tight text-[#0f172a]">用量明细</h1>
          <p class="mt-0.5 text-[12px] text-[#64748b]">查看每日、模型维度的请求量、Token、缓存命中和 Credits 消耗。</p>
        </div>
        <div class="inline-flex items-center gap-1 ui-card p-1">
          <button
            v-for="option in quickRanges"
            :key="option.key"
            class="ui-control-h rounded-md px-2.5 text-[12px] font-medium transition"
            :class="activeRange === option.key ? 'bg-[#1677ff] text-white shadow-sm' : 'text-[#64748b] hover:bg-[#f8fafc] hover:text-[#0f172a]'"
            @click="applyQuickRange(option)"
          >
            {{ option.label }}
          </button>
          <span class="mx-1 h-4 w-px bg-[#e2e8f0]"></span>
          <input v-model="startDate" type="date" class="ui-control-h ui-control px-2 text-[12px] text-[#475569] outline-none focus:border-[#93c5fd]" @change="activeRange = 'custom'" />
          <span class="text-[12px] text-[#94a3b8]">至</span>
          <input v-model="endDate" type="date" class="ui-control-h ui-control px-2 text-[12px] text-[#475569] outline-none focus:border-[#93c5fd]" @change="activeRange = 'custom'" />
          <button
            class="inline-flex ui-control-h ui-control items-center gap-1.5 px-2.5 text-[12px] font-medium text-[#475569] hover:bg-[#f8fafc] disabled:opacity-60"
            :disabled="loading"
            @click="refresh"
          >
            <IconRefresh class="h-3.5 w-3.5" :class="{ 'animate-spin': loading }" />
            {{ loading ? '加载中' : '刷新' }}
          </button>
        </div>
      </header>

      <p v-if="validationError || error" class="rounded-xl border border-[#fecaca] bg-[#fef2f2] px-3 py-2 text-[12px] text-[#dc2626]">用量明细加载失败：{{ validationError || error }}</p>

      <section class="grid grid-cols-2 gap-2.5 lg:grid-cols-4">
        <div v-for="card in summaryCards" :key="card.label" class="ui-card px-4 py-3">
          <div class="grid h-[58px] grid-cols-[34px_minmax(0,1fr)] items-center gap-3">
            <div class="flex h-8 w-8 items-center justify-center rounded-lg border" :class="toneMap[card.tone]">
              <component :is="card.icon" class="h-4 w-4" />
            </div>
            <div class="min-w-0">
              <div class="truncate text-[11px] font-medium leading-4 text-[#64748b]">{{ card.label }}</div>
              <div class="mt-0.5 text-[22px] font-semibold leading-6 tracking-tight text-[#0f172a] tabular-nums">{{ card.value }}</div>
              <div class="mt-1 truncate text-[11px] leading-3.5 text-[#64748b]">{{ card.sub }}</div>
            </div>
          </div>
        </div>
      </section>

      <section class="relative overflow-hidden ui-card">
        <div class="flex items-center justify-between border-b border-[#edf0f5] bg-[#fbfdff] px-3 py-2">
          <div>
            <h2 class="text-[14px] font-semibold text-[#0f172a]">每日明细</h2>
            <p class="mt-0.5 text-[12px] text-[#64748b]">按日期聚合全部模型的使用情况</p>
          </div>
          <span class="rounded-full bg-white px-2.5 py-1 text-[11px] text-[#94a3b8] ring-1 ring-[#edf0f5]">范围：{{ range.start || startDate }} ~ {{ range.end || endDate }}</span>
        </div>

        <div v-if="loading" class="absolute inset-x-0 top-[49px] z-10 h-0.5 overflow-hidden bg-[#eff6ff]">
          <div class="h-full w-1/3 animate-pulse rounded-full bg-[#1677ff]"></div>
        </div>
        <div v-if="dailyRows.length === 0 && !loading" class="py-12 text-center text-[13px] text-[#64748b]">该时间段无数据</div>
        <div v-else class="overflow-x-auto px-3 pb-3 pt-2">
          <table class="w-full min-w-[900px] text-[12px]">
            <thead>
              <tr class="border-b border-[#edf0f5] bg-[#fbfdff] text-left text-[#64748b]">
                <th class="sticky left-0 z-[1] bg-[#fbfdff] py-2 pr-3 font-medium">日期</th>
                <th class="py-2 pr-3 text-right font-medium">模型数</th>
                <th class="py-2 pr-3 text-right font-medium">请求数</th>
                <th class="py-2 pr-3 text-right font-medium">错误</th>
                <th class="py-2 pr-3 text-right font-medium">输入 Token</th>
                <th class="py-2 pr-3 text-right font-medium">输出 Token</th>
                <th class="py-2 pr-3 text-right font-medium">缓存命中</th>
                <th class="py-2 pr-3 text-right font-medium">Credits</th>
                <th class="py-2 text-right font-medium">命中率</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="row in dailyRows"
                :key="row.date"
                class="cursor-pointer border-b border-[#f1f5f9] bg-white last:border-b-0 hover:bg-[#fbfdff]"
                :class="selectedDate === row.date ? 'bg-[#eff6ff]' : ''"
                @click="selectDate(row.date)"
              >
                <td class="sticky left-0 z-[1] bg-inherit py-2 pr-3 font-medium text-[#0f172a]"><span class="rounded-md px-2 py-1 font-mono text-[11px] ring-1" :class="selectedDate === row.date ? 'bg-[#1677ff] text-white ring-[#1677ff]' : 'bg-[#f8fafc] ring-[#e2e8f0]'">{{ row.date }}</span></td>
                <td class="py-2 pr-3 text-right text-[#334155]"><span class="rounded-full bg-[#f1f5f9] px-2 py-0.5 text-[11px]">{{ row.models.size }}</span></td>
                <td class="py-2 pr-3 text-right tabular-nums text-[#334155]">{{ row.requests.toLocaleString() }}</td>
                <td class="py-2 pr-3 text-right tabular-nums"><span class="rounded-full px-2 py-0.5 text-[11px]" :class="row.errors > 0 ? 'bg-[#fef2f2] text-[#dc2626]' : 'bg-[#f8fafc] text-[#94a3b8]'">{{ row.errors }}</span></td>
                <td class="py-2 pr-3 text-right tabular-nums text-[#334155]">{{ formatNum(row.input) }}</td>
                <td class="py-2 pr-3 text-right tabular-nums text-[#334155]">{{ formatNum(row.output) }}</td>
                <td class="py-2 pr-3 text-right tabular-nums text-[#059669]">{{ formatNum(row.cacheRead) }}</td>
                <td class="py-2 pr-3 text-right tabular-nums text-[#d97706]">{{ formatCredits(row.credits) }}</td>
                <td class="py-2 text-right tabular-nums"><span class="rounded-full bg-[#f0fdf4] px-2 py-0.5 text-[11px] text-[#059669]">{{ rowCacheHitRate(row).toFixed(1) }}%</span></td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section v-if="selectedDate" class="overflow-hidden ui-card border-[#bfdbfe]">
        <div class="flex items-center justify-between border-b border-[#dbeafe] bg-[#eff6ff] px-3 py-2">
          <div>
            <h2 class="text-[14px] font-semibold text-[#1d4ed8]">{{ selectedDate }} 日志详情</h2>
            <p class="mt-0.5 text-[12px] text-[#64748b]">上方保留按模型统计，下方展示该统计日期对应 JSONL 真实请求日志</p>
          </div>
          <button class="h-7 rounded-lg border border-[#bfdbfe] bg-white px-2.5 text-[12px] font-medium text-[#2563eb] hover:bg-[#f8fafc]" @click="closeSelectedDate">收起</button>
        </div>
        <div class="space-y-3 px-3 pb-3 pt-2">
          <div class="overflow-x-auto rounded-lg border border-[#dbeafe]">
            <div class="flex items-center justify-between border-b border-[#dbeafe] bg-[#f8fbff] px-3 py-2">
              <h3 class="text-[12px] font-semibold text-[#1d4ed8]">模型明细</h3>
              <span class="text-[11px] text-[#64748b]">{{ selectedDateRows.length }} 个模型</span>
            </div>
            <table class="w-full min-w-[820px] text-[12px]">
              <thead>
                <tr class="border-b border-[#dbeafe] bg-white text-left text-[#64748b]">
                  <th class="sticky left-0 z-[1] bg-white py-2 pl-3 pr-3 font-medium">模型</th>
                  <th class="py-2 pr-3 text-right font-medium">请求数</th>
                  <th class="py-2 pr-3 text-right font-medium">错误</th>
                  <th class="py-2 pr-3 text-right font-medium">输入 Token</th>
                  <th class="py-2 pr-3 text-right font-medium">输出 Token</th>
                  <th class="py-2 pr-3 text-right font-medium">缓存命中</th>
                  <th class="py-2 pr-3 text-right font-medium">Credits</th>
                  <th class="py-2 pr-3 text-right font-medium">命中率</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="row in selectedDateRows" :key="row.model" class="border-b border-[#f1f5f9] bg-white last:border-b-0 hover:bg-[#fbfdff]">
                  <td class="sticky left-0 z-[1] max-w-[320px] truncate bg-inherit py-2 pl-3 pr-3 font-medium text-[#0f172a]"><span class="rounded-md bg-white px-2 py-1 font-mono text-[11px] ring-1 ring-[#dbeafe]">{{ row.model }}</span></td>
                  <td class="py-2 pr-3 text-right tabular-nums text-[#334155]">{{ row.requests.toLocaleString() }}</td>
                  <td class="py-2 pr-3 text-right tabular-nums"><span class="rounded-full px-2 py-0.5 text-[11px]" :class="row.errors > 0 ? 'bg-[#fef2f2] text-[#dc2626]' : 'bg-[#f8fafc] text-[#94a3b8]'">{{ row.errors }}</span></td>
                  <td class="py-2 pr-3 text-right tabular-nums text-[#334155]">{{ formatNum(row.input) }}</td>
                  <td class="py-2 pr-3 text-right tabular-nums text-[#334155]">{{ formatNum(row.output) }}</td>
                  <td class="py-2 pr-3 text-right tabular-nums text-[#059669]">{{ formatNum(row.cacheRead) }}</td>
                  <td class="py-2 pr-3 text-right tabular-nums text-[#d97706]">{{ formatCredits(row.credits) }}</td>
                  <td class="py-2 pr-3 text-right tabular-nums"><span class="rounded-full bg-[#f0fdf4] px-2 py-0.5 text-[11px] text-[#059669]">{{ rowCacheHitRate(row).toFixed(1) }}%</span></td>
                </tr>
              </tbody>
            </table>
          </div>

          <div class="overflow-hidden rounded-lg border border-[#dbeafe]">
            <div class="flex items-center justify-between border-b border-[#dbeafe] bg-[#f8fbff] px-3 py-2">
              <div>
                <h3 class="text-[12px] font-semibold text-[#1d4ed8]">请求日志</h3>
                <p class="mt-0.5 text-[11px] text-[#64748b]">按 JSONL 文件读取，最多显示最近 200 条</p>
              </div>
              <span class="rounded-full bg-white px-2 py-0.5 text-[11px] text-[#64748b] ring-1 ring-[#dbeafe]">{{ logsLoading ? '读取中' : `${selectedDateLogs.length} 条` }}</span>
            </div>

            <div v-if="logsLoading" class="py-8 text-center text-[12px] text-[#64748b]">正在读取日志...</div>
            <div v-else-if="logsError" class="m-3 rounded-lg border border-[#fecaca] bg-[#fef2f2] px-3 py-2 text-[12px] text-[#dc2626]">日志加载失败：{{ logsError }}</div>
            <div v-else-if="selectedDateLogs.length === 0" class="py-8 text-center text-[12px] text-[#64748b]">该日期暂无可展示日志</div>
            <div v-else class="max-h-[360px] overflow-auto">
              <table class="w-full min-w-[980px] text-[12px]">
                <thead class="sticky top-0 z-10">
                  <tr class="border-b border-[#dbeafe] bg-white text-left text-[#64748b]">
                    <th class="py-2 pl-3 pr-3 font-medium">时间</th>
                    <th class="py-2 pr-3 font-medium">级别</th>
                    <th class="py-2 pr-3 font-medium">分类</th>
                    <th class="py-2 pr-3 font-medium">消息</th>
                    <th class="py-2 pr-3 font-medium">结构化信息</th>
                    <th class="py-2 pr-3 font-medium">详情</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="entry in selectedDateLogs" :key="entry.id" class="border-b border-[#f1f5f9] last:border-b-0 hover:bg-[#fbfdff]">
                    <td class="whitespace-nowrap py-2 pl-3 pr-3 font-mono text-[11px] text-[#475569]" :title="entry.full_time || entry.time">{{ logTime(entry) }}</td>
                    <td class="py-2 pr-3"><span class="rounded-full px-2 py-0.5 text-[11px] font-medium ring-1" :class="logLevelClass(entry.level)">{{ entry.level || 'info' }}</span></td>
                    <td class="py-2 pr-3"><span class="rounded-md bg-[#f8fafc] px-2 py-1 font-mono text-[11px] text-[#475569] ring-1 ring-[#e2e8f0]">{{ entry.category || '—' }}</span></td>
                    <td class="max-w-[300px] truncate py-2 pr-3 font-medium text-[#0f172a]">{{ logTextPreview(entry.message, 160) || '—' }}</td>
                    <td class="max-w-[300px] truncate py-2 pr-3 text-[11px] text-[#64748b]" :title="logMetaLine(entry)">{{ logMetaLine(entry) || '—' }}</td>
                    <td class="max-w-[280px] truncate py-2 pr-3 text-[11px] text-[#94a3b8]">{{ logTextPreview(entry.detail, 220) || '—' }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </div>
      </section>

      <section class="overflow-hidden ui-card">
        <div class="border-b border-[#edf0f5] bg-[#fbfdff] px-3 py-2">
          <h2 class="text-[14px] font-semibold text-[#0f172a]">模型汇总</h2>
          <p class="mt-0.5 text-[12px] text-[#64748b]">按模型聚合当前时间范围内的用量</p>
        </div>
        <div class="overflow-x-auto px-3 pb-3 pt-2">
          <table class="w-full min-w-[900px] text-[12px]">
            <thead>
              <tr class="border-b border-[#edf0f5] bg-[#fbfdff] text-left text-[#64748b]">
                <th class="sticky left-0 z-[1] bg-[#fbfdff] py-2 pr-3 font-medium">模型</th>
                <th class="py-2 pr-3 text-right font-medium">天数</th>
                <th class="py-2 pr-3 text-right font-medium">请求数</th>
                <th class="py-2 pr-3 text-right font-medium">错误</th>
                <th class="py-2 pr-3 text-right font-medium">输入 Token</th>
                <th class="py-2 pr-3 text-right font-medium">输出 Token</th>
                <th class="py-2 pr-3 text-right font-medium">缓存命中</th>
                <th class="py-2 pr-3 text-right font-medium">Credits</th>
                <th class="py-2 text-right font-medium">命中率</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in modelRows" :key="row.model" class="border-b border-[#f1f5f9] bg-white last:border-b-0 hover:bg-[#fbfdff]">
                <td class="sticky left-0 z-[1] max-w-[280px] truncate bg-inherit py-2 pr-3 font-medium text-[#0f172a]"><span class="rounded-md bg-[#f8fafc] px-2 py-1 font-mono text-[11px] ring-1 ring-[#e2e8f0]">{{ row.model }}</span></td>
                <td class="py-2 pr-3 text-right text-[#334155]"><span class="rounded-full bg-[#f1f5f9] px-2 py-0.5 text-[11px]">{{ row.days.size }}</span></td>
                <td class="py-2 pr-3 text-right tabular-nums text-[#334155]">{{ row.requests.toLocaleString() }}</td>
                <td class="py-2 pr-3 text-right tabular-nums"><span class="rounded-full px-2 py-0.5 text-[11px]" :class="row.errors > 0 ? 'bg-[#fef2f2] text-[#dc2626]' : 'bg-[#f8fafc] text-[#94a3b8]'">{{ row.errors }}</span></td>
                <td class="py-2 pr-3 text-right tabular-nums text-[#334155]">{{ formatNum(row.input) }}</td>
                <td class="py-2 pr-3 text-right tabular-nums text-[#334155]">{{ formatNum(row.output) }}</td>
                <td class="py-2 pr-3 text-right tabular-nums text-[#059669]">{{ formatNum(row.cacheRead) }}</td>
                <td class="py-2 pr-3 text-right tabular-nums text-[#d97706]">{{ formatCredits(row.credits) }}</td>
                <td class="py-2 text-right tabular-nums"><span class="rounded-full bg-[#f0fdf4] px-2 py-0.5 text-[11px] text-[#059669]">{{ rowCacheHitRate(row).toFixed(1) }}%</span></td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>
  </div>
</template>
