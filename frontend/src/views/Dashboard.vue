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

function formatTokens(n) {
  if (!n) return '0'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return String(n)
}

function formatCredits(n) {
  const value = Number(n || 0)
  if (value >= 100) return value.toFixed(2)
  if (value >= 1) return value.toFixed(4)
  return value.toFixed(6)
}

const cacheHitRate = computed(() => {
  const totalIn = stats.value.input_tokens || 0
  const cached = Math.min(stats.value.cache_read_tokens || 0, totalIn)
  if (totalIn === 0) return '—'
  return ((cached / totalIn) * 100).toFixed(1) + '%'
})

const cards = computed(() => [
  {
    label: '总请求数',
    value: stats.value.total_requests ?? 0,
    sub: stats.value.total_errors ? `错误 ${stats.value.total_errors}` : '累计代理请求',
    color: '#10b981',
    icon: IconLogs,
  },
  {
    label: '总 Token',
    value: formatTokens(stats.value.total_tokens),
    sub: `输入 ${formatTokens(stats.value.input_tokens)} / 输出 ${formatTokens(stats.value.output_tokens)}`,
    color: '#111827',
    icon: IconDashboard,
  },
  {
    label: '缓存命中',
    value: formatTokens(stats.value.cache_read_tokens),
    sub: `命中率 ${cacheHitRate.value}`,
    color: '#10b981',
    icon: IconWifi,
  },
  {
    label: 'Credits',
    value: formatCredits(stats.value.credits),
    sub: '按模型价格累计',
    color: '#f59e0b',
    icon: IconArrowRight,
  },
])

function handleClearStats() {
  if (loading.value || clearStatsBusy.value) return
  clearStatsError.value = ''
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

function formatUptime(s) {
  if (!s) return '—'
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
}
</script>

<template>
  <div class="p-6 space-y-6 max-w-5xl">
    <div class="flex items-start justify-between gap-3">
      <div>
        <h1 class="text-lg font-semibold text-[#111827]">仪表盘</h1>
        <p class="text-xs text-[#6b7280] mt-0.5">Kiro Proxy 运行状态总览</p>
        <p v-if="error" class="text-xs text-[#dc2626] mt-1">统计加载失败：{{ error }}</p>
      </div>
      <button
        class="text-xs px-3 py-1.5 border border-[#fecaca] text-[#dc2626] rounded hover:bg-[#fef2f2] disabled:opacity-50 disabled:cursor-not-allowed"
        :disabled="loading || clearStatsBusy"
        @click="handleClearStats"
      >
        {{ clearStatsBusy ? '清空中…' : '清空统计' }}
      </button>
    </div>

    <div class="grid grid-cols-4 gap-3">
      <StatsCard
        v-for="card in cards"
        :key="card.label"
        v-bind="card"
      />
    </div>

    <div class="grid grid-cols-3 gap-4">
      <ServiceStatus />
      <CAStatus />
      <ProxyConfig />
    </div>

    <div class="grid grid-cols-1 gap-4 lg:grid-cols-3">
      <AdvancedSettings />
      <UsageBreakdown :refresh-key="usageRefreshKey" />
    </div>
  </div>

  <Teleport to="body">
    <div
      v-if="showClearStatsModal"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/35 px-4 backdrop-blur-sm"
      @click.self="closeClearStatsModal"
    >
      <div class="w-full max-w-md overflow-hidden rounded-2xl bg-white shadow-2xl ring-1 ring-black/5">
        <div class="flex items-start gap-3 border-b border-[#fee2e2] bg-gradient-to-br from-[#fff7ed] to-white p-5">
          <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-[#fee2e2] text-[#dc2626]">
            <svg class="h-5 w-5" viewBox="0 0 20 20" fill="none" aria-hidden="true">
              <path d="M10 6v4.5m0 3h.01M17 10a7 7 0 1 1-14 0 7 7 0 0 1 14 0Z" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
          </div>
          <div>
            <h2 class="text-base font-semibold text-[#111827]">确认清空统计？</h2>
            <p class="mt-1 text-xs leading-5 text-[#6b7280]">该操作会清空累计请求、Token、Credits 和用量明细。清空后历史统计无法恢复。</p>
          </div>
        </div>

        <div class="space-y-3 p-5">
          <div class="rounded-xl border border-[#fde68a] bg-[#fffbeb] px-3 py-2 text-xs leading-5 text-[#92400e]">
            代理服务不会停止，只会重置仪表盘统计数据。
          </div>
          <p v-if="clearStatsError" class="rounded-xl border border-[#fecaca] bg-[#fef2f2] px-3 py-2 text-xs text-[#dc2626]">
            {{ clearStatsError }}
          </p>
        </div>

        <div class="flex justify-end gap-2 border-t border-[#f3f4f6] bg-[#fafafa] px-5 py-4">
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
