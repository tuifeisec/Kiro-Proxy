const IDENTITY_FIELDS = Object.freeze([
  { key: 'definition_id', label: 'definition_id', maxBytes: 1024 },
  { key: 'definition_revision', label: 'definition_revision', maxBytes: 256 },
  { key: 'node_path', label: 'node_path', maxBytes: 1024 },
])

const CONTEXT_FIELDS = Object.freeze([
  'workflow_id',
  'conversation_id',
  'execution_id',
  'workflow_metadata_version',
  'workflow_stage',
  'workflow_stage_source',
  'workflow_stage_status',
  'workflow_stage_provided',
  'selected_stage',
  'agent_task_type',
  'agent_mode',
  'chat_trigger_type',
  'task_type',
  'node_id',
  'definition_id',
  'definition_revision',
  'node_path',
])

const PRIMARY_CONTEXT_FIELDS = Object.freeze([
  'workflow_id',
  'conversation_id',
  'execution_id',
  'workflow_metadata_version',
  'workflow_stage',
  'workflow_stage_source',
  'workflow_stage_status',
  'workflow_stage_provided',
  'selected_stage',
  'agent_task_type',
  'agent_mode',
  'chat_trigger_type',
  'task_type',
  'definition_id',
  'definition_revision',
  'node_path',
])

const ROUTE_FIELDS = Object.freeze([
  ...CONTEXT_FIELDS,
  'workflow_name',
  'selected_duty',
  'selection_source',
  'unknown_reason',
  'target_group_id',
  'target_group_name',
  'model_config_id',
  'outbound_model',
  'effective_effort',
  'channel_identity',
  'lock_scope',
])

const DIAGNOSTIC_VARIANT_FIELDS = Object.freeze([
  'agent_mode',
  'workflow_stage',
  'selected_stage',
  'agent_task_type',
  'chat_trigger_type',
  'task_type',
  'selected_duty',
  'selection_source',
  'unknown_reason',
  'target_group_id',
  'target_group_name',
  'model_config_id',
  'outbound_model',
  'effective_effort',
  'channel_identity',
  'lock_scope',
])

const NODE_EVIDENCE_FIELDS = Object.freeze([
  'node_id',
  'node_path',
  'definition_id',
  'definition_revision',
])

function textValue(record, key) {
  const value = record?.[key]
  if (value === null || value === undefined) return ''
  if (typeof value === 'string') return value
  if (typeof value === 'boolean' || typeof value === 'number') return String(value)
  return ''
}

export function nodeIdentityErrors(record) {
  const errors = []
  for (const field of IDENTITY_FIELDS) {
    const value = record?.[field.key]
    if (typeof value !== 'string') {
      errors.push(`${field.label} 必须是字符串，不能创建快捷规则。`)
      continue
    }
    if (field.key === 'node_path' && !value) errors.push('node_path 缺失，不能创建快捷规则。')
    const hasBoundaryNEL = value.charCodeAt(0) === 0x85 || value.charCodeAt(value.length - 1) === 0x85
    if (value !== value.trim() || hasBoundaryNEL) errors.push(`${field.label} 含首尾空白，不能创建快捷规则。`)
    if (/[\0\r\n]/.test(value)) errors.push(`${field.label} 含 NUL、CR 或 LF，不能创建快捷规则。`)
    if (new TextEncoder().encode(value).length > field.maxBytes) {
      errors.push(`${field.label} 超过 ${field.maxBytes} 个 UTF-8 字节，不能创建快捷规则。`)
    }
  }
  if (typeof record?.definition_id === 'string' && typeof record?.definition_revision === 'string' &&
    Boolean(record.definition_id) !== Boolean(record.definition_revision)) {
    errors.push('definition_id 与 definition_revision 必须同时存在；两者都为空时按 node_path 配置。')
  }
  return [...new Set(errors)]
}

export function autoNodeIdentityKey(record) {
  if (nodeIdentityErrors(record).length) return null
  return JSON.stringify(IDENTITY_FIELDS.map(({ key }) => record[key]))
}

function contextValues(record) {
  return CONTEXT_FIELDS.map((key) => textValue(record, key))
}

function hasContext(record) {
  return PRIMARY_CONTEXT_FIELDS.some((key) => textValue(record, key) !== '')
}

