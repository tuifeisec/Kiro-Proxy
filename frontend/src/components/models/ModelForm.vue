<script setup>
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { startCodexOAuth, exchangeCodexOAuth, fetchUpstreamModels } from '../../api/models.js'
import IconEye from '../icons/IconEye.vue'
import IconEyeOff from '../icons/IconEyeOff.vue'
import IconClose from '../icons/IconClose.vue'
import IconRefresh from '../icons/IconRefresh.vue'
import IconSearch from '../icons/IconSearch.vue'
import IconCheck from '../icons/IconCheck.vue'
import ConfirmDialog from '../common/ConfirmDialog.vue'

const props = defineProps({
  model: { type: Object, default: null },
  visible: { type: Boolean, default: false },
  saving: { type: Boolean, default: false },
  error: { type: String, default: '' }
})

const emit = defineEmits(['save', 'cancel'])

const defaultForm = () => ({
  provider: 'openai',
  name: '',
  model_id: '',
  api_key: '',
  refresh_token: '',
  base_url: '',
  proxy_url: '',
  endpoint: '/chat/completions',
  disable_auto_suffix: false,
  context_window: 0,
  max_tokens: 0,
  reasoning_effort: '',
  image_input_mode: 'auto',
  service_tier: '',
  rate_multiplier: 1.0,
  input_price: 5,
  output_price: 30,
  cache_input_price: 0.5,
  cache_output_price: 30,
  enable_continuation: false,
  enable_opencode_session: false,
  note: ''
})

const form = ref(defaultForm())
const showKey = ref(false)
const loadingModel = ref(false)
const oauthLoading = ref(false)
const oauthAuthUrl = ref('')
const oauthCallbackUrl = ref('')
const oauthError = ref('')
const syncingUpstreamModels = ref(false)
const upstreamModels = ref([])
const upstreamModelSearch = ref('')
const upstreamModelsError = ref('')
const upstreamModelsDialogVisible = ref(false)
const modalRef = ref(null)
const upstreamModelsDialogRef = ref(null)
const focusedUpstreamIndex = ref(0)
const initialSnapshot = ref('')
const discardConfirmVisible = ref(false)
let lastFocused = null

const isDirty = computed(() => Boolean(initialSnapshot.value) && JSON.stringify(form.value) !== initialSnapshot.value)

watch(() => props.visible, (val) => {
  if (val) {
    lastFocused = document.activeElement
    loadingModel.value = true
    if (props.model) {
      form.value = { ...defaultForm(), ...props.model }
    } else {
      form.value = defaultForm()
    }
    showKey.value = false
    oauthAuthUrl.value = ''
    oauthCallbackUrl.value = ''
    oauthError.value = ''
    resetUpstreamModels()
    document.addEventListener('keydown', onKeydown)
    nextTick(() => {
      initialSnapshot.value = JSON.stringify(form.value)
      modalRef.value?.querySelector('input, select, textarea')?.focus()
      loadingModel.value = false
    })
  } else {
    document.removeEventListener('keydown', onKeydown)
    discardConfirmVisible.value = false
    initialSnapshot.value = ''
    lastFocused?.focus?.()
  }
})

onBeforeUnmount(() => document.removeEventListener('keydown', onKeydown))

function trapFocus(event, container) {
  if (event.key !== 'Tab' || !container) return
  const focusables = Array.from(container.querySelectorAll(
    'button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])'
  )).filter((element) => element.offsetParent !== null)
  if (!focusables.length) return
  const first = focusables[0]
  const last = focusables[focusables.length - 1]
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}

function onKeydown(event) {
  if (discardConfirmVisible.value) return
  if (event.key === 'Escape') {
    event.preventDefault()
    if (upstreamModelsDialogVisible.value) {
      closeUpstreamModelsDialog()
      return
    }
    requestCancel()
    return
  }
  if (upstreamModelsDialogVisible.value) {
    const list = filteredUpstreamModels.value
    if (!list.length) return
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      focusedUpstreamIndex.value = (focusedUpstreamIndex.value + 1) % list.length
      scrollUpstreamIntoView(focusedUpstreamIndex.value)
      return
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault()
      focusedUpstreamIndex.value = (focusedUpstreamIndex.value - 1 + list.length) % list.length
      scrollUpstreamIntoView(focusedUpstreamIndex.value)
      return
    }
    if (event.key === 'Enter') {
      event.preventDefault()
      selectUpstreamModel(list[focusedUpstreamIndex.value])
      return
    }
  }
  trapFocus(event, upstreamModelsDialogVisible.value ? upstreamModelsDialogRef.value : modalRef.value)
}

function closeForm() {
  discardConfirmVisible.value = false
  emit('cancel')
}

function requestCancel() {
  if (props.saving) return
  if (isDirty.value) {
    discardConfirmVisible.value = true
    return
  }
  closeForm()
}

watch(() => form.value.provider, (val) => {
  if (loadingModel.value) return
  const options = val === 'anthropic'
    ? ['/messages']
    : val === 'gemini'
      ? ['/v1beta/models/{model}:streamGenerateContent']
      : val === 'codex'
        ? ['/codex/responses']
        : ['/chat/completions', '/responses']
  if (!form.value.disable_auto_suffix && !options.includes(form.value.endpoint)) {
    form.value.endpoint = options[0]
  }
  applyDefaultPrices(val)
})

watch(
  () => [form.value.provider, form.value.base_url, form.value.api_key, form.value.proxy_url],
  () => {
    if (!syncingUpstreamModels.value) resetUpstreamModels()
  }
)

watch(upstreamModelSearch, () => {
  focusedUpstreamIndex.value = 0
})

