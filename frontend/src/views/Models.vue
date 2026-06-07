<script setup>
import { computed, ref, onMounted } from 'vue'
import { getModels, createModel, updateModel, deleteModel, testModel, refreshCodexModel } from '../api/models.js'
import ModelRow from '../components/models/ModelRow.vue'
import ModelForm from '../components/models/ModelForm.vue'
import IconPlus from '../components/icons/IconPlus.vue'
import IconModels from '../components/icons/IconModels.vue'

const models = ref([])
const showForm = ref(false)
const editingModel = ref(null)
const testResults = ref({})
const testingIds = ref({})
const refreshingCodexIds = ref({})
const togglingEnabledIds = ref({})
const saving = ref(false)
const loadingModels = ref(false)
const errorMessage = ref('')
const searchText = ref('')
const providerFilter = ref('all')
const statusFilter = ref('all')

const providerOptions = [
  { value: 'all', label: '全部 Provider' },
  { value: 'openai', label: 'OpenAI' },
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'gemini', label: 'Gemini' },
  { value: 'codex', label: 'Codex' }
]

const statusOptions = [
  { value: 'all', label: '全部状态' },
  { value: 'enabled', label: '已启用' },
  { value: 'disabled', label: '已禁用' },
  { value: 'success', label: '测试成功' },
  { value: 'failed', label: '测试失败' }
]

const primaryModelId = computed(() => models.value.find((m) => m.enabled !== false)?.id || '')

const filteredModels = computed(() => {
  const keyword = searchText.value.trim().toLowerCase()
  return models.value.filter((m) => {
    if (providerFilter.value !== 'all' && (m.provider || 'openai') !== providerFilter.value) return false
    if (statusFilter.value === 'enabled' && m.enabled === false) return false
    if (statusFilter.value === 'disabled' && m.enabled !== false) return false
    if (statusFilter.value === 'success' && !testResults.value[m.id]?.ok) return false
    if (statusFilter.value === 'failed' && testResults.value[m.id]?.ok !== false) return false
    if (!keyword) return true
    return [m.name, m.model_id, m.provider, m.base_url, m.endpoint, m.proxy_url, m.note]
      .filter(Boolean)
      .some((value) => String(value).toLowerCase().includes(keyword))
  })
})

const summary = computed(() => {
  const resultList = Object.values(testResults.value).filter(Boolean)
  const success = resultList.filter((r) => r.ok).length
  const failed = resultList.filter((r) => r.ok === false).length
  const speedRows = resultList.filter((r) => r.ok && Number(r.tokens_per_sec) > 0)
  const avgSpeed = speedRows.length
    ? speedRows.reduce((sum, r) => sum + Number(r.tokens_per_sec || 0), 0) / speedRows.length
    : 0
  return {
    total: models.value.length,
    enabled: models.value.filter((m) => m.enabled !== false).length,
    success,
    failed,
    avgSpeed
  }
})

const summaryItems = computed(() => [
  { label: '模型总数', value: summary.value.total, suffix: '', icon: 'cube', tone: 'text-[#111827] bg-[#f8fafc] border-[#e5e7eb]' },
  { label: '已启用', value: summary.value.enabled, suffix: '', icon: 'server', tone: 'text-[#2563eb] bg-[#eff6ff] border-[#bfdbfe]', dot: true },
  { label: '测试成功', value: summary.value.success, suffix: '', icon: 'check', tone: 'text-[#059669] bg-[#ecfdf5] border-[#a7f3d0]' },
  { label: '测试失败', value: summary.value.failed, suffix: '', icon: 'alert', tone: 'text-[#dc2626] bg-[#fef2f2] border-[#fecaca]' },
  { label: '平均响应速度', value: summary.value.avgSpeed ? summary.value.avgSpeed.toFixed(1) : '-', suffix: summary.value.avgSpeed ? 't/s' : '', icon: 'trend', tone: 'text-[#2563eb] bg-[#eff6ff] border-[#bfdbfe]' }
])

const TEST_RESULTS_STORAGE_KEY = 'kiro-proxy:model-test-results:v1'

