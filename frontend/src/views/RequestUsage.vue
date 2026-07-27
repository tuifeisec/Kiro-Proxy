<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { getRequestUsage } from '../api/requestUsage.js'
import IconClose from '../components/icons/IconClose.vue'
import IconSearch from '../components/icons/IconSearch.vue'
import IconCopy from '../components/icons/IconCopy.vue'
import { useToast } from '../composables/useToast.js'
import { daysAgo } from '../utils/date.js'

const items = ref([])
const summary = ref({})
const loading = ref(false)
const error = ref('')
const startDate = ref(daysAgo(6))
const endDate = ref(daysAgo(0))
const keyword = ref('')
const provider = ref('')
const status = ref('')
const currentPage = ref(1)
const pageSize = ref(25)
const total = ref(0)
const totalPages = ref(0)
const tooltip = ref(null)
const selectedItem = ref(null)
const tableScrollRef = ref(null)
const detailRef = ref(null)
const detailCloseRef = ref(null)
const { showToast } = useToast()
let detailLastFocused = null

watch(selectedItem, async (item) => {
  if (item) {
    detailLastFocused = document.activeElement
    await nextTick()
    detailCloseRef.value?.focus()
  } else {
    detailLastFocused?.focus?.()
    detailLastFocused = null
  }
})

const activeFilterCount = computed(() => [keyword.value, provider.value, status.value].filter((value) => String(value).trim()).length)

const displayItems = computed(() => [...items.value].sort((left, right) => {
  const leftTime = Date.parse(left.created_at || '')
  const rightTime = Date.parse(right.created_at || '')
  const timeDiff = (Number.isFinite(rightTime) ? rightTime : 0) - (Number.isFinite(leftTime) ? leftTime : 0)
  return timeDiff || Number(right.id || 0) - Number(left.id || 0)
}))

function showTooltip(type, item, event) {
  tooltip.value = { type, item, x: event.clientX, y: event.clientY }
}

function moveTooltip(event) {
  if (!tooltip.value) return
  tooltip.value = { ...tooltip.value, x: event.clientX, y: event.clientY }
}

function hideTooltip() {
  tooltip.value = null
}

const tooltipStyle = computed(() => {
  if (!tooltip.value) return {}
  const width = 272
  const height = tooltip.value.type === 'total' ? 390 : tooltip.value.type === 'timing' ? 360 : tooltip.value.type === 'tokens' ? 260 : 230
  const left = Math.min(tooltip.value.x + 14, window.innerWidth - width - 12)
  const preferredTop = tooltip.value.y + 14
  const top = preferredTop + height > window.innerHeight ? tooltip.value.y - height - 10 : preferredTop
  return { left: `${Math.max(12, left)}px`, top: `${Math.max(12, top)}px`, width: `${width}px` }
})

function formatNum(value) {
  const number = Number(value || 0)
  if (number >= 1_000_000) return `${(number / 1_000_000).toFixed(2)}M`
  if (number >= 1_000) return `${(number / 1_000).toFixed(1)}K`
  return number.toLocaleString()
}

function formatTime(value) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

function formatDuration(value) {
  const ms = Number(value || 0)
  if (!ms) return '—'
  return ms >= 1000 ? `${(ms / 1000).toFixed(2)} s` : `${ms} ms`
}

function streamingDuration(item) {
  return Math.max(0, Number(item.duration_ms || 0) - Number(item.first_byte_ms || 0))
}

function formatCredits(value) {
  return `$${Number(value || 0).toFixed(6)}`
}

function totalTokens(item) {
  return totalInputTokens(item) + Number(item.output_tokens || 0)
}

function totalInputTokens(item) {
  const input = Number(item.input_tokens || 0)
  const cached = cacheTokens(item)
  // 修复前的 Anthropic 记录保存的是未缓存输入，可由不可能的 input < cached 识别。
  return input < cached ? input + cached : input
}

