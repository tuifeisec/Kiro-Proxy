<script setup>
import { ref, computed } from 'vue'
import IconCopy from '../icons/IconCopy.vue'
import IconCheck from '../icons/IconCheck.vue'
import IconExport from '../icons/IconExport.vue'

const DETAIL_PREVIEW_LIMIT = 60000

const props = defineProps({
  entry: { type: Object, default: null }
})

const copied = ref('')
const copyFailed = ref('')

function fallbackCopy(text) {
  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.setAttribute('readonly', '')
  textarea.style.position = 'fixed'
  textarea.style.left = '-9999px'
  textarea.style.top = '0'
  document.body.appendChild(textarea)
  textarea.select()
  const ok = document.execCommand('copy')
  document.body.removeChild(textarea)
  if (!ok) throw new Error('copy command failed')
}

async function copyContent(text, label) {
  copyFailed.value = ''
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
    } else {
      fallbackCopy(text)
    }
    copied.value = label
    setTimeout(() => { copied.value = '' }, 1500)
  } catch {
    try {
      fallbackCopy(text)
      copied.value = label
      setTimeout(() => { copied.value = '' }, 1500)
    } catch {
      copyFailed.value = label
      setTimeout(() => { copyFailed.value = '' }, 1800)
    }
  }
}

async function copyAll() {
  if (!props.entry) return
  await copyContent(JSON.stringify(props.entry, null, 2), 'all')
}

async function copyDetail() {
  if (!props.entry?.detail) return
  await copyContent(props.entry.detail, 'detail')
}

