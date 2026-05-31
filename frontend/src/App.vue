<script setup>
import Sidebar from './components/layout/Sidebar.vue'
import StatusBar from './components/layout/StatusBar.vue'
import { useHeartbeat } from './composables/useHeartbeat.js'
import { startWebSocket } from './composables/useWebSocket.js'

startWebSocket()
useHeartbeat()
</script>

<template>
  <div class="flex h-screen bg-white overflow-hidden">
    <Sidebar />
    <main class="flex-1 flex flex-col overflow-hidden">
      <div class="flex-1 overflow-auto">
        <router-view />
      </div>
      <StatusBar />
    </main>
  </div>
</template>
