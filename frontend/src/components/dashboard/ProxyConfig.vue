<script setup>
import { computed, ref, onMounted, onUnmounted } from 'vue'
import IconCopy from '../icons/IconCopy.vue'
import IconCheck from '../icons/IconCheck.vue'
import { useCopy } from '../../composables/useCopy.js'
import { getProxyAccessMode, getKiroProxyStatus, configureKiroProxy, restoreKiroProxy, restartKiroProxy } from '../../api/settings.js'

// 后端代理服务监听端口：仅用于在未取到后端返回的 proxy_url 时，前端拼接展示/复制给 Kiro 的代理地址（数据，非 api 路径）
const PROXY_PORT = '18081'
const accessMode = ref({ mode: 'local', host: '127.0.0.1', proxy_url: `http://127.0.0.1:${PROXY_PORT}` })
const proxyHost = computed(() => accessMode.value.host || '127.0.0.1')
const proxyURL = computed(() => accessMode.value.proxy_url || `http://${proxyHost.value}:${PROXY_PORT}`)
// 两处复制按钮各自独立维护"已复制"反馈状态
const { copied, copy: copyProxyURL } = useCopy()
const { copied: copiedProxy, copy: copyCurrentProxy } = useCopy()
const kiroStatus = ref(null)
const loading = ref(false)
const restartLoading = ref(false)
const result = ref('')
let kiroStatusTimer = null

async function fetchProxyAccessMode() {
  try {
    const data = await getProxyAccessMode()
    if (data) accessMode.value = data
  } catch {
    // 诊断类请求，失败时保持默认值，不影响主流程
  }
}

async function fetchKiroStatus() {
  try {
    kiroStatus.value = await getKiroProxyStatus()
  } catch {
    // 诊断类请求，失败时保持默认值，不影响主流程
  }
}

async function configureKiro() {
  loading.value = true
  result.value = ''
  try {
    const data = await configureKiroProxy()
    result.value = data.message
    clearTimeout(kiroStatusTimer)
    kiroStatusTimer = setTimeout(fetchKiroStatus, 3000)
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
    const data = await restoreKiroProxy()
    result.value = data.message
    clearTimeout(kiroStatusTimer)
    kiroStatusTimer = setTimeout(fetchKiroStatus, 3000)
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
    const data = await restartKiroProxy()
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

onUnmounted(() => clearTimeout(kiroStatusTimer))
</script>

<template>
  <div class="ui-card min-w-0 p-3">
    <div class="mb-2 flex items-center justify-between">
      <h3 class="text-[14px] font-semibold text-ink">Kiro 代理配置</h3>
      <span class="inline-flex items-center gap-1.5 rounded-full ui-status-success px-2 py-0.5 text-[12px] font-medium" v-if="kiroStatus?.configured">
        <span class="h-1.5 w-1.5 rounded-full bg-brand"></span>
        已接管 Kiro 代理
      </span>
    </div>

    <div class="mb-2 rounded-lg border border-success-tint-line bg-success-tint px-3 py-2" v-if="kiroStatus?.configured">
      <div class="mb-1.5 flex items-center gap-2 text-[12px] font-medium text-success">
        <svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg>
        <span>已接管 Kiro 代理</span>
      </div>
      <div class="text-[12px] text-ink-3">目标代理地址</div>
      <div class="mt-1 flex items-center gap-2">
        <span class="min-w-0 flex-1 truncate font-mono text-[13px] text-ink">{{ kiroStatus.current_proxy || proxyURL }}</span>
        <button class="text-ink-3 hover:text-ink" aria-label="复制代理地址" @click="copyCurrentProxy(kiroStatus.current_proxy || proxyURL)">
          <IconCheck v-if="copiedProxy" class="text-success" />
          <IconCopy v-else />
        </button>
      </div>
    </div>

    <div v-else class="mb-2 rounded-lg border border-line-2 bg-surface-2 px-3 py-2 text-[12px] text-ink-3">
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
        class="h-8 ui-control bg-surface text-[12px] font-medium text-ink-2 hover:bg-hover disabled:opacity-60"
        :disabled="loading || restartLoading"
        @click="restoreKiro"
      >
        {{ loading ? '还原中...' : '还原原始代理' }}
      </button>
      <button
        class="h-8 ui-control bg-surface text-[12px] font-medium text-ink-2 hover:bg-hover disabled:opacity-60"
        :disabled="loading || restartLoading"
        @click="restartKiro"
      >
        {{ restartLoading ? '重启中...' : '重启 Kiro' }}
      </button>
    </div>

    <div v-if="result" class="mt-2 rounded-lg border px-3 py-1.5 text-[12px]" :class="result.includes('✓') ? 'border-success-tint-line bg-success-tint text-success-fg' : 'border-danger-tint-line bg-danger-tint text-danger-fg'">
      {{ result }}
    </div>

    <div v-if="!kiroStatus?.configured" class="mt-2 flex items-center gap-2 rounded-lg border border-line-2 bg-surface-2 px-3 py-1.5 font-mono text-[12px]">
      <span class="flex-1 truncate text-success">{{ proxyURL }}</span>
      <button class="text-ink-3 hover:text-ink" aria-label="复制代理地址" @click="copyProxyURL(proxyURL)">
        <IconCheck v-if="copied" class="text-success" />
        <IconCopy v-else />
      </button>
    </div>
  </div>
</template>
