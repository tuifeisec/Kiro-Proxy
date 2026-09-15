<script setup>
import { useStats } from '../../composables/useStats.js'
import { useServiceStatus } from '../../composables/useServiceStatus.js'

const { stats } = useStats()
const { service } = useServiceStatus()
</script>

<template>
  <footer class="h-6 flex items-center gap-4 px-3 bg-[#f9fafb] border-t border-[#e5e7eb] text-xs text-[#6b7280] shrink-0">
    <span title="正在接收实时日志的 WebSocket 客户端数">活跃连接: {{ service.active_connections ?? 0 }}</span>
    <span class="text-[#e5e7eb]">|</span>
    <span>总请求: {{ stats.total_requests ?? 0 }}</span>
    <span class="flex-1"></span>
    <span>Kiro Proxy</span>
  </footer>
</template>
