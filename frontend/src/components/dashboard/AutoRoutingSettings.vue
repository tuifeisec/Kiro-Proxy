<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { get } from '../../api/http.js'
import { getModelGroups, getModels } from '../../api/models.js'
import { getSettings, updateSettings } from '../../api/settings.js'
import { useToast } from '../../composables/useToast.js'
import {
  AUTO_DUTY_GROUPS,
  AUTO_DUTY_LAYOUTS,
  AUTO_DUTY_LAYOUT_LABELS,
  AUTO_DUTY_OPTIONS,
  AUTO_ROUTING_SETTINGS_KEY,
  autoTargetDefaultEffort,
  autoTargetEffortOptions,
  autoTargetGroupCandidates,
  applyDutyForLayout,
  emptyAutoDutyConfig,
  isAutoTargetSelectable,
  layoutMergeConflicts,
  migrateAutoDutyConfig,
  nodeIdentityComplete,
  observationIdentity,
  observationRecordKey,
  parseAutoDutyConfig,
  resolveAutoTarget,
  validateAutoDutyConfig,
} from '../../utils/autoDutyConfig.js'
import { getAutoDutyDescription, getAutoNodeReference } from '../../utils/autoNodeDescriptions.js'

const models = ref([])
const groups = ref([])
const config = ref(emptyAutoDutyConfig())
const snapshot = ref(null)
const rawConfig = ref('')
const legacyData = ref(null)
const migrationActive = ref(false)
const configError = ref('')
const loadError = ref('')
const observationsError = ref('')
const saveError = ref('')
const observations = ref([])
const selectedNodeDuties = ref({})
const selectedNodeModels = ref({})
const loading = ref(false)
const saving = ref(false)
const selectedMigrationLayout = ref('standard')
const pendingLayout = ref('')
const mergeResolutions = ref({})
const bindingResolutions = ref({})
const descriptionDialog = ref(null)
const descriptionCloseButton = ref(null)
const descriptionTitleId = useId()
const activeDescription = ref(null)
let descriptionTrigger = null
const { showToast } = useToast()
const emit = defineEmits(['busy', 'dirty', 'saved', 'cancel'])

const enabledModels = computed(() => models.value.filter((model) => model.enabled === true))
const selectableModels = computed(() => enabledModels.value.filter((model) => (
  resolveAutoTarget(model.id, models.value, groups.value).kind === 'model'
)))
const enabledGroups = computed(() => groups.value.filter((group) => (
  group.enabled === true &&
  resolveAutoTarget(group.id, models.value, groups.value).kind === 'group' &&
  autoTargetGroupCandidates(group.id, models.value, groups.value).length > 0
)))
const availableTargetCount = computed(() => selectableModels.value.length + enabledGroups.value.length)
const modelById = computed(() => new Map(models.value.map((model) => [model.id, model])))
const duties = computed(() => AUTO_DUTY_LAYOUTS[config.value.layout])
const activeNodeReference = computed(() => (
  activeDescription.value?.kind === 'node'
    ? getAutoNodeReference(activeDescription.value.observation?.node_id)
    : null
))
const activeObservedDuty = computed(() => {
  const selectedDuty = activeDescription.value?.observation?.selected_duty
  if (!selectedDuty) return null
  for (const layout of Object.values(AUTO_DUTY_LAYOUTS)) {
    const duty = layout.find((item) => item.key === selectedDuty || item.label === selectedDuty)
    if (duty) return { label: duty.label, description: getAutoDutyDescription(duty.key) }
  }
  return { label: selectedDuty, description: null }
})
const migrationMappings = computed(() => {
  const layout = migrationActive.value ? config.value.layout : selectedMigrationLayout.value
  return [
  ['run', 'orchestration'],
  ['plan', 'implementation_plan'],
  ['implement', 'implementation'],
  ['review', 'code_review'],
  ['finalize', layout === 'expanded' ? 'finalization' : 'routine_support'],
  ]
})
const migrationPreview = computed(() => migrationMappings.value.map(([stage, duty]) => {
  const layout = migrationActive.value ? config.value.layout : selectedMigrationLayout.value
  const label = AUTO_DUTY_LAYOUTS[layout].find((item) => item.key === duty)?.label || duty
  return `${stage} → ${label}：${legacyData.value?.stage_models?.[stage] || '留空'}`
}))
const formErrors = computed(() => validateAutoDutyConfig(config.value, models.value, groups.value))
const canSave = computed(() => (
  !loading.value && !saving.value && !loadError.value && !configError.value &&
  (!legacyData.value || migrationActive.value) && formErrors.value.length === 0 &&
  config.value.node_bindings.every((item) => duties.value.some((duty) => duty.key === item.duty))
))
const dirty = computed(() => (
  migrationActive.value || (snapshot.value !== null && JSON.stringify(config.value) !== snapshot.value)
))
const sortedObservations = computed(() => [...observations.value].sort((a, b) => (
  String(a.definition_id || '').localeCompare(String(b.definition_id || '')) ||
  String(a.node_path || '').localeCompare(String(b.node_path || ''))
)))
const unknownObservations = computed(() => sortedObservations.value.filter((item) => !item.selected_duty || item.unknown_reason))
const classifiedObservations = computed(() => sortedObservations.value.filter((item) => item.selected_duty && !item.unknown_reason))
const mergeConflicts = computed(() => pendingLayout.value ? layoutMergeConflicts(config.value) : [])
const incompatibleBindings = computed(() => {
  if (pendingLayout.value !== 'expanded' || config.value.layout !== 'standard') return []
  return config.value.node_bindings.flatMap((item, index) => {
    if (!AUTO_DUTY_GROUPS[item.duty]) return []
    return [{ index, item, children: AUTO_DUTY_GROUPS[item.duty] }]
  })
})

