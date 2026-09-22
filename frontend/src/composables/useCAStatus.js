import { ref, onMounted, onUnmounted } from 'vue'

const API_BASE = ''
const REQUEST_TIMEOUT_MS = 3000

export function useCAStatus(intervalMs = 5000) {
  const caStatus = ref({
    cert_exists: false,
    installed: false,
    subject: '',
    not_after: '',
    error: '',
  })
  const installing = ref(false)
  const installResult = ref('')

  let timer = null
  let controller = null
  let installController = null
  let inFlight = false
  let disposed = false
  let refreshTimer = null

  async function fetchStatus() {
    if (inFlight || disposed || document.hidden) return
    inFlight = true
    controller = new AbortController()
    const timeout = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS)
    try {
      const resp = await fetch(`${API_BASE}/api/ca-status`, { signal: controller.signal })
      if (resp.ok) {
        caStatus.value = await resp.json()
        if (caStatus.value.installed && installResult.value && !installResult.value.includes('✓')) {
          installResult.value = ''
        }
      }
    } catch {
      // 忽略网络错误
    } finally {
      clearTimeout(timeout)
      controller = null
      inFlight = false
    }
  }

  async function install() {
    if (installing.value || disposed) return
    installing.value = true
    installResult.value = ''
    installController = new AbortController()
    const timeout = setTimeout(() => installController?.abort(), REQUEST_TIMEOUT_MS)
    try {
      const resp = await fetch(`${API_BASE}/api/install-ca`, {
        method: 'POST',
        signal: installController.signal,
      })
      const data = await resp.json()
      installResult.value = data.message || ''
      await fetchStatus()
      refreshTimer = setTimeout(() => { void fetchStatus() }, 2000)
    } catch (e) {
      installResult.value = e.name === 'AbortError' ? '请求超时' : '请求失败: ' + e.message
    } finally {
      clearTimeout(timeout)
      installController = null
      installing.value = false
    }
  }

  function handleVisibilityChange() {
    if (!document.hidden) void fetchStatus()
  }

  onMounted(() => {
    void fetchStatus()
    timer = setInterval(() => { void fetchStatus() }, intervalMs)
    document.addEventListener('visibilitychange', handleVisibilityChange)
  })

  onUnmounted(() => {
    disposed = true
    clearInterval(timer)
    timer = null
    clearTimeout(refreshTimer)
    refreshTimer = null
    document.removeEventListener('visibilitychange', handleVisibilityChange)
    controller?.abort()
    installController?.abort()
  })

  return { caStatus, installing, installResult, install, refresh: fetchStatus }
}
