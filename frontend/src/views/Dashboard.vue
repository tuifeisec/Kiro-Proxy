<script setup>
import { computed } from 'vue'
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

const { stats } = useStats()

function formatTokens(n) {
  if (!n) return '0'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return String(n)
}

const cacheHitRate = computed(() => {
  const totalIn = stats.value.input_tokens || 0
  const cached = stats.value.cache_read_tokens || 0
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
    label: '运行时长',
    value: formatUptime(stats.value.uptime_seconds),
    sub: '自上次启动',
    color: '#6b7280',
    icon: IconArrowRight,
  },
])

function formatUptime(s) {
  if (!s) return '—'
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
}
</script>

<template>
  <div class="p-6 space-y-6 max-w-5xl">
    <div>
      <h1 class="text-lg font-semibold text-[#111827]">仪表盘</h1>
      <p class="text-xs text-[#6b7280] mt-0.5">Kiro Proxy 运行状态总览</p>
    </div>

    <div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
      <StatsCard
        v-for="card in cards"
        :key="card.label"
        v-bind="card"
      />
    </div>

    <div class="grid grid-cols-1 gap-4 lg:grid-cols-3">
      <ServiceStatus />
      <CAStatus />
      <ProxyConfig />
      <AdvancedSettings />
      <UsageBreakdown />
    </div>
  </div>
</template>
