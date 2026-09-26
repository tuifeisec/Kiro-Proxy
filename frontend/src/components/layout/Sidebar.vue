<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { getAppVersion } from '../../api/license.js'
import { useLicense } from '../../composables/useLicense.js'
import IconDashboard from '../icons/IconDashboard.vue'
import IconModels from '../icons/IconModels.vue'
import IconUsage from '../icons/IconUsage.vue'
import IconLogs from '../icons/IconLogs.vue'

const route = useRoute()
const { trafficLogAllowed, loadLicenseInfo } = useLicense()
const appVersion = ref('dev')

const navItems = computed(() => [
  { path: '/', label: '仪表盘', icon: IconDashboard },
  { path: '/models', label: '模型配置', icon: IconModels },
  { path: '/usage', label: '用量明细', icon: IconUsage },
  { path: '/request-usage', label: '请求统计', icon: IconLogs },
  ...(trafficLogAllowed.value ? [{ path: '/inspector', label: '流量抓包', icon: IconLogs }] : []),
])

onMounted(async () => {
  loadLicenseInfo()

  try {
    const versionInfo = await getAppVersion()
    appVersion.value = versionInfo.version || 'dev'
  } catch {
    appVersion.value = 'dev'
  }
})
</script>

<template>
  <aside class="w-14 flex flex-col items-center py-4 bg-surface border-r border-line shrink-0">
    <!-- Logo -->
    <div class="mb-6 w-8 h-8 rounded-lg bg-brand flex items-center justify-center text-white font-bold text-sm shadow-sm ring-1 ring-black/5 select-none">
      K
    </div>

    <!-- Nav items -->
    <nav class="flex flex-col gap-1 flex-1">
      <router-link
        v-for="item in navItems"
        :key="item.path"
        :to="item.path"
        class="group relative flex h-10 w-10 items-center justify-center rounded-lg text-ink-3 transition-colors hover:bg-hover-strong hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-tint-line-strong"
        :class="{ 'text-brand bg-success-tint': route.path === item.path }"
        :aria-label="item.label"
      >
        <component :is="item.icon" />
        <span class="pointer-events-none absolute left-12 z-50 whitespace-nowrap rounded-md bg-tooltip px-2 py-1 text-[11px] font-medium text-tooltip-text opacity-0 shadow-lg transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100">
          {{ item.label }}
        </span>
      </router-link>
    </nav>

    <!-- Bottom version -->
    <div class="text-ink-4 text-xs select-none">v{{ appVersion }}</div>
  </aside>
</template>
