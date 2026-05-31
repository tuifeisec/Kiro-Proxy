<script setup>
import { ref, watch } from 'vue'

defineProps({
  total: { type: Number, default: 0 },
})

const emit = defineEmits(['filter', 'clear'])

const search = ref('')
const level = ref('')

watch([search, level], () => {
  emit('filter', { search: search.value, level: level.value })
})
</script>

<template>
  <div class="flex items-center gap-2 px-3 py-2 bg-[#f9fafb] border-b border-[#e5e7eb] shrink-0">
    <input
      v-model="search"
      class="flex-1 bg-white border border-[#e5e7eb] rounded px-2.5 py-1 text-xs text-[#111827] placeholder-[#9ca3af] focus:border-[#10b981] outline-none transition-colors"
      placeholder="搜索消息、分类、详情..."
    />

    <select
      v-model="level"
      class="bg-white border border-[#e5e7eb] rounded px-2 py-1 text-xs text-[#6b7280] focus:border-[#10b981] outline-none transition-colors"
    >
      <option value="">全部级别</option>
      <option value="info">Info</option>
      <option value="warn">Warn</option>
      <option value="error">Error</option>
      <option value="debug">Debug</option>
    </select>

    <span class="text-xs text-[#6b7280] shrink-0">{{ total }} 条</span>
    <button
      class="text-xs text-[#6b7280] hover:text-[#ef4444] transition-colors shrink-0"
      @click="emit('clear')"
    >清空</button>
  </div>
</template>
