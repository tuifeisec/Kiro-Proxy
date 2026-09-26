<script setup>
import IconSun from '../icons/IconSun.vue'
import IconMoon from '../icons/IconMoon.vue'
import { useTheme } from '../../composables/useTheme.js'

const { isDark, toggleTheme } = useTheme()

function callDesktop(name) {
  const fn = window?.[name]
  if (typeof fn === 'function') {
    fn()
  }
}

function startDrag(event) {
  if (event.button !== 0) return
  if (event.target.closest('.titlebar-controls')) return
  callDesktop('desktopWindowStartDrag')
}
</script>

<template>
  <div class="titlebar h-9 shrink-0 flex items-center justify-between bg-surface/90 border-b border-line select-none" @mousedown="startDrag">
    <div class="titlebar-drag flex-1 h-full flex items-center gap-2 px-3">
      <div class="w-4 h-4 rounded bg-brand text-white text-[10px] leading-4 text-center font-bold">K</div>
      <span class="text-xs font-medium text-ink-2">Kiro Proxy</span>
      <span class="text-[10px] text-ink-4">后台代理运行中</span>
    </div>
    <div class="titlebar-controls h-full flex items-center pr-1 gap-1">
      <button
        class="relative w-8 h-8 flex items-center justify-center rounded-md text-ink-3 hover:bg-hover hover:text-ink transition-colors"
        :aria-label="isDark ? '切换到浅色模式' : '切换到深色模式'"
        :title="isDark ? '切换到浅色模式' : '切换到深色模式'"
        @click="toggleTheme"
      >
        <IconSun class="absolute w-4 h-4 transition-[opacity,transform] duration-[var(--dur-fast)] ease-[var(--ease-out-quart)]"
                 :class="isDark ? 'opacity-0 -rotate-90 scale-75' : 'opacity-100 rotate-0 scale-100'" />
        <IconMoon class="absolute w-4 h-4 transition-[opacity,transform] duration-[var(--dur-fast)] ease-[var(--ease-out-quart)]"
                  :class="isDark ? 'opacity-100 rotate-0 scale-100' : 'opacity-0 rotate-90 scale-75'" />
      </button>
      <button
        class="w-8 h-8 flex items-center justify-center rounded-md text-ink-3 hover:bg-hover hover:text-ink transition-colors"
        aria-label="隐藏到后台"
        title="隐藏到后台"
        @click="callDesktop('desktopWindowMinimize')"
      >
        <svg class="w-4 h-4" viewBox="0 0 16 16" aria-hidden="true">
          <path d="M3.5 8h9" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
        </svg>
      </button>
      <button
        class="w-8 h-8 flex items-center justify-center rounded-md text-ink-3 hover:bg-danger-tint hover:text-danger transition-colors"
        aria-label="关闭到后台"
        title="关闭到后台"
        @click="callDesktop('desktopWindowClose')"
      >
        <svg class="w-4 h-4" viewBox="0 0 16 16" aria-hidden="true">
          <path d="M4.5 4.5l7 7m0-7l-7 7" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
        </svg>
      </button>
    </div>
  </div>
</template>
