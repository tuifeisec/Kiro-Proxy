<script setup>
defineProps({
  label: { type: String, required: true },
  value: { type: [String, Number], default: '—' },
  sub: { type: String, default: '' },
  color: { type: String, default: '#10b981' },
  icon: { type: [Object, null], default: null },
})
</script>

<template>
  <div class="bg-white border border-[#e5e7eb] rounded-lg p-4 flex flex-col gap-1 hover:border-[#d1d5db] transition-colors">
    <div class="flex items-center justify-between">
      <span class="text-xs text-[#6b7280] uppercase tracking-wide">{{ label }}</span>
      <component v-if="icon" :is="icon" class="text-[#6b7280]" />
    </div>
    <div class="text-2xl font-semibold" :style="{ color }">{{ value }}</div>
    <div v-if="sub" class="text-xs text-[#6b7280]">{{ sub }}</div>
  </div>
</template>
