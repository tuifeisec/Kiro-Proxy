<template>
  <div class="flex flex-col h-screen bg-[#1e1e2e] text-[#cdd6f4] font-mono text-sm select-none">

    <!-- ── 顶栏 ── -->
    <header class="flex items-center gap-3 px-4 py-2 bg-[#181825] border-b border-[#313244] shrink-0">
      <div class="flex items-center gap-2">
        <span class="text-lg">🔍</span>
        <span class="font-bold text-[#cba6f7]">Kiro Proxy</span>
        <span class="text-xs text-[#6c7086]">流量观察</span>
      </div>

      <!-- WS 连接状态 -->
      <div class="flex items-center gap-1.5 ml-2">
        <span :class="wsConnected ? 'bg-[#a6e3a1]' : 'bg-[#f38ba8]'"
              class="w-2 h-2 rounded-full transition-colors"></span>
        <span class="text-xs" :class="wsConnected ? 'text-[#a6e3a1]' : 'text-[#f38ba8]'">
          {{ wsConnected ? '已连接' : '未连接' }}
        </span>
      </div>

      <!-- CA 证书状态 + 安装按钮 -->
      <div class="flex items-center gap-2 ml-2 pl-3 border-l border-[#313244]">
        <!-- 未检测到证书文件 -->
        <template v-if="!caStatus.cert_exists">
          <span class="text-xs text-[#f38ba8]">⚠ CA 未生成</span>
        </template>
        <!-- 证书存在但未安装 -->
        <template v-else-if="!caStatus.installed">
          <span class="text-xs text-[#fab387]">🔒 CA 未安装</span>
          <button
            @click="installCA"
            :disabled="caInstalling"
            class="flex items-center gap-1 px-2.5 py-1 text-xs rounded border transition-colors"
            :class="caInstalling
              ? 'bg-[#313244] border-[#45475a] text-[#6c7086] cursor-not-allowed'
              : 'bg-[#2d1f3d] border-[#cba6f7] text-[#cba6f7] hover:bg-[#3d2a52] cursor-pointer'"
          >
            <span v-if="caInstalling" class="animate-spin">⟳</span>
            <span v-else>🛡</span>
            {{ caInstalling ? '安装中...' : '安装 CA 证书' }}
          </button>
        </template>
        <!-- 已安装 -->
        <template v-else>
          <span class="text-xs text-[#a6e3a1]">✓ CA 已信任</span>
          <span class="text-[10px] text-[#6c7086]">{{ caStatus.not_after }} 到期</span>
        </template>
      </div>

      <div class="flex-1"></div>

      <!-- 过滤器 -->
      <div class="flex items-center gap-2">
        <input
          v-model="filterText"
          placeholder="过滤 URL / 方法..."
          class="bg-[#313244] border border-[#45475a] rounded px-2 py-1 text-xs w-52 outline-none focus:border-[#cba6f7] placeholder-[#6c7086]"
        />
        <select v-model="filterType"
          class="bg-[#313244] border border-[#45475a] rounded px-2 py-1 text-xs outline-none focus:border-[#cba6f7]">
          <option value="">全部</option>
          <option value="request">请求</option>
          <option value="response">响应</option>
        </select>
      </div>

      <!-- 操作按钮 -->
      <button @click="clearLogs"
        class="px-3 py-1 text-xs rounded bg-[#313244] hover:bg-[#45475a] border border-[#45475a] transition-colors">
        清空
      </button>
      <button @click="exportLogs"
        class="px-3 py-1 text-xs rounded bg-[#313244] hover:bg-[#45475a] border border-[#45475a] transition-colors">
        导出
      </button>
      <label class="flex items-center gap-1.5 text-xs cursor-pointer">
        <input type="checkbox" v-model="autoScroll" class="accent-[#cba6f7]" />
        自动滚动
      </label>
    </header>

    <!-- ── 主体：左侧列表 + 右侧详情 ── -->
    <div class="flex flex-1 overflow-hidden">

      <!-- 左侧：请求列表 -->
      <div class="w-[420px] shrink-0 flex flex-col border-r border-[#313244]">
        <!-- 统计栏 -->
        <div class="flex gap-4 px-3 py-1.5 bg-[#181825] border-b border-[#313244] text-xs text-[#6c7086]">
          <span>共 <b class="text-[#cdd6f4]">{{ logs.length }}</b> 条</span>
          <span>请求 <b class="text-[#89b4fa]">{{ reqCount }}</b></span>
          <span>响应 <b class="text-[#a6e3a1]">{{ resCount }}</b></span>
        </div>

        <!-- 列表 -->
        <div ref="listEl" class="flex-1 overflow-y-auto">
          <div
            v-for="entry in filteredLogs"
            :key="entry.id + entry.type"
            @click="select(entry)"
            :class="[
              'flex items-start gap-2 px-3 py-2 border-b border-[#313244] cursor-pointer transition-colors',
              selected?.id === entry.id && selected?.type === entry.type
                ? 'bg-[#313244]'
                : 'hover:bg-[#262637]'
            ]"
          >
            <!-- 类型标签 -->
            <span :class="entry.type === 'request'
              ? 'bg-[#1e3a5f] text-[#89b4fa] border-[#89b4fa]'
              : statusColor(entry.status_code)"
              class="shrink-0 text-[10px] font-bold px-1.5 py-0.5 rounded border mt-0.5 w-[52px] text-center">
              {{ entry.type === 'request' ? entry.method : entry.status_code }}
            </span>

            <!-- URL + 时间 -->
            <div class="flex-1 min-w-0">
              <div class="truncate text-xs text-[#cdd6f4]">{{ shortUrl(entry.url) }}</div>
              <div class="flex gap-2 mt-0.5 text-[10px] text-[#6c7086]">
                <span>{{ entry.time.slice(11) }}</span>
                <span v-if="entry.elapsed_ms"
                  :class="entry.elapsed_ms > 2000 ? 'text-[#f38ba8]' : entry.elapsed_ms > 500 ? 'text-[#fab387]' : 'text-[#a6e3a1]'">
                  {{ entry.elapsed_ms }}ms
                </span>
                <span>{{ formatSize(entry.body_size) }}</span>
              </div>
            </div>
          </div>

          <div v-if="filteredLogs.length === 0"
            class="flex flex-col items-center justify-center h-40 text-[#6c7086] text-xs gap-2">
            <span class="text-2xl">📭</span>
            <span>{{ logs.length === 0 ? '等待 Kiro 流量...' : '无匹配结果' }}</span>
          </div>
        </div>
      </div>

      <!-- 右侧：详情面板 -->
      <div class="flex-1 flex flex-col overflow-hidden">
        <template v-if="selected">
          <!-- 详情顶栏 -->
          <div class="flex items-center gap-3 px-4 py-2 bg-[#181825] border-b border-[#313244] shrink-0">
            <span :class="selected.type === 'request'
              ? 'bg-[#1e3a5f] text-[#89b4fa] border-[#89b4fa]'
              : statusColor(selected.status_code)"
              class="text-xs font-bold px-2 py-0.5 rounded border">
              {{ selected.type === 'request' ? selected.method : selected.status_code }}
            </span>
            <span class="text-xs text-[#cdd6f4] flex-1 truncate">{{ selected.url }}</span>
            <span class="text-xs text-[#6c7086]">{{ selected.time }}</span>
            <span v-if="selected.elapsed_ms" class="text-xs text-[#6c7086]">{{ selected.elapsed_ms }}ms</span>
          </div>

          <!-- Tab 切换 -->
          <div class="flex gap-0 border-b border-[#313244] bg-[#181825] shrink-0">
            <button v-for="tab in tabs" :key="tab.key"
              @click="activeTab = tab.key"
              :class="activeTab === tab.key
                ? 'border-b-2 border-[#cba6f7] text-[#cba6f7]'
                : 'text-[#6c7086] hover:text-[#cdd6f4]'"
              class="px-4 py-2 text-xs transition-colors">
              {{ tab.label }}
            </button>
          </div>

          <!-- Tab 内容 -->
          <div class="flex-1 overflow-y-auto p-4">

            <!-- Headers -->
            <template v-if="activeTab === 'headers'">
              <div v-for="(vals, key) in selected.headers" :key="key"
                class="flex gap-2 py-1 border-b border-[#313244] text-xs">
                <span class="text-[#89b4fa] shrink-0 w-52 truncate">{{ key }}</span>
                <span class="text-[#cdd6f4] break-all">{{ vals.join(', ') }}</span>
              </div>
            </template>

            <!-- Body -->
            <template v-if="activeTab === 'body'">
              <div class="flex justify-end mb-2 gap-2">
                <button @click="copyBody"
                  class="px-3 py-1 text-xs rounded bg-[#313244] hover:bg-[#45475a] border border-[#45475a] transition-colors">
                  📋 复制
                </button>
                <button @click="bodyRaw = !bodyRaw"
                  class="px-3 py-1 text-xs rounded bg-[#313244] hover:bg-[#45475a] border border-[#45475a] transition-colors">
                  {{ bodyRaw ? '格式化' : '原始' }}
                </button>
              </div>
              <pre v-if="bodyRaw"
                class="text-xs text-[#cdd6f4] whitespace-pre-wrap break-all leading-5">{{ selected.body }}</pre>
              <pre v-else
                class="text-xs leading-5 whitespace-pre-wrap break-all"
                v-html="highlightedBody"></pre>
              <div v-if="!selected.body" class="text-[#6c7086] text-xs">(空 body)</div>
            </template>

            <!-- 完整 JSON（方便一键复制整条记录） -->
            <template v-if="activeTab === 'raw'">
              <div class="flex justify-end mb-2">
                <button @click="copyRaw"
                  class="px-3 py-1 text-xs rounded bg-[#313244] hover:bg-[#45475a] border border-[#45475a] transition-colors">
                  📋 复制完整记录
                </button>
              </div>
              <pre class="text-xs text-[#cdd6f4] whitespace-pre-wrap break-all leading-5">{{ JSON.stringify(selected, null, 2) }}</pre>
            </template>

          </div>
        </template>

        <!-- 未选中时的占位 -->
        <div v-else class="flex flex-col items-center justify-center h-full text-[#6c7086] gap-2">
          <span class="text-4xl">👈</span>
          <span class="text-sm">点击左侧条目查看详情</span>
        </div>
      </div>

    </div>

    <!-- ── 底栏 ── -->
    <footer class="flex items-center gap-4 px-4 py-1 bg-[#181825] border-t border-[#313244] text-[10px] text-[#6c7086] shrink-0">
      <span>代理端口: <b class="text-[#cdd6f4]">18081</b></span>
      <span>Web UI: <b class="text-[#cdd6f4]">18082</b></span>
      <span class="flex-1"></span>
      <!-- CA 安装结果提示 -->
      <span v-if="caMsg"
        :class="caMsg.includes('已安装') ? 'text-[#a6e3a1]' : 'text-[#f38ba8]'"
        class="transition-opacity">
        {{ caMsg }}
      </span>
      <span v-else-if="copied" class="text-[#a6e3a1]">✓ 已复制</span>
    </footer>

  </div>
