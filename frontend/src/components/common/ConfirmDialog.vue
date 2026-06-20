<script setup>
const props = defineProps({
  visible: { type: Boolean, default: false },
  title: { type: String, default: '确认操作' },
  message: { type: String, default: '' },
  confirmText: { type: String, default: '确认' },
  cancelText: { type: String, default: '取消' },
  tone: { type: String, default: 'primary' }
})

const emit = defineEmits(['confirm', 'cancel'])
</script>

<template>
  <Teleport to="body">
    <div
      v-if="visible"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/30 px-4 backdrop-blur-sm"
      @click.self="emit('cancel')"
    >
      <div class="w-full max-w-[420px] rounded-2xl border border-[#e5e7eb] bg-white p-5 shadow-2xl">
        <div class="flex items-start gap-3">
          <div
            class="flex h-10 w-10 shrink-0 items-center justify-center rounded-full"
            :class="tone === 'danger' ? 'bg-[#fef2f2] text-[#dc2626]' : 'bg-[#eff6ff] text-[#2563eb]'"
          >
            <svg class="h-5 w-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <path d="M12 9v4" />
              <path d="M12 17h.01" />
              <circle cx="12" cy="12" r="9" />
            </svg>
          </div>
          <div class="min-w-0 flex-1">
            <h3 class="text-[15px] font-semibold text-[#111827]">{{ title }}</h3>
            <p class="mt-2 whitespace-pre-line text-[13px] leading-5 text-[#475569]">{{ message }}</p>
          </div>
        </div>

        <div class="mt-5 flex justify-end gap-2">
          <button
            class="rounded-lg border border-[#e5e7eb] bg-white px-4 py-2 text-[13px] font-medium text-[#374151] transition hover:bg-[#f8fafc]"
            @click="emit('cancel')"
          >
            {{ cancelText }}
          </button>
          <button
            class="rounded-lg px-4 py-2 text-[13px] font-semibold text-white transition"
            :class="tone === 'danger' ? 'bg-[#dc2626] hover:bg-[#b91c1c]' : 'bg-[#1677ff] hover:bg-[#0958d9]'"
            @click="emit('confirm')"
          >
            {{ confirmText }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