watch(saving, (value) => emit('busy', value), { immediate: true })
watch(dirty, (value) => emit('dirty', value), { immediate: true })

function modelIdentity(model) {
  if (!model) return '模型不存在'
  return `模型：${model.model_id || model.id} · ${model.id} · ${model.provider || 'openai'}`
}

function groupIdentity(group) {
  if (!group) return '分组不存在'
  return `分组：${group.name || group.id} · ${group.id}`
}

function targetIdentity(targetId) {
  const target = resolveAutoTarget(targetId, models.value, groups.value)
  if (target.kind === 'model') return modelIdentity(target.model)
  if (target.kind === 'group') return groupIdentity(target.group)
  if (target.kind === 'collision') return `${targetId}（模型/分组 ID 冲突）`
  return `${targetId}（配置不存在）`
}

function targetTitle(target) {
  return target?.model_id ? targetIdentity(target.model_id) : '未配置'
}

function optionsFor(currentId) {
  const options = selectableModels.value
    .map((model) => ({ id: model.id, label: modelIdentity(model), disabled: false }))
  for (const group of enabledGroups.value) {
    if (resolveAutoTarget(group.id, models.value, groups.value).kind === 'group') {
      options.push({ id: group.id, label: groupIdentity(group), disabled: false })
    }
  }
  if (currentId && !options.some((option) => option.id === currentId)) {
    const target = resolveAutoTarget(currentId, models.value, groups.value)
    let reason = '配置不存在，保留现有值'
    if (target.kind === 'collision') reason = '模型/分组 ID 冲突，保留现有值'
    else if (target.kind === 'model') reason = '模型已停用，保留现有值'
    else if (target.kind === 'group') {
      reason = target.group.enabled !== true
        ? '分组已停用，保留现有值'
        : autoTargetGroupCandidates(currentId, models.value, groups.value).length === 0
          ? '分组没有启用候选，保留现有值'
          : '分组不可用，保留现有值'
    }
    options.unshift({
      id: currentId,
      label: `${targetIdentity(currentId)}（${reason}）`,
      disabled: true,
    })
  }
  return options
}

function effortOptions(modelId, currentEffort) {
  const allowed = autoTargetEffortOptions(modelId, models.value, groups.value)
  if (currentEffort && !allowed.includes(currentEffort)) return [...allowed, currentEffort]
  return allowed
}

function changeTarget(target, modelId) {
  target.model_id = modelId
  target.reasoning_effort = autoTargetDefaultEffort(modelId, models.value, groups.value)
}

function updateConfigTarget(target, modelId) {
  changeTarget(target, modelId)
}

function describeEffort(value) {
  return value || '上游默认'
}

function currentLayoutBindingsAreValid() {
  return config.value.node_bindings.every((item) => duties.value.some((duty) => duty.key === item.duty))
}

function requestLayoutChange(layout) {
  if (layout === config.value.layout) {
    pendingLayout.value = ''
    return
  }
  pendingLayout.value = layout
  mergeResolutions.value = {}
  bindingResolutions.value = {}
  const conflicts = layout === 'standard' ? layoutMergeConflicts(config.value) : []
  const needsBindingChoices = layout === 'expanded' && config.value.layout === 'standard' && config.value.node_bindings.some((item) => AUTO_DUTY_GROUPS[item.duty])
  if (!conflicts.length && !needsBindingChoices) applyLayoutChange(layout)
}

function applyLayoutChange(layout) {
  if (Object.values(mergeResolutions.value).includes('block')) {
    pendingLayout.value = ''
    return
  }
  const resolutions = Object.fromEntries(Object.entries(mergeResolutions.value).map(([combined, choice]) => {
    const children = Object.keys(AUTO_DUTY_GROUPS[combined] || {})
    return [combined, choice === 'keepFirst' ? children[0] : choice === 'keepSecond' ? children[1] : choice]
  }))
  for (const [index, duty] of Object.entries(bindingResolutions.value)) resolutions[`binding:${index}`] = duty
  const next = applyDutyForLayout(config.value, layout, resolutions)
  config.value = next
  pendingLayout.value = ''
}

function canApplyLayout() {
  if (!pendingLayout.value) return false
  if (mergeConflicts.value.some((conflict) => !['keepFirst', 'keepSecond', 'block'].includes(mergeResolutions.value[conflict.combined]))) return false
  if (incompatibleBindings.value.some((entry) => !Object.hasOwn(entry.children, bindingResolutions.value[entry.index]))) return false
  return true
}

function beginMigration() {
  if (!legacyData.value) return
  config.value = migrateAutoDutyConfig(legacyData.value, selectedMigrationLayout.value)
  migrationActive.value = true
  snapshot.value = null
}

function fillUnconfiguredFromDefault() {
  const defaultTargetId = config.value.default_model_id
  if (!isAutoTargetSelectable(defaultTargetId, models.value, groups.value)) return
  for (const duty of duties.value) {
    const target = config.value.duty_models[duty.key]
    if (!target.model_id) changeTarget(target, defaultTargetId)
  }
}

function setObservedDuty(observation, duty) {
  selectedNodeDuties.value[observationIdentity(observation)] = duty
}

function setObservedModel(observation, modelId) {
  selectedNodeModels.value[observationIdentity(observation)] = {
    model_id: modelId,
    reasoning_effort: autoTargetDefaultEffort(modelId, models.value, groups.value),
  }
}

function setObservedEffort(observation, reasoningEffort) {
  const key = observationIdentity(observation)
  if (selectedNodeModels.value[key]) selectedNodeModels.value[key].reasoning_effort = reasoningEffort
}

