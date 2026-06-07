const BASE = '/api'

export async function getLogsByDate(date, limit = 100) {
  const params = new URLSearchParams()
  if (date) params.set('date', date)
  if (limit) params.set('limit', String(limit))

  const res = await fetch(`${BASE}/logs?${params.toString()}`)
  if (!res.ok) throw new Error(await res.text())
  const data = await res.json()
  return data.data ?? data
}
