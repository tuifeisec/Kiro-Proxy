const BASE = '/api'

export async function getSettings() {
  const res = await fetch(`${BASE}/settings`)
  if (!res.ok) throw new Error(await res.text())
  const data = await res.json()
  return data.data ?? data
}

export async function updateSettings(settings) {
  const res = await fetch(`${BASE}/settings`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(settings),
  })
  if (!res.ok) throw new Error(await res.text())
  return true
}

export async function reloadRouter() {
  const res = await fetch(`${BASE}/reload`, { method: 'POST' })
  if (!res.ok) throw new Error(await res.text())
  return true
}
