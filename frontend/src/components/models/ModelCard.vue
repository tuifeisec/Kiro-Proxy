<script setup>
import { computed } from 'vue'
import IconPlay from '../icons/IconPlay.vue'
import IconEdit from '../icons/IconEdit.vue'
import IconTrash from '../icons/IconTrash.vue'

const props = defineProps({
  model: { type: Object, required: true },
  testResult: { type: Object, default: null },
  testing: { type: Boolean, default: false }
})

const emit = defineEmits(['edit', 'delete', 'test'])

const displayName = computed(() => props.model.name || props.model.model_id)
const maskedKey = computed(() => {
  const key = props.model.api_key || ''
  if (key.length <= 8) return '••••••••'
  return key.slice(0, 4) + '••••' + key.slice(-4)
})
const truncatedUrl = computed(() => {
  const url = props.model.base_url || ''
  return url.length > 40 ? url.slice(0, 40) + '...' : url
})
</script>

<template>
  <div class="bg-white border border-[#e5e7eb] rounded-lg p-4 flex flex-col gap-3 hover:border-[#d1d5db] transition-colors">
    <div class="flex items-start justify-between">
      <div class="flex-1 min-w-0">
        <h3 class="text-sm font-medium text-[#111827] truncate">{{ displayName }}</h3>
        <p class="text-xs text-[#6b7280] mt-0.5 font-mono">{{ model.model_id }}</p>
      </div>
      <span class="shrink-0 ml-2 px-2 py-0.5 text-[10px] font-medium rounded bg-[#ecfdf5] text-[#10b981] border border-[#10b981]/20">
        {{ model.provider === 'anthropic' ? 'Anthropic' : 'OpenAI' }}
      </span>
    </div>

    <div class="flex flex-col gap-1.5 text-xs">
      <div class="flex items-center gap-2">
        <span class="text-[#6b7280] w-12 shrink-0">HOST</span>
        <span class="text-[#111827] truncate">{{ truncatedUrl }}</span>
      </div>
      <div class="flex items-center gap-2">
        <span class="text-[#6b7280] w-12 shrink-0">KEY</span>
        <span class="text-[#111827] font-mono text-[11px]">{{ maskedKey }}</span>
      </div>
      <div class="flex items-center gap-2">
        <span class="text-[#6b7280] w-12 shrink-0">端点</span>
        <span class="text-[#111827]">{{ model.endpoint }}</span>
      </div>
    </div>

    <div v-if="testResult"
      class="text-xs px-2 py-1.5 rounded border"
      :class="testResult.ok
        ? 'bg-[#ecfdf5] border-[#10b981]/20 text-[#10b981]'
        : 'bg-[#fef2f2] border-[#ef4444]/20 text-[#ef4444]'">
      <template v-if="testResult.ok">
        {{ Math.round(testResult.tokens_per_sec) }} t/s | 首字 {{ (testResult.ttfb_ms / 1000).toFixed(1) }}s
      </template>
      <template v-else>
        {{ testResult.error }}
      </template>
    </div>

    <div class="flex items-center gap-3 pt-2 border-t border-[#e5e7eb]">
      <button class="flex items-center gap-1 text-xs text-[#6b7280] hover:text-[#10b981] disabled:opacity-50"
        :disabled="testing" @click="emit('test')">
        <IconPlay class="w-3 h-3" />
        {{ testing ? '测试中...' : '测试' }}
      </button>
      <button class="flex items-center gap-1 text-xs text-[#6b7280] hover:text-[#111827]" @click="emit('edit')">
        <IconEdit class="w-3 h-3" />
        编辑
      </button>
      <button class="flex items-center gap-1 text-xs text-[#6b7280] hover:text-[#ef4444]" @click="emit('delete')">
        <IconTrash class="w-3 h-3" />
        删除
      </button>
    </div>
  </div>
</template>
