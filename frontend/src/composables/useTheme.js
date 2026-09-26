// 主题切换：'light' | 'dark' | 'system'（跟随系统）。
// 实际明暗由 isDark 决定，落到 <html> 的 .dark class；选择持久化到 localStorage。
// 模块级单例，init() 幂等，main.js 中调用一次。
import { computed, ref, watch } from 'vue'

const STORAGE_KEY = 'ui-theme'
const SWITCHING_CLASS = 'theme-switching'
const SWITCHING_MS = 350

const theme = ref(readStored())
const systemDark = ref(
  typeof window !== 'undefined' && window.matchMedia
    ? window.matchMedia('(prefers-color-scheme: dark)').matches
    : false
)

const isDark = computed(
  () => theme.value === 'dark' || (theme.value === 'system' && systemDark.value)
)

let initialized = false
let switchingTimer = null
let mql = null

function readStored() {
  try {
    const v = localStorage.getItem(STORAGE_KEY)
    return v === 'light' || v === 'dark' ? v : 'system'
  } catch {
    return 'system'
  }
}

function applyTheme() {
  document.documentElement.classList.toggle('dark', isDark.value)
}

// 切换瞬间启用全站颜色过渡窗口（style.css 的 .theme-switching 规则）
function animateThemeChange() {
  if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return
  const root = document.documentElement
  root.classList.add(SWITCHING_CLASS)
  clearTimeout(switchingTimer)
  switchingTimer = setTimeout(() => root.classList.remove(SWITCHING_CLASS), SWITCHING_MS)
}

function setTheme(next) {
  if (next === theme.value) return
  animateThemeChange()
  theme.value = next
}

// 在浅色/深色间切换（记录显式选择，不再跟随系统）
function toggleTheme() {
  setTheme(isDark.value ? 'light' : 'dark')
}

function init() {
  if (initialized || typeof window === 'undefined') return
  initialized = true
  mql = window.matchMedia('(prefers-color-scheme: dark)')
  const onSystemChange = (e) => { systemDark.value = e.matches }
  if (mql.addEventListener) mql.addEventListener('change', onSystemChange)
  watch(isDark, applyTheme)
  watch(theme, () => {
    try { localStorage.setItem(STORAGE_KEY, theme.value) } catch {}
  })
  applyTheme()
}

export function useTheme() {
  return { theme, isDark, setTheme, toggleTheme, init }
}
