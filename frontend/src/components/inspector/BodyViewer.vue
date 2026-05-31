<script setup>
import { computed, ref } from 'vue'
import IconCopy from '../icons/IconCopy.vue'
import IconCheck from '../icons/IconCheck.vue'

const props = defineProps({
  content: { type: String, default: '' },
  title: { type: String, default: 'Body' },
})

const copied = ref(false)

const parsed = computed(() => {
  if (!props.content) return null
  try {
    return JSON.parse(props.content)
  } catch {
    return null
  }
})

const highlighted = computed(() => {
  if (!parsed.value) return null
  return syntaxHighlight(JSON.stringify(parsed.value, null, 2))
})

function syntaxHighlight(json) {
  return json
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(
      /("(\\u[a-zA-Z0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d*)?(?:[eE][+-]?\d+)?)/g,
      (match) => {
        let cls = 'text-[#059669]'
        if (/^"/.test(match)) {
          if (/:$/.test(match)) cls = 'text-[#2563eb]'
        } else if (/true|false/.test(match)) {
          cls = 'text-[#7c3aed]'
        } else if (/null/.test(match)) {
          cls = 'text-[#6b7280]'
        } else {
          cls = 'text-[#d97706]'
        }
        return `<span class="${cls}">${match}</span>`
      }
    )
}

async function copy() {
  try {
    await navigator.clipboard.writeText(props.content)
    copied.value = true
    setTimeout(() => { copied.value = false }, 1500)
  } catch {}
}
</script>

<template>
  <div class="flex flex-col h-full">
    <div class="flex items-center justify-between px-3 py-1.5 bg-[#f9fafb] border-b border-[#e5e7eb] shrink-0">
      <span class="text-xs text-[#6b7280]">{{ title }}</span>
      <button
        v-if="content"
        class="flex items-center gap-1 text-xs text-[#6b7280] hover:text-[#111827] transition-colors"
        @click="copy"
      >
        <IconCheck v-if="copied" class="w-3 h-3 text-[#10b981]" />
        <IconCopy v-else class="w-3 h-3" />
        {{ copied ? '已复制' : '复制' }}
      </button>
    </div>

    <div class="flex-1 overflow-auto p-3">
      <div v-if="!content" class="text-xs text-[#6b7280] italic">（空）</div>

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
