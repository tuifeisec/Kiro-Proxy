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
      imgClass: 'dark:invert',
      icon: 'bg-surface border-line',
      badge: 'bg-primary-tint text-primary-fg'
    },
    anthropic: {
      label: 'Anthropic',
      image: claudeIcon,
      icon: 'bg-surface border-warning-tint-line',
      badge: 'bg-warning-tint text-warning-fg'
    },
    gemini: {
      label: 'Gemini',
      image: geminiIcon,
      icon: 'bg-surface border-primary-tint-line',
      badge: 'bg-primary-tint text-accent'
    },
    codex: {
      label: 'Codex',
      mark: 'CODEX',
      icon: 'bg-[#050505] text-white border-[#050505]',
      badge: 'bg-success-tint text-success-fg'
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
    return { label: '已禁用', sub: '未参与路由', badge: 'bg-hover text-ink-3', dot: 'bg-ink-4' }
  }
  if (props.testing) {
    return { label: '测试中...', sub: '正在发送请求...', badge: 'bg-primary-tint text-primary-fg', dot: 'bg-primary animate-pulse' }
  }
  if (!props.testResult) {
    return { label: '未测试', sub: '等待测试', badge: 'bg-surface-2 text-ink-3', dot: 'bg-ink-4' }
  }
  if (props.testResult.ok) {
    return {
      label: '可用',
      sub: `${Number(props.testResult.tokens_per_sec || 0).toFixed(1)} t/s  |  ${Math.round(Number(props.testResult.ttfb_ms || 0))}ms`,
      badge: 'bg-success-tint text-success-fg',
      dot: 'bg-brand'
    }
  }
  return { label: '连接失败', sub: props.testResult.error || '测试失败', badge: 'bg-danger-tint text-danger-fg', dot: 'bg-danger' }
})

const lastTestStatus = computed(() => {
  if (!props.testResult) return { label: '未知', text: 'text-ink-4', dot: 'bg-ink-4', title: '从未测试' }
  if (props.testResult.ok) return { label: '成功', text: 'text-success', dot: 'bg-brand', title: '最近一次连接测试成功' }
  return { label: '失败', text: 'text-danger', dot: 'bg-danger', title: props.testResult.error || '最近一次连接测试失败' }
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
  return { background: `conic-gradient(var(--ui-chart-green) ${value * 3.6}deg, var(--ui-border) 0deg)` }
})

const updatedAtLabel = computed(() => {
  const value = props.model.updated_at || ''
  if (!value) return '-'
  return value.replace('T', ' ').slice(0, 19)
})

