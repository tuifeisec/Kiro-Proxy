<script setup>
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
  <div class="titlebar h-9 shrink-0 flex items-center justify-between bg-white/90 border-b border-[#e5e7eb] select-none" @mousedown="startDrag">
    <div class="titlebar-drag flex-1 h-full flex items-center gap-2 px-3">
      <div class="w-4 h-4 rounded bg-[#10b981] text-white text-[10px] leading-4 text-center font-bold">K</div>
      <span class="text-xs font-medium text-[#374151]">Kiro Proxy</span>
      <span class="text-[10px] text-[#9ca3af]">后台代理运行中</span>
    </div>
    <div class="titlebar-controls h-full flex items-center pr-1 gap-1">
      <button
        class="w-8 h-8 flex items-center justify-center rounded-md text-[#6b7280] hover:bg-[#f3f4f6] hover:text-[#111827] transition-colors"
        title="隐藏到后台"
        @click="callDesktop('desktopWindowMinimize')"
      >
        <svg class="w-4 h-4" viewBox="0 0 16 16" aria-hidden="true">
          <path d="M3.5 8h9" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
        </svg>
      </button>
      <button
        class="w-8 h-8 flex items-center justify-center rounded-md text-[#6b7280] hover:bg-[#fee2e2] hover:text-[#dc2626] transition-colors"
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
