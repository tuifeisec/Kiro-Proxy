const BASE = ''

async function parseJSONResponse(r) {
  const data = await r.json().catch(() => ({}))
  if (!r.ok || data?.error) {
    throw new Error(data?.error || `HTTP ${r.status}`)
  }
  return data
}

export async function getModels() {
  const r = await fetch(`${BASE}/api/models`)
  return parseJSONResponse(r)
}

export async function createModel(data) {
  const r = await fetch(`${BASE}/api/models`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data)
  })
  return parseJSONResponse(r)
}

export async function updateModel(id, data) {
  const r = await fetch(`${BASE}/api/models/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data)
  })
  return parseJSONResponse(r)
}

export async function deleteModel(id) {
  const r = await fetch(`${BASE}/api/models/${id}`, { method: 'DELETE' })
  return parseJSONResponse(r)
}

export async function testModel(modelId, model) {
  const r = await fetch(`${BASE}/api/test-model`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ model_id: modelId, model })
  })
  return parseJSONResponse(r)
}

export async function refreshCodexModel(id) {
  const r = await fetch(`${BASE}/api/refresh-codex-model`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ model_id: id })
  })
  return parseJSONResponse(r)
}

export async function startCodexOAuth() {
  const r = await fetch(`${BASE}/api/codex-oauth/start`, { method: 'POST' })
  return parseJSONResponse(r)
}

export async function exchangeCodexOAuth(callbackUrl, proxyUrl) {
  const r = await fetch(`${BASE}/api/codex-oauth/exchange`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ callback_url: callbackUrl, proxy_url: proxyUrl || '' })
  })
  return parseJSONResponse(r)
}
