<script setup>
import { computed } from 'vue'
import IconPlay from '../icons/IconPlay.vue'
import IconEdit from '../icons/IconEdit.vue'
import IconTrash from '../icons/IconTrash.vue'
import openaiIcon from '../../assets/providers/openai.svg'
import claudeIcon from '../../assets/providers/claue.png'
import geminiIcon from '../../assets/providers/gemini.svg'

const props = defineProps({
  model: { type: Object, required: true },
  testResult: { type: Object, default: null },
  testing: { type: Boolean, default: false },
  refreshingCodex: { type: Boolean, default: false },
  togglingEnabled: { type: Boolean, default: false },
  primary: { type: Boolean, default: false },
  groupMembership: { type: Object, default: null },
  hidePrivateInfo: { type: Boolean, default: false }
})

const emit = defineEmits(['edit', 'delete', 'test', 'refresh-codex', 'toggle-enabled'])

const PRIVATE_MASK = '******'

const displayName = computed(() => props.model.name || props.model.model_id || '未命名模型')
const provider = computed(() => props.model.provider || 'openai')
const isEnabled = computed(() => props.model.enabled !== false)

const providerMeta = computed(() => {
  const map = {
    openai: {
      label: 'OpenAI',
      image: openaiIcon,
      icon: 'bg-white border-[#d9dde5]',
      badge: 'bg-[#eef6ff] text-[#2563eb]'
    },
    anthropic: {
      label: 'Anthropic',
      image: claudeIcon,
      icon: 'bg-white border-[#ead7c8]',
      badge: 'bg-[#fff1e7] text-[#b45309]'
    },
    gemini: {
      label: 'Gemini',
      image: geminiIcon,
      icon: 'bg-white border-[#dbe5ff]',
      badge: 'bg-[#f1f5ff] text-[#4f46e5]'
    },
    codex: {
      label: 'Codex',
      mark: 'CODEX',
      icon: 'bg-[#050505] text-white border-[#050505]',
      badge: 'bg-[#ecfdf5] text-[#059669]'
    }
  }
  return map[provider.value] || map.openai
})

const maskedCredential = computed(() => {
  if (props.hidePrivateInfo) return PRIVATE_MASK
  const key = props.model.api_key || props.model.refresh_token || ''
  if (!key) return '-'
  const plain = key.replace(/\*/g, '').replace(/•/g, '')
  if (key.includes('****') || key.includes('••••')) {
    const prefix = key.slice(0, 4)
    const suffix = key.slice(-4)
    return `${prefix}••••${suffix}`
  }
  if (plain.length <= 8) return '••••••••'
  return `${key.slice(0, 4)}••••${key.slice(-4)}`
})

const privateLabel = computed(() => props.hidePrivateInfo ? PRIVATE_MASK : null)
const baseUrl = computed(() => privateLabel.value || props.model.base_url || (provider.value === 'codex' ? 'https://api.openai.com' : '-'))
const proxyLabel = computed(() => privateLabel.value || props.model.proxy_url || '直连')
const endpointLabel = computed(() => privateLabel.value || props.model.endpoint || '-')
const codexEmailLabel = computed(() => privateLabel.value || props.model.codex_email || '-')
const codexPlanLabel = computed(() => privateLabel.value || props.model.codex_plan_type || '-')
const codexAccountIdLabel = computed(() => privateLabel.value || props.model.codex_account_id || '-')

const statusMeta = computed(() => {
  if (!isEnabled.value) {
    return { label: '已禁用', sub: '未参与路由', badge: 'bg-[#f3f4f6] text-[#6b7280]', dot: 'bg-[#9ca3af]' }
  }
  if (props.testing) {
    return { label: '测试中...', sub: '正在发送请求...', badge: 'bg-[#eff6ff] text-[#2563eb]', dot: 'bg-[#3b82f6] animate-pulse' }
  }
  if (!props.testResult) {
    return { label: '未测试', sub: '等待测试', badge: 'bg-[#f8fafc] text-[#64748b]', dot: 'bg-[#94a3b8]' }
  }
  if (props.testResult.ok) {
    return {
      label: '可用',
      sub: `${Number(props.testResult.tokens_per_sec || 0).toFixed(1)} t/s  |  ${Math.round(Number(props.testResult.ttfb_ms || 0))}ms`,
      badge: 'bg-[#ecfdf5] text-[#059669]',
      dot: 'bg-[#10b981]'
    }
  }
  return { label: '连接失败', sub: props.testResult.error || '测试失败', badge: 'bg-[#fef2f2] text-[#dc2626]', dot: 'bg-[#ef4444]' }
})

