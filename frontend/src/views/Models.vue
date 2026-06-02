<script setup>
import { ref, onMounted } from 'vue'
import { getModels, createModel, updateModel, deleteModel, testModel, refreshCodexModel } from '../api/models.js'
import ModelCard from '../components/models/ModelCard.vue'
import ModelForm from '../components/models/ModelForm.vue'
import IconPlus from '../components/icons/IconPlus.vue'
import IconModels from '../components/icons/IconModels.vue'

const models = ref([])
const showForm = ref(false)
const editingModel = ref(null)
const testResults = ref({})
const testingIds = ref({})
const refreshingCodexIds = ref({})
const saving = ref(false)
const errorMessage = ref('')

async function loadModels() {
  try {
    const data = await getModels()
    models.value = Array.isArray(data) ? data : []
  } catch (e) {
    models.value = []
  }
}

function openAdd() {
  editingModel.value = null
  errorMessage.value = ''
  showForm.value = true
}

function openEdit(m) {
  editingModel.value = m
  errorMessage.value = ''
  showForm.value = true
}

async function handleSave(form) {
  saving.value = true
  errorMessage.value = ''
  try {
    if (editingModel.value?.id) {
      await updateModel(editingModel.value.id, form)
    } else {
      await createModel(form)
    }
    showForm.value = false
    await loadModels()
  } catch (e) {
    errorMessage.value = e?.message || '保存失败'
  } finally {
    saving.value = false
  }
}

async function handleDelete(m) {
  if (!confirm(`确定删除模型「${m.name || m.model_id}」？`)) return
  errorMessage.value = ''
  try {
    await deleteModel(m.id)
    await loadModels()
  } catch (e) {
    errorMessage.value = e?.message || '删除失败'
  }
}

async function handleTest(m) {
  testingIds.value[m.id] = true
  try {
    const result = await testModel(m.id, m.model_id)
    testResults.value[m.id] = result
  } catch (e) {
    testResults.value[m.id] = { ok: false, error: e.message }
  } finally {
    testingIds.value[m.id] = false
  }
}

async function handleRefreshCodex(m) {
  refreshingCodexIds.value[m.id] = true
  errorMessage.value = ''
  try {
    await refreshCodexModel(m.id)
    await loadModels()
  } catch (e) {
    errorMessage.value = e?.message || '刷新 Codex 信息失败'
  } finally {
    refreshingCodexIds.value[m.id] = false
  }
}

onMounted(loadModels)
</script>

<template>
  <div class="p-6">
    <div class="flex items-center justify-between mb-6">
      <div>
        <h1 class="text-lg font-semibold text-[#111827]">模型配置</h1>
        <p class="text-xs text-[#6b7280] mt-1">配置 Kiro 可用的 AI 模型，每个模型对应一个上游 API 接口</p>
      </div>
      <button
        class="flex items-center gap-1.5 px-4 py-2 text-sm font-medium text-white bg-[#10b981] hover:bg-[#059669] rounded-lg transition-colors"
        @click="openAdd"
      >
        <IconPlus class="w-4 h-4" />
        新增模型
      </button>
    </div>

    <div v-if="errorMessage" class="mb-4 px-3 py-2 text-sm text-[#ef4444] bg-[#fef2f2] border border-[#ef4444]/20 rounded-md">
      {{ errorMessage }}
    </div>

    <div v-if="models.length > 0" class="grid grid-cols-1 md:grid-cols-2 gap-4">
      <ModelCard
        v-for="m in models"
        :key="m.id"
        :model="m"
        :test-result="testResults[m.id]"
        :testing="!!testingIds[m.id]"
        :refreshing-codex="!!refreshingCodexIds[m.id]"
        @edit="openEdit(m)"
        @delete="handleDelete(m)"
        @test="handleTest(m)"
        @refresh-codex="handleRefreshCodex(m)"
      />
    </div>

    <div v-else class="flex flex-col items-center justify-center py-20 text-[#6b7280]">
      <IconModels class="w-8 h-8 mb-3" />
      <p class="text-sm">暂无模型，点击「新增模型」开始配置</p>
    </div>

    <ModelForm
      :visible="showForm"
      :model="editingModel"
      :saving="saving"
      :error="errorMessage"
      @save="handleSave"
      @cancel="showForm = false"
    />
  </div>
</template>
