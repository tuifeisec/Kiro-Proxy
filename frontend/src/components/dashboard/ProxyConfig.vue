<script setup>
import { computed, ref, onMounted } from 'vue'
import IconCopy from '../icons/IconCopy.vue'
import IconCheck from '../icons/IconCheck.vue'

const proxyPort = '18081'
const accessMode = ref({ mode: 'local', host: '127.0.0.1', proxy_url: 'http://127.0.0.1:18081' })
const proxyHost = computed(() => accessMode.value.host || '127.0.0.1')
const proxyURL = computed(() => accessMode.value.proxy_url || `http://${proxyHost.value}:${proxyPort}`)
const copied = ref(false)
const copiedProxy = ref(false)
const kiroStatus = ref(null)
const loading = ref(false)
const restartLoading = ref(false)
const result = ref('')

async function copyToClipboard(text, type = 'manual') {
  try {
    await navigator.clipboard.writeText(text)
    if (type === 'proxy') {
      copiedProxy.value = true
      setTimeout(() => { copiedProxy.value = false }, 1500)
    } else {
      copied.value = true
      setTimeout(() => { copied.value = false }, 1500)
    }
  } catch {
    // 剪贴板 API 不可用或权限被拒时静默失败，不影响主流程
  }
}

async function fetchProxyAccessMode() {
  try {
    const r = await fetch('/api/proxy-access-mode')
    if (!r.ok) return
    accessMode.value = await r.json()
  } catch {
    // 诊断类请求，失败时保持默认值，不影响主流程
  }
}

async function fetchKiroStatus() {
  try {
    const r = await fetch('/api/kiro-proxy')
    kiroStatus.value = await r.json()
  } catch {
    // 诊断类请求，失败时保持默认值，不影响主流程
  }
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

onMounted(() => {
  fetchProxyAccessMode()
  fetchKiroStatus()
})
</script>

<template>
  <div class="ui-card min-w-0 p-3">
    <div class="mb-2 flex items-center justify-between">
      <h3 class="text-[14px] font-semibold text-[#0f172a]">Kiro 代理配置</h3>
      <span class="inline-flex items-center gap-1.5 rounded-full ui-status-success px-2 py-0.5 text-[12px] font-medium" v-if="kiroStatus?.configured">
        <span class="h-1.5 w-1.5 rounded-full bg-[#22c55e]"></span>
        已接管 Kiro 代理
      </span>
    </div>

    <div class="mb-2 rounded-lg border border-[#dcfce7] bg-[#f0fdf4] px-3 py-2" v-if="kiroStatus?.configured">
      <div class="mb-1.5 flex items-center gap-2 text-[12px] font-medium text-[#059669]">
        <svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg>
        <span>已接管 Kiro 代理</span>
      </div>
      <div class="text-[12px] text-[#64748b]">目标代理地址</div>
      <div class="mt-1 flex items-center gap-2">
        <span class="min-w-0 flex-1 truncate font-mono text-[13px] text-[#0f172a]">{{ kiroStatus.current_proxy || proxyURL }}</span>
        <button class="text-[#64748b] hover:text-[#0f172a]" aria-label="复制代理地址" @click="copyToClipboard(kiroStatus.current_proxy || proxyURL, 'proxy')">
          <IconCheck v-if="copiedProxy" class="text-[#059669]" />
          <IconCopy v-else />
        </button>
      </div>
    </div>

    <div v-else class="mb-2 rounded-lg border border-[#e8edf5] bg-[#fbfdff] px-3 py-2 text-[12px] text-[#64748b]">
      当前未接管 Kiro 代理，可一键写入代理配置。
    </div>

    <div class="grid grid-cols-2 gap-2">
      <button
        v-if="!kiroStatus?.configured"
        class="col-span-2 h-8 ui-primary-button text-[12px] font-semibold shadow-sm disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="loading || restartLoading"
        @click="configureKiro"
      >
        {{ loading ? '配置中...' : '一键配置 Kiro 代理' }}
      </button>
      <button
        v-else
        class="h-8 ui-control bg-white text-[12px] font-medium text-[#475569] hover:bg-[#f8fafc] disabled:opacity-60"
        :disabled="loading || restartLoading"
        @click="restoreKiro"
      >
        {{ loading ? '还原中...' : '还原原始代理' }}
      </button>
      <button
        class="h-8 ui-control bg-white text-[12px] font-medium text-[#475569] hover:bg-[#f8fafc] disabled:opacity-60"
        :disabled="loading || restartLoading"
        @click="restartKiro"
      >
        {{ restartLoading ? '重启中...' : '重启 Kiro' }}
      </button>
    </div>

    <div v-if="result" class="mt-2 rounded-lg border px-3 py-1.5 text-[12px]" :class="result.includes('✓') ? 'border-[#bbf7d0] bg-[#f0fdf4] text-[#059669]' : 'border-[#fecaca] bg-[#fef2f2] text-[#dc2626]'">
      {{ result }}
    </div>

    <div v-if="!kiroStatus?.configured" class="mt-2 flex items-center gap-2 rounded-lg border border-[#e8edf5] bg-[#fbfdff] px-3 py-1.5 font-mono text-[12px]">
      <span class="flex-1 truncate text-[#059669]">{{ proxyURL }}</span>
      <button class="text-[#64748b] hover:text-[#0f172a]" aria-label="复制代理地址" @click="copyToClipboard(proxyURL)">
        <IconCheck v-if="copied" class="text-[#059669]" />
        <IconCopy v-else />
      </button>
    </div>
  </div>
</template>
