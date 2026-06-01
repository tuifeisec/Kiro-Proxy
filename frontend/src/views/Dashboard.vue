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

async function handleClearStats() {
  if (clearStatsBusy.value) return
  const prompts = [
    '第一次确认：确定要清空所有累计统计和用量明细吗？',
    '第二次确认：清空后历史统计无法恢复，继续吗？',
    '第三次确认：请再次确认清空统计。',
  ]
  for (const message of prompts) {
    if (!confirm(message)) return
  }

  clearStatsBusy.value = true
  try {
    await clearStats()
    await refresh()
    usageRefreshKey.value++
  } catch (e) {
    alert(`清空统计失败：${e.message}`)
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
</template>
