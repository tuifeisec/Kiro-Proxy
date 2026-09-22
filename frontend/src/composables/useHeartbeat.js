// useHeartbeat.js
// 每 3 秒向后端发送心跳，仅用于状态感知/诊断。
// 后端进程生命周期由桌面壳窗口关闭或系统信号控制，不再根据 heartbeat 超时自动退出。

import { onMounted, onUnmounted } from 'vue'

const REQUEST_TIMEOUT_MS = 3000

export function useHeartbeat(intervalMs = 3000) {
  let timer = null
  let controller = null
  let inFlight = false

  async function sendHeartbeat() {
    if (document.hidden || inFlight) return
    inFlight = true
    controller = new AbortController()
    const timeout = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS)
    try {
      await fetch('/api/heartbeat', { signal: controller.signal })
    } catch {
      // 心跳仅用于诊断，网络错误不影响页面状态。
    } finally {
      clearTimeout(timeout)
      controller = null
      inFlight = false
    }
  }

  function handleVisibilityChange() {
    if (!document.hidden) void sendHeartbeat()
  }

  onMounted(() => {
    void sendHeartbeat()
    timer = setInterval(() => { void sendHeartbeat() }, intervalMs)
    document.addEventListener('visibilitychange', handleVisibilityChange)
  })

  onUnmounted(() => {
    clearInterval(timer)
    timer = null
    document.removeEventListener('visibilitychange', handleVisibilityChange)
    controller?.abort()
  })
}
