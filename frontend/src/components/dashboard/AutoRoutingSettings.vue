<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { getModels } from '../../api/models.js'
import { getSettings, updateSettings } from '../../api/settings.js'
import { useToast } from '../../composables/useToast.js'
import {
  AUTO_ROUTING_SETTINGS_KEY,
  AUTO_STAGE_OPTIONS,
  emptyAutoStageConfig,
  parseAutoStageConfig
} from '../../utils/autoStageConfig.js'

const models = ref([])
const config = ref(emptyAutoStageConfig())
const snapshot = ref(null)
const legacyConfig = ref(false)
const configError = ref('')
const loadError = ref('')
const saveError = ref('')
const loading = ref(false)
const saving = ref(false)
const { showToast } = useToast()
const emit = defineEmits(['busy', 'dirty', 'saved', 'cancel'])

const enabledModels = computed(() => models.value.filter((model) => model.enabled === true))
const enabledModelIds = computed(() => new Set(enabledModels.value.map((model) => model.id)))
const formErrors = computed(() => {
  const errors = []
  const current = config.value
  const targets = [
    { label: '阶段未提供或未匹配的固定目标', id: current.default_model_id },
    ...AUTO_STAGE_OPTIONS.map((stage) => ({ label: `${stage.label}阶段模型`, id: current.stage_models[stage.key] }))
  ]
  for (const target of targets) {
    if (current.enabled && !enabledModelIds.value.has(target.id)) {
      errors.push(`启用 Auto 路由时，${target.label}必选已启用的模型。`)
    } else if (target.id && !enabledModelIds.value.has(target.id)) {
      errors.push(`${target.label}必须选择已启用的模型，或清空该项。`)
    }
  }
  return errors
})

const canSave = computed(() => !loading.value && !saving.value && !loadError.value && !configError.value && formErrors.value.length === 0)
const dirty = computed(() => snapshot.value !== null && JSON.stringify(config.value) !== snapshot.value)

watch(saving, (value) => emit('busy', value), { immediate: true })
watch(dirty, (value) => emit('dirty', value), { immediate: true })

function modelName(model) {
  return `${model.name || model.model_id || model.id} · ${model.model_id || model.id}`
}

function optionsFor(currentId) {
  const options = enabledModels.value.map((model) => ({ id: model.id, label: modelName(model), disabled: false }))
  if (currentId && !options.some((option) => option.id === currentId)) {
    const existing = models.value.find((model) => model.id === currentId)
    options.unshift({
      id: currentId,
      label: existing
        ? `${modelName(existing)}（已停用，保留现有值）`
        : `${currentId}（模型不存在，保留现有值）`,
      disabled: true
    })
  }
  return options
}

