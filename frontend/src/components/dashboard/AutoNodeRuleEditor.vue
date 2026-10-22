<script setup>
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import {
  autoTargetDefaultEffort,
  autoTargetEffortOptions,
  isAutoTargetSelectable,
  nodeIdentityComplete,
  observationIdentity,
  pathOnlyNodeRuleConflictMessage,
  resolveAutoTarget,
} from '../../utils/autoDutyConfig.js'
import { getAutoNodeDescription } from '../../utils/autoNodeDescriptions.js'

const props = defineProps({
  open: { type: Boolean, default: false },
  models: { type: Array, default: () => [] },
  groups: { type: Array, default: () => [] },
  bindings: { type: Array, default: () => [] },
  rules: { type: Array, default: () => [] },
  editingIndex: { type: Number, default: null },
  draft: {
    type: Object,
    default: () => ({
      definition_id: '',
      definition_revision: '',
      node_path: '',
      model_id: '',
      reasoning_effort: '',
    }),
  },
})

const emit = defineEmits(['apply', 'close'])

const dialog = ref(null)
const firstIdentityInput = ref(null)
const titleId = useId()
const draftValue = ref(emptyDraft())
let restoreFocusTarget = null
let closeRequestedByComponent = false

const editing = computed(() => Number.isInteger(props.editingIndex) && props.editingIndex >= 0)
const nodeDescription = computed(() => getAutoNodeDescription(draftValue.value))
const targetOptions = computed(() => {
  const options = []
  for (const model of props.models) {
    if (isAutoTargetSelectable(model.id, props.models, props.groups)) {
      options.push({ id: model.id, label: modelLabel(model), disabled: false })
    }
  }
  for (const group of props.groups) {
    if (isAutoTargetSelectable(group.id, props.models, props.groups)) {
      options.push({ id: group.id, label: groupLabel(group), disabled: false })
    }
  }
  const currentId = draftValue.value.model_id
  if (currentId && !options.some((option) => option.id === currentId)) {
    options.unshift({
      id: currentId,
      label: `${targetLabel(currentId)}（当前目标不可用，保留原值）`,
      disabled: true,
    })
  }
  return options
})
const effortOptions = computed(() => {
  const allowed = autoTargetEffortOptions(draftValue.value.model_id, props.models, props.groups)
  const current = draftValue.value.reasoning_effort
  return current && !allowed.includes(current) ? [...allowed, current] : allowed
})
const validationErrors = computed(() => {
  const errors = []
  const identityFields = [
    { key: 'definition_id', label: 'definition_id', maxBytes: 1024 },
    { key: 'definition_revision', label: 'definition_revision', maxBytes: 256 },
    { key: 'node_path', label: 'node_path', maxBytes: 1024 },
  ]
  const identityValid = nodeIdentityComplete(draftValue.value)

  for (const field of identityFields) {
    const value = draftValue.value[field.key]
    if (typeof value !== 'string') {
      errors.push(`${field.label}必须是字符串。`)
      continue
    }
    if (field.key === 'node_path' && !value) errors.push('node_path不能为空。')
    const hasNELAtBoundary = value.charCodeAt(0) === 0x85 || value.charCodeAt(value.length - 1) === 0x85
    if (value !== value.trim() || hasNELAtBoundary) errors.push(`${field.label}不能有首尾空白；字段会按原值保存。`)
    if (/[\0\r\n]/.test(value)) errors.push(`${field.label}不能包含 NUL、CR 或 LF。`)
    if (new TextEncoder().encode(value).length > field.maxBytes) {
      errors.push(`${field.label}不能超过 ${field.maxBytes} 个 UTF-8 字节。`)
    }
  }
  if (typeof draftValue.value.definition_id === 'string' && typeof draftValue.value.definition_revision === 'string' &&
    Boolean(draftValue.value.definition_id) !== Boolean(draftValue.value.definition_revision)) {
    errors.push('definition_id 和 definition_revision 必须同时填写；两者都留空时，规则按 node_path 跨工作流定义匹配。')
  }

  if (identityValid && props.rules.some((rule, index) => (
    index !== props.editingIndex &&
    nodeIdentityComplete(rule) &&
    observationIdentity(rule) === observationIdentity(draftValue.value)
  ))) {
    errors.push('相同节点路径身份已存在于其他规则中。')
  }
  if (identityValid) {
    const conflict = pathOnlyNodeRuleConflictMessage({
      node_bindings: props.bindings,
      node_overrides: props.rules,
    }, draftValue.value, props.editingIndex)
    if (conflict) errors.push(conflict)
  }

  if (!draftValue.value.model_id) {
    errors.push('请选择一个已启用的模型或有效分组。')
  } else if (!isAutoTargetSelectable(draftValue.value.model_id, props.models, props.groups)) {
    errors.push('目标必须是已启用且可用的模型或分组。')
  }

  if (typeof draftValue.value.reasoning_effort !== 'string') {
    errors.push('思考等级必须是字符串。')
  } else if (!autoTargetEffortOptions(draftValue.value.model_id, props.models, props.groups).includes(draftValue.value.reasoning_effort)) {
    errors.push('所选思考等级不受该模型或分组的 Provider 能力支持。')
  }
  return errors
})
const canApply = computed(() => validationErrors.value.length === 0)

