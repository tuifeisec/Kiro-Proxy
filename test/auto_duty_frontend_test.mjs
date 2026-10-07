import test from 'node:test'
import assert from 'node:assert/strict'
import {
  autoTargetDefaultEffort,
  autoTargetEffortOptions,
  autoTargetGroupCandidates,
  emptyAutoDutyConfig,
  observationRecordKey,
  parseAutoDutyConfig,
  resolveAutoTarget,
  validateAutoDutyConfig,
} from '../frontend/src/utils/autoDutyConfig.js'

const models = [
  { id: 'model-enabled', enabled: true, provider: 'openai' },
  { id: 'model-disabled', enabled: false, provider: 'openai' },
]

function configWithOverride(modelId = '', reasoningEffort = '') {
  const config = emptyAutoDutyConfig()
  config.node_overrides = [{
    definition_id: 'workflow-definition',
    definition_revision: 'revision-1',
    node_path: 'review-loop/review',
    model_id: modelId,
    reasoning_effort: reasoningEffort,
  }]
  return parseAutoDutyConfig(JSON.stringify(config)).config
}

test('disabled Auto keeps an empty node override draft editable', () => {
  const config = configWithOverride()
  assert.deepEqual(validateAutoDutyConfig(config, models), [])
  config.default_model_id = 'model-enabled'
  config.duty_models.design = { model_id: 'model-enabled', reasoning_effort: 'high' }
  assert.deepEqual(validateAutoDutyConfig(config, models), [])
  const reloaded = parseAutoDutyConfig(JSON.stringify(config)).config
  assert.equal(reloaded.node_overrides[0].model_id, '')
  assert.deepEqual(validateAutoDutyConfig(reloaded, models), [])
})

test('enabled Auto requires a valid model for the same node override draft', () => {
  const config = configWithOverride()
  config.enabled = true
  config.default_model_id = 'model-enabled'
  for (const target of Object.values(config.duty_models)) target.model_id = 'model-enabled'
  const errors = validateAutoDutyConfig(config, models)
  assert.equal(errors.length, 1)
  assert.match(errors[0], /节点覆盖 1/)
  config.node_overrides[0].model_id = 'model-enabled'
  assert.deepEqual(validateAutoDutyConfig(config, models), [])
})

test('disabled Auto still rejects nonempty unavailable override models and unsupported effort', () => {
  for (const modelId of ['missing-model', 'model-disabled']) {
    const errors = validateAutoDutyConfig(configWithOverride(modelId), models)
    assert.equal(errors.length, 1)
    assert.match(errors[0], /节点覆盖 1/)
  }
  const errors = validateAutoDutyConfig(configWithOverride('model-enabled', 'xhigh'), [
    { id: 'model-enabled', enabled: true, provider: 'anthropic' },
  ])
  assert.equal(errors.length, 1)
  assert.match(errors[0], /思考等级/)
})

test('group UUID stays in v2 targets and expands ordered members plus enabled fallback members', () => {
  const groupModels = [
    { id: 'gpt-openai', enabled: true, provider: 'openai', reasoning_effort: 'high' },
    { id: 'gpt-anthropic', enabled: true, provider: 'anthropic', reasoning_effort: 'high' },
    { id: 'gpt-gemini', enabled: true, provider: 'gemini', reasoning_effort: 'high' },
    { id: 'gpt-disabled', enabled: false, provider: 'openai', reasoning_effort: 'high' },
  ]
  const groups = [
    {
      id: 'group-gpt', name: 'GPT 通用组', enabled: true, reasoning_effort: '', fallback_group_id: 'group-fallback',
      members: [{ model_id: 'gpt-anthropic', priority: 1 }, { model_id: 'gpt-openai', priority: 0 }, { model_id: 'gpt-disabled', priority: 2 }],
    },
    {
      id: 'group-fallback', name: 'GPT 回退组', enabled: true, reasoning_effort: '', fallback_group_id: '',
      members: [{ model_id: 'gpt-gemini', priority: 0 }],
    },
  ]
  const config = emptyAutoDutyConfig()
  config.default_model_id = 'group-gpt'
  config.duty_models.design = { model_id: 'group-gpt', reasoning_effort: 'high' }
  const reloaded = parseAutoDutyConfig(JSON.stringify(config)).config

  assert.equal(resolveAutoTarget('group-gpt', groupModels, groups).kind, 'group')
  assert.deepEqual(autoTargetGroupCandidates('group-gpt', groupModels, groups).map((model) => model.id), [
    'gpt-openai', 'gpt-anthropic', 'gpt-gemini',
  ])
  assert.deepEqual(autoTargetEffortOptions('group-gpt', groupModels, groups), ['', 'low', 'medium', 'high'])
  assert.equal(autoTargetDefaultEffort('group-gpt', groupModels, groups), 'high')
  assert.equal(reloaded.default_model_id, 'group-gpt')
  assert.equal(reloaded.duty_models.design.model_id, 'group-gpt')
  assert.deepEqual(validateAutoDutyConfig(reloaded, groupModels, groups), [])
})