const expiresAtLabel = computed(() => {
  const value = Number(props.model.access_token_expires_at || 0)
  if (!value) return '-'
  return new Date(value * 1000).toISOString().replace('T', ' ').slice(0, 19)
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
  <div class="model-row bg-surface border-b border-line-2 last:border-b-0 hover:bg-hover transition-colors">
    <div class="grid min-w-[1153px] grid-cols-[48px_205px_400px_225px_133px_142px] min-h-[76px] items-center">
      <div class="flex justify-center px-3">
        <button
          type="button"
          class="flex h-[28px] w-[28px] items-center justify-center rounded-full border transition-[color,background-color,border-color,box-shadow] disabled:cursor-not-allowed disabled:opacity-70"
          :class="isEnabled
            ? 'border-primary-tint-line bg-primary-tint text-primary-fg shadow-card hover:border-primary-tint-line-strong hover:bg-primary-tint-strong'
            : 'border-line bg-surface text-ink-4 hover:border-line-strong hover:bg-hover hover:text-ink-3'"
          :aria-label="togglingEnabled ? '切换中' : (isEnabled ? '禁用模型' : '启用模型')"
          :title="togglingEnabled ? '切换中...' : (isEnabled ? '点击禁用模型' : '点击启用模型')"
          :disabled="togglingEnabled"
          @click="emit('toggle-enabled')"
        >
          <span v-if="togglingEnabled" class="h-[15px] w-[15px] animate-spin rounded-full border-2 border-current border-t-transparent"></span>
          <svg v-else class="h-[15px] w-[15px]" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
            <path d="M12 3v8" />
            <path d="M7.05 5.85a8 8 0 1 0 9.9 0" />
          </svg>
        </button>
      </div>

      <div class="flex min-w-0 items-center gap-2.5 px-3">
        <div
          class="flex h-[40px] w-[40px] shrink-0 items-center justify-center overflow-hidden rounded-[9px] border text-[11px] font-bold shadow-card"
          :class="providerMeta.icon"
        >
          <img v-if="providerMeta.image" :src="providerMeta.image" :alt="providerMeta.label" class="h-[25px] w-[25px] object-contain" :class="providerMeta.imgClass" />
          <span v-else class="scale-[0.72]">{{ providerMeta.mark }}</span>
        </div>
        <div class="min-w-0">
          <div class="flex min-w-0 items-center gap-1.5">
            <div class="truncate text-[14px] font-semibold leading-5 text-ink">{{ displayName }}</div>
            <span v-if="primary" class="shrink-0 rounded-[4px] bg-primary-tint px-1.5 py-[1px] text-[11px] font-medium text-primary-fg">主力</span>
          </div>
          <div class="mt-[2px] truncate font-mono text-[11px] leading-4 text-ink-3">{{ model.model_id || '-' }}</div>
          <div class="mt-[4px] flex min-w-0 items-center gap-1.5">
            <span class="shrink-0 inline-flex rounded-[4px] px-1.5 py-[1px] text-[11px] leading-4" :class="providerMeta.badge">{{ providerMeta.label }}</span>
          </div>
        </div>
      </div>

      <div class="grid grid-cols-[50px_minmax(165px,1fr)_54px_95px] gap-x-2 gap-y-[7px] px-2 text-[12px] leading-5">
        <span class="font-medium text-ink-3">HOST</span>
        <span class="truncate text-ink" :title="baseUrl">{{ baseUrl }}</span>
        <span class="font-medium text-ink-3">API Key</span>
        <span class="truncate font-mono text-ink" :title="maskedCredential">{{ maskedCredential }}</span>
        <span class="font-medium text-ink-3">Endpoint</span>
        <span class="truncate font-mono text-ink" :title="endpointLabel">{{ endpointLabel }}</span>
        <span class="font-medium text-ink-3">代理</span>
        <span class="truncate font-medium" :class="!hidePrivateInfo && model.proxy_url ? 'text-primary-fg' : 'text-success'" :title="proxyLabel">{{ proxyLabel }}</span>
      </div>

      <div class="grid grid-cols-5 gap-0.5 px-1.5 text-center text-[12px] leading-5">
        <div>
          <div class="text-ink-3">输入</div>
          <div class="mt-1 font-medium tabular-nums text-ink">{{ formatPrice(model.input_price) }}</div>
        </div>
        <div>
          <div class="text-ink-3">输出</div>
          <div class="mt-1 font-medium tabular-nums text-ink">{{ formatPrice(model.output_price) }}</div>
        </div>
        <div>
          <div class="text-ink-3">缓存读</div>
          <div class="mt-1 font-medium tabular-nums text-ink">{{ formatPrice(model.cache_input_price) }}</div>
        </div>
        <div>
          <div class="text-ink-3">缓存写</div>
          <div class="mt-1 font-medium tabular-nums text-ink">{{ formatPrice(model.cache_output_price) }}</div>
        </div>
        <div>
          <div class="text-ink-3">倍率</div>
          <div class="mt-1 font-medium tabular-nums text-ink">{{ formatMultiplier(model.rate_multiplier) }}</div>
        </div>
      </div>

      <div class="px-3">
        <div class="inline-flex rounded-[5px] px-2 py-[3px] text-[12px] font-semibold leading-4" :class="statusMeta.badge">{{ statusMeta.label }}</div>
        <div class="mt-[5px] flex items-center gap-1.5 text-[12px] leading-5 text-ink-3">
          <span class="h-[6px] w-[6px] rounded-full" :class="statusMeta.dot"></span>
          <span class="truncate" :title="statusMeta.sub">{{ statusMeta.sub }}</span>
        </div>
      </div>

      <div class="flex items-center justify-start gap-1.5 px-1 whitespace-nowrap">
        <button
          class="inline-flex h-[30px] w-[70px] shrink-0 items-center justify-center gap-1 ui-control bg-surface px-1 text-[12px] font-medium leading-none text-ink-2 hover:border-primary-tint-line hover:bg-primary-tint hover:text-primary-fg disabled:opacity-50"
          :disabled="testing || !isEnabled"
          @click="emit('test')"
        >
          <IconPlay class="h-3 w-3 shrink-0" />
          <span>{{ testing ? '测试中' : '测试' }}</span>
        </button>
        <button class="flex h-[30px] w-[30px] shrink-0 items-center justify-center ui-control bg-surface text-ink-3 hover:bg-hover hover:text-ink" title="编辑" aria-label="编辑" @click="emit('edit')">
          <IconEdit class="h-3.5 w-3.5" />
        </button>
        <button class="flex h-[30px] w-[30px] shrink-0 items-center justify-center ui-control bg-surface text-ink-3 hover:border-danger-tint-line hover:bg-danger-tint hover:text-danger" title="删除" aria-label="删除" @click="emit('delete')">
          <IconTrash class="h-3.5 w-3.5" />
        </button>
      </div>
    </div>

    <div v-if="provider === 'codex'" class="min-w-[1153px] px-4 pb-4">
      <div class="ml-[48px] grid grid-cols-[245px_1fr_170px_165px] overflow-hidden ui-card bg-surface">
        <div class="border-r border-line-2 px-4 py-4 text-[12px]">
          <div class="mb-3 font-semibold text-ink">账户信息</div>
          <div class="grid grid-cols-[70px_minmax(0,1fr)] gap-y-2 leading-4">
            <span class="text-ink-3">邮箱</span><span class="truncate text-ink">{{ codexEmailLabel }}</span>
            <span class="text-ink-3">计划</span><span class="truncate text-ink">{{ codexPlanLabel }}</span>
            <span class="text-ink-3">账户 ID</span><span class="truncate font-mono text-ink">{{ codexAccountIdLabel }}</span>
            <span class="text-ink-3">Access Token</span><span class="truncate text-warning">将于 {{ expiresAtLabel }} 过期</span>
          </div>
        </div>

        <div class="border-r border-line-2 px-4 py-4 text-[12px]">
          <div class="mb-3 font-semibold text-ink">使用限制</div>
          <div v-if="codexWindows.length" class="space-y-3">
            <div v-for="item in codexWindows" :key="item.label">
              <div class="mb-1.5 flex items-center justify-between">
                <span class="font-medium text-ink-2">{{ item.label }}</span>
                <span class="tabular-nums text-ink-3">{{ remainingPercent(item.window) }}%，{{ usedText(item.window) }}</span>
              </div>
              <div class="h-[7px] overflow-hidden rounded-full bg-hover-strong">
                <div class="h-full rounded-full bg-brand" :style="{ width: `${remainingPercent(item.window)}%` }"></div>
              </div>
              <div class="mt-1 text-[11px] text-ink-4">重置时间：{{ resetText(item.window.reset_after_seconds) }}</div>
            </div>
          </div>
          <div v-else class="rounded-lg border border-dashed border-line px-3 py-3 text-ink-3">暂无用量信息，点击刷新信息获取。</div>
        </div>

        <div class="flex flex-col items-center justify-center border-r border-line-2 px-4 py-4 text-[12px]">
          <div class="mb-3 font-semibold text-ink self-start">剩余额度</div>
          <div v-if="codexRemaining !== null" class="rounded-full p-[7px]" :style="codexRingStyle">
            <div class="flex h-[74px] w-[74px] flex-col items-center justify-center rounded-full bg-surface shadow-inner">
              <span class="text-lg font-bold tabular-nums text-brand">{{ codexRemaining }}%</span>
            </div>
          </div>
          <div v-else class="flex h-[88px] w-[88px] items-center justify-center rounded-full bg-hover text-ink-4">无数据</div>
          <div class="mt-2 text-[11px] text-ink-3">{{ codexRemaining !== null ? '基于当前 5 小时窗口' : '等待刷新用量' }}</div>
        </div>

        <div class="px-4 py-4 text-[12px]">
          <div class="mb-3 font-semibold text-ink">最近刷新</div>
          <div class="mb-4 text-ink-3">{{ updatedAtLabel }}</div>
          <div class="mb-2 font-semibold text-ink">最新状态</div>
          <div class="mb-4 flex items-center gap-1.5" :class="lastTestStatus.text" :title="lastTestStatus.title"><span class="h-[7px] w-[7px] rounded-full" :class="lastTestStatus.dot"></span>{{ lastTestStatus.label }}</div>
          <button
            class="inline-flex h-[30px] items-center gap-1 ui-control px-3 text-[12px] font-medium text-primary-fg hover:bg-primary-tint disabled:opacity-50"
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