function stableObservationLabel(observation) {
  return `${observation.definition_id}@${observation.definition_revision} · ${observation.node_path}`
}

function openDutyDescription(duty, trigger) {
  descriptionTrigger = trigger
  restoreDescriptionFocus = true
  activeDescription.value = { kind: 'duty', duty }
  nextTick(() => {
    if (!descriptionDialog.value?.open) descriptionDialog.value?.showModal()
    descriptionCloseButton.value?.focus()
  })
}

function openNodeDescription(observation, trigger) {
  descriptionTrigger = trigger
  restoreDescriptionFocus = true
  activeDescription.value = { kind: 'node', observation }
  nextTick(() => {
    if (!descriptionDialog.value?.open) descriptionDialog.value?.showModal()
    descriptionCloseButton.value?.focus()
  })
}

let restoreDescriptionFocus = true

function finishDescriptionDialogClose() {
  activeDescription.value = null
  const trigger = descriptionTrigger
  descriptionTrigger = null
  if (restoreDescriptionFocus && trigger?.isConnected) trigger.focus({ preventScroll: true })
  restoreDescriptionFocus = true
}

function closeDescriptionDialog(restoreFocus = true) {
  restoreDescriptionFocus = restoreFocus
  if (descriptionDialog.value?.open) {
    descriptionDialog.value.close()
    return
  }
  finishDescriptionDialogClose()
}

function handleDescriptionDialogClose() {
  if (descriptionDialog.value?.open) return
  finishDescriptionDialogClose()
}

function observedTargetLabel(observation) {
  if (observation.target_group_id) {
    const group = groups.value.find((item) => item.id === observation.target_group_id)
    const name = group?.name || observation.target_group_name || observation.target_group_id
    const model = modelById.value.get(observation.model_config_id)
    const actual = model
      ? `，实际成员 ${model.model_id || model.id}`
      : observation.outbound_model
        ? `，实际出站 ${observation.outbound_model}`
        : ''
    return `分组：${name} · ${observation.target_group_id}${actual}`
  }
  const model = modelById.value.get(observation.model_config_id)
  if (model) return modelIdentity(model)
  return observation.outbound_model || observation.model_config_id || '未知'
}

function addNodeBinding(observation) {
  if (!nodeIdentityComplete(observation)) return
  const duty = selectedNodeDuties.value[observationIdentity(observation)]
  if (!duty || !duties.value.some((item) => item.key === duty)) return
  const item = { definition_id: observation.definition_id, definition_revision: observation.definition_revision, node_path: observation.node_path, duty }
  const key = observationIdentity(item)
  config.value.node_bindings = [...config.value.node_bindings.filter((existing) => observationIdentity(existing) !== key), item]
}

function addNodeOverride(observation) {
  if (!nodeIdentityComplete(observation)) return
  const target = selectedNodeModels.value[observationIdentity(observation)]
  if (!target?.model_id) return
  const item = {
    definition_id: observation.definition_id,
    definition_revision: observation.definition_revision,
    node_path: observation.node_path,
    ...target,
  }
  const key = observationIdentity(item)
  config.value.node_overrides = [...config.value.node_overrides.filter((existing) => observationIdentity(existing) !== key), item]
}

function removeNodeBinding(item) {
  const key = observationIdentity(item)
  config.value.node_bindings = config.value.node_bindings.filter((existing) => observationIdentity(existing) !== key)
}

function removeNodeOverride(item) {
  const key = observationIdentity(item)
  config.value.node_overrides = config.value.node_overrides.filter((existing) => observationIdentity(existing) !== key)
}

function initializeObservationChoices() {
  selectedNodeDuties.value = Object.fromEntries(config.value.node_bindings.map((item) => [observationIdentity(item), item.duty]))
  selectedNodeModels.value = Object.fromEntries(config.value.node_overrides.map((item) => [observationIdentity(item), { model_id: item.model_id, reasoning_effort: item.reasoning_effort }]))
}

async function load() {
  loading.value = true
  snapshot.value = null
  rawConfig.value = ''
  configError.value = ''
  loadError.value = ''
  observationsError.value = ''
  try {
    const [modelsData, groupsData, settings] = await Promise.all([getModels(), getModelGroups(), getSettings()])
    models.value = Array.isArray(modelsData) ? modelsData : []
    groups.value = Array.isArray(groupsData) ? groupsData : []
    rawConfig.value = settings?.[AUTO_ROUTING_SETTINGS_KEY] ?? ''
    try {
      const parsed = parseAutoDutyConfig(rawConfig.value)
      legacyData.value = parsed.legacy ? parsed.legacyData : null
      if (parsed.legacy) {
        config.value = emptyAutoDutyConfig()
        selectedMigrationLayout.value = 'standard'
      } else {
        config.value = parsed.config
        snapshot.value = JSON.stringify(config.value)
      }
      initializeObservationChoices()
    } catch (error) {
      configError.value = error.message || 'Auto 模型配置无法解析。'
      showToast(configError.value, { tone: 'error' })
    }
  } catch (error) {
    loadError.value = error?.message || '设置读取失败，无法安全保存。'
    console.error('加载 Auto 模型配置失败', error)
    showToast(loadError.value, { tone: 'error' })
  }
  try {
    const data = await get('/api/auto-routing/observations')
    observations.value = Array.isArray(data) ? data : Array.isArray(data?.data) ? data.data : []
  } catch (error) {
    observationsError.value = error?.message || '节点观察列表读取失败。'
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!canSave.value) return
  saving.value = true
  saveError.value = ''
  try {
    const payload = {
      version: 2,
      mode: 'duty_fixed',
      enabled: config.value.enabled,
      layout: config.value.layout,
      default_model_id: config.value.default_model_id,
      duty_models: Object.fromEntries(duties.value.map(({ key }) => [key, { ...config.value.duty_models[key] }])),
      node_bindings: config.value.node_bindings.map((item) => ({ ...item })),
      node_overrides: config.value.node_overrides.map((item) => ({ ...item })),
    }
    await updateSettings({ [AUTO_ROUTING_SETTINGS_KEY]: JSON.stringify(payload) })
    config.value = payload
    snapshot.value = JSON.stringify(payload)
    rawConfig.value = JSON.stringify(payload)
    legacyData.value = null
    migrationActive.value = false
    showToast('Auto 职责路由配置已保存', { tone: 'success' })
    emit('saved')
  } catch (error) {
    console.error('保存 Auto 职责路由配置失败', error)
    saveError.value = error?.message || '保存 Auto 职责路由配置失败'
    showToast(saveError.value, { tone: 'error' })
  } finally {
    saving.value = false
  }
}

