<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { get } from '../../api/http.js'
import { getModelGroups, getModels } from '../../api/models.js'
import { getSettings, updateSettings } from '../../api/settings.js'
import { useToast } from '../../composables/useToast.js'
import AutoNodeRuleEditor from './AutoNodeRuleEditor.vue'
import {
  AUTO_DUTY_GROUPS,
  AUTO_DUTY_LAYOUTS,
  AUTO_DUTY_LAYOUT_LABELS,
  AUTO_DUTY_OPTIONS,
  AUTO_ROUTING_SETTINGS_KEY,
  autoDutyValidationIssues,
  autoTargetDefaultEffort,
  autoTargetEffortOptions,
  autoTargetGroupCandidates,
  applyNodeBindingRule,
  applyNodeOverrideRule,
  applyDutyForLayout,
  emptyAutoDutyConfig,
  isAutoTargetSelectable,
  layoutMergeConflicts,
  migrateAutoDutyConfig,
  observationIdentity,
  nodeIdentityComplete,
  pathOnlyNodeRuleConflictMessage,
  parseAutoDutyConfig,
  resolveAutoTarget,
  validateAutoDutyConfig,
} from '../../utils/autoDutyConfig.js'
import {
  autoNodeContextEntries,
  autoNodeIdentityKey,
  buildAutoNodeCatalog,
  nodeIdentityErrors,
} from '../../utils/autoNodeCatalog.js'
import {
  describeAutoRouteReason,
  getAutoDutyDescription,
  getAutoNodeDescription,
  getAutoNodeReference,
} from '../../utils/autoNodeDescriptions.js'

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
const historyError = ref('')
const saveError = ref('')
const observations = ref([])
const historyData = ref(null)
const evidenceLoaded = ref(false)
const observationsAvailable = ref(false)
const historyAvailable = ref(false)
const activityNodesAvailable = ref(false)
const quickSelections = ref({})
const selectedBatchIdentities = ref([])
const batchQuickSelection = ref('')
const batchSelectionNotice = ref('')
const catalogSearch = ref('')
const catalogFilter = ref('all')
const loading = ref(false)
const evidenceLoading = ref(false)
const saving = ref(false)
const selectedMigrationLayout = ref('standard')
const pendingLayout = ref('')
const mergeResolutions = ref({})
const bindingResolutions = ref({})
const descriptionDialog = ref(null)
const descriptionCloseButton = ref(null)
const descriptionTitleId = useId()
const activeDescription = ref(null)
const dutyTargetsDetails = ref(null)
const defaultTargetElement = ref(null)
const dutyTargetElements = Object.create(null)
const nodeRuleEditorOpen = ref(false)
const editingNodeRuleIndex = ref(null)
const nodeRuleDraft = ref(emptyNodeRule())
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
const activeNodeDescription = computed(() => (
  activeDescription.value?.kind === 'node'
    ? nodeDescription(activeDescription.value.catalogCard || activeDescription.value.observation)
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
const catalogCards = computed(() => buildAutoNodeCatalog(observations.value, historyData.value?.items || []))
const catalogByIdentity = computed(() => new Map(catalogCards.value
  .filter((card) => card.identityComplete)
  .map((card) => [card.identityKey, card])))
const sortedCatalogCards = computed(() => [...catalogCards.value].sort((a, b) => (
  catalogCardSortPriority(a) - catalogCardSortPriority(b) ||
  nodeTitle(a).localeCompare(nodeTitle(b)) ||
  String(a.key).localeCompare(String(b.key))
)))
const filteredCatalogCards = computed(() => {
  const query = catalogSearch.value.trim().toLocaleLowerCase()
  return sortedCatalogCards.value.filter((card) => {
    if (catalogFilter.value === 'pending' && cardHasRule(card)) return false
    if (catalogFilter.value === 'rule' && !cardHasRule(card)) return false
    if (catalogFilter.value === 'unclassified' && !cardIsUnclassified(card)) return false
    if (catalogFilter.value === 'missing_identity' && card.identityComplete) return false
    if (!query) return true
    return catalogSearchText(card).toLocaleLowerCase().includes(query)
  })
})
const filteredActivityNodes = computed(() => {
  const query = catalogSearch.value.trim().toLocaleLowerCase()
  return (historyData.value?.activity_nodes || []).filter((item) => item?.has_auto_route === false).filter((item) => {
    if (!query) return true
    return [item.workflow_name, item.node_id, item.workflow_id, item.conversation_id]
      .filter((value) => typeof value === 'string')
      .join(' ')
      .toLocaleLowerCase()
      .includes(query)
  })
})
const quickDutyOptions = computed(() => duties.value.map((duty) => {
  const target = config.value.duty_models[duty.key]
  const modelId = target?.model_id || ''
  const effort = target?.reasoning_effort
  const selectable = Boolean(modelId) && isAutoTargetSelectable(modelId, models.value, groups.value)
  const effortAllowed = typeof effort === 'string' && autoTargetEffortOptions(modelId, models.value, groups.value)
    .includes(effort.trim().toLowerCase())
  return {
    ...duty,
    value: `duty:${duty.key}`,
    modelId,
    effort: typeof effort === 'string' ? effort : '',
    disabled: !selectable || !effortAllowed,
    reason: !modelId
      ? '职责尚无目标'
      : !selectable
        ? '职责目标不可用'
        : !effortAllowed
          ? '职责等级不受目标支持'
          : '',
  }
}))
const quickTargetOptions = computed(() => [
  ...selectableModels.value.map((model) => quickTargetOption(model.id, 'model')),
  ...enabledGroups.value.map((group) => quickTargetOption(group.id, 'group')),
])
const selectedBatchObservations = computed(() => {
  const selected = new Set(selectedBatchIdentities.value)
  return sortedCatalogCards.value.filter((card) => selected.has(card.key))
})
const visibleSelectedBatchCount = computed(() => {
  const visible = new Set(filteredCatalogCards.value.map((card) => card.key))
  return selectedBatchObservations.value.filter((card) => visible.has(card.key)).length
})
const hiddenSelectedBatchCount = computed(() => selectedBatchObservations.value.length - visibleSelectedBatchCount.value)
const orphanedBatchSelectionCount = computed(() => selectedBatchIdentities.value.length - selectedBatchObservations.value.length)
const batchValidationErrors = computed(() => buildBatchCandidate().errors)
const canApplyBatchQuickRule = computed(() => (
  selectedBatchIdentities.value.length > 0 &&
  orphanedBatchSelectionCount.value === 0 &&
  Boolean(batchQuickSelection.value) &&
  batchValidationErrors.value.length === 0
))
const batchQuickPreview = computed(() => quickSelectionDetails(batchQuickSelection.value))
const batchActionLabel = computed(() => {
  if (batchQuickPreview.value?.kind !== 'binding') {
    const replacesPathOnlyBinding = selectedBatchObservations.value.some((card) => (
      isPathOnlyNodeIdentity(card.latestRoute) && nodeBindingForObservation(card.latestRoute)
    ))
    if (replacesPathOnlyBinding) return '批量切换为独立规则并替换同身份职责绑定'
    const hasExistingOverride = selectedBatchObservations.value.some((card) => nodeOverrideForObservation(card.latestRoute))
    return hasExistingOverride ? '批量更新独立规则' : '批量添加独立规则'
  }
  const hasExistingBinding = selectedBatchObservations.value.some((card) => nodeBindingForObservation(card.latestRoute))
  const hasExistingOverride = selectedBatchObservations.value.some((card) => nodeOverrideForObservation(card.latestRoute))
  if (hasExistingOverride) return '批量切换到职责绑定并删除覆盖'
  return hasExistingBinding ? '批量更新职责绑定' : '批量添加职责绑定'
})
const mergeConflicts = computed(() => pendingLayout.value ? layoutMergeConflicts(config.value) : [])
const incompatibleBindings = computed(() => {
  if (pendingLayout.value !== 'expanded' || config.value.layout !== 'standard') return []
  return config.value.node_bindings.flatMap((item, index) => {
    if (!AUTO_DUTY_GROUPS[item.duty]) return []
    return [{ identityKey: observationIdentity(item), index, item, children: AUTO_DUTY_GROUPS[item.duty] }]
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

function shortTargetId(targetId) {
  const value = String(targetId || '')
  return value.length > 10 ? `${value.slice(0, 8)}…` : value
}

function quickTargetLabel(targetId) {
  const target = resolveAutoTarget(targetId, models.value, groups.value)
  if (target.kind === 'model') {
    const name = target.model.name || target.model.model_id || target.model.id
    return `模型：${name}${name !== target.model.id ? ` · ${shortTargetId(target.model.id)}` : ''}`
  }
  if (target.kind === 'group') {
    const name = target.group.name || target.group.id
    return `分组：${name}${name !== target.group.id ? ` · ${shortTargetId(target.group.id)}` : ''}`
  }
  return targetIdentity(targetId)
}

function quickTargetOption(targetId, kind) {
  const effort = autoTargetDefaultEffort(targetId, models.value, groups.value)
  const allowed = autoTargetEffortOptions(targetId, models.value, groups.value)
  return {
    id: targetId,
    kind,
    label: quickTargetLabel(targetId),
    effort,
    disabled: !allowed.includes(effort),
  }
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
  for (const entry of incompatibleBindings.value) {
    const duty = bindingResolutions.value[entry.identityKey]
    if (duty !== undefined) resolutions[`binding:${entry.index}`] = duty
  }
  const next = applyDutyForLayout(config.value, layout, resolutions)
  config.value = next
  pendingLayout.value = ''
}

function canApplyLayout() {
  if (!pendingLayout.value) return false
  if (mergeConflicts.value.some((conflict) => !['keepFirst', 'keepSecond', 'block'].includes(mergeResolutions.value[conflict.combined]))) return false
  if (incompatibleBindings.value.some((entry) => !Object.hasOwn(entry.children, bindingResolutions.value[entry.identityKey]))) return false
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

function quickSelectionDetails(selection) {
  if (!selection) return null
  if (selection.startsWith('duty:')) {
    const duty = duties.value.find((item) => item.key === selection.slice('duty:'.length))
    if (!duty) return { error: '所选职责不属于当前布局。' }
    const target = config.value.duty_models[duty.key]
    const modelId = target?.model_id || ''
    const effort = typeof target?.reasoning_effort === 'string' ? target.reasoning_effort : ''
    const option = quickDutyOptions.value.find((item) => item.key === duty.key)
    return {
      kind: 'binding',
      duty,
      modelId,
      effort,
      targetLabel: quickTargetLabel(modelId),
      error: option?.disabled ? `${duty.label}：${option.reason}。` : '',
    }
  }
  if (selection.startsWith('target:')) {
    const modelId = selection.slice('target:'.length)
    const option = quickTargetOptions.value.find((item) => item.id === modelId)
    const effort = option?.effort ?? ''
    return {
      kind: 'override',
      modelId,
      effort,
      targetLabel: quickTargetLabel(modelId),
      error: !option || option.disabled ? '目标或默认等级当前不可用。' : '',
    }
  }
  return { error: '快捷规则选择无效。' }
}

function quickRuleValidationErrors(observation, selection) {
  const errors = nodeIdentityErrors(observation)
  const details = quickSelectionDetails(selection)
  if (!details) return [...errors, '请选择职责、模型或分组。']
  if (details.error) errors.push(details.error)
  const pathConflict = pathOnlyNodeRuleConflictMessage(config.value, observation)
  if (pathConflict) errors.push(pathConflict)
  if (!details.modelId) errors.push('目标为空；请选择已配置的职责目标、启用模型或有效分组。')
  else if (!isAutoTargetSelectable(details.modelId, models.value, groups.value)) errors.push('目标不是已启用模型或有效分组。')
  if (typeof details.effort !== 'string') errors.push('思考等级必须是字符串。')
  else if (!autoTargetEffortOptions(details.modelId, models.value, groups.value).includes(details.effort.trim().toLowerCase())) {
    errors.push('思考等级不受所选目标支持。')
  }
  if (errors.length || !autoNodeIdentityKey(observation)) return [...new Set(errors)]

  const candidate = cloneConfigForQuickRule()
  applyQuickSelection(candidate, observation, details)
  errors.push(...newlyIntroducedAutoDutyIssues(candidate).map((issue) => issue.message))
  return [...new Set(errors)]
}

function newlyIntroducedAutoDutyIssues(candidate) {
  const existingKeys = new Set(autoDutyValidationIssues(config.value, models.value, groups.value).map((issue) => issue.key))
  return autoDutyValidationIssues(candidate, models.value, groups.value).filter((issue) => !existingKeys.has(issue.key))
}

function cloneConfigForQuickRule() {
  return {
    ...config.value,
    duty_models: Object.fromEntries(Object.entries(config.value.duty_models).map(([key, value]) => [key, { ...value }])),
    node_bindings: config.value.node_bindings.map((item) => ({ ...item })),
    node_overrides: config.value.node_overrides.map((item) => ({ ...item })),
  }
}

function applyQuickSelection(candidate, observation, details) {
  if (details.kind === 'binding') {
    const item = {
      definition_id: observation.definition_id,
      definition_revision: observation.definition_revision,
      node_path: observation.node_path,
      duty: details.duty.key,
    }
    const next = applyNodeBindingRule(candidate, item)
    candidate.node_bindings = next.node_bindings
    candidate.node_overrides = next.node_overrides
    return
  }
  const item = {
    definition_id: observation.definition_id,
    definition_revision: observation.definition_revision,
    node_path: observation.node_path,
    model_id: details.modelId,
    reasoning_effort: details.effort,
  }
  const next = applyNodeOverrideRule(candidate, item, { upsertIdentity: true })
  candidate.node_bindings = next.node_bindings
  candidate.node_overrides = next.node_overrides
}

function buildBatchCandidate() {
  const errors = []
  if (orphanedBatchSelectionCount.value) errors.push(`有 ${orphanedBatchSelectionCount.value} 个已选身份当前证据中不可核对；请重新读取证据或清空选择。`)
  if (!selectedBatchObservations.value.length || !batchQuickSelection.value) return { candidate: null, errors }
  const candidate = cloneConfigForQuickRule()
  const details = quickSelectionDetails(batchQuickSelection.value)
  for (const card of selectedBatchObservations.value) {
    const observation = card.latestRoute
    const cardErrors = quickRuleValidationErrors(observation, batchQuickSelection.value)
    errors.push(...cardErrors.map((error) => `${stableObservationLabel(observation)}：${error}`))
    if (!cardErrors.length && details) applyQuickSelection(candidate, observation, details)
  }
  if (details) {
    const checkedIdentities = new Set()
    for (const card of selectedBatchObservations.value) {
      const observation = card.latestRoute
      const identity = observationIdentity(observation)
      if (checkedIdentities.has(identity)) continue
      checkedIdentities.add(identity)
      const candidateConflict = pathOnlyNodeRuleConflictMessage(candidate, observation)
      const currentConflict = pathOnlyNodeRuleConflictMessage(config.value, observation)
      if (candidateConflict && candidateConflict !== currentConflict) {
        errors.push(`${stableObservationLabel(observation)}：${candidateConflict}`)
      }
    }
  }
  errors.push(...newlyIntroducedAutoDutyIssues(candidate).map((issue) => `最终草稿：${issue.message}`))
  return { candidate, errors: [...new Set(errors)] }
}

function setQuickSelection(card, selection) {
  quickSelections.value = { ...quickSelections.value, [card.key]: selection }
}

function quickSelectionFor(card) {
  return quickSelections.value[card.key] || ''
}

function setBatchIdentitySelected(card, selected) {
  const identity = card.key
  const selectedIdentities = new Set(selectedBatchIdentities.value)
  if (!selected) {
    selectedIdentities.delete(identity)
    selectedBatchIdentities.value = [...selectedIdentities]
    return
  }
  if (!card.identityComplete || nodeIdentityErrors(card.latestRoute).length) return
  selectedIdentities.add(identity)
  selectedBatchIdentities.value = [...selectedIdentities]
}

function isBatchIdentitySelected(card) {
  return selectedBatchIdentities.value.includes(card.key)
}

function canSelectBatchIdentity(card) {
  return card.identityComplete && nodeIdentityErrors(card.latestRoute).length === 0
}

function quickActionLabel(card) {
  const observation = card.latestRoute
  const details = quickSelectionDetails(quickSelectionFor(card))
  if (!details) return '快捷添加'
  if (details.kind === 'binding') {
    if (nodeOverrideForObservation(observation)) return '切换到职责绑定并删除覆盖'
    return nodeBindingForObservation(observation) ? '更新职责绑定' : '快捷添加职责绑定'
  }
  if (isPathOnlyNodeIdentity(observation) && nodeBindingForObservation(observation)) {
    return nodeOverrideForObservation(observation)
      ? '更新独立规则并替换同身份职责绑定'
      : '切换到独立规则并替换同身份职责绑定'
  }
  return nodeOverrideForObservation(observation) ? '更新独立规则' : '添加独立规则'
}

function applyQuickRule(card) {
  const observation = card.latestRoute
  const selection = quickSelectionFor(card)
  if (quickRuleValidationErrors(observation, selection).length) return
  const details = quickSelectionDetails(selection)
  if (!details) return
  const candidate = cloneConfigForQuickRule()
  applyQuickSelection(candidate, observation, details)
  if (newlyIntroducedAutoDutyIssues(candidate).length) return
  config.value.node_bindings = candidate.node_bindings
  config.value.node_overrides = candidate.node_overrides
  const nextSelections = { ...quickSelections.value }
  delete nextSelections[card.key]
  quickSelections.value = nextSelections
}

function applyBatchQuickRule() {
  if (!canApplyBatchQuickRule.value) return
  const { candidate, errors } = buildBatchCandidate()
  if (!candidate || errors.length) return
  config.value.node_bindings = candidate.node_bindings
  config.value.node_overrides = candidate.node_overrides
  selectedBatchIdentities.value = []
  batchQuickSelection.value = ''
  batchSelectionNotice.value = ''
  quickSelections.value = {}
}

function descriptionRecord(record) {
  if (record?.records && Array.isArray(record.records)) return record.evidenceRecord || record.latestRoute || record.displayRecord || {}
  const identity = autoNodeIdentityKey(record)
  const card = identity ? catalogByIdentity.value.get(identity) : null
  return card?.evidenceRecord || card?.latestRoute || record
}

function nodeDescription(record) {
  return getAutoNodeDescription(descriptionRecord(record))
}

function nodeTitle(record) {
  const description = nodeDescription(record)
  if (description.nodeId) return description.nodeId
  const route = record?.latestRoute || record
  if (route?.node_id) return route.node_id
  const path = String(route?.node_path || '')
  return path.split('/').filter(Boolean).at(-1) || '节点名未提供'
}

function cardWorkflowName(card) {
  return typeof card?.evidenceRecord?.workflow_name === 'string' ? card.evidenceRecord.workflow_name : ''
}

function nodeWorkflowName(record) {
  const card = record?.records && Array.isArray(record.records)
    ? record
    : catalogByIdentity.value.get(autoNodeIdentityKey(record))
  return cardWorkflowName(card)
}

function isPathOnlyNodeIdentity(record) {
  return record?.definition_id === '' && record?.definition_revision === '' &&
    typeof record?.node_path === 'string' && record.node_path !== '' && nodeIdentityErrors(record).length === 0
}

function cardHasRule(card) {
  if (!card?.identityComplete) return false
  return config.value.node_bindings.some((item) => observationIdentity(item) === card.identityKey) ||
    config.value.node_overrides.some((item) => observationIdentity(item) === card.identityKey)
}

function catalogCardSortPriority(card) {
  if (!card.identityComplete) return 1
  return cardHasRule(card) ? 2 : 0
}

function cardIsUnclassified(card) {
  const route = card?.latestRoute
  if (isObservedCustomRule(route)) return false
  if (!route?.selected_duty || route.unknown_reason) return true
  return !duties.value.some((duty) => duty.key === route.selected_duty || duty.label === route.selected_duty)
}

function catalogSearchText(card) {
  const values = [nodeTitle(card), cardWorkflowName(card), nodeUsage(card), nodeEvidenceLabel(card)]
  if (card.identityComplete) values.push(card.identityKey)
  for (const record of card.records) {
    values.push(
      record.node_id,
      record.node_path,
      record.definition_id,
      record.definition_revision,
      record.workflow_id,
      record.workflow_name,
      record.task_type,
      record.agent_task_type,
      record.agent_mode,
      record.selected_duty,
      record.unknown_reason,
      record.session_excerpt,
    )
  }
  return values.filter((value) => typeof value === 'string').join(' ')
}

function cardFilterLabel(card) {
  if (!card.identityComplete) return '缺身份；仅按上下文展示'
  if (nodeOverrideForObservation(card.latestRoute)) return '已有独立规则'
  if (nodeBindingForObservation(card.latestRoute)) return '已有职责绑定'
  return '待配置'
}

function routeSummary(record) {
  if (!record) return '没有可显示的路由记录'
  const duty = record.selected_duty || '职责未确认'
  const source = record.selection_source ? ` · source=${record.selection_source}` : ''
  const taskType = record.task_type ? ` · task_type=${record.task_type}` : ''
  const reason = record.unknown_reason ? ` · 原因=${routeReasonText(record.unknown_reason)}（${record.unknown_reason}）` : ''
  return `${duty} → ${observedTargetLabel(record)} / ${record.effective_effort || '上游默认'}${source}${taskType}${reason}`
}

function sourceReference(record, prefix = 'source') {
  if (!record) return ''
  const file = record[`${prefix}_file`]
  const line = record[`${prefix}_line`]
  const relative = record[`${prefix}_line_relative`] === true
  const offset = record[`${prefix}_byte_offset`]
  const parts = []
  if (file) parts.push(file)
  if (line !== undefined && line !== null && line !== '') {
    parts.push(relative ? `窗口相对行 ${line}` : `第 ${line} 行`)
  } else if (offset !== undefined && offset !== null && offset !== '') {
    parts.push(`字节偏移 ${offset}`)
  }
  if (offset !== undefined && offset !== null && offset !== '' && line !== undefined && line !== null && line !== '') {
    parts.push(`字节偏移 ${offset}`)
  }
  return parts.join(' · ') || '未提供来源位置'
}

function evidenceProvenance(record) {
  const card = record?.records && Array.isArray(record.records)
    ? record
    : catalogByIdentity.value.get(autoNodeIdentityKey(record))
  const evidence = card?.evidenceRecord || record
  return evidence?.catalog_source === 'history' && evidence.workflow_name
    ? `历史标题来源：${sourceReference(evidence, 'activity_source')}`
    : ''
}

function fileCoverageLabel(file) {
  const status = file.status === 'ok' ? '读取完成' : file.status === 'missing' ? '未找到来源' : '读取失败'
  const location = file.line_relative
    ? `行号相对截取窗口，起始字节 ${file.start_byte_offset ?? '未知'}`
    : '行号为文件绝对行'
  const size = `读取 ${file.read_bytes ?? 0}/${file.size_bytes ?? 0} 字节`
  const range = file.start_line !== undefined || file.end_line !== undefined
    ? `行范围 ${file.start_line ?? '未知'}–${file.end_line ?? '未知'}`
    : ''
  return [status, size, range, location, file.truncated ? '文件读取有截断' : ''].filter(Boolean).join(' · ')
}

function warningLocation(warning) {
  if (!warning?.file) return warning?.line ? `第 ${warning.line} 行` : ''
  const file = historyData.value?.files?.find((item) => item.name === warning.file)
  const line = warning?.line
  return line === undefined || line === null || line === ''
    ? warning.file
    : `${warning.file} · ${file?.line_relative ? `窗口相对行 ${line}` : `第 ${line} 行`}`
}

function selectFilteredCatalogCards() {
  const candidates = filteredCatalogCards.value
  const eligible = candidates.filter((card) => canSelectBatchIdentity(card))
  const skipped = candidates.length - eligible.length
  const selected = new Set(selectedBatchIdentities.value)
  eligible.forEach((card) => selected.add(card.key))
  selectedBatchIdentities.value = [...selected]
  batchSelectionNotice.value = `已选中当前筛选结果中的 ${eligible.length} 个完整身份；跳过 ${skipped} 个缺身份或非法身份。`
}

function clearBatchSelection() {
  selectedBatchIdentities.value = []
  batchQuickSelection.value = ''
  batchSelectionNotice.value = '已清空批量选择。'
}

function setDutyTargetElement(key, element) {
  if (element) dutyTargetElements[key] = element
  else delete dutyTargetElements[key]
}

function configureCardTarget(card) {
  const duty = cardCurrentDuty(card)
  const targetElement = duty ? dutyTargetElements[duty.key] : defaultTargetElement.value
  if (!targetElement) return
  if (duty && dutyTargetsDetails.value) dutyTargetsDetails.value.open = true
  nextTick(() => {
    targetElement.scrollIntoView({ behavior: 'smooth', block: 'center' })
    targetElement.focus({ preventScroll: true })
  })
}

function cardCurrentDuty(card) {
  const selectedDuty = card?.latestRoute?.selected_duty
  return duties.value.find((item) => item.key === selectedDuty || item.label === selectedDuty) || null
}

function cardTargetScopeText(card) {
  const route = card.latestRoute || {}
  const duty = cardCurrentDuty(card)
  const scope = duty
    ? `修改当前布局中“${duty.label}”的职责目标，会影响之后按该职责选择目标的请求；不会重写这条已观察或历史路由。`
    : '修改固定默认目标，会影响之后未匹配职责或规则的请求；不会为这条上下文记录创建节点规则。'
  const legacyStage = Boolean(route.selected_stage) && Number(route.workflow_metadata_version) !== 2
  if (['node_override', 'node_override_path'].includes(route.selection_source) || route.lock_scope || legacyStage) {
    return `${scope} 此记录还显示独立覆盖、锁定范围或旧版阶段路由；改职责目标不能证明会控制这条路由。`
  }
  return scope
}

function nodeUsage(record) {
  const description = nodeDescription(record)
  return description.known && description.usage ? description.usage : '用途待确认'
}

function nodeEvidenceLabel(record) {
  return nodeDescription(record).evidenceLabel || '说明依据不足'
}

function routeReasonText(reason) {
  return describeAutoRouteReason(reason)
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
  const catalogCard = observation?.records && Array.isArray(observation.records) ? observation : null
  descriptionTrigger = trigger
  restoreDescriptionFocus = true
  activeDescription.value = {
    kind: 'node',
    observation: catalogCard?.latestRoute || observation,
    catalogCard,
    activityOnly: !catalogCard && observation?.has_auto_route === false,
  }
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

function emptyNodeRule() {
  return {
    definition_id: '',
    definition_revision: '',
    node_path: '',
    model_id: '',
    reasoning_effort: '',
  }
}

function openNodeRuleEditor(draft, editingIndex = null) {
  editingNodeRuleIndex.value = editingIndex
  nodeRuleDraft.value = { ...draft }
  nodeRuleEditorOpen.value = true
}

function addNodeRule() {
  openNodeRuleEditor(emptyNodeRule())
}

function applyNodeRule(item) {
  const next = applyNodeOverrideRule(config.value, item, { editingIndex: editingNodeRuleIndex.value })
  if (!next) return
  config.value.node_bindings = next.node_bindings
  config.value.node_overrides = next.node_overrides
  closeNodeRuleEditor()
}

function closeNodeRuleEditor() {
  nodeRuleEditorOpen.value = false
  editingNodeRuleIndex.value = null
  nodeRuleDraft.value = emptyNodeRule()
}

function isObservedCustomRule(observation) {
  return ['node_override', 'node_override_path'].includes(observation?.selection_source) && !observation.unknown_reason
}

function hasCustomRuleForObservation(observation) {
  if (!nodeIdentityComplete(observation)) return false
  const identity = observationIdentity(observation)
  return config.value.node_overrides.some((item) => observationIdentity(item) === identity)
}

function nodeOverrideIndex(observation) {
  if (!nodeIdentityComplete(observation)) return -1
  const identity = observationIdentity(observation)
  return config.value.node_overrides.findIndex((item) => observationIdentity(item) === identity)
}

function nodeOverrideForObservation(observation) {
  const index = nodeOverrideIndex(observation)
  return index >= 0 ? config.value.node_overrides[index] : null
}

function nodeBindingForObservation(observation) {
  if (!nodeIdentityComplete(observation)) return null
  const identity = observationIdentity(observation)
  return config.value.node_bindings.find((item) => observationIdentity(item) === identity) || null
}

function bindingTargetLabel(item) {
  const target = config.value.duty_models[item.duty]
  if (!target?.model_id) return '目标未配置'
  return `${quickTargetLabel(target.model_id)} / ${describeEffort(target.reasoning_effort)}`
}

function bindingTargetFullId(item) {
  return config.value.duty_models[item.duty]?.model_id || ''
}

function hasCurrentProcessObservation(item) {
  if (!nodeIdentityComplete(item)) return false
  const identity = observationIdentity(item)
  return observations.value.some((observation) => (
    nodeIdentityComplete(observation) && observationIdentity(observation) === identity
  ))
}

function removeNodeBinding(item) {
  const key = observationIdentity(item)
  config.value.node_bindings = config.value.node_bindings.filter((existing) => observationIdentity(existing) !== key)
}

function removeNodeOverride(item) {
  const key = observationIdentity(item)
  config.value.node_overrides = config.value.node_overrides.filter((existing) => observationIdentity(existing) !== key)
  selectedBatchIdentities.value = selectedBatchIdentities.value.filter((identity) => identity !== `identity:${key}`)
}

async function load() {
  loading.value = true
  snapshot.value = null
  rawConfig.value = ''
  configError.value = ''
  loadError.value = ''
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
    } catch (error) {
      configError.value = error.message || 'Auto 模型配置无法解析。'
      showToast(configError.value, { tone: 'error' })
    }
  } catch (error) {
    loadError.value = error?.message || '设置读取失败，无法安全保存。'
    console.error('加载 Auto 模型配置失败', error)
    showToast(loadError.value, { tone: 'error' })
  } finally {
    loading.value = false
  }
  await refreshEvidence()
}

function responseErrorMessage(error, fallback) {
  if (typeof error?.message === 'string' && error.message) return error.message
  if (typeof error === 'string' && error) return error
  return fallback
}

async function refreshEvidence() {
  if (evidenceLoading.value) return
  evidenceLoading.value = true
  const [observationsResult, historyResult] = await Promise.allSettled([
    get('/api/auto-routing/observations'),
    get('/api/auto-routing/history'),
  ])

  if (observationsResult.status === 'fulfilled') {
    const data = observationsResult.value
    const nextObservations = Array.isArray(data)
      ? data
      : Array.isArray(data?.data)
        ? data.data
        : null
    if (nextObservations) {
      observations.value = nextObservations
      observationsAvailable.value = true
      observationsError.value = ''
    } else {
      observationsError.value = '节点观察接口返回格式无效：预期为观察记录数组。'
    }
  } else {
    observationsError.value = responseErrorMessage(observationsResult.reason, '节点观察列表读取失败。')
  }

  if (historyResult.status === 'fulfilled') {
    const raw = historyResult.value
    const data = raw?.data && typeof raw.data === 'object' && !Array.isArray(raw.data) ? raw.data : raw
    if (data && typeof data === 'object' && !Array.isArray(data) && Array.isArray(data.items)) {
      historyData.value = {
        ...data,
        files: Array.isArray(data.files) ? data.files : [],
        warnings: Array.isArray(data.warnings) ? data.warnings : [],
        activity_nodes: Array.isArray(data.activity_nodes) ? data.activity_nodes : [],
        warning_count: Number.isFinite(Number(data.warning_count)) ? Number(data.warning_count) : (Array.isArray(data.warnings) ? data.warnings.length : 0),
        route_count: Number.isFinite(Number(data.route_count)) ? Number(data.route_count) : data.items.length,
        activity_count: Number.isFinite(Number(data.activity_count)) ? Number(data.activity_count) : 0,
      }
      historyAvailable.value = true
      activityNodesAvailable.value = Array.isArray(data.activity_nodes)
      historyError.value = ''
    } else {
      historyError.value = '历史路由接口返回格式无效：缺少 items 数组。'
    }
  } else {
    historyError.value = responseErrorMessage(historyResult.reason, '历史路由读取失败。')
  }
  evidenceLoaded.value = true
  evidenceLoading.value = false
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
              <div v-for="entry in incompatibleBindings" :key="entry.identityKey" class="grid gap-2 rounded border border-warning-tint-line bg-surface p-2 sm:grid-cols-[1fr_220px]">
                <span>节点绑定 {{ stableObservationLabel(entry.item) }} 当前为 {{ AUTO_DUTY_OPTIONS.find((item) => item.key === entry.item.duty)?.label }}，请选择细分职责。</span>
                <select v-model="bindingResolutions[entry.identityKey]" class="ui-control-h ui-control px-2 text-ink">
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
              <select id="auto-duty-default" ref="defaultTargetElement" :value="config.default_model_id" class="ui-control-h ui-control w-full px-2.5 text-[12px] text-ink" @change="config.default_model_id = $event.target.value">
                <option value="">未配置</option>
                <option v-for="option in optionsFor(config.default_model_id)" :key="option.id" :value="option.id" :disabled="option.disabled">{{ option.label }}</option>
              </select>
            </section>

            <section class="rounded-lg border border-line-2 p-3">
              <details ref="dutyTargetsDetails">
                <summary class="cursor-pointer text-[12px] font-semibold text-ink">职责固定目标（{{ duties.length }}）</summary>
                <p class="mb-3 mt-1 text-[11px] leading-4 text-ink-4">目标由模型或分组 UUID 和独立思考等级组成；选择分组时沿用其成员与后备组调度。</p>
                <div class="space-y-2">
                <div v-for="duty in duties" :key="duty.key" class="grid gap-2 rounded-md border border-line-2 px-2.5 py-2 sm:grid-cols-[minmax(130px,0.8fr)_minmax(180px,1.5fr)_130px]">
                  <div class="min-w-0">
                    <div class="flex items-center justify-between gap-2">
                      <div class="text-[11px] font-medium text-ink-2">{{ duty.label }}</div>
                      <button type="button" class="shrink-0 rounded border border-line-2 bg-surface px-2 py-1 text-[10px] text-ink-2 hover:bg-hover" @click="openDutyDescription(duty, $event.currentTarget)">说明</button>
                    </div>
                    <div class="mt-0.5 truncate font-mono text-[9px] leading-4 text-ink-4" :title="duty.nodes.join(', ')">{{ duty.nodes.join(' · ') }}</div>
                  </div>
                  <select :ref="(element) => setDutyTargetElement(duty.key, element)" :value="config.duty_models[duty.key].model_id" class="ui-control-h ui-control min-w-0 px-2 text-[11px] text-ink" :aria-label="`${duty.label}模型或分组`" @change="updateConfigTarget(config.duty_models[duty.key], $event.target.value)">
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
              </details>
            </section>

            <section class="rounded-lg border border-line-2 p-3">
              <div class="flex flex-wrap items-start justify-between gap-2">
                <div><h4 class="text-[12px] font-semibold text-ink">Auto 节点目录与路由证据</h4><p class="mt-1 text-[11px] text-ink-4">完整身份按 definition_id、definition_revision、node_path 原值归并；path-only 规则按完全相同的 node_path 跨工作流定义匹配。进程观察与日志历史分开计数；两者可能覆盖同一请求，不相加。快捷操作先更新草稿，点击本节下方“保存”才提交配置。</p></div>
                <button type="button" class="shrink-0 rounded border border-line-2 bg-surface px-2.5 py-1.5 text-[10px] text-ink-2 disabled:opacity-50" :disabled="evidenceLoading" @click="refreshEvidence">{{ evidenceLoading ? '正在读取证据…' : '刷新路由证据' }}</button>
              </div>
              <div class="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-[10px] text-ink-4">
                <span>进程观察：{{ observationsError ? (observationsAvailable ? `本次读取失败，保留上次成功的 ${observations.length} 条` : '读取失败') : `${observations.length} 条变体` }}</span>
                <span>历史日志：{{ historyError ? (historyAvailable ? `本次读取失败，保留上次成功的 ${historyData?.route_count ?? 0} 条记录` : '读取失败') : historyData ? `${historyData.route_count} 条 Auto 路由记录` : '尚未读取' }}</span>
                <span v-if="historyData">活动记录 {{ historyData.activity_count }} 条（不是节点数） · 返回目录项 {{ historyData.items.length }} 条</span>
              </div>
              <div v-if="observationsError" class="mt-2 rounded border border-danger-tint-line bg-danger-tint px-2.5 py-2 text-[11px] text-danger" role="alert">
                <p>进程观察读取失败：{{ observationsError }}</p>
                <p class="mt-1 text-[10px]">{{ observationsAvailable ? '页面保留上次成功读取的观察，并标为旧数据。' : '没有成功读取过进程观察；不能将空列表解释为没有节点。' }}</p>
              </div>
              <div v-if="historyError" class="mt-2 rounded border border-danger-tint-line bg-danger-tint px-2.5 py-2 text-[11px] text-danger" role="alert">
                <p>历史日志读取失败：{{ historyError }}</p>
                <p class="mt-1 text-[10px]">{{ historyAvailable ? '页面保留上次成功读取的历史，并标为旧数据。' : '没有成功读取过历史日志；不能将空列表解释为没有节点。' }}</p>
              </div>
              <section v-if="historyData" class="mt-2 rounded-md border border-line-2 bg-surface-2 p-2.5 text-[10px] text-ink-3">
                <p>历史覆盖：成功解析 {{ historyData.route_count }} 条 Auto 路由记录、{{ historyData.activity_count }} 条活动记录；当前返回 {{ historyData.items.length }} 个目录项。进程观察与历史记录分别展示，不合并成总调用数。</p>
                <p v-if="historyData.items_truncated" class="mt-1 text-warning-fg">历史目录项达到返回上限，未返回的路由变体不会出现在卡片中。</p>
                <p v-if="historyData.context_truncated" class="mt-1 text-warning-fg">活动/关联信息读取不完整，查看警告；工作流标题或请求节选可能不完整。</p>
                <details v-if="historyData.files.length" class="mt-1.5">
                  <summary class="cursor-pointer text-ink-4">查看日志文件读取状态（{{ historyData.files.length }}）</summary>
                  <ul class="mt-1 space-y-1 pl-3">
                    <li v-for="file in historyData.files" :key="file.name" class="break-all">{{ file.name }}：{{ fileCoverageLabel(file) }}</li>
                  </ul>
                </details>
                <details v-if="historyData.warning_count || historyData.warnings.length" class="mt-1.5">
                  <summary class="cursor-pointer text-warning-fg">查看读取与解析警告（{{ historyData.warning_count }}，返回 {{ historyData.warnings.length }}）</summary>
                  <ul class="mt-1 space-y-1 pl-3 text-warning-fg">
                    <li v-for="(warning, index) in historyData.warnings" :key="`${warning.file || ''}:${warning.line || ''}:${warning.code || ''}:${index}`" class="break-words"><span v-if="warningLocation(warning)">{{ warningLocation(warning) }} · </span>{{ warning.code || 'warning' }}：{{ warning.message || '无详细信息' }}</li>
                  </ul>
                </details>
              </section>
              <details v-if="historyData" class="mt-2 rounded-md border border-line-2 bg-surface-2 p-2.5 text-[10px] text-ink-3">
                <summary class="cursor-pointer font-medium text-ink-2">活动线索（没有同会话 Auto 路由证据）<span v-if="activityNodesAvailable"> · {{ filteredActivityNodes.length }} 条匹配</span></summary>
                <p class="mt-1">这些节点有日志活动，但当前没有同会话、同工作流的 Auto 路由证据。活动条数不代表模型调用数；缺少独立规则匹配身份时，也无法确定它们的职责目标。</p>
                <p v-if="!activityNodesAvailable" class="mt-1 text-warning-fg">当前历史响应未提供 activity_nodes，无法判断未关联 Auto 路由的活动线索。</p>
                <ul v-else-if="filteredActivityNodes.length" class="mt-2 space-y-2">
                  <li v-for="(activity, index) in filteredActivityNodes" :key="`${activity.workflow_id || ''}:${activity.conversation_id || ''}:${activity.node_id || ''}:${index}`" class="rounded border border-line-2 bg-surface p-2">
                    <div class="flex flex-wrap items-start justify-between gap-2">
                      <div><strong class="text-ink-2">{{ activity.workflow_name || activity.node_id || '活动标题未提供' }}</strong><span v-if="activity.workflow_name && activity.node_id" class="ml-1 font-mono text-ink-4">节点 {{ activity.node_id }}</span></div>
                      <button type="button" class="rounded border border-line-2 bg-surface px-2 py-0.5 text-[9px] text-ink-2" @click="openNodeDescription(activity, $event.currentTarget)">名称说明</button>
                    </div>
                    <p class="mt-1">用途参考：{{ nodeUsage(activity) }} <span class="text-ink-4">（名称/标题参考，不代表已观察到 Auto 职责）</span></p>
                    <p>活动条数={{ activity.activity_count ?? '未知' }}；此数字不是模型调用数。</p>
                    <details class="mt-1">
                      <summary class="cursor-pointer text-[9px] text-ink-4">核对活动上下文与来源</summary>
                      <p class="mt-1 break-all">workflow_id={{ activity.workflow_id || '未提供' }} · conversation_id={{ activity.conversation_id || '未提供' }} · node_id={{ activity.node_id || '未提供' }}</p>
                      <p>活动上下文状态={{ activity.context_status || '未提供' }} · 来源={{ sourceReference(activity) }}</p>
                      <p>该记录没有可用于独立 Auto 规则匹配的完整身份；活动 node_id 不会被当作规则身份。</p>
                    </details>
                  </li>
                </ul>
                <p v-else-if="activityNodesAvailable && !(historyData.activity_nodes || []).some((item) => item?.has_auto_route === false)" class="mt-1 text-ink-4">当前日志没有返回未关联 Auto 路由证据的活动线索。</p>
                <p v-else class="mt-1 text-ink-4">当前搜索条件没有匹配的活动线索。</p>
              </details>
              <div class="mt-3 grid gap-2 sm:grid-cols-[minmax(0,1fr)_190px]">
                <input v-model="catalogSearch" type="search" class="ui-control-h ui-control min-w-0 px-2.5 text-[11px] text-ink" placeholder="搜索工作流标题、节点名、用途或完整身份" aria-label="搜索 Auto 节点目录" />
                <select v-model="catalogFilter" class="ui-control-h ui-control px-2 text-[11px] text-ink" aria-label="筛选 Auto 节点目录">
                  <option value="all">全部</option><option value="pending">待配置</option><option value="rule">已有规则</option><option value="unclassified">未归类</option><option value="missing_identity">缺身份</option>
                </select>
              </div>
              <p class="mt-1 text-[10px] text-ink-4">显示 {{ filteredCatalogCards.length }} / {{ catalogCards.length }} 张卡片。默认优先列出尚无规则、可以配置的记录。</p>
              <div v-if="catalogCards.length || selectedBatchIdentities.length" class="mt-3 space-y-2">
                <section class="rounded-md border border-line-2 bg-surface-2 p-3">
                  <div class="flex flex-wrap items-center justify-between gap-2">
                    <h5 class="text-[11px] font-semibold text-ink-2">按完整身份批量快捷配置</h5>
                    <span class="text-[10px] text-ink-4">已选 {{ selectedBatchIdentities.length }} 个 · 当前筛选可见 {{ visibleSelectedBatchCount }} 个 · 隐藏 {{ hiddenSelectedBatchCount }} 个<span v-if="orphanedBatchSelectionCount"> · 当前证据缺项 {{ orphanedBatchSelectionCount }} 个</span></span>
                  </div>
                  <p class="mt-1 text-[10px] leading-4 text-ink-4">批量操作会校验所有已选身份和最终草稿；隐藏项仍参与校验。选择职责会更新绑定并删除同身份独立覆盖；选择模型或分组会添加或更新同身份独立规则。</p>
                  <div class="mt-2 flex flex-wrap gap-2">
                    <button type="button" class="rounded border border-line-2 bg-surface px-2.5 py-1 text-[10px] text-ink-2" @click="selectFilteredCatalogCards">选择当前筛选中的可配置节点</button>
                    <button type="button" class="rounded border border-line-2 bg-surface px-2.5 py-1 text-[10px] text-ink-2 disabled:opacity-50" :disabled="!selectedBatchIdentities.length" @click="clearBatchSelection">清空选择</button>
                  </div>
                  <p v-if="batchSelectionNotice" class="mt-1 text-[10px] text-ink-4">{{ batchSelectionNotice }}</p>
                  <div class="mt-2 grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto]">
                    <select v-model="batchQuickSelection" class="ui-control-h ui-control min-w-0 px-2 text-[10px] text-ink">
                      <option value="">选择批量规则目标</option>
                      <optgroup label="沿用职责配置并写入职责绑定">
                        <option v-for="option in quickDutyOptions" :key="option.value" :value="option.value" :disabled="option.disabled" :title="option.modelId">{{ option.label }}{{ option.disabled ? `（${option.reason}）` : '' }}</option>
                      </optgroup>
                      <optgroup label="启用模型或有效分组并添加 / 更新独立规则">
                        <option v-for="option in quickTargetOptions" :key="option.id" :value="`target:${option.id}`" :disabled="option.disabled" :title="option.id">{{ option.label }} · {{ describeEffort(option.effort) }}{{ option.disabled ? '（默认等级不可用）' : '' }}</option>
                      </optgroup>
                    </select>
                    <button type="button" class="rounded-md bg-primary px-3 py-2 text-[10px] font-medium text-white disabled:cursor-not-allowed disabled:opacity-50" :disabled="!canApplyBatchQuickRule" @click="applyBatchQuickRule">{{ batchActionLabel }}</button>
                  </div>
                  <p v-if="batchQuickPreview?.kind === 'binding'" class="mt-2 text-[10px] leading-4 text-ink-3" :title="batchQuickPreview.modelId">将写入职责绑定 {{ batchQuickPreview.duty.label }}；目标与等级沿用当前职责配置：{{ batchQuickPreview.targetLabel }} / {{ describeEffort(batchQuickPreview.effort) }}。<span v-if="selectedBatchObservations.some((card) => nodeOverrideForObservation(card.latestRoute))" class="text-warning-fg">同身份独立覆盖会被删除。</span></p>
                  <p v-else-if="batchQuickPreview?.kind === 'override'" class="mt-2 text-[10px] leading-4 text-ink-3" :title="batchQuickPreview.modelId">将使用 {{ batchQuickPreview.targetLabel }} / {{ describeEffort(batchQuickPreview.effort) }}；等级取目标配置的默认值。已存在的同身份独立规则会被更新；path-only 身份会替换并删除同身份职责绑定，完整三元组身份保留职责绑定并由独立规则优先。若规则冲突涉及 path-only 身份，批量应用会被阻止；不同完整三元组之间可共用路径。</p>
                  <p v-if="batchValidationErrors.length" class="mt-2 text-[10px] leading-4 text-danger" role="alert">{{ batchValidationErrors.join('；') }}</p>
                </section>
                <article v-for="card in filteredCatalogCards" :key="card.key" class="rounded-md border border-line-2 p-2.5 text-[10px] leading-4 text-ink-3">
                  <div class="flex flex-wrap items-start justify-between gap-2">
                    <div class="flex min-w-0 items-start gap-2">
                      <input v-if="card.identityComplete" type="checkbox" class="mt-0.5 accent-primary" :checked="isBatchIdentitySelected(card)" :disabled="!canSelectBatchIdentity(card) && !isBatchIdentitySelected(card)" :aria-label="`批量选择 ${nodeTitle(card)}`" @change="setBatchIdentitySelected(card, $event.target.checked)" />
                      <span v-else class="mt-0.5 rounded border border-warning-tint-line px-1 text-[9px] text-warning-fg" title="上下文不是节点身份">上下文</span>
                      <div class="min-w-0">
                        <div class="flex flex-wrap items-center gap-2"><strong class="text-ink-2">{{ cardWorkflowName(card) || nodeTitle(card) }}</strong><span v-if="cardWorkflowName(card)" class="font-mono text-ink-4">节点 {{ nodeTitle(card) }}</span><span class="rounded border border-line-2 px-1.5 py-0.5 text-[9px]">{{ cardFilterLabel(card) }}</span><span class="rounded border border-line-2 px-1.5 py-0.5 text-[9px]">{{ isObservedCustomRule(card.latestRoute) ? '路由记录命中独立规则' : card.latestRoute?.unknown_reason ? '路由未知' : card.latestRoute?.selected_duty ? '路由记录有职责' : '职责未确认' }}</span></div>
                        <p class="mt-1">用途：{{ nodeUsage(card) }} <span class="text-ink-4">· 依据：{{ nodeEvidenceLabel(card) }}<span v-if="evidenceProvenance(card.evidenceRecord)"> · {{ evidenceProvenance(card.evidenceRecord) }}</span></span></p>
                      </div>
                    </div>
                    <button type="button" class="shrink-0 rounded border border-line-2 bg-surface px-2 py-1 text-[10px] text-ink-2" @click="openNodeDescription(card, $event.currentTarget)">节点说明</button>
                  </div>
                  <p class="mt-2"><strong class="text-ink-2">最近路由：</strong>{{ routeSummary(card.latestRoute) }}</p>
                  <div class="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-[9px] text-ink-4">
                    <span>当前进程：{{ observationsError ? (observationsAvailable ? `读取失败，旧数据 ${card.processCount} 次` : '读取失败，状态未知') : `${card.processCount} 次` }}</span>
                    <span>历史日志：{{ historyError ? (historyAvailable ? `读取失败，旧数据 ${card.historyCount} 条` : '读取失败，状态未知') : historyData ? `${card.historyCount} 条聚合记录` : '状态未知' }}</span>
                  </div>
                  <details class="mt-1">
                    <summary class="cursor-pointer text-[10px] text-ink-4">{{ card.identityComplete ? (isPathOnlyNodeIdentity(card.latestRoute) ? '核对路径规则身份' : '核对完整稳定身份') : '核对上下文（不是节点身份）' }}</summary>
                    <div v-if="card.identityComplete" class="mt-1 break-all font-mono">definition_id={{ card.latestRoute.definition_id || '（path-only）' }} · definition_revision={{ card.latestRoute.definition_revision || '（path-only）' }} · node_path={{ card.latestRoute.node_path }}<p v-if="isPathOnlyNodeIdentity(card.latestRoute)" class="mt-1 text-[9px] text-warning-fg">该规则按完全相同的 node_path 跨定义匹配；路径歧义时使用默认目标。iter-N 按原值匹配。</p></div>
                    <div v-else class="mt-1 space-y-0.5">
                      <p>这些字段只用于区分观察上下文，不能作为独立节点规则身份。</p>
                      <p v-for="entry in autoNodeContextEntries(card.latestRoute)" :key="entry.key" class="break-all">{{ entry.label }}={{ entry.value }}</p>
                      <p class="break-all font-mono">definition_id={{ card.latestRoute?.definition_id || '缺失' }} · definition_revision={{ card.latestRoute?.definition_revision || '缺失' }} · node_path={{ card.latestRoute?.node_path || '缺失' }}</p>
                    </div>
                  </details>
                  <details v-if="card.variants.length" class="mt-1">
                    <summary class="cursor-pointer text-[10px] text-ink-4">展开路由变体与来源（{{ card.variants.length }} 种；进程 {{ card.processCount }} 次，历史 {{ card.historyCount }} 条）</summary>
                    <div class="mt-2 space-y-2">
                      <section v-for="(variant, variantIndex) in card.variants" :key="variant.key" class="rounded border border-line-2 bg-surface-2 p-2">
                        <h6 class="font-semibold text-ink-2">变体 {{ variantIndex + 1 }}：{{ routeSummary(variant.latestRecord) }}</h6>
                        <p class="mt-0.5 text-[9px] text-ink-4">当前进程 {{ variant.processCount }} 次 · 历史 {{ variant.historyCount }} 条聚合记录（返回项 {{ variant.historyRecordCount }}）</p>
                        <div v-for="record in variant.records" :key="`${record.catalog_source}:${record.catalog_index}`" class="mt-2 border-t border-line-2 pt-2">
                          <p><strong>{{ record.catalog_source === 'history' ? '历史日志记录' : '当前进程观察' }}</strong><span v-if="record.catalog_source === 'history'"> · {{ sourceReference(record) }}</span><span v-else> · {{ record.count || 0 }} 次，最近 {{ record.last_seen || '时间未提供' }}</span></p>
                          <p v-if="record.workflow_name">关联工作流标题：{{ record.workflow_name }}<span v-if="record.catalog_source === 'history'"> · {{ sourceReference(record, 'activity_source') }}</span></p>
                          <p v-if="record.catalog_source === 'history' && record.activity_source_file">关联 activity：{{ sourceReference(record, 'activity_source') }}</p>
                          <p>selected_duty={{ record.selected_duty || '未提供' }} · task_type={{ record.task_type || '未提供' }} · source={{ record.selection_source || '未提供' }} · target={{ observedTargetLabel(record) }} · effort={{ record.effective_effort || '上游默认' }}</p>
                          <p v-if="record.unknown_reason" class="text-warning-fg">路由原因：{{ routeReasonText(record.unknown_reason) }}；原始码：<code>{{ record.unknown_reason }}</code></p>
                          <template v-if="record.catalog_source === 'history'">
                            <p class="mt-1">关联会话原始请求：{{ record.session_excerpt_status_detail || '后端未提供节选状态说明。' }}。该节选是关联会话的原始请求信息，不是节点职责定义，也不参与路由。</p>
                            <p v-if="record.session_excerpt_status === 'available' && record.session_excerpt" class="mt-1 whitespace-pre-wrap break-words rounded bg-surface px-2 py-1 text-ink-3">{{ record.session_excerpt }}</p>
                            <p v-else-if="record.session_excerpt_status === 'available'" class="mt-1 text-ink-4">节选状态为 available，但没有可显示文本。</p>
                            <p v-if="record.excerpt_source_file" class="mt-1 text-[9px] text-ink-4">节选来源：{{ sourceReference(record, 'excerpt_source') }}</p>
                          </template>
                        </div>
                      </section>
                    </div>
                  </details>
                  <div v-if="card.identityComplete" class="mt-2 space-y-2">
                    <div v-if="nodeOverrideForObservation(card.latestRoute)" class="flex flex-wrap items-center justify-between gap-2 rounded border border-warning-tint-line bg-warning-tint px-2 py-1.5 text-warning-fg">
                      <span>草稿已有独立规则：{{ targetTitle(nodeOverrideForObservation(card.latestRoute)) }} / {{ describeEffort(nodeOverrideForObservation(card.latestRoute).reasoning_effort) }}。可快捷更新；切换到职责会删除此身份的独立覆盖。<template v-if="isPathOnlyNodeIdentity(card.latestRoute) && nodeBindingForObservation(card.latestRoute)">更新 path-only 独立规则时也会替换并删除同身份职责绑定。</template></span>
                      <button type="button" class="shrink-0 rounded border border-warning-tint-line bg-surface px-2 py-1 text-[10px] text-ink-2" @click="openNodeRuleEditor(nodeOverrideForObservation(card.latestRoute), nodeOverrideIndex(card.latestRoute))">编辑独立规则</button>
                    </div>
                    <div class="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto]">
                      <select :value="quickSelectionFor(card)" class="ui-control-h ui-control min-w-0 px-2 text-[10px] text-ink" @change="setQuickSelection(card, $event.target.value)">
                        <option value="">选择快捷规则目标</option>
                        <optgroup label="沿用职责配置并写入职责绑定">
                          <option v-for="option in quickDutyOptions" :key="option.value" :value="option.value" :disabled="option.disabled" :title="option.modelId">{{ option.label }}{{ option.disabled ? `（${option.reason}）` : '' }}</option>
                        </optgroup>
                        <optgroup label="启用模型或有效分组并添加 / 更新独立规则">
                          <option v-for="option in quickTargetOptions" :key="option.id" :value="`target:${option.id}`" :disabled="option.disabled" :title="option.id">{{ option.label }} · {{ describeEffort(option.effort) }}{{ option.disabled ? '（默认等级不可用）' : '' }}</option>
                        </optgroup>
                      </select>
                      <button type="button" class="rounded-md border border-line-2 bg-surface px-3 py-2 text-[10px] text-ink-2 disabled:cursor-not-allowed disabled:opacity-50" :disabled="!quickSelectionFor(card) || quickRuleValidationErrors(card.latestRoute, quickSelectionFor(card)).length > 0" @click="applyQuickRule(card)">{{ quickActionLabel(card) }}</button>
                    </div>
                    <template v-if="quickSelectionFor(card)">
                      <p v-if="quickSelectionDetails(quickSelectionFor(card))?.kind === 'binding'" class="text-[10px]" :title="quickSelectionDetails(quickSelectionFor(card)).modelId">将写入职责绑定 {{ quickSelectionDetails(quickSelectionFor(card)).duty.label }}；目标与等级沿用当前职责配置：{{ quickSelectionDetails(quickSelectionFor(card)).targetLabel }} / {{ describeEffort(quickSelectionDetails(quickSelectionFor(card)).effort) }}。<span v-if="nodeOverrideForObservation(card.latestRoute)" class="text-warning-fg">提交后会删除同身份独立覆盖。</span></p>
                      <p v-else-if="quickSelectionDetails(quickSelectionFor(card))?.kind === 'override'" class="text-[10px]" :title="quickSelectionDetails(quickSelectionFor(card)).modelId">{{ nodeOverrideForObservation(card.latestRoute) ? '将更新该身份已有独立规则' : '将添加独立节点规则' }}：{{ quickSelectionDetails(quickSelectionFor(card)).targetLabel }} / {{ describeEffort(quickSelectionDetails(quickSelectionFor(card)).effort) }}；等级取目标配置的默认值。<span v-if="isPathOnlyNodeIdentity(card.latestRoute) && nodeBindingForObservation(card.latestRoute)" class="text-warning-fg">应用时会替换并删除同身份职责绑定。</span><span v-else-if="nodeBindingForObservation(card.latestRoute)" class="text-warning-fg">完整三元组下保留职责绑定，由独立规则优先。</span></p>
                      <p v-if="quickRuleValidationErrors(card.latestRoute, quickSelectionFor(card)).length" class="text-danger" role="alert">{{ quickRuleValidationErrors(card.latestRoute, quickSelectionFor(card)).join('；') }}</p>
                    </template>
                    <p v-if="nodeBindingForObservation(card.latestRoute)" class="text-[9px] text-ink-4">当前草稿已有职责绑定 {{ nodeBindingForObservation(card.latestRoute).duty }}；<template v-if="isPathOnlyNodeIdentity(card.latestRoute)">选择独立目标后会替换并删除此绑定。</template><template v-else>完整三元组下由独立规则优先，绑定仍保留。</template></p>
                  </div>
                  <div v-else class="mt-2 rounded border border-warning-tint-line bg-warning-tint px-2 py-1.5 text-warning-fg">
                    <p>没有可用的节点路径身份；此卡只代表上下文，不能创建节点绑定或覆盖。</p>
                    <ul class="mt-1 list-disc pl-4"><li v-for="error in nodeIdentityErrors(card.latestRoute)" :key="error">{{ error }}</li></ul>
                    <p class="mt-1">{{ cardTargetScopeText(card) }}</p>
                    <button type="button" class="mt-1 rounded border border-warning-tint-line bg-surface px-2.5 py-1 text-[10px] text-ink-2" @click="configureCardTarget(card)">{{ cardCurrentDuty(card) ? `定位职责目标：${cardCurrentDuty(card).label}` : '定位默认目标' }}</button>
                  </div>
                </article>
                <p v-if="catalogCards.length && !filteredCatalogCards.length" class="mt-3 text-[11px] text-ink-4">当前搜索和筛选条件没有匹配卡片。</p>
              </div>
              <p v-else-if="!observationsError && !historyError && evidenceLoaded" class="mt-3 text-[11px] text-ink-4">进程观察与历史日志均未返回路由记录。</p>
              <p v-else-if="observationsError || historyError" class="mt-3 text-[11px] text-warning-fg">至少一个证据来源读取失败，当前不能判断没有节点。</p>
              <div v-if="config.node_bindings.length" class="mt-3 border-t border-line-2 pt-3">
                <h5 class="mb-2 text-[11px] font-semibold text-ink-2">已保存职责绑定</h5>
                <div v-for="item in config.node_bindings" :key="observationIdentity(item)" class="flex flex-wrap items-center justify-between gap-2 py-1 text-[10px]">
                  <div class="min-w-0">
                    <div><strong class="text-ink-2">{{ nodeTitle(item) }}</strong><span v-if="nodeWorkflowName(item)" class="ml-1 text-ink-4">· {{ nodeWorkflowName(item) }}</span> → {{ duties.find((duty) => duty.key === item.duty)?.label || item.duty }}</div>
                    <div class="mt-0.5">用途：{{ nodeUsage(item) }} · 依据：{{ nodeEvidenceLabel(item) }}<span v-if="evidenceProvenance(item)"> · {{ evidenceProvenance(item) }}</span></div>
                    <div class="mt-0.5" :title="bindingTargetFullId(item)">职责目标与等级：{{ bindingTargetLabel(item) }}</div>
                    <details class="mt-1"><summary class="cursor-pointer text-[9px] text-ink-4">核对{{ isPathOnlyNodeIdentity(item) ? '路径规则身份' : '完整身份' }}</summary><div class="break-all font-mono">definition_id={{ item.definition_id || '（path-only）' }} · definition_revision={{ item.definition_revision || '（path-only）' }} · node_path={{ item.node_path }}</div><p v-if="isPathOnlyNodeIdentity(item)" class="mt-1 text-[9px] text-warning-fg">跨工作流定义匹配完全相同的 node_path；iter-N 按原值匹配。</p></details>
                  </div>
                  <button type="button" class="text-danger" @click="removeNodeBinding(item)">删除</button>
                </div>
              </div>
              <div class="mt-3 border-t border-line-2 pt-3">
                <div class="flex flex-wrap items-center justify-between gap-2">
                  <h5 class="text-[11px] font-semibold text-ink-2">自定义节点规则（{{ config.node_overrides.length }}）</h5>
                  <button type="button" class="rounded border border-line-2 bg-surface px-2.5 py-1 text-[10px] text-ink-2" @click="addNodeRule">新增规则</button>
                </div>
                <p class="mt-1 text-[10px] leading-4 text-ink-4">完整三元组规则按身份精确匹配，同身份独立覆盖优先于职责绑定；path-only 规则仅在 definition 身份缺失时按 node_path 匹配，切换为独立目标会替换并删除同身份职责绑定。完整三元组与 path-only 身份共用路径会让缺少 definition 身份的请求歧义并使用默认目标，新增或编辑规则时会阻止此类冲突；不同完整三元组之间可共用路径。iter-N 按原值精确匹配，不做自动删除。观察状态按对应身份匹配本进程记录：“本进程尚未观察”不代表节点不存在，也不表示当前仍在运行或新规则已生效。</p>
                <p class="mt-1 text-[10px] leading-4 text-ink-4">外层保存后，新执行实例采用新规则；进行中的实例继续沿用已有目标。</p>
                <p v-if="config.node_overrides.length" class="mt-1 text-[10px] leading-4 text-warning-fg">path-only 独立规则会替换同身份职责绑定；完整三元组的独立覆盖优先于同身份职责绑定，绑定仍保留。</p>
                <div v-for="(item, index) in config.node_overrides" :key="observationIdentity(item)" class="flex flex-wrap items-center justify-between gap-2 py-1 text-[10px]">
                  <div class="min-w-0">
                    <div><strong class="text-ink-2">{{ nodeTitle(item) }}</strong><span v-if="nodeWorkflowName(item)" class="ml-1 text-ink-4">· {{ nodeWorkflowName(item) }}</span> → {{ targetTitle(item) }} / {{ describeEffort(item.reasoning_effort) }}</div>
                    <div class="mt-0.5">用途：{{ nodeUsage(item) }} · 依据：{{ nodeEvidenceLabel(item) }}<span v-if="evidenceProvenance(item)"> · {{ evidenceProvenance(item) }}</span></div>
                    <div class="mt-0.5 text-[9px] text-ink-4">{{ observationsError ? '观察状态未知（读取失败）' : hasCurrentProcessObservation(item) ? '本进程已观察' : '本进程尚未观察' }}</div>
                    <details class="mt-1"><summary class="cursor-pointer text-[9px] text-ink-4">核对{{ isPathOnlyNodeIdentity(item) ? '路径规则身份' : '完整身份' }}</summary><div class="break-all font-mono">definition_id={{ item.definition_id || '（path-only）' }} · definition_revision={{ item.definition_revision || '（path-only）' }} · node_path={{ item.node_path }}</div><p v-if="isPathOnlyNodeIdentity(item)" class="mt-1 text-[9px] text-warning-fg">跨工作流定义匹配完全相同的 node_path；iter-N 按原值匹配。</p></details>
                  </div>
                  <div class="flex shrink-0 items-center gap-2">
                    <button type="button" class="rounded border border-line-2 bg-surface px-2 py-1 text-[10px] text-ink-2" @click="openNodeRuleEditor(item, index)">编辑</button>
                    <button type="button" class="text-danger" @click="removeNodeOverride(item)">删除</button>
                  </div>
                </div>
                <p v-if="!config.node_overrides.length" class="mt-2 text-[10px] text-ink-4">尚无自定义节点规则。可手工新增完整身份或 path-only 规则。</p>
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
  <AutoNodeRuleEditor
    :open="nodeRuleEditorOpen"
    :models="models"
    :groups="groups"
    :bindings="config.node_bindings"
    :rules="config.node_overrides"
    :editing-index="editingNodeRuleIndex"
    :draft="nodeRuleDraft"
    @apply="applyNodeRule"
    @close="closeNodeRuleEditor"
  />
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
              <h3 class="font-semibold text-ink">{{ activeDescription.activityOnly ? '活动线索（未关联 Auto 路由）' : '实际路由职责' }}</h3>
              <p v-if="activeDescription.activityOnly" class="mt-1 text-ink-3">该日志活动没有同会话、同工作流的 Auto 路由证据；它不能说明实际路由职责或默认目标。</p>
              <p v-else class="mt-1 break-words text-base font-semibold text-ink">{{ activeDescription.observation.selected_duty || '未确认' }}<span v-if="activeObservedDuty?.label && activeObservedDuty.label !== activeDescription.observation.selected_duty" class="ml-1 text-sm font-normal text-ink-3">（{{ activeObservedDuty.label }}）</span></p>
              <p v-if="!activeDescription.activityOnly && activeDescription.observation.selected_duty" class="mt-1 text-ink-3">{{ activeObservedDuty?.description?.purpose || '观察记录保留了该 selected_duty 值，但没有匹配到已知职责说明。' }}</p>
              <p v-else-if="activeDescription.activityOnly" class="mt-1 text-[11px] text-ink-4">活动记录的名称或工作流标题只作参考，不参与职责判断。</p>
              <p v-else-if="isObservedCustomRule(activeDescription.observation)" class="mt-1 text-[11px] text-ink-4">该观察没有 selected_duty；source=node_override 或 node_override_path 表示请求匹配了自定义节点规则。职责未确认不代表规则未生效。</p>
              <p v-else class="mt-1 text-[11px] text-ink-4">当前实际用途未确认。节点名称和 task_type 只作参考，不会被用来推断路由职责。</p>
            </section>
            <section>
              <h3 class="font-semibold text-ink">{{ activeDescription.activityOnly ? '活动上下文' : '节点与路由来源' }}</h3>
              <dl class="mt-2 grid gap-x-3 gap-y-1 sm:grid-cols-[max-content_minmax(0,1fr)]">
                <dt class="text-ink-4">node_id</dt><dd class="break-all font-mono text-ink-3">{{ activeDescription.observation.node_id || '缺失' }}</dd>
                <template v-if="activeDescription.activityOnly">
                  <dt class="text-ink-4">workflow_id</dt><dd class="break-all font-mono text-ink-3">{{ activeDescription.observation.workflow_id || '未提供' }}</dd>
                  <dt class="text-ink-4">conversation_id</dt><dd class="break-all font-mono text-ink-3">{{ activeDescription.observation.conversation_id || '未提供' }}</dd>
                  <dt class="text-ink-4">活动上下文状态</dt><dd class="break-words text-ink-3">{{ activeDescription.observation.context_status || '未提供' }}</dd>
                </template>
                <template v-else>
                  <dt class="text-ink-4">task_type</dt><dd class="break-words text-ink-3">{{ activeDescription.observation.task_type || '未提供' }}</dd>
                  <dt class="text-ink-4">source</dt><dd class="break-words text-ink-3">{{ activeDescription.observation.selection_source || '未提供' }}</dd>
                  <dt class="text-ink-4">unknown_reason</dt><dd class="break-words text-ink-3">{{ activeDescription.observation.unknown_reason ? routeReasonText(activeDescription.observation.unknown_reason) : '未提供' }}</dd>
                  <dt class="text-ink-4">unknown_reason 原始码</dt><dd class="break-all font-mono text-ink-3">{{ activeDescription.observation.unknown_reason || '无' }}</dd>
                </template>
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
              <h3 class="font-semibold text-ink">节点用途参考（不决定职责）</h3>
              <p class="mt-1 text-ink-3"><code class="font-mono">{{ activeNodeDescription?.nodeId || activeDescription.observation.node_id || '节点名未提供' }}</code>：{{ activeNodeDescription?.known && activeNodeDescription?.usage ? activeNodeDescription.usage : '用途待确认' }}</p>
              <p v-if="activeDescription.activityOnly" class="mt-1 text-[11px] text-ink-4">活动日志中的名称/标题仅作用途参考；本节点没有已关联的 Auto 路由职责。</p>
              <p v-else class="mt-1 text-[11px] text-ink-4">说明依据：{{ activeNodeDescription?.evidenceLabel || '说明依据不足' }}。用途只供核对，不能代替上方的实际 selected_duty 或用于推断路由。</p>
              <p class="mt-2 text-[11px] text-ink-4">同名节点可能来自不同工作流；以上说明按完整身份和可用任务声明解释，不按裸 node_id 归并。此处只解释，不改变职责、模型选择或配置。</p>
            </section>
          </div>
        </template>
      </div>
    </div>
  </dialog>
</template>