function contextKey(record) {
  return hasContext(record) ? JSON.stringify(contextValues(record)) : null
}

function routeVariantKey(record, fields = ROUTE_FIELDS) {
  return JSON.stringify(fields.map((key) => textValue(record, key)))
}

function sourceLocation(record) {
  const file = textValue(record, 'source_file')
  const line = textValue(record, 'source_line')
  const offset = textValue(record, 'source_byte_offset')
  return [file, line, offset].join('\u0000')
}

function recordTimestamp(record) {
  for (const key of ['last_seen', 'timestamp', 'time', 'created_at']) {
    const value = textValue(record, key)
    if (!value) continue
    const timestamp = Date.parse(value)
    if (Number.isFinite(timestamp)) return timestamp
  }
  return null
}

function historyOrder(record) {
  const file = textValue(record, 'source_file')
  const line = Number(record?.source_line)
  const currentFile = !/\.1(?:\.|$)/.test(file)
  return [currentFile ? 1 : 0, Number.isFinite(line) ? line : -1]
}

function compareRecency(left, right) {
  const leftTimestamp = recordTimestamp(left)
  const rightTimestamp = recordTimestamp(right)
  if (leftTimestamp !== null || rightTimestamp !== null) {
    if (leftTimestamp === null) return -1
    if (rightTimestamp === null) return 1
    if (leftTimestamp !== rightTimestamp) return leftTimestamp - rightTimestamp
  }
  if (left?.catalog_source !== right?.catalog_source) {
    if (left?.catalog_source === 'process') return 1
    if (right?.catalog_source === 'process') return -1
  }
  const leftOrder = historyOrder(left)
  const rightOrder = historyOrder(right)
  if (leftOrder[0] !== rightOrder[0]) return leftOrder[0] - rightOrder[0]
  return leftOrder[1] - rightOrder[1]
}

function latest(records) {
  return [...records].sort(compareRecency).at(-1) || null
}

function addRecord(cardsByKey, record, source, index) {
  const identityKey = autoNodeIdentityKey(record)
  const context = contextKey(record)
  const provenance = source === 'history'
    ? sourceLocation(record) || String(index)
    : String(index)
  const key = identityKey
    ? `identity:${identityKey}`
    : context
      ? `context:${context}`
      : `unidentified:${source}:${provenance}`
  let card = cardsByKey.get(key)
  if (!card) {
    card = {
      key,
      identityKey,
      contextKey: context,
      identityComplete: Boolean(identityKey),
      contextOnly: !identityKey && Boolean(context),
      records: [],
      variants: [],
    }
    cardsByKey.set(key, card)
  }
  card.records.push({ ...record, catalog_source: source, catalog_index: index })
}

function finishCard(card, variantFields = ROUTE_FIELDS) {
  card.processRecords = card.records.filter((record) => record.catalog_source === 'process')
  card.historyRecords = card.records.filter((record) => record.catalog_source === 'history')
  card.processCount = card.processRecords.reduce((total, record) => total + observationCount(record), 0)
  card.historyCount = card.historyRecords.reduce((total, record) => total + observationCount(record), 0)

  const variants = new Map()
  for (const record of card.records) {
    const key = routeVariantKey(record, variantFields)
    let variant = variants.get(key)
    if (!variant) {
      variant = { key, records: [], processRecords: [], historyRecords: [] }
      variants.set(key, variant)
    }
    variant.records.push(record)
    if (record.catalog_source === 'history') variant.historyRecords.push(record)
    else variant.processRecords.push(record)
  }
  card.variants = [...variants.values()].map((variant) => ({
    ...variant,
    latestRecord: latest(variant.records),
    processCount: variant.processRecords.reduce((total, record) => {
      return total + observationCount(record)
    }, 0),
    historyCount: variant.historyRecords.reduce((total, record) => total + observationCount(record), 0),
    historyRecordCount: variant.historyRecords.length,
  })).sort((left, right) => compareRecency(left.latestRecord, right.latestRecord))

  card.latestProcess = latest(card.processRecords)
  card.latestHistory = latest(card.historyRecords)
  card.latestRoute = latest(card.records) || null
  const titledHistory = card.historyRecords.filter((record) => textValue(record, 'workflow_name'))
  card.evidenceRecord = latest(titledHistory) || card.latestRoute
  card.displayRecord = card.evidenceRecord || card.latestRoute || {}
  card.records.sort(compareRecency)
  return card
}