</template>

<script setup>
import { ref, computed, watch, nextTick, onMounted, onUnmounted } from 'vue'

// ── 状态 ──────────────────────────────────────────────────────────
const logs       = ref([])
const selected   = ref(null)
const filterText = ref('')
const filterType = ref('')
const autoScroll = ref(true)
const activeTab  = ref('body')
const bodyRaw    = ref(false)
const copied     = ref(false)
const wsConnected = ref(false)
const listEl     = ref(null)

// CA 状态
const caStatus    = ref({ cert_exists: false, installed: false })
const caInstalling = ref(false)
const caMsg       = ref('')

const tabs = [
  { key: 'body',    label: 'Body' },
  { key: 'headers', label: 'Headers' },
  { key: 'raw',     label: 'Raw JSON' },
]

// ── WebSocket ─────────────────────────────────────────────────────
let ws = null
let reconnectTimer = null

function connect() {
  const wsUrl = `ws://${location.hostname}:18082/ws`
  ws = new WebSocket(wsUrl)

  ws.onopen = () => {
    wsConnected.value = true
    clearTimeout(reconnectTimer)
  }

  ws.onmessage = (e) => {
    try {
      const entry = JSON.parse(e.data)
      logs.value.push(entry)
      if (autoScroll.value) {
        nextTick(() => {
          if (listEl.value) listEl.value.scrollTop = listEl.value.scrollHeight
        })
      }
    } catch {}
  }

  ws.onclose = () => {
    wsConnected.value = false
    reconnectTimer = setTimeout(connect, 2000)
  }

  ws.onerror = () => {
    ws.close()
  }
}

