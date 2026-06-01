const BASE = '/api'

export async function getStats() {
  const res = await fetch(`${BASE}/stats`)
  if (!res.ok) throw new Error(await res.text())
  const data = await res.json()
  return data.data ?? data
}

export async function clearStats() {
  const res = await fetch(`${BASE}/stats`, { method: 'DELETE' })
  if (!res.ok) throw new Error(await res.text())
  const data = await res.json()
  return data.data ?? data
}

// 查询按日 + 按模型的统计明细
// start/end 格式：YYYY-MM-DD（可选，默认最近 7 天）
export async function getDailyStats(start, end) {
  const params = new URLSearchParams({ type: 'daily' })
  if (start) params.set('start', start)
  if (end) params.set('end', end)
  const res = await fetch(`${BASE}/stats?${params.toString()}`)
  if (!res.ok) throw new Error(await res.text())
  const data = await res.json()
  return data.data ?? data
}