watch(() => props.open, async (isOpen) => {
  if (!isOpen) {
    if (dialog.value?.open) {
      closeRequestedByComponent = true
      dialog.value.close()
    }
    return
  }

  draftValue.value = copyDraft(props.draft)
  await nextTick()
  if (!props.open || !dialog.value) return
  restoreFocusTarget = document.activeElement
  if (!dialog.value.open) dialog.value.showModal()
  await nextTick()
  firstIdentityInput.value?.focus({ preventScroll: true })
}, { flush: 'post' })

function emptyDraft() {
  return {
    definition_id: '',
    definition_revision: '',
    node_path: '',
    model_id: '',
    reasoning_effort: '',
  }
}

function copyDraft(value) {
  return {
    definition_id: value?.definition_id ?? '',
    definition_revision: value?.definition_revision ?? '',
    node_path: value?.node_path ?? '',
    model_id: value?.model_id ?? '',
    reasoning_effort: value?.reasoning_effort ?? '',
  }
}

function modelLabel(model) {
  return `模型：${model.model_id || model.id} · ${model.id} · ${model.provider || 'openai'}`
}

function groupLabel(group) {
  return `分组：${group.name || group.id} · ${group.id}`
}

function targetLabel(targetId) {
  const target = resolveAutoTarget(targetId, props.models, props.groups)
  if (target.kind === 'model') return modelLabel(target.model)
  if (target.kind === 'group') return groupLabel(target.group)
  if (target.kind === 'collision') return `${targetId}（模型/分组 ID 冲突）`
  return `${targetId}（配置不存在）`
}

function describeEffort(value) {
  return value || '上游默认'
}

function changeTarget(targetId) {
  draftValue.value = {
    ...draftValue.value,
    model_id: targetId,
    reasoning_effort: autoTargetDefaultEffort(targetId, props.models, props.groups),
  }
}

function closeEditor() {
  if (dialog.value?.open) {
    closeRequestedByComponent = true
    dialog.value.close()
  } else {
    restoreFocus()
  }
  emit('close')
}

function handleDialogClose() {
  if (dialog.value?.open) return
  restoreFocus()
  if (closeRequestedByComponent) {
    closeRequestedByComponent = false
    return
  }
  if (props.open) emit('close')
}

function restoreFocus() {
  const target = restoreFocusTarget
  restoreFocusTarget = null
  if (target?.isConnected) target.focus({ preventScroll: true })
}

function applyDraft() {
  if (!canApply.value) return
  emit('apply', {
    definition_id: draftValue.value.definition_id,
    definition_revision: draftValue.value.definition_revision,
    node_path: draftValue.value.node_path,
    model_id: draftValue.value.model_id,
    reasoning_effort: draftValue.value.reasoning_effort,
  })
  closeEditor()
}

onBeforeUnmount(() => {
  if (dialog.value?.open) dialog.value.close()
  restoreFocusTarget = null
})
</script>

