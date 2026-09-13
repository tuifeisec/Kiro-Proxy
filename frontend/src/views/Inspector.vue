<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useWebSocket } from '../composables/useWebSocket.js'
import LogFilter from '../components/inspector/LogFilter.vue'
import LogList from '../components/inspector/LogList.vue'
import LogDetail from '../components/inspector/LogDetail.vue'
import IconForward from '../components/icons/IconForward.vue'
import IconCapture from '../components/icons/IconCapture.vue'
import IconSettings from '../components/icons/IconSettings.vue'
import { useToast } from '../composables/useToast.js'
import { getProxyMode, updateProxyMode } from '../api/settings.js'
import { getLogDetail, updateLogDetail } from '../api/logs.js'

const { messages, connected, trafficLogAllowed, clearMessages, refreshTrafficLogPermission } = useWebSocket()
const router = useRouter()

const selected = ref(null)
const filter = ref({ search: '', level: '', includeDetail: false })
const proxyMode = ref('forward')
const logDetailMode = ref('summary')
const logMaxBody = ref(65536)
const showSettings = ref(false)
const modeSaving = ref(false)
const logDetailSaving = ref(false)
const settingsError = ref('')
const { showToast } = useToast()

async function fetchMode() {
  try {
    const data = await getProxyMode()
    proxyMode.value = data.mode
  } catch (e) {
    settingsError.value = e.message || '读取代理模式失败'
  }
}

async function fetchLogDetail() {
  try {
    const data = await getLogDetail()
    logDetailMode.value = data.mode || 'summary'
    logMaxBody.value = data.max_body || 65536
  } catch (e) {
    settingsError.value = e.message || '读取日志模式失败'
  }
}

