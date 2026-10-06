import test from 'node:test'
import assert from 'node:assert/strict'
import {
  emptyAutoDutyConfig,
  parseAutoDutyConfig,
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
