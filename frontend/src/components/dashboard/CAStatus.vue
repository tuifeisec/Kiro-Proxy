<script setup>
import { useCAStatus } from '../../composables/useCAStatus.js'
import IconShield from '../icons/IconShield.vue'

const { caStatus, installing, installResult, install } = useCAStatus()
</script>

<template>
  <div class="bg-white border border-[#e5e7eb] rounded-lg p-4 hover:border-[#d1d5db] transition-colors">
    <div class="flex items-center justify-between mb-3">
      <h3 class="text-sm font-medium text-[#111827]">CA 证书</h3>
      <span
        class="text-xs px-2 py-0.5 rounded-full font-medium"
        :class="caStatus.installed
          ? 'bg-[#ecfdf5] text-[#10b981]'
          : caStatus.cert_exists
            ? 'bg-[#fffbeb] text-[#f59e0b]'
            : 'bg-[#fef2f2] text-[#ef4444]'"
      >
        {{ caStatus.installed ? '已信任' : caStatus.cert_exists ? '未安装' : '未生成' }}
      </span>
    </div>

    <div class="space-y-2 text-sm mb-3">
      <div v-if="caStatus.subject" class="flex gap-2">
        <span class="text-[#6b7280] shrink-0">名称</span>
        <span class="text-[#111827]">{{ caStatus.subject }}</span>
      </div>
      <div v-if="caStatus.not_after" class="flex gap-2">
        <span class="text-[#6b7280] shrink-0">到期</span>
        <span class="text-[#111827]">{{ caStatus.not_after }}</span>
      </div>
    </div>

    <div v-if="installResult" class="text-xs mb-2 p-2 rounded bg-[#f9fafb]"
      :class="installResult.includes('✓') ? 'text-[#10b981]' : 'text-[#f59e0b]'">
      {{ installResult }}
    </div>

    <button
      v-if="caStatus.cert_exists && !caStatus.installed"
      class="w-full py-2 rounded text-sm font-medium bg-[#10b981] text-white hover:bg-[#059669] disabled:opacity-50 disabled:cursor-not-allowed transition-colors flex items-center justify-center gap-2"
      :disabled="installing"
      @click="install"
    >
      <IconShield v-if="!installing" />
      {{ installing ? '安装中（等待系统授权）...' : '安装 CA 证书到系统' }}
    </button>

    <div v-else-if="caStatus.installed" class="text-xs text-[#6b7280]">
      证书已信任，HTTPS 流量可正常解密
    </div>

    <div v-else-if="!caStatus.cert_exists" class="text-xs text-[#ef4444]">
      CA 证书未找到，请重新启动代理程序
    </div>
  </div>
</template>
