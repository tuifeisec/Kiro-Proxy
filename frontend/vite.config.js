import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [tailwindcss(), vue()],
  build: {
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (/[\\/]node_modules[\\/](vue|@vue|vue-router)[\\/]/.test(id)) return 'vendor'
        }
      }
    }
  },
  server: {
    proxy: {
      '/api': 'http://localhost:18082',
      '/ws': { target: 'ws://localhost:18082', ws: true }
    }
  }
})
