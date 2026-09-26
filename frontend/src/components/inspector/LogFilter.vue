<script setup>
import { ref, watch, onScopeDispose } from 'vue'
import IconSearch from '../icons/IconSearch.vue'

// 搜索防抖时长：LogList 对全部日志（含 detail 大文本）线性扫描，逐键 emit 会造成输入卡顿
const SEARCH_DEBOUNCE = 250

defineProps({
  total: { type: Number, default: 0 },
})

const emit = defineEmits(['filter', 'clear'])

const search = ref('')
const level = ref('')
const includeDetail = ref(false)

let searchTimer = null

function emitFilter() {
  emit('filter', { search: search.value, level: level.value, includeDetail: includeDetail.value })
}

// 级别 / 搜详情变化立即生效；仅对 search 做防抖，定时器触发时读取的是最新最终值
watch([level, includeDetail], emitFilter)

watch(search, () => {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(emitFilter, SEARCH_DEBOUNCE)
})

onScopeDispose(() => clearTimeout(searchTimer))
</script>

<template>
  <div class="flex shrink-0 items-center gap-2 border-b border-line bg-surface px-3 py-2">
    <div class="relative min-w-0 flex-1">
      <IconSearch class="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-4" />
      <input
        v-model="search"
        class="h-8 w-full rounded-lg border border-line bg-surface-2 pl-7 pr-3 text-xs text-ink outline-none transition-colors placeholder:text-ink-4 focus:border-primary-tint-line-strong focus:bg-surface focus:ring-2 focus:ring-primary-ring"
        placeholder="搜索消息、分类..."
      />
    </div>

    <label class="inline-flex h-8 shrink-0 items-center gap-1 rounded-lg border border-line bg-surface px-2 text-[11px] text-ink-3">
      <input v-model="includeDetail" type="checkbox" class="h-3.5 w-3.5 accent-primary" />
      搜详情
    </label>

    <select
      v-model="level"
      class="h-8 rounded-lg border border-line bg-surface px-2.5 text-xs text-ink-3 outline-none transition-colors focus:border-primary-tint-line-strong focus:ring-2 focus:ring-primary-ring"
    >
      <option value="">全部级别</option>
      <option value="info">Info</option>
      <option value="warn">Warn</option>
      <option value="error">Error</option>
      <option value="debug">Debug</option>
    </select>

    <span class="shrink-0 rounded-md bg-hover px-2 py-1 text-xs text-ink-3">{{ total }} 条</span>
    <button
      class="h-8 shrink-0 rounded-lg border border-line bg-surface px-3 text-xs text-ink-3 transition-colors hover:border-danger-tint-line hover:bg-danger-tint hover:text-danger"
      @click="emit('clear')"
    >清空</button>
  </div>
</template>
