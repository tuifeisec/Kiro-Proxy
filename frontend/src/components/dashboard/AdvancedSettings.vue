<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { getModels } from '../../api/models.js'
import { getSettings, updateSettings } from '../../api/settings.js'
import { useToast } from '../../composables/useToast.js'
import {
  effectiveClassifierChoice,
  parseTypesafeConfig,
  TYPESAFE_CLASSIFIER_CHOICE,
  TYPESAFE_SETTINGS_KEY,
} from '../../utils/classifierSettings.js'
import SettingsDialog from '../common/SettingsDialog.vue'
import TypesafeClassifierSettings from './TypesafeClassifierSettings.vue'

const CLASSIFIER_DISABLED_VALUE = '__disabled__'

const models = ref([])
const classifierChoice = ref('')
const effectiveChoice = ref('')
const settingsKnown = ref(false)
const settingsError = ref('')
const typesafeModel = ref('jev-1.13.0')
const typesafeVisible = ref(false)
const typesafeBusy = ref(false)
const typesafeDirty = ref(false)
const typesafeDialog = ref(null)
const saving = ref(false)
const saved = ref(false)
const { showToast } = useToast()
let savedTimer = null
let preserveDraftAfterUnknown = false

const choiceDirty = computed(() => settingsKnown.value && classifierChoice.value !== effectiveChoice.value)
const missingModelChoice = computed(() => (
  Boolean(classifierChoice.value)
  && classifierChoice.value !== CLASSIFIER_DISABLED_VALUE
  && classifierChoice.value !== TYPESAFE_CLASSIFIER_CHOICE
  && !models.value.some((model) => model.id === classifierChoice.value)
))
const typesafeSettingsDisabled = computed(() => (
  saving.value || typesafeBusy.value || !settingsKnown.value || classifierChoice.value !== TYPESAFE_CLASSIFIER_CHOICE
))

function choiceLabel(choice) {
  if (choice === TYPESAFE_CLASSIFIER_CHOICE) return `Typesafe classifier · ${typesafeModel.value}`
  if (choice === CLASSIFIER_DISABLED_VALUE) return '不使用 Classifier'
  if (!choice) return '跟随主模型'
  const model = models.value.find((item) => item.id === choice)
  return model ? (model.name || model.model_id) : choice
}

function updateSettingsSummary(settings) {
  const config = parseTypesafeConfig(settings)
  typesafeModel.value = config.model
  effectiveChoice.value = effectiveClassifierChoice(settings?.classifier_model_id ?? '', config)
  settingsKnown.value = true
  settingsError.value = ''
  return effectiveChoice.value
}

async function reloadSettings({ resetChoice = false, preserveDraft = false } = {}) {
  const keepDraft = !resetChoice && (preserveDraft || preserveDraftAfterUnknown || choiceDirty.value)
  try {
    const settings = await getSettings()
    const actualChoice = updateSettingsSummary(settings)
    if (!keepDraft) classifierChoice.value = actualChoice
    preserveDraftAfterUnknown = false
    return true
  } catch (error) {
    preserveDraftAfterUnknown ||= !resetChoice && (preserveDraft || choiceDirty.value)
    settingsKnown.value = false
    settingsError.value = error?.message || '读取高级设置失败'
    return false
  }
}

async function loadModels() {
  try {
    const data = await getModels()
    models.value = Array.isArray(data) ? data : []
  } catch (error) {
    models.value = []
    console.error('加载 Classifier 模型列表失败', error)
    showToast(error?.message || '加载 Classifier 模型列表失败', { tone: 'error' })
  }
}

async function load() {
  await Promise.all([loadModels(), reloadSettings({ resetChoice: true })])
  if (!settingsKnown.value) {
    console.error('加载高级设置失败', settingsError.value)
    showToast(settingsError.value, { tone: 'error' })
  }
}

function openTypesafeSettings() {
  if (typesafeSettingsDisabled.value) return
  typesafeVisible.value = true
}

function closeTypesafeSettings() {
  typesafeVisible.value = false
  typesafeBusy.value = false
  typesafeDirty.value = false
  void reloadSettings()
}

function onTypesafeSaved() {
  typesafeVisible.value = false
  typesafeBusy.value = false
  typesafeDirty.value = false
  void reloadSettings()
}

