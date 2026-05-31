<script setup>
import { ref, onMounted } from 'vue'
import IconCopy from '../icons/IconCopy.vue'
import IconCheck from '../icons/IconCheck.vue'

const proxyHost = '127.0.0.1'
const proxyPort = '18081'
const copied = ref(false)

// Kiro 代理配置状态
const kiroStatus = ref(null)
const loading = ref(false)
const restartLoading = ref(false)
const result = ref('')

async function copyToClipboard(text) {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = true
    setTimeout(() => { copied.value = false }, 1500)
  } catch {}
}

async function fetchKiroStatus() {
  try {
    const r = await fetch('/api/kiro-proxy')
    kiroStatus.value = await r.json()
  } catch {}
}

async function configureKiro() {
  loading.value = true
  result.value = ''
  try {
    const r = await fetch('/api/kiro-proxy', { method: 'POST' })
    const data = await r.json()
    result.value = data.message
    setTimeout(fetchKiroStatus, 3000)
  } catch (e) {
    result.value = '请求失败: ' + e.message
  } finally {
    loading.value = false
  }
}

async function restoreKiro() {
  loading.value = true
  result.value = ''
  try {
    const r = await fetch('/api/kiro-proxy', { method: 'DELETE' })
    const data = await r.json()
    result.value = data.message
    setTimeout(fetchKiroStatus, 3000)
  } catch (e) {
    result.value = '请求失败: ' + e.message
  } finally {
    loading.value = false
  }
}

async function restartKiro() {
  restartLoading.value = true
  result.value = ''
  try {
    const r = await fetch('/api/kiro-restart', { method: 'POST' })
    const data = await r.json()
    result.value = data.message
  } catch (e) {
    result.value = '请求失败: ' + e.message
  } finally {
    restartLoading.value = false
  }
}

onMounted(fetchKiroStatus)
</script>

<template>
  <div class="bg-white border border-[#e5e7eb] rounded-lg p-4 hover:border-[#d1d5db] transition-colors">
    <h3 class="text-sm font-medium text-[#111827] mb-3">Kiro 代理配置</h3>

    <!-- 当前状态 -->
    <div v-if="kiroStatus" class="mb-3">
      <div class="flex items-center gap-2 text-xs">
        <span class="text-[#6b7280]">当前代理：</span>
        <span :class="kiroStatus.configured ? 'text-[#10b981]' : 'text-[#6b7280]'" class="font-mono">
          {{ kiroStatus.current_proxy || '(未设置)' }}
        </span>
        <span v-if="kiroStatus.configured"
          class="px-1.5 py-0.5 rounded text-[10px] bg-[#ecfdf5] text-[#10b981] border border-[#10b981]/20">
          已接管
        </span>
      </div>
      <div v-if="kiroStatus.original_proxy" class="text-[10px] text-[#9ca3af] mt-1">
        原始代理：{{ kiroStatus.original_proxy }}
      </div>
    </div>

    <!-- 操作按钮 -->
    <div class="flex gap-2 mb-3">
      <button
        v-if="!kiroStatus?.configured"
        class="flex-1 py-2 text-xs font-medium rounded border transition-colors disabled:opacity-50"
        :class="loading
          ? 'bg-[#f9fafb] border-[#e5e7eb] text-[#9ca3af]'
          : 'bg-[#10b981] border-[#10b981] text-white hover:bg-[#059669]'"
        :disabled="loading || restartLoading"
        @click="configureKiro"
      >
        {{ loading ? '配置中...' : '一键配置 Kiro 代理' }}
      </button>

      <button
        v-if="kiroStatus?.configured"
        class="flex-1 py-2 text-xs font-medium rounded border border-[#e5e7eb] text-[#6b7280] hover:text-[#111827] hover:border-[#d1d5db] transition-colors disabled:opacity-50"
        :disabled="loading || restartLoading"
        @click="restoreKiro"
      >
        {{ loading ? '还原中...' : '还原原始代理' }}
      </button>

      <button
        class="flex-1 py-2 text-xs font-medium rounded border border-[#e5e7eb] text-[#6b7280] hover:text-[#111827] hover:border-[#d1d5db] transition-colors disabled:opacity-50"
        :disabled="loading || restartLoading"
        @click="restartKiro"
      >
        {{ restartLoading ? '重启中...' : '重启 Kiro' }}
      </button>
    </div>

    <!-- 操作结果 -->
    <div v-if="result" class="text-xs p-2 rounded border mb-3"
      :class="result.includes('✓') ? 'bg-[#ecfdf5] border-[#10b981]/20 text-[#10b981]' : 'bg-[#fef2f2] border-[#ef4444]/20 text-[#ef4444]'">
      {{ result }}
    </div>

    <!-- 手动配置提示 -->
    <div class="border-t border-[#f3f4f6] pt-3 mt-2">
      <p class="text-[10px] text-[#9ca3af] mb-2">手动配置：</p>
      <div class="flex items-center gap-2 bg-[#f9fafb] rounded px-3 py-1.5 font-mono text-xs border border-[#e5e7eb]">
        <span class="text-[#10b981] flex-1">http://{{ proxyHost }}:{{ proxyPort }}</span>
        <button
          class="text-[#6b7280] hover:text-[#111827] transition-colors"
          @click="copyToClipboard(`http://${proxyHost}:${proxyPort}`)"
        >
          <IconCheck v-if="copied" class="text-[#10b981]" />
          <IconCopy v-else />
        </button>
      </div>
    </div>
  </div>
</template>
