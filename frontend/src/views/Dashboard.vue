<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import AdvertisementList from '../components/common/AdvertisementList.vue'
import { useStats } from '../composables/useStats.js'
import { useSmoothNumber } from '../composables/useSmoothNumber.js'
import StatsCard from '../components/dashboard/StatsCard.vue'
import ServiceStatus from '../components/dashboard/ServiceStatus.vue'
import CAStatus from '../components/dashboard/CAStatus.vue'
import ProxyConfig from '../components/dashboard/ProxyConfig.vue'
import AdvancedSettings from '../components/dashboard/AdvancedSettings.vue'
import UsageBreakdown from '../components/dashboard/UsageBreakdown.vue'
import ConfirmDialog from '../components/common/ConfirmDialog.vue'
import IconLogs from '../components/icons/IconLogs.vue'
import IconDashboard from '../components/icons/IconDashboard.vue'
import IconWifi from '../components/icons/IconWifi.vue'
import IconArrowRight from '../components/icons/IconArrowRight.vue'
import IconMore from '../components/icons/IconMore.vue'
import { useToast } from '../composables/useToast.js'
import { statsDaysAgo } from '../utils/date.js'
import { formatTokens, formatUsd } from '../utils/format.js'
import { getProxyAccessMode, updateProxyAccessMode } from '../api/settings.js'

const { stats, loading, error, clearStats, refresh } = useStats()
const { showToast } = useToast()
const usageRefreshKey = ref(0)
const clearStatsBusy = ref(false)
const showClearStatsModal = ref(false)
const clearStatsError = ref('')
const showRangeMenu = ref(false)
const showMoreMenu = ref(false)
const rangeMenuRef = ref(null)
const moreMenuRef = ref(null)
const rangeMenuPanelRef = ref(null)
const moreMenuPanelRef = ref(null)
const selectedRangeLabel = ref('今天')
const selectedRangeDays = ref(0)
const rangeSummary = ref({
  total_requests: 0,
  total_errors: 0,
  total_tokens: 0,
  input_tokens: 0,
  output_tokens: 0,
  cache_read_tokens: 0,
  cache_write_tokens: 0,
  cost_usd: 0,
})
const proxyAccess = ref({ mode: 'local', host: '127.0.0.1' })
const proxyAccessLoading = ref(false)
const proxyAccessError = ref('')
const showProxyAccessConfirm = ref(false)
const pendingProxyAccessMode = ref('')

const proxyAccessConfirmTitle = computed(() => pendingProxyAccessMode.value === 'lan' ? '切换到局域网代理？' : '切回本地代理？')
const proxyAccessConfirmMessage = computed(() => {
  const message = pendingProxyAccessMode.value === 'lan'
    ? '切换到局域网代理后，同一局域网内的其他设备可能访问你的代理端口。\n请确认当前网络可信，并确保不要暴露敏感流量。'
    : '切换回本地代理后，仅本机使用 127.0.0.1 访问代理，局域网设备将无法继续使用。'
  return proxyAccessError.value ? `${message}\n\n切换失败：${proxyAccessError.value}` : message
})

async function loadProxyAccess() {
  try {
    const data = await getProxyAccessMode()
    if (data) proxyAccess.value = data
  } catch {
    // 诊断类请求，失败时保持默认值 local/127.0.0.1，不影响主流程
  }
}

function toggleProxyAccess() {
  if (proxyAccessLoading.value) return
  proxyAccessError.value = ''
  pendingProxyAccessMode.value = proxyAccess.value.mode === 'lan' ? 'local' : 'lan'
  showProxyAccessConfirm.value = true
}

async function confirmProxyAccessToggle() {
  if (!pendingProxyAccessMode.value) return
  proxyAccessLoading.value = true
  try {
    const data = await updateProxyAccessMode(pendingProxyAccessMode.value)
    if (!data) throw new Error('切换失败')
    proxyAccess.value = data
    proxyAccessError.value = ''
    showProxyAccessConfirm.value = false
    pendingProxyAccessMode.value = ''
    showToast(data.mode === 'lan' ? '已保存为局域网代理，重启 Kiro Proxy 后生效' : '已保存为本地代理，重启 Kiro Proxy 后生效')
  } catch (e) {
    proxyAccessError.value = e.message || '切换失败'
    showToast(proxyAccessError.value, { tone: 'error' })
  } finally {
    proxyAccessLoading.value = false
  }
}

function cancelProxyAccessToggle() {
  if (proxyAccessLoading.value) return
  showProxyAccessConfirm.value = false
  pendingProxyAccessMode.value = ''
  proxyAccessError.value = ''
}

