<script setup>
import Sidebar from './components/layout/Sidebar.vue'
import StatusBar from './components/layout/StatusBar.vue'
import TitleBar from './components/layout/TitleBar.vue'
import ToastHost from './components/common/ToastHost.vue'
import { useHeartbeat } from './composables/useHeartbeat.js'
import { startWebSocket } from './composables/useWebSocket.js'

startWebSocket()
useHeartbeat()
</script>

<template>
  <div class="flex h-screen flex-col overflow-hidden bg-canvas">
    <ToastHost />
    <TitleBar />

    <div class="flex min-h-0 flex-1 overflow-hidden">
      <Sidebar />
      <main class="flex flex-1 flex-col overflow-hidden">
        <div class="scroll-stable flex-1 overflow-auto">
          <router-view v-slot="{ Component }">
            <Transition name="motion-view" mode="out-in">
              <component :is="Component" :key="$route.fullPath" />
            </Transition>
          </router-view>
        </div>
        <StatusBar />
      </main>
    </div>
  </div>
</template>