<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { exportUrls, reportsApi } from '@/api/endpoints'
import type { BalanceRow, BalancesPayload, RankingEntry } from '@/api/types'
import { useMoney } from '@/composables/useMoney'
import { useFlashStore } from '@/stores/flash'
import IncomeChart from '@/components/IncomeChart.vue'
import BalanceTable, { type BalanceSortKey } from '@/components/BalanceTable.vue'

/**
 * The balances page, ported from _old/templates/balances.html.
 *
 * "Saldo" here means collected payments plus donations minus recorded goods
 * received. It is deliberately not called profit: a reorder would make a
 * profit figure swing wildly for a week and mislead the band.
 */
const { t } = useI18n()
const { format } = useMoney()
const flash = useFlashStore()

const data = ref<BalancesPayload | null>(null)
const loading = ref(true)
const filter = ref('')
const onlyPurchased = ref(true)
const grouped = ref(false)
/** The money rankings toggle between takings and profit, as in the original. */
const rankingMode = ref<'income' | 'profit'>('income')
type BalanceView = 'reorder' | 'obsolete'
type SortState = { key: BalanceSortKey | null; direction: 'default' | 'asc' | 'desc' }
const sort = ref<Record<BalanceView, SortState>>({
  reorder: { key: null, direction: 'default' },
  obsolete: { key: null, direction: 'default' },
})
const collator = new Intl.Collator('de-DE', { numeric: true, sensitivity: 'base' })

onMounted(async () => {
  try {
    data.value = await reportsApi.balances()
  } catch {
    flash.error(t('errors.generic'))
  } finally {
    loading.value = false
  }
})

function matches(row: BalanceRow) {
  const needle = normalise(filter.value).trim()
  if (!needle) return true
  return normalise(`${row.article_name} ${row.variant_label}`).includes(needle)
}

function normalise(value: string) {
  return value.toLocaleLowerCase('de-DE').normalize('NFD').replace(/[\u0300-\u036f]/g, '')
}

function sourceRows(view: BalanceView) {
  return view === 'reorder' ? (data.value?.reorder_rows ?? []) : (data.value?.obsolete_rows ?? [])
}

function sortableValue(row: BalanceRow, key: BalanceSortKey): string | number | null {
  if (key === 'article_name') return `${row.article_name}\u0000${row.variant_label}`
  if (key === 'minimum_stock') return row.minimum_stock
  if (key === 'below_minimum' || key === 'no_reorder' || key === 'is_available_for_sale') return row[key] ? 1 : 0
  return row[key]
}

function sortedRows(view: BalanceView) {
  const visible = sourceRows(view)
    .filter((row) => matches(row) && (!onlyPurchased.value || row.purchased > 0))
    .slice()
  const current = sort.value[view]
  if (!current.key || current.direction === 'default') return visible
  return visible.sort((left, right) => {
    const leftValue = sortableValue(left, current.key!)
    const rightValue = sortableValue(right, current.key!)
    if (leftValue === null || rightValue === null) {
      if (leftValue === null && rightValue !== null) return 1
      if (rightValue === null && leftValue !== null) return -1
    }
    let comparison = current.key === 'article_name'
      ? collator.compare(String(leftValue), String(rightValue))
      : Number(leftValue) - Number(rightValue)
    if (comparison === 0) {
      comparison = collator.compare(`${left.article_name} ${left.variant_label}`, `${right.article_name} ${right.variant_label}`)
    }
    return current.direction === 'asc' ? comparison : -comparison
  })
}

const reorderRows = computed(() => sortedRows('reorder'))
const obsoleteRows = computed(() => sortedRows('obsolete'))
const visibleRowCount = computed(() => reorderRows.value.length + obsoleteRows.value.length)

function cycleSort(view: BalanceView, key: BalanceSortKey) {
  const current = sort.value[view]
  if (current.key !== key || current.direction === 'default') sort.value[view] = { key, direction: 'asc' }
  else if (current.direction === 'asc') sort.value[view] = { key, direction: 'desc' }
  else sort.value[view] = { key: null, direction: 'default' }
}

