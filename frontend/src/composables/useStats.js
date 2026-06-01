import { ref, onUnmounted } from 'vue'
import { getStats, getDailyStats, clearStats as clearStatsApi } from '../api/stats.js'

export function useStats(intervalMs = 3000) {
  const stats = ref({
    total_requests: 0,
    total_errors: 0,
    total_tokens: 0,
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    credits: 0,
    active_connections: 0,
    uptime_seconds: 0,
  })
  const loading = ref(false)
  const error = ref(null)

  let timer = null

  async function fetchStats() {
    loading.value = true
    try {
      const data = await getStats()
      stats.value = data
      error.value = null
    } catch (e) {
      error.value = e.message
    } finally {
      loading.value = false
    }
  }

  async function clearStats() {
    loading.value = true
    try {
      stats.value = await clearStatsApi()
      error.value = null
    } catch (e) {
      error.value = e.message
      throw e
    } finally {
      loading.value = false
    }
  }

  function start() {
    fetchStats()
    timer = setInterval(fetchStats, intervalMs)
  }

  function stop() {
    clearInterval(timer)
    timer = null
  }

  start()
  onUnmounted(stop)

  return { stats, loading, error, refresh: fetchStats, clearStats }
}

// 按日期范围拉取按模型的明细统计
export function useDailyStats() {
  const rows = ref([])         // [{date, model_id, requests, input_tokens, ...}]
  const loading = ref(false)
  const error = ref(null)
  const range = ref({ start: '', end: '' })

  async function load(start, end) {
    loading.value = true
    try {
      const data = await getDailyStats(start, end)
      rows.value = data.daily || []
      range.value = { start: data.start, end: data.end }
      error.value = null
    } catch (e) {
      error.value = e.message
      rows.value = []
    } finally {
      loading.value = false
    }
  }

  return { rows, loading, error, range, load }
}
