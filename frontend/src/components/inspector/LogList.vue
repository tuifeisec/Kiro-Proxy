<script setup>
import { computed, ref, nextTick, watch } from 'vue'
import IconCopy from '../icons/IconCopy.vue'
import IconExport from '../icons/IconExport.vue'
import { useCopy } from '../../composables/useCopy.js'
import { useToast } from '../../composables/useToast.js'

const { showToast } = useToast()
const { copy } = useCopy()

const props = defineProps({
  messages: { type: Array, default: () => [] },
  selected: { type: Object, default: null },
  filter: { type: Object, default: () => ({}) },
})

const emit = defineEmits(['select'])
const listEl = ref(null)
const checkedSet = ref(new Set())
const allChecked = ref(false)
const visibleLimit = ref(800)

const filtered = computed(() => {
  let list = props.messages
  const { search, level, includeDetail } = props.filter

  if (search) {
    const q = search.toLowerCase()
    list = list.filter(m =>
      (m.message || '').toLowerCase().includes(q) ||
      (m.category || '').toLowerCase().includes(q) ||
      (includeDetail && (m.detail || '').toLowerCase().includes(q))
    )
  }
  if (level) {
    list = list.filter(m => m.level === level)
  }
  return list
})

const visible = computed(() => filtered.value.slice(Math.max(0, filtered.value.length - visibleLimit.value)))
const hiddenCount = computed(() => Math.max(0, filtered.value.length - visible.value.length))

watch(() => props.messages.length, () => {
  nextTick(() => {
    if (listEl.value) {
      listEl.value.scrollTop = listEl.value.scrollHeight
    }
  })
})

// 监听 visible 引用而非 length：日志达到裁剪上限后 length 恒定，无法感知新消息；
// visible 每次重算都产生新数组引用，新消息到达或筛选变化均触发重置
watch(visible, () => {
  checkedSet.value = new Set()
  allChecked.value = false
})

function toggleAll() {
  if (allChecked.value) {
    checkedSet.value = new Set()
    allChecked.value = false
  } else {
    checkedSet.value = new Set(visible.value)
    allChecked.value = true
  }
}

function toggleItem(msg) {
  const s = new Set(checkedSet.value)
  if (s.has(msg)) {
    s.delete(msg)
  } else {
    s.add(msg)
  }
  checkedSet.value = s
  allChecked.value = s.size === visible.value.length
}

function cancelSelection() {
  checkedSet.value = new Set()
  allChecked.value = false
}

async function copySelected() {
  const items = visible.value.filter(m => checkedSet.value.has(m))
  const ok = await copy(JSON.stringify(items, null, 2))
  if (!ok) {
    showToast('复制失败：无法访问剪贴板', { tone: 'error' })
  }
}

function exportSelected() {
  const items = visible.value.filter(m => checkedSet.value.has(m))
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
  <div class="flex h-full flex-col overflow-hidden">
    <div class="grid shrink-0 grid-cols-[28px_78px_64px_86px_1fr] items-center gap-2 border-b border-[#e5e7eb] bg-[#f9fafb] px-3 py-2 text-[11px] font-medium text-[#6b7280]">
      <input type="checkbox" :checked="allChecked" @change="toggleAll"
        class="h-3.5 w-3.5 accent-[#2563eb]" />
      <span>时间</span>
      <span>级别</span>
      <span>分类</span>
      <span>消息摘要</span>
    </div>

    <div v-if="filtered.length === 0" class="flex flex-1 items-center justify-center text-sm text-[#6b7280]">
      等待日志...
    </div>

    <div v-else ref="listEl" class="flex-1 overflow-y-auto text-[11px] leading-5">
      <div v-if="hiddenCount" class="border-b border-[#e5e7eb] bg-[#fffbeb] px-3 py-1.5 text-center text-[11px] text-[#92400e]">
        已隐藏较早的 {{ hiddenCount.toLocaleString() }} 条记录，仅渲染最近 {{ visible.length.toLocaleString() }} 条以保持流畅
      </div>
      <div
        v-for="msg in visible"
        :key="msg"
        class="log-row grid grid-cols-[28px_78px_64px_86px_1fr] items-center gap-2 border-b border-[#f3f4f6] px-3 py-2 cursor-pointer transition-colors hover:bg-[#f8fafc]"
        :class="selected === msg ? 'bg-[#eff6ff] ring-1 ring-inset ring-[#bfdbfe]' : ''"
        @click="emit('select', msg)"
      >
        <input type="checkbox" :checked="checkedSet.has(msg)" @click.stop @change="toggleItem(msg)"
          class="h-3.5 w-3.5 accent-[#2563eb]" />
        <span class="font-mono text-[#6b7280]" :title="msg.full_time || msg.time">{{ msg.time }}</span>
        <span class="inline-flex w-fit items-center rounded px-1.5 py-0.5 text-[10px] font-medium"
          :class="{
            'bg-[#fef2f2] text-[#ef4444]': msg.level === 'error',
            'bg-[#fffbeb] text-[#d97706]': msg.level === 'warn',
            'bg-[#eff6ff] text-[#2563eb]': msg.level === 'info',
            'bg-[#f3f4f6] text-[#6b7280]': msg.level === 'debug',
          }">
          {{ msg.level || 'log' }}
        </span>
        <span class="truncate font-medium" :class="categoryColor(msg.category)">{{ msg.category || '-' }}</span>
        <span class="truncate text-[#111827]" :title="msg.message">{{ msg.message }}</span>
      </div>
    </div>

    <div v-if="checkedSet.size > 0"
      class="shrink-0 flex items-center gap-3 border-t border-[#e5e7eb] bg-[#f9fafb] px-3 py-2 text-xs">
      <span class="text-[#6b7280]">已选 {{ checkedSet.size }} 条</span>
      <button class="inline-flex items-center gap-1 text-[#2563eb] hover:text-[#1d4ed8]" @click="copySelected">
        <IconCopy class="h-3.5 w-3.5" /> 复制
      </button>
      <button class="inline-flex items-center gap-1 text-[#2563eb] hover:text-[#1d4ed8]" @click="exportSelected">
        <IconExport class="h-3.5 w-3.5" /> 导出 JSON
      </button>
      <button class="text-[#6b7280] hover:text-[#111827]" @click="cancelSelection">取消选择</button>
    </div>
  </div>
</template>

<style scoped>
/* 长列表优化：视口外行跳过布局与绘制（滚动容器为上方 overflow-y-auto 的 listEl，行是其直接子元素）。
   41px = badge 内容行高 20px（text-[10px] 不带 line-height，继承 leading-5）
   + badge py-0.5 4px + 行 py-2 16px + border-b 1px；auto 在行渲染过一次后改用实际尺寸 */
.log-row {
  content-visibility: auto;
  contain-intrinsic-size: auto 41px;
}
</style>
