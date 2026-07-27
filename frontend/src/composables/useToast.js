import { ref } from 'vue'

const DEFAULT_DURATION = 2800
const MAX_VISIBLE_TOASTS = 4

const toasts = ref([])
let nextToastId = 1

function dismissToast(id) {
  toasts.value = toasts.value.filter((toast) => toast.id !== id)
}

function showToast(message, options = {}) {
  const text = String(message || '').trim()
  if (!text) return

  const id = nextToastId++
  const duration = Number(options.duration ?? DEFAULT_DURATION)
  toasts.value = [
    ...toasts.value.slice(-(MAX_VISIBLE_TOASTS - 1)),
    { id, message: text, tone: options.tone || 'success' },
  ]

  if (duration > 0) {
    window.setTimeout(() => dismissToast(id), duration)
  }
}

export function useToast() {
  return { toasts, showToast, dismissToast }
}