onMounted(() => {
  loadProxyAccess()
  document.addEventListener('pointerdown', handleDocumentPointerDown)
  document.addEventListener('keydown', handleDocumentKeydown)
})

onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', handleDocumentPointerDown)
  document.removeEventListener('keydown', handleDocumentKeydown)
})

const rangeOptions = [
  { label: '全部', days: -1 },
  { label: '今天', days: 0 },
  { label: '最近 7 天', days: 6 },
  { label: '最近 30 天', days: 29 },
]

const displayedStats = computed(() => selectedRangeDays.value < 0 ? stats.value : rangeSummary.value)

const cacheHitRate = computed(() => {
  const totalIn = Number(displayedStats.value.input_tokens || 0)
  const cached = Math.min(Number(displayedStats.value.cache_read_tokens || 0), totalIn)
  if (totalIn === 0) return 0
  return (cached / totalIn) * 100
})

// 核心指标平滑：轮询更新时滚动到新值；snapFirstChange 让冷启动首个真实数据直接落位，不做 0 → 值的首屏滚动。
function smoothStat(source) {
  return useSmoothNumber(source, { snapFirstChange: true })
}

const smoothTotalRequests = smoothStat(() => Number(displayedStats.value.total_requests || 0))
const smoothTotalTokens = smoothStat(() => Number(displayedStats.value.total_tokens || 0))
const smoothCacheHitRate = smoothStat(cacheHitRate)
const smoothCostUsd = smoothStat(() => Number(displayedStats.value.cost_usd || 0))

const cards = computed(() => [
  {
    label: '总请求数',
    value: Math.round(smoothTotalRequests.value).toLocaleString(),
    sub: `错误 ${displayedStats.value.total_errors || 0} · ${selectedRangeLabel.value}`,
    subTone: displayedStats.value.total_errors ? 'danger' : 'neutral',
    icon: IconLogs,
    tone: 'blue',
  },
  {
    label: '总 Token',
    value: formatTokens(Math.round(smoothTotalTokens.value)),
    sub: `输入 ${formatTokens(displayedStats.value.input_tokens)} · 输出 ${formatTokens(displayedStats.value.output_tokens)}`,
    icon: IconDashboard,
    tone: 'green',
  },
  {
    label: '缓存命中',
    value: smoothCacheHitRate.value ? smoothCacheHitRate.value.toFixed(1) + '%' : '—',
    sub: `命中 ${formatTokens(displayedStats.value.cache_read_tokens)} · 写入 ${formatTokens(displayedStats.value.cache_write_tokens)}`,
    icon: IconWifi,
    tone: 'purple',
  },
  {
    label: '费用（USD）',
    value: formatUsd(smoothCostUsd.value),
    sub: `按渠道配置单价计算 · ${selectedRangeLabel.value}`,
    icon: IconArrowRight,
    tone: 'amber',
  },
])

async function handleRefresh() {
  await refresh()
  usageRefreshKey.value++
}

function selectRange(option) {
  const daysChanged = selectedRangeDays.value !== option.days
  selectedRangeLabel.value = option.label
  selectedRangeDays.value = option.days
  if (!daysChanged) usageRefreshKey.value++
  showRangeMenu.value = false
}

function handleUsageSummary(summary) {
  rangeSummary.value = summary
}

function handleUsageRangeChange(value) {
  if (value.all) {
    selectedRangeLabel.value = '全部'
    selectedRangeDays.value = -1
    return
  }
  const matched = rangeOptions.find((option) => value.start === statsDaysAgo(option.days) && value.end === statsDaysAgo(0))
  selectedRangeLabel.value = matched?.label || '自定义'
  if (matched) selectedRangeDays.value = matched.days
}

function toggleRangeMenu() {
  showRangeMenu.value = !showRangeMenu.value
  showMoreMenu.value = false
  if (showRangeMenu.value) focusFirstMenuItem('rangeMenuPanelRef')
}

function toggleMoreMenu() {
  showMoreMenu.value = !showMoreMenu.value
  showRangeMenu.value = false
  if (showMoreMenu.value) focusFirstMenuItem('moreMenuPanelRef')
}

// 菜单打开后焦点落到第一项（WAI-ARIA menu 模式：方向键在项间循环，Home/End 跳端）
function focusFirstMenuItem(panelRefName) {
  nextTick(() => {
    const items = menuItemsOf(panelRefName)
    items[0]?.focus()
  })
}

