<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { getAppVersion, getLicenseInfo } from '../../api/license.js'
import IconDashboard from '../icons/IconDashboard.vue'
import IconModels from '../icons/IconModels.vue'
import IconUsage from '../icons/IconUsage.vue'
import IconLogs from '../icons/IconLogs.vue'

const route = useRoute()
const trafficLogAllowed = ref(false)
const appVersion = ref('dev')

const navItems = computed(() => [
  { path: '/', label: '仪表盘', icon: IconDashboard },
  { path: '/models', label: '模型配置', icon: IconModels },
  { path: '/usage', label: '用量明细', icon: IconUsage },
  { path: '/request-usage', label: '请求统计', icon: IconLogs },
  ...(trafficLogAllowed.value ? [{ path: '/inspector', label: '流量抓包', icon: IconLogs }] : []),
])

onMounted(async () => {
  try {
    const info = await getLicenseInfo()
    trafficLogAllowed.value = !!(info.authorized && info.traffic_logs)
  } catch {
    trafficLogAllowed.value = false
  }

  try {
    const versionInfo = await getAppVersion()
    appVersion.value = versionInfo.version || 'dev'
  } catch {
    appVersion.value = 'dev'
  }
})
</script>

<template>
  <aside class="w-14 flex flex-col items-center py-4 bg-[#f9fafb] border-r border-[#e5e7eb] shrink-0">
    <!-- Logo -->
    <div class="mb-6 w-8 h-8 rounded-lg bg-[#10b981] flex items-center justify-center text-white font-bold text-sm shadow-sm ring-1 ring-black/5 select-none">
      K
    </div>

    <!-- Nav items -->
    <nav class="flex flex-col gap-1 flex-1">
      <router-link
        v-for="item in navItems"
        :key="item.path"
        :to="item.path"
        class="w-10 h-10 flex items-center justify-center rounded-lg text-[#6b7280] hover:text-[#111827] hover:bg-[#e5e7eb] transition-colors"
        :class="{ 'text-[#10b981] bg-[#ecfdf5]': route.path === item.path }"
        :title="item.label"
      >
        <component :is="item.icon" />
      </router-link>
    </nav>

    <!-- Bottom version -->
    <div class="text-[#9ca3af] text-xs select-none">v{{ appVersion }}</div>
  </aside>
</template>