function loadStoredTestResults() {
  try {
    const raw = localStorage.getItem(TEST_RESULTS_STORAGE_KEY)
    if (!raw) return
    const parsed = JSON.parse(raw)
    testResults.value = parsed && typeof parsed === 'object' ? parsed : {}
  } catch {
    testResults.value = {}
  }
}

function persistTestResults() {
  try {
    localStorage.setItem(TEST_RESULTS_STORAGE_KEY, JSON.stringify(testResults.value))
  } catch {
    // localStorage 不可用时只保留当前会话结果，不影响测试功能
  }
}

function pruneTestResults() {
  const ids = new Set(models.value.map((m) => m.id))
  const next = {}
  for (const [id, result] of Object.entries(testResults.value)) {
    if (ids.has(id)) next[id] = result
  }
  testResults.value = next
  persistTestResults()
}

async function loadModels() {
  if (loadingModels.value) return
  loadingModels.value = true
  errorMessage.value = ''
  try {
    const data = await getModels()
    models.value = Array.isArray(data) ? data : []
    pruneTestResults()
  } catch (e) {
    models.value = []
    errorMessage.value = e?.message || '加载模型失败'
  } finally {
    loadingModels.value = false
  }
}

function openAdd() {
  editingModel.value = null
  errorMessage.value = ''
  showForm.value = true
}

function openEdit(m) {
  editingModel.value = m
  errorMessage.value = ''
  showForm.value = true
}

async function handleSave(form) {
  saving.value = true
  errorMessage.value = ''
  try {
    if (editingModel.value?.id) {
      await updateModel(editingModel.value.id, form)
    } else {
      await createModel(form)
    }
    showForm.value = false
    await loadModels()
  } catch (e) {
    errorMessage.value = e?.message || '保存失败'
  } finally {
    saving.value = false
  }
}

async function handleDelete(m) {
  if (!confirm(`确定删除模型「${m.name || m.model_id}」？`)) return
  errorMessage.value = ''
  try {
    await deleteModel(m.id)
    await loadModels()
  } catch (e) {
    errorMessage.value = e?.message || '删除失败'
  }
}

async function handleToggleEnabled(m) {
  if (!m?.id || togglingEnabledIds.value[m.id]) return
  togglingEnabledIds.value[m.id] = true
  errorMessage.value = ''
  const nextEnabled = m.enabled === false
  try {
    await updateModel(m.id, { ...m, enabled: nextEnabled })
    m.enabled = nextEnabled
    await loadModels()
  } catch (e) {
    errorMessage.value = e?.message || '切换启用状态失败'
  } finally {
    togglingEnabledIds.value[m.id] = false
  }
}

async function handleTest(m) {
  testingIds.value[m.id] = true
  try {
    const result = await testModel(m.id, m.model_id)
    testResults.value[m.id] = { ...result, tested_at: new Date().toISOString() }
    persistTestResults()
  } catch (e) {
    testResults.value[m.id] = { ok: false, error: e.message, tested_at: new Date().toISOString() }
    persistTestResults()
  } finally {
    testingIds.value[m.id] = false
  }
}

async function handleRefreshCodex(m) {
  refreshingCodexIds.value[m.id] = true
  errorMessage.value = ''
  try {
    await refreshCodexModel(m.id)
    await loadModels()
  } catch (e) {
    errorMessage.value = e?.message || '刷新 Codex 信息失败'
  } finally {
    refreshingCodexIds.value[m.id] = false
  }
}

onMounted(() => {
  loadStoredTestResults()
  loadModels()
})
</script>

