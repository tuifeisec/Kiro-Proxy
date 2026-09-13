<script setup>
import Sidebar from './components/layout/Sidebar.vue'
import StatusBar from './components/layout/StatusBar.vue'
import TitleBar from './components/layout/TitleBar.vue'
import ConfirmDialog from './components/common/ConfirmDialog.vue'
import AdvertisementList from './components/common/AdvertisementList.vue'
import ToastHost from './components/common/ToastHost.vue'
import { computed, onMounted, ref } from 'vue'
import { getUpdateInfo, openExternalURL } from './api/license.js'
import { useHeartbeat } from './composables/useHeartbeat.js'
import { useLicense } from './composables/useLicense.js'
import { startWebSocket } from './composables/useWebSocket.js'

const { licenseInfo, loadLicenseInfo } = useLicense()
const updateDialogVisible = ref(false)
const updateInfo = ref(null)

const branding = computed(() => licenseInfo.value?.branding || {})
const appName = computed(() => branding.value.app_name || 'Kiro Proxy')
const unauthorizedText = computed(() => branding.value.unauthorized_text || '请加入群获取免费授权后使用')
const machineCode = computed(() => licenseInfo.value?.machine_code || '-')
const isAuthorized = computed(() => licenseInfo.value?.authorized === true)
const licenseLoaded = computed(() => licenseInfo.value !== null)
const updateMessage = computed(() => {
  const info = updateInfo.value || {}
  return `当前有更新的版本，建议更新，点击确定跳转更新地址\n\n当前版本：${info.current_version || '-'}\n最新版本：${info.latest_version || '-'}`
})

async function checkUpdate() {
  try {
    const info = await getUpdateInfo()
    if (info.has_update && info.download_url) {
      updateInfo.value = info
      updateDialogVisible.value = true
    }
  } catch {
    updateInfo.value = null
  }
}

async function confirmUpdate() {
  const url = updateInfo.value?.download_url
  updateDialogVisible.value = false
  if (!url) return
  try {
    await openExternalURL(url)
  } catch {
    window.open(url, '_blank', 'noopener,noreferrer')
  }
}

startWebSocket()
useHeartbeat()
onMounted(() => {
  loadLicenseInfo()
  checkUpdate()
})
</script>

<template>
  <div class="flex h-screen flex-col overflow-hidden bg-white">
    <ToastHost />
    <TitleBar />

    <div v-if="licenseLoaded && !isAuthorized" class="relative flex flex-1 flex-col bg-[#fbfbfc]">
      <div class="absolute left-0 right-0 top-6 px-6">
        <div class="mx-auto max-w-[1320px]">
          <AdvertisementList />
        </div>
      </div>
      <div class="flex flex-1 items-center justify-center px-6 pb-16">
        <div class="text-center">
          <h1 class="text-[34px] font-semibold tracking-tight text-[#111827]">{{ appName }}</h1>
          <p class="mt-3 text-[14px] leading-6 text-[#6b7280]">{{ unauthorizedText }}</p>
        </div>
      </div>
      <div class="absolute bottom-6 left-0 right-0 text-center text-[12px] text-[#9ca3af]">
        设备码：<span class="font-mono text-[#64748b]">{{ machineCode }}</span>
      </div>
    </div>

    <div v-else-if="isAuthorized" class="flex min-h-0 flex-1 overflow-hidden">
      <Sidebar />
      <main class="flex flex-1 flex-col overflow-hidden">
        <div class="flex-1 overflow-auto">
          <router-view />
        </div>
        <StatusBar />
      </main>
    </div>

    <ConfirmDialog
      :visible="updateDialogVisible"
      title="发现新版本"
      :message="updateMessage"
      confirm-text="确定"
      cancel-text="取消"
      @confirm="confirmUpdate"
      @cancel="updateDialogVisible = false"
    />
  </div>
</template>
