<script setup>
import { ref, onMounted } from 'vue'
import IconCopy from '../icons/IconCopy.vue'
import IconCheck from '../icons/IconCheck.vue'

const proxyHost = '127.0.0.1'
const proxyPort = '18081'
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
  <div class="rounded-xl border border-[#e8edf5] bg-white p-3 shadow-[0_1px_3px_rgba(15,23,42,0.04)]">
    <div class="mb-2 flex items-center justify-between">
      <h3 class="text-[14px] font-semibold text-[#0f172a]">Kiro 代理配置</h3>
      <span class="inline-flex items-center gap-1.5 rounded-full bg-[#ecfdf5] px-2 py-0.5 text-[12px] font-medium text-[#059669]" v-if="kiroStatus?.configured">
        <span class="h-1.5 w-1.5 rounded-full bg-[#22c55e]"></span>
        已接管 Kiro 代理
      </span>
    </div>

    <div class="mb-2 rounded-xl border border-[#dcfce7] bg-[#f0fdf4] px-3 py-2" v-if="kiroStatus?.configured">
      <div class="mb-1.5 flex items-center gap-2 text-[12px] font-medium text-[#059669]">
        <span>♡</span>
        <span>已接管 Kiro 代理</span>
      </div>
      <div class="text-[12px] text-[#64748b]">目标代理地址</div>
      <div class="mt-1 flex items-center gap-2">
        <span class="font-mono text-[13px] text-[#0f172a]">{{ kiroStatus.current_proxy || `http://${proxyHost}:${proxyPort}` }}</span>
        <button class="text-[#64748b] hover:text-[#0f172a]" @click="copyToClipboard(kiroStatus.current_proxy || `http://${proxyHost}:${proxyPort}`, 'proxy')">
          <IconCheck v-if="copiedProxy" class="text-[#059669]" />
          <IconCopy v-else />
        </button>
      </div>
    </div>

    <div v-else class="mb-2 rounded-xl border border-[#e8edf5] bg-[#fbfdff] px-3 py-2 text-[12px] text-[#64748b]">
      当前未接管 Kiro 代理，可一键写入代理配置。
    </div>

    <div class="grid grid-cols-2 gap-2">
      <button
        v-if="!kiroStatus?.configured"
        class="col-span-2 h-8 rounded-lg bg-[#1677ff] text-[12px] font-semibold text-white shadow-sm hover:bg-[#0958d9] disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="loading || restartLoading"
        @click="configureKiro"
      >
        {{ loading ? '配置中...' : '一键配置 Kiro 代理' }}
      </button>
      <button
        v-else
        class="h-8 rounded-lg border border-[#e2e8f0] bg-white text-[12px] font-medium text-[#475569] hover:bg-[#f8fafc] disabled:opacity-60"
        :disabled="loading || restartLoading"
        @click="restoreKiro"
      >
        {{ loading ? '还原中...' : '还原原始代理' }}
      </button>
      <button
        class="h-8 rounded-lg border border-[#e2e8f0] bg-white text-[12px] font-medium text-[#475569] hover:bg-[#f8fafc] disabled:opacity-60"
        :disabled="loading || restartLoading"
        @click="restartKiro"
      >
        {{ restartLoading ? '重启中...' : '重启 Kiro' }}
      </button>
    </div>

    <div v-if="result" class="mt-2 rounded-lg border px-3 py-1.5 text-[12px]" :class="result.includes('✓') ? 'border-[#bbf7d0] bg-[#f0fdf4] text-[#059669]' : 'border-[#fecaca] bg-[#fef2f2] text-[#dc2626]'">
      {{ result }}
    </div>

    <div class="mt-2 flex items-center gap-2 rounded-lg border border-[#e8edf5] bg-[#fbfdff] px-3 py-1.5 font-mono text-[12px]">
      <span class="flex-1 truncate text-[#059669]">http://{{ proxyHost }}:{{ proxyPort }}</span>
      <button class="text-[#64748b] hover:text-[#0f172a]" @click="copyToClipboard(`http://${proxyHost}:${proxyPort}`)">
        <IconCheck v-if="copied" class="text-[#059669]" />
        <IconCopy v-else />
      </button>
    </div>
  </div>
</template>
