<script setup>
import { computed, ref, nextTick, watch } from 'vue'
import IconCopy from '../icons/IconCopy.vue'
import IconExport from '../icons/IconExport.vue'

const props = defineProps({
  messages: { type: Array, default: () => [] },
  selected: { type: Object, default: null },
  filter: { type: Object, default: () => ({}) },
})

const emit = defineEmits(['select'])
const listEl = ref(null)
const checkedSet = ref(new Set())
const allChecked = ref(false)

const filtered = computed(() => {
  let list = props.messages
  const { search, level } = props.filter

  if (search) {
    const q = search.toLowerCase()
    list = list.filter(m =>
      (m.message || '').toLowerCase().includes(q) ||
      (m.category || '').toLowerCase().includes(q) ||
      (m.detail || '').toLowerCase().includes(q)
    )
  }
  if (level) {
    list = list.filter(m => m.level === level)
  }
  return list
})

watch(() => props.messages.length, () => {
  nextTick(() => {
    if (listEl.value) {
      listEl.value.scrollTop = listEl.value.scrollHeight
    }
  })
})

function toggleAll() {
  if (allChecked.value) {
    checkedSet.value = new Set()
    allChecked.value = false
  } else {
    checkedSet.value = new Set(filtered.value.map((_, i) => i))
    allChecked.value = true
  }
}

function toggleItem(idx) {
  const s = new Set(checkedSet.value)
  if (s.has(idx)) {
    s.delete(idx)
  } else {
    s.add(idx)
  }
  checkedSet.value = s
  allChecked.value = s.size === filtered.value.length
}

function cancelSelection() {
  checkedSet.value = new Set()
  allChecked.value = false
}

async function copySelected() {
  const items = filtered.value.filter((_, i) => checkedSet.value.has(i))
  await navigator.clipboard.writeText(JSON.stringify(items, null, 2))
}

function exportSelected() {
  const items = filtered.value.filter((_, i) => checkedSet.value.has(i))
  const blob = new Blob([JSON.stringify(items, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `logs-${Date.now()}.json`
  a.click()
  URL.revokeObjectURL(url)
}

function levelColor(level) {
  switch (level) {
    case 'error': return 'text-[#ef4444]'
    case 'warn': return 'text-[#f59e0b]'
    case 'info': return 'text-[#10b981]'
    default: return 'text-[#6b7280]'
  }
}

function levelIcon(level) {
  switch (level) {
    case 'error': return '✗'
    case 'warn': return '⚠'
    case 'info': return '→'
    default: return '·'
  }
}

function categoryColor(cat) {
  switch (cat) {
    case 'interceptor': return 'text-[#3b82f6]'
    case 'forwarder': return 'text-[#10b981]'
    case 'converter': return 'text-[#8b5cf6]'
    case 'router': return 'text-[#f59e0b]'
    case 'proxy': return 'text-[#6b7280]'
    default: return 'text-[#6b7280]'
  }
}
</script>

<template>
  <div class="flex flex-col h-full overflow-hidden">
    <!-- Select all -->
    <div class="flex items-center gap-2 px-3 py-1 border-b border-[#e5e7eb] bg-[#f9fafb] shrink-0">
      <input type="checkbox" :checked="allChecked" @change="toggleAll"
        class="w-3 h-3 accent-[#10b981]" />
      <span class="text-[10px] text-[#6b7280]">全选</span>
    </div>

    <div v-if="filtered.length === 0" class="flex-1 flex items-center justify-center text-[#6b7280] text-sm">
      等待日志...
    </div>

    <div v-else ref="listEl" class="flex-1 overflow-y-auto font-mono text-[11px] leading-5">
      <div
        v-for="(msg, i) in filtered"
        :key="i"
        class="flex items-start gap-1.5 px-3 py-1 border-b border-[#f3f4f6] cursor-pointer hover:bg-[#f9fafb] transition-colors"
        :class="{ 'bg-[#ecfdf5]': selected === msg }"
        @click="emit('select', msg)"
      >
        <input type="checkbox" :checked="checkedSet.has(i)" @click.stop @change="toggleItem(i)"
          class="w-3 h-3 mt-0.5 accent-[#10b981] shrink-0" />
        <span class="shrink-0 text-[#9ca3af] w-[72px]">{{ msg.time }}</span>
        <span class="shrink-0 w-3" :class="levelColor(msg.level)">{{ levelIcon(msg.level) }}</span>
        <span class="shrink-0 w-[80px] truncate" :class="categoryColor(msg.category)">{{ msg.category }}</span>
        <span class="flex-1 text-[#111827] break-all">{{ msg.message }}</span>
      </div>
    </div>

    <!-- Selection action bar -->
    <div v-if="checkedSet.size > 0"
      class="shrink-0 flex items-center gap-3 px-3 py-2 border-t border-[#e5e7eb] bg-[#f9fafb] text-xs">
      <span class="text-[#6b7280]">已选 {{ checkedSet.size }} 条</span>
      <button class="flex items-center gap-1 text-[#10b981] hover:text-[#059669]" @click="copySelected">
        <IconCopy class="w-3 h-3" /> 复制
      </button>
      <button class="flex items-center gap-1 text-[#10b981] hover:text-[#059669]" @click="exportSelected">
        <IconExport class="w-3 h-3" /> 导出 JSON
      </button>
      <button class="text-[#6b7280] hover:text-[#111827]" @click="cancelSelection">取消选择</button>
    </div>
  </div>
</template>