const providerOptions = [
  { value: 'openai', label: 'OpenAI', icon: '◎' },
  { value: 'anthropic', label: 'Anthropic', icon: 'AI' },
  { value: 'gemini', label: 'Gemini', icon: '✦' },
  { value: 'codex', label: 'Codex', icon: '⬡' },
]

const providerPlaceholders = computed(() => {
  switch (form.value.provider) {
    case 'anthropic':
      return {
        name: 'Anthropic - claude-3-5-sonnet',
        modelId: 'claude-3-5-sonnet-20241022',
        baseUrl: 'https://api.anthropic.com/v1',
        apiKey: 'sk-ant-...'
      }
    case 'gemini':
      return {
        name: 'Gemini - gemini-2.5-pro',
        modelId: 'gemini-2.5-pro',
        baseUrl: 'https://generativelanguage.googleapis.com',
        apiKey: 'AIza...'
      }
    case 'codex':
      return {
        name: 'Codex - gpt-5-codex',
        modelId: 'gpt-5-codex',
        baseUrl: 'https://chatgpt.com/backend-api',
        token: '粘贴 Codex refresh_token，或通过下方授权链接生成 auth JSON'
      }
    default:
      return {
        name: 'OpenAI - gpt-4.1',
        modelId: 'gpt-4.1',
        baseUrl: 'https://api.openai.com/v1',
        apiKey: 'sk-...'
      }
  }
})

const endpointOptions = computed(() => {
  if (form.value.provider === 'anthropic') {
    return ['/messages']
  }
  if (form.value.provider === 'gemini') {
    return ['/v1beta/models/{model}:streamGenerateContent']
  }
  if (form.value.provider === 'codex') {
    return ['/codex/responses']
  }
  return ['/chat/completions', '/responses']
})

const canSyncUpstreamModels = computed(() => {
  return form.value.provider !== 'codex'
    && Boolean(form.value.base_url?.trim())
    && Boolean(form.value.api_key?.trim())
})

const filteredUpstreamModels = computed(() => {
  const keyword = upstreamModelSearch.value.trim().toLowerCase()
  if (!keyword) return upstreamModels.value
  return upstreamModels.value.filter((model) => model.toLowerCase().includes(keyword))
})

function scrollUpstreamIntoView(index) {
  nextTick(() => {
    const container = upstreamModelsDialogRef.value?.querySelector('[data-upstream-list]')
    if (!container) return
    const items = container.querySelectorAll('[data-upstream-item]')
    items[index]?.scrollIntoView({ block: 'nearest' })
  })
}

function focusUpstreamItem(index) {
  focusedUpstreamIndex.value = index
}

const reasoningOptions = computed(() => {
  const options = [
    { value: '', label: '自动' },
    { value: 'low', label: '低' },
    { value: 'medium', label: '中' },
    { value: 'high', label: '高' }
  ]
  if (form.value.provider === 'openai' || form.value.provider === 'codex') {
    options.push({ value: 'xhigh', label: '极高' })
  }
  return options
})

const imageInputOptions = [
  { value: 'auto', label: '自动' },
  { value: 'supported', label: '支持' },
  { value: 'unsupported', label: '不支持' }
]

const codexServiceTierOptions = [
  { value: '', label: '标准' },
  { value: 'fast', label: '快速' }
]

const NO_AUTO_SUFFIX_VALUE = '__no_auto_suffix__'
const endpointSelectValue = computed({
  get() {
    return form.value.disable_auto_suffix ? NO_AUTO_SUFFIX_VALUE : form.value.endpoint
  },
  set(value) {
    if (value === NO_AUTO_SUFFIX_VALUE) {
      form.value.disable_auto_suffix = true
      return
    }
    form.value.disable_auto_suffix = false
    form.value.endpoint = value
  }
})

const defaultPrices = {
  openai: { input_price: 5, output_price: 30, cache_input_price: 0.5, cache_output_price: 30 },
  anthropic: { input_price: 5, output_price: 25, cache_input_price: 0.5, cache_output_price: 6.25 },
  gemini: { input_price: 1.5, output_price: 9, cache_input_price: 0.15, cache_output_price: 1.5 },
  codex: { input_price: 5, output_price: 30, cache_input_price: 0.5, cache_output_price: 30 },
}

function applyDefaultPrices(provider) {
  const prices = defaultPrices[provider] || defaultPrices.openai
  form.value = { ...form.value, ...prices }
}

function handleSave() {
  if (props.saving) return
  emit('save', { ...form.value })
}

function resetUpstreamModels() {
  upstreamModels.value = []
  upstreamModelSearch.value = ''
  upstreamModelsError.value = ''
  upstreamModelsDialogVisible.value = false
}

function upstreamModelsConfigKey() {
  return JSON.stringify([
    form.value.provider,
    form.value.base_url,
    form.value.api_key,
    form.value.proxy_url
  ])
}

async function handleSyncUpstreamModels() {
  if (syncingUpstreamModels.value || !canSyncUpstreamModels.value) return
  const configKey = upstreamModelsConfigKey()
  syncingUpstreamModels.value = true
  upstreamModelsError.value = ''
  try {
    const data = await fetchUpstreamModels({
      source_id: props.model?.id || '',
      provider: form.value.provider,
      base_url: form.value.base_url,
      api_key: form.value.api_key,
      proxy_url: form.value.proxy_url
    })
    if (upstreamModelsConfigKey() !== configKey) {
      resetUpstreamModels()
      return
    }
    upstreamModels.value = Array.isArray(data?.models) ? data.models : []
    upstreamModelSearch.value = ''
    focusedUpstreamIndex.value = 0
    if (upstreamModels.value.length === 0) {
      upstreamModelsError.value = '上游未返回可用模型'
    } else {
      upstreamModelsDialogVisible.value = true
      nextTick(() => upstreamModelsDialogRef.value?.querySelector('input')?.focus())
    }
  } catch (e) {
    if (upstreamModelsConfigKey() !== configKey) {
      resetUpstreamModels()
      return
    }
    upstreamModels.value = []
    upstreamModelsError.value = e?.message || '同步上游模型失败'
  } finally {
    syncingUpstreamModels.value = false
  }
}

