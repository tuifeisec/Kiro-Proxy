<script setup>
import { useServiceStatus } from '../../composables/useServiceStatus.js'

const { service, status, uptime } = useServiceStatus()
</script>

<template>
  <div class="ui-card min-w-0 p-3">
    <div class="mb-2 flex items-center justify-between">
      <h3 class="text-[14px] font-semibold text-[#0f172a]">代理服务状态</h3>
      <span
        class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[12px] font-medium"
        :class="{
          'ui-status-success': status === 'running',
          'ui-status-danger': status === 'stopped',
          'bg-[#f8fafc] text-[#64748b]': status === 'checking',
        }"
      >
        <span class="h-1.5 w-1.5 rounded-full" :class="status === 'running' ? 'bg-[#22c55e]' : status === 'stopped' ? 'bg-[#ef4444]' : 'bg-[#94a3b8]'"></span>
        {{ status === 'running' ? '运行中' : status === 'stopped' ? '已停止' : '检测中' }}
      </span>
    </div>

    <div class="ui-card overflow-hidden bg-[#fbfdff]">
      <div class="grid grid-cols-[24px_1fr_auto] items-center border-b border-[#edf0f5] px-2.5 py-1.5 text-[12px]">
        <svg class="h-3.5 w-3.5 text-[#2563eb]" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg><span class="text-[#64748b]">监听地址</span><span class="font-mono text-[#0f172a]">{{ service.proxy_url }}</span>
      </div>
      <div class="grid grid-cols-[24px_1fr_auto] items-center border-b border-[#edf0f5] px-2.5 py-1.5 text-[12px]">
        <svg class="h-3.5 w-3.5 text-[#2563eb]" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="3" width="18" height="18" rx="2"/><path d="M3 9h18M9 3v18"/></svg><span class="text-[#64748b]">管理端口</span><span class="font-mono text-[#0f172a]">{{ service.ui_port }}</span>
      </div>
      <div class="grid grid-cols-[24px_1fr_auto] items-center border-b border-[#edf0f5] px-2.5 py-1.5 text-[12px]">
        <svg class="h-3.5 w-3.5 text-[#2563eb]" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 7h16M4 12h16M4 17h16"/></svg><span class="text-[#64748b]">协议</span><span class="text-[#0f172a]">{{ service.protocol }}</span>
      </div>
      <div class="grid grid-cols-[24px_1fr_auto] items-center px-2.5 py-1.5 text-[12px]">
        <svg class="h-3.5 w-3.5 text-[#2563eb]" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 3"/></svg><span class="text-[#64748b]">运行时长</span><span class="font-mono text-[#0f172a]">{{ uptime || '—' }}</span>
      </div>
    </div>

  </div>
</template>
