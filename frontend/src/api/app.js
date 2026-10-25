import { get } from './http.js'

const BASE = '/api'

export async function getAppVersion() {
  try {
    return await get(`${BASE}/app-version`)
  } catch (e) {
    throw new Error(`获取版本号失败: ${e.message}`)
  }
}