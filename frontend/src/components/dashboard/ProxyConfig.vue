<script setup>
import { computed, ref, onMounted, onUnmounted } from 'vue'
import IconCopy from '../icons/IconCopy.vue'
import IconCheck from '../icons/IconCheck.vue'
import { useCopy } from '../../composables/useCopy.js'
import { getProxyAccessMode, getKiroProxyStatus, configureKiroProxy, restoreKiroProxy, restartKiroProxy, updateProxyMode } from '../../api/settings.js'

// 后端代理服务监听端口：仅用于在未取到后端返回的 proxy_url 时，前端拼接展示/复制给 Kiro 的代理地址（数据，非 api 路径）
const PROXY_PORT = '18081'
const accessMode = ref({ mode: 'local', host: '127.0.0.1', proxy_url: `http://127.0.0.1:${PROXY_PORT}` })
const proxyHost = computed(() => accessMode.value.host || '127.0.0.1')
const proxyURL = computed(() => accessMode.value.proxy_url || `http://${proxyHost.value}:${PROXY_PORT}`)
const { copied, copy: copyProxyURL } = useCopy()
const { copied: copiedProxy, copy: copyCurrentProxy } = useCopy()

const kiroStatus = ref(null)
const statusLoading = ref(true)
const statusError = ref('')
const busy = ref('')
const result = ref(null)
let mounted = false
let statusRequestId = 0
let pollTimer = null
let statusRefreshTimer = null

const stageBridge = computed(() => kiroStatus.value?.stage_bridge || null)
const proxyStatusIssue = computed(() => Boolean(kiroStatus.value?.error))
const actionLabel = computed(() => {
  if (kiroStatus.value?.proxy_mode === 'capture') return '转发并重启 Kiro'
  const installation = stageBridge.value?.installation
  if (
    stageBridge.value?.restart_required
    && ['record_current', 'record_previous', 'original', 'legacy_previous'].includes(installation)
  ) return '更新并重启 Kiro'
  return '重启 Kiro'
})
const pageIsVisible = () => typeof document === 'undefined' || document.visibilityState !== 'hidden'

function assertSuccess(data, fallback) {
  if (!data || data.ok === false) {
    throw new Error(data?.message || data?.error || fallback)
  }
  return data
}

async function fetchProxyAccessMode() {
  try {
    const data = await getProxyAccessMode()
    if (data) accessMode.value = data
  } catch {
    // 地址有稳定的本地默认值；Kiro 状态读取失败则单独呈现。
  }
}

async function fetchKiroStatus() {
  if (!mounted || !pageIsVisible()) return
  const requestId = ++statusRequestId
  statusLoading.value = true
  try {
    const data = assertSuccess(await getKiroProxyStatus(), '读取 Kiro 代理状态失败')
    if (requestId !== statusRequestId) return
    kiroStatus.value = data
    statusError.value = ''
  } catch (error) {
    if (requestId !== statusRequestId) return
    kiroStatus.value = null
    statusError.value = error?.message || '读取 Kiro 代理状态失败'
  } finally {
    if (requestId === statusRequestId) statusLoading.value = false
  }
}

function scheduleStatusRefresh(delay = 900) {
  clearTimeout(statusRefreshTimer)
  statusRefreshTimer = null
  if (!mounted || !pageIsVisible()) return
  statusRefreshTimer = setTimeout(() => {
    statusRefreshTimer = null
    void fetchKiroStatus()
  }, delay)
}

async function refreshAfterAction() {
  await fetchKiroStatus()
  scheduleStatusRefresh()
}

async function runAction(name, action, fallback) {
  if (busy.value) return
  busy.value = name
  result.value = null
  try {
    const data = assertSuccess(await action(), fallback)
    result.value = { success: true, message: data.message || fallback }
  } catch (error) {
    result.value = { success: false, message: error?.message || fallback }
  } finally {
    await refreshAfterAction()
    busy.value = ''
  }
}

function configureKiro() {
  return runAction('configure', configureKiroProxy, 'Kiro 代理配置已提交')
}

function restoreKiro() {
  return runAction('restore', restoreKiroProxy, '已还原 Kiro 代理配置')
}

async function restartOrSwitchForward() {
  if (busy.value) return
  busy.value = 'restart'
  result.value = null
  try {
    if (kiroStatus.value?.proxy_mode === 'capture') {
      const modeData = assertSuccess(await updateProxyMode('forward'), '切换到转发模式失败')
      if (kiroStatus.value) {
        kiroStatus.value = { ...kiroStatus.value, proxy_mode: modeData.mode || 'forward' }
      }
    }
    const data = assertSuccess(await restartKiroProxy(), '重启 Kiro 失败')
    result.value = { success: true, message: data.message || 'Kiro 重启请求已提交' }
  } catch (error) {
    result.value = { success: false, message: error?.message || '重启 Kiro 失败' }
  } finally {
    await refreshAfterAction()
    busy.value = ''
  }
}

function handleVisibilityChange() {
  if (pageIsVisible()) void fetchKiroStatus()
  else {
    clearTimeout(statusRefreshTimer)
    statusRefreshTimer = null
  }
}

