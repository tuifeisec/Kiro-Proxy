// useWebSocket.js
// 全局单例 WebSocket：日志在模块级共享，切换路由不丢失

import { ref } from 'vue'
import { getLicenseInfo } from '../api/license.js'

const messages = ref([])
const connected = ref(false)
const trafficLogAllowed = ref(false)
const MAX_MESSAGES = 1000

let ws = null
let reconnectTimer = null
let started = false

function connect() {
  if (!trafficLogAllowed.value) return

  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const url = `${protocol}//${location.host}/ws`

  ws = new WebSocket(url)

  ws.onopen = () => {
    connected.value = true
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
    if (trafficLogAllowed.value) {
      reconnectTimer = setTimeout(connect, 2000)
    }
  }

  ws.onerror = () => {
    if (ws) ws.close()
  }
}

function clearMessages() {
  messages.value = []
}

async function refreshTrafficLogPermission() {
  try {
    const info = await getLicenseInfo()
    trafficLogAllowed.value = !!(info.authorized && info.traffic_logs)
  } catch {
    trafficLogAllowed.value = false
  }
  return trafficLogAllowed.value
}

// 全局启动一次（应用初始化时调用）
export async function startWebSocket() {
  if (started) return
  started = true
  if (await refreshTrafficLogPermission()) {
    connect()
  }
}

export function useWebSocket() {
  return { messages, connected, trafficLogAllowed, clearMessages, refreshTrafficLogPermission }
}
