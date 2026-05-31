<script setup>
import { computed, onMounted, ref } from 'vue'
import { useDailyStats } from '../../composables/useStats.js'

const { rows, loading, error, range, load } = useDailyStats()

// 默认查询最近 7 天
function daysAgo(n) {
  const d = new Date()
  d.setDate(d.getDate() - n)
  return d.toISOString().slice(0, 10)
}

const startDate = ref(daysAgo(7))
const endDate = ref(daysAgo(0))

async function refresh() {
  await load(startDate.value, endDate.value)
}

onMounted(refresh)

function formatNum(n) {
  if (!n) return '0'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return String(n)
}

function statNum(row, ...keys) {
  for (const key of keys) {
    const value = Number(row?.[key] ?? 0)
    if (value) return value
  }
  return 0
}

function cacheHitTokens(row) {
  return statNum(row, 'cache_read_tokens', 'cache_read')
}

function cacheInputTokens(row) {
  return cacheHitTokens(row) + statNum(row, 'cache_write_tokens', 'cache_write')
}

// 按日期分组：{ date: [rows...] }
const grouped = computed(() => {
  const map = new Map()
  for (const r of rows.value) {
    if (!map.has(r.date)) map.set(r.date, [])
    map.get(r.date).push(r)
  }
  return map
})

// 按模型聚合（跨日期）
const byModel = computed(() => {
  const map = new Map()
  for (const r of rows.value) {
    const cur = map.get(r.model_id) || {
      model_id: r.model_id,
      requests: 0,
      errors: 0,
      input_tokens: 0,
      output_tokens: 0,
      cache_input_tokens: 0,
      cache_hit_tokens: 0,
    }
    cur.requests += r.requests
    cur.errors += r.errors
    cur.input_tokens += r.input_tokens
    cur.output_tokens += r.output_tokens
    cur.cache_input_tokens += cacheInputTokens(r)
    cur.cache_hit_tokens += cacheHitTokens(r)
    map.set(r.model_id, cur)
  }
  return Array.from(map.values()).sort((a, b) =>
    (b.input_tokens + b.output_tokens) - (a.input_tokens + a.output_tokens)
  )
})

// 缓存命中率
function cacheHitRate(row) {
  const totalIn = row.input_tokens || 0
  const cached = row.cache_hit_tokens ?? cacheHitTokens(row)
  if (totalIn === 0) return 0
  return ((cached / totalIn) * 100).toFixed(1)
}
</script>

<template>
  <div class="bg-white border border-[#e5e7eb] rounded-lg p-4 lg:col-span-3">
    <div class="flex items-center justify-between mb-3">
      <div>
        <h3 class="text-sm font-semibold text-[#111827]">用量明细</h3>
        <p class="text-xs text-[#6b7280] mt-0.5">按日期 + 模型分组的 token 用量与缓存命中</p>
      </div>
      <div class="flex items-center gap-2">
        <input
          v-model="startDate"
          type="date"
          class="text-xs px-2 py-1 border border-[#e5e7eb] rounded bg-white"
        />
        <span class="text-xs text-[#6b7280]">至</span>
        <input
          v-model="endDate"
          type="date"
          class="text-xs px-2 py-1 border border-[#e5e7eb] rounded bg-white"
        />
        <button
          @click="refresh"
          class="text-xs px-3 py-1 bg-[#111827] text-white rounded hover:bg-[#1f2937]"
          :disabled="loading"
        >
          {{ loading ? '加载中…' : '刷新' }}
        </button>
      </div>
    </div>

    <div v-if="error" class="text-xs text-[#dc2626] mb-2">{{ error }}</div>

    <div v-if="rows.length === 0 && !loading" class="text-xs text-[#6b7280] py-6 text-center">
      该时间段无数据
    </div>

    <div v-else class="space-y-4">
      <!-- 按模型聚合表 -->
      <div>
        <div class="text-xs font-semibold text-[#374151] mb-2">按模型聚合（{{ range.start }} ~ {{ range.end }}）</div>
        <div class="overflow-x-auto">
          <table class="w-full text-xs">
            <thead>
              <tr class="text-left text-[#6b7280] border-b border-[#e5e7eb]">
                <th class="py-2 pr-3 font-medium">模型</th>
                <th class="py-2 pr-3 font-medium text-right">请求数</th>
                <th class="py-2 pr-3 font-medium text-right">错误</th>
                <th class="py-2 pr-3 font-medium text-right">输入 Token</th>
                <th class="py-2 pr-3 font-medium text-right">输出 Token</th>
                <th class="py-2 pr-3 font-medium text-right">缓存输入</th>
                <th class="py-2 font-medium text-right">缓存命中率</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="m in byModel" :key="m.model_id" class="border-b border-[#f3f4f6] hover:bg-[#fafafa]">
                <td class="py-2 pr-3 font-medium text-[#111827]">{{ m.model_id }}</td>
                <td class="py-2 pr-3 text-right tabular-nums">{{ m.requests }}</td>
                <td class="py-2 pr-3 text-right tabular-nums" :class="m.errors > 0 ? 'text-[#dc2626]' : 'text-[#6b7280]'">{{ m.errors }}</td>
                <td class="py-2 pr-3 text-right tabular-nums">{{ formatNum(m.input_tokens) }}</td>
                <td class="py-2 pr-3 text-right tabular-nums">{{ formatNum(m.output_tokens) }}</td>
                <td class="py-2 pr-3 text-right tabular-nums text-[#10b981]">{{ formatNum(m.cache_input_tokens) }}</td>
                <td class="py-2 text-right tabular-nums">
                  <span :class="cacheHitRate(m) > 30 ? 'text-[#10b981]' : 'text-[#6b7280]'">
                    {{ cacheHitRate(m) }}%
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- 按日期 + 模型详情 -->
      <div>
        <div class="text-xs font-semibold text-[#374151] mb-2">每日明细</div>
        <div class="space-y-2">
          <div v-for="[date, items] in grouped" :key="date" class="border border-[#f3f4f6] rounded">
            <div class="px-3 py-1.5 bg-[#fafafa] text-xs font-medium text-[#374151] border-b border-[#f3f4f6]">
              {{ date }}
            </div>
            <table class="w-full text-xs">
              <tbody>
                <tr v-for="r in items" :key="r.model_id" class="border-b border-[#f3f4f6] last:border-0">
                  <td class="py-1.5 px-3 text-[#111827]">{{ r.model_id }}</td>
                  <td class="py-1.5 px-3 text-right text-[#6b7280] tabular-nums">{{ r.requests }} 请求</td>
                  <td class="py-1.5 px-3 text-right tabular-nums">
                    <span class="text-[#6b7280]">in </span>{{ formatNum(r.input_tokens) }}
                  </td>
                  <td class="py-1.5 px-3 text-right tabular-nums">
                    <span class="text-[#6b7280]">out </span>{{ formatNum(r.output_tokens) }}
                  </td>
                  <td class="py-1.5 px-3 text-right tabular-nums text-[#10b981]">
                    <span class="text-[#6b7280]">cache in </span>{{ formatNum(cacheInputTokens(r)) }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
