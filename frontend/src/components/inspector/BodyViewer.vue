<script setup>
import { computed } from 'vue'
import IconCopy from '../icons/IconCopy.vue'
import IconCheck from '../icons/IconCheck.vue'
import { highlightJSON } from '../../utils/highlight.js'
import { useCopy } from '../../composables/useCopy.js'

const props = defineProps({
  content: { type: String, default: '' },
  title: { type: String, default: 'Body' },
})

// 与 LogDetail.vue 的 DETAIL_PREVIEW_LIMIT 保持一致：超过该长度的内容不做 JSON 解析
// 和语法高亮（JSON.parse / stringify / 正则替换 / v-html 全量渲染会导致页面卡死），
// 降级为纯文本截断展示
const CONTENT_PREVIEW_LIMIT = 60000

const { copied, copy } = useCopy()

const isLargeContent = computed(() => props.content.length > CONTENT_PREVIEW_LIMIT)

const truncatedContent = computed(() => {
  if (!isLargeContent.value) return ''
  return props.content.slice(0, CONTENT_PREVIEW_LIMIT)
})

const parsed = computed(() => {
  if (!props.content || isLargeContent.value) return null
  try {
    return JSON.parse(props.content)
  } catch {
    return null
  }
})

const highlighted = computed(() => {
  if (!parsed.value) return null
  return highlightJSON(JSON.stringify(parsed.value, null, 2))
})
</script>

<template>
  <div class="flex flex-col h-full">
    <div class="flex items-center justify-between px-3 py-1.5 bg-[#f9fafb] border-b border-[#e5e7eb] shrink-0">
      <span class="text-xs text-[#6b7280]">{{ title }}</span>
      <button
        v-if="content"
        class="flex items-center gap-1 text-xs text-[#6b7280] hover:text-[#111827] transition-colors"
        @click="copy(content)"
      >
        <IconCheck v-if="copied" class="w-3 h-3 text-[#10b981]" />
        <IconCopy v-else class="w-3 h-3" />
        {{ copied ? '已复制' : '复制' }}
      </button>
    </div>

    <div class="flex-1 overflow-auto p-3">
      <div v-if="!content" class="text-xs text-[#6b7280] italic">（空）</div>

      <template v-else-if="isLargeContent">
        <pre
          class="text-xs font-mono text-[#111827] leading-relaxed whitespace-pre-wrap break-all"
        >{{ truncatedContent }}</pre>
        <div class="mt-2 text-xs text-[#6b7280] italic">
          内容已截断，仅显示前 {{ CONTENT_PREVIEW_LIMIT.toLocaleString() }} 字符（共 {{ content.length.toLocaleString() }} 字符）
        </div>
      </template>

      <pre
        v-else-if="highlighted"
        class="text-xs font-mono leading-relaxed whitespace-pre-wrap break-all"
        v-html="highlighted"
      ></pre>

      <pre
        v-else
        class="text-xs font-mono text-[#111827] leading-relaxed whitespace-pre-wrap break-all"
      >{{ content }}</pre>
    </div>
  </div>
</template>
