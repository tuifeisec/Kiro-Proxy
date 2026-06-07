<script setup>
import { computed, ref } from 'vue'
import { useStats } from '../composables/useStats.js'
import StatsCard from '../components/dashboard/StatsCard.vue'
import ServiceStatus from '../components/dashboard/ServiceStatus.vue'
import CAStatus from '../components/dashboard/CAStatus.vue'
import ProxyConfig from '../components/dashboard/ProxyConfig.vue'
import AdvancedSettings from '../components/dashboard/AdvancedSettings.vue'
import UsageBreakdown from '../components/dashboard/UsageBreakdown.vue'
import IconLogs from '../components/icons/IconLogs.vue'
import IconDashboard from '../components/icons/IconDashboard.vue'
import IconWifi from '../components/icons/IconWifi.vue'
import IconArrowRight from '../components/icons/IconArrowRight.vue'

const { stats, loading, error, clearStats, refresh } = useStats()
const usageRefreshKey = ref(0)
const clearStatsBusy = ref(false)
const showClearStatsModal = ref(false)
const clearStatsError = ref('')
const showRangeMenu = ref(false)
const showMoreMenu = ref(false)
const selectedRangeLabel = ref('今天')
const selectedRangeDays = ref(0)

const rangeOptions = [
  { label: '今天', days: 0 },
  { label: '最近 7 天', days: 7 },
  { label: '最近 30 天', days: 30 },
]

function formatTokens(n) {
  const value = Number(n || 0)
  if (value >= 1_000_000) return (value / 1_000_000).toFixed(2).replace(/\.00$/, '') + 'M'
  if (value >= 1_000) return (value / 1_000).toFixed(1).replace(/\.0$/, '') + 'K'
  return String(value)
}

function formatCredits(n) {
  const value = Number(n || 0)
  if (value >= 100) return '$' + value.toFixed(2)
  if (value >= 1) return '$' + value.toFixed(4)
  return '$' + value.toFixed(6)
}

const cacheHitRate = computed(() => {
  const totalIn = Number(stats.value.input_tokens || 0)
  const cached = Math.min(Number(stats.value.cache_read_tokens || 0), totalIn)
  if (totalIn === 0) return 0
  return (cached / totalIn) * 100
})

const cards = computed(() => [
  {
    label: '总请求数',
    value: Number(stats.value.total_requests || 0).toLocaleString(),
    sub: `错误 ${stats.value.total_errors || 0} · 今日 +${Number(stats.value.today_requests || 0).toLocaleString()}`,
    subTone: stats.value.total_errors ? 'danger' : 'neutral',
    icon: IconLogs,
    tone: 'blue',
  },
  {
    label: '总 Token',
    value: formatTokens(stats.value.total_tokens),
    sub: `今日 ${formatTokens(stats.value.today_total_tokens)} · 输入 ${formatTokens(stats.value.input_tokens)}`,
    icon: IconDashboard,
    tone: 'green',
  },
  {
    label: '缓存命中',
    value: cacheHitRate.value ? cacheHitRate.value.toFixed(1) + '%' : '—',
    sub: `命中 ${formatTokens(stats.value.cache_read_tokens)} · 写入 ${formatTokens(stats.value.cache_write_tokens)}`,
    icon: IconWifi,
    tone: 'purple',
  },
  {
    label: 'Credits',
    value: formatCredits(stats.value.credits),
    sub: '按模型价格累计',
    icon: IconArrowRight,
    tone: 'amber',
  },
])

async function handleRefresh() {
  await refresh()
  usageRefreshKey.value++
}

function selectRange(option) {
  selectedRangeLabel.value = option.label
  selectedRangeDays.value = option.days
  showRangeMenu.value = false
}

function toggleRangeMenu() {
  showRangeMenu.value = !showRangeMenu.value
  showMoreMenu.value = false
}

function toggleMoreMenu() {
  showMoreMenu.value = !showMoreMenu.value
  showRangeMenu.value = false
}

function handleClearStats() {
  if (loading.value || clearStatsBusy.value) return
  clearStatsError.value = ''
  showMoreMenu.value = false
  showClearStatsModal.value = true
}

function closeClearStatsModal() {
  if (clearStatsBusy.value) return
  showClearStatsModal.value = false
  clearStatsError.value = ''
}

async function confirmClearStats() {
  if (clearStatsBusy.value) return
  clearStatsBusy.value = true
  clearStatsError.value = ''
  try {
    await clearStats()
    await refresh()
    usageRefreshKey.value++
    showClearStatsModal.value = false
  } catch (e) {
    clearStatsError.value = e?.message || '清空统计失败，请稍后重试'
  } finally {
    clearStatsBusy.value = false
  }
}
</script>

