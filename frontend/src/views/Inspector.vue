<script setup>
import { ref, onMounted } from 'vue'
import { useWebSocket } from '../composables/useWebSocket.js'
import LogFilter from '../components/inspector/LogFilter.vue'
import LogList from '../components/inspector/LogList.vue'
import LogDetail from '../components/inspector/LogDetail.vue'
import IconForward from '../components/icons/IconForward.vue'
import IconCapture from '../components/icons/IconCapture.vue'
import IconSettings from '../components/icons/IconSettings.vue'
import { useToast } from '../composables/useToast.js'
import { getProxyMode, updateProxyMode, restartKiroProxy } from '../api/settings.js'
import { getLogDetail, updateLogDetail } from '../api/logs.js'

const { messages, connected, clearMessages } = useWebSocket()

const selected = ref(null)
const filter = ref({ search: '', level: '', includeDetail: false })
const proxyMode = ref('forward')
const logDetailMode = ref('summary')
const logMaxBody = ref(65536)
const showSettings = ref(false)
const modeSaving = ref(false)
const restartLoading = ref(false)
const kiroRestartRequired = ref(false)
const kiroRestartReason = ref('')
const kiroRestartError = ref('')
const modeError = ref('')
const logDetailSaving = ref(false)
const settingsError = ref('')
const { showToast } = useToast()

function assertSuccess(data, fallback) {
  if (!data || data.ok === false) throw new Error(data?.message || data?.error || fallback)
  return data
}

function applyModeStatus(data) {
  if (data.mode) proxyMode.value = data.mode
  kiroRestartRequired.value = data.kiro_restart_required === true
  kiroRestartReason.value = data.kiro_restart_reason || ''
}

async function fetchMode() {
  try {
    const data = assertSuccess(await getProxyMode(), '读取代理模式失败')
    applyModeStatus(data)
    modeError.value = ''
  } catch (e) {
    modeError.value = e.message || '读取代理模式失败'
    showToast(modeError.value, { tone: 'error' })
  }
}

async function fetchLogDetail() {
  try {
    const data = await getLogDetail()
    logDetailMode.value = data.mode || 'summary'
    logMaxBody.value = data.max_body || 65536
  } catch (e) {
    settingsError.value = e.message || '读取日志模式失败'
    // 初始加载时设置面板默认收起，面板内错误不可见，需额外 toast 提示用户。
    showToast(settingsError.value, { tone: 'error' })
  }
}

async function setMode(mode) {
  if (modeSaving.value || restartLoading.value || mode === proxyMode.value) return
  modeSaving.value = true
  modeError.value = ''
  try {
    const data = assertSuccess(await updateProxyMode(mode), '切换代理模式失败')
    applyModeStatus({ ...data, mode: data.mode || mode })
    showToast(proxyMode.value === 'capture' ? '已切换为抓包模式' : '已切换为转发模式')
  } catch (e) {
    modeError.value = e.message || '切换代理模式失败'
    showToast(modeError.value, { tone: 'error' })
  } finally {
    modeSaving.value = false
  }
}

async function restartKiroForMode() {
  if (modeSaving.value || restartLoading.value) return
  restartLoading.value = true
  kiroRestartError.value = ''
  try {
    const data = assertSuccess(await restartKiroProxy(), '重启 Kiro 失败')
    showToast(data.message || 'Kiro 重启请求已提交')
  } catch (e) {
    kiroRestartError.value = e.message || '重启 Kiro 失败'
    showToast(kiroRestartError.value, { tone: 'error' })
  } finally {
    await fetchMode()
    restartLoading.value = false
  }
}

async function setLogDetail(mode) {
  if (logDetailSaving.value || mode === logDetailMode.value) return
  logDetailSaving.value = true
  settingsError.value = ''
  try {
    const data = await updateLogDetail(mode)
    logDetailMode.value = data.mode || mode
    logMaxBody.value = data.max_body || logMaxBody.value
    showToast(logDetailMode.value === 'full' ? '已开启详细日志' : '已切换为默认摘要')
  } catch (e) {
    settingsError.value = e.message || '切换日志模式失败'
    showToast(settingsError.value, { tone: 'error' })
  } finally {
    logDetailSaving.value = false
  }
}

