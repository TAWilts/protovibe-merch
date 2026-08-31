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

const visiblePurchases = computed(() => {
  const needle = filter.value.trim().toLowerCase()
  if (!needle) return purchases.value
  return purchases.value.filter((purchase) =>
    `${purchase.receipt_id} ${purchase.article_name} ${purchase.variant_label} ` +
    `${purchase.supplier} ${purchase.invoice_reference} ${purchase.comment}`
      .toLowerCase()
      .includes(needle),
  )
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
    cart.value = []
    supplier.value = ''
    invoiceReference.value = ''
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
 * One hidden file input serves every button in the table; `pending` records
 * what the next chosen file is for, so the same element can back both the
 * per-purchase invoice and the per-receipt attachments.
 */
const fileInput = ref<HTMLInputElement | null>(null)
const pending = ref<{ kind: 'invoice'; purchase: Purchase } | { kind: 'attachment' } | null>(null)
const attachmentsFor = ref<Purchase | null>(null)
const attachments = ref<Attachment[]>([])

function pickInvoice(purchase: Purchase) {
  pending.value = { kind: 'invoice', purchase }
  fileInput.value?.click()
}

function pickAttachment() {
  pending.value = { kind: 'attachment' }
  fileInput.value?.click()
}

async function onFileChosen(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  const target = pending.value
  // Cleared straight away so picking the same file twice still fires a change.
  input.value = ''
  pending.value = null
  if (!file || !target) return

  try {
    if (target.kind === 'invoice') {
      await attachmentsApi.uploadInvoice(target.purchase.id, file)
      flash.success(t('purchases.invoiceUploaded'))
      await loadPurchases()
    } else if (attachmentsFor.value) {
      await attachmentsApi.upload(attachmentsFor.value.receipt_id, file)
      await loadAttachments()
    }
  } catch (error) {
    report(error)
  }
}

async function removeInvoice(purchase: Purchase) {
  try {
    await attachmentsApi.removeInvoice(purchase.id)
    await loadPurchases()
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

async function remove(purchase: Purchase) {
  try {
    await purchasesApi.remove(purchase.id)
    flash.success(t('purchases.removed'))
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

      <p v-if="!visiblePurchases.length" class="muted">{{ t('purchases.empty') }}</p>
      <div v-else class="table-scroll">
        <table>
          <thead>
            <tr>
              <th>{{ t('history.receipt') }}</th>
              <th>{{ t('sales.articles') }}</th>
              <th>{{ t('common.date') }}</th>
              <th class="numeric">{{ t('common.quantity') }}</th>
              <th class="numeric">{{ t('purchases.unitCost') }}</th>
              <th class="numeric">{{ t('purchases.total') }}</th>
              <th>{{ t('purchases.supplier') }}</th>
              <th>{{ t('purchases.invoice') }}</th>
              <th v-if="canManage"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="purchase in visiblePurchases" :key="purchase.id">
              <td><code>{{ purchase.receipt_id }}</code></td>
              <td>
                <strong>{{ purchase.article_name }}</strong>
                <small>{{ purchase.variant_label }}</small>
              </td>
              <td>{{ purchase.purchased_on }}</td>
              <td class="numeric">{{ purchase.quantity }}</td>
              <td class="numeric">{{ format(purchase.unit_cost_cents) }}</td>
              <td class="numeric">{{ format(purchase.total_cost_cents) }}</td>
              <td>{{ purchase.supplier || '—' }}</td>
              <td>
                <a
                  v-if="purchase.has_invoice_file"
                  :href="attachmentsApi.invoiceUrl(purchase.id)"
                >{{ t('purchases.download') }}</a>
                <span v-else>{{ purchase.invoice_reference || '—' }}</span>
                <div v-if="canManage" class="attachment-actions">
                  <button class="compact-button" type="button" @click="pickInvoice(purchase)">
                    {{ purchase.has_invoice_file ? t('purchases.replaceInvoice') : t('purchases.uploadInvoice') }}
                  </button>
                  <button
                    v-if="purchase.has_invoice_file"
                    class="compact-button danger-button"
                    type="button"
                    @click="removeInvoice(purchase)"
                  >{{ t('purchases.removeInvoice') }}</button>
                </div>
              </td>
              <td v-if="canManage" class="purchase-actions">
                <button class="compact-button" type="button" @click="openAttachments(purchase)">
                  {{ t('purchases.attachments') }}
                </button>
                <button class="compact-button danger-button" type="button" @click="remove(purchase)">
                  {{ t('common.delete') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- One hidden input serves every upload button in the table. -->
    <input ref="fileInput" type="file" hidden @change="onFileChosen" />

    <dialog v-if="attachmentsFor" class="confirmation-dialog" open>
      <div class="stack-form">
        <div>
          <p class="eyebrow"><code>{{ attachmentsFor.receipt_id }}</code></p>
          <h2>{{ t('purchases.attachments') }}</h2>
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
</style>
