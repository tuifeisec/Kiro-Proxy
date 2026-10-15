import { get, post, put, request, jsonInit } from './http.js'

const BASE = '/api'
const KIRO_LIFECYCLE_TIMEOUT_MS = 180000

export async function getSettings() {
  const data = await get(`${BASE}/settings`)
  return data.data ?? data
}

export async function updateSettings(settings) {
  await put(`${BASE}/settings`, settings)
  return true
}

export async function reloadRouter() {
  await post(`${BASE}/reload`)
  return true
}

export async function getProxyAccessMode() {
  return get(`${BASE}/proxy-access-mode`)
}

export async function updateProxyAccessMode(mode) {
  return put(`${BASE}/proxy-access-mode`, { mode })
}

export async function getProxyMode() {
  return get(`${BASE}/proxy-mode`)
}

export async function updateProxyMode(mode) {
  return put(`${BASE}/proxy-mode`, { mode })
}

export async function getKiroProxyStatus() {
  return get(`${BASE}/kiro-proxy`)
}

export async function configureKiroProxy() {
  // 客户端等待预算为 180 秒；超时不代表后端流程已终止，因此不自动重试。
  return request(`${BASE}/kiro-proxy`, { ...jsonInit('POST'), timeout: KIRO_LIFECYCLE_TIMEOUT_MS })
}

export async function restoreKiroProxy() {
  // 超时不代表后端还原流程已终止，因此不自动重试。
  return request(`${BASE}/kiro-proxy`, { method: 'DELETE', timeout: KIRO_LIFECYCLE_TIMEOUT_MS })
}

export async function restartKiroProxy() {
  // 超时不代表后端重启流程已终止，因此不自动重试。
  return request(`${BASE}/kiro-restart`, { ...jsonInit('POST'), timeout: KIRO_LIFECYCLE_TIMEOUT_MS })
}
