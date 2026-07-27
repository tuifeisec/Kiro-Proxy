<script setup>
import { useToast } from '../../composables/useToast.js'
import IconClose from '../icons/IconClose.vue'

const { toasts, dismissToast } = useToast()

const toneClasses = {
  success: 'border-[#bbf7d0] bg-white text-[#166534]',
  error: 'border-[#fecaca] bg-white text-[#b91c1c]',
  warning: 'border-[#fed7aa] bg-white text-[#b45309]',
  info: 'border-[#bfdbfe] bg-white text-[#1d4ed8]',
}

const dotClasses = {
  success: 'bg-[#22c55e]',
  error: 'bg-[#ef4444]',
  warning: 'bg-[#f59e0b]',
  info: 'bg-[#3b82f6]',
}
</script>

<template>
  <div class="pointer-events-none fixed right-4 top-12 z-[100] w-[min(360px,calc(100vw-32px))]" aria-live="polite" aria-atomic="true">
    <TransitionGroup name="toast" tag="div" class="space-y-2">
      <div
        v-for="toast in toasts"
        :key="toast.id"
        class="pointer-events-auto flex items-start gap-2.5 rounded-lg border px-3 py-2.5 shadow-[0_8px_24px_rgba(15,23,42,0.12)]"
        :class="toneClasses[toast.tone] || toneClasses.info"
        role="status"
      >
        <span class="mt-1.5 h-2 w-2 shrink-0 rounded-full" :class="dotClasses[toast.tone] || dotClasses.info"></span>
        <span class="min-w-0 flex-1 text-[12px] leading-5">{{ toast.message }}</span>
        <button
          type="button"
          class="flex h-6 w-6 shrink-0 items-center justify-center rounded-md text-current opacity-60 transition hover:bg-black/5 hover:opacity-100"
          aria-label="关闭提示"
          @click="dismissToast(toast.id)"
        >
          <IconClose class="h-3.5 w-3.5" />
        </button>
      </div>
    </TransitionGroup>
  </div>
</template>

<style scoped>
.toast-enter-active,
.toast-leave-active {
  transition: opacity 160ms ease, transform 160ms ease;
}

.toast-enter-from,
.toast-leave-to {
  opacity: 0;
  transform: translateY(-6px);
}
</style>
