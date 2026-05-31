<script setup>
import { ref, watch, computed } from 'vue'
import IconEye from '../icons/IconEye.vue'
import IconEyeOff from '../icons/IconEyeOff.vue'

const props = defineProps({
  model: { type: Object, default: null },
  visible: { type: Boolean, default: false }
})

const emit = defineEmits(['save', 'cancel'])

const defaultForm = () => ({
  provider: 'openai',
  name: '',
  model_id: '',
  api_key: '',
  base_url: '',
  endpoint: '/chat/completions',
  context_window: 0,
  max_tokens: 0,
  reasoning_effort: '',
  rate_multiplier: 1.0,
  enable_continuation: false,
  note: ''
})

const form = ref(defaultForm())
const showKey = ref(false)

watch(() => props.visible, (val) => {
  if (val) {
    if (props.model) {
      form.value = { ...defaultForm(), ...props.model }
    } else {
      form.value = defaultForm()
    }
    showKey.value = false
  }
})

watch(() => form.value.provider, (val) => {
  const options = val === 'anthropic'
    ? ['/messages']
    : val === 'gemini'
      ? ['/v1beta/models/{model}:streamGenerateContent']
      : ['/chat/completions', '/responses']
  if (!options.includes(form.value.endpoint)) {
    form.value.endpoint = options[0]
  }
})

const endpointOptions = computed(() => {
  if (form.value.provider === 'anthropic') {
    return ['/messages']
  }
  if (form.value.provider === 'gemini') {
    return ['/v1beta/models/{model}:streamGenerateContent']
  }
  return ['/chat/completions', '/responses']
})

const reasoningOptions = [
  { value: '', label: '自动' },
  { value: 'low', label: '低' },
  { value: 'medium', label: '中' },
  { value: 'high', label: '高' }
]

function handleSave() {
  emit('save', { ...form.value })
}
</script>

<template>
  <Teleport to="body">
    <div v-if="visible" class="fixed inset-0 z-50 flex items-center justify-center">
      <div class="absolute inset-0 bg-black/30" @click="emit('cancel')"></div>

      <div class="relative bg-white border border-[#e5e7eb] rounded-xl w-full max-w-2xl mx-4 p-6 shadow-xl max-h-[90vh] overflow-y-auto">
        <h2 class="text-base font-semibold text-[#111827] mb-5">
          {{ model ? '编辑模型' : '新增模型' }}
        </h2>

        <!-- Provider 切换 -->
        <div class="mb-5">
          <label class="block text-xs text-[#6b7280] mb-1.5">Provider</label>
          <div class="flex gap-2">
            <button
              class="px-4 py-1.5 text-xs rounded-md border transition-colors"
              :class="form.provider === 'openai'
                ? 'bg-[#ecfdf5] border-[#10b981] text-[#10b981]'
                : 'border-[#e5e7eb] text-[#6b7280] hover:text-[#111827]'"
              @click="form.provider = 'openai'"
            >OpenAI</button>
            <button
              class="px-4 py-1.5 text-xs rounded-md border transition-colors"
              :class="form.provider === 'anthropic'
                ? 'bg-[#ecfdf5] border-[#10b981] text-[#10b981]'
                : 'border-[#e5e7eb] text-[#6b7280] hover:text-[#111827]'"
              @click="form.provider = 'anthropic'"
            >Anthropic</button>
            <button
              class="px-4 py-1.5 text-xs rounded-md border transition-colors"
              :class="form.provider === 'gemini'
                ? 'bg-[#ecfdf5] border-[#10b981] text-[#10b981]'
                : 'border-[#e5e7eb] text-[#6b7280] hover:text-[#111827]'"
              @click="form.provider = 'gemini'"
            >Gemini</button>
          </div>
        </div>

        <!-- 两列表单 -->
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

          <div>
            <label class="block text-xs text-[#6b7280] mb-1.5">接口地址</label>
            <input v-model="form.base_url"
              class="w-full px-3 py-2 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none"
              placeholder="https://api.openai.com/v1" />
            <p class="text-[10px] text-[#6b7280] mt-1">只填写上游域名或基础地址，系统不会自动拼接版本路径</p>
          </div>

          <div>
            <label class="block text-xs text-[#6b7280] mb-1.5">访问密钥</label>
            <div class="relative">
              <input v-model="form.api_key"
                :type="showKey ? 'text' : 'password'"
                class="w-full px-3 py-2 pr-10 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none"
                placeholder="sk-..." />
              <button
                class="absolute right-2 top-1/2 -translate-y-1/2 text-[#6b7280] hover:text-[#111827]"
                @click="showKey = !showKey"
              >
                <IconEyeOff v-if="showKey" />
                <IconEye v-else />
              </button>
            </div>
          </div>

          <div>
            <label class="block text-xs text-[#6b7280] mb-1.5">接口端点</label>
            <select v-model="form.endpoint"
              class="w-full px-3 py-2 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none">
              <option v-for="ep in endpointOptions" :key="ep" :value="ep">{{ ep }}</option>
            </select>
            <p class="text-[10px] text-[#6b7280] mt-1">仅选择端点类型，不包含版本前缀；系统不会自动拼接 /v1</p>
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
            <label class="block text-xs text-[#6b7280] mb-1.5">倍率 (rateMultiplier)</label>
            <input v-model.number="form.rate_multiplier" type="number" step="0.1" min="0"
              class="w-full px-3 py-2 text-sm bg-white border border-[#e5e7eb] rounded-md text-[#111827] focus:border-[#10b981] focus:outline-none"
              placeholder="1.0" />
            <p class="text-[10px] text-[#6b7280] mt-1">Credit 消耗倍率；Opus 类建议 2.2，Sonnet 类建议 1.3，小模型建议 0.25~0.5</p>
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

        <div class="flex justify-end gap-3 mt-6">
          <button class="px-4 py-2 text-sm text-[#6b7280] hover:text-[#111827] border border-[#e5e7eb] rounded-md"
            @click="emit('cancel')">取消</button>
          <button class="px-4 py-2 text-sm text-white bg-[#10b981] hover:bg-[#059669] rounded-md font-medium"
            @click="handleSave">保存</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
