<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { exportUrls, reportsApi } from '@/api/endpoints'
import type { BalanceRow, BalancesPayload, RankingEntry } from '@/api/types'
import { useMoney } from '@/composables/useMoney'
import { useFlashStore } from '@/stores/flash'
import IncomeChart from '@/components/IncomeChart.vue'

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
/** The money rankings toggle between takings and profit, as in the original. */
const rankingMode = ref<'income' | 'profit'>('income')

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
  const needle = filter.value.trim().toLowerCase()
  if (!needle) return true
  return `${row.article_name} ${row.variant_label}`.toLowerCase().includes(needle)
}

const reorderRows = computed(() => (data.value?.reorder_rows ?? []).filter(matches))
const obsoleteRows = computed(() => (data.value?.obsolete_rows ?? []).filter(matches))

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
            <a class="secondary-button" :href="exportUrls.csv('bestand')">{{ t('balances.exportStock') }}</a>
          </div>
        </div>

        <div class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{{ t('sales.articles') }}</th>
                <th class="numeric">{{ t('balances.purchased') }}</th>
                <th class="numeric">{{ t('balances.sold') }}</th>
                <th class="numeric">{{ t('balances.onHand') }}</th>
                <th class="numeric">{{ t('balances.minimum') }}</th>
                <th class="numeric">{{ t('balances.cost') }}</th>
                <th class="numeric">{{ t('balances.revenue') }}</th>
                <th>{{ t('balances.offered') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="row in reorderRows"
                :key="row.variant_id"
                :class="{ 'low-stock-row': row.below_minimum }"
              >
                <td>
                  <strong>{{ row.article_name }}</strong>
                  <small>{{ row.variant_label }}</small>
                </td>
                <td class="numeric">{{ row.purchased }}</td>
                <td class="numeric">{{ row.sold }}</td>
                <td class="numeric" :class="{ 'out-of-stock': row.on_hand <= 0 }">{{ row.on_hand }}</td>
                <td class="numeric">{{ row.minimum_stock ?? '—' }}</td>
                <td class="numeric">{{ format(row.purchase_cost_cents) }}</td>
                <td class="numeric">{{ format(row.collected_cents) }}</td>
                <td>{{ row.is_offered ? t('common.yes') : t('common.no') }}</td>
              </tr>
              <tr v-if="!reorderRows.length">
                <td colspan="8" class="muted">{{ t('balances.noRows') }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section v-if="obsoleteRows.length" class="table-section">
        <div class="section-heading">
          <div><h2>{{ t('balances.obsolete') }}</h2><p>{{ t('balances.obsoleteHint') }}</p></div>
        </div>
        <div class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{{ t('sales.articles') }}</th>
                <th class="numeric">{{ t('balances.onHand') }}</th>
                <th class="numeric">{{ t('balances.revenue') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in obsoleteRows" :key="row.variant_id">
                <td><strong>{{ row.article_name }}</strong><small>{{ row.variant_label }}</small></td>
                <td class="numeric">{{ row.on_hand }}</td>
                <td class="numeric">{{ format(row.collected_cents) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
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
</style>
