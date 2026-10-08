<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { getSettings, updateSettings } from '../../api/settings.js'
import { useToast } from '../../composables/useToast.js'
import {
  defaultTypesafeConfig,
  parseTypesafeConfig,
  TYPESAFE_KEY_MASK,
  TYPESAFE_SETTINGS_KEY,
} from '../../utils/classifierSettings.js'

function serializeConfig(value) {
  return JSON.stringify(value)
}

const config = ref(defaultTypesafeConfig())
const loading = ref(true)
const saving = ref(false)
const loadError = ref(false)
const rereadFailed = ref(false)
const snapshot = ref(null)
const errorMessage = ref('')
const { showToast } = useToast()
const emit = defineEmits(['busy', 'dirty', 'saved', 'cancel'])

const dirty = computed(() => snapshot.value !== null && serializeConfig(config.value) !== snapshot.value)
const fieldsDisabled = computed(() => loading.value || saving.value || loadError.value || rereadFailed.value)

watch(dirty, (value) => emit('dirty', value), { immediate: true })

async function load() {
  const retryingAfterSave = rereadFailed.value
  loading.value = true
  loadError.value = false
  rereadFailed.value = false
  errorMessage.value = ''
  try {
    const nextConfig = parseTypesafeConfig(await getSettings())
    config.value = nextConfig
    snapshot.value = serializeConfig(nextConfig)
  } catch (error) {
    if (retryingAfterSave) rereadFailed.value = true
    else loadError.value = true
    errorMessage.value = error?.message || '读取 Typesafe classifier 设置失败'
    showToast(errorMessage.value, { tone: 'error' })
  } finally {
    loading.value = false
  }
}

async function save() {
  if (fieldsDisabled.value || snapshot.value === null) return

  saving.value = true
  emit('busy', true)
  errorMessage.value = ''
  const submittedKey = config.value.api_key
  const payload = { ...config.value, api_key: submittedKey }

  try {
    await updateSettings({ [TYPESAFE_SETTINGS_KEY]: JSON.stringify(payload) })
  } catch (error) {
    config.value.api_key = submittedKey === TYPESAFE_KEY_MASK ? TYPESAFE_KEY_MASK : ''
    errorMessage.value = error?.message || '保存 Typesafe classifier 设置失败'
    showToast(errorMessage.value, { tone: 'error' })
    saving.value = false
    emit('busy', false)
    return
  }

  config.value.api_key = submittedKey === '' ? '' : TYPESAFE_KEY_MASK
  snapshot.value = serializeConfig(config.value)

  try {
    const nextConfig = parseTypesafeConfig(await getSettings())
    config.value = nextConfig
    snapshot.value = serializeConfig(nextConfig)
    showToast('Typesafe classifier 设置已保存', { tone: 'success' })
    emit('saved')
  } catch (error) {
    rereadFailed.value = true
    errorMessage.value = error?.message || '设置已保存，重新读取失败'
    showToast('设置已保存，重新读取失败。请重读设置后再继续。', { tone: 'error' })
  } finally {
    saving.value = false
    emit('busy', false)
  }
}

function cancel() {
  emit('cancel')
}

onMounted(load)
</script>

