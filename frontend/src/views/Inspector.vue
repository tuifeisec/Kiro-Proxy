<script setup>
import { ref, computed, onMounted } from 'vue'
import { useWebSocket } from '../composables/useWebSocket.js'
import LogFilter from '../components/inspector/LogFilter.vue'
import LogList from '../components/inspector/LogList.vue'
import LogDetail from '../components/inspector/LogDetail.vue'
import IconForward from '../components/icons/IconForward.vue'
import IconCapture from '../components/icons/IconCapture.vue'
import IconSettings from '../components/icons/IconSettings.vue'

const { messages, connected, clearMessages } = useWebSocket()

const selected = ref(null)
const filter = ref({ search: '', level: '' })
const proxyMode = ref('forward')
const logDetailMode = ref('summary')
const logMaxBody = ref(65536)
const showSettings = ref(false)

async function fetchMode() {
  try {
    const r = await fetch('/api/proxy-mode')
    const data = await r.json()
    proxyMode.value = data.mode
  } catch {}
}

async function fetchLogDetail() {
  try {
    const r = await fetch('/api/log-detail')
    const data = await r.json()
    logDetailMode.value = data.mode || 'summary'
    logMaxBody.value = data.max_body || 65536
  } catch {}
}

async function setMode(mode) {
  try {
    await fetch('/api/proxy-mode', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode })
    })
    proxyMode.value = mode
  } catch {}
}

async function setLogDetail(mode) {
  try {
    const r = await fetch('/api/log-detail', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode })
    })
    const data = await r.json()
    logDetailMode.value = data.mode || mode
    logMaxBody.value = data.max_body || logMaxBody.value
  } catch {}
}

onMounted(() => {
  fetchMode()
  fetchLogDetail()
})

const filteredCount = computed(() => {
  let list = messages.value
  const { search, level } = filter.value
  if (search) {
    const q = search.toLowerCase()
    list = list.filter(m =>
      (m.message || '').toLowerCase().includes(q) ||
      (m.category || '').toLowerCase().includes(q) ||
      (m.detail || '').toLowerCase().includes(q)
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
  <div class="flex flex-col h-full">
    <div class="flex items-center gap-3 px-4 py-3 border-b border-[#e5e7eb] shrink-0">
      <h1 class="text-sm font-semibold text-[#111827]">流量日志</h1>
      <span
        class="text-xs px-2 py-0.5 rounded-full"
        :class="connected ? 'bg-[#ecfdf5] text-[#10b981]' : 'bg-[#fef2f2] text-[#ef4444]'"
      >
        {{ connected ? '实时' : '断开' }}
      </span>
      <span class="text-xs text-[#6b7280]">{{ messages.length }} 条</span>

      <div class="flex-1"></div>

      <span class="flex items-center gap-1 text-xs px-2 py-0.5 rounded border"
        :class="proxyMode === 'capture'
          ? 'border-[#f59e0b] text-[#f59e0b] bg-[#fffbeb]'
          : 'border-[#10b981] text-[#10b981] bg-[#ecfdf5]'">
        <IconCapture v-if="proxyMode === 'capture'" class="w-3 h-3" />
        <IconForward v-else class="w-3 h-3" />
        {{ proxyMode === 'capture' ? '抓包模式' : '转发模式' }}
      </span>

      <button
        class="flex items-center gap-1 text-xs text-[#6b7280] hover:text-[#111827] px-2 py-1 border border-[#e5e7eb] rounded hover:border-[#d1d5db] transition-colors"
        @click="showSettings = !showSettings"
      >
        <IconSettings class="w-3 h-3" />
        设置
      </button>
    </div>

    <div v-if="showSettings" class="px-4 py-3 bg-[#f9fafb] border-b border-[#e5e7eb] shrink-0">
      <div class="flex flex-col gap-3">
        <div class="flex items-center gap-4 flex-wrap">
          <span class="text-xs text-[#6b7280] w-16">代理模式：</span>
          <button
            class="flex items-center gap-1 px-3 py-1.5 text-xs rounded border transition-colors"
            :class="proxyMode === 'forward'
              ? 'bg-[#ecfdf5] border-[#10b981] text-[#10b981]'
              : 'border-[#e5e7eb] text-[#6b7280] hover:text-[#111827]'"
            @click="setMode('forward')"
          >
            <IconForward class="w-3 h-3" />
            转发模式
          </button>
          <button
            class="flex items-center gap-1 px-3 py-1.5 text-xs rounded border transition-colors"
            :class="proxyMode === 'capture'
              ? 'bg-[#fffbeb] border-[#f59e0b] text-[#f59e0b]'
              : 'border-[#e5e7eb] text-[#6b7280] hover:text-[#111827]'"
            @click="setMode('capture')"
          >
            <IconCapture class="w-3 h-3" />
            抓包模式
          </button>
        </div>

        <div class="flex items-center gap-4 flex-wrap">
          <span class="text-xs text-[#6b7280] w-16">日志模式：</span>
          <button
            class="px-3 py-1.5 text-xs rounded border transition-colors"
            :class="logDetailMode === 'summary'
              ? 'bg-[#eff6ff] border-[#3b82f6] text-[#2563eb]'
              : 'border-[#e5e7eb] text-[#6b7280] hover:text-[#111827]'"
            @click="setLogDetail('summary')"
          >
            默认摘要
          </button>
          <button
            class="px-3 py-1.5 text-xs rounded border transition-colors"
            :class="logDetailMode === 'full'
              ? 'bg-[#fef2f2] border-[#ef4444] text-[#ef4444]'
              : 'border-[#e5e7eb] text-[#6b7280] hover:text-[#111827]'"
            @click="setLogDetail('full')"
          >
            详细日志
          </button>
          <span class="text-[10px] text-[#6b7280]">
            当前：{{ logDetailMode === 'full' ? '完整记录 body' : `最多记录 ${Math.round(logMaxBody / 1024)}KB detail` }}
          </span>
        </div>
      </div>

      <p class="text-[10px] text-[#6b7280] mt-2">
        <b>转发模式</b>：拦截 Kiro 请求 → 格式转换 → 转发到配置的 API 后端<br>
        <b>抓包模式</b>：仅记录请求和响应原始数据，不做任何修改，流量直通原始服务器<br>
        <b>默认摘要</b>：保护内存与前端性能，超大 body 会截断；<b>详细日志</b>：完整记录 body，仅建议排查问题时临时开启
      </p>
    </div>

    <LogFilter :total="filteredCount" @filter="handleFilter" @clear="handleClear" />

    <div class="flex flex-1 overflow-hidden">
      <div class="w-1/2 shrink-0 border-r border-[#e5e7eb] overflow-hidden flex flex-col">
        <LogList
          :messages="messages"
          :selected="selected"
          :filter="filter"
          @select="selected = $event"
        />
      </div>
      <div class="flex-1 overflow-hidden">
        <LogDetail :entry="selected" />
      </div>
    </div>
  </div>
</template>
