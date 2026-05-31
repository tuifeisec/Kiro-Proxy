// useWebSocket.js
// 全局单例 WebSocket：日志在模块级共享，切换路由不丢失

import { ref } from 'vue'

const messages = ref([])
const connected = ref(false)
const MAX_MESSAGES = 1000

let ws = null
let reconnectTimer = null
let started = false

function connect() {
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
    reconnectTimer = setTimeout(connect, 2000)
  }

  ws.onerror = () => {
    if (ws) ws.close()
  }
}

function clearMessages() {
  messages.value = []
}

// 全局启动一次（应用初始化时调用）
export function startWebSocket() {
  if (started) return
  started = true
  connect()
}

export function useWebSocket() {
  return { messages, connected, clearMessages }
}
