import { ref, onScopeDispose } from 'vue'

// 已复制反馈的自动复位时长
const COPIED_RESET_DELAY = 1500

// clipboard API 不可用时的兜底：隐藏 textarea + execCommand('copy')
function fallbackCopy(text) {
  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.setAttribute('readonly', '')
  textarea.style.position = 'fixed'
  textarea.style.left = '-9999px'
  textarea.style.top = '0'
  document.body.appendChild(textarea)
  textarea.select()
  const ok = document.execCommand('copy')
  document.body.removeChild(textarea)
  if (!ok) throw new Error('copy command failed')
}

// 统一的剪贴板复制：navigator.clipboard 优先，失败或不可用时降级 execCommand。
// copy(text) 成功时置 copied 为 true 并在 COPIED_RESET_DELAY 后自动复位，返回 true；
// 两种途径都失败时返回 false，由调用方决定如何提示用户（如 toast）。
export function useCopy() {
  const copied = ref(false)
  let resetTimer = null

  function markCopied() {
    copied.value = true
    clearTimeout(resetTimer)
    resetTimer = setTimeout(() => { copied.value = false }, COPIED_RESET_DELAY)
  }

  async function copy(text) {
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text)
      } else {
        fallbackCopy(text)
      }
      markCopied()
      return true
    } catch {
      try {
        fallbackCopy(text)
        markCopied()
        return true
      } catch {
        return false
      }
    }
  }

  onScopeDispose(() => clearTimeout(resetTimer))

  return { copied, copy }
}
