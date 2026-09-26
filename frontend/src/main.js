import { createApp } from 'vue'
import { router } from './router/index.js'
import App from './App.vue'
import './style.css'
import { useTheme } from './composables/useTheme.js'

useTheme().init()

createApp(App).use(router).mount('#app')