async function setMode(mode) {
  if (modeSaving.value || mode === proxyMode.value) return
  modeSaving.value = true
  settingsError.value = ''
  try {
    const data = await updateProxyMode(mode)
    proxyMode.value = data.mode || mode
    showToast(proxyMode.value === 'capture' ? '已切换为抓包模式' : '已切换为转发模式')
  } catch (e) {
    settingsError.value = e.message || '切换代理模式失败'
    showToast(settingsError.value, { tone: 'error' })
  } finally {
    modeSaving.value = false
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

onMounted(async () => {
  await refreshTrafficLogPermission()
  if (!trafficLogAllowed.value) {
    router.replace('/')
    return
  }
  fetchMode()
  fetchLogDetail()
})

const filteredCount = computed(() => {
  let list = messages.value
  const { search, level, includeDetail } = filter.value
  if (search) {
    const q = search.toLowerCase()
    list = list.filter(m =>
      (m.message || '').toLowerCase().includes(q) ||
      (m.category || '').toLowerCase().includes(q) ||
      (includeDetail && (m.detail || '').toLowerCase().includes(q))
    )
  }
  if (level) list = list.filter(m => m.level === level)
  return list.length
})

function handleFilter(f) {
  filter.value = f
}

function handleClear() {
  clearMessages()
  selected.value = null
}
</script>

<template>
  <div class="flex h-full flex-col overflow-hidden bg-[#f8fafc] p-3">
    <div class="mb-3 flex shrink-0 items-start justify-between gap-4">
      <div>
        <h1 class="text-lg font-semibold text-[#111827]">流量日志</h1>
        <p class="mt-1 text-xs text-[#6b7280]">实时检查 Kiro 请求经过代理后的全过程</p>
      </div>

      <div class="flex flex-wrap items-center justify-end gap-2">
        <span class="inline-flex h-8 items-center gap-1.5 ui-control bg-white px-3 text-xs text-[#374151] shadow-sm">
          <span class="h-2 w-2 rounded-full" :class="connected ? 'bg-[#10b981]' : 'bg-[#ef4444]'"></span>
          WebSocket {{ connected ? '实时' : '断开' }}
        </span>
        <span class="inline-flex h-8 items-center ui-control bg-white px-3 text-xs text-[#374151] shadow-sm">
          日志 {{ messages.length.toLocaleString() }} 条
        </span>
        <span class="inline-flex h-8 items-center gap-1.5 rounded-lg border bg-white px-3 text-xs shadow-sm"
          :class="proxyMode === 'capture'
            ? 'ui-status-warning border-[#fed7aa]'
            : 'bg-[#eff6ff] text-[#2563eb] border-[#bfdbfe]'">
          当前代理模式：{{ proxyMode === 'capture' ? '抓包模式' : '转发模式' }}
        </span>
        <button
          class="inline-flex h-8 items-center gap-1 ui-control bg-white px-3 text-xs text-[#6b7280] shadow-sm transition-colors hover:border-[#d1d5db] hover:text-[#111827]"
          @click="showSettings = !showSettings"
        >
          <IconSettings class="h-3.5 w-3.5" />
          {{ showSettings ? '收起设置' : '设置' }}
        </button>
      </div>
    </div>

    <div v-show="showSettings" class="mb-3 shrink-0 ui-card bg-white p-3">
      <p v-if="settingsError" class="mb-3 rounded-lg border border-[#fecaca] bg-[#fef2f2] px-3 py-2 text-[12px] text-[#dc2626]" role="alert">
        {{ settingsError }}
      </p>
      <div class="grid grid-cols-2 gap-6">
        <div>
          <div class="mb-2 flex items-center gap-1 text-xs font-semibold text-[#111827]">代理模式 <svg class="h-3 w-3 text-[#9ca3af]" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="提示"><circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/></svg></div>
          <div class="grid grid-cols-2 gap-2">
            <button
              class="ui-control-h inline-flex items-center justify-center gap-1.5 rounded-md border text-xs font-medium transition-colors"
              :class="proxyMode === 'forward'
                ? 'border-[#93c5fd] bg-[#eff6ff] text-[#2563eb] shadow-sm'
                : 'border-[#e5e7eb] bg-white text-[#6b7280] hover:text-[#111827]'"
              :disabled="modeSaving"
              @click="setMode('forward')"
            >
              <IconForward class="h-3.5 w-3.5" />
              转发模式
            </button>
            <button
              class="ui-control-h inline-flex items-center justify-center gap-1.5 rounded-md border text-xs font-medium transition-colors"
              :class="proxyMode === 'capture'
                ? 'border-[#fbbf24] bg-[#fffbeb] text-[#d97706] shadow-sm'
                : 'border-[#e5e7eb] bg-white text-[#6b7280] hover:text-[#111827]'"
              :disabled="modeSaving"
              @click="setMode('capture')"
            >
              <IconCapture class="h-3.5 w-3.5" />
              抓包模式
            </button>
          </div>
          <div class="mt-2 space-y-1 text-[11px] leading-4 text-[#6b7280]">
            <p><b class="text-[#2563eb]">转发模式</b>：拦截 Kiro 请求，进行格式转换，再转发到配置的 API 后端。</p>
            <p><b class="text-[#d97706]">抓包模式</b>：仅记录请求和响应原始数据，不做修改，流量直通原始服务器。</p>
          </div>
        </div>

        <div>
          <div class="mb-2 flex items-center gap-1 text-xs font-semibold text-[#111827]">日志模式 <svg class="h-3 w-3 text-[#9ca3af]" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="提示"><circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/></svg></div>
          <div class="flex items-center gap-2">
            <button
              class="ui-control-h flex-1 rounded-md border text-xs font-medium transition-colors"
              :class="logDetailMode === 'summary'
                ? 'border-[#93c5fd] bg-[#eff6ff] text-[#2563eb] shadow-sm'
                : 'border-[#e5e7eb] bg-white text-[#6b7280] hover:text-[#111827]'"
              :disabled="logDetailSaving"
              @click="setLogDetail('summary')"
            >默认摘要</button>
            <button
              class="ui-control-h flex-1 rounded-md border text-xs font-medium transition-colors"
              :class="logDetailMode === 'full'
                ? 'border-[#fecaca] bg-[#fef2f2] text-[#ef4444] shadow-sm'
                : 'border-[#e5e7eb] bg-white text-[#6b7280] hover:text-[#111827]'"
              :disabled="logDetailSaving"
              @click="setLogDetail('full')"
            >详细日志</button>
            <span class="rounded-md bg-[#fffbeb] px-2 py-1 text-[10px] text-[#92400e]">
              {{ logDetailMode === 'full' ? '完整记录 body' : `最多 ${Math.round(logMaxBody / 1024)}KB` }}
            </span>
          </div>
          <p class="mt-2 text-[11px] leading-4 text-[#6b7280]">
            默认摘要保护内存与前端性能；详细日志会完整记录 body，仅建议排查问题时临时开启。
          </p>
        </div>
      </div>
    </div>

    <div class="flex min-h-0 flex-1 gap-3 overflow-hidden">
      <div class="flex min-w-0 flex-[0_0_46%] flex-col overflow-hidden ui-card bg-white">
        <LogFilter :total="filteredCount" @filter="handleFilter" @clear="handleClear" />
        <LogList
          :messages="messages"
          :selected="selected"
          :filter="filter"
          @select="selected = $event"
        />
      </div>
      <div class="min-w-0 flex-1 overflow-hidden ui-card bg-white">
        <LogDetail :entry="selected" />
      </div>
    </div>
  </div>
</template>
