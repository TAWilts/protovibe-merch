<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import type { BalanceRow } from '@/api/types'
import { useMoney } from '@/composables/useMoney'

export type BalanceSortKey =
  | 'article_name'
  | 'purchased'
  | 'sold'
  | 'on_hand'
  | 'minimum_stock'
  | 'below_minimum'
  | 'no_reorder'
  | 'is_available_for_sale'
  | 'purchase_cost_cents'
  | 'revenue_cents'
  | 'donation_cents'

const props = defineProps<{
  rows: BalanceRow[]
  grouped: boolean
  sortKey: BalanceSortKey | null
  sortDirection: 'default' | 'asc' | 'desc'
  emptyMessage: string
}>()

const emit = defineEmits<{ sort: [key: BalanceSortKey] }>()
const { t } = useI18n()
const { format } = useMoney()

const headers: Array<{ key: BalanceSortKey; label: string; numeric?: boolean }> = [
  { key: 'article_name', label: 'sales.articles' },
  { key: 'purchased', label: 'balances.purchased', numeric: true },
  { key: 'sold', label: 'balances.sold', numeric: true },
  { key: 'on_hand', label: 'balances.onHand', numeric: true },
  { key: 'minimum_stock', label: 'balances.minimum', numeric: true },
  { key: 'below_minimum', label: 'balances.warning' },
  { key: 'no_reorder', label: 'balances.reorder' },
  { key: 'is_available_for_sale', label: 'balances.offered' },
  { key: 'purchase_cost_cents', label: 'balances.cost', numeric: true },
  { key: 'revenue_cents', label: 'balances.revenue', numeric: true },
  { key: 'donation_cents', label: 'balances.donation', numeric: true },
]

const rowGroups = computed(() => {
  if (!props.grouped) return [{ name: '', rows: props.rows }]
  const groups: Array<{ name: string; rows: BalanceRow[] }> = []
  const byName = new Map<string, { name: string; rows: BalanceRow[] }>()
  for (const row of props.rows) {
    let group = byName.get(row.article_name)
    if (!group) {
      group = { name: row.article_name, rows: [] }
      byName.set(row.article_name, group)
      groups.push(group)
    }
    group.rows.push(row)
  }
  return groups
})

function ariaSort(key: BalanceSortKey) {
  if (props.sortKey !== key || props.sortDirection === 'default') return 'none'
  return props.sortDirection === 'asc' ? 'ascending' : 'descending'
}

function sortIcon(key: BalanceSortKey) {
  if (props.sortKey !== key || props.sortDirection === 'default') return ''
  return props.sortDirection === 'asc' ? '↑' : '↓'
}
</script>

<template>
  <p v-if="!rows.length" class="muted">{{ emptyMessage }}</p>
  <template v-else>
  <section
    v-for="group in rowGroups"
    :key="group.name || 'flat'"
    class="balance-row-group"
  >
    <div v-if="grouped" class="balance-group-heading">
      <h3>{{ group.name }}</h3>
      <small>{{ t('balances.variantCount', { count: group.rows.length }) }}</small>
    </div>
    <div class="table-scroll">
      <table class="balance-table">
        <thead>
          <tr>
            <th
              v-for="header in headers"
              :key="header.key"
              :class="{ numeric: header.numeric }"
              :aria-sort="ariaSort(header.key)"
            >
              <button class="balance-sort-button" type="button" @click="emit('sort', header.key)">
                {{ t(header.label) }} <span aria-hidden="true">{{ sortIcon(header.key) }}</span>
              </button>
            </th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="row in group.rows"
            :key="row.variant_id"
            :class="{ 'low-stock-row': row.below_minimum }"
          >
            <td>
              <strong>{{ row.article_name }}</strong>
              <small>{{ row.variant_label }}</small>
              <small v-if="!row.is_active" class="muted">{{ t('balances.retiredVariant') }}</small>
            </td>
            <td class="numeric">{{ row.purchased }}</td>
            <td class="numeric">{{ row.sold }}</td>
            <td class="numeric" :class="{ 'out-of-stock': row.on_hand <= 0 }">{{ row.on_hand }}</td>
            <td class="numeric">{{ row.minimum_stock ?? '—' }}</td>
            <td>
              <span v-if="row.below_minimum" class="status warning">{{ t('balances.limitReached') }}</span>
              <span v-else>—</span>
            </td>
            <td><span class="status" :class="row.no_reorder ? 'warning' : 'good'">{{ row.no_reorder ? t('common.no') : t('common.yes') }}</span></td>
            <td><span class="status" :class="row.is_available_for_sale ? 'good' : 'warning'">{{ row.is_available_for_sale ? t('common.yes') : t('common.no') }}</span></td>
            <td class="numeric">{{ format(row.purchase_cost_cents) }}</td>
            <td class="numeric">{{ format(row.revenue_cents) }}</td>
            <td class="numeric">{{ format(row.donation_cents) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
  </template>
</template>

<style scoped>
.balance-row-group + .balance-row-group {
  margin-top: 18px;
}

.balance-group-heading {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  margin: 2px 2px 8px;
}

.balance-group-heading h3 {
  margin: 0;
}

.balance-group-heading small,
td small {
  display: block;
  color: var(--muted);
}

.balance-sort-button {
  width: 100%;
  padding: 0;
  border: 0;
  color: inherit;
  background: transparent;
  cursor: pointer;
  font: inherit;
  font-weight: inherit;
  text-align: inherit;
  white-space: nowrap;
}

.balance-sort-button span {
  display: inline-block;
  min-width: 0.8em;
  color: var(--accent-bright);
}

.numeric {
  text-align: right;
  font-variant-numeric: tabular-nums;
}
</style>
