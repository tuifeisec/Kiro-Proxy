<script setup>
import { ref, onMounted, onUnmounted } from 'vue'

const status = ref('checking')
const uptime = ref('')
let timer = null

async function checkStatus() {
  try {
    const res = await fetch('/api/stats', { signal: AbortSignal.timeout(3000) })
    if (res.ok) {
      const data = await res.json()
      const seconds = data?.data?.uptime_seconds ?? data?.uptime_seconds ?? 0
      status.value = 'running'
      uptime.value = formatUptime(seconds)
    } else {
      status.value = 'stopped'
    }
  } catch {
    status.value = 'stopped'
  }
}

function formatUptime(seconds) {
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  return `${h}h ${m}m`
}

onMounted(() => {
  checkStatus()
  timer = setInterval(checkStatus, 5000)
})
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <div class="bg-white border border-[#e5e7eb] rounded-lg p-4 hover:border-[#d1d5db] transition-colors">
    <div class="flex items-center justify-between mb-3">
      <h3 class="text-sm font-medium text-[#111827]">代理服务</h3>
      <span
        class="text-xs px-2 py-0.5 rounded-full font-medium"
        :class="{
          'bg-[#ecfdf5] text-[#10b981]': status === 'running',
          'bg-[#fef2f2] text-[#ef4444]': status === 'stopped',
          'bg-[#f9fafb] text-[#6b7280]': status === 'checking',
        }"
      >
        {{ status === 'running' ? '运行中' : status === 'stopped' ? '已停止' : '检测中' }}
      </span>
    </div>

    <div class="grid grid-cols-2 gap-3 text-sm">
      <div>
        <div class="text-[#6b7280] text-xs mb-0.5">监听地址</div>
        <div class="text-[#111827] font-mono">127.0.0.1:18081</div>
      </div>
      <div>
        <div class="text-[#6b7280] text-xs mb-0.5">运行时长</div>
        <div class="text-[#111827] font-mono">{{ uptime || '—' }}</div>
      </div>
      <div>
        <div class="text-[#6b7280] text-xs mb-0.5">管理端口</div>
        <div class="text-[#111827] font-mono">18082</div>
      </div>
      <div>
        <div class="text-[#6b7280] text-xs mb-0.5">协议</div>
        <div class="text-[#111827]">HTTP / HTTPS MITM</div>
      </div>
    </div>
  </div>
</template>