function regularInputTokens(item) {
  const input = Number(item.input_tokens || 0)
  const cached = cacheTokens(item)
  // 新记录的 input_tokens 是总输入；旧 Anthropic 记录保存的是未缓存输入。
  return input < cached ? input : Math.max(0, input - cached)
}

function cacheTokens(item) {
  return Number(item.cache_read_tokens || 0) + Number(item.cache_write_tokens || 0)
}

async function load(resetPage = false) {
  if (resetPage) currentPage.value = 1
  if (startDate.value && endDate.value && startDate.value > endDate.value) {
    error.value = '开始日期不能晚于结束日期'
    return
  }
  loading.value = true
  error.value = ''
  try {
    const data = await getRequestUsage(startDate.value, endDate.value, {
      page: currentPage.value,
      pageSize: pageSize.value,
      q: keyword.value.trim(),
      provider: provider.value.trim(),
      status: status.value,
    })
    items.value = data.items || []
    summary.value = data.summary || {}
    currentPage.value = Number(data.page || currentPage.value)
    pageSize.value = Number(data.page_size || pageSize.value)
    total.value = Number(data.total || 0)
    totalPages.value = Number(data.total_pages || 0)
    await nextTick()
    if (tableScrollRef.value) tableScrollRef.value.scrollTop = 0
  } catch (e) {
    error.value = e.message || String(e)
    items.value = []
    total.value = 0
    totalPages.value = 0
  } finally {
    loading.value = false
  }
}

function clearFilters() {
  keyword.value = ''
  provider.value = ''
  status.value = ''
  load(true)
}

function changePage(nextPage) {
  if (loading.value || nextPage < 1 || nextPage > totalPages.value) return
  currentPage.value = nextPage
  load()
}

function openDetail(item) {
  selectedItem.value = item
  hideTooltip()
}

function closeDetail() {
  selectedItem.value = null
}

function trapDetailFocus(event) {
  if (event.key !== 'Tab' || !detailRef.value) return
  const focusables = Array.from(detailRef.value.querySelectorAll(
    'button:not(:disabled), [href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])'
  )).filter((element) => element.offsetParent !== null)
  if (!focusables.length) return
  const first = focusables[0]
  const last = focusables[focusables.length - 1]
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}

function handleKeydown(event) {
  if (!selectedItem.value) return
  if (event.key === 'Escape') closeDetail()
  else trapDetailFocus(event)
}

async function copyText(value, successMessage) {
  const text = String(value || '').trim()
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
    showToast(successMessage)
  } catch (e) {
    showToast(e?.message || '复制失败', { tone: 'error' })
  }
}

const summaryInputTokens = computed(() => {
  const input = Number(summary.value.input_tokens || 0)
  const cached = Number(summary.value.cache_read_tokens || 0) + Number(summary.value.cache_write_tokens || 0)
  return Math.max(0, input - cached)
})

const cards = computed(() => [
  { label: '请求数', value: formatNum(summary.value.requests), sub: `异常 ${formatNum(summary.value.errors)}`, tone: 'blue' },
  { label: '输入 Token', value: formatNum(summaryInputTokens.value), sub: `不含缓存 · 缓存读取 ${formatNum(summary.value.cache_read_tokens)}`, tone: 'cyan' },
  { label: '输出 Token', value: formatNum(summary.value.output_tokens), sub: `缓存写入 ${formatNum(summary.value.cache_write_tokens)}`, tone: 'violet' },
  { label: '成本', value: formatCredits(summary.value.credits), sub: `首字 ${formatDuration(summary.value.average_first_byte_ms)} · 耗时 ${formatDuration(summary.value.average_duration_ms)}`, tone: 'amber' },
])

onMounted(() => {
  load()
  document.addEventListener('keydown', handleKeydown)
})

onBeforeUnmount(() => document.removeEventListener('keydown', handleKeydown))
</script>

