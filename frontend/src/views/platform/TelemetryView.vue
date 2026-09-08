<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { platformApi } from '@/api/endpoints'
import type { TelemetryEvent, TelemetryPayload } from '@/api/telemetry-types'
import StatusBadge from '@/components/ui/StatusBadge.vue'
import { useMoney } from '@/composables/useMoney'
import { useFlashStore } from '@/stores/flash'

const { t } = useI18n()
const { format } = useMoney()
const flash = useFlashStore()

const payload = ref<TelemetryPayload | null>(null)
const loading = ref(true)
const days = ref(30)

const featureKeys = [
  'sales',
  'operations',
  'slideshow',
  'catalogue',
  'purchases',
  'band_finances',
  'balances',
  'payment_qr',
  'csv_import',
  'packing_list',
] as const

const events = computed(() => payload.value?.events ?? [])
const routeRows = computed(() =>
  (payload.value?.rows ?? []).filter((row) => row.event_kind === 'api_route'),
)
const requestCount = computed(() =>
  routeRows.value.reduce((sum, row) => sum + row.sample_count, 0),
)
const totalRequestBytes = computed(() =>
  routeRows.value.reduce((sum, row) => sum + row.total_request_bytes, 0),
)
const totalResponseBytes = computed(() =>
  routeRows.value.reduce((sum, row) => sum + row.total_response_bytes, 0),
)
const totalDurationMs = computed(() =>
  routeRows.value.reduce((sum, row) => sum + row.total_duration_ms, 0),
)

const sessionObservations = computed(() =>
  events.value.filter((event) => event.event_type === 'session_seen').length,
)
const uniqueBands = computed(() =>
  new Set(events.value.map((event) => event.band_alias).filter(Boolean)).size,
)

const latestSaleState = computed(() => {
  const byOperation = new Map<string, TelemetryEvent>()
  for (const event of events.value) {
    if (
      (event.event_type === 'sale_created' || event.event_type === 'sale_status') &&
      event.operation_alias &&
      !byOperation.has(event.operation_alias)
    ) {
      byOperation.set(event.operation_alias, event)
    }
  }
  return byOperation
})

const createdSales = computed(() => {
  const seen = new Set<string>()
  const rows: TelemetryEvent[] = []
  for (const created of events.value) {
    if (
      created.event_type !== 'sale_created' ||
      !created.operation_alias ||
      seen.has(created.operation_alias)
    ) {
      continue
    }
    seen.add(created.operation_alias)
    rows.push(latestSaleState.value.get(created.operation_alias) ?? created)
  }
  return rows
})

const saleRevenue = computed(() =>
  createdSales.value
    .filter((event) => event.status !== 'cancelled')
    .reduce((sum, event) => sum + (event.amount_cents ?? 0), 0),
)
const averageSaleValue = computed(() => {
  const active = createdSales.value.filter((event) => event.status !== 'cancelled')
  return active.length ? Math.round(saleRevenue.value / active.length) : 0
})
const openOperations = computed(() =>
  [...latestSaleState.value.values()].filter((event) => event.is_open === true).length,
)

const latestStorage = computed(() => {
  const byBand = new Map<string, number>()
  for (const event of events.value) {
    if (
      event.event_type === 'storage_snapshot' &&
      event.band_alias &&
      event.storage_bytes !== undefined &&
      !byBand.has(event.band_alias)
    ) {
      byBand.set(event.band_alias, event.storage_bytes)
    }
  }
  return byBand
})
const storageTotal = computed(() =>
  [...latestStorage.value.values()].reduce((sum, bytes) => sum + bytes, 0),
)
const storageAverage = computed(() =>
  latestStorage.value.size ? Math.round(storageTotal.value / latestStorage.value.size) : 0,
)

const paymentRows = computed(() =>
  countBy(
    createdSales.value.filter((event) => event.status !== 'cancelled'),
    (event) => event.payment_method || '—',
  ),
)
const roleRows = computed(() =>
  countBy(
    events.value.filter((event) => event.event_type === 'session_seen'),
    (event) => event.role || '—',
  ),
)
const locationRows = computed(() =>
  countByWithBands(
    events.value.filter((event) => event.location),
    (event) => event.location,
  ),
)
const featureRows = computed(() => {
  const relevant = events.value.filter((event) => event.event_type === 'feature_used')
  return featureKeys.map((key) => {
    const matching = relevant.filter((event) => event.feature_key === key)
    return {
      key,
      count: matching.length,
      bands: new Set(matching.map((event) => event.band_alias).filter(Boolean)).size,
    }
  })
})

const recentSales = computed(() => [...latestSaleState.value.values()].slice(0, 100))

