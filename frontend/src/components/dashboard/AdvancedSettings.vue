<script setup>
import { ref, onMounted } from 'vue'
import { getModels } from '../../api/models.js'
import { getSettings, updateSettings } from '../../api/settings.js'

const models = ref([])
const classifierModelId = ref('')
const upstreamProxyUrl = ref('')
const saving = ref(false)
const saved = ref(false)

async function load() {
  const [modelsData, settings] = await Promise.all([getModels(), getSettings()])
  models.value = Array.isArray(modelsData) ? modelsData : []
  classifierModelId.value = settings?.classifier_model_id || ''
  upstreamProxyUrl.value = settings?.upstream_proxy_url || ''
}

async function save() {
  saving.value = true
  await updateSettings({
    classifier_model_id: classifierModelId.value,
    upstream_proxy_url: upstreamProxyUrl.value.trim(),
  })
  saving.value = false
  saved.value = true
  setTimeout(() => { saved.value = false }, 2000)
}

onMounted(load)
</script>

<template>
  <div class="bg-white border border-[#e5e7eb] rounded-lg p-4 lg:col-span-3">
    <h3 class="text-sm font-medium text-[#111827] mb-3">高级设置</h3>

    <div class="space-y-3">
      <div class="flex items-center gap-3 flex-wrap">
        <label class="text-xs text-[#6b7280] whitespace-nowrap">Classifier 模型</label>
        <select
          v-model="classifierModelId"
          class="flex-1 min-w-[180px] text-sm bg-white border border-[#e5e7eb] rounded px-2 py-1.5 focus:outline-none focus:border-[#10b981] transition-colors"
        >
          <option value="">(跟随主模型)</option>
          <option v-for="m in models" :key="m.id" :value="m.id">
            {{ m.name }} - {{ m.model_id }}
          </option>
        </select>
      </div>

      <div class="flex items-center gap-3 flex-wrap">
        <label class="text-xs text-[#6b7280] whitespace-nowrap">上游代理</label>
        <input
          v-model="upstreamProxyUrl"
          type="text"
          placeholder="可留空，例如 127.0.0.1:7890 或 http://127.0.0.1:7890"
          class="flex-1 min-w-[260px] text-sm bg-white border border-[#e5e7eb] rounded px-2 py-1.5 focus:outline-none focus:border-[#10b981] transition-colors"
        />
      </div>

      <div class="flex items-center gap-3">
        <button
          @click="save"
          :disabled="saving"
          class="px-3 py-1.5 text-xs text-white bg-[#10b981] rounded hover:bg-[#059669] transition-colors disabled:opacity-50"
        >
          {{ saving ? '保存中...' : '保存' }}
        </button>
        <span v-if="saved" class="text-xs text-[#10b981]">✓ 已保存</span>
      </div>
    </div>

    <p class="text-xs text-[#9ca3af] mt-2">
      Classifier 用于判断用户意图（Do/Spec），走轻量小模型可以节省费用。上游代理仅作用于模型 API 请求，留空则直连。
    </p>
  </div>
</template>