<template>
  <section class="flex min-h-0 flex-1 flex-col">
    <div class="min-h-0 flex-1 overflow-y-auto px-4 py-3">
      <div v-if="loading" class="py-8 text-center text-[12px] text-ink-4" role="status">正在读取设置...</div>

      <div v-else-if="loadError" class="rounded-lg border border-danger-tint-line bg-danger-tint px-3 py-3 text-[12px] text-danger" role="alert">
        <p>设置读取失败，当前不会允许保存覆盖。</p>
        <p v-if="errorMessage" class="mt-1 break-words">{{ errorMessage }}</p>
        <button type="button" class="mt-2 underline underline-offset-2" @click="load">重新读取</button>
      </div>

      <template v-else>
        <div v-if="rereadFailed" class="mb-3 rounded-lg border border-danger-tint-line bg-danger-tint px-3 py-3 text-[12px] text-danger" role="alert">
          <p>已保存，重新读取失败。保存已完成；重读成功前不能继续修改或保存。</p>
          <p v-if="errorMessage" class="mt-1 break-words">{{ errorMessage }}</p>
          <button type="button" class="mt-2 underline underline-offset-2" :disabled="saving" @click="load">重新读取设置</button>
        </div>

        <div class="mb-3">
          <p class="text-[12px] leading-5 text-ink-3">启用由首页 Classifier 选择保存决定。此处只编辑 Typesafe 参数。</p>
        </div>

        <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
          <label class="block">
            <span class="mb-1 block text-[12px] font-medium text-ink-2">API Key</span>
            <input
              v-model="config.api_key"
              type="password"
              autocomplete="new-password"
              class="ui-control-h ui-control w-full px-3 text-[12px] text-ink outline-none transition focus:border-primary-tint-line-strong focus:ring-2 focus:ring-primary-ring"
              placeholder="未配置；留空表示清除"
              :disabled="fieldsDisabled"
            >
            <span class="mt-1 block text-[11px] leading-4 text-ink-4">当前显示掩码时原 Key 保持不变；清空后保存会删除 Key。</span>
          </label>

          <label class="block">
            <span class="mb-1 block text-[12px] font-medium text-ink-2">模型</span>
            <input
              v-model="config.model"
              type="text"
              class="ui-control-h ui-control w-full px-3 text-[12px] text-ink outline-none transition focus:border-primary-tint-line-strong focus:ring-2 focus:ring-primary-ring"
              placeholder="jev-1.13.0"
              :disabled="fieldsDisabled"
            >
          </label>

          <label class="block md:col-span-2">
            <span class="mb-1 block text-[12px] font-medium text-ink-2">基础地址</span>
            <input
              v-model="config.base_url"
              type="url"
              class="ui-control-h ui-control w-full px-3 text-[12px] text-ink outline-none transition focus:border-primary-tint-line-strong focus:ring-2 focus:ring-primary-ring"
              placeholder="https://api.typesafe.ai"
              :disabled="fieldsDisabled"
            >
          </label>

          <label class="block">
            <span class="mb-1 block text-[12px] font-medium text-ink-2">超时（秒）</span>
            <input
              v-model.number="config.timeout_seconds"
              type="number"
              min="1"
              max="120"
              step="1"
              class="ui-control-h ui-control w-full px-3 text-[12px] text-ink outline-none transition focus:border-primary-tint-line-strong focus:ring-2 focus:ring-primary-ring"
              :disabled="fieldsDisabled"
            >
          </label>

          <label class="block">
            <span class="mb-1 block text-[12px] font-medium text-ink-2">输入价格（USD / 百万 Token）</span>
            <input
              v-model.number="config.input_price_per_million"
              type="number"
              min="0.000001"
              step="0.001"
              class="ui-control-h ui-control w-full px-3 text-[12px] text-ink outline-none transition focus:border-primary-tint-line-strong focus:ring-2 focus:ring-primary-ring"
              :disabled="fieldsDisabled"
            >
            <span class="mt-1 block text-[11px] leading-4 text-ink-4">默认 $0.042 / 百万输入 Token；输出 Token 免费。</span>
          </label>

          <label class="block md:col-span-2">
            <span class="mb-1 block text-[12px] font-medium text-ink-2">出站代理（可选）</span>
            <input
              v-model="config.proxy_url"
              type="text"
              class="ui-control-h ui-control w-full px-3 text-[12px] text-ink outline-none transition focus:border-primary-tint-line-strong focus:ring-2 focus:ring-primary-ring"
              placeholder="留空表示直连"
              :disabled="fieldsDisabled"
            >
            <span class="mt-1 block text-[11px] leading-4 text-ink-4">不会继承主模型或其他流量的代理地址。</span>
          </label>
        </div>

        <div class="mt-3 rounded-lg bg-surface-2 px-3 py-2 text-[12px] leading-5 text-ink-3">
          <p><span class="font-medium text-ink-2">分类说明：</span>chat 为咨询、解释和对话；do 为执行请求（如修改代码或文件）；spec 为明确要求规格、需求或设计文档。confidence 是模型对分类的信心值，不作为额外分类阈值。</p>
          <p class="mt-1">费用按输入 Token 和配置单价计算，以 USD 显示。</p>
          <p class="mt-1">
            <a class="text-primary underline-offset-2 hover:underline" href="https://docs.typesafe.ai/api" target="_blank" rel="noopener noreferrer">官方 API 文档</a>
            <span class="px-1 text-ink-4">·</span>
            <a class="text-primary underline-offset-2 hover:underline" href="https://docs.typesafe.ai/models" target="_blank" rel="noopener noreferrer">模型说明</a>
            <span class="px-1 text-ink-4">·</span>
            <a class="text-primary underline-offset-2 hover:underline" href="https://docs.typesafe.ai/primitives/choice" target="_blank" rel="noopener noreferrer">Choice 说明</a>
          </p>
        </div>
      </template>
    </div>

    <footer class="flex shrink-0 items-center justify-end gap-2 border-t border-line-2 px-4 py-3">
      <button
        type="button"
        class="h-8 rounded-lg border border-line px-3 text-[12px] font-medium text-ink-2 hover:bg-hover disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="saving"
        @click="cancel"
      >取消</button>
      <button
        type="button"
        class="h-8 rounded-lg bg-primary px-3 text-[12px] font-semibold text-white shadow-sm hover:bg-primary-hover disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="fieldsDisabled"
        @click="save"
      >
        {{ saving ? '保存中...' : '保存 Typesafe 设置' }}
      </button>
    </footer>
  </section>
</template>
