// useHeartbeat.js
// 每 3 秒向后端发送心跳，仅用于状态感知/诊断。
// 后端进程生命周期由桌面壳窗口关闭或系统信号控制，不再根据 heartbeat 超时自动退出。

import { onMounted, onUnmounted } from 'vue'

export function useHeartbeat(intervalMs = 3000) {
  let timer = null

  function sendHeartbeat() {
    fetch('/api/heartbeat').catch(() => {})
  }

  onMounted(() => {
    sendHeartbeat()
    timer = setInterval(sendHeartbeat, intervalMs)
  })

  onUnmounted(() => {
    clearInterval(timer)
  })
}
