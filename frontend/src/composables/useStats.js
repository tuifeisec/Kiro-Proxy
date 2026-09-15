import { ref, onUnmounted } from 'vue'
import { getStats, getDailyStats, clearStats as clearStatsApi } from '../api/stats.js'

// 模块级单例：所有调用方共享同一份状态与轮询，避免重复请求。
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
let refCount = 0
let activeIntervalMs = 0
let fetching = false

async function fetchStats() {
  if (fetching) return
  fetching = true
  loading.value = true
  try {
    const data = await getStats()
    stats.value = data
    error.value = null
  } catch (e) {
    error.value = e.message
  } finally {
    loading.value = false
    fetching = false
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

// 多使用方要求不同间隔时以最快者为准；更快者卸载后不回退到更慢间隔。
function ensureTimer(intervalMs) {
  if (timer && activeIntervalMs <= intervalMs) return
  const nextIntervalMs = timer ? Math.min(activeIntervalMs, intervalMs) : intervalMs
  if (timer) clearInterval(timer)
  activeIntervalMs = nextIntervalMs
  fetchStats()
  timer = setInterval(fetchStats, nextIntervalMs)
}

function releaseTimer() {
  refCount--
  if (refCount <= 0) {
    clearInterval(timer)
    timer = null
    refCount = 0
    activeIntervalMs = 0
  }
}

export function useStats(intervalMs = 3000) {
  refCount++
  ensureTimer(intervalMs)

  // 组件卸载时释放引用；最后一个使用者卸载才真正停止轮询。
  onUnmounted(releaseTimer)

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
      // 轮询中瞬时失败不清空已加载数据，避免界面闪"无数据"；错误由消费方横幅展示
      error.value = e.message
    } finally {
      loading.value = false
    }
  }

  return { rows, loading, error, range, load }
}
