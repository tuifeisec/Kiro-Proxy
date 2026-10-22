import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import {
  applyNodeBindingRule,
  applyNodeOverrideRule,
  emptyAutoDutyConfig,
  pathOnlyNodeRuleConflictMessage,
  nodeIdentityComplete,
  observationIdentity,
  parseAutoDutyConfig,
} from '../src/utils/autoDutyConfig.js'
import {
  autoNodeIdentityKey,
  buildAutoNodeCatalog,
  nodeIdentityErrors,
} from '../src/utils/autoNodeCatalog.js'

const pathOnly = {
  definition_id: '',
  definition_revision: '',
  node_path: 'loop/review',
}

test('path-only activity identity can be configured, grouped, and recognized', () => {
  assert.equal(nodeIdentityComplete(pathOnly), true)
  assert.deepEqual(nodeIdentityErrors(pathOnly), [])
  assert.equal(autoNodeIdentityKey(pathOnly), JSON.stringify(['', '', 'loop/review']))

  const cards = buildAutoNodeCatalog([
    { ...pathOnly, conversation_id: 'session-one', workflow_id: 'workflow-one', workflow_name: 'First' },
    { ...pathOnly, conversation_id: 'session-two', workflow_id: 'workflow-two', workflow_name: 'Second' },
  ])
  assert.equal(cards.length, 1)
  assert.equal(cards[0].identityComplete, true)
  assert.equal(cards[0].identityKey, autoNodeIdentityKey(pathOnly))
})

test('path-only rules keep separate keys from full identities and reject partial definitions', () => {
  const pathOnlyKey = observationIdentity(pathOnly)
  const fullKey = observationIdentity({ ...pathOnly, definition_id: 'workflow', definition_revision: 'rev-1' })
  assert.notEqual(pathOnlyKey, fullKey)

  for (const invalid of [
    { ...pathOnly, definition_id: 'workflow' },
    { ...pathOnly, definition_revision: 'rev-1' },
    { ...pathOnly, node_path: '' },
  ]) {
    assert.equal(nodeIdentityComplete(invalid), false)
    assert.notDeepEqual(nodeIdentityErrors(invalid), [])
  }
})

test('path-only rules round-trip and duplicate path-only rules are rejected', () => {
  const config = emptyAutoDutyConfig()
  config.node_bindings.push({ ...pathOnly, duty: 'code_review' })
  config.node_overrides.push({ ...pathOnly, model_id: 'model-a', reasoning_effort: '' })
  const parsed = parseAutoDutyConfig(JSON.stringify(config)).config
  assert.deepEqual(parsed.node_bindings[0], { ...pathOnly, duty: 'code_review' })
  assert.deepEqual(parsed.node_overrides[0], { ...pathOnly, model_id: 'model-a', reasoning_effort: '' })

  config.node_bindings.push({ ...pathOnly, duty: 'design' })
  assert.throws(() => parseAutoDutyConfig(JSON.stringify(config)), /重复节点身份/)
})

test('path-only independent targets replace only the same-identity duty binding and upsert model or group', () => {
  const otherIdentity = { definition_id: 'workflow-a', definition_revision: 'rev-a', node_path: pathOnly.node_path }
  const config = emptyAutoDutyConfig()
  config.node_bindings.push(
    { ...pathOnly, duty: 'code_review' },
    { ...otherIdentity, duty: 'design' },
  )

  let next = applyNodeOverrideRule(config, { ...pathOnly, model_id: 'model-a', reasoning_effort: 'high' }, { upsertIdentity: true })
  assert.deepEqual(next.node_bindings, [{ ...otherIdentity, duty: 'design' }])
  assert.deepEqual(next.node_overrides, [{ ...pathOnly, model_id: 'model-a', reasoning_effort: 'high' }])
  assert.equal(config.node_bindings.length, 2, 'the helper does not mutate the original configuration')

  next = applyNodeOverrideRule(next, { ...pathOnly, model_id: 'group-a', reasoning_effort: '' }, { upsertIdentity: true })
  assert.deepEqual(next.node_overrides, [{ ...pathOnly, model_id: 'group-a', reasoning_effort: '' }])
  assert.deepEqual(next.node_bindings, [{ ...otherIdentity, duty: 'design' }], 'other-identity rules are preserved')
})