const codexUsage = computed(() => {
  if (!props.model.codex_usage_json) return null
  try {
    return JSON.parse(props.model.codex_usage_json)
  } catch {
    return null
  }
})

const codexRateLimit = computed(() => codexUsage.value?.rate_limit || null)
const codexWindows = computed(() => {
  const rate = codexRateLimit.value
  if (!rate) return []
  return [
    { label: '5 小时限制', window: rate.primary_window },
    { label: '本周限制', window: rate.secondary_window }
  ].filter(item => item.window)
})

const codexRemaining = computed(() => {
  const primary = codexWindows.value[0]?.window
  if (!primary) return null
  return remainingPercent(primary)
})

const codexRingStyle = computed(() => {
  const value = codexRemaining.value ?? 0
  return { background: `conic-gradient(#34d399 ${value * 3.6}deg, #e5e7eb 0deg)` }
})

const updatedAtLabel = computed(() => {
  const value = props.model.updated_at || ''
  if (!value) return '-'
  return value.replace('T', ' ').slice(0, 19)
})

function formatPrice(value) {
  const num = Number(value || 0)
  if (!Number.isFinite(num)) return '$0'
  if (num === 0) return '$0'
  if (num < 0.001) return `$${num.toFixed(5).replace(/0+$/, '')}`
  return `$${num.toFixed(4).replace(/0+$/, '').replace(/\.$/, '')}`
}

function formatMultiplier(value) {
  return `${Number(value || 1).toFixed(2)}x`
}

function remainingPercent(row) {
  const used = Number(row?.used_percent || 0)
  return Math.max(0, Math.min(100, Math.round(100 - used)))
}

function resetText(seconds) {
  const value = Number(seconds || 0)
  if (value <= 0) return '即将重置'
  const days = Math.floor(value / 86400)
  const hours = Math.floor((value % 86400) / 3600)
  const minutes = Math.floor((value % 3600) / 60)
  if (days > 0) return `${days}天${hours}小时后`
  if (hours > 0) return `${hours}小时${minutes}分钟后`
  return `${minutes}分钟后`
}

function usedText(row) {
  const used = Number(row?.used || 0)
  const limit = Number(row?.limit || 0)
  if (!limit) return `已用 ${row?.used_percent || 0}%`
  return `${used.toLocaleString()} / ${limit.toLocaleString()}`
}
</script>

