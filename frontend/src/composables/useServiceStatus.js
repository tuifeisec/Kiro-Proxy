import { ref, onUnmounted } from 'vue'

// 模块级单例：所有调用方共享同一份服务状态与轮询，避免重复请求。
const service = ref({
  proxy_url: 'http://127.0.0.1:18081',
  ui_port: 18082,
  protocol: 'HTTP / HTTPS MITM',
  uptime_seconds: 0,
  mode: 'forward',
})
const status = ref('checking')
const uptime = ref('')

const REQUEST_TIMEOUT_MS = 3000
let timer = null
let refCount = 0
let inFlight = false
let controller = null
let requestGeneration = 0

function formatUptime(seconds) {
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  return `${h}h ${m}m`
}

async function fetchJSON(path, generation = requestGeneration) {
  const requestController = new AbortController()
  controller = requestController
  const timeout = setTimeout(() => requestController.abort(), REQUEST_TIMEOUT_MS)
  try {
    const res = await fetch(path, { signal: requestController.signal })
    if (!res.ok) throw new Error(await res.text())
    return await res.json()
  } finally {
    clearTimeout(timeout)
    if (controller === requestController && generation === requestGeneration) controller = null
  }
}

async function checkStatusFromStats(generation = requestGeneration) {
  try {
    const payload = await fetchJSON('/api/stats', generation)
    if (generation !== requestGeneration) return
    const data = payload.data ?? payload
    status.value = 'running'
    uptime.value = formatUptime(Number(data.uptime_seconds || 0))
  } catch {
    status.value = 'checking'
    uptime.value = ''
  }
}

async function checkStatus() {
  if (document.hidden || inFlight) return
  const generation = requestGeneration
  inFlight = true
  try {
    const data = await fetchJSON('/api/service/status', generation)
    if (generation !== requestGeneration) return
    service.value = data
    // 能拿到 service/status 响应，说明 UI/API 服务正在运行；不要只因 proxy_running 字段异常就显示“已停止”。
    status.value = data.running || data.proxy_running || data.ui_running ? 'running' : 'stopped'
    uptime.value = formatUptime(Number(data.uptime_seconds || 0))
  } catch {
    await checkStatusFromStats(generation)
  } finally {
    inFlight = false
  }
}

function handleVisibilityChange() {
  if (!document.hidden) void checkStatus()
}

function ensureTimer(intervalMs) {
  if (timer) return
  void checkStatus()
  timer = setInterval(() => { void checkStatus() }, intervalMs)
  document.addEventListener('visibilitychange', handleVisibilityChange)
}

function releaseTimer() {
  refCount--
  if (refCount <= 0) {
    clearInterval(timer)
    timer = null
    refCount = 0
    document.removeEventListener('visibilitychange', handleVisibilityChange)
    requestGeneration++
    controller?.abort()
    controller = null
    inFlight = false
  }
}

export function useServiceStatus(intervalMs = 5000) {
  refCount++
  ensureTimer(intervalMs)

  // 组件卸载时释放引用；最后一个使用者卸载才真正停止轮询。
  onUnmounted(releaseTimer)

  return { service, status, uptime }
}
