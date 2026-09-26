<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { getModels } from '../../api/models.js'
import { getSettings, updateSettings } from '../../api/settings.js'
import { useToast } from '../../composables/useToast.js'

const CLASSIFIER_DISABLED_VALUE = '__disabled__'

const models = ref([])
const classifierModelId = ref('')
const saving = ref(false)
const saved = ref(false)
const { showToast } = useToast()
let savedTimer = null

async function load() {
  try {
    const [modelsData, settings] = await Promise.all([getModels(), getSettings()])
    models.value = Array.isArray(modelsData) ? modelsData : []
    classifierModelId.value = settings?.classifier_model_id || ''
  } catch (e) {
    console.error('加载高级设置失败', e)
    showToast(e?.message || '加载高级设置失败', { tone: 'error' })
  }
}

async function save() {
  saving.value = true
  try {
    await updateSettings({ classifier_model_id: classifierModelId.value })
    saved.value = true
    clearTimeout(savedTimer)
    savedTimer = setTimeout(() => { saved.value = false }, 2000)
  } catch (e) {
    console.error('保存高级设置失败', e)
    showToast(e?.message || '保存高级设置失败', { tone: 'error' })
  } finally {
    saving.value = false
  }
}

onMounted(load)
onUnmounted(() => clearTimeout(savedTimer))
</script>

<template>
  <div class="ui-card p-3">
    <div class="mb-3 flex items-center justify-between">
      <h3 class="text-[14px] font-semibold text-ink">高级设置</h3>
      <span v-if="saved" class="inline-flex items-center gap-1.5 rounded-full bg-success-tint px-2 py-0.5 text-[12px] font-medium text-success">✓ 已保存</span>
    </div>

    <div class="space-y-2.5">
      <div>
        <div class="mb-1 flex items-center gap-1.5 text-[12px] font-medium text-ink-2">
          Classifier 模型
          <span class="text-ink-4" title="用于判断用户意图">ⓘ</span>
        </div>
        <select
          v-model="classifierModelId"
          class="ui-control-h ui-control w-full px-3 text-[12px] text-ink outline-none transition focus:border-primary-tint-line-strong focus:ring-2 focus:ring-primary-ring"
        >
          <option value="">跟随主模型</option>
          <option :value="CLASSIFIER_DISABLED_VALUE">不使用 Classifier</option>
          <option v-for="m in models" :key="m.id" :value="m.id">
            {{ m.name || m.model_id }} - {{ m.model_id }}
          </option>
        </select>
      </div>

      <p class="text-[12px] leading-4 text-ink-4">Classifier 用于判断用户意图，可使用轻量小模型节省费用。</p>

      <div class="flex items-center gap-3 pt-1">
        <button
          class="h-8 rounded-lg bg-primary px-3 text-[12px] font-semibold text-white shadow-sm hover:bg-primary-hover disabled:cursor-not-allowed disabled:opacity-60"
          :disabled="saving"
          @click="save"
        >
          {{ saving ? '保存中...' : '保存设置' }}
        </button>
        <span v-if="saved" class="text-[12px] text-success">✓ 设置已同步</span>
      </div>
    </div>
  </div>
</template>
