<script setup>
import { useWebSocket } from '../../composables/useWebSocket.js'
import { useStats } from '../../composables/useStats.js'

const { connected } = useWebSocket()
const { stats } = useStats()
</script>

<template>
  <footer class="h-6 flex items-center gap-4 px-3 bg-[#f9fafb] border-t border-[#e5e7eb] text-xs text-[#6b7280] shrink-0">
    <span class="flex items-center gap-1">
      <span
        class="w-1.5 h-1.5 rounded-full"
        :class="connected ? 'bg-[#10b981]' : 'bg-[#ef4444]'"
      ></span>
      {{ connected ? 'WS 已连接' : 'WS 断开' }}
    </span>

    <span class="text-[#e5e7eb]">|</span>
    <span>活跃连接: {{ stats.active_connections ?? 0 }}</span>
    <span class="text-[#e5e7eb]">|</span>
    <span>总请求: {{ stats.total_requests ?? 0 }}</span>
    <span class="flex-1"></span>
    <span>Kiro Proxy</span>
  </footer>
</template>
