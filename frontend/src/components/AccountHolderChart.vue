<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import type { AccountHolderTotal } from '@/api/types'
import { useMoney } from '@/composables/useMoney'

const props = defineProps<{ points: AccountHolderTotal[] }>()
const { t } = useI18n()
const { format } = useMoney()

const height = 330
const padding = { top: 48, right: 24, bottom: 56, left: 76 }
const chartWidth = computed(() => Math.max(780, props.points.length * 250))
const maximum = computed(() => Math.max(
  1,
  ...props.points.flatMap((point) => [point.income_cents, point.expense_cents, point.difference_cents]),
))
const minimum = computed(() => Math.min(0, ...props.points.map((point) => point.difference_cents)))
const ticks = computed(() => [...new Set([maximum.value, 0, minimum.value])])

function y(value: number) {
  const usableHeight = height - padding.top - padding.bottom
  const range = Math.max(1, maximum.value - minimum.value)
  return padding.top + ((maximum.value - value) / range) * usableHeight
}

function accountHolderLabel(point: AccountHolderTotal) {
  return point.account_holder_username || t('bandFinances.bandCash')
}

const groups = computed(() => {
  const usableWidth = chartWidth.value - padding.left - padding.right
  const slot = usableWidth / Math.max(1, props.points.length)
  const barWidth = Math.min(48, slot * 0.18)
  const gap = Math.min(16, slot * 0.06)
  const zero = y(0)

  return props.points.map((point, index) => {
    const center = padding.left + slot * index + slot / 2
    const values = [
      { key: 'income', value: point.income_cents, className: 'income-bar' },
      { key: 'expense', value: point.expense_cents, className: 'expense-bar' },
      { key: 'difference', value: point.difference_cents, className: 'difference-bar' },
    ]
    const start = center - (barWidth * 3 + gap * 2) / 2
    return {
      point,
      center,
      label: accountHolderLabel(point),
      bars: values.map((series, seriesIndex) => {
        const valueY = y(series.value)
        return {
          ...series,
          x: start + seriesIndex * (barWidth + gap),
          y: series.value === 0 ? zero - 1 : Math.min(zero, valueY),
          width: barWidth,
          height: series.value === 0 ? 2 : Math.max(1, Math.abs(zero - valueY)),
          labelY: series.value < 0 ? Math.min(height - padding.bottom + 18, valueY + 15) : Math.max(13, valueY - 8),
        }
      }),
    }
  })
})
</script>

<template>
  <div v-if="points.length" class="account-holder-chart-wrap">
    <div class="chart-legend" aria-hidden="true">
      <span><i class="income-key"></i>{{ t('bandFinances.income') }}</span>
      <span><i class="expense-key"></i>{{ t('bandFinances.expense') }}</span>
      <span><i class="difference-key"></i>{{ t('balances.privateContribution') }}</span>
    </div>
    <div class="account-holder-chart-scroll" tabindex="0">
      <svg
        class="account-holder-chart"
        :width="chartWidth"
        :height="height"
        :viewBox="`0 0 ${chartWidth} ${height}`"
        role="img"
        :aria-label="t('balances.accountHolderChartDescription')"
      >
        <g v-for="tick in ticks" :key="tick" class="axis-tick">
          <line
            :class="{ 'zero-line': tick === 0 }"
            :x1="padding.left"
            :x2="chartWidth - padding.right"
            :y1="y(tick)"
            :y2="y(tick)"
          />
          <text :x="padding.left - 8" :y="y(tick) + 4">{{ format(tick) }}</text>
        </g>
        <g v-for="group in groups" :key="group.point.account_holder_user_id ?? 'band'" class="holder-group" tabindex="0">
          <g v-for="bar in group.bars" :key="bar.key">
            <rect :class="bar.className" :x="bar.x" :y="bar.y" :width="bar.width" :height="bar.height" rx="3">
              <title>{{ `${group.label}, ${format(bar.value)}` }}</title>
            </rect>
            <text class="bar-value" :x="bar.x + bar.width / 2" :y="bar.labelY">
              {{ format(bar.value) }}
            </text>
          </g>
          <text class="holder-label" :x="group.center" :y="height - 20">{{ group.label }}</text>
        </g>
      </svg>
    </div>
    <table class="visually-hidden">
      <caption>{{ t('balances.accountHolderChart') }}</caption>
      <thead>
        <tr>
          <th>{{ t('bandFinances.accountHolder') }}</th>
          <th>{{ t('bandFinances.income') }}</th>
          <th>{{ t('bandFinances.expense') }}</th>
          <th>{{ t('balances.privateContribution') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="point in points" :key="point.account_holder_user_id ?? 'band'">
          <td>{{ accountHolderLabel(point) }}</td>
          <td>{{ format(point.income_cents) }}</td>
          <td>{{ format(point.expense_cents) }}</td>
          <td>{{ format(point.difference_cents) }}</td>
        </tr>
      </tbody>
    </table>
  </div>
  <p v-else class="muted">{{ t('balances.noAccountHolderTotals') }}</p>
</template>

<style scoped>
.account-holder-chart-wrap { display: grid; gap: 10px; }
.chart-legend { display: flex; flex-wrap: wrap; gap: 16px; color: var(--text-secondary); font-size: .84rem; }
.chart-legend span { display: inline-flex; align-items: center; gap: 6px; }
.chart-legend i { width: 12px; height: 12px; border-radius: 3px; }
.income-key, .income-bar { fill: var(--success-text); background: var(--success-text); }
.expense-key, .expense-bar { fill: var(--danger); background: var(--danger); }
.difference-key, .difference-bar { fill: var(--accent); background: var(--accent); }
.account-holder-chart-scroll { overflow-x: auto; touch-action: pan-x pan-y; border-radius: var(--radius-control); outline: none; }
.account-holder-chart-scroll:focus-visible { outline: 3px solid var(--focus-ring); outline-offset: 2px; }
.account-holder-chart { display: block; max-width: none; }
.axis-tick line { stroke: var(--border-subtle); stroke-width: 1; }
.axis-tick line.zero-line { stroke: var(--border-strong); }
.axis-tick text { fill: var(--text-tertiary); font-size: 10px; text-anchor: end; }
.bar-value { fill: var(--text-primary); font-size: 10px; font-weight: 700; text-anchor: middle; }
.holder-label { fill: var(--text-secondary); font-size: 12px; font-weight: 700; text-anchor: middle; }
.holder-group:focus { outline: none; }
.holder-group:focus rect { stroke: var(--text-primary); stroke-width: 2px; }
</style>
