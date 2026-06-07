<script setup>
import { useCAStatus } from '../../composables/useCAStatus.js'

const { caStatus, installing, installResult, install } = useCAStatus()
</script>

<template>
  <div class="rounded-xl border border-[#e8edf5] bg-white p-3 shadow-[0_1px_3px_rgba(15,23,42,0.04)]">
    <div class="mb-2 flex items-center justify-between">
      <h3 class="text-[14px] font-semibold text-[#0f172a]">CA 证书</h3>
      <span
        class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[12px] font-medium"
        :class="caStatus.installed
          ? 'bg-[#ecfdf5] text-[#059669]'
          : caStatus.cert_exists
            ? 'bg-[#fffbeb] text-[#d97706]'
            : 'bg-[#fef2f2] text-[#dc2626]'"
      >
        <span class="h-1.5 w-1.5 rounded-full" :class="caStatus.installed ? 'bg-[#22c55e]' : caStatus.cert_exists ? 'bg-[#f59e0b]' : 'bg-[#ef4444]'"></span>
        {{ caStatus.installed ? '已安装并受信任' : caStatus.cert_exists ? '未安装' : '未生成' }}
      </span>
    </div>

    <div v-if="caStatus.installed" class="space-y-2">
      <div class="rounded-xl border border-[#dcfce7] bg-[#f0fdf4] px-3 py-2">
        <div class="mb-1.5 flex items-center gap-2 text-[12px] font-semibold text-[#059669]">
          <span>♡</span>
          <span>已安装并受信任</span>
        </div>
        <div class="grid grid-cols-[72px_1fr] gap-y-1.5 text-[12px]">
          <span class="text-[#64748b]">证书名称</span><span class="truncate text-[#0f172a]">{{ caStatus.subject || 'Kiro Proxy CA' }}</span>
          <span class="text-[#64748b]">到期时间</span><span class="text-[#0f172a]">{{ caStatus.not_after || '—' }}</span>
        </div>
      </div>

      <div class="space-y-2">
        <button
          class="h-8 w-full rounded-lg border border-[#fde68a] bg-white text-[12px] font-medium text-[#d97706] hover:bg-[#fffbeb] disabled:cursor-not-allowed disabled:opacity-60"
          :disabled="installing"
          @click="install"
        >
          {{ installing ? '重新安装中...' : '重新安装证书' }}
        </button>
      </div>
    </div>

    <div v-else-if="caStatus.cert_exists" class="space-y-2">
      <div class="flex items-center justify-between rounded-xl border border-[#fde68a] bg-[#fffbeb] px-3 py-2 text-[12px] text-[#d97706]">
        <span class="inline-flex items-center gap-2 font-semibold"><span>!</span>未安装</span>
        <button
          class="h-7 rounded-lg border border-[#e2e8f0] bg-white px-3 text-[12px] font-semibold text-[#0f172a] shadow-sm hover:bg-[#f8fafc] disabled:cursor-not-allowed disabled:opacity-60"
          :disabled="installing"
          @click="install"
        >
          {{ installing ? '安装中...' : '安装 CA 证书' }}
        </button>
      </div>
      <button
        class="h-8 w-full rounded-lg border border-[#e2e8f0] bg-white text-[12px] font-medium text-[#64748b] opacity-60"
        disabled
        title="证书已存在；当前后端未提供重新生成 CA 证书接口"
      >
        重新生成 CA 证书
      </button>
    </div>

    <div v-else class="space-y-2">
      <div class="flex items-center justify-between rounded-xl border border-[#fecaca] bg-[#fef2f2] px-3 py-2 text-[12px] text-[#dc2626]">
        <span class="inline-flex items-center gap-2 font-semibold"><span>×</span>未生成</span>
        <button
          class="h-7 rounded-lg border border-[#e2e8f0] bg-white px-3 text-[12px] font-semibold text-[#0f172a] opacity-60"
          disabled
          title="当前后端未提供生成 CA 证书接口"
        >
          生成 CA 证书
        </button>
      </div>
      <button
        class="h-8 w-full rounded-lg border border-[#e2e8f0] bg-white text-[12px] font-medium text-[#64748b] opacity-60"
        disabled
        title="需要先生成证书后才能安装"
      >
        安装 CA 证书
      </button>
    </div>

    <div v-if="installResult" class="mt-2 rounded-lg border px-3 py-1.5 text-[12px]" :class="installResult.includes('✓') ? 'border-[#bbf7d0] bg-[#f0fdf4] text-[#059669]' : 'border-[#fde68a] bg-[#fffbeb] text-[#92400e]'">
      {{ installResult }}
    </div>
  </div>
</template>
