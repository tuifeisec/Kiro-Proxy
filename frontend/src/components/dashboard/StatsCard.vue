<script setup>
const props = defineProps({
  label: { type: String, required: true },
  value: { type: [String, Number], default: '—' },
  sub: { type: String, default: '' },
  subTone: { type: String, default: 'neutral' },
  color: { type: String, default: '#2563eb' },
  icon: { type: [Object, null], default: null },
  tone: { type: String, default: 'blue' },
})

const toneMap = {
  blue: 'bg-[#eff6ff] text-[#2563eb] border-[#bfdbfe]',
  green: 'bg-[#ecfdf5] text-[#059669] border-[#a7f3d0]',
  purple: 'bg-[#f5f3ff] text-[#7c3aed] border-[#ddd6fe]',
  amber: 'bg-[#fff7ed] text-[#d97706] border-[#fed7aa]',
}

const trendStroke = {
  blue: '#60a5fa',
  green: '#34d399',
  purple: '#a78bfa',
  amber: '#fbbf24',
}
</script>

<template>
  <div class="group overflow-hidden rounded-xl border border-[#edf0f5] bg-white px-4 py-3 shadow-[0_1px_2px_rgba(15,23,42,0.035)] transition hover:border-[#dbe3ef] hover:shadow-[0_6px_18px_rgba(15,23,42,0.06)]">
    <div class="grid h-[58px] grid-cols-[34px_minmax(0,1fr)_58px] items-center gap-3">
      <div class="flex h-8 w-8 items-center justify-center rounded-lg border" :class="toneMap[tone] || toneMap.blue">
        <component v-if="icon" :is="icon" class="h-4 w-4" />
      </div>

      <div class="min-w-0 self-stretch">
        <div class="truncate text-[11px] font-medium leading-4 text-[#64748b]">{{ label }}</div>
        <div class="mt-0.5 text-[22px] font-semibold leading-6 tracking-tight text-[#0f172a] tabular-nums">{{ value }}</div>
        <div
          v-if="sub"
          class="mt-1 truncate text-[11px] leading-3.5"
          :class="subTone === 'danger' ? 'text-[#dc2626]' : subTone === 'success' ? 'text-[#059669]' : subTone === 'warning' ? 'text-[#d97706]' : 'text-[#64748b]'"
        >
          {{ sub }}
        </div>
      </div>

      <div class="flex h-full items-end justify-end pb-1">
        <svg class="h-7 w-[58px] opacity-90" viewBox="0 0 96 40" fill="none" aria-hidden="true">
          <rect x="5" y="29" width="7" height="8" rx="3.5" :fill="trendStroke[tone] || trendStroke.blue" opacity="0.16" />
          <rect x="18" y="24" width="7" height="13" rx="3.5" :fill="trendStroke[tone] || trendStroke.blue" opacity="0.24" />
          <rect x="31" y="27" width="7" height="10" rx="3.5" :fill="trendStroke[tone] || trendStroke.blue" opacity="0.2" />
          <rect x="44" y="18" width="7" height="19" rx="3.5" :fill="trendStroke[tone] || trendStroke.blue" opacity="0.34" />
          <rect x="57" y="13" width="7" height="24" rx="3.5" :fill="trendStroke[tone] || trendStroke.blue" opacity="0.5" />
          <rect x="70" y="9" width="7" height="28" rx="3.5" :fill="trendStroke[tone] || trendStroke.blue" opacity="0.68" />
          <rect x="83" y="5" width="7" height="32" rx="3.5" :fill="trendStroke[tone] || trendStroke.blue" opacity="0.9" />
        </svg>
      </div>
    </div>
  </div>
</template>
