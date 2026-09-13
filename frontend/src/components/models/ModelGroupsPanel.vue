<script setup>
import { computed, ref } from 'vue'
import IconArrowRight from '../icons/IconArrowRight.vue'
import IconCheck from '../icons/IconCheck.vue'
import IconEdit from '../icons/IconEdit.vue'
import IconModels from '../icons/IconModels.vue'
import IconPlus from '../icons/IconPlus.vue'
import IconTrash from '../icons/IconTrash.vue'
import IconUsage from '../icons/IconUsage.vue'

const props = defineProps({
  groups: { type: Array, default: () => [] },
  models: { type: Array, default: () => [] },
  health: { type: Array, default: () => [] },
  testResults: { type: Object, default: () => ({}) },
  saving: { type: Boolean, default: false },
  deleting: { type: Boolean, default: false },
})

const emit = defineEmits(['save', 'delete'])
const showEditor = ref(false)
const editingID = ref('')
const groupName = ref('')
const groupDescription = ref('')
const groupEnabled = ref(true)
const firstByteTimeoutMs = ref(10000)
const maxAttempts = ref(4)
const fallbackGroupID = ref('')
const selectedModelIDs = ref([])
const memberConcurrency = ref({})
const validationError = ref('')
const deleteTarget = ref(null)
const expandedGroupIDs = ref(new Set())

const modelByID = computed(() => new Map(props.models.map((model) => [model.id, model])))
const healthByModelID = computed(() => new Map(props.health.map((status) => [status.model_config_id, status])))
const groupedModelIDs = computed(() => {
  const result = new Map()
  for (const group of props.groups) {
    for (const member of group.members || []) result.set(member.model_id, group.id)
  }
  return result
})

const selectedModels = computed(() => selectedModelIDs.value
  .map((id) => modelByID.value.get(id))
  .filter(Boolean))

const availableModels = computed(() => props.models.filter((model) => {
  const owner = groupedModelIDs.value.get(model.id)
  return !owner || owner === editingID.value
}))

function sortedMembers(group) {
  return [...(group.members || [])].sort((a, b) => Number(a.priority || 0) - Number(b.priority || 0))
}

function isExpanded(groupID) {
  return expandedGroupIDs.value.has(groupID)
}

function toggleGroup(groupID) {
  const next = new Set(expandedGroupIDs.value)
  if (next.has(groupID)) next.delete(groupID)
  else next.add(groupID)
  expandedGroupIDs.value = next
}

function toggleAllGroups() {
  if (props.groups.length > 0 && props.groups.every((group) => isExpanded(group.id))) {
    expandedGroupIDs.value = new Set()
    return
  }
  expandedGroupIDs.value = new Set(props.groups.map((group) => group.id))
}

const allGroupsExpanded = computed(() => props.groups.length > 0 && props.groups.every((group) => isExpanded(group.id)))

function openCreate() {
  editingID.value = ''
  groupName.value = ''
  groupDescription.value = ''
  groupEnabled.value = true
  firstByteTimeoutMs.value = 10000
  maxAttempts.value = 4
  fallbackGroupID.value = ''
  selectedModelIDs.value = []
  memberConcurrency.value = {}
  validationError.value = ''
  showEditor.value = true
}

function openEdit(group) {
  editingID.value = group.id
  groupName.value = group.name
  groupDescription.value = group.description || ''
  groupEnabled.value = group.enabled !== false
  firstByteTimeoutMs.value = Number(group.first_byte_timeout_ms ?? 10000)
  maxAttempts.value = Number(group.max_attempts ?? 4)
  fallbackGroupID.value = group.fallback_group_id || ''
  const members = sortedMembers(group)
  selectedModelIDs.value = members.map((member) => member.model_id)
  memberConcurrency.value = Object.fromEntries(members.map((member) => [member.model_id, Number(member.max_concurrency || 0)]))
  validationError.value = ''
  showEditor.value = true
}

function closeEditor() {
  if (props.saving) return
  showEditor.value = false
}

function toggleMember(modelID) {
  const index = selectedModelIDs.value.indexOf(modelID)
  if (index >= 0) {
    selectedModelIDs.value.splice(index, 1)
    const next = { ...memberConcurrency.value }
    delete next[modelID]
    memberConcurrency.value = next
  } else {
    selectedModelIDs.value.push(modelID)
    memberConcurrency.value = { ...memberConcurrency.value, [modelID]: 0 }
  }
}

function setMemberConcurrency(modelID, value) {
  const normalized = Math.min(1000, Math.max(0, Math.floor(Number(value) || 0)))
  memberConcurrency.value = { ...memberConcurrency.value, [modelID]: normalized }
}

function moveMember(index, offset) {
  const target = index + offset
  if (target < 0 || target >= selectedModelIDs.value.length) return
  const next = [...selectedModelIDs.value]
  ;[next[index], next[target]] = [next[target], next[index]]
  selectedModelIDs.value = next
}

