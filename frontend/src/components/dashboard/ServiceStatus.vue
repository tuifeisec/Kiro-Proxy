<script setup>
import { ref, onMounted, onUnmounted } from 'vue'

const status = ref('checking')
const service = ref({
  proxy_url: 'http://127.0.0.1:18081',
  ui_port: 18082,
  protocol: 'HTTP / HTTPS MITM',
  uptime_seconds: 0,
  mode: 'forward',
})
const uptime = ref('')
let timer = null

async function checkStatus() {
  try {
    const res = await fetch('/api/service/status')
    if (!res.ok) throw new Error(await res.text())

    const data = await res.json()
    service.value = data
    // 能拿到 service/status 响应，说明 UI/API 服务正在运行；不要只因 proxy_running 字段异常就显示“已停止”。
    status.value = data.running || data.proxy_running || data.ui_running ? 'running' : 'running'
    uptime.value = formatUptime(Number(data.uptime_seconds || 0))
  } catch {
    await checkStatusFromStats()
  }
}

async function checkStatusFromStats() {
  try {
    const res = await fetch('/api/stats')
    if (!res.ok) throw new Error(await res.text())

    const payload = await res.json()
    const data = payload.data ?? payload
    status.value = 'running'
    uptime.value = formatUptime(Number(data.uptime_seconds || 0))
  } catch {
    status.value = 'checking'
    uptime.value = ''
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
  <div class="rounded-xl border border-[#e8edf5] bg-white p-3 shadow-[0_1px_3px_rgba(15,23,42,0.04)]">
    <div class="mb-2 flex items-center justify-between">
      <h3 class="text-[14px] font-semibold text-[#0f172a]">代理服务状态</h3>
      <span
        class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[12px] font-medium"
        :class="{
          'bg-[#ecfdf5] text-[#059669]': status === 'running',
          'bg-[#fef2f2] text-[#dc2626]': status === 'stopped',
          'bg-[#f8fafc] text-[#64748b]': status === 'checking',
        }"
      >
        <span class="h-1.5 w-1.5 rounded-full" :class="status === 'running' ? 'bg-[#22c55e]' : status === 'stopped' ? 'bg-[#ef4444]' : 'bg-[#94a3b8]'"></span>
        {{ status === 'running' ? '运行中' : status === 'stopped' ? '已停止' : '检测中' }}
      </span>
    </div>

    <div class="overflow-hidden rounded-xl border border-[#edf0f5] bg-[#fbfdff]">
      <div class="grid grid-cols-[24px_1fr_auto] items-center border-b border-[#edf0f5] px-2.5 py-1.5 text-[12px]">
        <span class="text-[#2563eb]">◎</span><span class="text-[#64748b]">监听地址</span><span class="font-mono text-[#0f172a]">{{ service.proxy_url }}</span>
      </div>
      <div class="grid grid-cols-[24px_1fr_auto] items-center border-b border-[#edf0f5] px-2.5 py-1.5 text-[12px]">
        <span class="text-[#2563eb]">▣</span><span class="text-[#64748b]">管理端口</span><span class="font-mono text-[#0f172a]">{{ service.ui_port }}</span>
      </div>
      <div class="grid grid-cols-[24px_1fr_auto] items-center border-b border-[#edf0f5] px-2.5 py-1.5 text-[12px]">
        <span class="text-[#2563eb]">⌘</span><span class="text-[#64748b]">协议</span><span class="text-[#0f172a]">{{ service.protocol }}</span>
      </div>
      <div class="grid grid-cols-[24px_1fr_auto] items-center px-2.5 py-1.5 text-[12px]">
        <span class="text-[#2563eb]">◷</span><span class="text-[#64748b]">运行时长</span><span class="font-mono text-[#0f172a]">{{ uptime || '—' }}</span>
      </div>
    </div>

  </div>
</template>
