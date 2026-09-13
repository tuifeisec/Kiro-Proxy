<script setup>
import { ref, watch, nextTick, onBeforeUnmount } from 'vue'

const props = defineProps({
  visible: { type: Boolean, default: false },
  title: { type: String, default: '确认操作' },
  message: { type: String, default: '' },
  confirmText: { type: String, default: '确认' },
  cancelText: { type: String, default: '取消' },
  tone: { type: String, default: 'primary' },
  busy: { type: Boolean, default: false }
})

const emit = defineEmits(['confirm', 'cancel'])

const dialogRef = ref(null)
const cancelBtnRef = ref(null)
let lastFocused = null

function trapFocus(event) {
  if (event.key !== 'Tab' || !dialogRef.value) return
  const focusables = dialogRef.value.querySelectorAll(
    'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'
  )
  if (!focusables.length) return
  const first = focusables[0]
  const last = focusables[focusables.length - 1]
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}

function onKeydown(event) {
  if (event.key === 'Escape' && !props.busy) emit('cancel')
  else trapFocus(event)
}

function cancel() {
  if (!props.busy) emit('cancel')
}

function confirm() {
  if (!props.busy) emit('confirm')
}

watch(
  () => props.visible,
  (visible) => {
    if (visible) {
      lastFocused = document.activeElement
      nextTick(() => cancelBtnRef.value?.focus())
      document.addEventListener('keydown', onKeydown)
    } else {
      document.removeEventListener('keydown', onKeydown)
      lastFocused?.focus?.()
    }
  }
)

onBeforeUnmount(() => document.removeEventListener('keydown', onKeydown))
</script>

<template>
  <Teleport to="body">
    <Transition name="motion-fade" appear>
      <div
        v-if="visible"
        class="fixed inset-0 z-50 flex items-center justify-center bg-black/30 px-4 backdrop-blur-sm"
        @click.self="cancel"
      >
        <div
          ref="dialogRef"
          role="dialog"
          aria-modal="true"
          aria-labelledby="confirm-dialog-title"
          class="motion-pop-panel w-full max-w-[420px] bg-white p-5"
          :style="{ borderRadius: 'var(--ui-radius-modal)', boxShadow: 'var(--ui-shadow-popover)', border: '1px solid var(--ui-border)' }"
        >
        <div class="flex items-start gap-3">
          <div
            class="flex h-10 w-10 shrink-0 items-center justify-center rounded-full"
            :class="tone === 'danger' ? 'bg-[#fef2f2] text-[#dc2626]' : 'bg-[#eff6ff] text-[#2563eb]'"
          >
            <svg class="h-5 w-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <path d="M12 9v4" />
              <path d="M12 17h.01" />
              <circle cx="12" cy="12" r="9" />
            </svg>
          </div>
          <div class="min-w-0 flex-1">
            <h3 id="confirm-dialog-title" class="text-[15px] font-semibold text-[#111827]">{{ title }}</h3>
            <p class="mt-2 whitespace-pre-line text-[13px] leading-5 text-[#475569]">{{ message }}</p>
          </div>
        </div>

        <div class="mt-5 flex justify-end gap-2">
          <button
            ref="cancelBtnRef"
            class="ui-control bg-white px-4 py-2 text-[13px] font-medium text-[#374151] transition hover:bg-[#f8fafc] disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="busy"
            @click="cancel"
          >
            {{ cancelText }}
          </button>
          <button
            class="ui-primary-button px-4 py-2 text-[13px] font-semibold transition disabled:cursor-not-allowed disabled:opacity-60"
            :class="tone === 'danger' ? '' : ''"
            :style="tone === 'danger' ? { background: 'var(--ui-danger)' } : {}"
            :disabled="busy"
            @click="confirm"
          >
            {{ busy ? '处理中...' : confirmText }}
          </button>
        </div>
      </div>
    </div>
    </Transition>
  </Teleport>
</template>
