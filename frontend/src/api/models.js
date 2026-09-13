import { get, post, put, del, request } from './http.js'

const BASE = '/api'

export async function getModels() {
  return get(`${BASE}/models`)
}

export async function createModel(data) {
  return post(`${BASE}/models`, data)
}

export async function updateModel(id, data) {
  return put(`${BASE}/models/${id}`, data)
}

export async function deleteModel(id) {
  return del(`${BASE}/models/${id}`)
}

export async function getModelGroups() {
  return get(`${BASE}/model-groups`)
}

export async function getModelGroupStatus() {
  return get(`${BASE}/model-groups/status`)
}

export async function createModelGroup(data) {
  return post(`${BASE}/model-groups`, data)
}

export async function updateModelGroup(id, data) {
  return put(`${BASE}/model-groups/${id}`, data)
}

export async function deleteModelGroup(id) {
  return del(`${BASE}/model-groups/${id}`)
}

export async function testModel(modelId, model) {
  // 后端上游测速超时为 30s（api/test_model.go），前端超时需长于它才能拿到后端的测试失败结果
  return request(`${BASE}/test-model`, { ...jsonInit('POST', { model_id: modelId, model }), timeout: 60000 })
}

export async function fetchUpstreamModels(data) {
  return post(`${BASE}/upstream-models`, data)
}

export async function refreshCodexModel(id) {
  return post(`${BASE}/refresh-codex-model`, { model_id: id })
}

export async function startCodexOAuth() {
  return post(`${BASE}/codex-oauth/start`)
}

export async function exchangeCodexOAuth(callbackUrl, proxyUrl) {
  return post(`${BASE}/codex-oauth/exchange`, { callback_url: callbackUrl, proxy_url: proxyUrl || '' })
}