async function save() {
  if (saving.value || !settingsKnown.value || !choiceDirty.value) return

  saving.value = true
  saved.value = false
  const submittedChoice = classifierChoice.value
  let latestSettings
  let latestConfig

  try {
    // 先读取最新完整配置，避免旧表单快照覆盖较新的配置字段。
    latestSettings = await getSettings()
    latestConfig = parseTypesafeConfig(latestSettings)
    updateSettingsSummary(latestSettings)
  } catch (error) {
    const originalError = error?.message || '保存高级设置失败'
    preserveDraftAfterUnknown ||= choiceDirty.value
    settingsKnown.value = false
    settingsError.value = originalError
    console.error('保存前读取高级设置失败', error)
    showToast(originalError, { tone: 'error' })
    saving.value = false
    return
  }

  try {
    if (submittedChoice === TYPESAFE_CLASSIFIER_CHOICE) {
      latestConfig.enabled = true
      await updateSettings({
        [TYPESAFE_SETTINGS_KEY]: JSON.stringify(latestConfig),
      })
    } else {
      latestConfig.enabled = false
      await updateSettings({
        classifier_model_id: submittedChoice,
        [TYPESAFE_SETTINGS_KEY]: JSON.stringify(latestConfig),
      })
    }
  } catch (error) {
    const originalError = error?.message || '保存高级设置失败'
    const rereadSucceeded = await reloadSettings({ preserveDraft: true })
    settingsError.value = rereadSucceeded
      ? originalError
      : `${originalError}；重读当前设置失败：${settingsError.value}`
    console.error('保存高级设置失败', error)
    showToast(originalError, { tone: 'error' })
    saving.value = false
    return
  }

  const rereadSucceeded = await reloadSettings({ resetChoice: true })
  saving.value = false
  if (!rereadSucceeded) {
    settingsError.value = `已保存，重新读取失败：${settingsError.value}`
    showToast('已保存，重新读取失败。重新读取成功前不能继续保存。', { tone: 'error' })
    return
  }

  saved.value = true
  clearTimeout(savedTimer)
  savedTimer = setTimeout(() => { saved.value = false }, 2000)
  showToast('Classifier 设置已保存', { tone: 'success' })
}

async function retrySettingsRead() {
  if (await reloadSettings()) showToast('设置已重新读取', { tone: 'success' })
  else showToast(settingsError.value, { tone: 'error' })
}

onMounted(load)
onUnmounted(() => clearTimeout(savedTimer))
</script>

<template>
  <div class="ui-card p-3">
    <h3 class="mb-3 text-[14px] font-semibold text-ink">高级设置</h3>

    <label for="classifier-model-choice" class="mb-1 flex items-center gap-1.5 text-[12px] font-medium text-ink-2">
      Classifier 模型
      <span class="text-ink-4" title="执行 Kiro 单独的意图分类请求；不负责本地 Auto 任务选模">ⓘ</span>
    </label>
    <select
      id="classifier-model-choice"
      v-model="classifierChoice"
      @change="saved = false"
      class="ui-control-h ui-control w-full px-3 text-[12px] text-ink outline-none transition focus:border-primary-tint-line-strong focus:ring-2 focus:ring-primary-ring disabled:cursor-not-allowed disabled:opacity-60"
      :disabled="!settingsKnown || saving || typesafeBusy"
    >
      <option value="">跟随主模型</option>
      <option :value="CLASSIFIER_DISABLED_VALUE">不使用 Classifier</option>
      <option :value="TYPESAFE_CLASSIFIER_CHOICE">{{ choiceLabel(TYPESAFE_CLASSIFIER_CHOICE) }}</option>
      <option v-if="missingModelChoice" :value="classifierChoice" disabled>
        已配置模型 · {{ classifierChoice }}（列表中不可用）
      </option>
      <option v-for="m in models" :key="m.id" :value="m.id">
        {{ m.name || m.model_id }} - {{ m.model_id }}
      </option>
    </select>

    <p v-if="settingsKnown" class="mt-1.5 text-[12px] leading-4 text-ink-4" role="status">
      {{ choiceDirty ? `待保存；当前生效：${choiceLabel(effectiveChoice)}` : `当前生效：${choiceLabel(effectiveChoice)}` }}
    </p>
    <div v-if="settingsError" class="mt-2 rounded-lg border border-danger-tint-line bg-danger-tint px-3 py-2 text-[12px] text-danger" role="alert">
      <p>{{ settingsKnown ? 'Classifier 设置保存失败。' : 'Classifier 设置读取失败，当前不能选择或保存。' }}</p>
      <p v-if="settingsError" class="mt-1 break-words">{{ settingsError }}</p>
      <button type="button" class="mt-1 underline underline-offset-2" @click="retrySettingsRead">重新读取</button>
    </div>

    <div class="mt-3 flex items-center gap-2 border-t border-line-2 pt-3">
      <button
        class="h-8 min-w-0 flex-1 ui-primary-button text-[12px] font-semibold shadow-sm disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="!settingsKnown || saving || typesafeBusy || !choiceDirty"
        @click="save"
      >
        {{ saving ? '保存中...' : saved ? '✓ 已保存' : '保存设置' }}
      </button>
      <button
        type="button"
        aria-label="设置 Typesafe"
        class="h-8 shrink-0 rounded-lg border border-line px-4 text-[12px] font-medium text-ink-2 hover:bg-hover disabled:cursor-not-allowed disabled:opacity-60"
        :title="classifierChoice === TYPESAFE_CLASSIFIER_CHOICE ? '设置 Typesafe classifier' : '选择 Typesafe classifier 后可编辑其参数'"
        :aria-expanded="typesafeVisible"
        :disabled="typesafeSettingsDisabled"
        @click="openTypesafeSettings"
      >
        设置
      </button>
    </div>
  </div>

  <SettingsDialog
    ref="typesafeDialog"
    :visible="typesafeVisible"
    title="Typesafe classifier 设置"
    description="仅替换 Kiro 独立的 chat / do / spec classifier。"
    :busy="typesafeBusy"
    :dirty="typesafeDirty"
    @close="closeTypesafeSettings"
  >
    <TypesafeClassifierSettings
      v-if="typesafeVisible"
      @busy="typesafeBusy = $event"
      @dirty="typesafeDirty = $event"
      @saved="onTypesafeSaved"
      @cancel="typesafeDialog?.requestClose()"
    />
  </SettingsDialog>
</template>
