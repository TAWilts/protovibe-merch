<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import type { EventTimelinePoint } from '@/api/types'
import { useMoney } from '@/composables/useMoney'

const props = defineProps<{ points: EventTimelinePoint[] }>()
const { t } = useI18n()
const { format } = useMoney()

const height = 340
const padding = { top: 48, right: 30, bottom: 70, left: 82 }
const chartWidth = computed(() => Math.max(760, props.points.length * 210))
const maximum = computed(() => Math.max(1, ...props.points.flatMap((point) => [point.income_cents, point.profit_cents])))
const minimum = computed(() => Math.min(0, ...props.points.map((point) => point.profit_cents)))
const ticks = computed(() => [...new Set([maximum.value, 0, minimum.value])])

function y(value: number) {
  const usable = height - padding.top - padding.bottom
  const range = Math.max(1, maximum.value - minimum.value)
  return padding.top + ((maximum.value - value) / range) * usable
}

const bars = computed(() => {
  const slot = (chartWidth.value - padding.left - padding.right) / Math.max(1, props.points.length)
  const width = Math.min(28, slot * 0.28)
  const zero = y(0)
  return props.points.map((point, index) => {
    const center = padding.left + slot * index + slot / 2
    const incomeY = y(point.income_cents)
    const profitY = y(point.profit_cents)
    return {
      point,
      income: {
        x: center - width - 3,
        y: point.income_cents === 0 ? zero - 1 : Math.min(zero, incomeY),
        width,
        height: point.income_cents === 0 ? 2 : Math.max(1, Math.abs(zero - incomeY)),
      },
      incomeLabel: {
        x: center - 4,
        y: point.income_cents < 0 ? incomeY + 16 : Math.max(14, incomeY - 8),
      },
      profit: {
        x: center + 3,
        y: point.profit_cents === 0 ? zero - 1 : Math.min(zero, profitY),
        width,
        height: point.profit_cents === 0 ? 2 : Math.max(1, Math.abs(zero - profitY)),
      },
      profitLabel: {
        x: center + 4,
        y: point.profit_cents < 0 ? profitY + 16 : Math.max(14, profitY - 8),
      },
      center,
      shortLabel: point.label.length > 16 ? `${point.label.slice(0, 15)}…` : point.label,
    }
  })
})
</script>

<template>
  <div v-if="points.length" class="event-timeline">
    <div class="chart-legend" aria-hidden="true">
      <span><i class="income-key"></i>{{ t('balances.income') }}</span>
      <span><i class="profit-key"></i>{{ t('balances.profit') }}</span>
    </div>
    <div class="event-chart-scroll" tabindex="0">
      <svg
        class="event-chart"
        :width="chartWidth"
        :height="height"
        :viewBox="`0 0 ${chartWidth} ${height}`"
        role="img"
        :aria-label="t('balances.eventTimelineDescription')"
      >
        <g v-for="tick in ticks" :key="tick" class="axis-tick">
          <line :class="{ 'zero-line': tick === 0 }" :x1="padding.left" :x2="chartWidth - padding.right" :y1="y(tick)" :y2="y(tick)" />
          <text :x="padding.left - 8" :y="y(tick) + 4">{{ format(tick) }}</text>
        </g>
        <g v-for="bar in bars" :key="bar.point.key" class="event-group" tabindex="0">
          <rect class="income-bar" v-bind="bar.income">
            <title>{{ `${bar.point.label}, ${bar.point.date}: ${t('balances.income')} ${format(bar.point.income_cents)}` }}</title>
          </rect>
          <text class="bar-value income-value" :x="bar.incomeLabel.x" :y="bar.incomeLabel.y">
            {{ format(bar.point.income_cents) }}
          </text>
          <rect class="profit-bar" v-bind="bar.profit">
            <title>{{ `${bar.point.label}, ${bar.point.date}: ${t('balances.profit')} ${format(bar.point.profit_cents)}` }}</title>
          </rect>
          <text class="bar-value profit-value" :x="bar.profitLabel.x" :y="bar.profitLabel.y">
            {{ format(bar.point.profit_cents) }}
          </text>
          <text class="event-label" :x="bar.center" :y="height - 36">{{ bar.shortLabel }}</text>
          <text class="date-label" :x="bar.center" :y="height - 18">{{ bar.point.date }}</text>
        </g>
      </svg>
    </div>
    <table class="visually-hidden">
      <caption>{{ t('balances.eventTimeline') }}</caption>
      <thead><tr><th>{{ t('sales.event') }}</th><th>{{ t('common.date') }}</th><th>{{ t('common.quantity') }}</th><th>{{ t('balances.income') }}</th><th>{{ t('balances.profit') }}</th></tr></thead>
      <tbody>
        <tr v-for="point in points" :key="point.key">
          <td>{{ point.label }}</td><td>{{ point.date }}</td><td>{{ point.quantity }}</td><td>{{ format(point.income_cents) }}</td><td>{{ format(point.profit_cents) }}</td>
        </tr>
      </tbody>
    </table>
  </div>
  <p v-else class="muted">{{ t('balances.noEventTimeline') }}</p>
</template>

<style scoped>
.event-timeline { display: grid; gap: 10px; }
.chart-legend { display: flex; flex-wrap: wrap; gap: 16px; color: var(--text-secondary); font-size: .84rem; }
.chart-legend span { display: inline-flex; align-items: center; gap: 6px; }
.chart-legend i { width: 12px; height: 12px; border-radius: 3px; }
.income-key, .income-bar { fill: var(--accent); background: var(--accent); }
.profit-key, .profit-bar { fill: var(--success-text); background: var(--success-text); }
.event-chart-scroll { overflow-x: auto; touch-action: pan-x pan-y; border-radius: var(--radius-control); outline: none; }
.event-chart-scroll:focus-visible { outline: 3px solid var(--focus-ring); outline-offset: 2px; }
.event-chart { display: block; max-width: none; }
.axis-tick line { stroke: var(--border-subtle); stroke-width: 1; }
.axis-tick line.zero-line { stroke: var(--border-strong); }
.axis-tick text { fill: var(--text-tertiary); font-size: 10px; text-anchor: end; }
.event-group:focus { outline: none; }
.event-group:focus rect { stroke: var(--text-primary); stroke-width: 2; }
.event-label, .date-label { fill: var(--text-secondary); text-anchor: middle; }
.event-label { font-size: 12px; font-weight: 700; }
.date-label { font-size: 10px; }
.bar-value { fill: var(--text-primary); font-size: 10px; font-weight: 700; }
.income-value { text-anchor: end; }
.profit-value { text-anchor: start; }
</style>
