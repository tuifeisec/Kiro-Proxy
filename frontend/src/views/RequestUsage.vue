<script setup>
import { computed, onMounted, ref } from 'vue'
import { getRequestUsage } from '../api/requestUsage.js'

const items = ref([])
const summary = ref({})
const loading = ref(false)
const error = ref('')
const startDate = ref(daysAgo(7))
const endDate = ref(daysAgo(0))
const keyword = ref('')
const provider = ref('')
const status = ref('')
const currentPage = ref(1)
const pageSize = ref(50)
const total = ref(0)
const totalPages = ref(0)
const tooltip = ref(null)

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

function daysAgo(days) {
  const date = new Date()
  date.setDate(date.getDate() - days)
  return date.toISOString().slice(0, 10)
}

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
  return Math.max(0, totalInputTokens(item) - cacheTokens(item))
}

function cacheTokens(item) {
  return Number(item.cache_read_tokens || 0) + Number(item.cache_write_tokens || 0)
}

async function load(resetPage = false) {
  if (resetPage) currentPage.value = 1
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

const cards = computed(() => [
  { label: '请求数', value: formatNum(summary.value.requests), sub: `异常 ${formatNum(summary.value.errors)}` },
  { label: '输入 Token', value: formatNum(summary.value.input_tokens), sub: `缓存读取 ${formatNum(summary.value.cache_read_tokens)}` },
  { label: '输出 Token', value: formatNum(summary.value.output_tokens), sub: `缓存写入 ${formatNum(summary.value.cache_write_tokens)}` },
  { label: '成本', value: formatCredits(summary.value.credits), sub: `首字 ${formatDuration(summary.value.average_first_byte_ms)} · 耗时 ${formatDuration(summary.value.average_duration_ms)}` },
])

onMounted(load)
</script>

<template>
  <div class="h-full overflow-y-auto bg-[#fbfbfc] px-3 py-3">
    <div class="mx-auto max-w-[1500px] space-y-3">
      <header class="flex items-center justify-between gap-3">
        <div>
          <h1 class="text-[22px] font-semibold tracking-tight text-[#0f172a]">请求统计</h1>
          <p class="mt-0.5 text-[12px] text-[#64748b]">逐条记录上游请求指标，不保存请求内容和返回内容。</p>
        </div>
        <div class="flex items-center gap-2 rounded-xl border border-[#e8edf5] bg-white p-1.5 shadow-sm">
          <input v-model="startDate" type="date" class="h-8 rounded-lg border border-[#e2e8f0] px-2 text-[12px]" />
          <span class="text-[12px] text-[#94a3b8]">至</span>
          <input v-model="endDate" type="date" class="h-8 rounded-lg border border-[#e2e8f0] px-2 text-[12px]" />
          <button class="h-8 rounded-lg bg-[#1677ff] px-3 text-[12px] font-medium text-white disabled:opacity-60" :disabled="loading" @click="load(true)">
            {{ loading ? '加载中' : '查询' }}
          </button>
        </div>
      </header>

      <div class="flex flex-wrap items-end gap-2 rounded-xl border border-[#e8edf5] bg-white p-2 shadow-sm">
        <label class="flex min-w-[220px] flex-1 flex-col gap-1 text-[11px] text-[#64748b]">
          关键词
          <input v-model="keyword" class="h-8 rounded-lg border border-[#e2e8f0] px-2 text-[12px] text-[#0f172a]" placeholder="模型、端点、路径或错误信息" @keyup.enter="load(true)" />
        </label>
        <label class="flex w-[160px] flex-col gap-1 text-[11px] text-[#64748b]">
          提供商
          <input v-model="provider" class="h-8 rounded-lg border border-[#e2e8f0] px-2 text-[12px] text-[#0f172a]" placeholder="例如 openai" @keyup.enter="load(true)" />
        </label>
        <label class="flex w-[120px] flex-col gap-1 text-[11px] text-[#64748b]">
          状态
          <select v-model="status" class="h-8 rounded-lg border border-[#e2e8f0] bg-white px-2 text-[12px] text-[#0f172a]" @change="load(true)">
            <option value="">全部</option>
            <option value="success">成功</option>
            <option value="error">异常</option>
          </select>
        </label>
        <button class="h-8 rounded-lg border border-[#e2e8f0] px-3 text-[12px] text-[#475569] hover:bg-[#f8fafc]" :disabled="loading" @click="clearFilters">清空筛选</button>
      </div>

      <p v-if="error" class="rounded-xl border border-[#fecaca] bg-[#fef2f2] px-3 py-2 text-[12px] text-[#dc2626]">{{ error }}</p>

      <section class="grid grid-cols-4 gap-2.5">
        <div v-for="card in cards" :key="card.label" class="rounded-xl border border-[#edf0f5] bg-white px-4 py-3 shadow-sm">
          <div class="text-[11px] font-medium text-[#64748b]">{{ card.label }}</div>
          <div class="mt-1 text-[22px] font-semibold tabular-nums text-[#0f172a]">{{ card.value }}</div>
          <div class="mt-1 text-[11px] text-[#64748b]">{{ card.sub }}</div>
        </div>
      </section>

      <section class="overflow-hidden rounded-xl border border-[#e8edf5] bg-white shadow-sm">
        <div class="flex items-center justify-between border-b border-[#edf0f5] bg-[#fbfdff] px-3 py-2">
          <div>
            <h2 class="text-[14px] font-semibold text-[#0f172a]">请求记录</h2>
            <p class="mt-0.5 text-[11px] text-[#64748b]">按创建时间倒序显示，筛选结果共 {{ formatNum(total) }} 条</p>
          </div>
          <span class="rounded-full bg-white px-2 py-0.5 text-[11px] text-[#64748b] ring-1 ring-[#e2e8f0]">{{ items.length }} 条</span>
        </div>
        <div v-if="!loading && displayItems.length === 0" class="py-12 text-center text-[12px] text-[#64748b]">暂无请求统计</div>
        <div v-else class="max-h-[calc(100vh-260px)] overflow-auto">
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
              <tr v-for="item in displayItems" :key="item.id" class="border-b border-[#f1f5f9] last:border-0 hover:bg-[#fbfdff]">
                <td class="whitespace-nowrap py-2 pl-3 pr-3 font-mono text-[11px] text-[#475569]">{{ formatTime(item.created_at) }}</td>
                <td class="max-w-[220px] py-2 pr-3">
                  <div class="truncate font-medium text-[#0f172a]">{{ item.model_id || 'unknown' }}</div>
                  <div class="mt-0.5 truncate text-[10px] text-[#94a3b8]">{{ item.provider || '—' }}</div>
                </td>
                <td class="max-w-[220px] truncate py-2 pr-3 font-mono text-[10px] text-[#64748b]">{{ item.endpoint || item.request_path || '—' }}</td>
                <td class="py-2 pr-3"><span class="rounded-full px-2 py-0.5 text-[11px]" :class="item.success ? 'bg-[#ecfdf5] text-[#059669]' : 'bg-[#fef2f2] text-[#dc2626]'">{{ item.status_code || 'ERR' }}</span></td>
                <td class="py-2 pr-3 text-right tabular-nums">
                  <span class="cursor-help whitespace-nowrap" @mouseenter="showTooltip('tokens', item, $event)" @mousemove="moveTooltip" @mouseleave="hideTooltip">↓{{ formatNum(totalInputTokens(item)) }} ↑{{ formatNum(item.output_tokens) }}<span class="ml-1 text-[#059669]">缓存{{ formatNum(cacheTokens(item)) }}</span></span>
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
              <select v-model.number="pageSize" class="h-7 rounded-md border border-[#e2e8f0] bg-white px-1.5 text-[12px]" :disabled="loading" @change="load(true)">
                <option :value="25">25</option>
                <option :value="50">50</option>
                <option :value="100">100</option>
                <option :value="200">200</option>
              </select>
              条
            </label>
            <button class="h-7 rounded-md border border-[#e2e8f0] px-2.5 disabled:cursor-not-allowed disabled:opacity-40" :disabled="loading || currentPage <= 1" @click="changePage(currentPage - 1)">上一页</button>
            <button class="h-7 rounded-md border border-[#e2e8f0] px-2.5 disabled:cursor-not-allowed disabled:opacity-40" :disabled="loading || !totalPages || currentPage >= totalPages" @click="changePage(currentPage + 1)">下一页</button>
          </div>
        </div>
      </section>
    </div>

    <Teleport to="body">
      <div v-if="tooltip" class="pointer-events-none fixed z-[100] rounded-xl border border-[#334155] bg-[#0f172a] p-3 text-left text-[12px] text-white shadow-2xl" :style="tooltipStyle">
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
  </div>
</template>
