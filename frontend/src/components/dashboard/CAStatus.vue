<script setup>
import { useCAStatus } from '../../composables/useCAStatus.js'
import IconClose from '../icons/IconClose.vue'

const { caStatus, installing, installResult, install } = useCAStatus()
</script>

<template>
  <div class="ui-card min-w-0 p-3">
    <div class="mb-2 flex items-center justify-between">
      <h3 class="text-[14px] font-semibold text-ink">CA 证书</h3>
      <span
        class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[12px] font-medium"
        :class="caStatus.installed
          ? 'ui-status-success'
          : caStatus.cert_exists
            ? 'ui-status-warning'
            : 'ui-status-danger'"
      >
        <span class="h-1.5 w-1.5 rounded-full" :class="caStatus.installed ? 'bg-success' : caStatus.cert_exists ? 'bg-warning' : 'bg-danger'"></span>
        {{ caStatus.installed ? '已安装并受信任' : caStatus.cert_exists ? '未安装' : '未生成' }}
      </span>
    </div>

    <div v-if="caStatus.installed" class="space-y-2">
      <div class="rounded-lg border border-success-tint-line bg-success-tint px-3 py-2">
        <div class="mb-1.5 flex items-center gap-2 text-[12px] font-semibold text-success">
          <svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg>
          <span>已安装并受信任</span>
        </div>
        <div class="grid grid-cols-[72px_1fr] gap-y-1.5 text-[12px]">
          <span class="text-ink-3">证书名称</span><span class="truncate text-ink">{{ caStatus.subject || 'Kiro Proxy CA' }}</span>
          <span class="text-ink-3">到期时间</span><span class="text-ink">{{ caStatus.not_after || '—' }}</span>
        </div>
      </div>

      <div class="space-y-2">
        <button
          class="h-8 w-full ui-control text-[12px] font-medium text-warning hover:bg-warning-tint disabled:cursor-not-allowed disabled:opacity-60"
          :disabled="installing"
          @click="install"
        >
          {{ installing ? '重新安装中...' : '重新安装证书' }}
        </button>
      </div>
    </div>

    <div v-else-if="caStatus.cert_exists" class="space-y-2">
      <div class="flex items-center justify-between rounded-lg border border-warning-tint-line bg-warning-tint px-3 py-2 text-[12px] text-warning">
        <span class="inline-flex items-center gap-2 font-semibold"><svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 9v4"/><path d="M12 17h.01"/><path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/></svg>未安装</span>
        <button
          class="h-7 ui-control px-3 text-[12px] font-semibold text-ink hover:bg-hover disabled:cursor-not-allowed disabled:opacity-60"
          :disabled="installing"
          @click="install"
        >
          {{ installing ? '安装中...' : '安装 CA 证书' }}
        </button>
      </div>
      <button
        class="h-8 w-full ui-control text-[12px] font-medium text-ink-3 opacity-60"
        disabled
        title="证书已存在；当前后端未提供重新生成 CA 证书接口"
      >
        重新生成 CA 证书
      </button>
    </div>

    <div v-else class="space-y-2">
      <div class="flex items-center justify-between rounded-lg border border-danger-tint-line bg-danger-tint px-3 py-2 text-[12px] text-danger">
        <span class="inline-flex items-center gap-2 font-semibold"><IconClose class="h-4 w-4" />未生成</span>
        <button
          class="h-7 ui-control px-3 text-[12px] font-semibold text-ink opacity-60"
          disabled
          title="当前后端未提供生成 CA 证书接口"
        >
          生成 CA 证书
        </button>
      </div>
      <button
        class="h-8 w-full ui-control text-[12px] font-medium text-ink-3 opacity-60"
        disabled
        title="需要先生成证书后才能安装"
      >
        安装 CA 证书
      </button>
    </div>

    <div v-if="installResult" class="mt-2 rounded-lg border px-3 py-1.5 text-[12px]" :class="installResult.includes('✓') ? 'border-success-tint-line bg-success-tint text-success-fg' : 'border-warning-tint-line bg-warning-tint text-warning-fg'">
      {{ installResult }}
    </div>
  </div>
</template>