test('batch and advanced add/edit use the same path-only override replacement helper', async () => {
  const settingsSource = await readFile(new URL('../src/components/dashboard/AutoRoutingSettings.vue', import.meta.url), 'utf8')
  const editorSource = await readFile(new URL('../src/components/dashboard/AutoNodeRuleEditor.vue', import.meta.url), 'utf8')
  const functionBody = (source, name, nextName) => {
    const start = source.indexOf(`function ${name}(`)
    assert.notEqual(start, -1, `missing ${name} in source`)
    const end = source.indexOf(`\nfunction ${nextName}(`, start)
    assert.notEqual(end, -1, `missing ${nextName} after ${name}`)
    return source.slice(start, end)
  }

  const quickSelectionBody = functionBody(settingsSource, 'applyQuickSelection', 'buildBatchCandidate')
  const quickValidationBody = functionBody(settingsSource, 'quickRuleValidationErrors', 'newlyIntroducedAutoDutyIssues')
  const batchBody = functionBody(settingsSource, 'buildBatchCandidate', 'setQuickSelection')
  const advancedBody = functionBody(settingsSource, 'applyNodeRule', 'closeNodeRuleEditor')
  assert.match(quickSelectionBody, /applyNodeOverrideRule\(candidate, item, \{ upsertIdentity: true \}\)/)
  assert.match(quickValidationBody, /pathOnlyNodeRuleConflictMessage\(config\.value, observation\)/)
  assert.match(batchBody, /applyQuickSelection\(candidate, observation, details\)/)
  assert.match(batchBody, /pathOnlyNodeRuleConflictMessage\(candidate, observation\)/)
  assert.match(advancedBody, /applyNodeOverrideRule\(config\.value, item, \{ editingIndex: editingNodeRuleIndex\.value \}\)/)
  assert.match(settingsSource, /:bindings="config\.node_bindings"/)
  assert.match(editorSource, /pathOnlyNodeRuleConflictMessage\(/)

  const config = emptyAutoDutyConfig()
  config.node_bindings.push({ ...pathOnly, duty: 'code_review' })
  let batchCandidate = { ...config, node_bindings: [...config.node_bindings], node_overrides: [...config.node_overrides] }
  for (const target of ['model-a', 'group-a']) {
    batchCandidate = applyNodeOverrideRule(batchCandidate, { ...pathOnly, model_id: target, reasoning_effort: '' }, { upsertIdentity: true })
  }
  assert.deepEqual(batchCandidate.node_bindings, [])
  assert.equal(batchCandidate.node_overrides.length, 1)
  assert.equal(batchCandidate.node_overrides[0].model_id, 'group-a')

  const advancedAdd = applyNodeOverrideRule(config, { ...pathOnly, model_id: 'model-a', reasoning_effort: '' })
  assert.deepEqual(advancedAdd.node_bindings, [])
  assert.equal(advancedAdd.node_overrides.length, 1)
  const advancedEdit = applyNodeOverrideRule(advancedAdd, { ...pathOnly, model_id: 'group-a', reasoning_effort: '' }, { editingIndex: 0 })
  assert.deepEqual(advancedEdit.node_bindings, [])
  assert.deepEqual(advancedEdit.node_overrides, [{ ...pathOnly, model_id: 'group-a', reasoning_effort: '' }])
})

test('switching back to a path-only duty binding removes its independent override', () => {
  const full = { definition_id: 'workflow-a', definition_revision: 'rev-a', node_path: pathOnly.node_path }
  const config = emptyAutoDutyConfig()
  config.node_overrides.push(
    { ...pathOnly, model_id: 'model-a', reasoning_effort: '' },
    { ...full, model_id: 'model-full', reasoning_effort: '' },
  )
  const next = applyNodeBindingRule(config, { ...pathOnly, duty: 'code_review' })
  assert.deepEqual(next.node_bindings, [{ ...pathOnly, duty: 'code_review' }])
  assert.deepEqual(next.node_overrides, [{ ...full, model_id: 'model-full', reasoning_effort: '' }])
})

test('path-only rules report other-identity same-path conflicts while full triples retain override priority', () => {
  const full = { definition_id: 'workflow-a', definition_revision: 'rev-a', node_path: pathOnly.node_path }
  const config = emptyAutoDutyConfig()
  config.node_bindings.push({ ...full, duty: 'design' })
  const message = pathOnlyNodeRuleConflictMessage(config, pathOnly)
  assert.match(message, /path-only 路径规则 node_path=loop\/review/)
  assert.match(message, /definition_id=workflow-a/)
  assert.match(message, /使用默认目标/)

  const editingConfig = emptyAutoDutyConfig()
  editingConfig.node_overrides.push({ ...full, model_id: 'model-a', reasoning_effort: '' })
  assert.equal(pathOnlyNodeRuleConflictMessage(editingConfig, pathOnly, 0), '', 'the edited override itself is excluded')

  const fullConfig = emptyAutoDutyConfig()
  fullConfig.node_bindings.push({ ...full, duty: 'design' })
  const next = applyNodeOverrideRule(fullConfig, { ...full, model_id: 'model-a', reasoning_effort: '' }, { upsertIdentity: true })
  assert.deepEqual(next.node_bindings, [{ ...full, duty: 'design' }])
  assert.deepEqual(next.node_overrides, [{ ...full, model_id: 'model-a', reasoning_effort: '' }])
  assert.equal(pathOnlyNodeRuleConflictMessage(next, full), '', 'full-triple override behavior stays unchanged')
})

test('adding a full triple beside an existing path-only identity reports the symmetric conflict', () => {
  const full = { definition_id: 'workflow-a', definition_revision: 'rev-a', node_path: pathOnly.node_path }
  const pathOnlyConfig = emptyAutoDutyConfig()
  pathOnlyConfig.node_overrides.push({ ...pathOnly, model_id: 'model-a', reasoning_effort: '' })

  const message = pathOnlyNodeRuleConflictMessage(pathOnlyConfig, full)
  assert.match(message, /完整三元组规则/)
  assert.match(message, /path-only identity/)
  assert.match(message, /使用默认目标/)

  const twoFullTriples = emptyAutoDutyConfig()
  twoFullTriples.node_bindings.push(
    { ...full, duty: 'design' },
    { definition_id: 'workflow-b', definition_revision: 'rev-b', node_path: full.node_path, duty: 'code_review' },
  )
  assert.equal(pathOnlyNodeRuleConflictMessage(twoFullTriples, full), '', 'full identities may share node_path')
})

test('actual settings source recognizes successful path overrides as independent rules', async () => {
  const source = await readFile(new URL('../src/components/dashboard/AutoRoutingSettings.vue', import.meta.url), 'utf8')
  const start = source.indexOf('function isObservedCustomRule(')
  const end = source.indexOf('\nfunction hasCustomRuleForObservation(', start)
  assert.ok(start >= 0 && end > start)
  const recognize = new Function(`return (${source.slice(start, end)})`)()
  for (const selection_source of ['node_override', 'node_override_path']) {
    assert.equal(recognize({ selection_source, unknown_reason: '' }), true)
    assert.equal(recognize({ selection_source, unknown_reason: 'node_path_ambiguous' }), false)
  }
  for (const observation of [undefined, {}, { selection_source: 'default' }, { selection_source: 'node_binding_path' }]) {
    assert.equal(recognize(observation), false)
  }
  const filterStart = source.indexOf('function cardIsUnclassified(')
  const filterEnd = source.indexOf('\nfunction catalogSearchText(', filterStart)
  assert.ok(filterStart >= 0 && filterEnd > filterStart)
  const unclassified = new Function('isObservedCustomRule', 'duties', `return (${source.slice(filterStart, filterEnd)})`)(recognize, { value: [] })
  assert.equal(unclassified({ latestRoute: { selection_source: 'node_override_path', unknown_reason: '' } }), false)
  assert.equal(unclassified({ latestRoute: { selection_source: 'default', unknown_reason: 'node_path_ambiguous' } }), true)
})