function saveGroup() {
  const name = groupName.value.trim()
  if (!name) {
    validationError.value = '请输入分组名称'
    return
  }
  if (selectedModelIDs.value.length === 0) {
    validationError.value = '请至少选择一个模型'
    return
  }
  const fallbackGroup = props.groups.find((group) => group.id === fallbackGroupID.value)
  if (fallbackGroup?.enabled === false) {
    validationError.value = '回退组已停用，请启用该分组或重新选择'
    return
  }
  validationError.value = ''
  emit('save', {
    id: editingID.value,
    name,
    description: groupDescription.value.trim(),
    enabled: groupEnabled.value,
    first_byte_timeout_ms: Math.min(300000, Math.max(10000, Math.floor(Number(firstByteTimeoutMs.value) || 10000))),
    max_attempts: Math.min(16, Math.max(1, Math.floor(Number(maxAttempts.value) || 1))),
    fallback_group_id: fallbackGroupID.value || '',
    members: selectedModelIDs.value.map((modelID, priority) => ({
      model_id: modelID,
      priority,
      max_concurrency: Math.min(1000, Math.max(0, Math.floor(Number(memberConcurrency.value[modelID]) || 0))),
    })),
    done: () => { showEditor.value = false },
    fail: (message) => { validationError.value = message },
  })
}

function healthMeta(model) {
  if (!model || model.enabled === false) return { label: '已禁用', tone: 'bg-[#f3f4f6] text-[#64748b]', dot: 'bg-[#94a3b8]' }
  const status = healthByModelID.value.get(model.id)
  if (!status) return { label: '状态未知', tone: 'bg-[#f3f4f6] text-[#64748b]', dot: 'bg-[#94a3b8]' }
  if (status.state === 'open') return { label: '熔断中', tone: 'bg-[#fef2f2] text-[#dc2626]', dot: 'bg-[#ef4444]' }
  if (status.upstream_bucket_state === 'open') return { label: '上游回避', tone: 'bg-[#fef2f2] text-[#dc2626]', dot: 'bg-[#ef4444]' }
  if (status.state === 'half-open') return { label: '恢复试探', tone: 'bg-[#fff7ed] text-[#c2410c]', dot: 'bg-[#f59e0b]' }
  if (status.upstream_bucket_state === 'half-open-busy') return { label: '上游试探中', tone: 'bg-[#fff7ed] text-[#c2410c]', dot: 'bg-[#f59e0b]' }
  if (status.upstream_bucket_state === 'half-open') return { label: '上游恢复试探', tone: 'bg-[#fff7ed] text-[#c2410c]', dot: 'bg-[#f59e0b]' }
  if (status.latency_degraded) return { label: '响应降级', tone: 'bg-[#fff7ed] text-[#c2410c]', dot: 'bg-[#f59e0b]' }
  if (Number(status.consecutive_failures || 0) > 0) return { label: '异常观察', tone: 'bg-[#fff7ed] text-[#c2410c]', dot: 'bg-[#f59e0b]' }
  return { label: '正常', tone: 'bg-[#ecfdf5] text-[#059669]', dot: 'bg-[#10b981]' }
}

function groupSummary(group) {
  const members = sortedMembers(group)
  let available = 0
  let open = 0
  let sessions = 0
  let active = 0
  let avoided = 0
  let concurrencyLimit = 0
  let unlimitedConcurrency = false
  for (const member of members) {
    const model = modelByID.value.get(member.model_id)
    const status = healthByModelID.value.get(member.model_id)
    const upstreamUnavailable = status?.upstream_bucket_state === 'open' || status?.upstream_bucket_state === 'half-open-busy'
    const candidateAvoided = upstreamUnavailable || Boolean(status?.latency_degraded) || Number(status?.avoided_scopes || 0) > 0
    if (status?.state === 'open') open += 1
    if (group.enabled !== false && model && model.enabled !== false && status?.state !== 'open' && !upstreamUnavailable) available += 1
    sessions += Number(status?.affinity_sessions || 0)
    active += Number(status?.current_concurrency ?? status?.active_requests ?? 0)
    if (candidateAvoided) avoided += 1
    const memberLimit = Number(status?.max_concurrency ?? member.max_concurrency ?? 0)
    if (memberLimit > 0) concurrencyLimit += memberLimit
    else unlimitedConcurrency = true
  }
  return {
    total: members.length,
    available,
    open,
    sessions,
    active,
    avoided,
    concurrencyLimit: unlimitedConcurrency ? 0 : concurrencyLimit,
  }
}

function memberHealth(member) {
  return healthByModelID.value.get(member.model_id) || {}
}

function memberRole(index) {
  return index === 0 ? '主选' : '备用'
}