function selectUpstreamModel(modelID) {
  form.value.model_id = modelID
  if (!form.value.name.trim()) {
    form.value.name = modelID
  }
  closeUpstreamModelsDialog()
}

function closeUpstreamModelsDialog() {
  upstreamModelsDialogVisible.value = false
  upstreamModelSearch.value = ''
  focusedUpstreamIndex.value = 0
}

async function handleStartOAuth() {
  if (oauthLoading.value) return
  oauthLoading.value = true
  oauthError.value = ''
  try {
    const data = await startCodexOAuth()
    oauthAuthUrl.value = data.auth_url || ''
    if (oauthAuthUrl.value) {
      window.open(oauthAuthUrl.value, '_blank', 'noopener,noreferrer')
    }
  } catch (e) {
    oauthError.value = e?.message || '创建 Codex 授权链接失败'
  } finally {
    oauthLoading.value = false
  }
}

async function handleExchangeOAuth() {
  if (oauthLoading.value) return
  oauthLoading.value = true
  oauthError.value = ''
  try {
    const data = await exchangeCodexOAuth(oauthCallbackUrl.value, form.value.proxy_url)
    const payload = {
      access_token: data.access_token,
      refresh_token: data.refresh_token,
      expires_at: data.expires_at || data.access_token_expires_at
    }
    form.value.refresh_token = JSON.stringify(payload, null, 2)
    oauthCallbackUrl.value = ''
  } catch (e) {
    oauthError.value = e?.message || 'Codex 授权回调处理失败'
  } finally {
    oauthLoading.value = false
  }
}
</script>

