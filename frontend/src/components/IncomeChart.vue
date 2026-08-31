<script setup lang="ts">
import { computed } from 'vue'

import type { DailyIncome } from '@/api/types'
import { useMoney } from '@/composables/useMoney'

/**
 * The income chart, ported from the hand-rolled SVG in _old/static/balances.js.
 *
 * It stays a plain inline SVG rather than a charting library: the shape is a
 * simple bar-per-day, and a dependency here would outweigh the whole page.
 */
const props = defineProps<{ points: DailyIncome[] }>()
const { format } = useMoney()

const width = 900
const height = 220
const padding = { top: 16, right: 12, bottom: 28, left: 12 }

const maximum = computed(() =>
  Math.max(1, ...props.points.map((point) => point.income_cents)),
)

const bars = computed(() => {
  const count = props.points.length
  if (!count) return []

  const usableWidth = width - padding.left - padding.right
  const usableHeight = height - padding.top - padding.bottom
  const slot = usableWidth / count
  const barWidth = Math.max(2, Math.min(38, slot * 0.62))

  return props.points.map((point, index) => {
    const barHeight = (point.income_cents / maximum.value) * usableHeight
    return {
      key: point.date,
      x: padding.left + slot * index + (slot - barWidth) / 2,
      y: padding.top + usableHeight - barHeight,
      width: barWidth,
      height: Math.max(1, barHeight),
      label: point.date.slice(5),
      title: `${point.date}: ${format(point.income_cents)}`,
      // Only every few dates get a label, otherwise a long season overlaps.
      showLabel: count <= 12 || index % Math.ceil(count / 12) === 0,
    }
  })
})
</script>

<template>
  <svg
    v-if="bars.length"
    class="income-chart"
    :viewBox="`0 0 ${width} ${height}`"
    role="img"
    preserveAspectRatio="none"
  >
    <g v-for="bar in bars" :key="bar.key">
      <rect :x="bar.x" :y="bar.y" :width="bar.width" :height="bar.height" rx="3">
        <title>{{ bar.title }}</title>
      </rect>
      <text v-if="bar.showLabel" :x="bar.x + bar.width / 2" :y="height - 8">
        {{ bar.label }}
      </text>
    </g>
  </svg>
  <p v-else class="muted">—</p>
</template>

<style scoped>
.income-chart {
  width: 100%;
  height: 220px;
}

.income-chart rect {
  fill: var(--accent);
}

.income-chart text {
  fill: var(--muted);
  font-size: 11px;
  text-anchor: middle;
}
</style>