onMounted(load)
onBeforeUnmount(() => closeDescriptionDialog(false))
</script>

<template>
  <section class="flex min-h-0 flex-1 flex-col">
    <div class="min-h-0 flex-1 overflow-y-auto p-4">
      <p class="mb-3 text-[12px] leading-5 text-ink-4">Auto 按固定职责选择模型配置或现有分组，并应用该职责固定的思考等级。普通子 Agent 继承当前 Auto 目标；显式声明的职责节点按自身绑定选择。选择分组时沿用该分组现有成员与后备组调度。</p>
      <div v-if="configError" class="mb-3 rounded-md border border-danger-tint-line bg-danger-tint px-3 py-2 text-[12px] leading-5 text-danger" role="alert">
        <p>{{ configError }}</p>
        <details class="mt-2"><summary class="cursor-pointer">查看保留的原始配置</summary><pre class="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-all font-mono text-[10px]">{{ rawConfig }}</pre></details>
      </div>
      <div v-if="loadError" class="mb-3 rounded-md border border-danger-tint-line bg-danger-tint px-3 py-2 text-[12px] leading-5 text-danger" role="alert">{{ loadError }}</div>
      <p v-if="loading" class="py-2 text-[12px] text-ink-4">正在加载设置与节点观察...</p>
      <div v-else-if="!configError && !loadError" class="space-y-3">
        <fieldset class="min-w-0 space-y-3 border-0 p-0" :disabled="saving">
          <section v-if="legacyData && !migrationActive" class="rounded-lg border border-warning-tint-line bg-warning-tint p-3 text-[11px] leading-5 text-warning-fg">
            <h4 class="font-semibold">发现旧 Auto 配置，保存前需要明确迁移</h4>
            <p class="mt-1">旧配置保持原样。下方只预览约定的五项映射，其余职责会留空；迁移后可逐项选择，或明确使用默认目标填充空项。</p>
            <div class="mt-2 grid gap-1 sm:grid-cols-2">
              <label class="text-ink-2">目标布局
                <select v-model="selectedMigrationLayout" class="ui-control-h ui-control ml-2 px-2 text-[11px] text-ink">
                  <option value="standard">{{ AUTO_DUTY_LAYOUT_LABELS.standard }}</option>
                  <option value="expanded">{{ AUTO_DUTY_LAYOUT_LABELS.expanded }}</option>
                </select>
              </label>
              <span>旧 default_model_id：{{ legacyData.default_model_id || '未配置' }}</span>
            </div>
            <ul class="mt-2 list-inside list-disc font-mono text-[10px]">
              <li v-for="item in migrationPreview" :key="item">{{ item }}</li>
            </ul>
            <button type="button" class="mt-3 rounded border border-warning-tint-line bg-surface px-3 py-1.5 text-[11px] font-medium text-ink-2" @click="beginMigration">开始迁移预览</button>
          </section>

          <template v-if="!legacyData || migrationActive">
            <div v-if="migrationActive" class="rounded-md border border-warning-tint-line bg-warning-tint px-3 py-2 text-[11px] leading-5 text-warning-fg">
              正在编辑迁移预览，原值仍保留在服务端；只有点击保存后才写入 v2。
            </div>
            <div class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-line-2 bg-surface-2 px-3 py-2.5">
              <div>
                <div class="text-[12px] font-medium text-ink">职责布局</div>
                <p class="mt-0.5 text-[11px] leading-4 text-ink-4">布局切换只处理被拆分/合并的职责；合并目标不同会要求明确选择。</p>
              </div>
              <select :value="config.layout" class="ui-control-h ui-control px-2 text-[12px] text-ink" @change="requestLayoutChange($event.target.value)">
                <option value="standard">{{ AUTO_DUTY_LAYOUT_LABELS.standard }}</option>
                <option value="expanded">{{ AUTO_DUTY_LAYOUT_LABELS.expanded }}</option>
              </select>
            </div>

            <section v-if="pendingLayout" class="space-y-3 rounded-lg border border-warning-tint-line bg-warning-tint p-3 text-[11px] leading-5 text-warning-fg">
              <p>切换到{{ AUTO_DUTY_LAYOUT_LABELS[pendingLayout] }}前，请确认下面的绑定转换。冲突可选择保留的值，或阻止本次切换。</p>
              <div v-for="conflict in mergeConflicts" :key="conflict.combined" class="grid gap-2 rounded border border-warning-tint-line bg-surface p-2 sm:grid-cols-[1fr_220px]">
                <span>{{ AUTO_DUTY_GROUPS[conflict.combined] && Object.values(AUTO_DUTY_GROUPS[conflict.combined]).join(' / ') }}分别是 {{ targetTitle(conflict.firstTarget) }}（{{ describeEffort(conflict.firstTarget.reasoning_effort) }}）与 {{ targetTitle(conflict.secondTarget) }}（{{ describeEffort(conflict.secondTarget.reasoning_effort) }}）</span>
                <select v-model="mergeResolutions[conflict.combined]" class="ui-control-h ui-control px-2 text-ink">
                  <option value="">选择如何合并</option>
                  <option value="keepFirst">保留{{ conflict.children[conflict.first] }}</option>
                  <option value="keepSecond">保留{{ conflict.children[conflict.second] }}</option>
                  <option value="block">阻止切换</option>
                </select>
              </div>
              <div v-for="entry in incompatibleBindings" :key="entry.index" class="grid gap-2 rounded border border-warning-tint-line bg-surface p-2 sm:grid-cols-[1fr_220px]">
                <span>节点绑定 {{ stableObservationLabel(entry.item) }} 当前为 {{ AUTO_DUTY_OPTIONS.find((item) => item.key === entry.item.duty)?.label }}，请选择细分职责。</span>
                <select v-model="bindingResolutions[entry.index]" class="ui-control-h ui-control px-2 text-ink">
                  <option value="">选择细分职责</option>
                  <option v-for="(label, key) in entry.children" :key="key" :value="key">{{ label }}</option>
                </select>
              </div>
              <div class="flex gap-2">
                <button type="button" class="rounded border border-warning-tint-line bg-surface px-3 py-1.5 font-medium text-ink-2 disabled:opacity-50" :disabled="!canApplyLayout()" @click="applyLayoutChange(pendingLayout)">应用切换</button>
                <button type="button" class="rounded border border-warning-tint-line bg-surface px-3 py-1.5 text-ink-2" @click="pendingLayout = ''">取消</button>
              </div>
            </section>

            <div class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-line-2 bg-surface-2 px-3 py-2.5">
              <label class="flex items-center gap-2 text-[12px] font-medium text-ink">
                <input v-model="config.enabled" type="checkbox" class="accent-current" />启用 Auto 职责路由
              </label>
              <button type="button" class="rounded border border-line-2 bg-surface px-3 py-1.5 text-[11px] text-ink-2 disabled:opacity-50" :disabled="!config.default_model_id" @click="fillUnconfiguredFromDefault">用默认目标填充未配置职责</button>
            </div>

            <section class="rounded-lg border border-line-2 p-3">
              <h4 class="mb-2 text-[12px] font-semibold text-ink">固定默认目标</h4>
              <label for="auto-duty-default" class="mb-1 block text-[11px] text-ink-3">未匹配职责时使用此模型或分组</label>
              <select id="auto-duty-default" :value="config.default_model_id" class="ui-control-h ui-control w-full px-2.5 text-[12px] text-ink" @change="config.default_model_id = $event.target.value">
                <option value="">未配置</option>
                <option v-for="option in optionsFor(config.default_model_id)" :key="option.id" :value="option.id" :disabled="option.disabled">{{ option.label }}</option>
              </select>
            </section>

            <section class="rounded-lg border border-line-2 p-3">
              <h4 class="mb-1 text-[12px] font-semibold text-ink">职责固定目标（{{ duties.length }}）</h4>
              <p class="mb-3 text-[11px] leading-4 text-ink-4">目标由模型或分组 UUID 和独立思考等级组成；选择分组时沿用其成员与后备组调度。</p>
              <div class="space-y-2">
                <div v-for="duty in duties" :key="duty.key" class="grid gap-2 rounded-md border border-line-2 px-2.5 py-2 sm:grid-cols-[minmax(130px,0.8fr)_minmax(180px,1.5fr)_130px]">
                  <div class="min-w-0">
                    <div class="flex items-center justify-between gap-2">
                      <div class="text-[11px] font-medium text-ink-2">{{ duty.label }}</div>
                      <button type="button" class="shrink-0 rounded border border-line-2 bg-surface px-2 py-1 text-[10px] text-ink-2 hover:bg-hover" @click="openDutyDescription(duty, $event.currentTarget)">说明</button>
                    </div>
                    <div class="mt-0.5 truncate font-mono text-[9px] leading-4 text-ink-4" :title="duty.nodes.join(', ')">{{ duty.nodes.join(' · ') }}</div>
                  </div>
                  <select :value="config.duty_models[duty.key].model_id" class="ui-control-h ui-control min-w-0 px-2 text-[11px] text-ink" :aria-label="`${duty.label}模型或分组`" @change="updateConfigTarget(config.duty_models[duty.key], $event.target.value)">
                    <option value="">未配置</option>
                    <option v-for="option in optionsFor(config.duty_models[duty.key].model_id)" :key="option.id" :value="option.id" :disabled="option.disabled">{{ option.label }}</option>
                  </select>
                  <select v-model="config.duty_models[duty.key].reasoning_effort" class="ui-control-h ui-control px-2 text-[11px] text-ink" :aria-label="`${duty.label}思考等级`">
                    <option v-for="effort in effortOptions(config.duty_models[duty.key].model_id, config.duty_models[duty.key].reasoning_effort)" :key="effort" :value="effort">{{ describeEffort(effort) }}</option>
                  </select>
                </div>
              </div>
              <p v-if="availableTargetCount === 0" class="mt-2 text-[11px] text-warning-fg">当前没有已启用模型或有效分组可供选择。</p>
              <p class="mt-3 text-[10px] leading-4 text-ink-4">思考等级能力按本地 Provider 规则校验；OpenAI/Codex 的 xhigh、max 是否受上游具体模型支持仍由上游决定。留空表示固定使用上游默认等级。</p>
            </section>

            <section class="rounded-lg border border-line-2 p-3">
              <div class="flex flex-wrap items-start justify-between gap-2">
                <div><h4 class="text-[12px] font-semibold text-ink">已观察节点与未归类节点</h4><p class="mt-1 text-[11px] text-ink-4">列表来自当前代理进程内观察快照。只有 definition_id、definition_revision、node_path 齐全时才可保存绑定或覆盖。</p></div>
                <span class="text-[10px] text-ink-4">{{ observations.length }} 条</span>
              </div>
              <p v-if="observationsError" class="mt-2 text-[11px] text-danger" role="alert">{{ observationsError }}</p>
              <div v-if="!observationsError && observations.length" class="mt-3 space-y-2">
                <div v-for="observation in unknownObservations" :key="observationRecordKey(observation)" class="rounded-md border border-warning-tint-line bg-warning-tint p-2.5 text-[10px] leading-4 text-warning-fg">
                  <div class="flex flex-wrap items-center justify-between gap-2"><strong>未归类 / 路由未知</strong><div class="flex items-center gap-2"><span>原因：{{ observation.unknown_reason || '尚未选择职责' }}</span><button type="button" class="rounded border border-warning-tint-line bg-surface px-2 py-1 text-[10px] text-ink-2" @click="openNodeDescription(observation, $event.currentTarget)">节点说明</button></div></div>
                  <div class="mt-1 break-all font-mono">{{ observation.definition_id || 'definition_id 缺失' }}@{{ observation.definition_revision || 'revision 缺失' }} · {{ observation.node_path || observation.node_id || 'node_path 缺失' }}</div>
                  <div class="mt-1 text-ink-3">task_type={{ observation.task_type || '空' }} · source={{ observation.selection_source || '空' }} · target={{ observedTargetLabel(observation) }} · effort={{ observation.effective_effort || '上游默认' }} · {{ observation.count || 0 }} 次</div>
                  <div v-if="nodeIdentityComplete(observation)" class="mt-2 grid gap-2 sm:grid-cols-[1fr_1fr_110px_auto_auto]">
                    <select :value="selectedNodeDuties[observationIdentity(observation)] || ''" class="ui-control-h ui-control px-2 text-[10px] text-ink" @change="setObservedDuty(observation, $event.target.value)"><option value="">选择职责</option><option v-for="duty in duties" :key="duty.key" :value="duty.key">{{ duty.label }}</option></select>
                    <select :value="selectedNodeModels[observationIdentity(observation)]?.model_id || ''" class="ui-control-h ui-control px-2 text-[10px] text-ink" @change="setObservedModel(observation, $event.target.value)"><option value="">选择覆盖模型或分组</option><option v-for="option in optionsFor(selectedNodeModels[observationIdentity(observation)]?.model_id)" :key="option.id" :value="option.id" :disabled="option.disabled">{{ option.label }}</option></select>
                    <select :value="selectedNodeModels[observationIdentity(observation)]?.reasoning_effort || ''" class="ui-control-h ui-control px-2 text-[10px] text-ink" @change="setObservedEffort(observation, $event.target.value)"><option v-for="effort in effortOptions(selectedNodeModels[observationIdentity(observation)]?.model_id, selectedNodeModels[observationIdentity(observation)]?.reasoning_effort)" :key="effort" :value="effort">{{ describeEffort(effort) }}</option></select>
                    <button type="button" class="rounded border border-warning-tint-line bg-surface px-2 text-[10px] text-ink-2" @click="addNodeBinding(observation)">绑定职责</button>
                    <button type="button" class="rounded border border-warning-tint-line bg-surface px-2 text-[10px] text-ink-2" @click="addNodeOverride(observation)">固定覆盖</button>
                  </div>
                  <p v-else class="mt-2">缺少稳定身份字段，不能保存长期绑定或覆盖。</p>
                </div>
                <div v-for="observation in classifiedObservations" :key="observationRecordKey(observation)" class="rounded-md border border-line-2 p-2.5 text-[10px] leading-4 text-ink-3">
                  <div class="flex flex-wrap items-center justify-between gap-2"><strong class="text-ink-2">{{ observation.selected_duty }}</strong><div class="flex items-center gap-2"><span>source={{ observation.selection_source || '空' }} · lock={{ observation.lock_scope || '未知' }}</span><button type="button" class="rounded border border-line-2 bg-surface px-2 py-1 text-[10px] text-ink-2" @click="openNodeDescription(observation, $event.currentTarget)">节点说明</button></div></div>
                  <div class="mt-1 break-all font-mono">{{ observation.definition_id || 'definition_id 缺失' }}@{{ observation.definition_revision || 'revision 缺失' }} · {{ observation.node_path || observation.node_id || 'node_path 缺失' }}</div>
                  <div class="mt-1">task_type={{ observation.task_type || '空' }} · target={{ observedTargetLabel(observation) }} · effort={{ observation.effective_effort || '上游默认' }} · channel={{ observation.channel_identity || '未知' }} · {{ observation.count || 0 }} 次 · 最近 {{ observation.last_seen || '未知' }}</div>
                  <div v-if="nodeIdentityComplete(observation)" class="mt-2 grid gap-2 sm:grid-cols-[1fr_1fr_110px_auto_auto]">
                    <select :value="selectedNodeDuties[observationIdentity(observation)] || ''" class="ui-control-h ui-control px-2 text-[10px] text-ink" @change="setObservedDuty(observation, $event.target.value)"><option value="">选择职责</option><option v-for="duty in duties" :key="duty.key" :value="duty.key">{{ duty.label }}</option></select>
                    <select :value="selectedNodeModels[observationIdentity(observation)]?.model_id || ''" class="ui-control-h ui-control px-2 text-[10px] text-ink" @change="setObservedModel(observation, $event.target.value)"><option value="">选择覆盖模型或分组</option><option v-for="option in optionsFor(selectedNodeModels[observationIdentity(observation)]?.model_id)" :key="option.id" :value="option.id" :disabled="option.disabled">{{ option.label }}</option></select>
                    <select :value="selectedNodeModels[observationIdentity(observation)]?.reasoning_effort || ''" class="ui-control-h ui-control px-2 text-[10px] text-ink" @change="setObservedEffort(observation, $event.target.value)"><option v-for="effort in effortOptions(selectedNodeModels[observationIdentity(observation)]?.model_id, selectedNodeModels[observationIdentity(observation)]?.reasoning_effort)" :key="effort" :value="effort">{{ describeEffort(effort) }}</option></select>
                    <button type="button" class="rounded border border-line-2 bg-surface px-2 text-[10px] text-ink-2" @click="addNodeBinding(observation)">绑定职责</button>
                    <button type="button" class="rounded border border-line-2 bg-surface px-2 text-[10px] text-ink-2" @click="addNodeOverride(observation)">固定覆盖</button>
                  </div>
                </div>
              </div>
              <p v-else-if="!observationsError" class="mt-3 text-[11px] text-ink-4">当前进程没有已观察节点。</p>
              <div v-if="config.node_bindings.length" class="mt-3 border-t border-line-2 pt-3">
                <h5 class="mb-2 text-[11px] font-semibold text-ink-2">已保存职责绑定</h5>
                <div v-for="item in config.node_bindings" :key="observationIdentity(item)" class="flex flex-wrap items-center justify-between gap-2 py-1 text-[10px]">
                  <span class="break-all font-mono">{{ stableObservationLabel(item) }} → {{ duties.find((duty) => duty.key === item.duty)?.label || item.duty }}</span>
                  <button type="button" class="text-danger" @click="removeNodeBinding(item)">删除</button>
                </div>
              </div>
              <div v-if="config.node_overrides.length" class="mt-3 border-t border-line-2 pt-3">
                <h5 class="mb-2 text-[11px] font-semibold text-ink-2">已保存节点固定覆盖</h5>
                <div v-for="item in config.node_overrides" :key="observationIdentity(item)" class="flex flex-wrap items-center justify-between gap-2 py-1 text-[10px]">
                  <span class="break-all font-mono">{{ stableObservationLabel(item) }} → {{ targetTitle(item) }} / {{ describeEffort(item.reasoning_effort) }}</span>
                  <button type="button" class="text-danger" @click="removeNodeOverride(item)">删除</button>
                </div>
              </div>
            </section>

            <ul v-if="formErrors.length" class="space-y-1 rounded-md border border-warning-tint-line bg-warning-tint px-3 py-2 text-[11px] leading-4 text-warning-fg">
              <li v-for="error in formErrors" :key="error">{{ error }}</li>
            </ul>
          </template>
        </fieldset>
      </div>
      <div v-if="saveError" class="mt-3 rounded-md border border-danger-tint-line bg-danger-tint px-3 py-2 text-[12px] leading-5 text-danger" role="alert">{{ saveError }}</div>
    </div>
    <footer class="flex shrink-0 items-center justify-end gap-2 border-t border-line-2 px-4 py-3">
      <button type="button" class="h-9 rounded-md border border-line bg-surface px-5 text-xs text-ink-2 shadow-sm hover:bg-hover disabled:cursor-not-allowed disabled:opacity-50" :disabled="saving" @click="emit('cancel')">取消</button>
      <button type="button" class="h-9 rounded-md bg-primary px-5 text-xs font-medium text-white shadow-sm hover:bg-primary-hover disabled:cursor-not-allowed disabled:opacity-50" :disabled="!canSave" @click="save">
        {{ saving ? '保存中...' : '保存 Auto 职责配置' }}
      </button>
    </footer>
  </section>
  <dialog
    ref="descriptionDialog"
    class="m-auto w-[min(42rem,calc(100vw-1rem))] max-h-[calc(100dvh-1rem)] overflow-hidden rounded-xl border border-line-2 bg-surface p-0 text-ink shadow-xl backdrop:bg-black/50 sm:w-[min(42rem,calc(100vw-2rem))] sm:max-h-[calc(100dvh-2rem)]"
    :aria-labelledby="descriptionTitleId"
    aria-modal="true"
    @click.self="closeDescriptionDialog()"
    @cancel.prevent="closeDescriptionDialog()"
    @close="handleDescriptionDialogClose"
    @keydown.stop
  >
    <div class="flex max-h-[calc(100dvh-1rem)] flex-col sm:max-h-[calc(100dvh-2rem)]">
      <header class="flex shrink-0 items-start justify-between gap-4 border-b border-line-2 px-4 py-3 sm:px-5">
        <div>
          <h2 :id="descriptionTitleId" class="text-sm font-semibold text-ink">
            {{ activeDescription?.kind === 'duty' ? `${activeDescription.duty.label}职责说明` : '节点说明' }}
          </h2>
          <p v-if="activeDescription?.kind === 'duty'" class="mt-1 text-[11px] text-ink-4">{{ AUTO_DUTY_LAYOUT_LABELS[config.layout] }} · 只读说明</p>
          <p v-else class="mt-1 text-[11px] text-ink-4">只读观察信息；节点名称用途仅供参考</p>
        </div>
        <button ref="descriptionCloseButton" type="button" autofocus aria-label="关闭说明弹窗" class="shrink-0 rounded-md border border-line-2 bg-surface px-3 py-1.5 text-xs text-ink-2 hover:bg-hover" @click="closeDescriptionDialog()">关闭</button>
      </header>
      <div class="min-h-0 overflow-y-auto p-4 sm:p-5">
        <template v-if="activeDescription?.kind === 'duty'">
          <template v-if="getAutoDutyDescription(activeDescription.duty.key)">
            <div class="space-y-4 text-[12px] leading-5">
              <section>
                <h3 class="font-semibold text-ink">职责用途</h3>
                <p class="mt-1 text-ink-3">{{ getAutoDutyDescription(activeDescription.duty.key).purpose }}</p>
              </section>
              <div class="grid gap-4 sm:grid-cols-2">
                <section>
                  <h3 class="font-semibold text-ink">主要输入</h3>
                  <ul class="mt-1 list-disc space-y-1 pl-5 text-ink-3"><li v-for="item in getAutoDutyDescription(activeDescription.duty.key).inputs" :key="item">{{ item }}</li></ul>
                </section>
                <section>
                  <h3 class="font-semibold text-ink">主要产物</h3>
                  <ul class="mt-1 list-disc space-y-1 pl-5 text-ink-3"><li v-for="item in getAutoDutyDescription(activeDescription.duty.key).outputs" :key="item">{{ item }}</li></ul>
                </section>
              </div>
              <section>
                <h3 class="font-semibold text-ink">选择模型时关注的能力</h3>
                <ul class="mt-1 list-disc space-y-1 pl-5 text-ink-3"><li v-for="item in getAutoDutyDescription(activeDescription.duty.key).modelFocus" :key="item">{{ item }}</li></ul>
                <p class="mt-2 text-[11px] text-ink-4">这是能力关注点，不绑定具体模型、思考等级或价格排名。</p>
              </section>
              <section>
                <h3 class="font-semibold text-ink">关联节点用途参考</h3>
                <ul class="mt-1 space-y-2 text-ink-3">
                  <li v-for="nodeId in activeDescription.duty.nodes" :key="nodeId" class="break-words rounded-md border border-line-2 bg-surface-2 px-3 py-2">
                    <code class="font-mono text-[11px] text-ink-2">{{ nodeId }}</code>
                    <span class="ml-2">{{ getAutoNodeReference(nodeId)?.usage || '该节点名目前没有独立用途说明。' }}</span>
                  </li>
                </ul>
                <p class="mt-2 text-[11px] text-ink-4">节点名称和用途仅作阅读参考，不参与职责判定或路由；路由依据可信职责来源和当前配置。</p>
              </section>
            </div>
          </template>
          <p v-else class="text-[12px] text-ink-3">该职责暂无说明。</p>
        </template>
        <template v-else-if="activeDescription?.kind === 'node'">
          <div class="space-y-4 text-[12px] leading-5">
            <section class="rounded-md border border-line-2 bg-surface-2 p-3">
              <h3 class="font-semibold text-ink">实际路由职责</h3>
              <p class="mt-1 break-words text-base font-semibold text-ink">{{ activeDescription.observation.selected_duty || '未确认' }}<span v-if="activeObservedDuty?.label && activeObservedDuty.label !== activeDescription.observation.selected_duty" class="ml-1 text-sm font-normal text-ink-3">（{{ activeObservedDuty.label }}）</span></p>
              <p v-if="activeDescription.observation.selected_duty" class="mt-1 text-ink-3">{{ activeObservedDuty?.description?.purpose || '观察记录保留了该 selected_duty 值，但没有匹配到已知职责说明。' }}</p>
              <p v-else class="mt-1 text-[11px] text-ink-4">当前实际用途未确认。节点名称和 task_type 只作参考，不会被用来推断路由职责。</p>
            </section>
            <section>
              <h3 class="font-semibold text-ink">节点与路由来源</h3>
              <dl class="mt-2 grid gap-x-3 gap-y-1 sm:grid-cols-[max-content_minmax(0,1fr)]">
                <dt class="text-ink-4">node_id</dt><dd class="break-all font-mono text-ink-3">{{ activeDescription.observation.node_id || '缺失' }}</dd>
                <dt class="text-ink-4">task_type</dt><dd class="break-words text-ink-3">{{ activeDescription.observation.task_type || '未提供' }}</dd>
                <dt class="text-ink-4">source</dt><dd class="break-words text-ink-3">{{ activeDescription.observation.selection_source || '未提供' }}</dd>
                <dt class="text-ink-4">unknown_reason</dt><dd class="break-words text-ink-3">{{ activeDescription.observation.unknown_reason || '未提供' }}</dd>
              </dl>
            </section>
            <section>
              <h3 class="font-semibold text-ink">稳定身份</h3>
              <dl class="mt-2 grid gap-x-3 gap-y-1 sm:grid-cols-[max-content_minmax(0,1fr)]">
                <dt class="text-ink-4">definition_id</dt><dd class="break-all font-mono text-ink-3">{{ activeDescription.observation.definition_id || '缺失' }}</dd>
                <dt class="text-ink-4">definition_revision</dt><dd class="break-all font-mono text-ink-3">{{ activeDescription.observation.definition_revision || '缺失' }}</dd>
                <dt class="text-ink-4">node_path</dt><dd class="break-all font-mono text-ink-3">{{ activeDescription.observation.node_path || '缺失' }}</dd>
              </dl>
            </section>
            <section class="rounded-md border border-line-2 bg-surface-2 p-3">
              <h3 class="font-semibold text-ink">节点名用途参考（不决定职责）</h3>
              <template v-if="activeNodeReference">
                <p class="mt-1 text-ink-3"><code class="font-mono">{{ activeNodeReference.nodeId }}</code>：{{ activeNodeReference.usage }}</p>
              </template>
              <p v-else class="mt-1 text-ink-3">该节点名没有独立参考说明；实际路由职责以上方观察记录为准。不会根据裸 node_id 推断路由。</p>
              <p class="mt-2 text-[11px] text-ink-4">此处只解释名称，不会改变职责、模型选择或配置。</p>
            </section>
          </div>
        </template>
      </div>
    </div>
  </dialog>
</template>