function observationCount(record) {
  const count = Number(record?.count)
  return Number.isFinite(count) && count >= 0 ? count : 1
}

export function buildAutoNodeCatalog(processObservations = [], historyItems = []) {
  const cardsByKey = new Map()
  processObservations.forEach((record, index) => {
    if (record && typeof record === 'object' && !Array.isArray(record) && hasNodeEvidence(record)) {
      addRecord(cardsByKey, record, 'process', index)
    }
  })
  historyItems.forEach((record, index) => {
    if (record && typeof record === 'object' && !Array.isArray(record) && hasNodeEvidence(record)) {
      addRecord(cardsByKey, record, 'history', index)
    }
  })
  return [...cardsByKey.values()].map((card) => finishCard(card))
}

function hasNodeEvidence(record) {
  return NODE_EVIDENCE_FIELDS.some((key) => {
    const value = record?.[key]
    return value !== undefined && value !== null && (typeof value !== 'string' || value !== '')
  })
}

function diagnosticGroupKey(record) {
  return JSON.stringify(DIAGNOSTIC_VARIANT_FIELDS.map((key) => textValue(record, key)))
}

export function buildAutoRequestDiagnostics(processObservations = [], historyItems = []) {
  const cardsByKey = new Map()
  const append = (records, source) => {
    records.forEach((record, index) => {
      if (!record || typeof record !== 'object' || Array.isArray(record) || hasNodeEvidence(record)) return
      const key = `request:${diagnosticGroupKey(record)}`
      let card = cardsByKey.get(key)
      if (!card) {
        card = {
          key,
          identityKey: null,
          contextKey: null,
          identityComplete: false,
          contextOnly: false,
          requestDiagnostic: true,
          records: [],
          variants: [],
        }
        cardsByKey.set(key, card)
      }
      card.records.push({ ...record, catalog_source: source, catalog_index: index })
    })
  }
  append(processObservations, 'process')
  append(historyItems, 'history')

  return [...cardsByKey.values()].map((card) => {
    finishCard(card, DIAGNOSTIC_VARIANT_FIELDS)
    card.conversationCount = new Set(card.records
      .map((record) => textValue(record, 'conversation_id'))
      .filter(Boolean)).size
    return card
  })
}

function hasSelectedTarget(record) {
  return ['target_group_id', 'target_group_name', 'model_config_id', 'outbound_model']
    .some((key) => textValue(record, key) !== '')
}

export function autoRouteSelectionLabel(record) {
  const source = textValue(record, 'selection_source')
  if (source === 'default' || source === 'legacy_default') {
    return hasSelectedTarget(record) ? '已选择默认目标' : '选模结果未提供'
  }
  if (['node_override', 'node_override_path'].includes(source)) return '已匹配独立规则'
  if (['node_binding', 'node_binding_path', 'task_type'].includes(source)) return '已匹配职责规则'
  if (['legacy_stage', 'legacy_node_stage', 'legacy_orchestration'].includes(source)) return '已匹配阶段规则'
  if (source) return '选模来源未识别'
  if (hasSelectedTarget(record)) return '已提供目标，来源未提供'
  return '选模结果未提供'
}

export function autoNodeContextEntries(record) {
  const labels = [
    ['workflow_id', 'workflowId'],
    ['workflow_name', '工作流标题'],
    ['conversation_id', 'conversationId'],
    ['execution_id', 'executionId'],
    ['workflow_metadata_version', '工作流元数据版本'],
    ['workflow_stage', '工作流阶段'],
    ['workflow_stage_source', '工作流阶段来源'],
    ['workflow_stage_status', '工作流阶段状态'],
    ['workflow_stage_provided', '请求是否提供阶段'],
    ['selected_stage', '选中阶段'],
    ['agent_task_type', 'Agent任务类型'],
    ['agent_mode', 'Agent模式'],
    ['chat_trigger_type', '触发类型'],
    ['task_type', '请求携带的职责参考'],
    ['node_id', 'node_id（名称参考）'],
  ]
  return labels.flatMap(([key, label]) => {
    const value = textValue(record, key)
    return value ? [{ key, label, value }] : []
  })
}
