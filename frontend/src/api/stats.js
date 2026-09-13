import { get, del } from './http.js'

const BASE = '/api'

export async function getStats() {
  const data = await get(`${BASE}/stats`)
  return data.data ?? data
}

export async function clearStats() {
  const data = await del(`${BASE}/stats`)
  return data.data ?? data
}

// 查询按日 + 按模型的统计明细
// start/end 格式：YYYY-MM-DD（可选，默认最近 7 天）
export async function getDailyStats(start, end) {
  const params = new URLSearchParams({ type: 'daily' })
  if (start) params.set('start', start)
  if (end) params.set('end', end)
  const data = await get(`${BASE}/stats?${params.toString()}`)
  return data.data ?? data
}
