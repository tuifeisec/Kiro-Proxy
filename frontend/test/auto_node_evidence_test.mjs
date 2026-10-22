import test from 'node:test'
import assert from 'node:assert/strict'
import {
  autoRouteSelectionLabel,
  buildAutoNodeCatalog,
  buildAutoRequestDiagnostics,
  nodeIdentityErrors,
} from '../src/utils/autoNodeCatalog.js'
import { describeAutoRouteReason } from '../src/utils/autoNodeDescriptions.js'

const ordinaryRequest = (overrides = {}) => ({
  workflow_id: 'wf-vibe',
  workflow_name: 'vibe',
  conversation_id: 'conversation-a',
  execution_id: 'execution-a',
  agent_mode: 'vibe',
  selection_source: 'default',
  unknown_reason: 'stable_identity_unavailable',
  model_config_id: 'model-default',
  outbound_model: 'claude-sonnet',
  count: 1,
  ...overrides,
})

test('ordinary vibe observations never become node cards and keep process/history counts separate', () => {
  const process = [
    ordinaryRequest({ conversation_id: 'conversation-a', execution_id: 'exec-1', count: 200 }),
    ordinaryRequest({ conversation_id: 'conversation-b', execution_id: 'exec-2', count: 158 }),
    ordinaryRequest({ conversation_id: 'conversation-c', execution_id: 'exec-3', count: 100 }),
  ]
  const history = [ordinaryRequest({ conversation_id: 'conversation-a', count: 37 })]

  assert.deepEqual(buildAutoNodeCatalog(process, history), [])
  const diagnostics = buildAutoRequestDiagnostics(process, history)
  assert.equal(diagnostics.length, 1)
  assert.equal(diagnostics[0].processCount, 458)
  assert.equal(diagnostics[0].historyCount, 37)
  assert.equal(diagnostics[0].conversationCount, 3, 'same conversation in process and history is counted once')
  assert.equal(diagnostics[0].records.length, 4)
  assert.deepEqual(diagnostics[0].records.map((record) => record.catalog_source).sort(), [
    'history', 'process', 'process', 'process',
  ])
})

test('diagnostics merge equivalent results across sessions but retain result and reason variants', () => {
  const diagnostics = buildAutoRequestDiagnostics([
    ordinaryRequest({ conversation_id: 'one', execution_id: 'exec-one' }),
    ordinaryRequest({ conversation_id: 'two', execution_id: 'exec-two' }),
    ordinaryRequest({ conversation_id: 'three', outbound_model: 'claude-opus' }),
    ordinaryRequest({ conversation_id: 'four', unknown_reason: 'node_path_ambiguous' }),
  ])

  assert.equal(diagnostics.length, 3)
  const sameResult = diagnostics.find((card) => card.records.length === 2)
  assert.ok(sameResult)
  assert.equal(sameResult.conversationCount, 2)
  assert.equal(sameResult.variants.length, 1)
  assert.equal(diagnostics.reduce((sum, card) => sum + card.records.length, 0), 4)
  assert.equal(diagnostics.some((card) => card.records.some((record) => record.outbound_model === 'claude-opus')),
    true)
  assert.equal(diagnostics.some((card) => card.records.some((record) => record.unknown_reason === 'node_path_ambiguous')),
    true)
})

test('node cards retain partial and invalid node evidence but workflow context alone is diagnostic', () => {
  const records = [
    { node_path: 'plan/review', conversation_id: 'path-only' },
    { definition_id: 'workflow', definition_revision: 'rev-1', node_path: 'implement' },
    { node_id: 'node-without-path', conversation_id: 'partial' },
    { definition_id: 'workflow-only' },
    { definition_revision: 'rev-without-id' },
    { node_path: ' bad-path ' },
    { node_path: { malformed: true } },
    { workflow_id: 'workflow-context-only', workflow_name: 'vibe', conversation_id: 'ordinary' },
  ]

  const cards = buildAutoNodeCatalog(records)
  assert.equal(cards.reduce((sum, card) => sum + card.records.length, 0), 7)
  assert.equal(cards.some((card) => card.records.some((record) => record.workflow_id === 'workflow-context-only')), false)
  assert.ok(cards.some((card) => card.records.some((record) => record.node_id === 'node-without-path')))
  assert.ok(cards.some((card) => card.records.some((record) => record.node_path === ' bad-path ')))
  assert.ok(cards.some((card) => card.records.some((record) => typeof record.node_path === 'object')))
  assert.notDeepEqual(nodeIdentityErrors({ definition_id: 'workflow-only', definition_revision: '', node_path: '' }), [])

  const diagnostics = buildAutoRequestDiagnostics(records)
  assert.equal(diagnostics.length, 1)
  assert.equal(diagnostics[0].records.length, 1)
  assert.equal(diagnostics[0].records[0].conversation_id, 'ordinary')
})

test('selection labels use source and target evidence instead of treating unknown_reason as unknown routing', () => {
  assert.equal(autoRouteSelectionLabel({
    selection_source: 'default', model_config_id: 'model-default', unknown_reason: 'stable_identity_unavailable',
  }), '已选择默认目标')
  assert.equal(autoRouteSelectionLabel({
    selection_source: 'legacy_default', target_group_name: 'Default group', unknown_reason: 'stage_unavailable',
  }), '已选择默认目标')
  assert.equal(autoRouteSelectionLabel({ selection_source: 'node_override_path' }), '已匹配独立规则')
  assert.equal(autoRouteSelectionLabel({ selection_source: 'node_binding' }), '已匹配职责规则')
  assert.equal(autoRouteSelectionLabel({ selection_source: 'legacy_node_stage' }), '已匹配阶段规则')
  assert.equal(autoRouteSelectionLabel({
    selection_source: 'future_source', model_config_id: 'model-a',
  }), '选模来源未识别')
  assert.equal(autoRouteSelectionLabel({ selection_source: 'default', unknown_reason: 'stable_identity_unavailable' }),
    '选模结果未提供')
  assert.equal(autoRouteSelectionLabel({ call_succeeded: true }), '选模结果未提供')
})

test('route explanations distinguish missing node identity and ambiguous path from unknown selected target', () => {
  const missing = describeAutoRouteReason('stable_identity_unavailable')
  const ambiguous = describeAutoRouteReason('node_path_ambiguous')
  assert.match(missing, /默认目标/)
  assert.match(missing, /不表示实际选模结果未知/)
  assert.doesNotMatch(missing, /路由未知/)
  assert.match(ambiguous, /路径/)
  assert.doesNotMatch(ambiguous, /路由未知/)
})