onMounted(connect)
onUnmounted(() => {
  clearTimeout(reconnectTimer)
  ws?.close()
})

// ── CA 证书 ───────────────────────────────────────────────────────
async function fetchCAStatus() {
  try {
    const r = await fetch(`http://${location.hostname}:18082/api/ca-status`)
    caStatus.value = await r.json()
  } catch {}
}

async function installCA() {
  caInstalling.value = true
  caMsg.value = ''
  try {
    const r = await fetch(`http://${location.hostname}:18082/api/install-ca`, { method: 'POST' })
    const data = await r.json()
    caMsg.value = data.message
    if (data.ok) {
      // 安装成功后刷新状态
      setTimeout(fetchCAStatus, 1000)
    } else {
      // 显示错误 3 秒
      setTimeout(() => { caMsg.value = '' }, 4000)
    }
  } catch (e) {
    caMsg.value = '请求失败: ' + e.message
    setTimeout(() => { caMsg.value = '' }, 4000)
  } finally {
    caInstalling.value = false
  }
}

// 页面加载时查一次，之后每 5 秒轮询
onMounted(() => {
  fetchCAStatus()
  setInterval(fetchCAStatus, 5000)
})

// ── 过滤 ──────────────────────────────────────────────────────────
const filteredLogs = computed(() => {
  return logs.value.filter(e => {
    if (filterType.value && e.type !== filterType.value) return false
    if (filterText.value) {
      const q = filterText.value.toLowerCase()
      return e.url.toLowerCase().includes(q) ||
             (e.method || '').toLowerCase().includes(q)
    }
    return true
  })
})

