<script setup>
import { ref, onMounted } from 'vue'
import { getModels } from '../../api/models.js'
import { getSettings, updateSettings } from '../../api/settings.js'

const CLASSIFIER_DISABLED_VALUE = '__disabled__'

const models = ref([])
const classifierModelId = ref('')
const saving = ref(false)
const saved = ref(false)

async function load() {
  const [modelsData, settings] = await Promise.all([getModels(), getSettings()])
  models.value = Array.isArray(modelsData) ? modelsData : []
  classifierModelId.value = settings?.classifier_model_id || ''
}

async function save() {
  saving.value = true
  await updateSettings({ classifier_model_id: classifierModelId.value })
  saving.value = false
  saved.value = true
  setTimeout(() => { saved.value = false }, 2000)
}

onMounted(load)
</script>

<template>
  <div class="ui-card p-3">
    <div class="mb-3 flex items-center justify-between">
      <h3 class="text-[14px] font-semibold text-[#0f172a]">高级设置</h3>
      <span v-if="saved" class="inline-flex items-center gap-1.5 rounded-full bg-[#ecfdf5] px-2 py-0.5 text-[12px] font-medium text-[#059669]">✓ 已保存</span>
    </div>

    <div class="space-y-2.5">
      <div>
        <div class="mb-1 flex items-center gap-1.5 text-[12px] font-medium text-[#475569]">
          Classifier 模型
          <span class="text-[#94a3b8]" title="用于判断用户意图">ⓘ</span>
        </div>
        <select
          v-model="classifierModelId"
          class="ui-control-h ui-control w-full px-3 text-[12px] text-[#0f172a] outline-none transition focus:border-[#93c5fd] focus:ring-2 focus:ring-[#dbeafe]"
        >
          <option value="">跟随主模型</option>
          <option :value="CLASSIFIER_DISABLED_VALUE">不使用 Classifier</option>
          <option v-for="m in models" :key="m.id" :value="m.id">
            {{ m.name || m.model_id }} - {{ m.model_id }}
          </option>
        </select>
      </div>

      <p class="text-[12px] leading-4 text-[#94a3b8]">Classifier 用于判断用户意图，可使用轻量小模型节省费用。</p>

      <div class="flex items-center gap-3 pt-1">
        <button
          class="h-8 rounded-lg bg-[#1677ff] px-3 text-[12px] font-semibold text-white shadow-sm hover:bg-[#0958d9] disabled:cursor-not-allowed disabled:opacity-60"
          :disabled="saving"
          @click="save"
        >
          {{ saving ? '保存中...' : '保存设置' }}
        </button>
        <span v-if="saved" class="text-[12px] text-[#059669]">✓ 设置已同步</span>
      </div>
    </div>
  </div>
</template>