<template>
  <div class="min-h-full bg-[#fbfbfc] px-3 py-3">
    <div class="mx-auto max-w-[1320px] space-y-3">
      <header class="flex items-center justify-between gap-3">
        <div class="flex items-center gap-2.5">
          <h1 class="text-[22px] font-semibold tracking-tight text-[#0f172a]">仪表盘</h1>
          <span class="text-[12px] text-[#64748b]">Kiro Proxy 运行状态总览</span>
          <span class="inline-flex items-center gap-1.5 rounded-full border border-[#e2e8f0] bg-white px-2.5 py-0.5 text-[12px] text-[#475569] shadow-sm">
            <span class="h-1.5 w-1.5 rounded-full bg-[#22c55e]"></span>
            本地代理 · 127.0.0.1
          </span>
        </div>

        <div class="flex items-center gap-2">
          <button
            class="inline-flex h-8 items-center gap-1.5 rounded-lg border border-[#e2e8f0] bg-white px-2.5 text-[12px] font-medium text-[#475569] shadow-sm transition hover:bg-[#f8fafc] active:scale-[0.98] disabled:opacity-60"
            :disabled="loading"
            @click="handleRefresh"
          >
            <svg class="h-4 w-4" :class="{ 'animate-spin': loading }" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round">
              <path d="M20 11a8.1 8.1 0 0 0-14.1-4.6L4 8.5" />
              <path d="M4 4.5v4h4" />
              <path d="M4 13a8.1 8.1 0 0 0 14.1 4.6L20 15.5" />
              <path d="M20 19.5v-4h-4" />
            </svg>
            {{ loading ? '刷新中' : '刷新' }}
          </button>

          <div class="relative">
            <button
              class="inline-flex h-8 items-center gap-1.5 rounded-lg border border-[#e2e8f0] bg-white px-2.5 text-[12px] font-medium text-[#475569] shadow-sm transition hover:bg-[#f8fafc] active:scale-[0.98]"
              @click="toggleRangeMenu"
            >
              {{ selectedRangeLabel }}
              <svg
                class="h-3.5 w-3.5 text-[#94a3b8] transition-transform duration-150"
                :class="showRangeMenu ? 'rotate-180' : ''"
                viewBox="0 0 20 20"
                fill="none"
                aria-hidden="true"
              >
                <path d="M5 7.5L10 12.5L15 7.5" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
              </svg>
            </button>
            <div v-if="showRangeMenu" class="absolute right-0 top-9 z-20 w-28 overflow-hidden rounded-lg border border-[#e2e8f0] bg-white py-1 text-[12px] shadow-lg">
              <button
                v-for="option in rangeOptions"
                :key="option.label"
                class="block w-full px-3 py-1.5 text-left text-[#475569] hover:bg-[#f8fafc]"
                :class="option.label === selectedRangeLabel ? 'font-semibold text-[#1677ff]' : ''"
                @click="selectRange(option)"
              >
                {{ option.label }}
              </button>
            </div>
          </div>

          <span class="inline-flex h-8 items-center gap-1.5 rounded-lg border border-[#dcfce7] bg-white px-2.5 text-[12px] font-medium text-[#059669] shadow-sm">
            <span class="h-1.5 w-1.5 rounded-full bg-[#22c55e]"></span>
            运行中
          </span>

          <div class="relative">
            <button
              class="flex h-8 w-8 items-center justify-center rounded-lg border border-[#e2e8f0] bg-white text-[#64748b] shadow-sm transition hover:bg-[#f8fafc] active:scale-[0.98]"
              @click="toggleMoreMenu"
            >
              ···
            </button>
            <div v-if="showMoreMenu" class="absolute right-0 top-9 z-20 w-28 overflow-hidden rounded-lg border border-[#e2e8f0] bg-white py-1 text-[12px] shadow-lg">
              <button class="block w-full px-3 py-1.5 text-left text-[#475569] hover:bg-[#f8fafc]" @click="handleRefresh">刷新数据</button>
              <button class="block w-full px-3 py-1.5 text-left text-[#dc2626] hover:bg-[#fef2f2]" @click="handleClearStats">清空统计</button>
            </div>
          </div>
        </div>
      </header>

      <p v-if="error" class="rounded-xl border border-[#fecaca] bg-[#fef2f2] px-3 py-2 text-[12px] text-[#dc2626]">统计加载失败：{{ error }}</p>

      <section class="grid grid-cols-4 gap-2.5">
        <StatsCard v-for="card in cards" :key="card.label" v-bind="card" />
      </section>

      <section class="grid grid-cols-[1fr_320px] gap-3">
        <div class="space-y-3">
          <div class="grid grid-cols-3 gap-3">
            <ServiceStatus />
            <CAStatus />
            <ProxyConfig />
          </div>
          <UsageBreakdown :refresh-key="usageRefreshKey" :days="selectedRangeDays" />
        </div>

        <aside class="space-y-3">
          <AdvancedSettings />

          <div class="rounded-xl border border-[#fecaca] bg-[#fffafa] p-3 shadow-[0_1px_3px_rgba(15,23,42,0.04)]">
            <div class="mb-2.5 flex items-center gap-2 text-[#dc2626]">
              <svg class="h-4 w-4" viewBox="0 0 20 20" fill="none">
                <path d="M10 6v4.5m0 3h.01M17 10a7 7 0 1 1-14 0 7 7 0 0 1 14 0Z" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" />
              </svg>
              <h3 class="text-[14px] font-semibold">危险操作</h3>
            </div>
            <p class="mb-3 text-[12px] leading-4 text-[#7f1d1d]">此操作不可逆，请谨慎操作。</p>
            <button
              class="inline-flex h-8 w-full items-center justify-center gap-2 rounded-lg border border-[#fecaca] bg-white text-[12px] font-semibold text-[#dc2626] transition hover:bg-[#fef2f2] disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="loading || clearStatsBusy"
              @click="handleClearStats"
            >
              <svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round">
                <path d="M4 7h16" />
                <path d="M10 11v6M14 11v6" />
                <path d="M6 7l1 14h10l1-14" />
                <path d="M9 7V4h6v3" />
              </svg>
              {{ clearStatsBusy ? '清空中…' : '清空统计' }}
            </button>
          </div>
        </aside>
      </section>
    </div>
  </div>

  <Teleport to="body">
    <div
      v-if="showClearStatsModal"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/30 px-4 backdrop-blur-sm"
      @click.self="closeClearStatsModal"
    >
      <div class="w-full max-w-md overflow-hidden rounded-2xl bg-white shadow-2xl ring-1 ring-black/5">
        <div class="flex items-start gap-3 border-b border-[#fee2e2] bg-gradient-to-br from-[#fff7ed] to-white p-4">
          <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-[#fee2e2] text-[#dc2626]">
            <svg class="h-5 w-5" viewBox="0 0 20 20" fill="none" aria-hidden="true">
              <path d="M10 6v4.5m0 3h.01M17 10a7 7 0 1 1-14 0 7 7 0 0 1 14 0Z" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
          </div>
          <div class="min-w-0">
            <h2 class="text-base font-semibold text-[#111827]">确认清空统计？</h2>
            <p class="mt-1 text-xs leading-5 text-[#6b7280]">此操作会清空请求、Token、Credits 和用量明细。清空后历史统计无法恢复。</p>
          </div>
          <button class="ml-auto text-[#94a3b8] hover:text-[#475569]" :disabled="clearStatsBusy" @click="closeClearStatsModal">×</button>
        </div>

        <div class="space-y-2 p-4">
          <div class="rounded-xl border border-[#fde68a] bg-[#fffbeb] px-3 py-2 text-xs leading-5 text-[#92400e]">
            代理服务不会停止，只会重置仪表盘统计数据。
          </div>
          <p v-if="clearStatsError" class="rounded-xl border border-[#fecaca] bg-[#fef2f2] px-3 py-2 text-xs text-[#dc2626]">
            {{ clearStatsError }}
          </p>
        </div>

        <div class="flex justify-end gap-2 border-t border-[#f3f4f6] bg-[#fafafa] px-4 py-3">
          <button
            class="rounded-lg border border-[#e5e7eb] bg-white px-4 py-2 text-xs font-medium text-[#374151] transition-colors hover:bg-[#f9fafb] disabled:opacity-50"
            :disabled="clearStatsBusy"
            @click="closeClearStatsModal"
          >
            取消
          </button>
          <button
            class="rounded-lg bg-[#dc2626] px-4 py-2 text-xs font-medium text-white shadow-sm transition-colors hover:bg-[#b91c1c] disabled:cursor-not-allowed disabled:opacity-60"
            :disabled="clearStatsBusy"
            @click="confirmClearStats"
          >
            {{ clearStatsBusy ? '正在清空…' : '确认清空' }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
