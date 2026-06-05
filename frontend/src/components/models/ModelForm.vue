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
    <div v-if="visible" class="fixed inset-0 z-50 flex items-center justify-center">
      <div class="absolute inset-0 bg-black/30" @click="emit('cancel')"></div>

      <div class="relative bg-white border border-[#e5e7eb] rounded-xl w-full max-w-2xl mx-4 p-6 shadow-xl max-h-[90vh] overflow-y-auto">
        <div class="flex items-start justify-between gap-4 mb-5">
          <h2 class="text-base font-semibold text-[#111827]">
            {{ model ? '编辑模型' : '新增模型' }}
          </h2>
          <div class="flex justify-end gap-3">
            <button type="button" class="px-4 py-2 text-sm text-[#6b7280] hover:text-[#111827] border border-[#e5e7eb] rounded-md disabled:opacity-50"
              :disabled="saving" @click="emit('cancel')">取消</button>
            <button type="button" class="px-4 py-2 text-sm text-white bg-[#10b981] hover:bg-[#059669] rounded-md font-medium disabled:opacity-50 disabled:cursor-not-allowed"
              :disabled="saving" @click="handleSave">{{ saving ? '保存中...' : '保存' }}</button>
          </div>
        </div>

        <div v-if="error" class="mb-4 px-3 py-2 text-sm text-[#ef4444] bg-[#fef2f2] border border-[#ef4444]/20 rounded-md whitespace-pre-wrap">
          {{ error }}
        </div>

        <!-- Provider 切换 -->
        <div class="mb-5">
          <label class="block text-xs text-[#6b7280] mb-1.5">Provider</label>
          <div class="flex gap-2">
            <button
              type="button"
              class="px-4 py-1.5 text-xs rounded-md border transition-colors"
              :class="form.provider === 'openai'
                ? 'bg-[#ecfdf5] border-[#10b981] text-[#10b981]'
                : 'border-[#e5e7eb] text-[#6b7280] hover:text-[#111827]'"
              @click="form.provider = 'openai'"
            >OpenAI</button>
            <button
              type="button"
              class="px-4 py-1.5 text-xs rounded-md border transition-colors"
              :class="form.provider === 'anthropic'
                ? 'bg-[#ecfdf5] border-[#10b981] text-[#10b981]'
                : 'border-[#e5e7eb] text-[#6b7280] hover:text-[#111827]'"
              @click="form.provider = 'anthropic'"
            >Anthropic</button>
            <button
              type="button"
              class="px-4 py-1.5 text-xs rounded-md border transition-colors"
              :class="form.provider === 'gemini'
                ? 'bg-[#ecfdf5] border-[#10b981] text-[#10b981]'
                : 'border-[#e5e7eb] text-[#6b7280] hover:text-[#111827]'"
              @click="form.provider = 'gemini'"
            >Gemini</button>
            <button
              type="button"
              class="px-4 py-1.5 text-xs rounded-md border transition-colors"
              :class="form.provider === 'codex'
                ? 'bg-[#ecfdf5] border-[#10b981] text-[#10b981]'
                : 'border-[#e5e7eb] text-[#6b7280] hover:text-[#111827]'"
              @click="form.provider = 'codex'"
            >Codex</button>
          </div>
          <div v-if="form.provider === 'codex'" class="mt-3 px-3 py-2 text-xs text-[#92400e] bg-[#fffbeb] border border-[#f59e0b]/30 rounded-md">
            Codex OAuth 反向代理：使用 ChatGPT 订阅的 Codex OAuth 反代可能违反 OpenAI 服务条款
          </div>
        </div>
        <div class="grid grid-cols-2 gap-4">
          <div>
            <label class="block text-xs text-[#6b7280] mb-1.5">下拉显示名称</label>
            <input v-model="form.name"
              class="w-full px-3 py-2 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none"
              placeholder="如：GPT-4o" />
            <p class="text-[10px] text-[#6b7280] mt-1">Kiro 模型下拉框里显示的名字</p>
          </div>

          <div>
            <label class="block text-xs text-[#6b7280] mb-1.5">模型标识</label>
            <input v-model="form.model_id"
              class="w-full px-3 py-2 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none"
              placeholder="如：gpt-4o" />
            <p class="text-[10px] text-[#6b7280] mt-1">Kiro 下拉列表里看到的名字，也是发给上游的 model 值</p>
          </div>

          <div v-if="form.provider !== 'codex'">
            <label class="block text-xs text-[#6b7280] mb-1.5">接口地址</label>
            <input v-model="form.base_url"
              class="w-full px-3 py-2 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none"
              placeholder="https://api.openai.com/v1" />
            <p class="text-[10px] text-[#6b7280] mt-1">只填写上游域名或基础地址，系统不会自动拼接版本路径</p>
          </div>

          <div :class="form.provider === 'codex' ? 'col-span-2' : ''">
            <label class="block text-xs text-[#6b7280] mb-1.5">{{ form.provider === 'codex' ? 'Refresh Token / Codex Auth JSON' : '访问密钥' }}</label>
            <div class="relative">
              <textarea v-if="form.provider === 'codex'" v-model="form.refresh_token"
                :rows="showKey ? 5 : 2"
                :class="showKey ? '' : 'blur-sm'"
                class="w-full px-3 py-2 pr-10 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none resize-y"
                placeholder="粘贴 Codex refresh_token，或通过下方授权链接生成 auth JSON"></textarea>
              <input v-else v-model="form.api_key"
                :type="showKey ? 'text' : 'password'"
                class="w-full px-3 py-2 pr-10 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none"
                placeholder="sk-..." />
              <button
                type="button"
                class="absolute right-2 top-2 text-[#6b7280] hover:text-[#111827]"
                @click="showKey = !showKey"
              >
                <IconEyeOff v-if="showKey" />
                <IconEye v-else />
              </button>
            </div>
            <p v-if="form.provider === 'codex'" class="text-[10px] text-[#6b7280] mt-1">可手动粘贴 refresh_token；也可生成授权链接登录，处理回调后会自动填入完整 auth JSON。</p>

            <div v-if="form.provider === 'codex'" class="mt-3 p-3 border border-[#e5e7eb] rounded-md bg-[#f9fafb] space-y-3">
              <div class="flex items-center justify-between gap-3">
                <div>
                  <div class="text-xs font-medium text-[#374151]">Codex OAuth 授权登录</div>
                  <p class="text-[10px] text-[#6b7280] mt-1">授权后浏览器会跳转到 localhost 回调地址；如果页面无法打开，复制地址栏完整 URL 到下方处理。</p>
                </div>
                <button type="button"
                  class="shrink-0 px-3 py-1.5 text-xs text-white bg-[#10b981] hover:bg-[#059669] rounded-md disabled:opacity-50 disabled:cursor-not-allowed"
                  :disabled="oauthLoading"
                  @click="handleStartOAuth">{{ oauthLoading ? '处理中...' : '生成授权链接' }}</button>
              </div>

              <div v-if="oauthAuthUrl" class="space-y-2">
                <a :href="oauthAuthUrl" target="_blank" rel="noopener noreferrer" class="inline-flex text-xs text-[#10b981] hover:underline">打开授权链接</a>
                <input :value="oauthAuthUrl" readonly
                  class="w-full px-3 py-2 text-xs bg-white border border-[#e5e7eb] rounded-md text-[#6b7280] focus:outline-none" />
              </div>

              <div class="space-y-2">
                <textarea v-model="oauthCallbackUrl" rows="2"
                  class="w-full px-3 py-2 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none resize-none"
                  placeholder="粘贴 http://localhost:1455/auth/callback?... 回调 URL"></textarea>
                <div class="flex items-center justify-between gap-3">
                  <p class="text-[10px] text-[#6b7280]">处理成功后会自动填入上方 Refresh Token / Auth JSON。</p>
                  <button type="button"
                    class="px-3 py-1.5 text-xs text-[#10b981] border border-[#10b981]/40 rounded-md hover:bg-[#ecfdf5] disabled:opacity-50 disabled:cursor-not-allowed"
                    :disabled="oauthLoading || !oauthCallbackUrl.trim()"
                    @click="handleExchangeOAuth">处理回调</button>
                </div>
              </div>

              <div v-if="oauthError" class="px-3 py-2 text-xs text-[#ef4444] bg-[#fef2f2] border border-[#ef4444]/20 rounded-md whitespace-pre-wrap">
                {{ oauthError }}
              </div>
            </div>
          </div>

          <div v-if="form.provider !== 'codex'">
            <label class="block text-xs text-[#6b7280] mb-1.5">接口端点</label>
            <select v-model="form.endpoint"
              class="w-full px-3 py-2 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none">
              <option v-for="ep in endpointOptions" :key="ep" :value="ep">{{ ep }}</option>
            </select>
            <p class="text-[10px] text-[#6b7280] mt-1">仅选择端点类型，不包含版本前缀；系统不会自动拼接 /v1</p>
          </div>

          <div>
            <label class="block text-xs text-[#6b7280] mb-1.5">代理地址</label>
            <input v-model="form.proxy_url"
              class="w-full px-3 py-2 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none"
              placeholder="留空直连，如 127.0.0.1:7890" />
            <p class="text-[10px] text-[#6b7280] mt-1">仅作用于当前模型，支持 http/https/socks5 代理</p>
          </div>

          <div>
            <label class="block text-xs text-[#6b7280] mb-1.5">推理强度</label>
            <select v-model="form.reasoning_effort"
              class="w-full px-3 py-2 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none">
              <option v-for="opt in reasoningOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
            </select>
          </div>

          <div>
            <label class="block text-xs text-[#6b7280] mb-1.5">上下文窗口</label>
            <input v-model.number="form.context_window" type="number"
              class="w-full px-3 py-2 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none"
              placeholder="0 = 默认" />
          </div>

            <div>
              <label class="block text-xs text-[#6b7280] mb-1.5">最大输出 Token</label>
              <input v-model.number="form.max_tokens" type="number"
                class="w-full px-3 py-2 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none"
                placeholder="0 = 默认" />
            </div>

            <div>
              <label class="block text-xs text-[#6b7280] mb-1.5">倍率</label>
              <input v-model.number="form.rate_multiplier" type="number" step="0.01" min="0"
                class="w-full px-3 py-2 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none"
                placeholder="1.0" />
              <p class="text-[10px] text-[#6b7280] mt-1">最终 Credits = 按价格计算结果 × 倍率</p>
            </div>

          </div>

        <div class="mt-4">
          <div class="flex items-center justify-between mb-2">
            <label class="block text-xs font-medium text-[#374151]">Credit 价格配置</label>
            <button type="button" class="text-[10px] text-[#10b981] hover:underline" @click="applyDefaultPrices(form.provider)">恢复当前 Provider 默认价格</button>
          </div>
          <div class="grid grid-cols-2 gap-4">
            <div>
              <label class="block text-xs text-[#6b7280] mb-1.5">输入价格</label>
              <input v-model.number="form.input_price" type="number" step="0.001" min="0"
                class="w-full px-3 py-2 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none"
                placeholder="5" />
              <p class="text-[10px] text-[#6b7280] mt-1">按百万输入 Token 计算，不需要填写单位</p>
            </div>
            <div>
              <label class="block text-xs text-[#6b7280] mb-1.5">输出价格</label>
              <input v-model.number="form.output_price" type="number" step="0.001" min="0"
                class="w-full px-3 py-2 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none"
                placeholder="30" />
              <p class="text-[10px] text-[#6b7280] mt-1">按百万输出 Token 计算，不需要填写单位</p>
            </div>
            <div>
              <label class="block text-xs text-[#6b7280] mb-1.5">缓存输入价格</label>
              <input v-model.number="form.cache_input_price" type="number" step="0.001" min="0"
                class="w-full px-3 py-2 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none"
                placeholder="0.5" />
              <p class="text-[10px] text-[#6b7280] mt-1">缓存命中/读取输入 Token 价格</p>
            </div>
            <div>
              <label class="block text-xs text-[#6b7280] mb-1.5">缓存输出价格</label>
              <input v-model.number="form.cache_output_price" type="number" step="0.001" min="0"
                class="w-full px-3 py-2 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none"
                placeholder="30" />
              <p class="text-[10px] text-[#6b7280] mt-1">缓存写入价格；通常只有 Claude 返回该统计</p>
            </div>
          </div>
        </div>

        <div class="mt-4 flex items-center gap-3">
          <label class="relative inline-flex items-center cursor-pointer">
            <input type="checkbox" v-model="form.enable_continuation" class="sr-only peer" />
            <div class="w-9 h-5 bg-[#e5e7eb] peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-gray-300 after:border after:rounded-full after:h-4 after:w-4 after:transition-all peer-checked:bg-[#10b981]"></div>
          </label>
          <div>
            <span class="text-xs text-[#111827]">启用续传 (previous_response_id)</span>
            <p class="text-[10px] text-[#6b7280]">仅直连 OpenAI 官方 API 时启用；使用 sub2api 等中转站时请关闭（中转站会通过 prompt_cache_key 自动管理续传）</p>
          </div>
        </div>

        <div class="mt-4">
          <label class="block text-xs text-[#6b7280] mb-1.5">下拉描述</label>
          <textarea v-model="form.note" rows="2"
            class="w-full px-3 py-2 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none resize-none"
            placeholder="Kiro 模型下拉框里的描述文字（可选）"></textarea>
        </div>

      </div>
    </div>
  </Teleport>
</template>
