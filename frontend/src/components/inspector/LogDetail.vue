<script setup>
import { ref, computed } from 'vue'
import IconCopy from '../icons/IconCopy.vue'
import IconCheck from '../icons/IconCheck.vue'

const props = defineProps({
  entry: { type: Object, default: null }
})

const copied = ref('')

async function copyContent(text, label) {
  await navigator.clipboard.writeText(text)
  copied.value = label
  setTimeout(() => { copied.value = '' }, 1500)
}

async function copyAll() {
  if (!props.entry) return
  copyContent(JSON.stringify(props.entry, null, 2), 'all')
}

async function copyDetail() {
  if (!props.entry?.detail) return
  copyContent(props.entry.detail, 'detail')
}

const formattedDetail = computed(() => {
  if (!props.entry?.detail) return ''
  try {
    const obj = JSON.parse(props.entry.detail)
    return highlightJSON(JSON.stringify(obj, null, 2))
  } catch {
    return null
  }
})

const rawDetail = computed(() => {
  if (!props.entry?.detail) return ''
  return props.entry.detail
})

const prefixAuditLabels = {
  model: '使用的模型名称',
  endpoint: '请求接口端点',
  conv: '会话/对话 ID',
  body: '请求体总大小（通常为字节数）',
  system_static: '固定系统提示词大小',
  system_dynamic: '动态系统提示词大小',
  env: '环境上下文大小',
  question: '当前用户问题大小',
  history: '历史对话内容数量/大小',
  tools: '本次注入的工具数量/规模',
  tools_hash: '工具定义哈希，用于判断工具列表是否变化',
  input_items: 'Responses 输入项数量',
  input_hash: '输入内容哈希',
  continuation: '是否为续写/延续请求'
}

const prefixAuditSource = computed(() => {
  return props.entry?.detail || props.entry?.message || ''
})

const prefixAuditDetail = computed(() => {
  const text = prefixAuditSource.value
  const match = text.match(/prefix_audit:\s*([^\n\r]*)/)
  if (!match) return []

  return [...match[1].matchAll(/([^\s=]+)=([^\s]*)/g)].map(([, key, value]) => ({
    key,
    value: value || '(空)',
    label: prefixAuditLabels[key] || '未定义字段'
  }))
})

function highlightJSON(json) {
  return json
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(
      /("(\\u[a-zA-Z0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d*)?(?:[eE][+-]?\d+)?)/g,
      (match) => {
        let cls = 'json-str'
        if (/^"/.test(match)) {
          if (/:$/.test(match)) cls = 'json-key'
        } else if (/true|false/.test(match)) {
          cls = 'json-bool'
        } else if (/null/.test(match)) {
          cls = 'json-null'
        } else {
          cls = 'json-num'
        }
        return `<span class="${cls}">${match}</span>`
      }
    )
}
</script>

<template>
  <div class="h-full flex flex-col overflow-hidden">
    <template v-if="entry">
      <div class="shrink-0 px-4 py-3 border-b border-[#e5e7eb] bg-[#f9fafb]">
        <div class="flex items-center gap-2 mb-1">
          <span class="text-xs font-mono text-[#6b7280]">{{ entry.time }}</span>
          <span class="text-xs px-1.5 py-0.5 rounded"
            :class="{
              'bg-[#fef2f2] text-[#ef4444]': entry.level === 'error',
              'bg-[#fffbeb] text-[#f59e0b]': entry.level === 'warn',
              'bg-[#ecfdf5] text-[#10b981]': entry.level === 'info',
              'bg-[#f9fafb] text-[#6b7280]': entry.level === 'debug',
            }">{{ entry.level }}</span>
          <span class="text-xs text-[#3b82f6]">{{ entry.category }}</span>
        </div>
        <div class="text-sm text-[#111827]">{{ entry.message }}</div>
      </div>

      <div class="flex-1 overflow-y-auto p-4">
        <div v-if="entry.detail || prefixAuditDetail.length" class="mb-3">
          <div v-if="entry.detail" class="flex items-center justify-between mb-2">
            <span class="text-xs text-[#6b7280]">详情</span>
            <div class="flex gap-2">
              <button @click="copyDetail" class="flex items-center gap-1 text-[10px] text-[#6b7280] hover:text-[#111827]">
                <IconCheck v-if="copied === 'detail'" class="w-3 h-3 text-[#10b981]" />
                <IconCopy v-else class="w-3 h-3" />
                复制详情
              </button>
              <button @click="copyAll" class="flex items-center gap-1 text-[10px] text-[#6b7280] hover:text-[#111827]">
                <IconCheck v-if="copied === 'all'" class="w-3 h-3 text-[#10b981]" />
                <IconCopy v-else class="w-3 h-3" />
                复制全部
              </button>
            </div>
          </div>
          <div v-if="prefixAuditDetail.length" class="mb-3 rounded border border-[#bfdbfe] bg-[#eff6ff] overflow-hidden">
            <div class="px-3 py-2 text-xs font-medium text-[#1d4ed8] border-b border-[#bfdbfe]">
              Prefix Audit 字段说明
            </div>
            <div class="divide-y divide-[#dbeafe]">
              <div v-for="item in prefixAuditDetail" :key="item.key" class="grid grid-cols-[140px_1fr_1.4fr] gap-2 px-3 py-2 text-[11px]">
                <span class="font-mono text-[#2563eb]">{{ item.key }}</span>
                <span class="font-mono text-[#111827] break-all">{{ item.value }}</span>
                <span class="text-[#4b5563]">{{ item.label }}</span>
              </div>
            </div>
          </div>
          <pre v-if="entry.detail && formattedDetail"
            class="text-[11px] font-mono whitespace-pre-wrap break-all bg-[#f9fafb] rounded p-3 border border-[#e5e7eb] overflow-y-auto"
            style="max-height: 70vh;"
            v-html="formattedDetail"></pre>
          <pre v-else-if="entry.detail"
            class="text-[11px] text-[#111827] font-mono whitespace-pre-wrap break-all bg-[#f9fafb] rounded p-3 border border-[#e5e7eb] overflow-y-auto"
            style="max-height: 70vh;">{{ rawDetail }}</pre>
        </div>

        <div v-else class="text-xs text-[#6b7280]">无详细数据</div>
      </div>
    </template>

    <div v-else class="flex-1 flex items-center justify-center text-[#6b7280] text-sm">
      点击左侧日志查看详情
    </div>
  </div>
</template>

<style scoped>
:deep(.json-key) { color: #2563eb; }
:deep(.json-str) { color: #059669; }
:deep(.json-num) { color: #d97706; }
:deep(.json-bool) { color: #7c3aed; }
:deep(.json-null) { color: #6b7280; }
</style>
