<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { attachmentsApi, catalogueApi, purchasesApi, salesApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { Article, Attachment, Purchase, Variant } from '@/api/types'
import { useMoney, parseAmount } from '@/composables/useMoney'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'

/**
 * Goods receipts, ported from _old/templates/purchases.html.
 *
 * Unlike a sale, a purchase may be corrected and deleted: it is the band's own
 * bookkeeping of what they ordered, and leaving a mistyped position behind
 * would distort the stock they rely on at the next gig.
 */
const { t } = useI18n()
const { format } = useMoney()
const flash = useFlashStore()
const session = useSessionStore()

const articles = ref<Article[]>([])
const purchases = ref<Purchase[]>([])
const loading = ref(true)
const busy = ref(false)
const filter = ref('')

const canManage = computed(() => session.capabilities?.can_manage_purchases ?? false)

const receiptId = ref('')
const purchasedOn = ref(new Date().toISOString().slice(0, 10))
const supplier = ref('')
const invoiceReference = ref('')
const receiptInvoice = ref<File | null>(null)
const receiptInvoiceInput = ref<HTMLInputElement | null>(null)

const selectedArticleId = ref<number | null>(null)
const chosenValues = ref<Record<number, number>>({})
const quantity = ref(1)
const unitCostInput = ref('')

interface CartLine {
  variantId: number
  label: string
  quantity: number
  unitCostCents: number
}
const cart = ref<CartLine[]>([])

const selectedArticle = computed(
  () => articles.value.find((article) => article.id === selectedArticleId.value) ?? null,
)

/** Purchases may target a withdrawn variant: restocking something that left
 *  the assortment is legitimate bookkeeping, so nothing is filtered here. */
const optionGroups = computed(() =>
  (selectedArticle.value?.option_groups ?? [])
    .filter((group) => group.is_active)
    .map((group) => ({ ...group, values: group.values.filter((value) => value.is_active) })),
)

const selectedVariant = computed<Variant | null>(() => {
  const article = selectedArticle.value
  if (!article) return null
  const chosen = optionGroups.value.map((group) => chosenValues.value[group.id])
  if (chosen.some((value) => value === undefined)) return null
  const wanted = [...chosen].sort((a, b) => a - b).join('|')
  return article.variants.find((variant) => variant.combination_key === wanted) ?? null
})

const variantLabel = computed(() => {
  const article = selectedArticle.value
  if (!article || !selectedVariant.value) return ''
  const parts = optionGroups.value.map((group) => {
    const value = group.values.find((entry) => entry.id === chosenValues.value[group.id])
    return `${group.name}: ${value?.value ?? '—'}`
  })
  return parts.length ? `${article.name} — ${parts.join(' · ')}` : article.name
})

const cartTotalCents = computed(() =>
  cart.value.reduce((sum, line) => sum + line.quantity * line.unitCostCents, 0),
)

interface PurchaseReceipt {
  receiptId: string
  purchasedOn: string
  supplier: string
  invoiceReference: string
  positions: Purchase[]
  totalCostCents: number
}

const visibleReceipts = computed(() => {
  const receipts: PurchaseReceipt[] = []
  const known = new Map<string, PurchaseReceipt>()
  for (const purchase of purchases.value) {
    let receipt = known.get(purchase.receipt_id)
    if (!receipt) {
      receipt = {
        receiptId: purchase.receipt_id,
        purchasedOn: purchase.purchased_on,
        supplier: purchase.supplier,
        invoiceReference: purchase.invoice_reference,
        positions: [],
        totalCostCents: 0,
      }
      known.set(purchase.receipt_id, receipt)
      receipts.push(receipt)
    }
    receipt.positions.push(purchase)
    receipt.totalCostCents += purchase.total_cost_cents
  }
  const needle = filter.value.trim().toLowerCase()
  if (!needle) return receipts
  return receipts.filter((receipt) => {
    const positions = receipt.positions
      .map((purchase) => `${purchase.article_name} ${purchase.variant_label} ${purchase.comment}`)
      .join(' ')
    return `${receipt.receiptId} ${receipt.supplier} ${receipt.invoiceReference} ${positions}`
      .toLowerCase()
      .includes(needle)
  })
})

onMounted(async () => {
  await Promise.all([loadArticles(), loadPurchases(), refreshPreview()])
  loading.value = false
})

async function loadArticles() {
  try {
    articles.value = (await catalogueApi.list()).articles
  } catch {
    flash.error(t('errors.generic'))
  }
}

async function loadPurchases() {
  try {
    purchases.value = (await purchasesApi.list()).purchases
  } catch {
    flash.error(t('errors.generic'))
  }
}

async function refreshPreview() {
  try {
    receiptId.value = (await salesApi.receiptPreview('purchase')).receipt_id
  } catch {
    receiptId.value = ''
  }
}

function selectArticle(article: Article) {
  selectedArticleId.value = article.id
  chosenValues.value = {}
  for (const group of article.option_groups.filter((entry) => entry.is_active)) {
    const first = group.values.find((value) => value.is_active)
    if (first) chosenValues.value[group.id] = first.id
  }
}

// The last price paid for a variant is pre-filled, so a reorder needs no
// retyping — but it stays editable, because suppliers change their prices.
watch(selectedVariant, async (variant) => {
  if (!variant) return
  try {
    const last = await purchasesApi.lastCost(variant.id)
    const cents = last.found ? last.unit_cost_cents : variant.default_purchase_price_cents
    unitCostInput.value = (cents / 100).toFixed(2).replace('.', ',')
  } catch {
    unitCostInput.value = (variant.default_purchase_price_cents / 100).toFixed(2).replace('.', ',')
  }
})

/**
 * The stepper is the only way to change the amount on a tablet, so it must
 * never leave the field in a state the basket cannot use: clearing a number
 * input yields NaN, which would silently disable the confirm button.
 */
function stepQuantity(delta: number) {
  const current = Number.isFinite(quantity.value) ? quantity.value : 1
  quantity.value = Math.max(1, current + delta)
}

function normalizeQuantity() {
  if (!Number.isFinite(quantity.value) || quantity.value < 1) quantity.value = 1
}

function addToCart() {
  const variant = selectedVariant.value
  const cost = parseAmount(unitCostInput.value)
  if (!variant || cost === null || cost < 0 || quantity.value <= 0) return

  cart.value.push({
    variantId: variant.id,
    label: variantLabel.value,
    quantity: quantity.value,
    unitCostCents: cost,
  })
  // The quantity carries over: ordering ten of every size is the common case.
}

function removeLine(index: number) {
  cart.value.splice(index, 1)
}

function report(error: unknown) {
  flash.error(
    error instanceof ApiError
      ? t(`errors.${error.detailCode ?? 'generic'}`, t('errors.generic'))
      : t('errors.network'),
  )
}

async function book() {
  if (!cart.value.length || busy.value) return
  busy.value = true
  try {
    const result = await purchasesApi.create({
      items: cart.value.map((line) => ({
        variant_id: line.variantId,
        quantity: line.quantity,
        unit_cost_cents: line.unitCostCents,
      })),
      purchased_on: purchasedOn.value,
      supplier: supplier.value.trim(),
      invoice_reference: invoiceReference.value.trim(),
      receipt_id: receiptId.value,
    })
    flash.success(t('purchases.booked', { receipt: result.receipt_id }))
    if (receiptInvoice.value) {
      try {
        await attachmentsApi.upload(result.receipt_id, receiptInvoice.value)
      } catch {
        flash.error(t('purchases.invoiceUploadAfterBookingFailed', { receipt: result.receipt_id }))
      }
    }
    cart.value = []
    supplier.value = ''
    invoiceReference.value = ''
    receiptInvoice.value = null
    if (receiptInvoiceInput.value) receiptInvoiceInput.value.value = ''
    await Promise.all([loadArticles(), loadPurchases(), refreshPreview()])
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

/**
 * Invoices and receipt attachments.
 *
 * Receipt attachments belong to the whole basket rather than to one position.
 */
const fileInput = ref<HTMLInputElement | null>(null)
const attachmentsFor = ref<Purchase | null>(null)
const attachments = ref<Attachment[]>([])

function pickAttachment() {
  fileInput.value?.click()
}

async function onFileChosen(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  // Cleared straight away so picking the same file twice still fires a change.
  input.value = ''
  if (!file || !attachmentsFor.value) return

  try {
    await attachmentsApi.upload(attachmentsFor.value.receipt_id, file)
    flash.success(t('purchases.invoiceUploaded'))
    await loadAttachments()
  } catch (error) {
    report(error)
  }
}

async function openAttachments(purchase: Purchase) {
  attachmentsFor.value = purchase
  attachments.value = []
  await loadAttachments()
}

async function loadAttachments() {
  if (!attachmentsFor.value) return
  try {
    attachments.value = (await attachmentsApi.list(attachmentsFor.value.receipt_id)).attachments
  } catch (error) {
    report(error)
  }
}

async function removeAttachment(file: Attachment) {
  if (!attachmentsFor.value) return
  try {
    await attachmentsApi.remove(attachmentsFor.value.receipt_id, file.id)
    await loadAttachments()
  } catch (error) {
    report(error)
  }
}

function formatBytes(bytes: number): string {
  if (bytes <= 0) return '—'
  const units = ['B', 'kB', 'MB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}

async function removeReceipt(receipt: PurchaseReceipt) {
  if (!window.confirm(t('purchases.deleteReceiptConfirm', { receipt: receipt.receiptId }))) return
  try {
    await purchasesApi.removeReceipt(receipt.receiptId)
    flash.success(t('purchases.receiptRemoved'))
    await Promise.all([loadArticles(), loadPurchases()])
  } catch (error) {
    report(error)
  }
}
</script>

<template>
  <main class="page-shell transaction-page">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('purchases.eyebrow') }}</p>
        <h1>{{ t('purchases.title') }}</h1>
      </div>
      <div class="receipt-preview">
        <span>{{ t('sales.receiptId') }}</span>
        <strong>{{ receiptId || t('sales.receiptLoading') }}</strong>
      </div>
    </div>

    <section v-if="canManage" class="transaction-layout">
      <aside class="selection-panel product-panel">
        <h2>{{ t('sales.articles') }}</h2>
        <p class="panel-hint">{{ t('sales.articlesHint') }}</p>
        <div class="button-list">
          <button
            v-for="article in articles"
            :key="article.id"
            type="button"
            class="selection-button"
            :class="{ active: article.id === selectedArticleId }"
            @click="selectArticle(article)"
          >
            <span>{{ article.name }}</span>
            <small>{{ t('sales.inStock', { count: article.total_stock }) }}</small>
          </button>
          <p v-if="!loading && !articles.length" class="muted">{{ t('sales.noArticles') }}</p>
        </div>
      </aside>

      <section class="selection-panel option-panel">
        <h2>{{ t('sales.options') }}</h2>
        <div class="option-groups">
          <div v-if="!selectedArticle" class="empty-selection">{{ t('sales.pickArticle') }}</div>
          <div v-for="group in optionGroups" :key="group.id" class="option-group">
            <h3>{{ group.name }}</h3>
            <div class="option-choices">
              <button
                v-for="value in group.values"
                :key="value.id"
                type="button"
                class="option-choice"
                :class="{ selected: chosenValues[group.id] === value.id }"
                @click="chosenValues = { ...chosenValues, [group.id]: value.id }"
              >
                {{ value.value }}
              </button>
            </div>
          </div>
        </div>
        <div v-if="selectedVariant" class="selected-variant-card">
          <span>{{ t('sales.selectedVariant') }}</span>
          <strong>{{ variantLabel }}</strong>
          <small>{{ t('sales.inStock', { count: selectedVariant.on_hand }) }}</small>
        </div>
      </section>

      <section class="selection-panel sale-details">
        <label>
          {{ t('purchases.unitCost') }}
          <input v-model="unitCostInput" inputmode="decimal" :disabled="!selectedVariant" />
        </label>

        <div class="quantity-and-total">
          <label class="quantity-control">
            {{ t('common.quantity') }}
            <span class="stepper">
              <button type="button" :aria-label="t('common.decrease')" @click="stepQuantity(-1)">−</button>
              <input
                v-model.number="quantity"
                type="number"
                min="1"
                inputmode="numeric"
                @blur="normalizeQuantity"
              />
              <button type="button" :aria-label="t('common.increase')" @click="stepQuantity(1)">+</button>
            </span>
          </label>
          <div class="total-box">
            <span>{{ t('purchases.receiptTotal') }}</span>
            <strong>{{ format(cartTotalCents) }}</strong>
          </div>
        </div>

        <button
          class="secondary-button full-width"
          type="button"
          :disabled="!selectedVariant"
          @click="addToCart"
        >
          {{ t('purchases.addPosition') }}
        </button>

        <section class="cart-section">
          <div class="cart-heading">
            <h3>{{ t('purchases.positions') }}</h3>
            <span class="muted">{{ t('sales.cartCount', { count: cart.length }) }}</span>
          </div>
          <div class="cart-items">
            <p v-if="!cart.length" class="muted">{{ t('purchases.noPositions') }}</p>
            <div v-for="(line, index) in cart" :key="index" class="cart-item">
              <span>
                <strong>{{ line.label }}</strong>
                <small>{{ line.quantity }} × {{ format(line.unitCostCents) }}</small>
              </span>
              <b>{{ format(line.quantity * line.unitCostCents) }}</b>
              <button class="icon-button" type="button" @click="removeLine(index)">×</button>
            </div>
          </div>
        </section>

        <div class="field-grid two-columns">
          <label>{{ t('common.date') }}<input v-model="purchasedOn" type="date" /></label>
          <label>{{ t('purchases.supplier') }}<input v-model="supplier" /></label>
        </div>
        <label>{{ t('purchases.invoiceReference') }}<input v-model="invoiceReference" /></label>
        <label>
          {{ t('purchases.invoiceFile') }}
          <input
            ref="receiptInvoiceInput"
            type="file"
            accept=".pdf,.jpg,.jpeg,.png,application/pdf,image/jpeg,image/png"
            @change="receiptInvoice = ($event.target as HTMLInputElement).files?.[0] ?? null"
          />
          <small class="muted">{{ t('purchases.invoiceFileHint') }}</small>
        </label>

        <button
          class="primary-button full-width large-button"
          type="button"
          :disabled="!cart.length || busy"
          @click="book"
        >
          {{ t('purchases.book') }}
        </button>
      </section>
    </section>

    <section class="table-section">
      <div class="section-heading ledger-heading">
        <div>
          <h2>{{ t('purchases.history') }}</h2>
          <p>{{ t('purchases.historyHint') }}</p>
        </div>
        <label class="table-filter">
          {{ t('common.filter') }}
          <input v-model="filter" type="search" />
        </label>
      </div>

      <p v-if="!visibleReceipts.length" class="muted">{{ t('purchases.empty') }}</p>
      <div v-else class="purchase-receipt-list">
        <details v-for="receipt in visibleReceipts" :key="receipt.receiptId" class="purchase-receipt-card">
          <summary>
            <span class="receipt-summary-main">
              <code>{{ receipt.receiptId }}</code>
              <small>{{ receipt.purchasedOn }} · {{ receipt.supplier || t('purchases.noSupplier') }}</small>
            </span>
            <span>{{ t('purchases.positionCount', { count: receipt.positions.length }) }}</span>
            <strong>{{ format(receipt.totalCostCents) }}</strong>
            <span class="receipt-chevron" aria-hidden="true">⌄</span>
          </summary>
          <div class="receipt-details">
            <div class="receipt-meta">
              <span><b>{{ t('purchases.supplier') }}:</b> {{ receipt.supplier || '—' }}</span>
              <span><b>{{ t('purchases.invoiceReference') }}:</b> {{ receipt.invoiceReference || '—' }}</span>
            </div>
            <div class="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>{{ t('sales.articles') }}</th>
                    <th class="numeric">{{ t('common.quantity') }}</th>
                    <th class="numeric">{{ t('purchases.unitCost') }}</th>
                    <th class="numeric">{{ t('purchases.total') }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="purchase in receipt.positions" :key="purchase.id">
                    <td>
                      <strong>{{ purchase.article_name }}</strong>
                      <small>{{ purchase.variant_label }}</small>
                      <a v-if="purchase.has_invoice_file" :href="attachmentsApi.invoiceUrl(purchase.id)">
                        {{ t('purchases.legacyInvoice') }}
                      </a>
                    </td>
                    <td class="numeric">{{ purchase.quantity }}</td>
                    <td class="numeric">{{ format(purchase.unit_cost_cents) }}</td>
                    <td class="numeric">{{ format(purchase.total_cost_cents) }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div class="receipt-actions">
              <button class="secondary-button" type="button" @click="openAttachments(receipt.positions[0])">
                {{ t('purchases.invoiceAndAttachments') }}
              </button>
              <button v-if="canManage" class="secondary-button danger-button" type="button" @click="removeReceipt(receipt)">
                {{ t('purchases.deleteReceipt') }}
              </button>
            </div>
          </div>
        </details>
      </div>
    </section>

    <input
      ref="fileInput"
      type="file"
      accept=".pdf,.jpg,.jpeg,.png,application/pdf,image/jpeg,image/png"
      hidden
      @change="onFileChosen"
    />

    <dialog v-if="attachmentsFor" class="confirmation-dialog" open>
      <div class="stack-form">
        <div>
          <p class="eyebrow"><code>{{ attachmentsFor.receipt_id }}</code></p>
          <h2>{{ t('purchases.invoiceAndAttachments') }}</h2>
          <p class="muted">{{ t('purchases.attachmentsHint') }}</p>
        </div>

        <p v-if="!attachments.length" class="muted">{{ t('purchases.noAttachments') }}</p>
        <ul v-else class="attachment-file-list">
          <li v-for="file in attachments" :key="file.id" class="attachment-file-row">
            <a :href="attachmentsApi.fileUrl(attachmentsFor.receipt_id, file.id)">
              {{ file.original_filename }}
            </a>
            <span class="muted">{{ formatBytes(file.size_bytes) }}</span>
            <button
              v-if="canManage"
              class="compact-button danger-button"
              type="button"
              @click="removeAttachment(file)"
            >{{ t('common.delete') }}</button>
          </li>
        </ul>

        <div class="dialog-actions">
          <button v-if="canManage" class="secondary-button" type="button" @click="pickAttachment">
            {{ t('purchases.addAttachment') }}
          </button>
          <button class="primary-button" type="button" @click="attachmentsFor = null">
            {{ t('common.close') }}
          </button>
        </div>
      </div>
    </dialog>
  </main>
</template>

<style scoped>
.purchase-receipt-list {
  display: grid;
  gap: 12px;
}

.purchase-receipt-card {
  overflow: hidden;
  border: 1px solid var(--border);
  border-radius: 14px;
  background: color-mix(in srgb, var(--surface) 92%, transparent);
}

.purchase-receipt-card summary {
  display: grid;
  grid-template-columns: minmax(180px, 1fr) auto auto auto;
  gap: 20px;
  align-items: center;
  padding: 16px 18px;
  cursor: pointer;
  list-style: none;
}

.purchase-receipt-card summary::-webkit-details-marker {
  display: none;
}

.receipt-summary-main {
  display: grid;
  gap: 4px;
}

.receipt-summary-main code {
  width: fit-content;
}

.receipt-summary-main small {
  color: var(--muted);
}

.receipt-chevron {
  font-size: 1.4rem;
  transition: transform 160ms ease;
}

.purchase-receipt-card[open] .receipt-chevron {
  transform: rotate(180deg);
}

.receipt-details {
  padding: 0 18px 18px;
  border-top: 1px solid var(--border);
}

.receipt-meta,
.receipt-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px 24px;
  padding: 14px 0;
}

.receipt-actions {
  justify-content: flex-end;
  padding-bottom: 0;
}

.attachment-actions,
.purchase-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 6px;
}

.attachment-file-list {
  display: grid;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.attachment-file-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto auto;
  gap: 10px;
  align-items: center;
}

.attachment-file-row a {
  min-width: 0;
  overflow-wrap: anywhere;
}

.numeric {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.option-group {
  margin-bottom: 18px;
}

.option-group h3 {
  margin: 0 0 8px;
  font-size: 0.92rem;
}

.option-choices {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.cart-item {
  display: grid;
  grid-template-columns: 1fr auto auto;
  align-items: center;
  gap: 10px;
  padding: 10px 0;
  border-bottom: 1px solid var(--border);
}

.cart-item span {
  display: grid;
  gap: 2px;
}

.cart-item small {
  color: var(--muted);
}

td small {
  display: block;
  color: var(--muted);
}

td a {
  display: block;
  margin-top: 4px;
  font-size: 0.82rem;
}

@media (max-width: 700px) {
  .purchase-receipt-card summary {
    grid-template-columns: 1fr auto;
    gap: 8px 12px;
  }

  .receipt-chevron {
    grid-column: 2;
    grid-row: 1;
  }
}
</style>
