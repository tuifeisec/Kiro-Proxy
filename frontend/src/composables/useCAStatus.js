import { ref, onMounted, onUnmounted } from 'vue'

const API_BASE = ''

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

  async function fetchStatus() {
    try {
      const resp = await fetch(`${API_BASE}/api/ca-status`)
      if (resp.ok) {
        caStatus.value = await resp.json()
        if (caStatus.value.installed && installResult.value && !installResult.value.includes('✓')) {
          installResult.value = ''
        }
      }
    } catch (e) {
      // 忽略网络错误
    }
  }

  async function install() {
    installing.value = true
    installResult.value = ''
    try {
      const resp = await fetch(`${API_BASE}/api/install-ca`, { method: 'POST' })
      const data = await resp.json()
      installResult.value = data.message || ''
      await fetchStatus()
      setTimeout(fetchStatus, 2000)
    } catch (e) {
      installResult.value = '请求失败: ' + e.message
    } finally {
      installing.value = false
    }
  }

  onMounted(() => {
    fetchStatus()
    timer = setInterval(fetchStatus, intervalMs)
  })

  onUnmounted(() => {
    clearInterval(timer)
  })

  return { caStatus, installing, installResult, install, refresh: fetchStatus }
}
