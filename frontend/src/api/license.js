import { get, post } from './http.js'

const BASE = '/api'

export async function getLicenseInfo() {
    try {
        return await get(`${BASE}/license-info`)
    } catch (e) {
        throw new Error(`获取授权状态失败: ${e.message}`)
    }
}

export async function getAdvertisements() {
    try {
        return await get(`${BASE}/advertisements`)
    } catch (e) {
        throw new Error(`获取广告失败: ${e.message}`)
    }
}

export async function getAppVersion() {
    try {
        return await get(`${BASE}/app-version`)
    } catch (e) {
        throw new Error(`获取版本号失败: ${e.message}`)
    }
}

export async function getUpdateInfo() {
    try {
        return await get(`${BASE}/update-info`)
    } catch (e) {
        throw new Error(`获取更新信息失败: ${e.message}`)
    }
}

export async function openExternalURL(url) {
    try {
        return await post(`${BASE}/open-external-url`, { url })
    } catch (e) {
        throw new Error(`打开下载地址失败: ${e.message}`)
    }
}
