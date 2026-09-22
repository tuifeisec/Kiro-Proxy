// useWebSocket.js
// 全局单例 WebSocket：日志在模块级共享，切换路由不丢失
// 权限状态来自 useLicense：吊销时断开并停止重连，恢复时自动重连

import { ref, watch } from 'vue'
import { useLicense } from './useLicense.js'

const { trafficLogAllowed, loadLicenseInfo } = useLicense()

const messages = ref([])
const connected = ref(false)
const MAX_MESSAGES = 500
const MAX_PENDING_MESSAGES = 500
const MAX_TOTAL_DETAIL_BYTES = 8 * 1024 * 1024
const MAX_MESSAGE_DETAIL_BYTES = 512 * 1024
let messageSequence = 0

function detailBytes(msg) {
  return typeof msg?.detail === 'string' ? msg.detail.length : 0
}

function normalizeMessage(msg) {
  if (!msg || typeof msg !== 'object') return msg
  const detail = msg.detail
  const normalized = msg.id ? msg : { ...msg, id: `ws-${Date.now()}-${messageSequence++}` }
  if (typeof detail !== 'string' || detail.length <= MAX_MESSAGE_DETAIL_BYTES) return normalized

  return {
    ...normalized,
    detail: `${detail.slice(0, MAX_MESSAGE_DETAIL_BYTES)}\n\n... 已截断，原始详情过大。`,
  }
}

function trimMessages(list, maxMessages = MAX_MESSAGES) {
  let totalDetailBytes = list.reduce((total, msg) => total + detailBytes(msg), 0)
  while (list.length > maxMessages || totalDetailBytes > MAX_TOTAL_DETAIL_BYTES) {
    const removed = list.shift()
    if (!removed) break
    totalDetailBytes -= detailBytes(removed)
  }
}

// rAF 合帧：到达的消息先进入普通缓冲，每帧最多 flush 一次到响应式 messages，
// 同时限制条数和 detail 总大小，避免转发模式长时间运行后 WebView 内存膨胀。
let pendingMessages = []
let flushHandle = 0

function flushMessages() {
  flushHandle = 0
  if (!pendingMessages.length) return
  const batch = pendingMessages
  pendingMessages = []
  messages.value.push(...batch)
  trimMessages(messages.value)
}

function enqueueMessage(msg) {
  pendingMessages.push(normalizeMessage(msg))
  trimMessages(pendingMessages, MAX_PENDING_MESSAGES)
  if (!flushHandle) {
    flushHandle = requestAnimationFrame(flushMessages)
  }
}

function cancelPendingFlush() {
  if (flushHandle) {
    cancelAnimationFrame(flushHandle)
    flushHandle = 0
  }
  pendingMessages = []
}

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

  const socket = new WebSocket(url)
  ws = socket

  socket.onopen = () => {
    if (ws !== socket) return
    connected.value = true
    reconnectDelay = RECONNECT_BASE_DELAY_MS
  }

  socket.onmessage = (event) => {
    if (ws !== socket) return
    try {
      enqueueMessage(JSON.parse(event.data))
    } catch {
      // ignore
    }
  }

  socket.onclose = () => {
    if (ws !== socket) return
    connected.value = false
    ws = null
    if (intentionalClose) {
      intentionalClose = false
      if (trafficLogAllowed.value) scheduleReconnect()
      return
    }
    if (trafficLogAllowed.value) {
      scheduleReconnect()
    }
  }

  socket.onerror = () => {
    if (ws === socket) socket.close()
  }
}

function clearMessages() {
  cancelPendingFlush()
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
