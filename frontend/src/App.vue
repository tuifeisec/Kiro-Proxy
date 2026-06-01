<script setup>
import Sidebar from './components/layout/Sidebar.vue'
import StatusBar from './components/layout/StatusBar.vue'
import TitleBar from './components/layout/TitleBar.vue'
import { useHeartbeat } from './composables/useHeartbeat.js'
import { startWebSocket } from './composables/useWebSocket.js'

startWebSocket()
useHeartbeat()
</script>

<template>
  <div class="flex flex-col h-screen bg-white overflow-hidden">
    <TitleBar />
    <div class="flex flex-1 min-h-0 overflow-hidden">
      <Sidebar />
      <main class="flex-1 flex flex-col overflow-hidden">
        <div class="flex-1 overflow-auto">
          <router-view />
        </div>
        <StatusBar />
      </main>
    </div>
  </div>
</template>