function menuItemsOf(panelRefName) {
  const panel = panelRefs[panelRefName]?.value
  return panel ? Array.from(panel.querySelectorAll('button')) : []
}

const panelRefs = { rangeMenuPanelRef, moreMenuPanelRef }

function onMenuKeydown(event) {
  const items = Array.from(event.currentTarget.querySelectorAll('button'))
  if (!items.length) return
  const index = items.indexOf(document.activeElement)
  if (event.key === 'ArrowDown') {
    event.preventDefault()
    items[(index + 1 + items.length) % items.length]?.focus()
  } else if (event.key === 'ArrowUp') {
    event.preventDefault()
    items[(index - 1 + items.length) % items.length]?.focus()
  } else if (event.key === 'Home') {
    event.preventDefault()
    items[0]?.focus()
  } else if (event.key === 'End') {
    event.preventDefault()
    items[items.length - 1]?.focus()
  }
}

function handleDocumentPointerDown(event) {
  if (showRangeMenu.value && !rangeMenuRef.value?.contains(event.target)) showRangeMenu.value = false
  if (showMoreMenu.value && !moreMenuRef.value?.contains(event.target)) showMoreMenu.value = false
}

function handleDocumentKeydown(event) {
  if (event.key !== 'Escape') return
  showRangeMenu.value = false
  showMoreMenu.value = false
}

function handleClearStats() {
  if (loading.value || clearStatsBusy.value) return
  clearStatsError.value = ''
  showMoreMenu.value = false
  showClearStatsModal.value = true
}

function closeClearStatsModal() {
  if (clearStatsBusy.value) return
  showClearStatsModal.value = false
  clearStatsError.value = ''
}

async function confirmClearStats() {
  if (clearStatsBusy.value) return
  clearStatsBusy.value = true
  clearStatsError.value = ''
  try {
    await clearStats()
    await refresh()
    usageRefreshKey.value++
    showClearStatsModal.value = false
    showToast('统计数据已清空')
  } catch (e) {
    clearStatsError.value = e?.message || '清空统计失败，请稍后重试'
  } finally {
    clearStatsBusy.value = false
  }
}
</script>