<template>
  <div class="h-full overflow-y-auto bg-[#fbfbfc] px-3 py-3">
    <div class="mx-auto max-w-[1320px] space-y-3">
      <header class="flex items-start justify-between gap-4 px-1 pt-1">
        <div class="min-w-0">
          <h1 class="text-[22px] font-semibold leading-7 tracking-tight text-[#111827]">模型配置</h1>
          <p class="mt-1 text-[12px] leading-4 text-[#6b7280]">配置 Kiro 可用的 AI 模型，每个模型对应一个上游 API 接口</p>
        </div>

        <div class="flex flex-wrap items-center justify-end gap-2">
          <div class="relative">
            <input
              v-model="searchText"
              class="h-9 w-[280px] rounded-lg border border-[#edf0f5] bg-white pl-9 pr-10 text-[12px] text-[#111827] shadow-sm outline-none transition focus:border-[#bfdbfe] focus:ring-2 focus:ring-[#eff6ff] lg:w-[330px]"
              placeholder="搜索模型名称、ID 或 Provider..."
            />
            <span class="absolute left-3 top-1/2 -translate-y-1/2 text-[#9ca3af]">⌕</span>
            <span class="absolute right-3 top-1/2 -translate-y-1/2 rounded border border-[#edf0f5] px-1.5 py-[1px] text-[10px] text-[#9ca3af]">Ctrl K</span>
          </div>

          <select
            v-model="providerFilter"
            class="h-9 rounded-lg border border-[#edf0f5] bg-white px-3 text-[12px] text-[#374151] shadow-sm outline-none"
          >
            <option v-for="option in providerOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
          </select>

          <select
            v-model="statusFilter"
            class="h-9 rounded-lg border border-[#edf0f5] bg-white px-3 text-[12px] text-[#374151] shadow-sm outline-none"
          >
            <option v-for="option in statusOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
          </select>

          <button
            class="flex h-9 w-9 items-center justify-center rounded-xl border border-[#e6ebf2] bg-white text-[#475569] shadow-[0_1px_2px_rgba(15,23,42,0.06)] transition hover:border-[#cbd5e1] hover:bg-[#f8fafc] hover:text-[#111827] disabled:cursor-not-allowed disabled:opacity-70"
            :title="loadingModels ? '刷新中...' : '刷新模型列表'"
            :disabled="loadingModels"
            @click="loadModels"
          >
            <svg
              class="h-4 w-4 transition-transform"
              :class="{ 'animate-spin': loadingModels }"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="1.9"
              stroke-linecap="round"
              stroke-linejoin="round"
            >
              <path d="M20 11a8.1 8.1 0 0 0-14.1-4.6L4 8.5" />
              <path d="M4 4.5v4h4" />
              <path d="M4 13a8.1 8.1 0 0 0 14.1 4.6L20 15.5" />
              <path d="M20 19.5v-4h-4" />
            </svg>
          </button>

          <button
            class="flex h-9 items-center gap-2 rounded-xl bg-[#1677ff] px-4 text-[13px] font-semibold leading-none text-white shadow-[0_6px_14px_rgba(22,119,255,0.24)] transition hover:bg-[#0958d9] hover:shadow-[0_8px_18px_rgba(22,119,255,0.28)] active:scale-[0.98]"
            @click="openAdd"
          >
            <IconPlus class="h-4 w-4" />
            <span>新增模型</span>
          </button>
        </div>
      </header>

      <section class="grid grid-cols-5 overflow-hidden rounded-xl border border-[#edf0f5] bg-white shadow-sm">
        <div
          v-for="(item, index) in summaryItems"
          :key="item.label"
          class="flex h-[72px] items-center gap-4 px-5"
          :class="index > 0 ? 'border-l border-[#edf0f5]' : ''"
        >
          <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border" :class="item.tone">
            <svg v-if="item.icon === 'cube'" class="h-[18px] w-[18px]" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
              <path d="M12 2.75 20 7.25v9.5l-8 4.5-8-4.5v-9.5l8-4.5Z" />
              <path d="m4.4 7.5 7.6 4.3 7.6-4.3" />
              <path d="M12 12v8.7" />
            </svg>
            <svg v-else-if="item.icon === 'server'" class="h-[18px] w-[18px]" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
              <rect x="5" y="4" width="14" height="6" rx="1.5" />
              <rect x="5" y="14" width="14" height="6" rx="1.5" />
              <path d="M8 7h.01M8 17h.01M11 7h5M11 17h5" />
            </svg>
            <svg v-else-if="item.icon === 'check'" class="h-[18px] w-[18px]" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round">
              <circle cx="12" cy="12" r="8.5" />
              <path d="m8.5 12.2 2.2 2.2 4.8-5" />
            </svg>
            <svg v-else-if="item.icon === 'alert'" class="h-[18px] w-[18px]" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round">
              <circle cx="12" cy="12" r="8.5" />
              <path d="M12 7.5v5.2" />
              <path d="M12 16.4h.01" />
            </svg>
            <svg v-else-if="item.icon === 'trend'" class="h-[18px] w-[18px]" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round">
              <path d="M4 17.5h16" />
              <path d="m6.5 14 3.2-3.2 2.4 2.4 5.4-6" />
              <path d="M14.8 7.2h2.7v2.7" />
            </svg>
          </div>
          <div>
            <div class="text-[11px] leading-4 text-[#64748b]">{{ item.label }}</div>
            <div class="mt-1 flex items-baseline gap-1">
              <span class="text-[22px] font-semibold leading-6 tabular-nums text-[#111827]">{{ item.value }}</span>
              <span v-if="item.dot" class="h-[5px] w-[5px] rounded-full bg-[#10b981]"></span>
              <span v-if="item.suffix" class="text-[12px] font-medium text-[#111827]">{{ item.suffix }}</span>
            </div>
          </div>
        </div>
      </section>

      <div v-if="errorMessage" class="rounded-lg border border-[#fecaca] bg-[#fef2f2] px-3 py-2 text-[12px] text-[#dc2626]">
        {{ errorMessage }}
      </div>

      <section v-if="models.length > 0" class="overflow-hidden rounded-xl border border-[#edf0f5] bg-white shadow-sm">
        <div class="overflow-x-auto">
          <div class="grid min-w-[1153px] grid-cols-[48px_205px_400px_225px_133px_142px] border-b border-[#edf0f5] bg-[#f8fafc] text-[13px] font-medium leading-5 text-[#64748b]">
            <div class="px-3 py-2.5 text-center"> </div>
            <div class="px-3 py-2.5">模型信息</div>
            <div class="px-3 py-2.5">连接配置</div>
            <div class="px-3 py-2.5 text-center">价格（USD / 1M tokens）</div>
            <div class="px-3 py-2.5">状态 / 测试结果</div>
            <div class="px-1 py-2.5 text-left">操作</div>
          </div>

          <ModelRow
            v-for="m in filteredModels"
            :key="m.id"
            :model="m"
            :primary="m.id === primaryModelId"
            :test-result="testResults[m.id]"
            :testing="!!testingIds[m.id]"
            :refreshing-codex="!!refreshingCodexIds[m.id]"
            :toggling-enabled="!!togglingEnabledIds[m.id]"
            @toggle-enabled="handleToggleEnabled(m)"
            @edit="openEdit(m)"
            @delete="handleDelete(m)"
            @test="handleTest(m)"
            @refresh-codex="handleRefreshCodex(m)"
          />

          <div v-if="filteredModels.length === 0" class="flex min-w-[1153px] flex-col items-center justify-center py-16 text-[#64748b]">
            <IconModels class="mb-3 h-8 w-8" />
            <p class="text-sm font-medium text-[#475569]">没有匹配当前筛选条件的模型</p>
            <p class="mt-1 text-xs">可以清空搜索或切换筛选条件后重试</p>
          </div>
        </div>
      </section>

      <section v-else class="flex flex-col items-center justify-center rounded-xl border border-dashed border-[#cbd5e1] bg-white py-20 text-[#64748b] shadow-sm">
        <IconModels class="mb-3 h-9 w-9" />
        <p class="text-sm font-medium text-[#475569]">暂无模型</p>
        <p class="mt-1 text-xs">点击「新增模型」开始配置 Kiro 可用的上游模型</p>
      </section>
    </div>

    <ModelForm
      :visible="showForm"
      :model="editingModel"
      :saving="saving"
      :error="errorMessage"
      @save="handleSave"
      @cancel="showForm = false"
    />
  </div>
</template>
