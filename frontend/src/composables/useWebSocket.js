// useWebSocket.js
// 全局单例 WebSocket：日志在模块级共享，切换路由不丢失
// 权限状态来自 useLicense：吊销时断开并停止重连，恢复时自动重连

import { ref, watch } from 'vue'
import { useLicense } from './useLicense.js'

const { trafficLogAllowed, loadLicenseInfo } = useLicense()

const messages = ref([])
const connected = ref(false)
const MAX_MESSAGES = 1000

const RECONNECT_BASE_DELAY_MS = 2000
const RECONNECT_MAX_DELAY_MS = 60000

let ws = null
let reconnectTimer = null
let reconnectDelay = RECONNECT_BASE_DELAY_MS
let started = false
// 主动关闭（权限吊销）标志：close 事件异步触发，用它防止 onclose 再排定重连
let intentionalClose = false

function clearReconnectTimer() {
  if (reconnectTimer) {
    clearTimeout(reconnectTimer)
    reconnectTimer = null
  }
}

// 指数退避：初始 2s，每次失败翻倍，上限 60s；连接成功（onopen）时复位
function scheduleReconnect() {
  clearReconnectTimer()
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null
    connect()
  }, reconnectDelay)
  reconnectDelay = Math.min(reconnectDelay * 2, RECONNECT_MAX_DELAY_MS)
}

function connect() {
  if (!trafficLogAllowed.value) return
  // 已有连接或已排定重连时防重入（watch 与 startWebSocket 可能同时触发）
  if (ws || reconnectTimer) return
  intentionalClose = false

  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const url = `${protocol}//${location.host}/ws`

  ws = new WebSocket(url)

  ws.onopen = () => {
    connected.value = true
    reconnectDelay = RECONNECT_BASE_DELAY_MS
  }

  ws.onmessage = (event) => {
    try {
      const msg = JSON.parse(event.data)
      messages.value.push(msg)
      if (messages.value.length > MAX_MESSAGES) {
        messages.value.splice(0, messages.value.length - MAX_MESSAGES)
      }
    } catch {
      // ignore
    }
  }

  ws.onclose = () => {
    connected.value = false
    ws = null
    if (intentionalClose) {
      intentionalClose = false
      return
    }
    if (trafficLogAllowed.value) {
      scheduleReconnect()
    }
  }

  ws.onerror = () => {
    if (ws) ws.close()
  }
}

function clearMessages() {
  messages.value = []
}

// 重新检查权限（Inspector 挂载时调用），经 useLicense 去重共享请求
async function refreshTrafficLogPermission() {
  await loadLicenseInfo()
  return trafficLogAllowed.value
}

// 权限吊销：断开现有连接、清掉待触发的重连并置标志使 onclose 不再重连；
// 权限恢复：无连接时主动接入一次，失败后同样走指数退避
watch(trafficLogAllowed, (allowed) => {
  if (!allowed) {
    intentionalClose = true
    clearReconnectTimer()
    if (ws) ws.close()
  } else if (started && !ws && !reconnectTimer) {
    connect()
  }
})

// 全局启动一次（应用初始化时调用）
export async function startWebSocket() {
  if (started) return
  started = true
  await loadLicenseInfo()
  connect()
}

export function useWebSocket() {
  return { messages, connected, trafficLogAllowed, clearMessages, refreshTrafficLogPermission }
}
