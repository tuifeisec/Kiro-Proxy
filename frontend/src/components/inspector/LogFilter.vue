<script setup>
import { ref, watch } from 'vue'
import IconSearch from '../icons/IconSearch.vue'

defineProps({
  total: { type: Number, default: 0 },
})

const emit = defineEmits(['filter', 'clear'])

const search = ref('')
const level = ref('')
const includeDetail = ref(false)

watch([search, level, includeDetail], () => {
  emit('filter', { search: search.value, level: level.value, includeDetail: includeDetail.value })
})
</script>

<template>
  <div class="flex shrink-0 items-center gap-2 border-b border-[#e5e7eb] bg-white px-3 py-2">
    <div class="relative min-w-0 flex-1">
      <IconSearch class="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-[#9ca3af]" />
      <input
        v-model="search"
        class="h-8 w-full rounded-lg border border-[#e5e7eb] bg-[#f9fafb] pl-7 pr-3 text-xs text-[#111827] outline-none transition-colors placeholder:text-[#9ca3af] focus:border-[#93c5fd] focus:bg-white focus:ring-2 focus:ring-[#dbeafe]"
        placeholder="搜索消息、分类..."
      />
    </div>

    <label class="inline-flex h-8 shrink-0 items-center gap-1 rounded-lg border border-[#e5e7eb] bg-white px-2 text-[11px] text-[#6b7280]">
      <input v-model="includeDetail" type="checkbox" class="h-3.5 w-3.5 accent-[#2563eb]" />
      搜详情
    </label>

    <select
      v-model="level"
      class="h-8 rounded-lg border border-[#e5e7eb] bg-white px-2.5 text-xs text-[#6b7280] outline-none transition-colors focus:border-[#93c5fd] focus:ring-2 focus:ring-[#dbeafe]"
    >
      <option value="">全部级别</option>
      <option value="info">Info</option>
      <option value="warn">Warn</option>
      <option value="error">Error</option>
      <option value="debug">Debug</option>
    </select>

    <span class="shrink-0 rounded-md bg-[#f3f4f6] px-2 py-1 text-xs text-[#6b7280]">{{ total }} 条</span>
    <button
      class="h-8 shrink-0 rounded-lg border border-[#e5e7eb] bg-white px-3 text-xs text-[#6b7280] transition-colors hover:border-[#fecaca] hover:bg-[#fef2f2] hover:text-[#ef4444]"
      @click="emit('clear')"
    >清空</button>
  </div>
</template>
