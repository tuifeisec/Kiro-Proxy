<script setup>
import { ref, watch, computed } from 'vue'
import { startCodexOAuth, exchangeCodexOAuth } from '../../api/models.js'
import IconEye from '../icons/IconEye.vue'
import IconEyeOff from '../icons/IconEyeOff.vue'

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
  context_window: 0,
  max_tokens: 0,
  reasoning_effort: '',
  rate_multiplier: 1.0,
  input_price: 5,
  output_price: 30,
  cache_input_price: 0.5,
  cache_output_price: 30,
  enable_continuation: false,
  note: ''
})

const form = ref(defaultForm())
const showKey = ref(false)
const loadingModel = ref(false)
const oauthLoading = ref(false)
const oauthAuthUrl = ref('')
const oauthCallbackUrl = ref('')
const oauthError = ref('')

watch(() => props.visible, (val) => {
  if (val) {
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
    loadingModel.value = false
  }
})

watch(() => form.value.provider, (val) => {
  const options = val === 'anthropic'
    ? ['/messages']
    : val === 'gemini'
      ? ['/v1beta/models/{model}:streamGenerateContent']
      : val === 'codex'
        ? ['/codex/responses']
        : ['/chat/completions', '/responses']
  if (!options.includes(form.value.endpoint)) {
    form.value.endpoint = options[0]
  }
  if (!loadingModel.value) {
    applyDefaultPrices(val)
  }
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

const reasoningOptions = [
  { value: '', label: '自动' },
  { value: 'low', label: '低' },
  { value: 'medium', label: '中' },
  { value: 'high', label: '高' }
]

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
    <div v-if="visible" class="fixed inset-0 z-50 flex items-center justify-center bg-black/20 px-4 py-6">
      <div class="absolute inset-0" @click="emit('cancel')"></div>

      <div class="relative flex max-h-[86vh] w-full max-w-[900px] flex-col overflow-hidden rounded-xl border border-[#e5e7eb] bg-white shadow-xl">
        <div class="flex shrink-0 items-center justify-between px-4 py-3">
          <div class="flex items-center gap-3">
            <h2 class="text-lg font-semibold text-[#111827]">
              {{ model ? '编辑模型' : '新增模型' }}
            </h2>
            <span class="rounded-md bg-[#eef2ff] px-2 py-0.5 text-[11px] font-medium text-[#64748b]">配置上游通道</span>
          </div>
          <button
            type="button"
            class="flex h-7 w-7 items-center justify-center rounded-full text-xl leading-none text-[#111827] hover:bg-[#f3f4f6] disabled:opacity-50"
            :disabled="saving"
            aria-label="关闭"
            @click="emit('cancel')"
          >×</button>
        </div>

        <div class="flex-1 overflow-y-auto px-4 pb-3">
          <section class="rounded-lg border border-[#e5e7eb] bg-white p-3">
            <div class="grid grid-cols-[72px_1fr] items-center gap-2.5 rounded-lg border border-[#eef2f7] bg-white p-2">
              <div class="text-xs font-semibold text-[#111827]">Provider</div>
              <div class="grid grid-cols-4 gap-1.5">
                <button
                  v-for="provider in providerOptions"
                  :key="provider.value"
                  type="button"
                  class="flex h-9 items-center justify-center gap-1.5 rounded-md border text-xs font-medium transition-colors"
                  :class="form.provider === provider.value
                    ? 'border-[#93c5fd] bg-[#f8fbff] text-[#2563eb] shadow-sm'
                    : 'border-[#edf2f7] bg-white text-[#374151] hover:border-[#d1d5db] hover:bg-[#f9fafb]'"
                  @click="form.provider = provider.value"
                >
                  <span class="text-base font-semibold leading-none">{{ provider.icon }}</span>
                  <span>{{ provider.label }}</span>
                </button>
              </div>
            </div>
            <div v-if="form.provider === 'codex'" class="mt-3 rounded-md border border-[#f59e0b]/30 bg-[#fffbeb] px-3 py-2 text-xs text-[#92400e]">
              Codex OAuth 反向代理：使用 ChatGPT 订阅的 Codex OAuth 反代可能违反 OpenAI 服务条款
            </div>
          </section>

          <section class="mt-3 rounded-lg border border-[#e5e7eb] bg-white p-3">
            <h3 class="mb-3 text-sm font-semibold text-[#111827]">基础信息</h3>
            <div class="grid grid-cols-2 gap-x-8 gap-y-3">
              <div>
                <label class="mb-1.5 block text-xs font-medium text-[#111827]">下拉显示名称 <span class="text-[#ef4444]">*</span></label>
                <input v-model="form.name"
                  class="h-9 w-full rounded-md border border-[#e5e7eb] bg-white px-2.5 text-xs text-[#111827] focus:border-[#93c5fd] focus:outline-none focus:ring-2 focus:ring-[#dbeafe]"
                  :placeholder="providerPlaceholders.name" />
                <p class="mt-1 text-[11px] text-[#9ca3af]">显示名称用于 Kiro 模型下拉</p>
              </div>

              <div v-if="form.provider !== 'codex'">
                <label class="mb-1.5 block text-xs font-medium text-[#111827]">请求端点 endpoint <span class="text-[#ef4444]">*</span></label>
                <select v-model="form.endpoint"
                  class="h-9 w-full rounded-md border border-[#e5e7eb] bg-white px-2.5 text-xs text-[#111827] focus:border-[#93c5fd] focus:outline-none focus:ring-2 focus:ring-[#dbeafe]">
                  <option v-for="ep in endpointOptions" :key="ep" :value="ep">{{ ep }}</option>
                </select>
                <p class="mt-1 text-[11px] text-[#9ca3af]">endpoint 只表示接口端点</p>
              </div>

              <div>
                <label class="mb-1.5 block text-xs font-medium text-[#111827]">模型标识 <span class="text-[#ef4444]">*</span></label>
                <input v-model="form.model_id"
                  class="h-9 w-full rounded-md border border-[#e5e7eb] bg-white px-2.5 text-xs text-[#111827] focus:border-[#93c5fd] focus:outline-none focus:ring-2 focus:ring-[#dbeafe]"
                  :placeholder="providerPlaceholders.modelId" />
                <p class="mt-1 text-[11px] text-[#9ca3af]">模型标识会发送给上游 API</p>
              </div>

              <div :class="form.provider === 'codex' ? 'col-span-2' : ''">
                <label class="mb-1.5 block text-xs font-medium text-[#111827]">{{ form.provider === 'codex' ? 'Refresh Token / Codex Auth JSON' : '访问密钥 API Key' }} <span class="text-[#ef4444]">*</span></label>
                <div class="relative">
                  <textarea v-if="form.provider === 'codex'" v-model="form.refresh_token"
                    :rows="showKey ? 5 : 2"
                    :class="showKey ? '' : 'blur-sm'"
                    class="w-full rounded-md border border-[#e5e7eb] bg-white px-3 py-2 pr-10 text-sm text-[#111827] resize-y focus:border-[#93c5fd] focus:outline-none focus:ring-2 focus:ring-[#dbeafe]"
                    :placeholder="providerPlaceholders.token"></textarea>
                  <input v-else v-model="form.api_key"
                    :type="showKey ? 'text' : 'password'"
                    class="h-9 w-full rounded-md border border-[#e5e7eb] bg-white px-2.5 pr-14 text-xs text-[#111827] focus:border-[#93c5fd] focus:outline-none focus:ring-2 focus:ring-[#dbeafe]"
                    :placeholder="providerPlaceholders.apiKey" />
                  <button
                    type="button"
                    class="absolute right-3 top-2.5 flex items-center gap-1 text-xs text-[#9ca3af] hover:text-[#111827]"
                    @click="showKey = !showKey"
                  >
                    <IconEyeOff v-if="showKey" class="h-4 w-4" />
                    <IconEye v-else class="h-4 w-4" />
                    <span v-if="form.provider !== 'codex'">显示</span>
                  </button>
                </div>
                <p class="mt-1 text-[11px] text-[#9ca3af]">用于请求上游 API 的鉴权</p>

                <div v-if="form.provider === 'codex'" class="mt-3 space-y-3 rounded-md border border-[#e5e7eb] bg-[#f9fafb] p-3">
                  <div class="flex items-center justify-between gap-3">
                    <div>
                      <div class="text-xs font-medium text-[#374151]">Codex OAuth 授权登录</div>
                      <p class="mt-1 text-[10px] text-[#6b7280]">授权后浏览器会跳转到 localhost 回调地址；如果页面无法打开，复制地址栏完整 URL 到下方处理。</p>
                    </div>
                    <button type="button"
                      class="shrink-0 rounded-md bg-[#2563eb] px-3 py-1.5 text-xs text-white hover:bg-[#1d4ed8] disabled:cursor-not-allowed disabled:opacity-50"
                      :disabled="oauthLoading"
                      @click="handleStartOAuth">{{ oauthLoading ? '处理中...' : '生成授权链接' }}</button>
                  </div>

                  <div v-if="oauthAuthUrl" class="space-y-2">
                    <a :href="oauthAuthUrl" target="_blank" rel="noopener noreferrer" class="inline-flex text-xs text-[#2563eb] hover:underline">打开授权链接</a>
                    <input :value="oauthAuthUrl" readonly
                      class="w-full rounded-md border border-[#e5e7eb] bg-white px-3 py-2 text-xs text-[#6b7280] focus:outline-none" />
                  </div>

                  <div class="space-y-2">
                    <textarea v-model="oauthCallbackUrl" rows="2"
                      class="w-full resize-none rounded-md border border-[#e5e7eb] bg-white px-3 py-2 text-sm text-[#111827] focus:border-[#93c5fd] focus:outline-none focus:ring-2 focus:ring-[#dbeafe]"
                      placeholder="粘贴 http://localhost:1455/auth/callback?... 回调 URL"></textarea>
                    <div class="flex items-center justify-between gap-3">
                      <p class="text-[10px] text-[#6b7280]">处理成功后会自动填入上方 Refresh Token / Auth JSON。</p>
                      <button type="button"
                        class="rounded-md border border-[#93c5fd] px-3 py-1.5 text-xs text-[#2563eb] hover:bg-[#eff6ff] disabled:cursor-not-allowed disabled:opacity-50"
                        :disabled="oauthLoading || !oauthCallbackUrl.trim()"
                        @click="handleExchangeOAuth">处理回调</button>
                    </div>
                  </div>

                  <div v-if="oauthError" class="rounded-md border border-[#ef4444]/20 bg-[#fef2f2] px-3 py-2 text-xs text-[#ef4444] whitespace-pre-wrap">
                    {{ oauthError }}
                  </div>
                </div>
              </div>

              <div v-if="form.provider !== 'codex'">
                <label class="mb-1.5 block text-xs font-medium text-[#111827]">接口地址 base URL <span class="text-[#ef4444]">*</span></label>
                <input v-model="form.base_url"
                  class="h-9 w-full rounded-md border border-[#e5e7eb] bg-white px-2.5 text-xs text-[#111827] focus:border-[#93c5fd] focus:outline-none focus:ring-2 focus:ring-[#dbeafe]"
                  :placeholder="providerPlaceholders.baseUrl" />
                <p class="mt-1 text-[11px] text-[#9ca3af]">用于请求上游 API</p>
              </div>

              <div>
                <label class="mb-1.5 block text-xs font-medium text-[#111827]">单模型代理地址</label>
                <input v-model="form.proxy_url"
                  class="h-9 w-full rounded-md border border-[#e5e7eb] bg-white px-2.5 text-xs text-[#111827] focus:border-[#93c5fd] focus:outline-none focus:ring-2 focus:ring-[#dbeafe]"
                  placeholder="可选，留空表示直连" />
                <p class="mt-1 text-[11px] text-[#9ca3af]">代理地址为空时表示直连</p>
              </div>

              <div class="col-span-2 grid grid-cols-[132px_1fr] items-center gap-3">
                <label class="text-xs font-medium text-[#111827]">备注描述</label>
                <div class="relative">
                  <input v-model="form.note"
                    maxlength="200"
                    class="h-9 w-full rounded-md border border-[#e5e7eb] bg-white px-2.5 pr-12 text-xs text-[#111827] focus:border-[#93c5fd] focus:outline-none focus:ring-2 focus:ring-[#dbeafe]"
                    placeholder="可选，便于识别与管理" />
                  <span class="absolute right-3 top-2.5 text-xs text-[#9ca3af]">{{ (form.note || '').length }}/200</span>
                </div>
              </div>
            </div>
          </section>

          <section class="mt-3 rounded-lg border border-[#e5e7eb] bg-white p-3">
            <h3 class="mb-2 text-sm font-semibold text-[#111827]">模型能力参数</h3>
            <div class="grid grid-cols-[1.6fr_.72fr_.72fr_1.6fr] items-start gap-4">
              <div class="border-r border-[#e5e7eb] pr-4">
                <label class="mb-1.5 flex items-center gap-1 text-xs font-medium text-[#111827]">
                  推理强度
                  <span class="group relative inline-flex h-3.5 w-3.5 shrink-0 cursor-help items-center justify-center rounded-full border border-[#d1d5db] text-[9px] font-normal leading-none text-[#9ca3af]">
                    i
                    <span class="pointer-events-none absolute bottom-5 left-0 z-20 hidden w-48 rounded-md border border-[#e5e7eb] bg-[#111827] px-2 py-1.5 text-left text-[11px] font-normal leading-4 text-white shadow-lg group-hover:block">
                      控制模型推理投入强度；自动表示由系统或上游模型自行决定
                    </span>
                  </span>
                </label>
                <div class="grid h-9 grid-cols-4 overflow-hidden rounded-md border border-[#e5e7eb] bg-[#f9fafb]">
                  <button
                    v-for="opt in reasoningOptions"
                    :key="opt.value"
                    type="button"
                    class="border-r border-[#e5e7eb] text-xs last:border-r-0"
                    :class="form.reasoning_effort === opt.value ? 'bg-[#eff6ff] text-[#2563eb] ring-1 ring-inset ring-[#93c5fd]' : 'text-[#374151] hover:bg-white'"
                    @click="form.reasoning_effort = opt.value"
                  >{{ opt.label }}</button>
                </div>
              </div>

              <div>
                <label class="mb-1.5 flex items-center gap-1 text-xs font-medium text-[#111827]">
                  上下文窗口
                  <span class="group relative inline-flex h-3.5 w-3.5 shrink-0 cursor-help items-center justify-center rounded-full border border-[#d1d5db] text-[9px] font-normal leading-none text-[#9ca3af]">
                    i
                    <span class="pointer-events-none absolute bottom-5 left-1/2 z-20 hidden w-52 -translate-x-1/2 rounded-md border border-[#e5e7eb] bg-[#111827] px-2 py-1.5 text-left text-[11px] font-normal leading-4 text-white shadow-lg group-hover:block">
                      模型可处理的最大上下文 Token 数，0 表示使用默认值
                    </span>
                  </span>
                </label>
                <input v-model.number="form.context_window" type="number" min="0"
                  class="h-9 w-full rounded-md border border-[#e5e7eb] bg-white px-2.5 text-xs text-[#111827] focus:border-[#93c5fd] focus:outline-none focus:ring-2 focus:ring-[#dbeafe]"
                  placeholder="128000" />
              </div>

              <div>
                <label class="mb-1.5 flex items-center gap-1 whitespace-nowrap text-xs font-medium text-[#111827]">
                  最大输出 Token
                  <span class="group relative inline-flex h-3.5 w-3.5 shrink-0 cursor-help items-center justify-center rounded-full border border-[#d1d5db] text-[9px] font-normal leading-none text-[#9ca3af]">
                    i
                    <span class="pointer-events-none absolute bottom-5 left-1/2 z-20 hidden w-52 -translate-x-1/2 rounded-md border border-[#e5e7eb] bg-[#111827] px-2 py-1.5 text-left text-[11px] font-normal leading-4 text-white shadow-lg group-hover:block">
                      单次响应允许的最大输出 Token 数，0 表示使用默认值
                    </span>
                  </span>
                </label>
                <input v-model.number="form.max_tokens" type="number" min="0"
                  class="h-9 w-full rounded-md border border-[#e5e7eb] bg-white px-2.5 text-xs text-[#111827] focus:border-[#93c5fd] focus:outline-none focus:ring-2 focus:ring-[#dbeafe]"
                  placeholder="4096" />
              </div>

              <div class="flex items-start gap-2.5 border-l border-[#e5e7eb] pl-4 pt-0.5">
                <div class="min-w-0 flex-1">
                  <div class="flex items-center justify-between gap-3">
                    <div class="truncate text-xs font-medium text-[#111827]">启用 continuation / previous_response_id</div>
                    <label class="relative inline-flex shrink-0 cursor-pointer items-center">
                      <input type="checkbox" v-model="form.enable_continuation" class="peer sr-only" />
                      <div class="h-5 w-9 rounded-full bg-[#e5e7eb] after:absolute after:left-[2px] after:top-[2px] after:h-4 after:w-4 after:rounded-full after:border after:border-gray-300 after:bg-white after:transition-all after:content-[''] peer-checked:bg-[#2563eb] peer-checked:after:translate-x-full peer-checked:after:border-white"></div>
                    </label>
                  </div>
                  <p class="mt-1 text-[11px] leading-4 text-[#9ca3af]">仅直连 OpenAI 官方 API 时建议启用；<br />使用中转站时建议关闭</p>
                </div>
              </div>
            </div>
          </section>

          <section class="mt-3 rounded-lg border border-[#e5e7eb] bg-white p-3">
            <div class="mb-2.5 flex items-center justify-between">
              <h3 class="text-sm font-semibold text-[#111827]">Credit 价格配置 <span class="text-xs font-normal text-[#6b7280]">（每百万 Token）</span></h3>
              <button type="button" class="text-xs font-medium text-[#2563eb] hover:underline" @click="applyDefaultPrices(form.provider)">⟳ 恢复当前 Provider 默认价格</button>
            </div>
            <div class="grid grid-cols-5 gap-3">
              <div>
                <label class="mb-1.5 block text-xs font-medium text-[#111827]">输入价格</label>
                <div class="relative">
                  <input v-model.number="form.input_price" type="number" step="0.001" min="0"
                    class="h-9 w-full rounded-md border border-[#e5e7eb] bg-white px-2.5 pr-9 text-xs text-[#111827] focus:border-[#93c5fd] focus:outline-none focus:ring-2 focus:ring-[#dbeafe]" />
                  <span class="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-[10px] text-[#6b7280]">USD</span>
                </div>
              </div>
              <div class="border-l border-[#e5e7eb] pl-3">
                <label class="mb-1.5 block text-xs font-medium text-[#111827]">输出价格</label>
                <div class="relative">
                  <input v-model.number="form.output_price" type="number" step="0.001" min="0"
                    class="h-9 w-full rounded-md border border-[#e5e7eb] bg-white px-2.5 pr-9 text-xs text-[#111827] focus:border-[#93c5fd] focus:outline-none focus:ring-2 focus:ring-[#dbeafe]" />
                  <span class="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-[10px] text-[#6b7280]">USD</span>
                </div>
              </div>
              <div class="border-l border-[#e5e7eb] pl-3">
                <label class="mb-1.5 block text-xs font-medium text-[#111827]">缓存输入价格</label>
                <div class="relative">
                  <input v-model.number="form.cache_input_price" type="number" step="0.001" min="0"
                    class="h-9 w-full rounded-md border border-[#e5e7eb] bg-white px-2.5 pr-9 text-xs text-[#111827] focus:border-[#93c5fd] focus:outline-none focus:ring-2 focus:ring-[#dbeafe]" />
                  <span class="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-[10px] text-[#6b7280]">USD</span>
                </div>
              </div>
              <div class="border-l border-[#e5e7eb] pl-3">
                <label class="mb-1.5 block text-xs font-medium text-[#111827]">缓存输出价格</label>
                <div class="relative">
                  <input v-model.number="form.cache_output_price" type="number" step="0.001" min="0"
                    class="h-9 w-full rounded-md border border-[#e5e7eb] bg-white px-2.5 pr-9 text-xs text-[#111827] focus:border-[#93c5fd] focus:outline-none focus:ring-2 focus:ring-[#dbeafe]" />
                  <span class="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-[10px] text-[#6b7280]">USD</span>
                </div>
              </div>
              <div class="border-l border-[#e5e7eb] pl-3">
                <label class="mb-1.5 block text-xs font-medium text-[#111827]">倍率</label>
                <input v-model.number="form.rate_multiplier" type="number" step="0.01" min="0"
                  class="h-9 w-full rounded-md border border-[#e5e7eb] bg-white px-2.5 text-xs text-[#111827] focus:border-[#93c5fd] focus:outline-none focus:ring-2 focus:ring-[#dbeafe]" />
              </div>
            </div>
          </section>
        </div>

        <div class="flex shrink-0 items-center gap-3 border-t border-[#f1f5f9] px-4 py-3">
          <div class="min-w-0 flex-1">
            <div v-if="error" class="rounded-md bg-[#fff1f2] px-3 py-1.5 text-xs font-medium text-[#dc2626] whitespace-pre-wrap">
              <span class="mr-2 inline-flex h-5 w-5 items-center justify-center rounded-full border border-[#fca5a5] text-xs">×</span>{{ error }}
            </div>
          </div>
          <button type="button" class="h-9 rounded-md border border-[#e5e7eb] bg-white px-7 text-xs text-[#374151] shadow-sm hover:bg-[#f9fafb] disabled:opacity-50"
            :disabled="saving" @click="emit('cancel')">取消</button>
          <button type="button" class="h-9 rounded-md bg-[#2563eb] px-7 text-xs font-medium text-white shadow-sm hover:bg-[#1d4ed8] disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="saving" @click="handleSave">{{ saving ? '保存中...' : '保存' }}</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
