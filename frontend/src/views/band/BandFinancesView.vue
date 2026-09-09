<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { bandFinanceAttachmentsApi, reportsApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { Attachment, BandLedger, BandTransaction } from '@/api/types'
import DateRangeFilter from '@/components/DateRangeFilter.vue'
import RecurringBandFinances from '@/components/RecurringBandFinances.vue'
import AppDialog from '@/components/ui/AppDialog.vue'
import { useMoney, parseAmount } from '@/composables/useMoney'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'
import { datedFilename, downloadCsv } from '@/utils/csvDownload'
import { isWithinDateRange } from '@/utils/dateRange'

/**
 * The band's own ledger, ported from _old/templates/band_finances.html.
 *
 * It stays deliberately separate from the merch books: a gig fee must never
 * change a historic merch balance, and a merch reorder must never look like a
 * band expense. Only the headline figure on the balances page adds them up.
 */
const { t } = useI18n()
const { format } = useMoney()
const flash = useFlashStore()
const session = useSessionStore()

const ledger = ref<BandLedger | null>(null)
const loading = ref(true)
const busy = ref(false)
const editingId = ref<number | null>(null)
const pendingFiles = ref<File[]>([])
const financeFileInput = ref<HTMLInputElement | null>(null)
const attachmentsFor = ref<BandTransaction | null>(null)
const attachments = ref<Attachment[]>([])
const attachmentBusy = ref(false)
const failedUploadFiles = ref<File[]>([])
const dateFrom = ref('')
const dateTo = ref('')

const canManage = computed(() => session.capabilities?.can_manage_band_finances ?? false)
const canCreate = computed(() => session.capabilities?.can_create_band_finances ?? false)

const visibleEntries = computed(() =>
  (ledger.value?.entries ?? []).filter((entry) =>
    isWithinDateRange(entry.transaction_on, dateFrom.value, dateTo.value),
  ),
)

const visibleTotals = computed(() => {
  let income = 0
  let expense = 0
  let openIncome = 0
  let openExpense = 0
  const categories = new Map<string, {
    category: string
    income_cents: number
    expense_cents: number
    balance_cents: number
  }>()

  for (const entry of visibleEntries.value) {
    if (entry.is_cancelled) continue
    if (!entry.is_settled) {
      if (entry.transaction_type === 'income') openIncome += entry.amount_cents
      else openExpense += entry.amount_cents
      continue
    }
    let category = categories.get(entry.category)
    if (!category) {
      category = { category: entry.category, income_cents: 0, expense_cents: 0, balance_cents: 0 }
      categories.set(entry.category, category)
    }
    if (entry.transaction_type === 'income') {
      income += entry.amount_cents
      category.income_cents += entry.amount_cents
    } else {
      expense += entry.amount_cents
      category.expense_cents += entry.amount_cents
    }
    category.balance_cents = category.income_cents - category.expense_cents
  }

  return {
    income_cents: income,
    expense_cents: expense,
    balance_cents: income - expense,
    open_income_cents: openIncome,
    open_expense_cents: openExpense,
    categories: [...categories.values()],
  }
})

function freshForm() {
  return {
    transaction_type: 'income' as 'income' | 'expense',
    transaction_on: new Date().toISOString().slice(0, 10),
    category: 'Gage',
    description: '',
    amount: '',
    is_settled: true,
    is_asset: false,
  }
}

const form = ref(freshForm())

const categoriesForType = computed(() => {
  if (!ledger.value) return []
  return form.value.transaction_type === 'income'
    ? ledger.value.suggested_income_categories
    : ledger.value.suggested_expense_categories
})

watch(() => form.value.transaction_type, (type) => {
  const categories = categoriesForType.value
  if (!categories.includes(form.value.category)) {
    form.value.category = categories[0] ?? 'Sonstiges'
  }
  if (type === 'income') form.value.is_asset = false
})

watch(() => form.value.category, (category) => {
  if (form.value.transaction_type === 'expense') {
    form.value.is_asset = category === 'Equipment'
  }
})

const amountCents = computed(() => parseAmount(form.value.amount))
const canSubmit = computed(
  () =>
    !busy.value &&
    !!form.value.category.trim() &&
    !!form.value.description.trim() &&
    amountCents.value !== null &&
    amountCents.value > 0,
)

onMounted(load)

function exportVisible() {
  downloadCsv(
    datedFilename('bandfinanzen'),
    [
      t('common.date'),
      t('bandFinances.type'),
      t('bandFinances.category'),
      t('bandFinances.description'),
      t('bandFinances.amount'),
      t('bandFinances.status'),
    ],
    visibleEntries.value.map((entry) => [
      entry.transaction_on,
      entry.transaction_type === 'income' ? t('bandFinances.income') : t('bandFinances.expense'),
      entry.category,
      entry.description,
      `${entry.transaction_type === 'expense' ? '-' : '+'}${format(entry.amount_cents)}`,
      statusLabel(entry),
    ]),
  )
}

async function load() {  loading.value = true
  try {
    ledger.value = await reportsApi.bandLedger()
  } catch {
    flash.error(t('errors.generic'))
  } finally {
    loading.value = false
  }
}

function report(error: unknown) {
  flash.error(
    error instanceof ApiError
      ? t(`errors.${error.detailCode ?? 'generic'}`, t('errors.generic'))
      : t('errors.network'),
  )
}

function statusLabel(entry: BandTransaction) {
  if (entry.is_cancelled) return t('bandFinances.statusCancelled')
  if (entry.transaction_type === 'income') {
    return entry.is_settled ? t('bandFinances.received') : t('bandFinances.notReceived')
  }
  return entry.is_settled ? t('bandFinances.paid') : t('bandFinances.notPaid')
}

function resetForm() {
  editingId.value = null
  form.value = freshForm()
  pendingFiles.value = []
  if (financeFileInput.value) financeFileInput.value.value = ''
}

function choosePendingFiles(event: Event) {
  const input = event.target as HTMLInputElement
  pendingFiles.value.push(...Array.from(input.files ?? []))
  input.value = ''
}

function removePendingFile(index: number) {
  pendingFiles.value.splice(index, 1)
}

async function uploadFiles(transactionId: number, files: File[]) {
  const failed: File[] = []
  for (const file of files) {
    try {
      await bandFinanceAttachmentsApi.upload(transactionId, file)
    } catch {
      failed.push(file)
    }
  }
  return failed
}

function startEdit(entry: BandTransaction) {
  if (entry.is_cancelled || entry.is_settled) return
  editingId.value = entry.id
  form.value = {
    transaction_type: entry.transaction_type,
    transaction_on: entry.transaction_on,
    category: entry.category,
    description: entry.description,
    amount: (entry.amount_cents / 100).toFixed(2).replace('.', ','),
    is_settled: false,
    is_asset: entry.is_asset,
  }
}

async function submit() {
  if (!canSubmit.value || amountCents.value === null) return
  if (editingId.value !== null ? !canManage.value : !canCreate.value) return
  busy.value = true
  const payload = {
    transaction_type: form.value.transaction_type,
    transaction_on: form.value.transaction_on,
    category: form.value.category.trim(),
    description: form.value.description.trim(),
    amount_cents: amountCents.value,
    is_asset: form.value.transaction_type === 'expense' && form.value.is_asset,
  }
  try {
    if (editingId.value !== null) {
      await reportsApi.updateBandEntry(editingId.value, payload)
      flash.success(t('bandFinances.updated'))
    } else {
      const created = await reportsApi.createBandEntry({ ...payload, is_settled: form.value.is_settled })
      const files = [...pendingFiles.value]
      const failed = await uploadFiles(created.id, files)
      flash.success(t('bandFinances.saved'))
      if (failed.length) {
        failedUploadFiles.value = failed
        attachmentsFor.value = created
        flash.error(t('bandFinances.attachmentPartialFailure', { count: failed.length }))
      }
    }
    resetForm()
    await load()
    if (attachmentsFor.value) await loadFinanceAttachments()
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

async function openFinanceAttachments(entry: BandTransaction) {
  attachmentsFor.value = entry
  failedUploadFiles.value = []
  await loadFinanceAttachments()
}

async function loadFinanceAttachments() {
  if (!attachmentsFor.value) return
  try {
    attachments.value = (await bandFinanceAttachmentsApi.list(attachmentsFor.value.id)).attachments
  } catch (error) {
    report(error)
  }
}

async function addFinanceAttachments(event: Event) {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  input.value = ''
  if (!attachmentsFor.value || !files.length || attachmentBusy.value) return
  attachmentBusy.value = true
  failedUploadFiles.value = await uploadFiles(attachmentsFor.value.id, files)
  await loadFinanceAttachments()
  attachmentBusy.value = false
  if (failedUploadFiles.value.length) {
    flash.error(t('bandFinances.attachmentPartialFailure', { count: failedUploadFiles.value.length }))
  }
}

async function retryFailedUploads() {
  if (!attachmentsFor.value || !failedUploadFiles.value.length || attachmentBusy.value) return
  attachmentBusy.value = true
  failedUploadFiles.value = await uploadFiles(attachmentsFor.value.id, failedUploadFiles.value)
  await loadFinanceAttachments()
  attachmentBusy.value = false
  if (!failedUploadFiles.value.length) flash.success(t('bandFinances.attachmentsUploaded'))
}

async function removeFinanceAttachment(file: Attachment) {
  if (!attachmentsFor.value || !window.confirm(t('bandFinances.deleteAttachmentConfirm', { name: file.original_filename }))) return
  try {
    await bandFinanceAttachmentsApi.remove(attachmentsFor.value.id, file.id)
    await loadFinanceAttachments()
    await load()
  } catch (error) {
    report(error)
  }
}

async function settleEntry(entry: BandTransaction) {
  try {
    await reportsApi.settleBandEntry(entry.id)
    flash.success(
      entry.transaction_type === 'income'
        ? t('bandFinances.markedReceived')
        : t('bandFinances.markedPaid'),
    )
    if (editingId.value === entry.id) resetForm()
    await load()
  } catch (error) {
    report(error)
  }
}

async function cancelEntry(id: number) {
  try {
    await reportsApi.cancelBandEntry(id)
    flash.success(t('bandFinances.cancelled'))
    if (editingId.value === id) resetForm()
    await load()
  } catch (error) {
    report(error)
  }
}
</script>

<template>
  <main class="page-shell operational-page band-finances-page">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('bandFinances.eyebrow') }}</p>
        <h1>{{ t('bandFinances.title') }}</h1>
      </div>
      <div class="data-toolbar">
        <DateRangeFilter v-model:from="dateFrom" v-model:to="dateTo" />
      </div>
    </div>

    <p v-if="loading" class="muted">{{ t('common.loading') }}</p>

    <template v-else-if="ledger">
      <section class="metric-grid band-finance-metrics">
        <article class="metric-card income-metric">
          <span>{{ t('bandFinances.income') }}</span>
          <strong>{{ format(visibleTotals.income_cents) }}</strong>
        </article>
        <article class="metric-card expense-metric">
          <span>{{ t('bandFinances.expense') }}</span>
          <strong>{{ format(visibleTotals.expense_cents) }}</strong>
        </article>
        <article class="metric-card balance-metric">
          <span>{{ t('bandFinances.balance') }}</span>
          <strong>{{ format(visibleTotals.balance_cents) }}</strong>
        </article>
        <article class="metric-card open-metric">
          <span>{{ t('bandFinances.openIncome') }}</span>
          <strong>{{ format(visibleTotals.open_income_cents) }}</strong>
        </article>
        <article class="metric-card open-metric">
          <span>{{ t('bandFinances.openExpense') }}</span>
          <strong>{{ format(visibleTotals.open_expense_cents) }}</strong>
        </article>
      </section>

      <section v-if="canCreate" class="table-section">
        <div class="section-heading">
          <div>
            <h2>{{ editingId === null ? t('bandFinances.newEntry') : t('bandFinances.editEntry') }}</h2>
            <p>{{ editingId === null ? t('bandFinances.newEntryHint') : t('bandFinances.editEntryHint') }}</p>
          </div>
        </div>
        <form class="stack-form" @submit.prevent="submit">
          <div class="field-grid two-columns">
            <label>
              {{ t('bandFinances.type') }}
              <select v-model="form.transaction_type">
                <option value="income">{{ t('bandFinances.income') }}</option>
                <option value="expense">{{ t('bandFinances.expense') }}</option>
              </select>
            </label>
            <label>
              {{ t('common.date') }}
              <input v-model="form.transaction_on" type="date" />
            </label>
          </div>
          <div class="field-grid two-columns">
            <label>
              {{ t('bandFinances.category') }}
              <select v-model="form.category" required>
                <option v-for="entry in categoriesForType" :key="entry" :value="entry">
                  {{ entry }}
                </option>
              </select>
            </label>
            <label>
              {{ t('bandFinances.amount') }}
              <input v-model="form.amount" inputmode="decimal" placeholder="0,00" required />
            </label>
          </div>
          <label>
            {{ t('bandFinances.description') }}
            <input v-model="form.description" required />
          </label>

          <label
            v-if="form.transaction_type === 'expense'"
            class="checkbox-row settlement-checkbox asset-checkbox"
          >
            <input v-model="form.is_asset" type="checkbox" />
            <span>{{ t('bandFinances.asset') }}</span>
          </label>
          <p v-if="form.transaction_type === 'expense'" class="muted settlement-hint">
            {{ t('bandFinances.assetHint') }}
          </p>

          <label v-if="editingId === null" class="checkbox-row settlement-checkbox">
            <input v-model="form.is_settled" type="checkbox" />
            <span>
              {{ form.transaction_type === 'income'
                ? t('bandFinances.alreadyReceived')
                : t('bandFinances.alreadyPaid') }}
            </span>
          </label>
          <p
            v-if="editingId === null && !form.is_settled"
            class="muted settlement-hint"
          >
            {{ form.transaction_type === 'income'
              ? t('bandFinances.openIncomeHint')
              : t('bandFinances.openExpenseHint') }}
          </p>

          <div v-if="editingId === null" class="finance-file-picker">
            <label>
              {{ t('bandFinances.attachments') }}
              <input
                ref="financeFileInput"
                type="file"
                accept="application/pdf,image/jpeg,image/png,image/webp"
                multiple
                @change="choosePendingFiles"
              />
            </label>
            <ul v-if="pendingFiles.length" class="finance-file-list">
              <li v-for="(file, index) in pendingFiles" :key="`${file.name}-${index}`">
                <span>{{ file.name }}</span>
                <button class="compact-button" type="button" @click="removePendingFile(index)">
                  {{ t('common.delete') }}
                </button>
              </li>
            </ul>
            <p class="muted">{{ t('bandFinances.attachmentsHint') }}</p>
          </div>

          <div class="form-actions">
            <button class="primary-button" type="submit" :disabled="!canSubmit">
              {{ editingId === null ? t('common.save') : t('bandFinances.saveChanges') }}
            </button>
            <button
              v-if="editingId !== null"
              class="secondary-button"
              type="button"
              @click="resetForm"
            >
              {{ t('common.cancel') }}
            </button>
          </div>
        </form>
      </section>

      <RecurringBandFinances
        v-if="canManage"
        :income-categories="ledger.suggested_income_categories"
        :expense-categories="ledger.suggested_expense_categories"
        @changed="load"
      />

      <section v-if="visibleTotals.categories.length" class="table-section">
        <div class="section-heading"><div><h2>{{ t('bandFinances.byCategory') }}</h2></div></div>
        <div class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{{ t('bandFinances.category') }}</th>
                <th class="numeric">{{ t('bandFinances.income') }}</th>
                <th class="numeric">{{ t('bandFinances.expense') }}</th>
                <th class="numeric">{{ t('bandFinances.balance') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="entry in visibleTotals.categories" :key="entry.category">
                <td>{{ entry.category }}</td>
                <td class="numeric">{{ format(entry.income_cents) }}</td>
                <td class="numeric">{{ format(entry.expense_cents) }}</td>
                <td class="numeric">{{ format(entry.balance_cents) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="table-section">
        <div class="section-heading ledger-heading">
          <div>
            <h2>{{ t('bandFinances.allEntries') }}</h2>
            <p>{{ t('bandFinances.entryCount', { count: visibleEntries.length }) }}</p>
          </div>
          <button class="secondary-button" type="button" @click="exportVisible">
            CSV
          </button>
        </div>
        <p v-if="!visibleEntries.length" class="muted">{{ t('bandFinances.empty') }}</p>
        <div v-else class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{{ t('common.date') }}</th>
                <th>{{ t('bandFinances.category') }}</th>
                <th>{{ t('bandFinances.description') }}</th>
                <th class="numeric">{{ t('bandFinances.amount') }}</th>
                <th>{{ t('bandFinances.status') }}</th>
                <th v-if="canManage"></th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="entry in visibleEntries"
                :key="entry.id"
                :class="{
                  'cancelled-row': entry.is_cancelled,
                  'unsettled-row': !entry.is_cancelled && !entry.is_settled,
                }"
              >
                <td>{{ entry.transaction_on }}</td>
                <td>
                  {{ entry.category }}
                  <small v-if="entry.is_asset" class="asset-label">{{ t('bandFinances.assetShort') }}</small>
                </td>
                <td>
                  {{ entry.description }}
                  <button
                    class="attachment-button"
                    type="button"
                    :title="t('bandFinances.manageAttachments')"
                    :aria-label="t('bandFinances.attachmentCount', { count: entry.attachments?.length ?? 0 })"
                    @click="openFinanceAttachments(entry)"
                  >📎 <span>{{ entry.attachments?.length ?? 0 }}</span></button>
                </td>
                <td class="numeric" :class="entry.transaction_type">
                  {{ entry.transaction_type === 'expense' ? '−' : '+' }}{{ format(entry.amount_cents) }}
                </td>
                <td>
                  <span
                    class="settlement-pill"
                    :class="{ open: !entry.is_settled && !entry.is_cancelled }"
                  >
                    {{ statusLabel(entry) }}
                  </span>
                </td>
                <td v-if="canManage">
                  <div v-if="!entry.is_cancelled" class="entry-actions">
                    <template v-if="!entry.is_settled">
                      <button
                        class="compact-button secondary-button"
                        type="button"
                        @click="startEdit(entry)"
                      >
                        {{ t('bandFinances.edit') }}
                      </button>
                      <button
                        class="compact-button primary-button"
                        type="button"
                        @click="settleEntry(entry)"
                      >
                        {{ entry.transaction_type === 'income'
                          ? t('bandFinances.markReceived')
                          : t('bandFinances.markPaid') }}
                      </button>
                    </template>
                    <button
                      class="compact-button danger-button"
                      type="button"
                      @click="cancelEntry(entry.id)"
                    >
                      {{ t('bandFinances.cancel') }}
                    </button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>

    <AppDialog v-if="attachmentsFor" :label="t('bandFinances.manageAttachments')" @close="attachmentsFor = null">
      <div class="stack-form">
        <div>
          <p class="eyebrow">{{ attachmentsFor.description }}</p>
          <h2>{{ t('bandFinances.attachments') }}</h2>
          <p class="muted">{{ t('bandFinances.attachmentsHint') }}</p>
        </div>
        <p v-if="!attachments.length" class="muted">{{ t('bandFinances.noAttachments') }}</p>
        <ul v-else class="finance-file-list">
          <li v-for="file in attachments" :key="file.id">
            <a :href="bandFinanceAttachmentsApi.fileUrl(attachmentsFor.id, file.id)">{{ file.original_filename }}</a>
            <button class="compact-button danger-button" type="button" @click="removeFinanceAttachment(file)">
              {{ t('common.delete') }}
            </button>
          </li>
        </ul>
        <div v-if="failedUploadFiles.length" class="notice error">
          <p>{{ t('bandFinances.attachmentPartialFailure', { count: failedUploadFiles.length }) }}</p>
          <button class="secondary-button" type="button" :disabled="attachmentBusy" @click="retryFailedUploads">
            {{ t('common.retry') }}
          </button>
        </div>
        <label>
          {{ t('bandFinances.addAttachments') }}
          <input
            type="file"
            accept="application/pdf,image/jpeg,image/png,image/webp"
            multiple
            :disabled="attachmentBusy"
            @change="addFinanceAttachments"
          />
        </label>
        <div class="form-actions">
          <button class="primary-button" type="button" @click="attachmentsFor = null">{{ t('common.close') }}</button>
        </div>
      </div>
    </AppDialog>
  </main>
</template>

<style scoped>
.numeric {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.numeric.income {
  color: var(--success);
}

.numeric.expense {
  color: var(--danger);
}

.open-metric strong {
  color: var(--warning-text);
}

.income-metric strong { color: var(--success-text); }
.expense-metric strong { color: var(--danger-text); }
.balance-metric { border-color: var(--border-default); background: var(--surface-raised); }

.unsettled-row {
  background: color-mix(in srgb, var(--warning) 12%, transparent);
  box-shadow: inset 4px 0 var(--warning);
}

.settlement-pill {
  display: inline-flex;
  padding: 4px 8px;
  border-radius: 999px;
  background: var(--input-bg);
  font-size: .78rem;
  font-weight: 700;
  white-space: nowrap;
}

.settlement-pill.open {
  color: var(--warning);
  border: 1px solid color-mix(in srgb, var(--warning) 55%, var(--border));
}

.entry-actions,
.form-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 7px;
}

.finance-file-picker {
  display: grid;
  gap: 8px;
  padding: 12px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-control);
  background: var(--surface-muted);
}

.finance-file-list {
  display: grid;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.finance-file-list li {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  overflow-wrap: anywhere;
}

.attachment-button {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  min-width: 44px;
  min-height: 34px;
  margin-left: 8px;
  padding: 4px 8px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-small);
  background: var(--surface-muted);
  color: var(--text-secondary);
}

.stack-form .settlement-checkbox {
  display: inline-flex;
  flex-direction: row;
  flex-wrap: nowrap;
  align-items: center;
  align-self: flex-start;
  gap: 10px;
  width: fit-content;
  max-width: 100%;
  margin: 4px 0 0;
  padding: 9px 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius-control);
  color: var(--text);
  background: color-mix(in srgb, var(--panel-raised) 82%, transparent);
  cursor: pointer;
}

.stack-form .settlement-checkbox input[type='checkbox'] {
  width: 18px;
  height: 18px;
  min-width: 18px;
  margin: 0;
  padding: 0;
  flex: 0 0 auto;
  accent-color: var(--accent);
}

.stack-form .settlement-checkbox span {
  line-height: 1.35;
}

.settlement-hint {
  max-width: 720px;
  margin: -2px 0 2px 12px;
  line-height: 1.4;
}
</style>