onMounted(() => {
  fetchMode()
  fetchLogDetail()
})

const filteredCount = ref(0)

function handleFilteredCount(count) {
  filteredCount.value = count
}

function handleFilter(f) {
  filter.value = f
}

function handleClear() {
  clearMessages()
  filteredCount.value = 0
  selected.value = null
}
</script>

<template>
  <div class="flex h-full flex-col overflow-hidden bg-canvas p-3">
    <div class="mb-3 flex shrink-0 items-start justify-between gap-4">
      <div>
        <h1 class="text-lg font-semibold text-ink">流量日志</h1>
        <p class="mt-1 text-xs text-ink-3">实时检查 Kiro 请求经过代理后的全过程</p>
      </div>

      <div class="flex flex-wrap items-center justify-end gap-2">
        <span class="inline-flex h-8 items-center gap-1.5 ui-control bg-surface px-3 text-xs text-ink-2 shadow-sm">
          <span class="h-2 w-2 rounded-full" :class="connected ? 'bg-brand' : 'bg-danger'"></span>
          WebSocket {{ connected ? '实时' : '断开' }}
        </span>
        <span class="inline-flex h-8 items-center ui-control bg-surface px-3 text-xs text-ink-2 shadow-sm">
          日志 {{ messages.length.toLocaleString() }} 条
        </span>
        <span class="inline-flex h-8 items-center gap-1.5 rounded-lg border bg-surface px-3 text-xs shadow-sm"
          :class="proxyMode === 'capture'
            ? 'ui-status-warning border-warning-tint-line'
            : 'bg-primary-tint text-primary-fg border-primary-tint-line'">
          当前代理模式：{{ proxyMode === 'capture' ? '抓包模式' : '转发模式' }}
        </span>
        <button
          class="inline-flex h-8 items-center gap-1 ui-control bg-surface px-3 text-xs text-ink-3 shadow-sm transition-colors hover:border-line-strong hover:text-ink"
          @click="showSettings = !showSettings"
        >
          <IconSettings class="h-3.5 w-3.5" />
          {{ showSettings ? '收起设置' : '设置' }}
        </button>
      </div>
    </div>

    <div v-if="kiroRestartRequired" class="mb-3 shrink-0 rounded-lg border border-warning-tint-line bg-warning-tint px-3 py-2 text-[12px]" role="status">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <div class="min-w-0 flex-1">
          <p class="font-semibold text-warning-fg">需要重启 Kiro 同步桥接设置</p>
          <p class="mt-0.5 text-warning-fg">{{ kiroRestartReason || '当前代理模式需要通过重启 Kiro 同步。' }}</p>
        </div>
        <button
          class="h-8 shrink-0 ui-primary-button px-3 text-[12px] font-semibold disabled:cursor-not-allowed disabled:opacity-60"
          :disabled="modeSaving || restartLoading"
          @click="restartKiroForMode"
        >
          {{ restartLoading ? '重启中...' : '重启 Kiro 以应用模式' }}
        </button>
      </div>
    </div>
    <p v-if="kiroRestartError" class="mb-3 shrink-0 rounded-lg border border-danger-tint-line bg-danger-tint px-3 py-2 text-[12px] text-danger" role="alert">
      {{ kiroRestartError }}
    </p>

    <div v-show="showSettings" class="mb-3 shrink-0 ui-card bg-surface p-3">
      <p v-if="modeError" class="mb-3 rounded-lg border border-danger-tint-line bg-danger-tint px-3 py-2 text-[12px] text-danger" role="alert">
        {{ modeError }}
      </p>
      <p v-if="settingsError" class="mb-3 rounded-lg border border-danger-tint-line bg-danger-tint px-3 py-2 text-[12px] text-danger" role="alert">
        {{ settingsError }}
      </p>
      <div class="grid grid-cols-2 gap-6">
        <div>
          <div class="mb-2 flex items-center gap-1 text-xs font-semibold text-ink">代理模式 <svg class="h-3 w-3 text-ink-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="提示"><circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/></svg></div>
          <div class="grid grid-cols-2 gap-2">
            <button
              class="ui-control-h inline-flex items-center justify-center gap-1.5 rounded-md border text-xs font-medium transition-colors"
              :class="proxyMode === 'forward'
                ? 'border-primary-tint-line-strong bg-primary-tint text-primary-fg shadow-sm'
                : 'border-line bg-surface text-ink-3 hover:text-ink'"
              :disabled="modeSaving || restartLoading"
              @click="setMode('forward')"
            >
              <IconForward class="h-3.5 w-3.5" />
              转发模式
            </button>
            <button
              class="ui-control-h inline-flex items-center justify-center gap-1.5 rounded-md border text-xs font-medium transition-colors"
              :class="proxyMode === 'capture'
                ? 'border-warning-tint-line bg-warning-tint text-warning-fg shadow-sm'
                : 'border-line bg-surface text-ink-3 hover:text-ink'"
              :disabled="modeSaving || restartLoading"
              @click="setMode('capture')"
            >
              <IconCapture class="h-3.5 w-3.5" />
              抓包模式
            </button>
          </div>
          <div class="mt-2 space-y-1 text-[11px] leading-4 text-ink-3">
            <p><b class="text-primary-fg">转发模式</b>：拦截 Kiro 请求，进行格式转换，再转发到配置的 API 后端。</p>
            <p><b class="text-warning">抓包模式</b>：Kiro 官方流量进入官方接口；按当前设置重启后关闭 Auto 桥接。</p>
          </div>
        </div>

        <div>
          <div class="mb-2 flex items-center gap-1 text-xs font-semibold text-ink">日志模式 <svg class="h-3 w-3 text-ink-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="提示"><circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/></svg></div>
          <div class="flex items-center gap-2">
            <button
              class="ui-control-h flex-1 rounded-md border text-xs font-medium transition-colors"
              :class="logDetailMode === 'summary'
                ? 'border-primary-tint-line-strong bg-primary-tint text-primary-fg shadow-sm'
                : 'border-line bg-surface text-ink-3 hover:text-ink'"
              :disabled="logDetailSaving"
              @click="setLogDetail('summary')"
            >默认摘要</button>
            <button
              class="ui-control-h flex-1 rounded-md border text-xs font-medium transition-colors"
              :class="logDetailMode === 'full'
                ? 'border-danger-tint-line bg-danger-tint text-danger-fg shadow-sm'
                : 'border-line bg-surface text-ink-3 hover:text-ink'"
              :disabled="logDetailSaving"
              @click="setLogDetail('full')"
            >详细日志</button>
            <span class="rounded-md bg-warning-tint px-2 py-1 text-[10px] text-warning">
              {{ logDetailMode === 'full' ? '完整记录 body' : `最多 ${Math.round(logMaxBody / 1024)}KB` }}
            </span>
          </div>
          <p class="mt-2 text-[11px] leading-4 text-ink-3">
            默认摘要保护内存与前端性能；详细日志会完整记录 body，仅建议排查问题时临时开启。
          </p>
        </div>
      </div>
    </div>

    <div class="flex min-h-0 flex-1 gap-3 overflow-hidden">
      <div class="flex min-w-0 flex-[0_0_46%] flex-col overflow-hidden ui-card bg-surface">
        <LogFilter :total="filteredCount" @filter="handleFilter" @clear="handleClear" />
        <LogList
          :messages="messages"
          :selected="selected"
          :filter="filter"
          @select="selected = $event"
          @filtered-count="handleFilteredCount"
        />
      </div>
      <div class="min-w-0 flex-1 overflow-hidden ui-card bg-surface">
        <LogDetail :entry="selected" />
      </div>
    </div>
  </div>
</template>