function countBy(rows: TelemetryEvent[], keyFor: (row: TelemetryEvent) => string) {
  const counts = new Map<string, number>()
  for (const row of rows) {
    const key = keyFor(row)
    counts.set(key, (counts.get(key) ?? 0) + 1)
  }
  return [...counts.entries()]
    .map(([key, count]) => ({ key, count }))
    .sort((left, right) => right.count - left.count || left.key.localeCompare(right.key))
}

function countByWithBands(rows: TelemetryEvent[], keyFor: (row: TelemetryEvent) => string) {
  const grouped = new Map<string, { count: number; bands: Set<string> }>()
  for (const row of rows) {
    const key = keyFor(row)
    const entry = grouped.get(key) ?? { count: 0, bands: new Set<string>() }
    entry.count += 1
    if (row.band_alias) entry.bands.add(row.band_alias)
    grouped.set(key, entry)
  }
  return [...grouped.entries()]
    .map(([key, value]) => ({ key, count: value.count, bands: value.bands.size }))
    .sort((left, right) => right.count - left.count || left.key.localeCompare(right.key))
}

function average(total: number, count: number) {
  return count > 0 ? Math.round(total / count) : 0
}

function formatBytes(bytes: number) {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value >= 10 || unit === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`
}

function formatTime(value: string) {
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'short',
    timeStyle: 'short',
  }).format(new Date(value))
}

async function load() {
  loading.value = true
  try {
    payload.value = await platformApi.telemetry(days.value)
  } catch {
    flash.error(t('errors.generic'))
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <main class="page-shell platform-page">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('telemetryAdmin.eyebrow') }}</p>
        <h1>{{ t('telemetryAdmin.title') }}</h1>
        <p class="muted">{{ t('telemetryAdmin.intro') }}</p>
      </div>
      <div class="telemetry-actions data-toolbar">
        <label>
          {{ t('telemetryAdmin.range') }}
          <select v-model.number="days" @change="load">
            <option :value="7">7 {{ t('telemetryAdmin.days') }}</option>
            <option :value="30">30 {{ t('telemetryAdmin.days') }}</option>
            <option :value="90">90 {{ t('telemetryAdmin.days') }}</option>
            <option :value="365">365 {{ t('telemetryAdmin.days') }}</option>
          </select>
        </label>
        <a class="secondary-button" :href="platformApi.telemetryExportUrl()" download>
          {{ t('telemetryAdmin.download') }}
        </a>
      </div>
    </div>

    <p class="privacy-note">{{ t('telemetryAdmin.privacy') }}</p>
    <p v-if="loading" class="muted">{{ t('common.loading') }}</p>

    <template v-else-if="payload">
      <section class="metric-grid telemetry-metrics">
        <article class="metric-card"><span>{{ t('telemetryAdmin.requests') }}</span><strong>{{ requestCount }}</strong></article>
        <article class="metric-card"><span>{{ t('telemetryAdmin.sessions') }}</span><strong>{{ sessionObservations }}</strong></article>
        <article class="metric-card"><span>{{ t('telemetryAdmin.bands') }}</span><strong>{{ uniqueBands }}</strong></article>
        <article class="metric-card"><span>{{ t('telemetryAdmin.sales') }}</span><strong>{{ createdSales.length }}</strong></article>
        <article class="metric-card"><span>{{ t('telemetryAdmin.revenue') }}</span><strong>{{ format(saleRevenue) }}</strong></article>
        <article class="metric-card"><span>{{ t('telemetryAdmin.avgSale') }}</span><strong>{{ format(averageSaleValue) }}</strong></article>
        <article class="metric-card"><span>{{ t('telemetryAdmin.open') }}</span><strong>{{ openOperations }}</strong></article>
        <article class="metric-card"><span>{{ t('telemetryAdmin.avgRequest') }}</span><strong>{{ formatBytes(average(totalRequestBytes, requestCount)) }}</strong></article>
        <article class="metric-card"><span>{{ t('telemetryAdmin.avgResponse') }}</span><strong>{{ formatBytes(average(totalResponseBytes, requestCount)) }}</strong></article>
        <article class="metric-card"><span>{{ t('telemetryAdmin.dataVolume') }}</span><strong>{{ formatBytes(totalRequestBytes + totalResponseBytes) }}</strong></article>
        <article class="metric-card"><span>{{ t('telemetryAdmin.avgDuration') }}</span><strong>{{ average(totalDurationMs, requestCount) }} ms</strong></article>
        <article class="metric-card"><span>{{ t('telemetryAdmin.storageAvg') }}</span><strong>{{ formatBytes(storageAverage) }}</strong></article>
        <article class="metric-card"><span>{{ t('telemetryAdmin.storageTotal') }}</span><strong>{{ formatBytes(storageTotal) }}</strong></article>
      </section>

      <section class="telemetry-grid">
        <article class="table-section">
          <div class="section-heading"><div><h2>{{ t('telemetryAdmin.featureTitle') }}</h2></div></div>
          <div class="distribution-list">
            <div v-for="row in featureRows" :key="row.key">
              <span>{{ t(`telemetryAdmin.featureLabels.${row.key}`) }}</span>
              <strong :class="{ unused: row.count === 0 }">
                {{ row.count }} · {{ t('telemetryAdmin.bandCount', { count: row.bands }) }}
              </strong>
            </div>
          </div>
        </article>

        <article class="table-section">
          <div class="section-heading"><div><h2>{{ t('telemetryAdmin.paymentTitle') }}</h2></div></div>
          <div v-if="paymentRows.length" class="distribution-list">
            <div v-for="row in paymentRows" :key="row.key">
              <span>{{ row.key }}</span><strong>{{ row.count }}</strong>
            </div>
          </div>
          <p v-else class="muted">{{ t('telemetryAdmin.empty') }}</p>
        </article>

        <article class="table-section">
          <div class="section-heading"><div><h2>{{ t('telemetryAdmin.roleTitle') }}</h2></div></div>
          <div v-if="roleRows.length" class="distribution-list">
            <div v-for="row in roleRows" :key="row.key">
              <span>{{ row.key }}</span><strong>{{ row.count }}</strong>
            </div>
          </div>
          <p v-else class="muted">{{ t('telemetryAdmin.empty') }}</p>
        </article>

        <article class="table-section">
          <div class="section-heading">
            <div>
              <h2>{{ t('telemetryAdmin.locationTitle') }}</h2>
              <p>{{ t('telemetryAdmin.locationHint') }}</p>
            </div>
          </div>
          <div v-if="locationRows.length" class="distribution-list">
            <div v-for="row in locationRows.slice(0, 30)" :key="row.key">
              <span>{{ row.key }}</span>
              <strong>{{ row.count }} · {{ t('telemetryAdmin.bandCount', { count: row.bands }) }}</strong>
            </div>
          </div>
          <p v-else class="muted">{{ t('telemetryAdmin.empty') }}</p>
        </article>
      </section>

      <section class="table-section">
        <div class="section-heading">
          <div>
            <h2>{{ t('telemetryAdmin.recent') }}</h2>
            <p>{{ t('telemetryAdmin.recentHint') }}</p>
          </div>
        </div>
        <p v-if="!recentSales.length" class="muted">{{ t('telemetryAdmin.empty') }}</p>
        <div v-else class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{{ t('common.date') }}</th>
                <th>{{ t('telemetryAdmin.band') }}</th>
                <th>{{ t('telemetryAdmin.article') }}</th>
                <th>{{ t('common.quantity') }}</th>
                <th class="numeric">{{ t('telemetryAdmin.unitPrice') }}</th>
                <th class="numeric">{{ t('telemetryAdmin.amount') }}</th>
                <th>{{ t('telemetryAdmin.payment') }}</th>
                <th>{{ t('telemetryAdmin.status') }}</th>
                <th>{{ t('telemetryAdmin.location') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="event in recentSales" :key="`${event.operation_alias}-${event.occurred_at}`">
                <td>{{ formatTime(event.occurred_at) }}</td>
                <td><code>{{ event.band_alias || '—' }}</code></td>
                <td><code>{{ event.subject_alias || '—' }}</code></td>
                <td>{{ event.quantity ?? '—' }}</td>
                <td class="numeric">{{ event.unit_price_cents === undefined ? '—' : format(event.unit_price_cents) }}</td>
                <td class="numeric">{{ event.amount_cents === undefined ? '—' : format(event.amount_cents) }}</td>
                <td>{{ event.payment_method || '—' }}</td>
                <td>
                  <StatusBadge :tone="event.status === 'cancelled' ? 'danger' : event.status === 'open' ? 'warning' : 'success'">
                    {{ event.status || '—' }}
                  </StatusBadge>
                </td>
                <td>{{ event.location || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>
  </main>
</template>

<style scoped>
.telemetry-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: end;
  gap: 10px;
}

.telemetry-actions label {
  display: grid;
  gap: 4px;
}

.privacy-note {
  margin: 0 0 18px;
  padding: 12px 14px;
  border: 1px solid var(--border);
  border-radius: var(--radius-control);
  background: var(--input-bg);
  color: var(--muted);
}

.telemetry-metrics {
  grid-template-columns: repeat(auto-fit, minmax(170px, 1fr));
}

.telemetry-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 18px;
  margin-top: 18px;
}

.distribution-list {
  display: grid;
  gap: 8px;
}

.distribution-list > div {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  padding: 8px 0;
  border-bottom: 1px solid var(--border);
}

.distribution-list > div:last-child {
  border-bottom: 0;
}

.unused {
  color: var(--muted);
}

.numeric {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

@media (max-width: 820px) {
  .telemetry-grid {
    grid-template-columns: 1fr;
  }

  .telemetry-actions {
    align-items: stretch;
    flex-direction: column;
  }
}
</style>