const reqCount = computed(() => logs.value.filter(e => e.type === 'request').length)
const resCount = computed(() => logs.value.filter(e => e.type === 'response').length)

// ── 选中 ──────────────────────────────────────────────────────────
function select(entry) {
  selected.value = entry
  activeTab.value = 'body'
  bodyRaw.value = false
}

// 切换选中时重置 tab
watch(selected, () => {
  activeTab.value = 'body'
  bodyRaw.value = false
})

// ── Body 高亮 ─────────────────────────────────────────────────────
const highlightedBody = computed(() => {
  if (!selected.value?.body) return ''
  try {
    const obj = JSON.parse(selected.value.body)
    return syntaxHighlight(JSON.stringify(obj, null, 2))
  } catch {
    return escapeHtml(selected.value.body)
  }
})

function syntaxHighlight(json) {
  return escapeHtml(json).replace(
    /("(\\u[a-zA-Z0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d*)?(?:[eE][+\-]?\d+)?)/g,
    (match) => {
      let cls = 'json-num'
      if (/^"/.test(match)) {
        cls = /:$/.test(match) ? 'json-key' : 'json-str'
      } else if (/true|false/.test(match)) {
        cls = 'json-bool'
      } else if (/null/.test(match)) {
        cls = 'json-null'
      }
      return `<span class="${cls}">${match}</span>`
    }
  )
}

function escapeHtml(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

// ── 工具函数 ──────────────────────────────────────────────────────
function shortUrl(url) {
  try {
    const u = new URL(url)
    return u.hostname + u.pathname
  } catch {
    return url
  }
}

function formatSize(bytes) {
  if (!bytes) return '0 B'
  if (bytes < 1024) return bytes + ' B'
  return (bytes / 1024).toFixed(1) + ' KB'
}

function statusColor(code) {
  if (!code) return 'bg-[#313244] text-[#6c7086] border-[#45475a]'
  if (code < 300) return 'bg-[#1e3d2f] text-[#a6e3a1] border-[#a6e3a1]'
  if (code < 400) return 'bg-[#3d3520] text-[#f9e2af] border-[#f9e2af]'
  return 'bg-[#3d1e1e] text-[#f38ba8] border-[#f38ba8]'
}

function showCopied() {
  copied.value = true
  setTimeout(() => { copied.value = false }, 1500)
}

async function copyBody() {
  if (!selected.value?.body) return
  await navigator.clipboard.writeText(selected.value.body)
  showCopied()
}

async function copyRaw() {
  if (!selected.value) return
  await navigator.clipboard.writeText(JSON.stringify(selected.value, null, 2))
  showCopied()
}

function clearLogs() {
  logs.value = []
  selected.value = null
}

function exportLogs() {
  const data = logs.value.map(e => JSON.stringify(e)).join('\n')
  const blob = new Blob([data], { type: 'application/jsonl' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `kiro-traffic-${Date.now()}.jsonl`
  a.click()
}
</script>
