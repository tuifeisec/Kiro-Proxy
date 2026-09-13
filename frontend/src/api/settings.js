import { get, post, put, del } from './http.js'

const BASE = '/api'

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
  return post(`${BASE}/kiro-proxy`)
}

export async function restoreKiroProxy() {
  return del(`${BASE}/kiro-proxy`)
}

export async function restartKiroProxy() {
  return post(`${BASE}/kiro-restart`)
}