onMounted(() => {
  mounted = true
  void fetchProxyAccessMode()
  void fetchKiroStatus()
  pollTimer = setInterval(() => {
    if (pageIsVisible() && !statusLoading.value) void fetchKiroStatus()
  }, 5000)
  document.addEventListener('visibilitychange', handleVisibilityChange)
})

onUnmounted(() => {
  mounted = false
  statusRequestId += 1
  clearInterval(pollTimer)
  clearTimeout(statusRefreshTimer)
  document.removeEventListener('visibilitychange', handleVisibilityChange)
})
</script>

<template>
  <div class="ui-card min-w-0 p-3">
    <div class="mb-2 flex items-center justify-between">
      <h3 class="text-[14px] font-semibold text-ink">Kiro 代理配置</h3>
      <span v-if="kiroStatus?.configured" class="inline-flex items-center gap-1.5 rounded-full ui-status-success px-2 py-0.5 text-[12px] font-medium">
        <span class="h-1.5 w-1.5 rounded-full bg-brand"></span>
        已配置
      </span>
      <span v-else-if="kiroStatus && !kiroStatus.configured" class="inline-flex items-center gap-1.5 rounded-full ui-control px-2 py-0.5 text-[12px] font-medium text-ink-3">
        未配置
      </span>
      <span v-else class="inline-flex items-center rounded-full ui-control px-2 py-0.5 text-[12px] text-ink-3">状态未知</span>
    </div>

    <p v-if="statusError" class="mb-2 rounded-lg border border-danger-tint-line bg-danger-tint px-3 py-2 text-[12px] text-danger" role="alert">
      Kiro 状态未知：{{ statusError }}
    </p>
    <p v-else-if="statusLoading && !kiroStatus" class="mb-2 rounded-lg border border-line-2 bg-surface-2 px-3 py-2 text-[12px] text-ink-3" role="status">
      正在读取 Kiro 代理状态…
    </p>

    <div v-if="kiroStatus" class="mb-2 flex items-center justify-between gap-2 px-0.5 text-[12px] text-ink-2">
      <span class="font-medium">{{ kiroStatus.proxy_mode === 'capture' ? '抓包模式' : kiroStatus.proxy_mode === 'forward' ? '转发模式' : '模式未知' }}</span>
      <span v-if="proxyStatusIssue" class="text-danger" role="alert" :title="kiroStatus.error">配置需检查</span>
      <span v-else-if="stageBridge?.restart_required" class="text-warning" role="status">需重启 Kiro</span>
    </div>

    <template v-if="kiroStatus?.configured">
      <div class="mb-2 rounded-lg border border-success-tint-line bg-success-tint px-3 py-2">
        <div class="text-[12px] text-ink-3">代理地址</div>
        <div class="mt-1 flex items-center gap-2">
          <span class="min-w-0 flex-1 truncate font-mono text-[13px] text-ink">{{ kiroStatus.current_proxy || proxyURL }}</span>
          <button class="text-ink-3 hover:text-ink" aria-label="复制代理地址" @click="copyCurrentProxy(kiroStatus.current_proxy || proxyURL)">
            <IconCheck v-if="copiedProxy" class="text-success" />
            <IconCopy v-else />
          </button>
        </div>
      </div>
    </template>

    <div class="grid grid-cols-2 gap-2">
      <button
        v-if="kiroStatus && !kiroStatus.configured"
        class="col-span-2 h-8 ui-primary-button text-[12px] font-semibold shadow-sm disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="Boolean(busy)"
        @click="configureKiro"
      >
        {{ busy === 'configure' ? '配置中...' : '一键配置 Kiro 代理' }}
      </button>
      <button
        v-if="kiroStatus?.configured"
        class="h-8 ui-control bg-surface text-[12px] font-medium text-ink-2 hover:bg-hover disabled:opacity-60"
        :disabled="Boolean(busy)"
        @click="restoreKiro"
      >
        {{ busy === 'restore' ? '还原中...' : '还原原始代理' }}
      </button>
      <button
        v-if="kiroStatus?.configured"
        class="h-8 ui-control bg-surface text-[12px] font-medium text-ink-2 hover:bg-hover disabled:opacity-60"
        :disabled="Boolean(busy)"
        @click="restartOrSwitchForward"
      >
        {{ busy === 'restart' ? '处理中...' : actionLabel }}
      </button>
    </div>

    <div v-if="result" class="mt-2 rounded-lg border px-3 py-1.5 text-[12px]" :class="result.success ? 'border-success-tint-line bg-success-tint text-success-fg' : 'border-danger-tint-line bg-danger-tint text-danger-fg'" :role="result.success ? 'status' : 'alert'">
      {{ result.message }}
    </div>

    <div v-if="kiroStatus && !kiroStatus.configured || !kiroStatus" class="mt-2 flex items-center gap-2 rounded-lg border border-line-2 bg-surface-2 px-3 py-1.5 font-mono text-[12px]">
      <span class="flex-1 truncate text-success">{{ proxyURL }}</span>
      <button class="text-ink-3 hover:text-ink" aria-label="复制代理地址" @click="copyProxyURL(proxyURL)">
        <IconCheck v-if="copied" class="text-success" />
        <IconCopy v-else />
      </button>
    </div>
  </div>
</template>