function exportDetail() {
  if (!props.entry?.detail) return
  const blob = new Blob([props.entry.detail], { type: detailObject.value ? 'application/json' : 'text/plain' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `log-detail-${props.entry.id || Date.now()}.${detailObject.value ? 'json' : 'txt'}`
  a.click()
  URL.revokeObjectURL(url)
}

const detailSize = computed(() => props.entry?.detail?.length || 0)
const isLargeDetail = computed(() => detailSize.value > DETAIL_PREVIEW_LIMIT)

const detailObject = computed(() => {
  if (!props.entry?.detail || isLargeDetail.value) return null
  try {
    return JSON.parse(props.entry.detail)
  } catch {
    return null
  }
})

const formattedDetail = computed(() => {
  if (!detailObject.value) return ''
  return highlightJSON(JSON.stringify(detailObject.value, null, 2))
})

const rawDetail = computed(() => {
  if (!props.entry?.detail) return ''
  if (!isLargeDetail.value) return props.entry.detail
  return `${props.entry.detail.slice(0, DETAIL_PREVIEW_LIMIT)}\n\n... 已省略 ${(detailSize.value - DETAIL_PREVIEW_LIMIT).toLocaleString()} 字符，使用“导出详情”查看完整内容。`
})

const prefixAuditLabels = {
  model: '使用的模型名称',
  model_config: '模型配置 ID',
  endpoint: '请求接口端点',
  api: '上游 API 类型',
  conv: '会话/对话 ID',
  convId: '会话/对话 ID',
  classifier: '是否为分类请求',
  body: '请求体大小（通常为字节数）',
  system_static: '固定系统提示词大小',
  system_dynamic: '动态系统提示词大小',
  system_hash: '固定系统提示词哈希',
  env: '环境上下文大小',
  question: '当前用户问题大小',
  history: '历史对话内容数量/大小',
  tools: '本次注入的工具数量/规模',
  tools_hash: '工具定义哈希，用于判断工具列表是否变化',
  raw_tools_hash: 'Kiro 原始工具定义哈希',
  provider_tools_hash: '实际发送给上游的工具定义哈希',
  tool_invalid: '无效工具数量',
  tool_schema_invalid: '无效工具 Schema 数量',
  tool_websearch_filtered: '被过滤的 Web Search 工具数量',
  tool_desc_truncated: '描述被截断的工具数量',
  tool_over_limit: '超过数量上限的工具数量',
  tool_results: '工具结果数量',
  tool_blocks: '工具结果内容块数量',
  tool_nontext_blocks: '非文本工具结果块数量',
  input_items: 'Responses 输入项数量',
  input_hash: '输入内容哈希',
  provider_input_items: '实际上游输入项数量',
  provider_input_hash: '实际上游输入内容哈希',
  continuation: '是否为续写/延续请求',
  previous_response: '是否携带 previous_response_id',
  previous_audit: '是否存在上一轮审计快照',
  cache_key: '实际 Prompt Cache Key',
  expected_key: '按当前规则计算的 Prompt Cache Key',
  input_relation: '与上一轮输入的关系',
  prefix_changed: '缓存敏感前缀是否变化',
  change_reasons: '前缀变化原因',
  stable_items: '与上一轮一致的前缀项数',
  added_items: '新增输入项数',
  removed_items: '移除输入项数',
  first_changed_item: '首个变化的输入项位置',
  provider: '上游 Provider',
  model_id: '上游真实模型 ID',
  status: '上游响应状态',
  status_code: '上游响应状态码',
  content_length: '响应 Content-Length',
  proto: 'HTTP 协议版本',
  content_type: 'Content-Type',
  request_id: '请求 ID',
  in: '输入 Token 数',
  out: '输出 Token 数',
  cache_read: '缓存读取 Token 数',
  input: '上游输入项数量',
  tools: '工具数量',
  reasoning_effort: '推理强度',
  stream: '是否流式请求',
  store: '是否存储响应',
  prompt_cache_key: 'Prompt Cache Key',
  user: '上游 user 字段',
  profileArn: 'Kiro Profile ARN',
  conversationState: 'Kiro 会话状态',
  currentMessage: '当前消息',
  history_count: '历史消息数量',
  nextDateReset: '用量限制重置时间',
  usageBreakdownList: '用量明细数量',
  subscriptionInfo: '订阅信息',
  userInfo: '用户信息',
  limits: '限制信息',
  type: '抓包记录类型',
  protocol: '识别到的协议',
  method: 'HTTP 方法',
  url: '完整 URL',
  host: '请求 Host',
  path: '请求路径',
  query: '查询参数',
  headers: 'Header 数量',
  body_type: 'Body 类型',
  body_size: 'Body 大小/规模',
  body_model: 'Body 中的模型',
  body_messages: 'Body 消息数量',
  body_input: 'Body 输入项数量',
  body_tools: 'Body 工具数量',
  body_events: 'AWS Event Stream 事件数量',
  event_types: '事件类型',
  encoding: 'Body 编码方式'
}

const parseableFieldKeys = new Set(Object.keys(prefixAuditLabels))

function displayValue(value) {
  if (value === undefined || value === null || value === '') return '(空)'
  if (Array.isArray(value)) return `${value.length} 项`
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

const fieldGroupMeta = {
  audit: { title: 'Prefix Audit', tone: 'blue' },
  request: { title: '请求 / 抓包', tone: 'amber' },
  body: { title: 'Body 摘要', tone: 'violet' },
  upstreamRequest: { title: '上游请求', tone: 'emerald' },
  upstreamResponse: { title: '上游响应', tone: 'green' },
  kiro: { title: 'Kiro 会话', tone: 'sky' },
  usage: { title: '用量限制', tone: 'orange' },
  token: { title: 'Token 统计', tone: 'purple' }
}

const fieldGroupOrder = ['audit', 'request', 'body', 'upstreamRequest', 'upstreamResponse', 'kiro', 'usage', 'token']

function inferMessageGroup(key) {
  if (['in', 'out', 'cache_read'].includes(key)) return 'token'
  if (['status', 'status_code', 'content_length', 'proto', 'content_type', 'request_id'].includes(key)) return 'upstreamResponse'
  if (['provider', 'model_id', 'model', 'input', 'tools', 'reasoning_effort', 'stream', 'store', 'prompt_cache_key', 'user'].includes(key)) return 'upstreamRequest'
  if (['conv', 'convId', 'classifier', 'profileArn', 'conversationState', 'currentMessage', 'history_count'].includes(key)) return 'kiro'
  if (['body', 'body_type', 'body_size', 'body_model', 'body_messages', 'body_input', 'body_tools', 'body_events', 'event_types', 'encoding'].includes(key)) return 'body'
  return 'request'
}

function appendField(fields, group, key, value, label = prefixAuditLabels[key]) {
  if (value === undefined || value === null || value === '') return
  fields.push({ group, key, value: displayValue(value), label: label || '未定义字段' })
}

function parseKeyValueFields(text, { whitelist = true, group = null } = {}) {
  return [...text.matchAll(/([A-Za-z_][\w]*)=([^,\s\n\r)]*)/g)]
    .filter(([, key]) => !whitelist || parseableFieldKeys.has(key))
    .map(([, key, value]) => ({
      group: group || inferMessageGroup(key),
      key,
      value: value || '(空)',
      label: prefixAuditLabels[key] || '未定义字段'
    }))
}

function appendBodySummary(fields, body) {
  if (body === undefined || body === null || body === '') return
  if (typeof body === 'string') {
    appendField(fields, 'body', 'body_type', 'text')
    appendField(fields, 'body', 'body_size', body.length)
    return
  }
  if (Array.isArray(body)) {
    appendField(fields, 'body', 'body_type', 'array')
    appendField(fields, 'body', 'body_size', body.length)
    return
  }
  if (typeof body !== 'object') {
    appendField(fields, 'body', 'body_type', typeof body)
    appendField(fields, 'body', 'body_size', String(body).length)
    return
  }

  appendField(fields, 'body', 'body_type', body.encoding || 'json')
  appendField(fields, 'body', 'encoding', body.encoding)
  appendField(fields, 'body', 'body_size', body.size)
  appendField(fields, 'body', 'body_model', body.model)
  appendField(fields, 'body', 'body_messages', Array.isArray(body.messages) ? body.messages.length : undefined)
  appendField(fields, 'body', 'body_input', Array.isArray(body.input) ? body.input.length : undefined)
  appendField(fields, 'body', 'body_tools', Array.isArray(body.tools) ? body.tools.length : undefined)
  if (Array.isArray(body.events)) {
    appendField(fields, 'body', 'body_events', body.events.length)
    const types = [...new Set(body.events.map((event) => event?.event_type).filter(Boolean))]
    appendField(fields, 'body', 'event_types', types.join(', '))
  }
}

function appendResponseSummary(fields, source) {
  if (!source || typeof source !== 'object') return
  appendField(fields, 'upstreamResponse', 'status_code', source.status_code)
  appendField(fields, 'upstreamResponse', 'status', source.status)
  appendField(fields, 'upstreamResponse', 'proto', source.proto)
  appendField(fields, 'upstreamResponse', 'content_length', source.content_length)
  if (source.headers && typeof source.headers === 'object') {
    appendField(fields, 'upstreamResponse', 'content_type', source.headers['Content-Type'] || source.headers['content-type'])
    appendField(fields, 'upstreamResponse', 'request_id', source.headers['X-Request-Id'] || source.headers['x-request-id'] || source.headers['Request-Id'])
  }
}

function parseDetailFields(obj) {
  const fields = []
  if (!obj || typeof obj !== 'object') return fields

  appendField(fields, 'request', 'type', obj.type)
  appendField(fields, 'request', 'protocol', obj.protocol)
  appendField(fields, 'request', 'method', obj.method)
  appendField(fields, 'request', 'host', obj.host)
  appendField(fields, 'request', 'path', obj.path)
  appendField(fields, 'request', 'url', obj.url)
  appendField(fields, 'request', 'query', obj.query)
  appendField(fields, 'request', 'content_type', obj.content_type)
  if (obj.headers && typeof obj.headers === 'object') {
    appendField(fields, 'request', 'headers', Object.keys(obj.headers).length)
  }

  appendBodySummary(fields, obj.body)
  appendResponseSummary(fields, obj)
  appendResponseSummary(fields, obj.upstream_response)

  appendField(fields, 'upstreamRequest', 'model', obj.model)
  appendField(fields, 'upstreamRequest', 'input', Array.isArray(obj.input) ? obj.input.length : undefined)
  appendField(fields, 'upstreamRequest', 'tools', Array.isArray(obj.tools) ? obj.tools.length : undefined)
  appendField(fields, 'upstreamRequest', 'reasoning_effort', obj.reasoning_effort)
  appendField(fields, 'upstreamRequest', 'stream', typeof obj.stream === 'boolean' ? obj.stream : undefined)
  appendField(fields, 'upstreamRequest', 'store', typeof obj.store === 'boolean' ? obj.store : undefined)
  appendField(fields, 'upstreamRequest', 'prompt_cache_key', obj.prompt_cache_key)
  appendField(fields, 'upstreamRequest', 'user', obj.user)

  appendField(fields, 'kiro', 'profileArn', obj.profileArn)
  if (obj.conversationState && typeof obj.conversationState === 'object') {
    appendField(fields, 'kiro', 'conversationState', '已解析', 'Kiro 会话状态')
    appendField(fields, 'kiro', 'currentMessage', obj.conversationState.currentMessage ? '存在' : undefined)
    appendField(fields, 'kiro', 'history_count', Array.isArray(obj.conversationState.history) ? obj.conversationState.history.length : undefined)
  }

  appendField(fields, 'usage', 'nextDateReset', obj.nextDateReset)
  appendField(fields, 'usage', 'usageBreakdownList', Array.isArray(obj.usageBreakdownList) ? obj.usageBreakdownList.length : undefined)
  appendField(fields, 'usage', 'subscriptionInfo', obj.subscriptionInfo ? '存在' : undefined)
  appendField(fields, 'usage', 'userInfo', obj.userInfo ? '存在' : undefined)
  appendField(fields, 'usage', 'limits', obj.limits ? '存在' : undefined)

  if (obj.usage && typeof obj.usage === 'object') {
    appendField(fields, 'token', 'in', obj.usage.input_tokens, '输入 Token 数')
    appendField(fields, 'token', 'out', obj.usage.output_tokens, '输出 Token 数')
    appendField(fields, 'token', 'cache_read', obj.usage.cache_read_tokens, '缓存读取 Token 数')
    appendField(fields, 'token', 'cache_write', obj.usage.cache_write_tokens, '缓存写入 Token 数')
  }

  return fields
}

const parsedFieldGroups = computed(() => {
  const message = props.entry?.message || ''
  const detail = props.entry?.detail || ''
  const auditMatch = `${message}\n${detail}`.match(/prefix_audit:\s*([^\n\r]*)/)
  const fields = auditMatch
    ? parseKeyValueFields(auditMatch[1], { whitelist: true, group: 'audit' })
    : parseKeyValueFields(message, { whitelist: true })

  fields.push(...parseDetailFields(detailObject.value))

  const seen = new Set()
  const uniqueFields = fields.filter((item) => {
    const id = `${item.group}:${item.key}`
    if (seen.has(id)) return false
    seen.add(id)
    return true
  })

  return fieldGroupOrder
    .map((id) => ({
      id,
      ...fieldGroupMeta[id],
      fields: uniqueFields.filter((item) => item.group === id)
    }))
    .filter((group) => group.fields.length)
})

const parsedFieldCount = computed(() => parsedFieldGroups.value.reduce((total, group) => total + group.fields.length, 0))

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
  <div class="flex h-full flex-col overflow-hidden">
    <template v-if="entry">
      <div class="shrink-0 border-b border-[#e5e7eb] bg-white px-4 py-3">
        <div class="mb-2 flex items-center justify-between gap-3">
          <h2 class="text-sm font-semibold text-[#111827]">日志详情</h2>
          <div class="flex items-center gap-2">
            <button @click="copyDetail" class="inline-flex h-7 items-center gap-1 rounded-md border border-[#e5e7eb] bg-white px-2.5 text-[11px] text-[#6b7280] hover:text-[#111827] disabled:opacity-50" :disabled="!entry.detail">
              <IconCheck v-if="copied === 'detail'" class="h-3.5 w-3.5 text-[#10b981]" />
              <IconCopy v-else class="h-3.5 w-3.5" />
              {{ copied === 'detail' ? '已复制' : copyFailed === 'detail' ? '复制失败' : '复制详情' }}
            </button>
            <button @click="exportDetail" class="inline-flex h-7 items-center gap-1 rounded-md border border-[#e5e7eb] bg-white px-2.5 text-[11px] text-[#6b7280] hover:text-[#111827] disabled:opacity-50" :disabled="!entry.detail">
              <IconExport class="h-3.5 w-3.5" />
              导出详情
            </button>
            <button @click="copyAll" class="inline-flex h-7 items-center gap-1 rounded-md border border-[#e5e7eb] bg-white px-2.5 text-[11px] text-[#6b7280] hover:text-[#111827]">
              <IconCheck v-if="copied === 'all'" class="h-3.5 w-3.5 text-[#10b981]" />
              <IconCopy v-else class="h-3.5 w-3.5" />
              {{ copied === 'all' ? '已复制' : copyFailed === 'all' ? '复制失败' : '复制全部' }}
            </button>
          </div>
        </div>
        <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-[#6b7280]">
          <span>时间：<b class="font-mono font-normal text-[#374151]">{{ entry.time }}</b></span>
          <span>级别：<b class="font-normal" :class="{
              'text-[#ef4444]': entry.level === 'error',
              'text-[#d97706]': entry.level === 'warn',
              'text-[#2563eb]': entry.level === 'info',
              'text-[#6b7280]': entry.level === 'debug',
            }">{{ entry.level }}</b></span>
          <span>分类：<b class="font-normal text-[#374151]">{{ entry.category }}</b></span>
          <span class="min-w-0 flex-1 truncate">消息：<b class="font-normal text-[#374151]">{{ entry.message }}</b></span>
        </div>
      </div>

      <div class="min-h-0 flex-1 overflow-y-auto p-3">
        <div v-if="entry.detail || parsedFieldCount" class="grid h-full gap-3" :class="entry.detail ? 'grid-cols-[1fr_300px]' : 'grid-cols-1'">
          <div v-if="entry.detail" class="min-w-0 overflow-hidden rounded-lg border border-[#1f2937] bg-[#111827]">
            <div class="flex items-center justify-between border-b border-white/10 px-3 py-2">
              <span class="text-xs font-medium text-white/80">detail</span>
              <span class="text-[10px] text-white/40">{{ isLargeDetail ? '预览' : 'JSON / raw' }} · {{ detailSize.toLocaleString() }} 字符</span>
            </div>
            <pre v-if="formattedDetail"
              class="h-[calc(100%-33px)] overflow-auto p-3 text-[11px] font-mono leading-5 whitespace-pre-wrap break-all text-[#d1d5db]"
              v-html="formattedDetail"></pre>
            <pre v-else
              class="h-[calc(100%-33px)] overflow-auto p-3 text-[11px] font-mono leading-5 whitespace-pre-wrap break-all text-[#d1d5db]">{{ rawDetail }}</pre>
          </div>

          <div class="min-w-0 overflow-hidden rounded-lg border border-[#e5e7eb] bg-white">
            <div class="flex items-center justify-between border-b border-[#e5e7eb] px-3 py-2">
              <span class="text-xs font-semibold text-[#111827]">关键字段摘要</span>
              <span class="rounded bg-[#f3f4f6] px-1.5 py-0.5 text-[10px] text-[#6b7280]">{{ parsedFieldCount }} 项</span>
            </div>

            <div v-if="parsedFieldGroups.length" class="h-[calc(100%-37px)] space-y-2 overflow-y-auto p-2">
              <section
                v-for="group in parsedFieldGroups"
                :key="group.id"
                class="overflow-hidden rounded-md border border-[#eef2f7] bg-[#fbfdff]"
              >
                <div class="flex items-center justify-between border-b border-[#eef2f7] bg-[#f8fafc] px-2.5 py-1.5">
                  <span class="text-[11px] font-semibold text-[#111827]">{{ group.title }}</span>
                  <span class="text-[10px] text-[#9ca3af]">{{ group.fields.length }} 项</span>
                </div>
                <div class="divide-y divide-[#f3f4f6]">
                  <div v-for="item in group.fields" :key="`${group.id}:${item.key}`" class="px-2.5 py-1.5">
                    <div class="flex min-w-0 items-center justify-between gap-2">
                      <span class="truncate font-mono text-[10px] text-[#2563eb]" :title="item.key">{{ item.key }}</span>
                      <span class="max-w-[148px] truncate font-mono text-[11px] text-[#111827]" :title="item.value">{{ item.value }}</span>
                    </div>
                    <div class="mt-0.5 truncate text-[10px] text-[#6b7280]" :title="item.label">{{ item.label }}</div>
                  </div>
                </div>
              </section>
            </div>
            <div v-else class="p-3 text-xs text-[#6b7280]">当前日志无可解析字段</div>
          </div>
        </div>

        <div v-else class="flex h-full items-center justify-center rounded-lg border border-dashed border-[#e5e7eb] text-xs text-[#6b7280]">无详细数据</div>
      </div>
    </template>

    <div v-else class="flex flex-1 items-center justify-center text-sm text-[#6b7280]">
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
