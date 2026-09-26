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
  blue: 'bg-primary-tint text-primary-fg border-primary-tint-line',
  green: 'bg-success-tint text-success-fg border-success-tint-line',
  purple: 'bg-accent-tint text-accent border-accent-tint-line',
  amber: 'bg-warning-tint text-warning-fg border-warning-tint-line',
}

const trendStroke = {
  blue: 'var(--ui-chart-blue)',
  green: 'var(--ui-chart-green)',
  purple: 'var(--ui-chart-purple)',
  amber: 'var(--ui-chart-amber)',
}
</script>

<template>
  <div class="stats-card ui-card group relative px-4 py-3 transition hover:shadow-[0_6px_18px_rgba(15,23,42,0.06)]">
    <div class="stats-card-layout grid h-[58px] items-center gap-3">
      <div class="flex h-8 w-8 items-center justify-center rounded-lg border" :class="toneMap[tone] || toneMap.blue">
        <component v-if="icon" :is="icon" class="h-4 w-4" />
      </div>

      <div class="min-w-0 self-stretch">
        <div class="truncate text-[11px] font-medium leading-4 text-ink-3">{{ label }}</div>
        <div class="mt-0.5 text-[22px] font-semibold leading-6 tracking-tight text-ink tabular-nums">{{ value }}</div>
        <div
          v-if="sub"
          class="mt-1 truncate text-[11px] leading-3.5"
          :class="subTone === 'danger' ? 'text-danger' : subTone === 'success' ? 'text-success' : subTone === 'warning' ? 'text-warning' : 'text-ink-3'"
        >
          {{ sub }}
        </div>
      </div>

      <div class="stats-card-trend pointer-events-none absolute right-3 top-2.5 z-0">
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

<style scoped>
.stats-card {
  container-type: inline-size;
}

.stats-card-layout {
  grid-template-columns: 34px minmax(0, 1fr);
  position: relative;
  z-index: 1;
}

.stats-card-trend {
  display: flex;
}
</style>
