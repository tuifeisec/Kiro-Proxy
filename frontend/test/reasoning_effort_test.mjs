import test from 'node:test'
import assert from 'node:assert/strict'
import {
  modelEffortOptions,
  modelReasoningEfforts,
  providerReasoningEfforts,
  sortReasoningEfforts,
} from '../src/utils/reasoningEffort.js'
import { autoTargetEffortOptions } from '../src/utils/autoDutyConfig.js'

test('provider default tables match the backend converter table', () => {
  assert.deepEqual(providerReasoningEfforts('openai'), ['none', 'low', 'medium', 'high', 'xhigh', 'max'])
  assert.deepEqual(providerReasoningEfforts('codex'), ['none', 'low', 'medium', 'high', 'xhigh', 'max'])
  assert.deepEqual(providerReasoningEfforts('anthropic'), ['none', 'low', 'medium', 'high', 'xhigh', 'max'])
  assert.deepEqual(providerReasoningEfforts('gemini'), ['low', 'medium', 'high'])
  assert.deepEqual(providerReasoningEfforts(''), providerReasoningEfforts('openai'))
})

test('declared tiers are sorted, deduplicated and limited to the provider table', () => {
  assert.deepEqual(sortReasoningEfforts(['max', 'LOW', 'high', 'low']), ['low', 'high', 'max'])
  assert.deepEqual(modelReasoningEfforts({ provider: 'openai', supported_reasoning_efforts: ['high', 'low'] }), ['low', 'high'])
  assert.deepEqual(modelReasoningEfforts({ provider: 'anthropic', supported_reasoning_efforts: [] }), ['none', 'low', 'medium', 'high', 'xhigh', 'max'])
  assert.deepEqual(modelReasoningEfforts({ provider: 'gemini', supported_reasoning_efforts: null }), ['low', 'medium', 'high'])
  assert.deepEqual(modelEffortOptions({ provider: 'openai', supported_reasoning_efforts: ['medium'] }), ['', 'medium'])
})

test('Auto target options derive from model effective tiers', () => {
  const models = [
    { id: 'gpt', provider: 'openai', enabled: true, supported_reasoning_efforts: ['low', 'high', 'max'] },
    { id: 'claude', provider: 'anthropic', enabled: true, supported_reasoning_efforts: [] },
  ]
  const groups = [{ id: 'mixed', enabled: true, members: [{ model_id: 'gpt', priority: 0 }, { model_id: 'claude', priority: 1 }] }]
  assert.deepEqual(autoTargetEffortOptions('gpt', models, groups), ['', 'low', 'high', 'max'])
  assert.deepEqual(autoTargetEffortOptions('mixed', models, groups), ['', 'low', 'high', 'max'])
})
