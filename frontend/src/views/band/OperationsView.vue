<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { operationsApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { DeliveryStatus, Position, Queues } from '@/api/types'
import { useMoney } from '@/composables/useMoney'
import { useFlashStore } from '@/stores/flash'

/**
 * The four work lists, ported from _old/templates/operations.html: parcels
 * still to send, parcels delivered, payments still to collect, and payments
 * that were chased successfully.
 *
 * The completed lists are kept separate rather than merged into the history,
 * so "what still needs doing" stays the first thing on the page.
 */
const { t } = useI18n()
const { format } = useMoney()
const flash = useFlashStore()

const queues = ref<Queues>({
  open_shipments: [],
  delivered_shipments: [],
  open_payments: [],
  settled_payments: [],
})
const loading = ref(true)

/** The forward-only steps the server accepts for a parcel. */
const deliveryOptions: { value: DeliveryStatus; label: string }[] = [
  { value: 'pending', label: 'operations.pending' },
  { value: 'shipped', label: 'operations.shipped' },
  { value: 'received', label: 'operations.received' },
]

onMounted(load)

/**
 * Reloads the list.
 *
 * `silent` is for a refresh after an inline change: the loading state replaces
 * the whole list with a one-line placeholder, which collapses the page and
 * makes the browser clamp the scroll position back to the top.
 */
/**
 * One parcel, not one line.
 *
 * The server returns positions, but a parcel is a receipt: three shirts on one
 * order are one thing to pack and one address to write. Grouping here is what
 * turns "an article and a quantity" back into an identifiable order.
 */
interface Shipment {
  receiptId: string
  soldOn: string
  customerName: string
  customerAddress: string
  eventName: string
  comment: string
  status: DeliveryStatus
  totalCents: number
  positions: Position[]
}

function groupByReceipt(positions: Position[]): Shipment[] {
  const byReceipt = new Map<string, Shipment>()
  for (const position of positions) {
    let shipment = byReceipt.get(position.receipt_id)
    if (!shipment) {
      shipment = {
        receiptId: position.receipt_id,
        soldOn: position.sold_on,
        customerName: position.customer_name,
        customerAddress: position.customer_address,
        eventName: position.event_name,
        comment: position.comment,
        status: position.delivery_status,
        totalCents: 0,
        positions: [],
      }
      byReceipt.set(position.receipt_id, shipment)
    }
    shipment.positions.push(position)
    shipment.totalCents += position.amount_due_cents
  }
  return [...byReceipt.values()]
}

const openShipments = computed(() => groupByReceipt(queues.value.open_shipments))

/** Copies an address in one go; nobody retypes a street from a screen. */
const copiedReceipt = ref('')

async function copyAddress(shipment: Shipment) {
  const text = `${shipment.customerName}\n${shipment.customerAddress}`.trim()
  try {
    await navigator.clipboard.writeText(text)
    copiedReceipt.value = shipment.receiptId
  } catch {
    flash.error(t('operations.copyFailed'))
  }
}

/** A parcel moves as a whole, so every line on the receipt moves with it. */
async function advanceShipment(shipment: Shipment, status: DeliveryStatus) {
  for (const position of shipment.positions) {
    await advance(position, status)
  }
}

async function load(silent = false) {
  if (!silent) loading.value = true
  try {
    queues.value = await operationsApi.queues()
  } catch {
    flash.error(t('errors.generic'))
  } finally {
    if (!silent) loading.value = false
  }
}

function report(error: unknown) {
  flash.error(
    error instanceof ApiError
      ? t(`errors.${error.detailCode ?? 'generic'}`, t('errors.generic'))
      : t('errors.network'),
  )
}

async function advance(position: Position, status: DeliveryStatus) {
  if (status === position.delivery_status) return
  try {
    await operationsApi.setDeliveryStatus(position.id, status)
    flash.success(t('operations.statusSaved'))
    await load(true)
  } catch (error) {
    report(error)
    await load(true)
  }
}

async function settle(position: Position) {
  try {
    await operationsApi.markPaid(position.id)
    flash.success(t('operations.paymentSaved'))
    await load(true)
  } catch (error) {
    report(error)
  }
}
</script>

<template>
  <main class="page-shell">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('operations.eyebrow') }}</p>
        <h1>{{ t('operations.title') }}</h1>
      </div>
    </div>

    <p v-if="loading" class="muted">{{ t('common.loading') }}</p>

    <template v-else>
      <section class="table-section">
        <div class="section-heading">
          <div>
            <h2>{{ t('operations.openShipments') }}</h2>
            <p>{{ t('operations.openShipmentsHint') }}</p>
          </div>
        </div>
        <p v-if="!openShipments.length" class="muted">{{ t('operations.nothingOpen') }}</p>
        <!-- Cards, not rows: packing a parcel needs an address, and an address
             does not fit a table cell. -->
        <div v-else class="shipment-grid">
          <article v-for="shipment in openShipments" :key="shipment.receiptId" class="shipment-card">
            <header>
              <code>{{ shipment.receiptId }}</code>
              <span class="muted">{{ shipment.soldOn }}</span>
            </header>

            <div class="shipment-address">
              <strong>{{ shipment.customerName || t('operations.noCustomer') }}</strong>
              <p>{{ shipment.customerAddress || '—' }}</p>
              <button
                v-if="shipment.customerAddress"
                class="compact-button"
                type="button"
                @click="copyAddress(shipment)"
              >
                {{ copiedReceipt === shipment.receiptId ? t('operations.copied') : t('operations.copyAddress') }}
              </button>
            </div>

            <ul class="shipment-items">
              <li v-for="position in shipment.positions" :key="position.id">
                <span>{{ position.quantity }}×</span>
                <span>
                  <strong>{{ position.article_name }}</strong>
                  <small>{{ position.variant_label }}</small>
                </span>
                <b>{{ format(position.amount_due_cents) }}</b>
              </li>
            </ul>

            <p v-if="shipment.eventName || shipment.comment" class="shipment-note muted">
              {{ [shipment.eventName, shipment.comment].filter(Boolean).join(' · ') }}
            </p>

            <footer>
              <span class="shipment-total">{{ format(shipment.totalCents) }}</span>
              <select
                class="status-select"
                :value="shipment.status"
                @change="advanceShipment(shipment, ($event.target as HTMLSelectElement).value as DeliveryStatus)"
              >
                <!-- Every state stays selectable: a mis-tap has to be
                     correctable, not permanent. -->
                <option v-for="option in deliveryOptions" :key="option.value" :value="option.value">
                  {{ t(option.label) }}
                </option>
              </select>
            </footer>
          </article>
        </div>
      </section>

      <section class="table-section">
        <div class="section-heading">
          <div>
            <h2>{{ t('operations.openPayments') }}</h2>
            <p>{{ t('operations.openPaymentsHint') }}</p>
          </div>
        </div>
        <p v-if="!queues.open_payments.length" class="muted">{{ t('operations.nothingOpen') }}</p>
        <div v-else class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{{ t('sales.receiptId') }}</th>
                <th>{{ t('operations.customer') }}</th>
                <th>{{ t('sales.articles') }}</th>
                <th class="numeric">{{ t('history.amount') }}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="position in queues.open_payments" :key="position.id">
                <td>
                  <code>{{ position.receipt_id }}</code>
                  <small>{{ position.sold_on }}</small>
                </td>
                <td>{{ position.customer_name || '—' }}</td>
                <td>
                  <strong>{{ position.article_name }}</strong>
                  <small>{{ position.variant_label }}</small>
                </td>
                <td class="numeric">{{ format(position.amount_due_cents) }}</td>
                <td>
                  <button class="compact-button" type="button" @click="settle(position)">
                    {{ t('operations.markPaid') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="table-section">
        <div class="section-heading">
          <div>
            <h2>{{ t('operations.deliveredShipments') }}</h2>
            <p>{{ t('operations.deliveredHint') }}</p>
          </div>
        </div>
        <p v-if="!queues.delivered_shipments.length" class="muted">{{ t('operations.nothingYet') }}</p>
        <ul v-else class="ranking-list">
          <li v-for="position in queues.delivered_shipments" :key="position.id" class="delivered-row">
            <span>
              <strong>{{ position.article_name }}</strong>
              <small>
                {{ position.variant_label }} ·
                {{ position.receipt_id }}
                <template v-if="position.customer_name"> · {{ position.customer_name }}</template>
              </small>
            </span>
            <b>{{ format(position.amount_due_cents) }}</b>
            <!-- The correction has to be reachable from here too: a parcel
                 marked received by mistake is only visible in this list. -->
            <select
              class="status-select"
              :value="position.delivery_status"
              @change="advance(position, ($event.target as HTMLSelectElement).value as DeliveryStatus)"
            >
              <option v-for="option in deliveryOptions" :key="option.value" :value="option.value">
                {{ t(option.label) }}
              </option>
            </select>
          </li>
        </ul>
      </section>

      <section class="table-section">
        <div class="section-heading">
          <div>
            <h2>{{ t('operations.settledPayments') }}</h2>
            <p>{{ t('operations.settledHint') }}</p>
          </div>
        </div>
        <p v-if="!queues.settled_payments.length" class="muted">{{ t('operations.nothingYet') }}</p>
        <ul v-else class="ranking-list">
          <li v-for="position in queues.settled_payments" :key="position.id">
            <span>
              <strong>{{ position.article_name }}</strong>
              <small>
                {{ position.variant_label }} ·
                {{ position.receipt_id }}
                <template v-if="position.customer_name"> · {{ position.customer_name }}</template>
              </small>
            </span>
            <b>{{ format(position.amount_due_cents) }}</b>
          </li>
        </ul>
      </section>
    </template>
  </main>
</template>

<style scoped>
.delivered-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto auto;
  gap: 12px;
  align-items: center;
}

.shipment-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 14px;
}

.shipment-card {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 16px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--panel-raised);
}

.shipment-card header {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 10px;
}

.shipment-card header code {
  font-size: 0.92rem;
  font-weight: 700;
}

.shipment-address {
  display: grid;
  justify-items: start;
  gap: 6px;
  padding: 12px;
  border-radius: 10px;
  background: var(--option-bg);
}

.shipment-address p {
  margin: 0;
  color: var(--muted);
  line-height: 1.45;
  white-space: pre-line;
}

.shipment-items {
  display: grid;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.shipment-items li {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  gap: 10px;
  align-items: baseline;
}

.shipment-items li > span:first-child {
  color: var(--accent-bright);
  font-variant-numeric: tabular-nums;
  font-weight: 800;
}

.shipment-items small {
  display: block;
  color: var(--muted);
}

.shipment-items b {
  font-variant-numeric: tabular-nums;
}

.shipment-note {
  margin: 0;
  font-size: 0.85rem;
}

.shipment-card footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: auto;
  padding-top: 12px;
  border-top: 1px solid var(--border);
}

.shipment-total {
  font-size: 1.1rem;
  font-variant-numeric: tabular-nums;
  font-weight: 800;
}

.numeric {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

td small {
  display: block;
  color: var(--muted);
}
</style>
