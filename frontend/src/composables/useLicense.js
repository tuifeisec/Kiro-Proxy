// useLicense.js
// 模块级单例：license 状态全局共享，启动时多个消费方并发加载只发一次请求

import { ref, computed } from 'vue'
import { getLicenseInfo } from '../api/license.js'

const licenseInfo = ref(null)
const loading = ref(false)

// 授权且允许流量日志时才开放 WS 与流量抓包入口
const trafficLogAllowed = computed(() => {
  const info = licenseInfo.value
  return !!(info && info.authorized && info.traffic_logs)
})

let inflight = null

// 并发调用共享同一个 in-flight Promise；完成后再次调用会重新请求。
// 永不 reject：请求失败时降级为未授权并保留默认品牌信息。
async function loadLicenseInfo() {
  if (inflight) return inflight
  loading.value = true
  inflight = (async () => {
    try {
      const info = await getLicenseInfo()
      licenseInfo.value = info
      return info
    } catch (error) {
      licenseInfo.value = {
        authorized: false,
        error: error.message,
        branding: {
          app_name: 'Kiro Proxy',
          unauthorized_text: '请加入群获取免费授权后使用',
        },
      }
      return licenseInfo.value
    } finally {
      loading.value = false
      inflight = null
    }
  })()
  return inflight
}

// 强制刷新：等当前 in-flight 请求结束后重新请求，避免旧响应覆盖新数据
async function refreshLicenseInfo() {
  if (inflight) await inflight
  return loadLicenseInfo()
}

export function useLicense() {
  return { licenseInfo, loading, trafficLogAllowed, loadLicenseInfo, refreshLicenseInfo }
}