<template>
  <div class="model-row bg-white border-b border-[#edf0f5] last:border-b-0 hover:bg-[#fcfdff] transition-colors">
    <div class="grid min-w-[1153px] grid-cols-[48px_205px_400px_225px_133px_142px] min-h-[76px] items-center">
      <div class="flex justify-center px-3">
        <button
          type="button"
          class="flex h-[28px] w-[28px] items-center justify-center rounded-full border transition-all disabled:cursor-not-allowed disabled:opacity-70"
          :class="isEnabled
            ? 'border-[#bfdbfe] bg-[#eff6ff] text-[#1677ff] shadow-[0_1px_2px_rgba(22,119,255,0.16)] hover:border-[#93c5fd] hover:bg-[#dbeafe]'
            : 'border-[#e5e7eb] bg-white text-[#94a3b8] hover:border-[#cbd5e1] hover:bg-[#f8fafc] hover:text-[#64748b]'"
          :aria-label="togglingEnabled ? '切换中' : (isEnabled ? '禁用模型' : '启用模型')"
          :title="togglingEnabled ? '切换中...' : (isEnabled ? '点击禁用模型' : '点击启用模型')"
          :disabled="togglingEnabled"
          @click="emit('toggle-enabled')"
        >
          <span v-if="togglingEnabled" class="h-[13px] w-[13px] animate-spin rounded-full border-2 border-current border-t-transparent"></span>
          <svg v-else class="h-[15px] w-[15px]" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
            <path d="M12 3v8" />
            <path d="M7.05 5.85a8 8 0 1 0 9.9 0" />
          </svg>
        </button>
      </div>

      <div class="flex min-w-0 items-center gap-2.5 px-3">
        <div
          class="flex h-[40px] w-[40px] shrink-0 items-center justify-center overflow-hidden rounded-[9px] border text-[11px] font-bold shadow-[0_1px_2px_rgba(15,23,42,0.06)]"
          :class="providerMeta.icon"
        >
          <img v-if="providerMeta.image" :src="providerMeta.image" :alt="providerMeta.label" class="h-[25px] w-[25px] object-contain" />
          <span v-else class="scale-[0.72]">{{ providerMeta.mark }}</span>
        </div>
        <div class="min-w-0">
          <div class="flex min-w-0 items-center gap-1.5">
            <div class="truncate text-[14px] font-semibold leading-5 text-[#111827]">{{ displayName }}</div>
            <span v-if="primary" class="shrink-0 rounded-[4px] bg-[#eef6ff] px-1.5 py-[1px] text-[11px] font-medium text-[#2563eb]">主力</span>
          </div>
          <div class="mt-[2px] truncate font-mono text-[11px] leading-4 text-[#64748b]">{{ model.model_id || '-' }}</div>
          <div class="mt-[4px] flex min-w-0 items-center gap-1.5">
            <span class="shrink-0 inline-flex rounded-[4px] px-1.5 py-[1px] text-[11px] leading-4" :class="providerMeta.badge">{{ providerMeta.label }}</span>
          </div>
        </div>
      </div>

      <div class="grid grid-cols-[50px_minmax(165px,1fr)_54px_95px] gap-x-2 gap-y-[7px] px-2 text-[12px] leading-5">
        <span class="font-medium text-[#64748b]">HOST</span>
        <span class="truncate text-[#111827]" :title="baseUrl">{{ baseUrl }}</span>
        <span class="font-medium text-[#64748b]">API Key</span>
        <span class="truncate font-mono text-[#111827]" :title="maskedCredential">{{ maskedCredential }}</span>
        <span class="font-medium text-[#64748b]">Endpoint</span>
        <span class="truncate font-mono text-[#111827]" :title="endpointLabel">{{ endpointLabel }}</span>
        <span class="font-medium text-[#64748b]">代理</span>
        <span class="truncate font-medium" :class="!hidePrivateInfo && model.proxy_url ? 'text-[#2563eb]' : 'text-[#059669]'" :title="proxyLabel">{{ proxyLabel }}</span>
      </div>

      <div class="grid grid-cols-5 gap-0.5 px-1.5 text-center text-[12px] leading-5">
        <div>
          <div class="text-[#64748b]">输入</div>
          <div class="mt-1 font-medium tabular-nums text-[#111827]">{{ formatPrice(model.input_price) }}</div>
        </div>
        <div>
          <div class="text-[#64748b]">输出</div>
          <div class="mt-1 font-medium tabular-nums text-[#111827]">{{ formatPrice(model.output_price) }}</div>
        </div>
        <div>
          <div class="text-[#64748b]">缓存读</div>
          <div class="mt-1 font-medium tabular-nums text-[#111827]">{{ formatPrice(model.cache_input_price) }}</div>
        </div>
        <div>
          <div class="text-[#64748b]">缓存写</div>
          <div class="mt-1 font-medium tabular-nums text-[#111827]">{{ formatPrice(model.cache_output_price) }}</div>
        </div>
        <div>
          <div class="text-[#64748b]">倍率</div>
          <div class="mt-1 font-medium tabular-nums text-[#111827]">{{ formatMultiplier(model.rate_multiplier) }}</div>
        </div>
      </div>

      <div class="px-3">
        <div class="inline-flex rounded-[5px] px-2 py-[3px] text-[12px] font-semibold leading-4" :class="statusMeta.badge">{{ statusMeta.label }}</div>
        <div class="mt-[5px] flex items-center gap-1.5 text-[12px] leading-5 text-[#64748b]">
          <span class="h-[6px] w-[6px] rounded-full" :class="statusMeta.dot"></span>
          <span class="truncate" :title="statusMeta.sub">{{ statusMeta.sub }}</span>
        </div>
      </div>

      <div class="flex items-center justify-start gap-1.5 px-1 whitespace-nowrap">
        <button
          class="inline-flex h-[30px] min-w-[62px] shrink-0 items-center justify-center gap-1 ui-control bg-white px-2 text-[12px] font-medium leading-none text-[#374151] hover:border-[#bfdbfe] hover:bg-[#eff6ff] hover:text-[#2563eb] disabled:opacity-50"
          :disabled="testing || !isEnabled"
          @click="emit('test')"
        >
          <IconPlay class="h-3 w-3 shrink-0" />
          <span>{{ testing ? '测试中' : '测试' }}</span>
        </button>
        <button class="flex h-[30px] w-[30px] shrink-0 items-center justify-center ui-control bg-white text-[#64748b] hover:bg-[#f8fafc] hover:text-[#111827]" title="编辑" aria-label="编辑" @click="emit('edit')">
          <IconEdit class="h-3.5 w-3.5" />
        </button>
        <button class="flex h-[30px] w-[30px] shrink-0 items-center justify-center ui-control bg-white text-[#64748b] hover:border-[#fecaca] hover:bg-[#fef2f2] hover:text-[#ef4444]" title="删除" aria-label="删除" @click="emit('delete')">
          <IconTrash class="h-3.5 w-3.5" />
        </button>
      </div>
    </div>

    <div v-if="provider === 'codex'" class="min-w-[1153px] px-4 pb-4">
      <div class="ml-[48px] grid grid-cols-[245px_1fr_170px_165px] overflow-hidden ui-card bg-white">
        <div class="border-r border-[#edf0f5] px-4 py-4 text-[12px]">
          <div class="mb-3 font-semibold text-[#111827]">账户信息</div>
          <div class="grid grid-cols-[70px_minmax(0,1fr)] gap-y-2 leading-4">
            <span class="text-[#64748b]">邮箱</span><span class="truncate text-[#111827]">{{ codexEmailLabel }}</span>
            <span class="text-[#64748b]">计划</span><span class="truncate text-[#111827]">{{ codexPlanLabel }}</span>
            <span class="text-[#64748b]">账户 ID</span><span class="truncate font-mono text-[#111827]">{{ codexAccountIdLabel }}</span>
            <span class="text-[#64748b]">Access Token</span><span class="truncate text-[#f59e0b]">将于 {{ updatedAtLabel }} 过期</span>
          </div>
        </div>

        <div class="border-r border-[#edf0f5] px-4 py-4 text-[12px]">
          <div class="mb-3 font-semibold text-[#111827]">使用限制</div>
          <div v-if="codexWindows.length" class="space-y-3">
            <div v-for="item in codexWindows" :key="item.label">
              <div class="mb-1.5 flex items-center justify-between">
                <span class="font-medium text-[#374151]">{{ item.label }}</span>
                <span class="tabular-nums text-[#64748b]">{{ remainingPercent(item.window) }}%，{{ usedText(item.window) }}</span>
              </div>
              <div class="h-[7px] overflow-hidden rounded-full bg-[#e5e7eb]">
                <div class="h-full rounded-full bg-[#2dd4bf]" :style="{ width: `${remainingPercent(item.window)}%` }"></div>
              </div>
              <div class="mt-1 text-[11px] text-[#94a3b8]">重置时间：{{ resetText(item.window.reset_after_seconds) }}</div>
            </div>
          </div>
          <div v-else class="rounded-lg border border-dashed border-[#cbd5e1] px-3 py-3 text-[#64748b]">暂无用量信息，点击刷新信息获取。</div>
        </div>

        <div class="flex flex-col items-center justify-center border-r border-[#edf0f5] px-4 py-4 text-[12px]">
          <div class="mb-3 font-semibold text-[#111827] self-start">剩余额度</div>
          <div v-if="codexRemaining !== null" class="rounded-full p-[7px]" :style="codexRingStyle">
            <div class="flex h-[74px] w-[74px] flex-col items-center justify-center rounded-full bg-white shadow-inner">
              <span class="text-lg font-bold tabular-nums text-[#10b981]">{{ codexRemaining }}%</span>
            </div>
          </div>
          <div v-else class="flex h-[88px] w-[88px] items-center justify-center rounded-full bg-[#f3f4f6] text-[#94a3b8]">无数据</div>
          <div class="mt-2 text-[11px] text-[#64748b]">{{ codexRemaining !== null ? '基于当前 5 小时窗口' : '等待刷新用量' }}</div>
        </div>

        <div class="px-4 py-4 text-[12px]">
          <div class="mb-3 font-semibold text-[#111827]">最近刷新</div>
          <div class="mb-4 text-[#64748b]">{{ updatedAtLabel }}</div>
          <div class="mb-2 font-semibold text-[#111827]">最新状态</div>
          <div class="mb-4 flex items-center gap-1.5 text-[#059669]"><span class="h-[7px] w-[7px] rounded-full bg-[#10b981]"></span>成功</div>
          <button
            class="inline-flex h-[30px] items-center gap-1 ui-control px-3 text-[12px] font-medium text-[#2563eb] hover:bg-[#eff6ff] disabled:opacity-50"
            :disabled="refreshingCodex"
            @click="emit('refresh-codex')"
          >
            <svg class="h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M23 4v6h-6"/><path d="M1 20v-6h6"/><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10"/><path d="M20.49 15a9 9 0 0 1-14.85 3.36L1 14"/></svg>
            {{ refreshingCodex ? '刷新中...' : '刷新信息' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
