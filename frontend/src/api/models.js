const BASE = ''

export async function getModels() {
  const r = await fetch(`${BASE}/api/models`)
  return r.json()
}

export async function createModel(data) {
  const r = await fetch(`${BASE}/api/models`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data)
  })
  return r.json()
}

export async function updateModel(id, data) {
  const r = await fetch(`${BASE}/api/models/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data)
  })
  return r.json()
}

export async function deleteModel(id) {
  const r = await fetch(`${BASE}/api/models/${id}`, { method: 'DELETE' })
  return r.json()
}

export async function testModel(modelId, model) {
  const r = await fetch(`${BASE}/api/test-model`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ model_id: modelId, model })
  })
  return r.json()
}
