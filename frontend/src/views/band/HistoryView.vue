<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { operationsApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { Receipt } from '@/api/types'
import DateRangeFilter from '@/components/DateRangeFilter.vue'
import { useMoney } from '@/composables/useMoney'
import { useFlashStore } from '@/stores/flash'
import { datedFilename, downloadCsv } from '@/utils/csvDownload'
import { isWithinDateRange } from '@/utils/dateRange'

/**
 * The receipt history, ported from _old/templates/history.html.
 *
 * Each purchase is one row that expands into its positions. Cancelled entries
 * stay visible and greyed out rather than disappearing — that is the whole
 * point of a cancellation as opposed to a deletion.
 */
const { t } = useI18n()
const { format } = useMoney()
const flash = useFlashStore()

const receipts = ref<Receipt[]>([])
const loading = ref(true)
const filter = ref('')
const dateFrom = ref('')
const dateTo = ref('')
const expanded = ref<Set<string>>(new Set())

/** Confirming a cancellation takes three seconds, as in the original — long
 *  enough that it cannot happen by a stray tap at a busy stand. */
const pendingCancel = ref<{ saleId: number; scope: 'item' | 'receipt' } | null>(null)
const holdProgress = ref(0)
let holdTimer: number | undefined

const visible = computed(() => {
  const needle = filter.value.trim().toLowerCase()
  return receipts.value.filter((receipt) => {
    if (!isWithinDateRange(receipt.sold_on, dateFrom.value, dateTo.value)) return false
    if (!needle) return true
    return [
      receipt.receipt_id,
      receipt.sold_on,
      receipt.customer_name,
      receipt.event_name,
      receipt.sold_by,
      receipt.payment_method,
      ...receipt.positions.map((position) => `${position.article_name} ${position.variant_label}`),
    ]
      .join(' ')
      .toLowerCase()
      .includes(needle)
  })
})
onMounted(load)

function exportVisible() {
  downloadCsv(
    datedFilename('verkaeufe'),
    [
      t('common.date'),
      t('history.receipt'),
      t('sales.event'),
      t('sales.soldBy'),
      t('sales.paymentMethod'),
      t('history.amount'),
      t('history.donation'),
      t('sales.customerName'),
    ],
    visible.value.map((receipt) => [
      receipt.sold_on,
      receipt.receipt_id,
      receipt.event_name,
      receipt.sold_by,
      receipt.payment_method,
      format(receipt.total_due_cents),
      receipt.donation_cents ? format(receipt.donation_cents) : '',
      receipt.customer_name,
    ]),
  )
}

async function load() {  loading.value = true
  try {
    receipts.value = (await operationsApi.history()).receipts
  } catch {
    flash.error(t('errors.generic'))
  } finally {
    loading.value = false
  }
}

function toggle(receiptId: string) {
  const next = new Set(expanded.value)
  if (next.has(receiptId)) next.delete(receiptId)
  else next.add(receiptId)
  expanded.value = next
}

function startCancel(saleId: number, scope: 'item' | 'receipt') {
  pendingCancel.value = { saleId, scope }
  holdProgress.value = 0
  window.clearInterval(holdTimer)
  holdTimer = window.setInterval(() => {
    holdProgress.value += 100 / 30
    if (holdProgress.value >= 100) {
      window.clearInterval(holdTimer)
    }
  }, 100)
}

function abortCancel() {
  pendingCancel.value = null
  holdProgress.value = 0
  window.clearInterval(holdTimer)
}

async function confirmCancel() {
  if (!pendingCancel.value || holdProgress.value < 100) return
  const { saleId, scope } = pendingCancel.value
  try {
    await operationsApi.cancel(saleId, scope)
    flash.success(t('history.cancelled'))
    await load()
  } catch (error) {
    flash.error(
      error instanceof ApiError
        ? t(`errors.${error.detailCode ?? 'generic'}`, t('errors.generic'))
        : t('errors.network'),
    )
  } finally {
    abortCancel()
  }
}
</script>

<template>
  <main class="page-shell">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('history.eyebrow') }}</p>
        <h1>{{ t('history.title') }}</h1>
      </div>
      <div class="history-toolbar">
        <DateRangeFilter
          v-model:from="dateFrom"
          v-model:to="dateTo"
          exportable
          @export="exportVisible"
        />
        <label class="table-filter">
          {{ t('common.filter') }}
          <input v-model="filter" type="search" :placeholder="t('history.filterHint')" />
        </label>
      </div>
    </div>

    <section class="table-section">
      <p v-if="loading" class="muted">{{ t('common.loading') }}</p>
      <p v-else-if="!visible.length" class="muted">{{ t('history.empty') }}</p>

      <div v-else class="table-scroll">
        <table>
          <thead>
            <tr>
              <th></th>
              <th>{{ t('history.receipt') }}</th>
              <th>{{ t('common.date') }}</th>
              <th>{{ t('sales.event') }}</th>
              <th>{{ t('sales.soldBy') }}</th>
              <th>{{ t('sales.paymentMethod') }}</th>
              <th class="numeric">{{ t('history.amount') }}</th>
              <th class="numeric">{{ t('history.donation') }}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <template v-for="receipt in visible" :key="receipt.receipt_id">
              <tr :class="{ 'cancelled-row': receipt.is_fully_cancelled }">
                <td>
                  <button class="icon-button" type="button" @click="toggle(receipt.receipt_id)">
                    {{ expanded.has(receipt.receipt_id) ? '▾' : '▸' }}
                  </button>
                </td>
                <td><code>{{ receipt.receipt_id }}</code></td>
                <td>{{ receipt.sold_on }}</td>
                <td>{{ receipt.event_name || '—' }}</td>
                <td>{{ receipt.sold_by || '—' }}</td>
                <td>{{ receipt.payment_method }}</td>
                <td class="numeric">{{ format(receipt.total_due_cents) }}</td>
                <td class="numeric">{{ receipt.donation_cents ? format(receipt.donation_cents) : '—' }}</td>
                <td>
                  <button
                    v-if="!receipt.is_fully_cancelled"
                    class="compact-button danger-button"
                    type="button"
                    @click="startCancel(receipt.positions[0].id, 'receipt')"
                  >
                    {{ t('history.cancelReceipt') }}
                  </button>
                </td>
              </tr>

              <tr v-if="expanded.has(receipt.receipt_id)" class="expanded-row">
                <td></td>
                <td colspan="8">
                  <div class="receipt-detail">
                    <table class="nested-table">
                      <tbody>
                        <tr
                          v-for="position in receipt.positions"
                          :key="position.id"
                          :class="{ 'cancelled-row': position.is_cancelled }"
                        >
                          <td>
                            <strong>{{ position.article_name }}</strong>
                            <small>{{ position.variant_label }}</small>
                          </td>
                          <td class="numeric">{{ position.quantity }} ×</td>
                          <td class="numeric">{{ format(position.unit_price_cents) }}</td>
                          <td class="numeric">{{ format(position.amount_due_cents) }}</td>
                          <td>
                            <span v-if="position.is_cancelled" class="status danger">
                              {{ t('history.cancelledLabel') }}
                            </span>
                            <span v-else-if="!position.is_paid" class="status warning">
                              {{ t('history.unpaid') }}
                            </span>
                            <span v-else-if="!position.is_received" class="status warning">
                              {{ t('history.notDelivered') }}
                            </span>
                          </td>
                          <td>
                            <button
                              v-if="!position.is_cancelled"
                              class="compact-button"
                              type="button"
                              @click="startCancel(position.id, 'item')"
                            >
                              {{ t('history.cancelItem') }}
                            </button>
                          </td>
                        </tr>
                      </tbody>
                    </table>

                    <dl class="receipt-meta">
                      <template v-if="receipt.customer_name">
                        <dt>{{ t('sales.customerName') }}</dt>
                        <dd>{{ receipt.customer_name }}</dd>
                      </template>
                      <template v-if="receipt.customer_address">
                        <dt>{{ t('sales.customerAddress') }}</dt>
                        <dd>{{ receipt.customer_address }}</dd>
                      </template>
                      <template v-if="receipt.comment">
                        <dt>{{ t('common.comment') }}</dt>
                        <dd>{{ receipt.comment }}</dd>
                      </template>
                    </dl>
                  </div>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>
    </section>

    <!-- The three-second hold is the original's safeguard against a stray tap. -->
    <dialog v-if="pendingCancel" class="confirmation-dialog" open>
      <div class="stack-form">
        <div>
          <p class="eyebrow">{{ t('history.cancelEyebrow') }}</p>
          <h2>{{ t('history.cancelTitle') }}</h2>
          <p>{{ t('history.cancelIntro') }}</p>
        </div>
        <div class="hold-progress" :style="{ '--progress': `${holdProgress}%` }"></div>
        <div class="dialog-actions">
          <button class="secondary-button" type="button" @click="abortCancel">
            {{ t('common.cancel') }}
          </button>
          <button
            class="danger-button"
            type="button"
            :disabled="holdProgress < 100"
            @click="confirmCancel"
          >
            {{ t('history.cancelConfirm') }}
          </button>
        </div>
      </div>
    </dialog>
  </main>
</template>

<style scoped>
.history-toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  align-items: flex-end;
  justify-content: flex-end;
}
.numeric {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.expanded-row > td {
  background: var(--panel-muted);
}

.receipt-detail {
  display: grid;
  gap: 14px;
  padding: 10px 0;
}

.nested-table td {
  padding: 6px 10px;
}

.nested-table td small {
  display: block;
  color: var(--muted);
}

.receipt-meta {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 4px 14px;
  margin: 0;
  color: var(--muted);
  font-size: 0.88rem;
}

.receipt-meta dt {
  font-weight: 650;
}

.receipt-meta dd {
  margin: 0;
}

.hold-progress {
  height: 6px;
  border-radius: 999px;
  background: var(--panel-muted);
  overflow: hidden;
}

.hold-progress::after {
  content: '';
  display: block;
  height: 100%;
  width: var(--progress, 0%);
  background: var(--danger);
  transition: width 0.1s linear;
}
</style>