function formatOpenUntil(value) {
  if (!value) return '-'
  const time = new Date(value)
  if (Number.isNaN(time.getTime())) return value
  return time.toLocaleString('zh-CN', { hour12: false })
}

function concurrencyText(member) {
  const status = memberHealth(member)
  const active = Number(status.current_concurrency ?? status.active_requests ?? 0)
  const limit = Number(status.max_concurrency ?? member.max_concurrency ?? 0)
  return `${active} / ${limit > 0 ? limit : '不限'}`
}

function formatLatency(value) {
  const milliseconds = Number(value || 0)
  if (milliseconds <= 0) return '-'
  if (milliseconds < 1000) return `${Math.round(milliseconds)}ms`
  return `${(milliseconds / 1000).toFixed(milliseconds < 10000 ? 1 : 0)}s`
}

function memberPerformance(member) {
  const runtime = memberHealth(member)
  const test = props.testResults?.[member.model_id]
  const sampleCount = Number(runtime.first_byte_sample_count || 0)
  const lastFirstByte = Number(runtime.last_first_byte_ms || 0)
  const averageFirstByte = Number(runtime.average_first_byte_ms || 0)
  const ewmaFirstByte = Number(runtime.ewma_first_byte_ms || 0)
  const p95FirstByte = Number(runtime.p95_first_byte_ms || 0)
  const slowSamples = Number(runtime.slow_first_byte_samples || 0)
  const testFirstByte = Number(test?.ttfb_ms || 0)
  const tokensPerSecond = Number(runtime.tokens_per_sec ?? test?.tokens_per_sec ?? 0)
  const hasRuntimeMeasurement = sampleCount > 0 && (lastFirstByte > 0 || ewmaFirstByte > 0 || averageFirstByte > 0)
  const firstByte = hasRuntimeMeasurement ? (ewmaFirstByte || lastFirstByte || averageFirstByte) : testFirstByte
  const failed = test?.ok === false && !hasRuntimeMeasurement && firstByte <= 0
  let detail = '暂无实测数据'
  if (hasRuntimeMeasurement) {
    detail = `P95 ${formatLatency(p95FirstByte || firstByte)} · ${sampleCount}次${slowSamples > 0 ? ` · 慢${slowSamples}` : ''}`
  } else if (testFirstByte > 0) {
    detail = tokensPerSecond > 0 ? `最近测速 · ${tokensPerSecond.toFixed(1)} t/s` : '最近连通性测试'
  }
  return {
    firstByte: failed ? '测试失败' : formatLatency(firstByte),
    detail,
    hasMeasurement: firstByte > 0,
    failed,
  }
}

function groupConcurrencyText(summary) {
  return `${summary.active} / ${summary.concurrencyLimit > 0 ? summary.concurrencyLimit : '不限'}`
}

function fallbackGroupName(group) {
  if (!group.fallback_group_id) return '无回退组'
  const fallback = props.groups.find((item) => item.id === group.fallback_group_id)
  if (!fallback) return group.fallback_group_id
  return `${fallback.name}${fallback.enabled === false ? '（已停用）' : ''}`
}

function memberName(member) {
  const model = modelByID.value.get(member.model_id)
  return model?.name || model?.model_id || member.model_id
}

const groupSummariesByID = computed(() => {
  const map = new Map()
  for (const group of props.groups) map.set(group.id, groupSummary(group))
  return map
})

const memberRowsByGroupID = computed(() => {
  const map = new Map()
  for (const group of props.groups) {
    map.set(group.id, sortedMembers(group).map((member, index) => {
      const model = modelByID.value.get(member.model_id)
      const health = memberHealth(member)
      return {
        member,
        index,
        model,
        health,
        performance: memberPerformance(member),
        healthMeta: healthMeta(model),
        concurrencyText: concurrencyText(member),
        openUntil: formatOpenUntil(health.open_until || health.latency_degraded_until),
      }
    }))
  }
  return map
})
</script>