async function load() {
  loading.value = true
  snapshot.value = null
  try {
    const [modelsData, settings] = await Promise.all([getModels(), getSettings()])
    models.value = Array.isArray(modelsData) ? modelsData : []
    try {
      const parsed = parseAutoStageConfig(settings?.[AUTO_ROUTING_SETTINGS_KEY])
      config.value = parsed.config
      legacyConfig.value = parsed.legacy
      snapshot.value = JSON.stringify(config.value)
    } catch (error) {
      configError.value = error.message || 'Auto 模型配置无法解析。'
      showToast(configError.value, { tone: 'error' })
    }
  } catch (error) {
    loadError.value = error?.message || '设置读取失败，无法安全保存。'
    console.error('加载 Auto 模型配置失败', error)
    showToast(loadError.value || '加载 Auto 模型配置失败', { tone: 'error' })
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!canSave.value) return
  saving.value = true
  saveError.value = ''
  try {
    const payload = {
      mode: 'stage_fixed',
      enabled: config.value.enabled,
      default_model_id: config.value.default_model_id,
      stage_models: { ...config.value.stage_models }
    }
    await updateSettings({ [AUTO_ROUTING_SETTINGS_KEY]: JSON.stringify(payload) })
    config.value = payload
    snapshot.value = JSON.stringify(payload)
    legacyConfig.value = false
    showToast('Auto 模型配置已保存', { tone: 'success' })
    emit('saved')
  } catch (error) {
    console.error('保存 Auto 模型配置失败', error)
    saveError.value = error?.message || '保存 Auto 模型配置失败'
    showToast(saveError.value, { tone: 'error' })
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <section class="flex min-h-0 flex-1 flex-col">
    <div class="min-h-0 flex-1 overflow-y-auto p-4">
      <p class="mb-3 text-[12px] leading-5 text-ink-4">本地选模策略仅作用于 Kiro 主生成请求中的 Auto，capture 模式不适用；真实运行模式由代理控制页管理。</p>
      <div v-if="configError" class="mb-3 rounded-md border border-danger-tint-line bg-danger-tint px-3 py-2 text-[12px] leading-5 text-danger" role="alert">
        {{ configError }} 请先修复原始配置；当前不会允许保存覆盖。
      </div>
      <div v-if="loadError" class="mb-3 rounded-md border border-danger-tint-line bg-danger-tint px-3 py-2 text-[12px] leading-5 text-danger" role="alert">
        {{ loadError }}
      </div>
      <p v-if="loading" class="py-2 text-[12px] text-ink-4">正在加载设置...</p>
      <div v-else-if="!configError && !loadError" class="space-y-3">
        <fieldset class="min-w-0 space-y-3 border-0 p-0" :disabled="saving">
          <div v-if="legacyConfig" class="rounded-md border border-warning-tint-line bg-warning-tint px-3 py-2 text-[11px] leading-5 text-warning-fg">
            新版 Auto 不再支持旧任务配置。请填写五阶段模型并保存；保存后替换旧记录，启用后按阶段路由。
          </div>

          <div class="flex items-center justify-between gap-4 rounded-lg border border-line-2 bg-surface-2 px-3 py-2.5">
            <div>
              <div class="text-[12px] font-medium text-ink">启用 Auto 路由</div>
              <p class="mt-0.5 text-[11px] leading-4 text-ink-4">阶段模式只使用阶段模型；连续工具回合保持首次选择的模型与渠道。</p>
            </div>
            <label class="relative inline-flex shrink-0 cursor-pointer items-center">
              <input v-model="config.enabled" type="checkbox" aria-label="启用 Auto 路由" class="peer sr-only" />
              <span class="h-5 w-9 rounded-full bg-hover-strong after:absolute after:left-[2px] after:top-[2px] after:h-4 after:w-4 after:rounded-full after:border after:border-line-strong after:bg-surface after:transition-transform after:content-[''] peer-checked:bg-primary peer-checked:after:translate-x-full peer-checked:after:border-white"></span>
            </label>
          </div>

          <section class="rounded-lg border border-line-2 p-3">
            <h4 class="mb-2 text-[12px] font-semibold text-ink">五阶段模型</h4>
            <p class="mb-3 text-[11px] leading-4 text-ink-4">五个阶段可以复用同一个模型配置。阶段未提供或未匹配时使用固定目标，也可复用任一阶段的模型配置。</p>
            <p class="mb-3 rounded-md border border-warning-tint-line bg-warning-tint px-3 py-2 text-[11px] leading-4 text-warning-fg">实际阶段信息需要同步桥接接入并通过代理启动 Kiro；仅配置模型不代表桥接已接通。接通后可在 auto-routing 日志核对。</p>
            <p class="mb-3 text-[11px] leading-4 text-ink-4">原生 Responses 请求目前仅支持 OpenAI 渠道的 Responses 端点；不兼容的模型能力会明确报错，不会自动切换模型。</p>
            <div class="grid grid-cols-1 gap-2.5 sm:grid-cols-2">
              <div v-for="stage in AUTO_STAGE_OPTIONS" :key="stage.key">
                <label :for="`auto-routing-stage-${stage.key}`" class="mb-1 block text-[12px] font-medium text-ink-2">{{ stage.label }}阶段模型<span v-if="config.enabled" class="ml-1 text-danger">必选</span></label>
                <select :id="`auto-routing-stage-${stage.key}`" v-model="config.stage_models[stage.key]" class="ui-control-h ui-control w-full px-2.5 text-[12px] text-ink outline-none focus:border-primary-tint-line-strong focus:ring-2 focus:ring-primary-ring">
                  <option value="">未配置</option>
                  <option v-for="option in optionsFor(config.stage_models[stage.key])" :key="option.id" :value="option.id" :disabled="option.disabled">{{ option.label }}</option>
                </select>
              </div>
              <div>
                <label for="auto-routing-stage-default" class="mb-1 block text-[12px] font-medium text-ink-2">阶段未提供或未匹配的固定目标<span v-if="config.enabled" class="ml-1 text-danger">必选</span></label>
                <select id="auto-routing-stage-default" v-model="config.default_model_id" class="ui-control-h ui-control w-full px-2.5 text-[12px] text-ink outline-none focus:border-primary-tint-line-strong focus:ring-2 focus:ring-primary-ring">
                  <option value="">未配置</option>
                  <option v-for="option in optionsFor(config.default_model_id)" :key="option.id" :value="option.id" :disabled="option.disabled">{{ option.label }}</option>
                </select>
              </div>
            </div>
            <p v-if="enabledModels.length === 0" class="mt-2 text-[11px] text-warning-fg">当前没有已启用模型可供选择。</p>
          </section>

          <ul v-if="formErrors.length" class="space-y-1 rounded-md border border-warning-tint-line bg-warning-tint px-3 py-2 text-[11px] leading-4 text-warning-fg">
            <li v-for="error in formErrors" :key="error">{{ error }}</li>
          </ul>
        </fieldset>
      </div>
      <div v-if="saveError" class="mt-3 rounded-md border border-danger-tint-line bg-danger-tint px-3 py-2 text-[12px] leading-5 text-danger" role="alert">{{ saveError }}</div>
    </div>
    <footer class="flex shrink-0 items-center justify-end gap-2 border-t border-line-2 px-4 py-3">
      <button type="button" class="h-9 rounded-md border border-line bg-surface px-5 text-xs text-ink-2 shadow-sm hover:bg-hover disabled:cursor-not-allowed disabled:opacity-50" :disabled="saving" @click="emit('cancel')">取消</button>
      <button type="button" class="h-9 rounded-md bg-primary px-5 text-xs font-medium text-white shadow-sm hover:bg-primary-hover disabled:cursor-not-allowed disabled:opacity-50" :disabled="!canSave" @click="save">
        {{ saving ? '保存中...' : '保存 Auto 模型配置' }}
      </button>
    </footer>
  </section>
</template>
