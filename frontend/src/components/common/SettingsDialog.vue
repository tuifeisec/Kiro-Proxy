<script setup>
import { nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import ConfirmDialog from './ConfirmDialog.vue'

const props = defineProps({
  visible: { type: Boolean, default: false },
  title: { type: String, required: true },
  description: { type: String, default: '' },
  busy: { type: Boolean, default: false },
  dirty: { type: Boolean, default: false },
})
const emit = defineEmits(['close'])
const titleId = useId()
const modalRef = ref(null)
const closeButtonRef = ref(null)
const discardVisible = ref(false)
let lastFocused = null
let releaseBackground = null

function lockBackground() {
  const app = document.getElementById('app')
  const previousInert = app?.inert
  const scrollContainers = [document.documentElement, document.body, ...document.querySelectorAll('#app .scroll-stable')]
  const overflows = scrollContainers.map((element) => [element, element.style.overflow])
  if (app) app.inert = true
  for (const [element] of overflows) element.style.overflow = 'hidden'
  releaseBackground = () => {
    if (app) app.inert = previousInert
    for (const [element, overflow] of overflows) element.style.overflow = overflow
    releaseBackground = null
  }
}

function requestClose() {
  if (props.busy || !props.visible) return
  if (props.dirty) discardVisible.value = true
  else emit('close')
}

function discard() {
  if (props.busy) return
  discardVisible.value = false
  emit('close')
}

function onKeydown(event) {
  if (discardVisible.value) return
  if (event.key === 'Escape') {
    event.preventDefault()
    requestClose()
    return
  }
  if (event.key !== 'Tab' || !modalRef.value) return
  const focusables = Array.from(modalRef.value.querySelectorAll(
    'button:not(:disabled), [href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])'
  )).filter((element) => element.getClientRects().length && !element.matches(':disabled'))
  const first = focusables[0]
  const last = focusables[focusables.length - 1]
  if (!first) {
    event.preventDefault()
    modalRef.value.focus()
  } else if (!modalRef.value.contains(document.activeElement)) {
    event.preventDefault()
    ;(event.shiftKey ? last : first).focus()
  } else if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}

function cleanup() {
  document.removeEventListener('keydown', onKeydown)
  releaseBackground?.()
  const restoreFocus = lastFocused
  lastFocused = null
  // 等待嵌套确认框撤下并完成自身焦点恢复，再将焦点交还入口。
  nextTick(() => {
    if (restoreFocus?.isConnected) restoreFocus.focus?.()
  })
}

watch(() => props.visible, async (visible) => {
  if (!visible) {
    discardVisible.value = false
    cleanup()
    return
  }
  lastFocused = document.activeElement
  lockBackground()
  document.addEventListener('keydown', onKeydown)
  await nextTick()
  if (props.visible) closeButtonRef.value?.focus()
}, { immediate: true })

onBeforeUnmount(cleanup)
defineExpose({ requestClose })
</script>

<template>
  <Teleport to="body">
    <Transition name="motion-fade" appear>
      <div
        v-if="visible"
        class="fixed inset-0 z-[var(--z-modal)] flex items-center justify-center bg-black/20 px-4 py-6"
        data-settings-overlay
        @click.self="requestClose"
      >
        <div
          ref="modalRef"
          role="dialog"
          aria-modal="true"
          :aria-labelledby="titleId"
          :aria-busy="busy"
          tabindex="-1"
          class="motion-pop-panel relative flex max-h-[86vh] w-full max-w-[900px] flex-col overflow-hidden rounded-xl border border-line bg-surface shadow-xl"
        >
          <header class="flex shrink-0 items-start justify-between gap-4 border-b border-line-2 px-4 py-3">
            <div class="min-w-0">
              <h2 :id="titleId" class="text-[15px] font-semibold text-ink">{{ title }}</h2>
              <p v-if="description" class="mt-1 text-[12px] leading-5 text-ink-3">{{ description }}</p>
            </div>
            <button
              ref="closeButtonRef"
              type="button"
              aria-label="关闭设置"
              class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-ink-3 hover:bg-hover hover:text-ink disabled:cursor-not-allowed disabled:opacity-50"
              :disabled="busy"
              @click="requestClose"
            >
              <svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="m6 6 12 12M18 6 6 18" /></svg>
            </button>
          </header>
          <div class="flex min-h-0 flex-1 flex-col"><slot /></div>
        </div>
      </div>
    </Transition>
  </Teleport>

  <ConfirmDialog
    :visible="discardVisible"
    title="放弃未保存的修改？"
    message="关闭后，本次修改不会保存。"
    confirm-text="放弃修改"
    cancel-text="继续编辑"
    :busy="busy"
    @confirm="discard"
    @cancel="discardVisible = false"
  />
</template>