<template>
  <Teleport to="body">
    <Transition name="motion-fade" appear>
    <div v-if="visible" class="fixed inset-0 z-[var(--z-modal)] flex items-center justify-center bg-black/20 px-4 py-6">
      <div class="absolute inset-0" @click="requestCancel"></div>

      <div ref="modalRef" role="dialog" aria-modal="true" aria-labelledby="model-form-title" class="motion-pop-panel relative flex max-h-[86vh] w-full max-w-[900px] flex-col overflow-hidden rounded-xl border border-line bg-surface shadow-xl">
        <div class="flex shrink-0 items-center justify-between px-4 py-3">
          <div class="flex items-center gap-3">
            <h2 id="model-form-title" class="text-lg font-semibold text-ink">
              {{ model ? '编辑模型' : '新增模型' }}
            </h2>
            <span class="rounded-md bg-primary-tint px-2 py-0.5 text-[11px] font-medium text-ink-3">配置上游通道</span>
          </div>
          <button
            type="button"
            class="flex h-7 w-7 items-center justify-center rounded-full text-xl leading-none text-ink hover:bg-hover disabled:opacity-50"
            :disabled="saving"
            aria-label="关闭"
            @click="requestCancel"
          >×</button>
        </div>

        <div class="flex-1 overflow-y-auto px-4 pb-3">
          <section class="rounded-lg border border-line bg-surface p-3">
            <div class="grid grid-cols-1 items-center gap-2.5 rounded-lg border border-line-2 bg-surface p-2 sm:grid-cols-[72px_1fr]">
              <div class="text-xs font-semibold text-ink">Provider</div>
              <div class="grid grid-cols-2 gap-1.5 sm:grid-cols-4">
                <button
                  v-for="provider in providerOptions"
                  :key="provider.value"
                  type="button"
                  class="flex h-9 items-center justify-center gap-1.5 rounded-md border text-xs font-medium transition-colors"
                  :class="form.provider === provider.value
                    ? 'border-primary-tint-line-strong bg-primary-tint text-primary-fg shadow-sm'
                    : 'border-line-2 bg-surface text-ink-2 hover:border-line hover:bg-hover'"
                  @click="form.provider = provider.value"
                >
                  <span class="text-base font-semibold leading-none">{{ provider.icon }}</span>
                  <span>{{ provider.label }}</span>
                </button>
              </div>
            </div>
            <div v-if="form.provider === 'codex'" class="mt-3 rounded-md border border-warning-tint-line bg-warning-tint px-3 py-2 text-xs text-warning-fg">
              Codex OAuth 反向代理：使用 ChatGPT 订阅的 Codex OAuth 反代可能违反 OpenAI 服务条款
            </div>
          </section>

          <section class="mt-3 rounded-lg border border-line bg-surface p-3">
            <h3 class="mb-3 text-sm font-semibold text-ink">基础信息</h3>
            <div class="grid grid-cols-1 gap-x-8 gap-y-3 sm:grid-cols-2">
              <div>
                <label class="mb-1.5 block text-xs font-medium text-ink">下拉显示名称 <span class="text-danger">*</span></label>
                <input v-model="form.name"
                  class="h-9 w-full rounded-md border border-line bg-surface px-2.5 text-xs text-ink focus:border-primary-tint-line-strong focus:outline-none focus:ring-2 focus:ring-primary-ring"
                  :placeholder="providerPlaceholders.name" />
                <p class="mt-1 text-[11px] text-ink-4">显示名称用于 Kiro 模型下拉</p>
              </div>

              <div>
                <label class="mb-1.5 block text-xs font-medium text-ink">请求端点 endpoint <span class="text-danger">*</span></label>
                <select v-model="endpointSelectValue"
                  class="h-9 w-full rounded-md border border-line bg-surface px-2.5 text-xs text-ink focus:border-primary-tint-line-strong focus:outline-none focus:ring-2 focus:ring-primary-ring">
                  <option v-for="ep in endpointOptions" :key="ep" :value="ep">{{ ep }}</option>
                  <option :value="NO_AUTO_SUFFIX_VALUE">不自动添加后缀</option>
                </select>
                <p class="mt-1 text-[11px] text-ink-4">选择“不自动添加后缀”时，只访问 base URL，不再拼接 endpoint</p>
              </div>

              <div>
                <label class="mb-1.5 block text-xs font-medium text-ink">模型标识 <span class="text-danger">*</span></label>
                <div class="flex gap-2">
                  <input v-model="form.model_id"
                    class="h-9 min-w-0 flex-1 rounded-md border border-line bg-surface px-2.5 text-xs text-ink focus:border-primary-tint-line-strong focus:outline-none focus:ring-2 focus:ring-primary-ring"
                    :placeholder="providerPlaceholders.modelId" />
                  <button
                    v-if="form.provider !== 'codex'"
                    type="button"
                    class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-primary-tint-line text-primary-fg hover:bg-primary-tint disabled:cursor-not-allowed disabled:opacity-50"
                    :disabled="syncingUpstreamModels || !canSyncUpstreamModels"
                    title="同步上游支持的模型"
                    aria-label="同步上游支持的模型"
                    @click="handleSyncUpstreamModels"
                  >
                    <IconRefresh class="h-4 w-4" :class="syncingUpstreamModels ? 'animate-spin' : ''" />
                  </button>
                </div>
                <p class="mt-1 text-[11px] text-ink-4">模型标识会发送给上游 API</p>
                <p v-if="upstreamModelsError" class="mt-1.5 text-[11px] text-danger">{{ upstreamModelsError }}</p>
              </div>

              <div :class="form.provider === 'codex' ? 'sm:col-span-2' : ''">
                <label class="mb-1.5 block text-xs font-medium text-ink">{{ form.provider === 'codex' ? 'Refresh Token / Codex Auth JSON' : '访问密钥 API Key' }} <span class="text-danger">*</span></label>
                <div class="relative">
                  <textarea v-if="form.provider === 'codex'" v-model="form.refresh_token"
                    :rows="showKey ? 5 : 2"
                    :class="showKey ? '' : 'blur-sm'"
                    class="w-full rounded-md border border-line bg-surface px-3 py-2 pr-10 text-sm text-ink resize-y focus:border-primary-tint-line-strong focus:outline-none focus:ring-2 focus:ring-primary-ring"
                    :placeholder="providerPlaceholders.token"></textarea>
                  <input v-else v-model="form.api_key"
                    :type="showKey ? 'text' : 'password'"
                    class="h-9 w-full rounded-md border border-line bg-surface px-2.5 pr-14 text-xs text-ink focus:border-primary-tint-line-strong focus:outline-none focus:ring-2 focus:ring-primary-ring"
                    :placeholder="providerPlaceholders.apiKey" />
                  <button
                    type="button"
                    class="absolute right-3 top-2.5 flex items-center gap-1 text-xs text-ink-4 hover:text-ink"
                    @click="showKey = !showKey"
                  >
                    <IconEyeOff v-if="showKey" class="h-4 w-4" />
                    <IconEye v-else class="h-4 w-4" />
                    <span v-if="form.provider !== 'codex'">显示</span>
                  </button>
                </div>
                <p class="mt-1 text-[11px] text-ink-4">用于请求上游 API 的鉴权</p>

                <div v-if="form.provider === 'codex'" class="mt-3 space-y-3 rounded-md border border-line bg-surface-2 p-3">
                  <div class="flex items-center justify-between gap-3">
                    <div>
                      <div class="text-xs font-medium text-ink-2">Codex OAuth 授权登录</div>
                      <p class="mt-1 text-[10px] text-ink-3">授权后浏览器会跳转到 localhost 回调地址；如果页面无法打开，复制地址栏完整 URL 到下方处理。</p>
                    </div>
                    <button type="button"
                      class="shrink-0 rounded-md bg-primary px-3 py-1.5 text-xs text-white hover:bg-primary-hover disabled:cursor-not-allowed disabled:opacity-50"
                      :disabled="oauthLoading"
                      @click="handleStartOAuth">{{ oauthLoading ? '处理中...' : '生成授权链接' }}</button>
                  </div>

                  <div v-if="oauthAuthUrl" class="space-y-2">
                    <a :href="oauthAuthUrl" target="_blank" rel="noopener noreferrer" class="inline-flex text-xs text-primary-fg hover:underline">打开授权链接</a>
                    <input :value="oauthAuthUrl" readonly
                      class="w-full rounded-md border border-line bg-surface px-3 py-2 text-xs text-ink-3 focus:border-primary-tint-line-strong focus:outline-none focus:ring-2 focus:ring-primary-ring" />
                  </div>

                  <div class="space-y-2">
                    <textarea v-model="oauthCallbackUrl" rows="2"
                      class="w-full resize-none rounded-md border border-line bg-surface px-3 py-2 text-sm text-ink focus:border-primary-tint-line-strong focus:outline-none focus:ring-2 focus:ring-primary-ring"
                      placeholder="粘贴 http://localhost:1455/auth/callback?... 回调 URL"></textarea>
                    <div class="flex items-center justify-between gap-3">
                      <p class="text-[10px] text-ink-3">处理成功后会自动填入上方 Refresh Token / Auth JSON。</p>
                      <button type="button"
                        class="rounded-md border border-primary-tint-line-strong px-3 py-1.5 text-xs text-primary-fg hover:bg-primary-tint disabled:cursor-not-allowed disabled:opacity-50"
                        :disabled="oauthLoading || !oauthCallbackUrl.trim()"
                        @click="handleExchangeOAuth">处理回调</button>
                    </div>
                  </div>

                  <div v-if="oauthError" class="rounded-md border border-danger-tint-line bg-danger-tint px-3 py-2 text-xs text-danger whitespace-pre-wrap">
                    {{ oauthError }}
                  </div>
                </div>
              </div>

              <div>
                <label class="mb-1.5 block text-xs font-medium text-ink">接口地址 base URL <span class="text-danger">*</span></label>
                <input v-model="form.base_url"
                  class="h-9 w-full rounded-md border border-line bg-surface px-2.5 text-xs text-ink focus:border-primary-tint-line-strong focus:outline-none focus:ring-2 focus:ring-primary-ring"
                  :placeholder="providerPlaceholders.baseUrl" />
                <p class="mt-1 text-[11px] text-ink-4">用于请求上游 API</p>
              </div>

              <div>
                <label class="mb-1.5 block text-xs font-medium text-ink">单模型代理地址</label>
                <input v-model="form.proxy_url"
                  class="h-9 w-full rounded-md border border-line bg-surface px-2.5 text-xs text-ink focus:border-primary-tint-line-strong focus:outline-none focus:ring-2 focus:ring-primary-ring"
                  placeholder="可选，留空表示直连" />
                <p class="mt-1 text-[11px] text-ink-4">代理地址为空时表示直连</p>
              </div>

              <div class="grid grid-cols-1 items-center gap-1.5 sm:col-span-2 sm:grid-cols-[132px_1fr] sm:gap-3">
                <label class="text-xs font-medium text-ink">备注描述</label>
                <div class="relative">
                  <input v-model="form.note"
                    maxlength="200"
                    class="h-9 w-full rounded-md border border-line bg-surface px-2.5 pr-12 text-xs text-ink focus:border-primary-tint-line-strong focus:outline-none focus:ring-2 focus:ring-primary-ring"
                    placeholder="可选，便于识别与管理" />
                  <span class="absolute right-3 top-2.5 text-xs text-ink-4">{{ (form.note || '').length }}/200</span>
                </div>
              </div>
            </div>
          </section>

          <section class="mt-2.5 rounded-lg border border-line bg-surface p-2.5">
            <h3 class="mb-1 text-sm font-semibold text-ink">模型能力参数</h3>
            <div class="grid grid-cols-1 items-start gap-2.5 sm:grid-cols-[minmax(180px,1.45fr)_minmax(104px,.78fr)_minmax(104px,.78fr)]">
              <div>
                <label class="mb-1.5 flex items-center gap-1 text-xs font-medium text-ink">
                  推理强度
                  <span class="group relative inline-flex h-3.5 w-3.5 shrink-0 cursor-help items-center justify-center rounded-full border border-line text-[9px] font-normal leading-none text-ink-4">
                    i
                    <span class="pointer-events-none absolute bottom-5 left-0 z-20 w-48 rounded-md border border-line bg-tooltip px-2 py-1.5 text-left text-[11px] font-normal leading-4 text-tooltip-text shadow-lg opacity-0 group-hover:opacity-100 transition-opacity duration-[var(--dur-fast)] delay-0 group-hover:delay-[250ms]">
                      控制模型推理投入强度；自动表示由系统或上游模型自行决定
                    </span>
                  </span>
                </label>
                <div class="grid h-8 overflow-hidden rounded-md border border-line bg-surface-2" :class="reasoningOptions.length === 5 ? 'grid-cols-5' : 'grid-cols-4'">
                  <button
                    v-for="opt in reasoningOptions"
                    :key="opt.value"
                    type="button"
                    class="border-r border-line text-xs leading-none last:border-r-0"
                    :class="form.reasoning_effort === opt.value ? 'bg-primary-tint text-primary-fg ring-1 ring-inset ring-primary-tint-line-strong' : 'text-ink-2 hover:bg-surface'"
                    @click="form.reasoning_effort = opt.value"
                  >{{ opt.label }}</button>
                </div>
              </div>

              <div>
                <label class="mb-1.5 flex items-center gap-1 text-xs font-medium text-ink">
                  上下文窗口
                  <span class="group relative inline-flex h-3.5 w-3.5 shrink-0 cursor-help items-center justify-center rounded-full border border-line text-[9px] font-normal leading-none text-ink-4">
                    i
                    <span class="pointer-events-none absolute bottom-5 right-0 z-20 w-44 rounded-md border border-line bg-tooltip px-2 py-1.5 text-left text-[11px] font-normal leading-4 text-tooltip-text shadow-lg opacity-0 group-hover:opacity-100 transition-opacity duration-[var(--dur-fast)] delay-0 group-hover:delay-[250ms] sm:w-48">
                      模型可处理的最大上下文 Token 数，0 表示使用默认值
                    </span>
                  </span>
                </label>
                <input v-model.number="form.context_window" type="number" min="0"
                  class="h-9 w-full rounded-md border border-line bg-surface px-2.5 text-xs text-ink focus:border-primary-tint-line-strong focus:outline-none focus:ring-2 focus:ring-primary-ring"
                  placeholder="128000" />
              </div>

              <div>
                <label class="mb-1.5 flex items-center gap-1 whitespace-nowrap text-xs font-medium text-ink">
                  最大输出 Token
                  <span class="group relative inline-flex h-3.5 w-3.5 shrink-0 cursor-help items-center justify-center rounded-full border border-line text-[9px] font-normal leading-none text-ink-4">
                    i
                    <span class="pointer-events-none absolute bottom-5 right-0 z-20 w-44 whitespace-normal rounded-md border border-line bg-tooltip px-2 py-1.5 text-left text-[11px] font-normal leading-4 text-tooltip-text shadow-lg opacity-0 group-hover:opacity-100 transition-opacity duration-[var(--dur-fast)] delay-0 group-hover:delay-[250ms]">
                      <span class="block">单次响应允许的最大输出 Token 数，</span>
                      <span class="block">0 表示使用默认值</span>
                    </span>
                  </span>
                </label>
                <input v-model.number="form.max_tokens" type="number" min="0"
                  class="h-9 w-full rounded-md border border-line bg-surface px-2.5 text-xs text-ink focus:border-primary-tint-line-strong focus:outline-none focus:ring-2 focus:ring-primary-ring"
                  placeholder="4096" />
              </div>
            </div>

            <div class="mt-2.5 grid grid-cols-1 items-start gap-2.5 border-t border-line-2 pt-2.5 sm:grid-cols-[minmax(180px,.88fr)_minmax(0,1.12fr)]">
              <div class="grid grid-cols-1 gap-2.5" :class="form.provider === 'codex' ? 'sm:grid-cols-2' : ''">
                <div>
                  <label class="mb-1.5 flex items-center gap-1 text-xs font-medium text-ink">
                    图片输入
                    <span class="group relative inline-flex h-3.5 w-3.5 shrink-0 cursor-help items-center justify-center rounded-full border border-line text-[9px] font-normal leading-none text-ink-4">
                      i
                      <span class="pointer-events-none absolute bottom-5 left-0 z-20 w-52 rounded-md border border-line bg-tooltip px-2 py-1.5 text-left text-[11px] font-normal leading-4 text-tooltip-text shadow-lg opacity-0 group-hover:opacity-100 transition-opacity duration-[var(--dur-fast)] delay-0 group-hover:delay-[250ms]">
                        自动会根据上游响应学习能力；明确设置后，图片请求会在调度前过滤不兼容模型
                      </span>
                    </span>
                  </label>
                  <div class="grid h-8 grid-cols-3 overflow-hidden rounded-md border border-line bg-surface-2">
                    <button
                      v-for="opt in imageInputOptions"
                      :key="opt.value"
                      type="button"
                      class="border-r border-line text-xs leading-none last:border-r-0"
                      :class="form.image_input_mode === opt.value ? 'bg-primary-tint text-primary-fg ring-1 ring-inset ring-primary-tint-line-strong' : 'text-ink-2 hover:bg-surface'"
                      @click="form.image_input_mode = opt.value"
                    >{{ opt.label }}</button>
                  </div>
                </div>

                <div v-if="form.provider === 'codex'">
                  <label class="mb-1.5 flex items-center gap-1 text-xs font-medium text-ink">
                    Codex 速度
                    <span class="group relative inline-flex h-3.5 w-3.5 shrink-0 cursor-help items-center justify-center rounded-full border border-line text-[9px] font-normal leading-none text-ink-4">
                      i
                      <span class="pointer-events-none absolute bottom-5 left-0 z-20 w-52 rounded-md border border-line bg-tooltip px-2 py-1.5 text-left text-[11px] font-normal leading-4 text-tooltip-text shadow-lg opacity-0 group-hover:opacity-100 transition-opacity duration-[var(--dur-fast)] delay-0 group-hover:delay-[250ms]">
                        快速模式会向 Codex 上游发送 service_tier=priority；标准模式不发送该字段
                      </span>
                    </span>
                  </label>
                  <div class="grid h-8 grid-cols-2 overflow-hidden rounded-md border border-line bg-surface-2">
                    <button
                      v-for="opt in codexServiceTierOptions"
                      :key="opt.value"
                      type="button"
                      class="border-r border-line text-xs leading-none last:border-r-0"
                      :class="form.service_tier === opt.value ? 'bg-success-tint text-success-fg ring-1 ring-inset ring-success-tint-line' : 'text-ink-2 hover:bg-surface'"
                      @click="form.service_tier = opt.value"
                    >{{ opt.label }}</button>
                  </div>
                </div>
              </div>

              <div class="space-y-2.5 sm:border-l sm:border-line-2 sm:pl-3.5">
                <div class="flex min-h-[44px] items-center justify-between gap-3">
                  <div class="min-w-0">
                    <div class="flex items-center gap-1 text-xs font-medium text-ink">
                      响应续接
                      <span class="group relative inline-flex h-3.5 w-3.5 shrink-0 cursor-help items-center justify-center rounded-full border border-line text-[9px] font-normal leading-none text-ink-4">
                        i
                        <span class="pointer-events-none absolute bottom-5 right-0 z-20 w-56 rounded-md border border-line bg-tooltip px-2 py-1.5 text-left text-[11px] font-normal leading-4 text-tooltip-text shadow-lg opacity-0 group-hover:opacity-100 transition-opacity duration-[var(--dur-fast)] delay-0 group-hover:delay-[250ms]">
                          启用 continuation / previous_response_id，仅适用于直连 OpenAI 官方 API
                        </span>
                      </span>
                    </div>
                    <p class="mt-1 text-[11px] leading-4 text-ink-4">直连 OpenAI 官方 API 时启用，中转服务保持关闭</p>
                  </div>
                  <label class="relative inline-flex shrink-0 cursor-pointer items-center">
                    <input type="checkbox" v-model="form.enable_continuation" class="peer sr-only" />
                    <div class="h-5 w-9 rounded-full bg-hover-strong after:absolute after:left-[2px] after:top-[2px] after:h-4 after:w-4 after:rounded-full after:border after:border-line-strong after:bg-surface after:transition-transform after:content-[''] peer-checked:bg-primary peer-checked:after:translate-x-full peer-checked:after:border-white"></div>
                  </label>
                </div>

                <div class="flex min-h-[52px] items-center justify-between gap-3 border-t border-line-2 pt-2.5">
                  <div class="min-w-0">
                    <div class="text-xs font-medium text-ink">OpenCode 会话兼容</div>
                    <p class="mt-1 text-[11px] leading-4 text-ink-4">仅在上游需要时开启，发送 x-opencode-session；客户端标识由 Provider 决定。</p>
                  </div>
                  <label class="relative inline-flex shrink-0 cursor-pointer items-center">
                    <input type="checkbox" v-model="form.enable_opencode_session" aria-label="OpenCode 会话兼容" class="peer sr-only" />
                    <div class="h-5 w-9 rounded-full bg-hover-strong after:absolute after:left-[2px] after:top-[2px] after:h-4 after:w-4 after:rounded-full after:border after:border-line-strong after:bg-surface after:transition-transform after:content-[''] peer-checked:bg-primary peer-checked:after:translate-x-full peer-checked:after:border-white"></div>
                  </label>
                </div>
              </div>
            </div>
          </section>

          <section class="mt-3 rounded-lg border border-line bg-surface p-3">
            <div class="mb-2.5 flex flex-wrap items-center justify-between gap-2">
              <h3 class="text-sm font-semibold text-ink">价格配置 <span class="text-xs font-normal text-ink-3">（USD / 百万 Token）</span></h3>
              <button type="button" class="text-xs font-medium text-primary-fg hover:underline" @click="applyDefaultPrices(form.provider)">⟳ 恢复当前 Provider 默认价格</button>
            </div>
            <p class="mb-2.5 text-[11px] leading-4 text-ink-4">默认单价仅用于预填，请按实际渠道价格填写。用量费用按请求 Token、单价和费用倍率计算，以 USD 显示。</p>
            <div class="grid grid-cols-1 gap-3 sm:grid-cols-5">
              <div>
                <label class="mb-1.5 block text-xs font-medium text-ink">输入价格</label>
                <div class="relative">
                  <input v-model.number="form.input_price" type="number" step="0.001" min="0"
                    class="h-9 w-full rounded-md border border-line bg-surface px-2.5 pr-9 text-xs text-ink focus:border-primary-tint-line-strong focus:outline-none focus:ring-2 focus:ring-primary-ring" />
                  <span class="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-[10px] text-ink-3">USD</span>
                </div>
              </div>
              <div class="sm:border-l sm:border-line sm:pl-3">
                <label class="mb-1.5 block text-xs font-medium text-ink">输出价格</label>
                <div class="relative">
                  <input v-model.number="form.output_price" type="number" step="0.001" min="0"
                    class="h-9 w-full rounded-md border border-line bg-surface px-2.5 pr-9 text-xs text-ink focus:border-primary-tint-line-strong focus:outline-none focus:ring-2 focus:ring-primary-ring" />
                  <span class="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-[10px] text-ink-3">USD</span>
                </div>
              </div>
              <div class="sm:border-l sm:border-line sm:pl-3">
                <label class="mb-1.5 block text-xs font-medium text-ink">缓存输入价格</label>
                <div class="relative">
                  <input v-model.number="form.cache_input_price" type="number" step="0.001" min="0"
                    class="h-9 w-full rounded-md border border-line bg-surface px-2.5 pr-9 text-xs text-ink focus:border-primary-tint-line-strong focus:outline-none focus:ring-2 focus:ring-primary-ring" />
                  <span class="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-[10px] text-ink-3">USD</span>
                </div>
              </div>
              <div class="sm:border-l sm:border-line sm:pl-3">
                <label class="mb-1.5 block text-xs font-medium text-ink">缓存输出价格</label>
                <div class="relative">
                  <input v-model.number="form.cache_output_price" type="number" step="0.001" min="0"
                    class="h-9 w-full rounded-md border border-line bg-surface px-2.5 pr-9 text-xs text-ink focus:border-primary-tint-line-strong focus:outline-none focus:ring-2 focus:ring-primary-ring" />
                  <span class="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-[10px] text-ink-3">USD</span>
                </div>
              </div>
              <div class="sm:border-l sm:border-line sm:pl-3">
                <label class="mb-1.5 block text-xs font-medium text-ink">本地费用倍率</label>
                <input v-model.number="form.rate_multiplier" type="number" step="0.01" min="0"
                  class="h-9 w-full rounded-md border border-line bg-surface px-2.5 text-xs text-ink focus:border-primary-tint-line-strong focus:outline-none focus:ring-2 focus:ring-primary-ring" />
              </div>
            </div>
          </section>
        </div>

        <div class="flex shrink-0 items-center gap-3 border-t border-line-2 px-4 py-3">
          <div class="min-w-0 flex-1">
            <div v-if="error" class="rounded-md bg-danger-tint px-3 py-1.5 text-xs font-medium text-danger whitespace-pre-wrap">
              <span class="mr-2 inline-flex h-5 w-5 items-center justify-center rounded-full border border-danger-tint-line text-xs">×</span>{{ error }}
            </div>
          </div>
          <button type="button" class="h-9 rounded-md border border-line bg-surface px-7 text-xs text-ink-2 shadow-sm hover:bg-hover disabled:opacity-50"
            :disabled="saving" @click="requestCancel">取消</button>
          <button type="button" class="h-9 rounded-md bg-primary px-7 text-xs font-medium text-white shadow-sm hover:bg-primary-hover disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="saving" @click="handleSave">{{ saving ? '保存中...' : '保存' }}</button>
        </div>
      </div>

      <Transition name="motion-fade" appear>
      <div v-if="upstreamModelsDialogVisible" class="fixed inset-0 z-[var(--z-modal-nested)] flex items-center justify-center px-4 py-6">
        <div class="absolute inset-0 bg-black/30" @click="closeUpstreamModelsDialog"></div>
        <div
          ref="upstreamModelsDialogRef"
          role="dialog"
          aria-modal="true"
          aria-labelledby="upstream-models-title"
          class="motion-pop-panel relative flex max-h-[72vh] w-full max-w-[560px] flex-col overflow-hidden rounded-lg border border-line bg-surface shadow-2xl"
        >
          <div class="flex shrink-0 items-center justify-between border-b border-line-2 px-4 py-3">
            <div class="min-w-0">
              <h3 id="upstream-models-title" class="text-sm font-semibold text-ink">选择上游模型</h3>
              <p class="mt-0.5 text-[11px] text-ink-4">
                共 {{ upstreamModels.length }} 个模型<span v-if="upstreamModelSearch.trim()">，匹配 {{ filteredUpstreamModels.length }} 个</span>
              </p>
            </div>
            <button
              type="button"
              class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-ink-3 hover:bg-hover hover:text-ink"
              aria-label="关闭模型选择"
              title="关闭"
              @click="closeUpstreamModelsDialog"
            >
              <IconClose class="h-4 w-4" />
            </button>
          </div>

          <div class="shrink-0 border-b border-line-2 px-4 py-3">
            <div class="relative">
              <IconSearch class="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-ink-4" />
              <input
                v-model="upstreamModelSearch"
                class="h-9 w-full rounded-md border border-line bg-surface pl-9 pr-9 text-xs text-ink focus:border-primary-tint-line-strong focus:outline-none focus:ring-2 focus:ring-primary-ring"
                placeholder="搜索上游模型..."
              />
              <button
                v-if="upstreamModelSearch"
                type="button"
                class="absolute right-3 top-1/2 flex h-4 w-4 -translate-y-1/2 items-center justify-center text-ink-4 hover:text-ink-2"
                aria-label="清除搜索"
                title="清除"
                @click="upstreamModelSearch = ''"
              >
                <IconClose class="h-3.5 w-3.5" />
              </button>
            </div>
          </div>

          <div class="min-h-0 flex-1 overflow-y-auto py-1" data-upstream-list>
            <button
              v-for="(modelID, index) in filteredUpstreamModels"
              :key="modelID"
              type="button"
              data-upstream-item
              class="flex min-h-10 w-full items-center justify-between gap-4 px-4 py-2 text-left transition-colors"
              :class="form.model_id === modelID
                ? 'bg-primary-tint text-primary-fg'
                : focusedUpstreamIndex === index
                  ? 'bg-hover text-ink'
                  : 'text-ink-2 hover:bg-hover'"
              @click="selectUpstreamModel(modelID)"
              @mouseenter="focusUpstreamItem(index)"
            >
              <span class="min-w-0 break-all font-mono text-[12px]">{{ modelID }}</span>
              <IconCheck v-if="form.model_id === modelID" class="h-4 w-4 shrink-0 text-primary-fg" />
            </button>
            <div v-if="filteredUpstreamModels.length === 0" class="flex flex-col items-center justify-center px-4 py-12 text-center">
              <IconSearch class="mb-2 h-7 w-7 text-ink-4" />
              <p class="text-xs font-medium text-ink-3">没有匹配的模型</p>
              <p class="mt-1 text-[11px] text-ink-4">尝试更换搜索关键词</p>
            </div>
          </div>

          <div class="flex shrink-0 items-center justify-between border-t border-line-2 px-4 py-2.5">
            <div class="flex items-center gap-3 text-[10px] text-ink-4">
              <span class="flex items-center gap-1">
                <kbd class="rounded border border-line bg-surface-2 px-1 py-0.5 font-sans text-[10px] leading-none">↑↓</kbd>
                导航
              </span>
              <span class="flex items-center gap-1">
                <kbd class="rounded border border-line bg-surface-2 px-1 py-0.5 font-sans text-[10px] leading-none">Enter</kbd>
                选择
              </span>
              <span class="flex items-center gap-1">
                <kbd class="rounded border border-line bg-surface-2 px-1 py-0.5 font-sans text-[10px] leading-none">Esc</kbd>
                关闭
              </span>
            </div>
            <button
              type="button"
              class="h-9 rounded-md border border-line bg-surface px-5 text-xs text-ink-2 hover:bg-hover"
              @click="closeUpstreamModelsDialog"
            >关闭</button>
          </div>
        </div>
      </div>
      </Transition>
    </div>
    </Transition>
  </Teleport>

  <ConfirmDialog
    :visible="discardConfirmVisible"
    title="放弃未保存的修改？"
    message="当前表单内容尚未保存，关闭后这些修改将丢失。"
    confirm-text="放弃修改"
    cancel-text="继续编辑"
    tone="danger"
    @confirm="closeForm"
    @cancel="discardConfirmVisible = false"
  />
</template>