<template>
  <dialog
    ref="dialog"
    class="m-auto w-[min(42rem,calc(100vw-1rem))] max-h-[calc(100dvh-1rem)] overflow-hidden rounded-xl border border-line-2 bg-surface p-0 text-ink shadow-xl backdrop:bg-black/50 sm:w-[min(42rem,calc(100vw-2rem))] sm:max-h-[calc(100dvh-2rem)]"
    :aria-labelledby="titleId"
    aria-modal="true"
    @click.self="closeEditor"
    @cancel.prevent="closeEditor"
    @close="handleDialogClose"
    @keydown.stop
  >
    <div class="flex max-h-[calc(100dvh-1rem)] flex-col sm:max-h-[calc(100dvh-2rem)]">
      <header class="flex shrink-0 items-start justify-between gap-4 border-b border-line-2 px-4 py-3 sm:px-5">
        <div>
          <h2 :id="titleId" class="text-sm font-semibold text-ink">{{ editing ? '编辑自定义节点规则' : '新增自定义节点规则' }}</h2>
          <p class="mt-1 text-[11px] text-ink-4">可使用完整三元组身份，或留空 definition_id 与 definition_revision，按 node_path 配置跨工作流定义匹配的规则。</p>
        </div>
        <button type="button" aria-label="关闭规则编辑" class="shrink-0 rounded-md border border-line-2 bg-surface px-3 py-1.5 text-xs text-ink-2 hover:bg-hover" @click="closeEditor">关闭</button>
      </header>

      <div class="min-h-0 space-y-4 overflow-y-auto p-4 sm:p-5">
        <section class="rounded-md border border-line-2 bg-surface-2 p-3 text-[11px]">
          <h3 class="font-semibold text-ink-2">节点用途参考</h3>
          <p class="mt-1 text-ink-3"><strong>{{ nodeDescription.nodeId || '节点名未提供' }}</strong> · {{ nodeDescription.known && nodeDescription.usage ? nodeDescription.usage : '用途待确认' }}</p>
          <p class="mt-1 text-[10px] text-ink-4">说明依据：{{ nodeDescription.evidenceLabel || '说明依据不足' }}。用途仅供核对，不会自动选择职责。</p>
        </section>

        <details class="rounded-md border border-line-2 p-3">
            <summary ref="firstIdentityInput" class="cursor-pointer text-[11px] font-medium text-ink-2">核对或编辑节点身份</summary>
          <div class="mt-3 grid gap-3">
            <label class="grid gap-1 text-[11px] text-ink-3">
              <span>工作流定义 ID（definition_id）</span>
              <input v-model="draftValue.definition_id" type="text" autocomplete="off" class="ui-control-h ui-control px-2.5 text-[12px] text-ink" />
            </label>
            <label class="grid gap-1 text-[11px] text-ink-3">
              <span>定义版本（definition_revision）</span>
              <input v-model="draftValue.definition_revision" type="text" autocomplete="off" class="ui-control-h ui-control px-2.5 text-[12px] text-ink" />
            </label>
            <label class="grid gap-1 text-[11px] text-ink-3">
              <span>节点路径（node_path）</span>
              <input v-model="draftValue.node_path" type="text" autocomplete="off" class="ui-control-h ui-control px-2.5 text-[12px] text-ink" />
            </label>
            <p class="text-[10px] leading-4 text-ink-4">填写两项 definition 字段时按完整三元组精确匹配；同身份职责绑定会保留，由独立规则优先。两项都留空时按完全相同的 node_path 跨定义匹配；保存 path-only 独立规则会替换并删除同身份职责绑定。若完整三元组与 path-only 身份共用路径，缺少 definition 身份的请求会歧义并使用默认目标，因此不能新增或保存该冲突规则；不同完整三元组之间可共用路径。activity 的 node_path 保留 iter-N 原值，不会自动移除。字段会按输入原值保存。</p>
          </div>
        </details>

        <section class="grid gap-3 sm:grid-cols-2">
          <label class="grid gap-1 text-[11px] text-ink-3">
            <span>模型或现有分组</span>
            <select :value="draftValue.model_id" class="ui-control-h ui-control px-2 text-[12px] text-ink" @change="changeTarget($event.target.value)">
              <option value="">选择已启用模型或有效分组</option>
              <option v-for="option in targetOptions" :key="option.id" :value="option.id" :disabled="option.disabled">{{ option.label }}</option>
            </select>
          </label>
          <label class="grid gap-1 text-[11px] text-ink-3">
            <span>固定思考等级</span>
            <select v-model="draftValue.reasoning_effort" class="ui-control-h ui-control px-2 text-[12px] text-ink">
              <option v-for="effort in effortOptions" :key="effort" :value="effort">{{ describeEffort(effort) }}</option>
            </select>
          </label>
        </section>

        <ul v-if="validationErrors.length" class="space-y-1 rounded-md border border-warning-tint-line bg-warning-tint px-3 py-2 text-[11px] leading-4 text-warning-fg" role="alert">
          <li v-for="error in validationErrors" :key="error">{{ error }}</li>
        </ul>
      </div>

      <footer class="flex shrink-0 items-center justify-end gap-2 border-t border-line-2 px-4 py-3">
        <button type="button" class="rounded-md border border-line bg-surface px-4 py-2 text-xs text-ink-2 shadow-sm hover:bg-hover" @click="closeEditor">取消</button>
        <button type="button" class="rounded-md bg-primary px-4 py-2 text-xs font-medium text-white shadow-sm hover:bg-primary-hover disabled:cursor-not-allowed disabled:opacity-50" :disabled="!canApply" @click="applyDraft">应用到 Auto 设置草稿</button>
      </footer>
    </div>
  </dialog>
</template>