<template>
  <div class="ui-page-shell">
    <div class="ui-page-inner space-y-3">
      <header class="flex flex-wrap items-center justify-between gap-3">
        <div class="flex items-center gap-2.5">
          <h1 class="text-[22px] font-semibold tracking-tight text-ink">仪表盘</h1>
          <span class="text-[12px] text-ink-3">Kiro Proxy 运行状态总览</span>
          <button
            type="button"
            class="inline-flex items-center gap-1.5 rounded-full border bg-surface px-2.5 py-0.5 text-[12px] text-ink-2 shadow-sm transition hover:border-primary-tint-line hover:bg-primary-tint hover:text-primary-fg disabled:cursor-not-allowed disabled:opacity-60"
            :class="proxyAccess.mode === 'lan' ? 'border-warning-tint-line text-warning hover:border-warning-tint-line-strong hover:bg-warning-tint hover:text-warning' : 'border-line'"
            :disabled="proxyAccessLoading"
            :title="proxyAccess.mode === 'lan' ? '点击切回本地代理' : '点击切换为局域网代理'"
            @click="toggleProxyAccess"
          >
            <span class="h-1.5 w-1.5 rounded-full" :class="proxyAccess.mode === 'lan' ? 'bg-warning' : 'bg-brand'"></span>
            {{ proxyAccess.mode === 'lan' ? '局域网代理' : '本地代理' }} · {{ proxyAccess.host || '127.0.0.1' }}
          </button>
        </div>

        <div class="flex items-center gap-2">
          <button
            class="inline-flex ui-control-h ui-control items-center gap-1.5 px-2.5 text-[12px] font-medium text-ink-2 shadow-sm transition hover:bg-hover active:scale-[0.98] disabled:opacity-60"
            :disabled="loading"
            @click="handleRefresh"
          >
            <svg class="h-4 w-4" :class="{ 'animate-spin': loading }" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round">
              <path d="M20 11a8.1 8.1 0 0 0-14.1-4.6L4 8.5" />
              <path d="M4 4.5v4h4" />
              <path d="M4 13a8.1 8.1 0 0 0 14.1 4.6L20 15.5" />
              <path d="M20 19.5v-4h-4" />
            </svg>
            {{ loading ? '刷新中' : '刷新' }}
          </button>

          <div ref="rangeMenuRef" class="relative">
            <button
              class="inline-flex ui-control-h ui-control items-center gap-1.5 px-2.5 text-[12px] font-medium text-ink-2 shadow-sm transition hover:bg-hover active:scale-[0.98]"
              :aria-expanded="showRangeMenu"
              aria-haspopup="menu"
              @click="toggleRangeMenu"
            >
              {{ selectedRangeLabel }}
              <svg
                class="h-3.5 w-3.5 text-ink-4 transition-transform duration-[var(--dur-fast)]"
                :class="showRangeMenu ? 'rotate-180' : ''"
                viewBox="0 0 20 20"
                fill="none"
                aria-hidden="true"
              >
                <path d="M5 7.5L10 12.5L15 7.5" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
              </svg>
            </button>
            <div v-if="showRangeMenu" ref="rangeMenuPanelRef" role="menu" class="absolute right-0 top-9 z-20 w-28 overflow-hidden rounded-lg border border-line bg-surface py-1 text-[12px] shadow-lg" @keydown="onMenuKeydown">
              <button
                v-for="option in rangeOptions"
                :key="option.label"
                role="menuitem"
                class="block w-full px-3 py-1.5 text-left focus:bg-hover focus:outline-none"
                :class="option.label === selectedRangeLabel ? 'bg-primary-tint font-semibold text-primary-fg hover:bg-primary-tint-strong' : 'text-ink-2 hover:bg-hover'"
                @click="selectRange(option)"
              >
                {{ option.label }}
              </button>
            </div>
          </div>

          <span
            class="inline-flex ui-control-h ui-control items-center gap-1.5 bg-surface px-2.5 text-[12px] font-medium shadow-sm"
            :class="error ? 'border-danger-tint-line text-danger-fg' : loading ? 'border-primary-tint-line text-primary-fg' : 'border-success-tint-line text-success-fg'"
          >
            <span class="h-1.5 w-1.5 rounded-full" :class="error ? 'bg-danger' : loading ? 'animate-pulse bg-primary' : 'bg-brand'"></span>
            {{ error ? '状态异常' : loading ? '检查中' : '运行中' }}
          </span>

          <div ref="moreMenuRef" class="relative">
            <button
              class="flex h-8 w-8 items-center justify-center ui-control bg-surface text-ink-3 shadow-sm transition hover:bg-hover active:scale-[0.98]"
              aria-label="更多操作"
              :aria-expanded="showMoreMenu"
              aria-haspopup="menu"
              @click="toggleMoreMenu"
            >
              <IconMore class="h-4 w-4" />
            </button>
            <div v-if="showMoreMenu" ref="moreMenuPanelRef" role="menu" class="absolute right-0 top-9 z-20 w-28 overflow-hidden ui-control bg-surface py-1 text-[12px] shadow-lg" @keydown="onMenuKeydown">
              <button role="menuitem" class="block w-full px-3 py-1.5 text-left text-ink-2 hover:bg-hover focus:bg-hover focus:outline-none" @click="handleRefresh">刷新数据</button>
              <button role="menuitem" class="block w-full px-3 py-1.5 text-left text-danger-fg hover:bg-danger-tint focus:bg-danger-tint focus:outline-none" @click="handleClearStats">清空统计</button>
            </div>
          </div>
        </div>
      </header>

      <section>
        <AdvertisementList />
      </section>

      <p v-if="error" class="ui-status-danger rounded-lg border border-danger-tint-line px-3 py-2 text-[12px]">统计加载失败：{{ error }}</p>
      <p v-if="proxyAccessError" class="ui-status-danger rounded-lg border border-danger-tint-line px-3 py-2 text-[12px]">代理模式切换失败：{{ proxyAccessError }}</p>

      <section class="dashboard-stats-grid grid gap-2.5">
        <StatsCard v-for="card in cards" :key="card.label" v-bind="card" />
      </section>

      <section class="grid min-w-0 grid-cols-1 gap-3 min-[1240px]:grid-cols-[minmax(0,1fr)_280px]">
        <div class="min-w-0 space-y-3">
          <div class="grid min-w-0 grid-cols-[repeat(auto-fit,minmax(280px,1fr))] gap-3">
            <ServiceStatus />
            <CAStatus />
            <ProxyConfig />
          </div>
          <UsageBreakdown
            :refresh-key="usageRefreshKey"
            :days="selectedRangeDays"
            @summary="handleUsageSummary"
            @range-change="handleUsageRangeChange"
          />
        </div>

        <aside class="min-w-0 space-y-3">
          <AdvancedSettings />

          <div class="ui-card bg-danger-tint/40 p-3">
            <div class="mb-2.5 flex items-center gap-2 text-danger">
              <svg class="h-4 w-4" viewBox="0 0 20 20" fill="none">
                <path d="M10 6v4.5m0 3h.01M17 10a7 7 0 1 1-14 0 7 7 0 0 1 14 0Z" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" />
              </svg>
              <h3 class="text-[14px] font-semibold">危险操作</h3>
            </div>
            <p class="mb-3 text-[12px] leading-4 text-danger">此操作不可逆，请谨慎操作。</p>
            <button
              class="inline-flex ui-control-h ui-control w-full items-center justify-center gap-2 border-danger-tint-line bg-surface text-[12px] font-semibold text-danger-fg transition hover:bg-danger-tint disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="loading || clearStatsBusy"
              @click="handleClearStats"
            >
              <svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round">
                <path d="M4 7h16" />
                <path d="M10 11v6M14 11v6" />
                <path d="M6 7l1 14h10l1-14" />
                <path d="M9 7V4h6v3" />
              </svg>
              {{ clearStatsBusy ? '清空中…' : '清空统计' }}
            </button>
          </div>
        </aside>
      </section>
    </div>

    <Teleport to="body">
    <Transition name="motion-fade" appear>
    <div
      v-if="showClearStatsModal"
      class="fixed inset-0 z-[var(--z-modal)] flex items-center justify-center bg-black/30 px-4 backdrop-blur-sm"
      @click.self="closeClearStatsModal"
    >
      <div class="motion-pop-panel w-full max-w-md overflow-hidden rounded-2xl bg-surface shadow-2xl ring-1 ring-black/5">
        <div class="flex items-start gap-3 border-b border-danger-tint-line bg-gradient-to-br from-warning-tint to-surface p-4">
          <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-danger-tint text-danger">
            <svg class="h-5 w-5" viewBox="0 0 20 20" fill="none" aria-hidden="true">
              <path d="M10 6v4.5m0 3h.01M17 10a7 7 0 1 1-14 0 7 7 0 0 1 14 0Z" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
          </div>
          <div class="min-w-0">
            <h2 class="text-base font-semibold text-ink">确认清空统计？</h2>
            <p class="mt-1 text-xs leading-5 text-ink-3">此操作会清空请求、Token、费用统计和用量明细。清空后历史统计无法恢复。</p>
          </div>
          <button class="ml-auto flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-ink-4 transition hover:bg-hover hover:text-ink-2" aria-label="关闭确认窗口" :disabled="clearStatsBusy" @click="closeClearStatsModal">×</button>
        </div>

        <div class="space-y-2 p-4">
          <div class="rounded-xl border border-warning-tint-line bg-warning-tint px-3 py-2 text-xs leading-5 text-warning-fg">
            代理服务不会停止，只会重置仪表盘统计数据。
          </div>
          <p v-if="clearStatsError" class="rounded-xl border border-danger-tint-line bg-danger-tint px-3 py-2 text-xs text-danger-fg">
            {{ clearStatsError }}
          </p>
        </div>

        <div class="flex justify-end gap-2 border-t border-line-2 bg-surface-2 px-4 py-3">
          <button
            class="rounded-lg border border-line bg-surface px-4 py-2 text-xs font-medium text-ink-2 transition-colors hover:bg-hover disabled:opacity-50"
            :disabled="clearStatsBusy"
            @click="closeClearStatsModal"
          >
            取消
          </button>
          <button
            class="rounded-lg bg-danger px-4 py-2 text-xs font-medium text-white shadow-sm transition-colors hover:bg-danger-hover disabled:cursor-not-allowed disabled:opacity-60"
            :disabled="clearStatsBusy"
            @click="confirmClearStats"
          >
            {{ clearStatsBusy ? '正在清空…' : '确认清空' }}
          </button>
        </div>
      </div>
    </div>
    </Transition>
  </Teleport>

  <ConfirmDialog
    :visible="showProxyAccessConfirm"
    :title="proxyAccessConfirmTitle"
    :message="proxyAccessConfirmMessage"
    :confirm-text="pendingProxyAccessMode === 'lan' ? '确认切换' : '确认切回'"
    cancel-text="取消"
    :tone="pendingProxyAccessMode === 'lan' ? 'danger' : 'primary'"
    :busy="proxyAccessLoading"
    @confirm="confirmProxyAccessToggle"
    @cancel="cancelProxyAccessToggle"
  />
  </div>
</template>

<style scoped>
.dashboard-stats-grid {
  grid-template-columns: minmax(0, 1fr);
}

@media (min-width: 640px) {
  .dashboard-stats-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (min-width: 1180px) {
  .dashboard-stats-grid {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }
}
</style>