function groupsFor(rows: BalanceRow[]) {
  const groups: BalanceRow[][] = []
  const known = new Map<string, BalanceRow[]>()
  for (const row of rows) {
    let group = known.get(row.article_name)
    if (!group) {
      group = []
      known.set(row.article_name, group)
      groups.push(group)
    }
    group.push(row)
  }
  return groups
}

function csvEscape(value: unknown) {
  const text = String(value ?? '')
  return /[;"\r\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text
}

function downloadCsv(kind: 'inventory' | 'articles') {
  const columns: Array<[string, (row: BalanceRow) => unknown]> = kind === 'inventory'
    ? [
        ['Artikel', (row) => row.article_name], ['Optionen', (row) => row.variant_label],
        ['Gekauft', (row) => row.purchased], ['Verkauft', (row) => row.sold],
        ['Aktueller Bestand', (row) => row.on_hand], ['Mindestbestand', (row) => row.minimum_stock ?? ''],
        ['Mindestbestandswarnung', (row) => row.below_minimum ? 'ja' : 'nein'],
        ['Nachbestellen', (row) => row.no_reorder ? 'nein' : 'ja'],
        ['Angeboten', (row) => row.is_available_for_sale ? 'ja' : 'nein'],
        ['Ausgaben', (row) => format(row.purchase_cost_cents)], ['Umsatz', (row) => format(row.revenue_cents)],
        ['Spenden', (row) => format(row.donation_cents)],
      ]
    : [
        ['Artikel', (row) => row.article_name], ['Optionen', (row) => row.variant_label],
        ['Verkaufspreis', (row) => format(row.sale_price_cents)],
        ['Standard-Einkaufspreis', (row) => format(row.default_purchase_price_cents)],
        ['Mindestbestand', (row) => row.minimum_stock ?? ''],
        ['Nachbestellen', (row) => row.no_reorder ? 'nein' : 'ja'],
        ['Angeboten', (row) => row.is_available_for_sale ? 'ja' : 'nein'],
        ['Status', (row) => row.is_active ? 'aktiv' : 'inaktiv'],
      ]
  const output: unknown[][] = []
  let hasRows = false
  for (const view of ['reorder', 'obsolete'] as const) {
    const rows = sortedRows(view)
    if (!rows.length) continue
    if (hasRows) output.push([])
    output.push([view === 'reorder' ? 'Artikelbilanz' : 'Obsolet'])
    output.push(columns.map(([label]) => label))
    const rowGroups = grouped.value ? groupsFor(rows) : [rows]
    rowGroups.forEach((group, index) => {
      if (grouped.value && index > 0) output.push([])
      group.forEach((row) => output.push(columns.map(([, value]) => value(row))))
    })
    hasRows = true
  }
  if (!hasRows) output.push([t('balances.noRows')])
  const csv = `\ufeff${output.map((row) => row.map(csvEscape).join(';')).join('\r\n')}\r\n`
  const url = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8' }))
  const link = document.createElement('a')
  link.href = url
  const now = new Date()
  const localDate = new Date(now.getTime() - now.getTimezoneOffset() * 60_000)
    .toISOString()
    .slice(0, 10)
  link.download = `${kind === 'inventory' ? 'bestand' : 'artikel'}-${localDate}.csv`
  document.body.append(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
}

function rankingValue(entry: RankingEntry) {
  return rankingMode.value === 'income' ? entry.income_cents : entry.profit_cents
}
</script>

<template>
  <main class="page-shell">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('balances.eyebrow') }}</p>
        <h1>{{ t('balances.title') }}</h1>
      </div>
      <a class="secondary-button" :href="exportUrls.zip()">{{ t('balances.exportAll') }}</a>
    </div>

    <p v-if="loading" class="muted">{{ t('common.loading') }}</p>

    <template v-else-if="data">
      <section class="metric-grid">
        <article class="metric-card">
          <span>{{ t('balances.purchaseCost') }}</span>
          <strong>{{ format(data.summary.purchase_cost_cents) }}</strong>
        </article>
        <article class="metric-card">
          <span>{{ t('balances.collected') }}</span>
          <strong>{{ format(data.summary.collected_cents) }}</strong>
        </article>
        <article class="metric-card">
          <span>{{ t('balances.donation') }}</span>
          <strong>{{ format(data.summary.donation_cents) }}</strong>
        </article>
        <article class="metric-card">
          <span>{{ t('balances.cashBalance') }}</span>
          <strong>{{ format(data.summary.cash_balance_cents) }}</strong>
        </article>
      </section>

      <section class="metric-grid small-metrics">
        <article class="metric-card">
          <span>{{ t('balances.outstanding') }}</span>
          <strong>{{ format(data.summary.outstanding_cents) }}</strong>
        </article>
        <article class="metric-card">
          <span>{{ t('balances.pendingDeliveries') }}</span>
          <strong>{{ data.summary.pending_delivery_count }}</strong>
        </article>
        <article class="metric-card">
          <span>{{ t('balances.stock') }}</span>
          <strong>{{ data.summary.stock_count }}</strong>
        </article>
        <article class="metric-card" :class="{ warning: data.summary.minimum_stock_warning_count > 0 }">
          <span>{{ t('balances.lowStock') }}</span>
          <strong>{{ data.summary.minimum_stock_warning_count }}</strong>
        </article>
      </section>

      <section class="table-section">
        <div class="section-heading">
          <div>
            <h2>{{ t('balances.bandLedger') }}</h2>
            <p>{{ t('balances.bandLedgerHint') }}</p>
          </div>
          <RouterLink class="secondary-button" :to="{ name: 'band-finances' }">
            {{ t('nav.bandFinances') }}
          </RouterLink>
        </div>
        <div class="metric-grid small-metrics">
          <article class="metric-card">
            <span>{{ t('balances.bandIncome') }}</span>
            <strong>{{ format(data.summary.band_income_cents) }}</strong>
          </article>
          <article class="metric-card">
            <span>{{ t('balances.bandExpense') }}</span>
            <strong>{{ format(data.summary.band_expense_cents) }}</strong>
          </article>
          <article class="metric-card">
            <span>{{ t('balances.overallBalance') }}</span>
            <strong>{{ format(data.summary.overall_balance_cents) }}</strong>
          </article>
        </div>
      </section>

      <section class="insight-grid">
        <article class="table-section insight-card">
          <div class="section-heading">
            <div><h2>{{ t('balances.topSelling') }}</h2><p>{{ t('balances.topSellingHint') }}</p></div>
          </div>
          <ol class="ranking-list">
            <li v-for="entry in data.top_selling_items" :key="entry.label">
              <span><strong>{{ entry.label }}</strong><small>{{ entry.quantity }} {{ t('balances.pieces') }}</small></span>
              <b>{{ format(entry.income_cents) }}</b>
            </li>
            <li v-if="!data.top_selling_items.length" class="muted">{{ t('balances.noSales') }}</li>
          </ol>
        </article>

        <article class="table-section insight-card">
          <div class="section-heading ranking-heading">
            <div><h2>{{ t('balances.topRevenue') }}</h2></div>
            <div class="ranking-mode-toggle" role="group">
              <button
                class="ranking-mode-button"
                :class="{ active: rankingMode === 'income' }"
                type="button"
                @click="rankingMode = 'income'"
              >{{ t('balances.income') }}</button>
              <button
                class="ranking-mode-button"
                :class="{ active: rankingMode === 'profit' }"
                type="button"
                @click="rankingMode = 'profit'"
              >{{ t('balances.profit') }}</button>
            </div>
          </div>
          <ol class="ranking-list">
            <li v-for="entry in data.top_revenue_items" :key="entry.label">
              <span><strong>{{ entry.label }}</strong><small>{{ entry.quantity }} {{ t('balances.pieces') }}</small></span>
              <b>{{ format(rankingValue(entry)) }}</b>
            </li>
            <li v-if="!data.top_revenue_items.length" class="muted">{{ t('balances.noSales') }}</li>
          </ol>
        </article>

        <article class="table-section insight-card">
          <div class="section-heading"><div><h2>{{ t('balances.topEvents') }}</h2></div></div>
          <ol class="ranking-list">
            <li v-for="entry in data.top_events" :key="entry.label">
              <span><strong>{{ entry.label }}</strong><small>{{ entry.quantity }} {{ t('balances.pieces') }}</small></span>
              <b>{{ format(rankingValue(entry)) }}</b>
            </li>
            <li v-if="!data.top_events.length" class="muted">{{ t('balances.noSales') }}</li>
          </ol>
        </article>

        <article class="table-section insight-card">
          <div class="section-heading"><div><h2>{{ t('balances.topSellers') }}</h2></div></div>
          <ol class="ranking-list">
            <li v-for="entry in data.top_sellers" :key="entry.label">
              <span><strong>{{ entry.label }}</strong><small>{{ entry.quantity }} {{ t('balances.pieces') }}</small></span>
              <b>{{ format(rankingValue(entry)) }}</b>
            </li>
            <li v-if="!data.top_sellers.length" class="muted">{{ t('balances.noSales') }}</li>
          </ol>
        </article>
      </section>

      <section class="table-section income-chart-section">
        <div class="section-heading">
          <div><h2>{{ t('balances.incomeChart') }}</h2><p>{{ t('balances.incomeChartHint') }}</p></div>
        </div>
        <IncomeChart :points="data.daily_income" />
      </section>

      <section class="table-section">
        <div class="section-heading ledger-heading">
          <div>
            <h2>{{ t('balances.ledger') }}</h2>
            <p>{{ t('balances.ledgerHint') }}</p>
          </div>
          <div class="ledger-actions">
            <label class="table-filter">
              {{ t('common.filter') }}
              <input v-model="filter" type="search" />
            </label>
            <label class="balance-toggle"><input v-model="onlyPurchased" type="checkbox" /> {{ t('balances.onlyPurchased') }}</label>
            <label class="balance-toggle"><input v-model="grouped" type="checkbox" /> {{ t('balances.groupByArticle') }}</label>
            <button class="secondary-button" type="button" @click="downloadCsv('inventory')">{{ t('balances.exportStock') }}</button>
            <button class="secondary-button" type="button" @click="downloadCsv('articles')">{{ t('balances.exportArticles') }}</button>
          </div>
        </div>
        <p class="muted balance-result-count">{{ t('balances.visibleCount', { count: visibleRowCount }) }}</p>
        <BalanceTable
          :rows="reorderRows"
          :grouped="grouped"
          :sort-key="sort.reorder.key"
          :sort-direction="sort.reorder.direction"
          :empty-message="t('balances.noRows')"
          @sort="cycleSort('reorder', $event)"
        />
      </section>

      <section class="table-section">
        <div class="section-heading">
          <div><h2>{{ t('balances.obsolete') }}</h2><p>{{ t('balances.obsoleteHint') }}</p></div>
        </div>
        <BalanceTable
          :rows="obsoleteRows"
          :grouped="grouped"
          :sort-key="sort.obsolete.key"
          :sort-direction="sort.obsolete.direction"
          :empty-message="t('balances.noObsoleteRows')"
          @sort="cycleSort('obsolete', $event)"
        />
      </section>
    </template>
  </main>
</template>

<style scoped>
.numeric {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

td small {
  display: block;
  color: var(--muted);
}

.ranking-mode-button.active {
  color: var(--text);
  border-color: var(--accent);
}

.metric-card.warning strong {
  color: var(--warning);
}

.ledger-actions,
.balance-toggle {
  display: flex;
  align-items: center;
  gap: 8px;
}

.ledger-actions {
  flex-wrap: wrap;
  justify-content: flex-end;
}

.balance-result-count {
  margin: 0 0 12px;
}
</style>