<template>
  <div class="ui-page-shell h-full overflow-y-auto">
    <div class="ui-page-inner space-y-3">
      <header class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 class="text-[22px] font-semibold tracking-tight text-[#0f172a]">请求统计</h1>
          <p class="mt-0.5 text-[12px] text-[#64748b]">逐条记录上游请求指标，不保存请求内容和返回内容。</p>
        </div>
        <div class="flex flex-wrap items-center gap-2 ui-card p-1.5">
          <span class="px-1 text-[11px] font-medium text-[#64748b]">时间范围</span>
          <input v-model="startDate" type="date" aria-label="开始日期" class="ui-control-h ui-control px-2 text-[12px]" />
          <span class="text-[12px] text-[#94a3b8]">至</span>
          <input v-model="endDate" type="date" aria-label="结束日期" class="ui-control-h ui-control px-2 text-[12px]" />
          <button class="ui-control-h ui-primary-button px-3 text-[12px] font-medium disabled:opacity-60" :disabled="loading" @click="load(true)">
            <IconSearch class="mr-1 inline-block h-3.5 w-3.5 align-[-2px]" />
            {{ loading ? '加载中' : '查询' }}
          </button>
        </div>
      </header>

      <div class="flex flex-wrap items-center gap-2 ui-card p-2">
        <div class="flex shrink-0 items-center gap-1.5 px-1 text-[12px] font-semibold text-[#334155]">
          <IconSearch class="h-3.5 w-3.5 text-[#64748b]" />
          <span>筛选</span>
          <span class="rounded-full bg-[#f1f5f9] px-1.5 py-0.5 text-[10px] font-medium text-[#64748b]">{{ activeFilterCount }}</span>
        </div>
        <label class="relative min-w-[220px] flex-1">
          <span class="sr-only">关键词</span>
          <IconSearch class="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-[#94a3b8]" />
          <input v-model="keyword" aria-label="关键词" class="ui-control-h ui-control w-full pl-8 pr-2 text-[12px] text-[#0f172a]" placeholder="模型、端点、路径或错误信息" @keyup.enter="load(true)" />
        </label>
        <label class="w-[160px]">
          <span class="sr-only">提供商</span>
          <input v-model="provider" aria-label="提供商" class="ui-control-h ui-control w-full px-2 text-[12px] text-[#0f172a]" placeholder="提供商" @keyup.enter="load(true)" />
        </label>
        <label class="w-[120px]">
          <span class="sr-only">状态</span>
          <select v-model="status" aria-label="状态" class="ui-control-h ui-control w-full px-2 text-[12px] text-[#0f172a]" @change="load(true)">
            <option value="">全部</option>
            <option value="success">成功</option>
            <option value="error">异常</option>
          </select>
        </label>
        <button class="inline-flex ui-control-h ui-control shrink-0 items-center gap-1.5 px-2.5 text-[11px] text-[#64748b] hover:bg-[#f8fafc]" :disabled="loading || activeFilterCount === 0" @click="clearFilters">
          <IconClose class="h-3 w-3" />
          清空
        </button>
      </div>

      <p v-if="error" class="ui-status-danger ui-card flex items-center gap-2 px-3 py-2 text-[12px]" role="alert">{{ error }}</p>

      <section class="grid grid-cols-2 gap-2.5 lg:grid-cols-4">
        <div v-for="card in cards" :key="card.label" class="ui-card border-t-2 px-4 py-3" :class="{
          'border-t-[#2563eb]': card.tone === 'blue',
          'border-t-[#06b6d4]': card.tone === 'cyan',
          'border-t-[#8b5cf6]': card.tone === 'violet',
          'border-t-[#f59e0b]': card.tone === 'amber',
        }">
          <div class="text-[11px] font-medium text-[#64748b]">{{ card.label }}</div>
          <div class="mt-1 text-[22px] font-semibold tabular-nums text-[#0f172a]">{{ card.value }}</div>
          <div class="mt-1 text-[11px] text-[#64748b]">{{ card.sub }}</div>
        </div>
      </section>

      <section class="overflow-hidden ui-card">
        <div class="flex flex-wrap items-center justify-between gap-2 border-b border-[#edf0f5] bg-[#fbfdff] px-3 py-2.5">
          <div>
            <h2 class="text-[14px] font-semibold text-[#0f172a]">请求记录</h2>
            <p class="mt-0.5 text-[11px] text-[#64748b]">按创建时间倒序显示，筛选结果共 {{ formatNum(total) }} 条</p>
          </div>
          <span class="rounded-full bg-white px-2 py-0.5 text-[11px] text-[#64748b] ring-1 ring-[#e2e8f0]">{{ items.length }} 条</span>
        </div>
        <div v-if="!loading && displayItems.length === 0" class="py-12 text-center text-[12px] text-[#64748b]">暂无请求统计</div>
        <div v-else ref="tableScrollRef" class="relative max-h-[calc(100vh-260px)] overflow-auto">
          <div v-if="loading" class="pointer-events-none absolute inset-x-0 top-0 z-20 h-0.5 overflow-hidden bg-[#dbeafe]" aria-hidden="true">
            <div class="h-full w-1/3 animate-pulse bg-[#2563eb]"></div>
          </div>
          <table class="w-full min-w-[1160px] text-[12px]">
            <thead class="sticky top-0 z-10 bg-white">
              <tr class="border-b border-[#edf0f5] text-left text-[#64748b]">
                <th class="py-2 pl-3 pr-3 font-medium">时间</th>
                <th class="py-2 pr-3 font-medium">模型 / 提供商</th>
                <th class="py-2 pr-3 font-medium">端点</th>
                <th class="py-2 pr-3 font-medium">状态</th>
                <th class="py-2 pr-3 text-right font-medium">Tokens</th>
                <th class="py-2 pr-3 text-right font-medium">成本</th>
                <th class="py-2 pr-3 text-right font-medium">首字</th>
                <th class="py-2 pr-3 text-right font-medium">总耗时</th>
                <th class="py-2 pr-3 font-medium">推理</th>
                <th class="py-2 pr-3 font-medium">档位</th>
                <th class="py-2 pr-3 font-medium">异常</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="item in displayItems"
                :key="item.id"
                class="cursor-pointer border-b border-[#f1f5f9] outline-none last:border-0 hover:bg-[#f8fbff] focus-visible:bg-[#eff6ff]"
                tabindex="0"
                :aria-label="`查看 ${item.model_id || 'unknown'} 请求详情`"
                @click="openDetail(item)"
                @keydown.enter.prevent="openDetail(item)"
                @keydown.space.prevent="openDetail(item)"
              >
                <td class="whitespace-nowrap py-2 pl-3 pr-3 font-mono text-[11px] text-[#475569]">{{ formatTime(item.created_at) }}</td>
                <td class="max-w-[220px] py-2 pr-3">
                  <div class="truncate font-medium text-[#0f172a]">{{ item.model_id || 'unknown' }}</div>
                  <div class="mt-0.5 truncate text-[10px] text-[#94a3b8]">{{ item.provider || '—' }}</div>
                </td>
                <td class="max-w-[220px] truncate py-2 pr-3 font-mono text-[10px] text-[#64748b]">{{ item.endpoint || item.request_path || '—' }}</td>
                <td class="py-2 pr-3"><span class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-medium" :class="item.success ? 'bg-[#ecfdf5] text-[#059669]' : 'bg-[#fef2f2] text-[#dc2626]'" :aria-label="item.success ? '请求成功' : '请求异常'"><span class="h-1.5 w-1.5 rounded-full" :class="item.success ? 'bg-[#10b981]' : 'bg-[#ef4444]'" aria-hidden="true"></span>{{ item.status_code || 'ERR' }}</span></td>
                <td class="py-2 pr-3 text-right tabular-nums">
                  <span class="inline-flex cursor-help items-center gap-1 whitespace-nowrap" @mouseenter="showTooltip('tokens', item, $event)" @mousemove="moveTooltip" @mouseleave="hideTooltip"><span class="text-[#2563eb]">↓{{ formatNum(regularInputTokens(item)) }}</span><span class="text-[#7c3aed]">↑{{ formatNum(item.output_tokens) }}</span><span class="text-[#059669]">缓存{{ formatNum(cacheTokens(item)) }}</span></span>
                </td>
                <td class="py-2 pr-3 text-right tabular-nums text-[#d97706]">
                  <span class="cursor-help whitespace-nowrap" @mouseenter="showTooltip('cost', item, $event)" @mousemove="moveTooltip" @mouseleave="hideTooltip">{{ formatCredits(item.credits) }}</span>
                </td>
                <td class="py-2 pr-3 text-right tabular-nums"><span class="cursor-help whitespace-nowrap" @mouseenter="showTooltip('timing', item, $event)" @mousemove="moveTooltip" @mouseleave="hideTooltip">{{ formatDuration(item.first_byte_ms) }}</span></td>
                <td class="py-2 pr-3 text-right tabular-nums"><span class="cursor-help whitespace-nowrap" @mouseenter="showTooltip('total', item, $event)" @mousemove="moveTooltip" @mouseleave="hideTooltip">{{ formatDuration(item.duration_ms) }}</span></td>
                <td class="py-2 pr-3">{{ item.reasoning_effort || 'Default' }}</td>
                <td class="py-2 pr-3">{{ item.service_tier || 'Standard' }}</td>
                <td class="max-w-[220px] truncate py-2 pr-3 text-[11px] text-[#dc2626]" :title="item.error_message">{{ item.error_message || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="flex flex-wrap items-center justify-between gap-2 border-t border-[#edf0f5] px-3 py-2 text-[12px] text-[#64748b]">
          <span>第 {{ totalPages ? currentPage : 0 }} / {{ totalPages }} 页</span>
          <div class="flex items-center gap-2">
            <label class="flex items-center gap-1">
              每页
              <select v-model.number="pageSize" class="ui-table-control-h ui-control px-1.5 text-[12px]" :disabled="loading" @change="load(true)">
                <option :value="25">25</option>
                <option :value="50">50</option>
                <option :value="100">100</option>
                <option :value="200">200</option>
              </select>
              条
            </label>
            <button class="ui-table-control-h ui-control px-2.5 disabled:cursor-not-allowed disabled:opacity-40" :disabled="loading || currentPage <= 1" @click="changePage(currentPage - 1)">上一页</button>
            <button class="ui-table-control-h ui-control px-2.5 disabled:cursor-not-allowed disabled:opacity-40" :disabled="loading || !totalPages || currentPage >= totalPages" @click="changePage(currentPage + 1)">下一页</button>
          </div>
        </div>
      </section>
    </div>

    <Teleport to="body">
      <div v-if="tooltip" class="ui-popover pointer-events-none fixed z-[100] border border-[#334155] bg-[#0f172a] p-3 text-left text-[12px] text-white" :style="tooltipStyle">
        <template v-if="tooltip.type === 'tokens'">
          <div class="mb-2 text-[14px] font-semibold">Token 明细</div>
          <div class="space-y-1.5 text-[#cbd5e1]">
            <div class="flex justify-between"><span>总输入 Token</span><span class="font-medium text-white">{{ formatNum(totalInputTokens(tooltip.item)) }}</span></div>
            <div class="flex justify-between"><span>输入 Token（不含缓存）</span><span class="font-medium text-white">{{ formatNum(regularInputTokens(tooltip.item)) }}</span></div>
            <div class="flex justify-between"><span>输出 Token</span><span class="font-medium text-white">{{ formatNum(tooltip.item.output_tokens) }}</span></div>
            <div class="flex justify-between"><span>缓存读取 Token</span><span class="font-medium text-white">{{ formatNum(tooltip.item.cache_read_tokens) }}</span></div>
            <div class="flex justify-between"><span>缓存写入 Token</span><span class="font-medium text-white">{{ formatNum(tooltip.item.cache_write_tokens) }}</span></div>
          </div>
          <div class="mt-2 flex justify-between border-t border-[#334155] pt-2"><span>总 Token</span><span class="font-semibold text-[#22d3ee]">{{ formatNum(totalTokens(tooltip.item)) }}</span></div>
        </template>
        <template v-else-if="tooltip.type === 'cost'">
          <div class="mb-2 text-[14px] font-semibold">成本明细</div>
          <div class="space-y-1.5 text-[#cbd5e1]">
            <div class="flex justify-between"><span>输入成本</span><span class="font-medium text-white">{{ formatCredits(tooltip.item.input_cost) }}</span></div>
            <div class="flex justify-between"><span>输出成本</span><span class="font-medium text-white">{{ formatCredits(tooltip.item.output_cost) }}</span></div>
            <div class="flex justify-between"><span>缓存读取成本</span><span class="font-medium text-white">{{ formatCredits(tooltip.item.cache_read_cost) }}</span></div>
            <div class="flex justify-between"><span>缓存写入成本</span><span class="font-medium text-white">{{ formatCredits(tooltip.item.cache_write_cost) }}</span></div>
          </div>
          <div class="mt-2 flex justify-between border-t border-[#334155] pt-2"><span>总成本</span><span class="font-semibold text-[#22d3ee]">{{ formatCredits(tooltip.item.credits) }}</span></div>
        </template>
        <template v-else>
          <div class="mb-2 text-[14px] font-semibold">{{ tooltip.type === 'total' ? '总耗时明细' : '首字耗时明细' }}</div>
          <div class="space-y-1.5 text-[#cbd5e1]">
            <div class="flex justify-between"><span>请求转换</span><span class="font-medium text-white">{{ formatDuration(tooltip.item.conversion_ms) }}</span></div>
            <div class="flex justify-between"><span>鉴权检查/刷新</span><span class="font-medium text-white">{{ formatDuration(tooltip.item.auth_ms) }}</span></div>
            <div class="flex justify-between"><span>DNS</span><span class="font-medium text-white">{{ formatDuration(tooltip.item.dns_ms) }}</span></div>
            <div class="flex justify-between"><span>连接代理/上游</span><span class="font-medium text-white">{{ formatDuration(tooltip.item.connect_ms) }}</span></div>
            <div class="flex justify-between"><span>TLS 握手</span><span class="font-medium text-white">{{ formatDuration(tooltip.item.tls_ms) }}</span></div>
            <div class="flex justify-between"><span>发送请求体</span><span class="font-medium text-white">{{ formatDuration(tooltip.item.request_send_ms) }}</span></div>
            <div class="flex justify-between"><span>上游等待响应</span><span class="font-medium text-white">{{ formatDuration(tooltip.item.upstream_wait_ms) }}</span></div>
            <div class="flex justify-between"><span>收到响应头总耗时</span><span class="font-medium text-white">{{ formatDuration(tooltip.item.response_header_ms) }}</span></div>
            <div class="flex justify-between"><span>响应头到首段流</span><span class="font-medium text-white">{{ formatDuration(tooltip.item.stream_wait_ms) }}</span></div>
            <div v-if="tooltip.type === 'total'" class="flex justify-between"><span>首字后流式阶段</span><span class="font-medium text-white">{{ formatDuration(streamingDuration(tooltip.item)) }}</span></div>
            <div class="flex justify-between"><span>连接复用</span><span class="font-medium text-white">{{ tooltip.item.reused_connection ? '是' : '否' }}</span></div>
            <div class="flex justify-between"><span>重试次数</span><span class="font-medium" :class="tooltip.item.retry_count ? 'text-[#fbbf24]' : 'text-white'">{{ tooltip.item.retry_count || 0 }}</span></div>
          </div>
          <div class="mt-2 flex justify-between border-t border-[#334155] pt-2"><span>{{ tooltip.type === 'total' ? '总耗时' : '总首字' }}</span><span class="font-semibold text-[#22d3ee]">{{ formatDuration(tooltip.type === 'total' ? tooltip.item.duration_ms : tooltip.item.first_byte_ms) }}</span></div>
        </template>
      </div>
    </Teleport>

    <Teleport to="body">
      <div v-if="selectedItem" class="fixed inset-0 z-[90] bg-black/15" @click.self="closeDetail">
        <aside ref="detailRef" class="ml-auto flex h-full w-full max-w-[440px] flex-col border-l border-[#e2e8f0] bg-white shadow-[-12px_0_32px_rgba(15,23,42,0.12)]" role="dialog" aria-modal="true" aria-labelledby="request-detail-title">
          <header class="flex shrink-0 items-start justify-between gap-3 border-b border-[#e2e8f0] px-4 py-3.5">
            <div class="min-w-0">
              <h2 id="request-detail-title" class="text-[16px] font-semibold text-[#0f172a]">请求详情</h2>
              <p class="mt-1 truncate font-mono text-[11px] text-[#64748b]">#{{ selectedItem.id || '—' }} · {{ formatTime(selectedItem.created_at) }}</p>
            </div>
            <button ref="detailCloseRef" type="button" class="flex h-8 w-8 shrink-0 items-center justify-center ui-control text-[#64748b] hover:bg-[#f8fafc]" aria-label="关闭请求详情" @click="closeDetail">
              <IconClose class="h-4 w-4" />
            </button>
          </header>

          <div class="min-h-0 flex-1 overflow-y-auto">
            <section class="border-b border-[#edf0f5] px-4 py-4">
              <div class="mb-3 flex items-center justify-between gap-3">
                <div class="min-w-0">
                  <div class="truncate text-[14px] font-semibold text-[#0f172a]">{{ selectedItem.model_id || 'unknown' }}</div>
                  <div class="mt-1 text-[11px] text-[#64748b]">{{ selectedItem.provider || '—' }} · {{ selectedItem.endpoint || selectedItem.request_path || '—' }}</div>
                </div>
                <span class="inline-flex shrink-0 items-center gap-1.5 rounded-full px-2 py-1 text-[11px] font-medium" :class="selectedItem.success ? 'bg-[#ecfdf5] text-[#059669]' : 'bg-[#fef2f2] text-[#dc2626]'">
                  <span class="h-1.5 w-1.5 rounded-full" :class="selectedItem.success ? 'bg-[#10b981]' : 'bg-[#ef4444]'"></span>
                  {{ selectedItem.status_code || 'ERR' }}
                </span>
              </div>
              <dl class="grid grid-cols-2 gap-x-4 gap-y-2 text-[12px]">
                <div><dt class="text-[#94a3b8]">推理程度</dt><dd class="mt-0.5 font-medium text-[#334155]">{{ selectedItem.reasoning_effort || 'Default' }}</dd></div>
                <div><dt class="text-[#94a3b8]">服务档位</dt><dd class="mt-0.5 font-medium text-[#334155]">{{ selectedItem.service_tier || 'Standard' }}</dd></div>
                <div><dt class="text-[#94a3b8]">连接复用</dt><dd class="mt-0.5 font-medium text-[#334155]">{{ selectedItem.reused_connection ? '是' : '否' }}</dd></div>
                <div><dt class="text-[#94a3b8]">重试次数</dt><dd class="mt-0.5 font-medium" :class="selectedItem.retry_count ? 'text-[#d97706]' : 'text-[#334155]'">{{ selectedItem.retry_count || 0 }}</dd></div>
              </dl>
            </section>

            <section class="border-b border-[#edf0f5] px-4 py-4">
              <h3 class="mb-3 text-[13px] font-semibold text-[#0f172a]">Token 与成本</h3>
              <dl class="grid grid-cols-2 gap-x-5 gap-y-2.5 text-[12px]">
                <div><dt class="text-[#94a3b8]">普通输入</dt><dd class="mt-0.5 font-medium tabular-nums text-[#2563eb]">{{ formatNum(regularInputTokens(selectedItem)) }}</dd></div>
                <div><dt class="text-[#94a3b8]">输出</dt><dd class="mt-0.5 font-medium tabular-nums text-[#7c3aed]">{{ formatNum(selectedItem.output_tokens) }}</dd></div>
                <div><dt class="text-[#94a3b8]">缓存读取</dt><dd class="mt-0.5 font-medium tabular-nums text-[#059669]">{{ formatNum(selectedItem.cache_read_tokens) }}</dd></div>
                <div><dt class="text-[#94a3b8]">缓存写入</dt><dd class="mt-0.5 font-medium tabular-nums text-[#059669]">{{ formatNum(selectedItem.cache_write_tokens) }}</dd></div>
                <div><dt class="text-[#94a3b8]">总 Token</dt><dd class="mt-0.5 font-semibold tabular-nums text-[#0f172a]">{{ formatNum(totalTokens(selectedItem)) }}</dd></div>
                <div><dt class="text-[#94a3b8]">总成本</dt><dd class="mt-0.5 font-semibold tabular-nums text-[#d97706]">{{ formatCredits(selectedItem.credits) }}</dd></div>
              </dl>
            </section>

            <section class="border-b border-[#edf0f5] px-4 py-4">
              <h3 class="mb-3 text-[13px] font-semibold text-[#0f172a]">耗时分解</h3>
              <dl class="grid grid-cols-[1fr_auto] gap-x-4 gap-y-2 text-[12px]">
                <dt class="text-[#64748b]">DNS</dt><dd class="tabular-nums text-[#334155]">{{ formatDuration(selectedItem.dns_ms) }}</dd>
                <dt class="text-[#64748b]">连接上游</dt><dd class="tabular-nums text-[#334155]">{{ formatDuration(selectedItem.connect_ms) }}</dd>
                <dt class="text-[#64748b]">TLS 握手</dt><dd class="tabular-nums text-[#334155]">{{ formatDuration(selectedItem.tls_ms) }}</dd>
                <dt class="text-[#64748b]">上游等待响应</dt><dd class="tabular-nums text-[#334155]">{{ formatDuration(selectedItem.upstream_wait_ms) }}</dd>
                <dt class="text-[#64748b]">首字耗时</dt><dd class="font-medium tabular-nums text-[#2563eb]">{{ formatDuration(selectedItem.first_byte_ms) }}</dd>
                <dt class="text-[#64748b]">总耗时</dt><dd class="font-medium tabular-nums text-[#0f172a]">{{ formatDuration(selectedItem.duration_ms) }}</dd>
              </dl>
            </section>

            <section v-if="selectedItem.error_message" class="px-4 py-4">
              <div class="mb-2 flex items-center justify-between gap-3">
                <h3 class="text-[13px] font-semibold text-[#b91c1c]">异常信息</h3>
                <button type="button" class="inline-flex h-7 items-center gap-1 ui-control px-2 text-[11px] text-[#64748b] hover:bg-[#f8fafc]" @click="copyText(selectedItem.error_message, '错误信息已复制')">
                  <IconCopy class="h-3 w-3" />
                  复制
                </button>
              </div>
              <pre class="whitespace-pre-wrap break-words rounded-lg border border-[#fecaca] bg-[#fef2f2] p-3 font-mono text-[11px] leading-5 text-[#b91c1c]">{{ selectedItem.error_message }}</pre>
            </section>
          </div>
        </aside>
      </div>
    </Teleport>
  </div>
</template>