<template>
  <div class="space-y-2">
    <div class="ui-card flex min-h-[60px] flex-wrap items-center gap-y-2 overflow-hidden px-3 py-2">
      <div class="grid w-full min-w-0 grid-cols-1 divide-y divide-[#e8edf3] sm:flex-1 sm:grid-cols-3 sm:divide-x sm:divide-y-0">
        <div class="flex min-w-[124px] items-center gap-2.5 px-3 first:pl-1">
          <span class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-[#dbeafe] bg-[#eff6ff] text-[#2563eb]">
            <IconModels class="h-4 w-4" />
          </span>
          <span class="min-w-0">
            <span class="block text-[10px] leading-3.5 text-[#64748b]">分组总数</span>
            <strong class="mt-0.5 block text-[19px] font-semibold leading-5 tabular-nums text-[#111827]">{{ groups.length }}</strong>
          </span>
        </div>
        <div class="flex min-w-[124px] items-center gap-2.5 px-3">
          <span class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-[#d1fae5] bg-[#ecfdf5] text-[#059669]">
            <IconCheck class="h-4 w-4" />
          </span>
          <span class="min-w-0">
            <span class="block text-[10px] leading-3.5 text-[#64748b]">已启用</span>
            <strong class="mt-0.5 block text-[19px] font-semibold leading-5 tabular-nums text-[#111827]">{{ groups.filter((group) => group.enabled !== false).length }}</strong>
          </span>
        </div>
        <div class="flex min-w-[144px] items-center gap-2.5 px-3">
          <span class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-[#ccfbf1] bg-[#f0fdfa] text-[#0f766e]">
            <IconUsage class="h-4 w-4" />
          </span>
          <span class="min-w-0">
            <span class="block text-[10px] leading-3.5 text-[#64748b]">已分组模型</span>
            <strong class="mt-0.5 block text-[19px] font-semibold leading-5 tabular-nums text-[#111827]">{{ groupedModelIDs.size }}</strong>
          </span>
        </div>
      </div>
      <div class="ml-auto flex w-full shrink-0 items-center justify-end gap-2 border-t border-[#e8edf3] pt-2 sm:w-auto sm:border-l sm:border-t-0 sm:pl-3 sm:pt-0">
        <button
          v-if="groups.length"
          class="h-8 ui-control px-3 text-[12px] font-medium text-[#475569] hover:bg-[#f8fafc]"
          @click="toggleAllGroups"
        >
          {{ allGroupsExpanded ? '全部收起' : '全部展开' }}
        </button>
        <button class="inline-flex h-8 items-center gap-1.5 ui-primary-button px-3.5 text-[12px] font-semibold" @click="openCreate">
          <IconPlus class="h-3.5 w-3.5" />
          新建分组
        </button>
      </div>
    </div>

    <div v-if="groups.length" class="space-y-1.5">
      <section v-for="group in groups" :key="group.id" class="overflow-hidden ui-card">
        <div class="flex items-center gap-2.5 px-3 py-2.5 transition-colors" :class="isExpanded(group.id) ? 'bg-white' : 'bg-[#f8fafc]'">
          <button
            class="flex h-7 w-7 shrink-0 items-center justify-center rounded text-[#64748b] transition hover:bg-white hover:text-[#111827]"
            :title="isExpanded(group.id) ? '收起候选模型' : '展开候选模型'"
            :aria-label="isExpanded(group.id) ? '收起候选模型' : '展开候选模型'"
            :aria-expanded="isExpanded(group.id)"
            @click="toggleGroup(group.id)"
          >
            <IconArrowRight class="h-3.5 w-3.5 transition-transform" :class="isExpanded(group.id) ? 'rotate-90' : ''" />
          </button>

          <button class="grid min-w-0 flex-1 gap-2 text-left lg:grid-cols-[minmax(220px,1fr)_auto] lg:items-center" :aria-expanded="isExpanded(group.id)" @click="toggleGroup(group.id)">
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <span class="h-2 w-2 shrink-0 rounded-full" :class="group.enabled !== false ? 'bg-[#10b981]' : 'bg-[#94a3b8]'" aria-hidden="true"></span>
                <h3 class="truncate text-[14px] font-semibold text-[#111827]">{{ group.name }}</h3>
                <span class="rounded px-1.5 py-0.5 text-[10px] font-medium" :class="group.enabled !== false ? 'bg-[#ecfdf5] text-[#059669]' : 'bg-[#f3f4f6] text-[#64748b]'">
                  {{ group.enabled !== false ? '运行中' : '已停用' }}
                </span>
              </div>
              <p v-if="group.description" class="mt-1 truncate text-[11px] text-[#64748b]" :title="group.description">{{ group.description }}</p>
              <div class="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-[10px] text-[#64748b]">
                <span><strong class="font-semibold text-[#334155]">{{ groupSummariesByID.get(group.id).total }}</strong> 个候选</span>
                <span><strong class="font-semibold text-[#059669]">{{ groupSummariesByID.get(group.id).available }}</strong> 个可调度</span>
                <span v-if="groupSummariesByID.get(group.id).open"><strong class="font-semibold text-[#dc2626]">{{ groupSummariesByID.get(group.id).open }}</strong> 个熔断</span>
                <span v-if="groupSummariesByID.get(group.id).avoided"><strong class="font-semibold text-[#c2410c]">{{ groupSummariesByID.get(group.id).avoided }}</strong> 个回避</span>
              </div>
            </div>
            <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-[10px] text-[#64748b] lg:justify-end">
              <span>模型并发 <strong class="font-semibold tabular-nums text-[#111827]">{{ groupConcurrencyText(groupSummariesByID.get(group.id)) }}</strong></span>
              <span>会话 <strong class="font-semibold tabular-nums text-[#2563eb]">{{ groupSummariesByID.get(group.id).sessions }}</strong></span>
              <span title="流式响应按首内容、非流式响应按首字节评估">首响应慢阈值 <strong class="font-semibold tabular-nums text-[#111827]">{{ formatLatency(group.first_byte_timeout_ms ?? 10000) }}</strong></span>
              <span>最多尝试 <strong class="font-semibold tabular-nums text-[#111827]">{{ group.max_attempts ?? 4 }}</strong> 次</span>
            </div>
          </button>

          <div class="flex shrink-0 items-center gap-1.5">
            <button class="flex h-7 w-7 items-center justify-center ui-control text-[#64748b] hover:bg-white hover:text-[#111827]" title="编辑分组" aria-label="编辑分组" @click.stop="openEdit(group)">
              <IconEdit class="h-3.5 w-3.5" />
            </button>
            <button class="flex h-7 w-7 items-center justify-center ui-control text-[#64748b] hover:border-[#fecaca] hover:bg-[#fef2f2] hover:text-[#dc2626]" title="删除分组" aria-label="删除分组" @click.stop="deleteTarget = group">
              <IconTrash class="h-3.5 w-3.5" />
            </button>
          </div>
        </div>

        <div v-if="isExpanded(group.id)" class="border-t border-[#edf0f5]">
          <div class="grid grid-cols-1 divide-y divide-[#edf0f5] border-b border-[#edf0f5] bg-[#fbfcfe] sm:grid-cols-3 sm:divide-x sm:divide-y-0">
            <div class="px-3 py-2">
              <div class="text-[10px] font-medium uppercase text-[#94a3b8]" title="流式响应按首内容、非流式响应按首字节评估">首响应慢阈值</div>
              <div class="mt-0.5 text-[12px] font-semibold tabular-nums text-[#111827]">{{ formatLatency(group.first_byte_timeout_ms ?? 10000) }}</div>
            </div>
            <div class="px-3 py-2">
              <div class="text-[10px] font-medium uppercase text-[#94a3b8]">最多尝试次数</div>
              <div class="mt-0.5 text-[12px] font-semibold tabular-nums text-[#111827]">{{ group.max_attempts ?? 4 }} 次</div>
            </div>
            <div class="min-w-0 px-3 py-2">
              <div class="text-[10px] font-medium uppercase text-[#94a3b8]">候选耗尽回退</div>
              <div class="mt-0.5 truncate text-[12px] font-semibold text-[#111827]" :title="fallbackGroupName(group)">{{ fallbackGroupName(group) }}</div>
            </div>
          </div>

          <div v-if="group.members?.length" class="overflow-x-auto">
            <div class="min-w-[950px]">
              <div class="grid grid-cols-[minmax(52px,0.5fr)_minmax(145px,1.5fr)_minmax(120px,1.1fr)_minmax(104px,1fr)_minmax(80px,0.8fr)_minmax(82px,0.85fr)_minmax(82px,0.85fr)_minmax(118px,1.2fr)] items-center gap-2 border-b border-[#edf0f5] bg-white px-3 py-1.5 text-[10px] font-medium text-[#64748b]">
                <span>顺位</span>
                <span>模型身份</span>
                <span>Provider / 路由</span>
                <span>首响应 EWMA / P95</span>
                <span>健康状态</span>
                <span>模型全局并发 / 上限</span>
                <span>会话 / 回避</span>
                <span>恢复 / 解除时间</span>
              </div>
              <div v-for="row in memberRowsByGroupID.get(group.id)" :key="row.member.model_id" class="grid grid-cols-[minmax(52px,0.5fr)_minmax(145px,1.5fr)_minmax(120px,1.1fr)_minmax(104px,1fr)_minmax(80px,0.8fr)_minmax(82px,0.85fr)_minmax(82px,0.85fr)_minmax(118px,1.2fr)] items-center gap-2 border-b border-[#edf0f5] px-3 py-2 text-[11px] transition-colors last:border-b-0 hover:bg-[#fbfcfe]">
                <div class="flex items-center gap-1.5">
                  <span class="font-mono text-[#64748b]">#{{ Number(row.member.priority || 0) + 1 }}</span>
                    <span class="rounded px-1 py-0.5 text-[9px] font-medium" :class="row.index === 0 ? 'bg-[#eff6ff] text-[#2563eb]' : 'bg-[#f3f4f6] text-[#64748b]'">{{ memberRole(row.index) }}</span>
                </div>
                <div class="min-w-0">
                  <div class="flex items-center gap-2">
                    <span class="truncate font-medium text-[#111827]">{{ memberName(row.member) }}</span>
                    <span v-if="row.model?.enabled === false" class="shrink-0 rounded bg-[#f3f4f6] px-1.5 py-0.5 text-[10px] text-[#64748b]">已禁用</span>
                  </div>
                  <div class="mt-0.5 truncate font-mono text-[10px] text-[#94a3b8]" :title="row.model?.model_id || row.member.model_id">{{ row.model?.model_id || row.member.model_id }}</div>
                </div>
                <div class="min-w-0">
                  <div class="truncate font-medium uppercase text-[#334155]">{{ row.model?.provider || '-' }}</div>
                  <div class="truncate font-mono text-[9px] text-[#94a3b8]" :title="`${row.model?.base_url || ''}${row.model?.endpoint || ''}`">{{ row.model?.endpoint || '-' }}</div>
                </div>
                <div class="min-w-0">
                  <div class="flex items-center gap-1.5">
                    <span class="h-1.5 w-1.5 rounded-full" :class="row.performance.failed ? 'bg-[#ef4444]' : row.performance.hasMeasurement ? 'bg-[#10b981]' : 'bg-[#cbd5e1]'" aria-hidden="true"></span>
                    <span class="font-semibold tabular-nums" :class="row.performance.failed ? 'text-[#dc2626]' : row.performance.hasMeasurement ? 'text-[#111827]' : 'text-[#94a3b8]'">{{ row.performance.firstByte }}</span>
                  </div>
                  <div class="truncate text-[9px] tabular-nums text-[#64748b]" :title="row.performance.detail">{{ row.performance.detail }}</div>
                </div>
                <div class="min-w-0">
                  <span class="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] font-medium" :class="row.healthMeta.tone">
                    <span class="h-1.5 w-1.5 rounded-full" :class="row.healthMeta.dot" aria-hidden="true"></span>
                    {{ row.healthMeta.label }}
                  </span>
                  <div v-if="Number(row.health.consecutive_failures || 0) > 0" class="mt-1 text-[10px] tabular-nums text-[#c2410c]">连续失败 {{ Number(row.health.consecutive_failures || 0) }}</div>
                </div>
                <span class="font-medium tabular-nums text-[#475569]">{{ row.concurrencyText }}</span>
                <span class="tabular-nums text-[#64748b]">
                  <strong :class="Number(row.health.affinity_sessions || 0) > 0 ? 'font-semibold text-[#2563eb]' : 'font-normal'">{{ Number(row.health.affinity_sessions || 0) }}</strong>
                  /
                  <strong :class="Number(row.health.avoided_scopes || 0) > 0 ? 'font-semibold text-[#c2410c]' : 'font-normal'">{{ Number(row.health.avoided_scopes || 0) }}</strong>
                </span>
                <span class="truncate text-[11px] text-[#64748b]" :title="row.openUntil">{{ row.openUntil }}</span>
              </div>
            </div>
          </div>
          <div v-else class="px-4 py-8 text-center text-[12px] text-[#64748b]">暂无候选模型</div>
        </div>
      </section>
    </div>

    <section v-else class="flex flex-col items-center justify-center border border-dashed border-[#cbd5e1] bg-white py-16 text-[#64748b]">
      <IconModels class="mb-3 h-8 w-8" />
      <p class="text-[13px] font-medium text-[#475569]">暂无模型分组</p>
    </section>

    <Teleport to="body">
      <div v-if="showEditor" class="fixed inset-0 z-50 flex items-center justify-center bg-black/30 px-4 backdrop-blur-sm" @click.self="closeEditor">
        <div class="flex max-h-[90vh] w-full max-w-[900px] flex-col overflow-hidden rounded-lg border border-[#e5e7eb] bg-white shadow-2xl">
          <div class="flex items-center justify-between border-b border-[#edf0f5] px-5 py-4">
            <div class="min-w-0">
              <h3 class="text-[16px] font-semibold text-[#111827]">{{ editingID ? '编辑分组' : '新建分组' }}</h3>
              <p v-if="editingID" class="mt-0.5 truncate text-[11px] text-[#94a3b8]">{{ groupName || '未命名分组' }}</p>
            </div>
            <button class="flex h-8 w-8 items-center justify-center rounded text-[#64748b] transition hover:bg-[#f8fafc] hover:text-[#111827]" title="关闭" aria-label="关闭" @click="closeEditor">
              <span class="text-[20px] font-light leading-none">×</span>
            </button>
          </div>

          <div class="min-h-0 flex-1 overflow-y-auto">
            <section class="px-5 py-4">
              <div class="mb-3 flex items-center justify-between gap-3">
                <div>
                  <h4 class="text-[12px] font-semibold text-[#111827]">分组信息</h4>
                  <p class="mt-0.5 text-[11px] text-[#94a3b8]">名称会作为 Kiro 模型列表中的分组入口显示</p>
                </div>
                <label class="inline-flex cursor-pointer items-center gap-2 text-[12px] font-medium text-[#475569]">
                  <input v-model="groupEnabled" type="checkbox" class="peer sr-only" />
                  <span class="relative h-5 w-9 rounded-full bg-[#cbd5e1] transition peer-checked:bg-[#2563eb] after:absolute after:left-0.5 after:top-0.5 after:h-4 after:w-4 after:rounded-full after:bg-white after:shadow-sm after:transition-transform peer-checked:after:translate-x-4"></span>
                  {{ groupEnabled ? '已启用' : '已停用' }}
                </label>
              </div>
              <label class="block text-[11px] font-medium text-[#475569]">
                分组名称
                <input v-model="groupName" class="mt-1.5 h-9 w-full ui-control px-3 text-[13px] text-[#111827] outline-none transition focus:border-[#93c5fd] focus:ring-2 focus:ring-[#eff6ff]" placeholder="例如 Claude 主力组" />
              </label>
              <label class="mt-3 block text-[11px] font-medium text-[#475569]">
                分组说明
                <textarea v-model="groupDescription" maxlength="500" rows="2" class="mt-1.5 w-full resize-none ui-control px-3 py-2 text-[12px] text-[#111827] outline-none transition focus:border-[#93c5fd] focus:ring-2 focus:ring-[#eff6ff]" placeholder="说明该组的用途、模型范围或调度定位"></textarea>
                <span class="mt-1 block text-right font-normal tabular-nums text-[#94a3b8]">{{ groupDescription.length }} / 500</span>
              </label>
            </section>

            <section class="border-t border-[#edf0f5] px-5 py-4">
              <div class="mb-3">
                <h4 class="text-[12px] font-semibold text-[#111827]">调度策略</h4>
                <p class="mt-0.5 text-[11px] text-[#94a3b8]">该配置对当前分组的所有候选生效</p>
              </div>
              <div class="grid gap-3 sm:grid-cols-3">
              <label class="block text-[11px] font-medium text-[#475569]">
                首字慢阈值（ms）
                <input v-model.number="firstByteTimeoutMs" type="number" min="10000" max="300000" step="1000" class="mt-1.5 h-9 w-full ui-control bg-white px-3 text-[12px] text-[#111827] outline-none transition focus:border-[#93c5fd] focus:ring-2 focus:ring-[#eff6ff]" />
                <span class="mt-1 block font-normal text-[#94a3b8]">软阈值 10,000-300,000；健康候选通常观察约 2 倍，单一图片候选至少 45 秒</span>
              </label>
              <label class="block text-[11px] font-medium text-[#475569]">
                最多尝试次数
                <input v-model.number="maxAttempts" type="number" min="1" max="16" class="mt-1.5 h-9 w-full ui-control bg-white px-3 text-[12px] text-[#111827] outline-none transition focus:border-[#93c5fd] focus:ring-2 focus:ring-[#eff6ff]" />
                <span class="mt-1 block font-normal text-[#94a3b8]">每个候选最多一次，实际不超过当前组可用成员数</span>
              </label>
              <label class="block text-[11px] font-medium text-[#475569]">
                候选耗尽回退组
                <select v-model="fallbackGroupID" class="mt-1.5 h-9 w-full ui-control bg-white px-2 text-[12px] text-[#111827] outline-none transition focus:border-[#93c5fd] focus:ring-2 focus:ring-[#eff6ff]">
                  <option value="">不使用回退组</option>
                  <option v-for="group in groups.filter((item) => item.id !== editingID)" :key="group.id" :value="group.id" :disabled="group.enabled === false">{{ group.name }}{{ group.enabled === false ? '（已停用）' : '' }}</option>
                </select>
                <span class="mt-1 block font-normal text-[#94a3b8]">当前组耗尽后继续调度</span>
              </label>
              </div>
            </section>

            <section class="border-t border-[#edf0f5] px-5 py-4">
              <div class="mb-3 flex flex-wrap items-end justify-between gap-2">
                <div>
                  <h4 class="text-[12px] font-semibold text-[#111827]">候选模型与全局并发</h4>
                  <p class="mt-0.5 text-[11px] text-[#94a3b8]">{{ selectedModels.length }} 个候选，按右侧顺序调度</p>
                </div>
                <span class="text-[11px] text-[#64748b]">模型全局并发上限为 0 时不限制</span>
              </div>
            <div class="grid gap-4 md:grid-cols-[minmax(0,0.9fr)_minmax(0,1.1fr)]">
              <div>
                <div class="mb-2 text-[11px] font-medium text-[#64748b]">可选模型</div>
                <div class="max-h-[320px] overflow-y-auto rounded border border-[#e5e7eb]">
                  <label v-for="model in availableModels" :key="model.id" class="flex cursor-pointer items-center gap-2 border-b border-[#edf0f5] px-3 py-2.5 last:border-b-0 hover:bg-[#f8fafc]">
                    <input type="checkbox" class="h-4 w-4 accent-[#1677ff]" :checked="selectedModelIDs.includes(model.id)" @change="toggleMember(model.id)" />
                    <span class="min-w-0">
                      <span class="block truncate text-[12px] font-medium text-[#111827]">{{ model.name || model.model_id }}</span>
                      <span class="block truncate text-[10px] text-[#64748b]">{{ model.provider }} · {{ model.model_id }}</span>
                    </span>
                  </label>
                  <div v-if="availableModels.length === 0" class="px-3 py-8 text-center text-[12px] text-[#94a3b8]">没有可加入的模型</div>
                </div>
              </div>

              <div>
                <div class="mb-2 grid grid-cols-[28px_minmax(0,1fr)_96px_64px] items-center gap-2 text-[11px] font-medium text-[#64748b]">
                  <span>顺位</span>
                  <span>模型身份</span>
                  <span>全局并发上限</span>
                  <span class="text-center">调整</span>
                </div>
                <div class="max-h-[320px] overflow-y-auto rounded border border-[#e5e7eb]">
                  <div v-for="(model, index) in selectedModels" :key="model.id" class="grid grid-cols-[28px_minmax(0,1fr)_96px_64px] items-center gap-2 border-b border-[#edf0f5] px-3 py-2.5 last:border-b-0">
                    <span class="font-mono text-[11px] text-[#64748b]">#{{ index + 1 }}</span>
                    <span class="min-w-0">
                      <span class="block truncate text-[12px] font-medium text-[#111827]">{{ model.name || model.model_id }}</span>
                      <span class="block truncate font-mono text-[10px] text-[#94a3b8]">{{ model.model_id }}</span>
                    </span>
                    <input
                      :value="memberConcurrency[model.id] || 0"
                      type="number"
                      min="0"
                      max="1000"
                      class="h-8 w-full ui-control px-2 text-[11px] tabular-nums text-[#111827] outline-none focus:border-[#93c5fd]"
                      title="模型全局最大并发，0 表示不限"
                      :aria-label="`${model.name || model.model_id} 的全局并发上限`"
                      @input="setMemberConcurrency(model.id, $event.target.value)"
                    />
                    <div class="flex items-center justify-end gap-1">
                      <button class="flex h-7 w-7 items-center justify-center ui-control text-[#64748b] hover:bg-[#f8fafc] disabled:opacity-30" title="上移" aria-label="上移" :disabled="index === 0" @click="moveMember(index, -1)">
                        <IconArrowRight class="h-3.5 w-3.5 -rotate-90" />
                      </button>
                      <button class="flex h-7 w-7 items-center justify-center ui-control text-[#64748b] hover:bg-[#f8fafc] disabled:opacity-30" title="下移" aria-label="下移" :disabled="index === selectedModels.length - 1" @click="moveMember(index, 1)">
                        <IconArrowRight class="h-3.5 w-3.5 rotate-90" />
                      </button>
                    </div>
                  </div>
                  <div v-if="selectedModels.length === 0" class="px-3 py-8 text-center text-[12px] text-[#94a3b8]">尚未选择模型</div>
                </div>
              </div>
            </div>
            </section>

            <div v-if="validationError" class="mx-5 mb-4 border border-[#fecaca] bg-[#fef2f2] px-3 py-2 text-[12px] text-[#dc2626]">{{ validationError }}</div>
          </div>

          <div class="flex justify-end gap-2 border-t border-[#edf0f5] px-5 py-3">
            <button class="h-9 ui-control px-4 text-[13px] text-[#475569]" :disabled="saving" @click="closeEditor">取消</button>
            <button class="h-9 ui-primary-button px-4 text-[13px] font-medium disabled:opacity-60" :disabled="saving" @click="saveGroup">{{ saving ? '保存中...' : '保存分组' }}</button>
          </div>
        </div>
      </div>

      <div v-if="deleteTarget" class="fixed inset-0 z-50 flex items-center justify-center bg-black/30 px-4 backdrop-blur-sm" @click.self="!deleting && (deleteTarget = null)">
        <div class="w-full max-w-[400px] rounded-lg border border-[#e5e7eb] bg-white p-5 shadow-2xl">
          <h3 class="text-[15px] font-semibold text-[#111827]">删除分组</h3>
          <p class="mt-2 text-[13px] leading-5 text-[#475569]">确定删除「{{ deleteTarget.name }}」？模型配置会保留。</p>
          <div class="mt-5 flex justify-end gap-2">
            <button class="h-9 ui-control px-4 text-[13px]" :disabled="deleting" @click="deleteTarget = null">取消</button>
            <button class="h-9 border border-[#dc2626] bg-[#dc2626] px-4 text-[13px] font-medium text-white disabled:opacity-60" :disabled="deleting" @click="emit('delete', { group: deleteTarget, done: () => { deleteTarget = null } })">{{ deleting ? '删除中...' : '删除' }}</button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>
