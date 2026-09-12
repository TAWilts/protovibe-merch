<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import type { DailyIncome } from '@/api/types'
import { useMoney } from '@/composables/useMoney'

const props = defineProps<{ points: DailyIncome[] }>()
const { t } = useI18n()
const { format } = useMoney()

const height = 260
const padding = { top: 46, right: 24, bottom: 38, left: 24 }
const chartWidth = computed(() => Math.max(900, props.points.length * 112))

const maximum = computed(() =>
  Math.max(1, ...props.points.map((point) => point.income_cents)),
)

const bars = computed(() => {
  const count = props.points.length
  if (!count) return []

  const usableWidth = chartWidth.value - padding.left - padding.right
  const usableHeight = height - padding.top - padding.bottom
  const slot = usableWidth / count
  const barWidth = Math.max(2, Math.min(38, slot * 0.62))

  return props.points.map((point, index) => {
    const barHeight = (point.income_cents / maximum.value) * usableHeight
    return {
      key: point.date,
      value: point.income_cents,
      x: padding.left + slot * index + (slot - barWidth) / 2,
      y: point.income_cents === 0
        ? padding.top + usableHeight - 1
        : padding.top + usableHeight - barHeight,
      width: barWidth,
      height: point.income_cents === 0 ? 2 : Math.max(1, barHeight),
      valueY: Math.max(14, padding.top + usableHeight - barHeight - 8),
      label: point.date.slice(5),
      title: `${point.date}: ${format(point.income_cents)}`,
    }
  })
})
</script>

<template>
  <div v-if="bars.length" class="income-chart-wrap">
    <div class="income-chart-scroll" tabindex="0">
      <svg
        class="income-chart"
        :width="chartWidth"
        :height="height"
        :viewBox="`0 0 ${chartWidth} ${height}`"
        role="img"
        :aria-label="t('balances.incomeChartDescription')"
      >
        <line
          class="zero-line"
          :x1="padding.left"
          :x2="chartWidth - padding.right"
          :y1="height - padding.bottom"
          :y2="height - padding.bottom"
        />
        <g v-for="bar in bars" :key="bar.key">
          <rect :x="bar.x" :y="bar.y" :width="bar.width" :height="bar.height" rx="3">
            <title>{{ bar.title }}</title>
          </rect>
          <text class="bar-value" :x="bar.x + bar.width / 2" :y="bar.valueY">
            {{ format(bar.value) }}
          </text>
          <text class="date-label" :x="bar.x + bar.width / 2" :y="height - 12">
            {{ bar.label }}
          </text>
        </g>
      </svg>
    </div>
    <table class="visually-hidden">
      <caption>{{ t('balances.incomeChart') }}</caption>
      <thead><tr><th>{{ t('common.date') }}</th><th>{{ t('balances.income') }}</th></tr></thead>
      <tbody>
        <tr v-for="point in points" :key="point.date">
          <td>{{ point.date }}</td><td>{{ format(point.income_cents) }}</td>
        </tr>
      </tbody>
    </table>
  </div>
  <p v-else class="muted">—</p>
</template>

<style scoped>
.income-chart-wrap { display: grid; }
.income-chart-scroll {
  overflow-x: auto;
  touch-action: pan-x pan-y;
  border-radius: var(--radius-control);
  outline: none;
}
.income-chart-scroll:focus-visible { outline: 3px solid var(--focus-ring); outline-offset: 2px; }
.income-chart { display: block; max-width: none; }
.income-chart .zero-line { stroke: var(--border-strong); stroke-width: 1px; }
.income-chart rect { fill: var(--accent); }
.income-chart text { fill: var(--muted); font-size: 11px; text-anchor: middle; }
.income-chart .bar-value { fill: var(--text-primary); font-size: 10px; font-weight: 700; }
</style>