test('group effort defaults follow all candidates and provider intersections, including unknown providers', () => {
  const groupModels = [
    { id: 'gpt-openai', enabled: true, provider: 'openai', reasoning_effort: 'high' },
    { id: 'gpt-anthropic', enabled: true, provider: 'anthropic', reasoning_effort: 'high' },
    { id: 'gpt-unknown', enabled: true, provider: 'custom', reasoning_effort: 'high' },
  ]
  const groups = [{
    id: 'group-mixed', name: '混合 Provider 组', enabled: true, reasoning_effort: '', fallback_group_id: '',
    members: groupModels.map((model, priority) => ({ model_id: model.id, priority })),
  }]

  assert.deepEqual(autoTargetEffortOptions('group-mixed', groupModels, groups), [''])
  assert.equal(autoTargetDefaultEffort('group-mixed', groupModels, groups), 'high')
  const config = emptyAutoDutyConfig()
  config.default_model_id = 'group-mixed'
  const errors = validateAutoDutyConfig(config, groupModels, groups)
  assert.equal(errors.length, 1)
  assert.match(errors[0], /默认固定目标.*思考等级/)

  groups[0].reasoning_effort = 'low'
  assert.equal(autoTargetDefaultEffort('group-mixed', groupModels, groups), 'low')
  assert.match(validateAutoDutyConfig(config, groupModels, groups)[0], /思考等级/)

  groups[0].reasoning_effort = ''
  groupModels[1].reasoning_effort = 'medium'
  assert.equal(autoTargetDefaultEffort('group-mixed', groupModels, groups), '')
  assert.deepEqual(validateAutoDutyConfig(config, groupModels, groups), [])
})

test('disabled, missing, empty-candidate, and model-colliding groups stay invalid targets', () => {
  const groupModels = [
    { id: 'gpt-enabled', enabled: true, provider: 'openai', reasoning_effort: '' },
    { id: 'gpt-disabled', enabled: false, provider: 'openai', reasoning_effort: '' },
  ]
  const groups = [
    { id: 'group-disabled', name: '停用组', enabled: false, reasoning_effort: '', fallback_group_id: '', members: [{ model_id: 'gpt-enabled', priority: 0 }] },
    { id: 'group-empty', name: '空组', enabled: true, reasoning_effort: '', fallback_group_id: '', members: [{ model_id: 'gpt-disabled', priority: 0 }] },
    { id: 'group-collision', name: '冲突组', enabled: true, reasoning_effort: '', fallback_group_id: '', members: [{ model_id: 'gpt-enabled', priority: 0 }] },
  ]
  groupModels.push({ id: 'group-collision', enabled: true, provider: 'openai', reasoning_effort: '' })

  for (const [targetID, expected] of [
    ['group-disabled', /已停用/],
    ['group-empty', /没有有效候选/],
    ['missing-group', /配置不存在/],
    ['group-collision', /ID 同时属于模型配置和分组/],
  ]) {
    const config = emptyAutoDutyConfig()
    config.duty_models.design.model_id = targetID
    assert.match(validateAutoDutyConfig(config, groupModels, groups)[0], expected)
  }
  assert.equal(resolveAutoTarget('group-collision', groupModels, groups).kind, 'collision')
})

test('default model target validates its configured reasoning effort', () => {
  const config = emptyAutoDutyConfig()
  config.default_model_id = 'model-enabled'
  const errors = validateAutoDutyConfig(config, [
    { id: 'model-enabled', enabled: true, provider: 'anthropic', reasoning_effort: 'xhigh' },
  ])
  assert.equal(errors.length, 1)
  assert.match(errors[0], /默认固定目标.*思考等级/)
})

test('observation render keys distinguish logical groups sharing the same actual member', () => {
  const observation = {
    definition_id: 'workflow-definition',
    definition_revision: 'revision-1',
    node_path: 'review-loop/review',
    model_config_id: 'shared-member',
    outbound_model: 'gpt-model',
    effective_effort: 'high',
    channel_identity: 'channel-1',
  }
  const first = observationRecordKey({
    ...observation,
    target_group_id: 'group-one',
    target_group_name: '第一组',
  })
  const second = observationRecordKey({
    ...observation,
    target_group_id: 'group-two',
    target_group_name: '第二组',
  })

  assert.notEqual(first, second)
  assert.notEqual(
    observationRecordKey({ ...observation, target_group_id: 'group-one', target_group_name: '旧名称' }),
    observationRecordKey({ ...observation, target_group_id: 'group-one', target_group_name: '新名称' }),
  )
})
