<script setup>
import { useToast } from '../../composables/useToast.js'
import IconClose from '../icons/IconClose.vue'

const { toasts, dismissToast } = useToast()

const toneClasses = {
  success: 'border-success-tint-line bg-surface text-success-fg',
  error: 'border-danger-tint-line bg-surface text-danger-fg',
  warning: 'border-warning-tint-line bg-surface text-warning-fg',
  info: 'border-primary-tint-line bg-surface text-primary-fg',
}

const dotClasses = {
  success: 'bg-brand',
  error: 'bg-danger',
  warning: 'bg-warning',
  info: 'bg-primary',
}
</script>

<template>
  <div class="pointer-events-none fixed right-4 top-12 z-[var(--z-toast)] w-[min(360px,calc(100vw-32px))]" aria-live="polite" aria-atomic="true">
    <TransitionGroup name="toast" tag="div" class="space-y-2">
      <div
        v-for="toast in toasts"
        :key="toast.id"
        class="pointer-events-auto flex items-start gap-2.5 rounded-lg border px-3 py-2.5 shadow-popover"
        :class="toneClasses[toast.tone] || toneClasses.info"
        role="status"
      >
        <span class="mt-1.5 h-2 w-2 shrink-0 rounded-full" :class="dotClasses[toast.tone] || dotClasses.info"></span>
        <span class="min-w-0 flex-1 text-[12px] leading-5">{{ toast.message }}</span>
        <button
          type="button"
          class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-current opacity-60 transition hover:bg-black/5 hover:opacity-100"
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
/* toast 进出场：进入从右侧轻微位移 + 淡入，退出反向；
   时长/缓动引用 style.css 全局动效令牌。
   .toast-move 处理列表重排：多条堆叠时其余条目平滑让位。 */
.toast-enter-active {
  transition: opacity var(--dur-base) var(--ease-out-quart), transform var(--dur-base) var(--ease-out-quart);
}

.toast-leave-active {
  transition: opacity var(--dur-fast) var(--ease-in-quart), transform var(--dur-fast) var(--ease-in-quart);
}

.toast-move {
  transition: transform var(--dur-base) var(--ease-out-quart);
}

.toast-enter-from,
.toast-leave-to {
  opacity: 0;
  transform: translateX(16px);
}

@media (prefers-reduced-motion: reduce) {
  .toast-enter-active,
  .toast-leave-active {
    transition: none;
  }
}
</style>
