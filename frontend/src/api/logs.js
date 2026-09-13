import { get, put } from './http.js'

const BASE = '/api'

export async function getLogsByDate(date, limit = 100) {
  const params = new URLSearchParams()
  if (date) params.set('date', date)
  if (limit) params.set('limit', String(limit))
  const data = await get(`${BASE}/logs?${params.toString()}`)
  return data.data ?? data
}

export async function getLogDetail() {
  return get(`${BASE}/log-detail`)
}

export async function updateLogDetail(mode) {
  return put(`${BASE}/log-detail`, { mode })
}
