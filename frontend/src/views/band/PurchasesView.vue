<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { attachmentsApi, catalogueApi, purchasesApi, salesApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { Article, Attachment, Purchase, RefillSuggestion, Variant } from '@/api/types'
import DateRangeFilter from '@/components/DateRangeFilter.vue'
import AppDialog from '@/components/ui/AppDialog.vue'
import AppToggle from '@/components/ui/AppToggle.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import TableSkeleton from '@/components/ui/TableSkeleton.vue'
import { useMoney, parseAmount } from '@/composables/useMoney'
import { usePendingChangesGuard } from '@/composables/usePendingChangesGuard'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'
import { datedFilename, downloadCsv } from '@/utils/csvDownload'
import { isWithinDateRange } from '@/utils/dateRange'

/**
 * Goods receipts, ported from _old/templates/purchases.html.
 *
 * Booked purchases stay in the audit trail. A cancellation reverses their
 * stock/finance effect without deleting the receipt or its attachments.
 */
const { t } = useI18n()
const { format } = useMoney()
const flash = useFlashStore()
const session = useSessionStore()

const articles = ref<Article[]>([])
const purchases = ref<Purchase[]>([])
const loading = ref(true)
const purchasesLoadFailed = ref(false)
const busy = ref(false)
const filter = ref('')
const dateFrom = ref('')
const dateTo = ref('')

const canManage = computed(() => session.capabilities?.can_manage_purchases ?? false)
const purchaseEditingEnabled = ref(false)

const receiptId = ref('')
const purchasedOn = ref(new Date().toISOString().slice(0, 10))
const supplier = ref('')
const invoiceReference = ref('')
const receiptInvoices = ref<File[]>([])
const receiptInvoiceInput = ref<HTMLInputElement | null>(null)

const selectedArticleId = ref<number | null>(null)
const chosenValues = ref<Record<number, number>>({})
const quantity = ref(1)
const unitCostInput = ref('')
const priceMode = ref<'unit' | 'basket'>('unit')
const basketPriceInput = ref('')
const pricesIncludeVat = ref(true)
const vatRateInput = ref('19')
const shippingCostInput = ref('0,00')
const rememberedArticleCost = ref<Record<number, string>>({})

interface CartLine {
  articleId: number
  variantId: number
  label: string
  quantity: number
  unitCostCents: number
}
const cart = ref<CartLine[]>([])
usePendingChangesGuard(() => cart.value.length > 0, () => t('purchases.unfinishedLeave'))

const purchasableArticles = computed(() =>
  articles.value.filter((article) => article.variants.some((variant) => !variant.no_reorder)),
)

const selectedArticle = computed(
	() => purchasableArticles.value.find((article) => article.id === selectedArticleId.value) ?? null,
)

/** Withdrawn merchandise remains bookable for historical corrections, but an
 * explicit no-reorder decision removes that exact combination from the picker. */
const purchasableVariants = computed(() =>
  selectedArticle.value?.variants.filter((variant) => !variant.no_reorder) ?? [],
)
const optionGroups = computed(() => {
  const used = new Set(purchasableVariants.value.flatMap((variant) => variant.option_value_ids))
  return (selectedArticle.value?.option_groups ?? [])
    .filter((group) => group.is_active)
    .map((group) => ({
      ...group,
      values: (group.values ?? []).filter((value) => value.is_active && used.has(value.id)),
    }))
    .filter((group) => group.values.length > 0)
})

const selectedVariant = computed<Variant | null>(() => {
  const article = selectedArticle.value
  if (!article) return null
  const chosen = optionGroups.value.map((group) => chosenValues.value[group.id])
  if (chosen.some((value) => value === undefined)) return null
  const wanted = [...chosen].sort((a, b) => a - b).join('|')
  return purchasableVariants.value.find((variant) => variant.combination_key === wanted) ?? null
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

function parseVatRate(raw: string): number | null {
  const normalized = raw.trim().replace(',', '.')
  if (!/^\d{1,3}(?:\.\d{1,2})?$/.test(normalized)) return null
  const percent = Number(normalized)
  if (!Number.isFinite(percent) || percent < 0 || percent > 100) return null
  return Math.round(percent * 100)
}

function grossFromEntered(cents: number, includesVat: boolean, vatBasisPoints: number) {
  return includesVat ? cents : Math.round(cents * (10000 + vatBasisPoints) / 10000)
}

function netFromGross(cents: number, vatBasisPoints: number) {
  if (vatBasisPoints <= 0) return cents
  return Math.round(cents * 10000 / (10000 + vatBasisPoints))
}

const vatRateBasisPoints = computed(() => parseVatRate(vatRateInput.value) ?? 1900)
const shippingEnteredCents = computed(() => parseAmount(shippingCostInput.value))
const basketEnteredCents = computed(() => parseAmount(basketPriceInput.value))
const goodsEnteredCents = computed(() => priceMode.value === 'basket'
  ? basketEnteredCents.value ?? 0
  : cart.value.reduce((sum, line) => sum + line.quantity * line.unitCostCents, 0))
const cartNetCents = computed(() => {
  const vat = vatRateBasisPoints.value
  const goods = priceMode.value === 'basket'
    ? (pricesIncludeVat.value ? netFromGross(goodsEnteredCents.value, vat) : goodsEnteredCents.value)
    : cart.value.reduce((sum, line) => {
      const entered = line.quantity * line.unitCostCents
      return sum + (pricesIncludeVat.value ? netFromGross(entered, vat) : entered)
    }, 0)
  const shipping = shippingEnteredCents.value ?? 0
  return goods + (pricesIncludeVat.value ? netFromGross(shipping, vat) : shipping)
})
const cartGrossCents = computed(() => {
  const vat = vatRateBasisPoints.value
  const goods = priceMode.value === 'basket'
    ? grossFromEntered(goodsEnteredCents.value, pricesIncludeVat.value, vat)
    : cart.value.reduce(
      (sum, line) => sum + line.quantity * grossFromEntered(line.unitCostCents, pricesIncludeVat.value, vat), 0,
    )
  const shipping = shippingEnteredCents.value ?? 0
  return goods + grossFromEntered(shipping, pricesIncludeVat.value, vat)
})

interface PurchaseReceipt {
  receiptId: string
  purchasedOn: string
  supplier: string
  invoiceReference: string
  positions: Purchase[]
  totalCostCents: number
  priceMode: 'unit' | 'basket'
  pricesIncludeVat: boolean
  vatRateBasisPoints: number
  shippingCostCents: number
  hasAttachment: boolean
  isCancelled: boolean
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
        priceMode: purchase.price_mode || 'unit',
        pricesIncludeVat: purchase.prices_include_vat,
        vatRateBasisPoints: purchase.vat_rate_basis_points || 1900,
        shippingCostCents: purchase.shipping_cost_cents,
        hasAttachment: false,
        isCancelled: false,
      }
      known.set(purchase.receipt_id, receipt)
      receipts.push(receipt)
    }
    receipt.positions.push(purchase)
    receipt.totalCostCents += purchase.total_cost_cents
    receipt.hasAttachment ||= purchase.has_invoice_file || purchase.has_receipt_attachment
  }
  for (const receipt of receipts) {
    receipt.totalCostCents += receipt.shippingCostCents
    receipt.isCancelled = receipt.positions.length > 0 &&
      receipt.positions.every((purchase) => purchase.is_cancelled)
  }

  const needle = filter.value.trim().toLowerCase()
  return receipts.filter((receipt) => {
    if (!isWithinDateRange(receipt.purchasedOn, dateFrom.value, dateTo.value)) return false
    if (!needle) return true
    const positions = receipt.positions
      .map((purchase) => `${purchase.article_name} ${purchase.variant_label} ${purchase.comment}`)
      .join(' ')
    return `${receipt.receiptId} ${receipt.supplier} ${receipt.invoiceReference} ${positions}`
      .toLowerCase()
      .includes(needle)
  })
})
function exportVisible() {
  downloadCsv(
    datedFilename('einkaeufe'),
    [
      t('common.date'),
      t('history.receipt'),
      t('sales.articles'),
      t('articles.variant'),
      t('common.quantity'),
      t('purchases.unitCost'),
      t('purchases.total'),
      t('purchases.supplier'),
      t('purchases.invoiceReference'),
      t('common.comment'),
      t('purchases.status'),
    ],
    visibleReceipts.value.flatMap((receipt) =>
      receipt.positions.map((purchase) => [
        receipt.purchasedOn,
        receipt.receiptId,
        purchase.article_name,
        purchase.variant_label,
        purchase.quantity,
        format(purchase.unit_cost_cents),
        format(purchase.total_cost_cents),
        receipt.supplier,
        receipt.invoiceReference,
        purchase.comment,
        purchase.is_cancelled ? t('purchases.cancelledLabel') : t('purchases.activeLabel'),
      ]),
    ),
  )
}

onMounted(async () => {  await Promise.all([loadArticles(), loadPurchases(), refreshPreview()])
  loading.value = false
})

async function loadArticles() {
  try {
    articles.value = (await catalogueApi.list()).articles
    if (!purchasableArticles.value.some((article) => article.id === selectedArticleId.value)) {
      selectedArticleId.value = null
      chosenValues.value = {}
    }
  } catch {
    flash.error(t('errors.generic'))
  }
}

async function loadPurchases(showLoading = false) {
  if (showLoading) loading.value = true
  purchasesLoadFailed.value = false
  try {
    const result = await purchasesApi.list()
    purchases.value = result.purchases
    purchaseEditingEnabled.value = result.editing_enabled
  } catch {
    purchasesLoadFailed.value = true
    flash.error(t('errors.generic'))
  } finally {
    if (showLoading) loading.value = false
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
  const firstVariant = article.variants.find((variant) => !variant.no_reorder)
  if (!firstVariant) return
  for (const group of article.option_groups.filter((entry) => entry.is_active)) {
    const first = (group.values ?? []).find(
      (value) => value.is_active && firstVariant.option_value_ids.includes(value.id),
    )
    if (first) chosenValues.value[group.id] = first.id
  }
}

// The last price paid for a variant is pre-filled, so a reorder needs no
// retyping — but it stays editable, because suppliers change their prices.
watch(selectedVariant, async (variant) => {
  if (!variant) return
  const articleId = selectedArticleId.value
  if (articleId !== null && rememberedArticleCost.value[articleId] !== undefined) {
    unitCostInput.value = rememberedArticleCost.value[articleId] ?? ''
    return
  }
  try {
    const last = await purchasesApi.lastCost(variant.id)
    if (!last.found) {
      unitCostInput.value = ''
      return
    }
    const cents = pricesIncludeVat.value
      ? last.unit_cost_cents
      : netFromGross(last.unit_cost_cents, vatRateBasisPoints.value)
    unitCostInput.value = (cents / 100).toFixed(2).replace('.', ',')
  } catch {
    unitCostInput.value = ''
  }
})

function onUnitCostChanged() {
  const article = selectedArticle.value
  const cents = parseAmount(unitCostInput.value)
  if (unitCostInput.value.trim() && (cents === null || cents < 0)) {
    flash.error(t('purchases.invalidPrice'))
    return
  }
  if (!article || cents === null || purchasableVariants.value.length <= 1) return
  if (!window.confirm(t('purchases.applyPriceToVariants'))) return
  rememberedArticleCost.value = { ...rememberedArticleCost.value, [article.id]: unitCostInput.value }
  cart.value = cart.value.map((line) => line.articleId === article.id ? { ...line, unitCostCents: cents } : line)
}

function changePriceMode(next: 'unit' | 'basket') {
  if (next === priceMode.value) return
  if (cart.value.length && !window.confirm(t('purchases.priceModeChangeConfirm'))) return
  cart.value = []
  priceMode.value = next
  basketPriceInput.value = ''
}

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
  if (!variant) return
  const cost = priceMode.value === 'unit' ? parseAmount(unitCostInput.value) : 0
  if (cost === null || cost < 0) {
    flash.error(t('purchases.invalidPrice'))
    return
  }
  if (quantity.value <= 0) return

  cart.value.push({
    articleId: selectedArticleId.value ?? 0,
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
  const vat = parseVatRate(vatRateInput.value)
  const shipping = parseAmount(shippingCostInput.value)
  if (vat === null) {
    flash.error(t('purchases.invalidVat'))
    return
  }
  if (shipping === null || shipping < 0) {
    flash.error(t('purchases.invalidShipping'))
    return
  }
  let goodsTotal: number | undefined
  if (priceMode.value === 'basket') {
    const parsed = parseAmount(basketPriceInput.value)
    if (parsed === null || parsed < 0) {
      flash.error(t('purchases.invalidBasketTotal'))
      return
    }
    goodsTotal = parsed
  }
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
      prices_include_vat: pricesIncludeVat.value,
      vat_rate_basis_points: vat,
      shipping_cost_cents: shipping,
      price_mode: priceMode.value,
      goods_total_cents: goodsTotal ?? undefined,
      receipt_id: receiptId.value,
    })
    flash.success(t('purchases.booked', { receipt: result.receipt_id }))
    const failedUploads = await uploadReceiptFiles(result.receipt_id, receiptInvoices.value)
    cart.value = []
    rememberedArticleCost.value = {}
    supplier.value = ''
    invoiceReference.value = ''
    shippingCostInput.value = '0,00'
    basketPriceInput.value = ''
    receiptInvoices.value = []
    if (receiptInvoiceInput.value) receiptInvoiceInput.value.value = ''
    await Promise.all([loadArticles(), loadPurchases(), refreshPreview()])
    if (failedUploads.length) {
      attachmentsForReceiptId.value = result.receipt_id
      failedUploadFiles.value = failedUploads
      await loadAttachments()
      flash.error(t('purchases.attachmentPartialFailure', { count: failedUploads.length }))
    }
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

function chooseReceiptInvoices(event: Event) {
  const input = event.target as HTMLInputElement
  receiptInvoices.value.push(...Array.from(input.files ?? []))
  input.value = ''
}

function removeReceiptInvoice(index: number) {
  receiptInvoices.value.splice(index, 1)
}

interface ReceiptEditLine {
  id: number
  label: string
  quantity: number
  unitCostInput: string
}

const editingReceipt = ref<PurchaseReceipt | null>(null)
const editPurchasedOn = ref('')
const editSupplier = ref('')
const editInvoiceReference = ref('')
const editPricesIncludeVat = ref(true)
const editVatRateInput = ref('19')
const editShippingCostInput = ref('0,00')
const editPriceMode = ref<'unit' | 'basket'>('unit')
const editBasketPriceInput = ref('')
const editLines = ref<ReceiptEditLine[]>([])

function toMoneyInput(cents: number) {
  return (cents / 100).toFixed(2).replace('.', ',')
}

function startEdit(receipt: PurchaseReceipt) {
  editingReceipt.value = receipt
  editPurchasedOn.value = receipt.purchasedOn
  editSupplier.value = receipt.supplier
  editInvoiceReference.value = receipt.invoiceReference
  editPricesIncludeVat.value = receipt.pricesIncludeVat
  editPriceMode.value = receipt.priceMode
  editVatRateInput.value = (receipt.vatRateBasisPoints / 100).toFixed(2).replace(/(?:[.,]00)$/, '').replace('.', ',')
  const enteredShipping = receipt.pricesIncludeVat
    ? receipt.shippingCostCents
    : netFromGross(receipt.shippingCostCents, receipt.vatRateBasisPoints)
  editShippingCostInput.value = toMoneyInput(enteredShipping)
  const goodsGross = receipt.positions
    .filter((purchase) => !purchase.is_cancelled)
    .reduce((sum, purchase) => sum + purchase.total_cost_cents, 0)
  editBasketPriceInput.value = toMoneyInput(receipt.pricesIncludeVat
    ? goodsGross
    : netFromGross(goodsGross, receipt.vatRateBasisPoints))
  editLines.value = receipt.positions.filter((purchase) => !purchase.is_cancelled).map((purchase) => ({
    id: purchase.id,
    label: `${purchase.article_name} — ${purchase.variant_label}`,
    quantity: purchase.quantity,
    unitCostInput: toMoneyInput(receipt.pricesIncludeVat
      ? purchase.unit_cost_cents
      : netFromGross(purchase.unit_cost_cents, receipt.vatRateBasisPoints)),
  }))
}

function changeEditPriceMode(next: 'unit' | 'basket') {
  if (next === editPriceMode.value) return
  if (next === 'basket') {
    const entered = editLines.value.map((line) => parseAmount(line.unitCostInput))
    if (entered.some((value) => value === null || value < 0) ||
      editLines.value.some((line) => !Number.isInteger(line.quantity) || line.quantity <= 0)) {
      flash.error(t('purchases.invalidPrice'))
      return
    }
    editBasketPriceInput.value = toMoneyInput(editLines.value.reduce(
      (sum, line, index) => sum + line.quantity * (entered[index] ?? 0), 0,
    ))
  } else {
    const total = parseAmount(editBasketPriceInput.value)
    const pieces = editLines.value.reduce((sum, line) => sum + line.quantity, 0)
    if (total === null || total < 0 || pieces <= 0) {
      flash.error(t('purchases.invalidBasketTotal'))
      return
    }
    const effectiveUnit = Math.round(total / pieces)
    editLines.value = editLines.value.map((line) => ({
      ...line,
      unitCostInput: toMoneyInput(effectiveUnit),
    }))
  }
  editPriceMode.value = next
}

async function saveReceiptEdit() {
  const receipt = editingReceipt.value
  if (!receipt || busy.value) return
  const vat = parseVatRate(editVatRateInput.value)
  const shipping = parseAmount(editShippingCostInput.value)
  if (vat === null) {
    flash.error(t('purchases.invalidVat'))
    return
  }
  if (shipping === null || shipping < 0) {
    flash.error(t('purchases.invalidShipping'))
    return
  }
  const parsedItems = editLines.value.map((line) => ({
    id: line.id,
    quantity: line.quantity,
    unit_cost_cents: parseAmount(line.unitCostInput),
  }))
  if (parsedItems.some((item) => item.quantity <= 0 || (editPriceMode.value === 'unit' && (item.unit_cost_cents === null || item.unit_cost_cents < 0)))) {
    flash.error(t('purchases.invalidPrice'))
    return
  }
  let goodsTotal: number | undefined
  if (editPriceMode.value === 'basket') {
    const parsed = parseAmount(editBasketPriceInput.value)
    if (parsed === null || parsed < 0) {
      flash.error(t('purchases.invalidBasketTotal'))
      return
    }
    goodsTotal = parsed
  }
  busy.value = true
  try {
    await purchasesApi.updateReceipt(receipt.receiptId, {
      items: parsedItems.map((item) => ({ id: item.id, quantity: item.quantity, unit_cost_cents: item.unit_cost_cents ?? 0 })),
      purchased_on: editPurchasedOn.value,
      supplier: editSupplier.value.trim(),
      invoice_reference: editInvoiceReference.value.trim(),
      prices_include_vat: editPricesIncludeVat.value,
      vat_rate_basis_points: vat,
      shipping_cost_cents: shipping,
      price_mode: editPriceMode.value,
      goods_total_cents: goodsTotal ?? undefined,
    })
    flash.success(t('purchases.updated'))
    editingReceipt.value = null
    await Promise.all([loadArticles(), loadPurchases()])
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

interface RefillRow extends RefillSuggestion {
  quantity: number
  unitCostInput: string
}

const refillOpen = ref(false)
const refillLoading = ref(false)
const refillRows = ref<RefillRow[]>([])

async function openRefill() {
  refillOpen.value = true
  refillLoading.value = true
  refillRows.value = []
  try {
    const { items } = await purchasesApi.refillSuggestions()
    refillRows.value = items.map((item) => ({
      ...item,
      quantity: item.suggested_quantity,
      unitCostInput: item.last_unit_cost_cents === null
        ? ''
        : toMoneyInput(pricesIncludeVat.value
          ? item.last_unit_cost_cents
          : netFromGross(item.last_unit_cost_cents, vatRateBasisPoints.value)),
    }))
  } catch (error) {
    report(error)
  } finally {
    refillLoading.value = false
  }
}

function removeRefillRow(index: number) {
  refillRows.value.splice(index, 1)
}

async function takeRefillAsCart() {
  if (!refillRows.value.length) return
  const valid = refillRows.value.every((row) =>
    Number.isInteger(row.quantity) && row.quantity > 0 &&
    parseAmount(row.unitCostInput) !== null && (parseAmount(row.unitCostInput) ?? -1) >= 0,
  )
  if (!valid) {
    flash.error(t('purchases.invalidRefill'))
    return
  }
  if (cart.value.length && !window.confirm(t('purchases.replaceCartConfirm'))) return
  priceMode.value = 'unit'
  basketPriceInput.value = ''
  cart.value = refillRows.value.map((row) => ({
    articleId: row.article_id,
    variantId: row.variant_id,
    label: row.variant_label ? `${row.article_name} — ${row.variant_label}` : row.article_name,
    quantity: row.quantity,
    unitCostCents: parseAmount(row.unitCostInput) ?? 0,
  }))
  refillOpen.value = false
}

/**
 * Invoices and receipt attachments.
 *
 * Receipt attachments belong to the whole basket rather than to one position.
 */
const fileInput = ref<HTMLInputElement | null>(null)
const attachmentsForReceiptId = ref<string | null>(null)
const attachments = ref<Attachment[]>([])
const attachmentBusy = ref(false)
const failedUploadFiles = ref<File[]>([])

async function uploadReceiptFiles(receiptID: string, files: File[]) {
  const failed: File[] = []
  for (const file of files) {
    try {
      await attachmentsApi.upload(receiptID, file)
    } catch {
      failed.push(file)
    }
  }
  return failed
}

function pickAttachment() {
  fileInput.value?.click()
}

async function onFilesChosen(event: Event) {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  // Cleared straight away so picking the same file twice still fires a change.
  input.value = ''
  if (!files.length || !attachmentsForReceiptId.value || attachmentBusy.value) return

  attachmentBusy.value = true
  try {
    const failed = await uploadReceiptFiles(attachmentsForReceiptId.value, files)
    failedUploadFiles.value.push(...failed)
    await Promise.all([loadAttachments(), loadPurchases()])
    if (failed.length) {
      flash.error(t('purchases.attachmentPartialFailure', { count: failed.length }))
    } else {
      flash.success(t('purchases.invoicesUploaded'))
    }
  } finally {
    attachmentBusy.value = false
  }
}

async function retryFailedUploads() {
  if (!attachmentsForReceiptId.value || !failedUploadFiles.value.length || attachmentBusy.value) return
  attachmentBusy.value = true
  try {
    failedUploadFiles.value = await uploadReceiptFiles(
      attachmentsForReceiptId.value,
      failedUploadFiles.value,
    )
    await Promise.all([loadAttachments(), loadPurchases()])
    if (failedUploadFiles.value.length) {
      flash.error(t('purchases.attachmentPartialFailure', { count: failedUploadFiles.value.length }))
    } else {
      flash.success(t('purchases.invoicesUploaded'))
    }
  } finally {
    attachmentBusy.value = false
  }
}

async function openAttachments(receiptID: string) {
  attachmentsForReceiptId.value = receiptID
  attachments.value = []
  failedUploadFiles.value = []
  await loadAttachments()
}

function closeAttachments() {
  if (attachmentBusy.value) return
  attachmentsForReceiptId.value = null
  attachments.value = []
  failedUploadFiles.value = []
}

async function loadAttachments() {
  if (!attachmentsForReceiptId.value) return
  try {
    attachments.value = (await attachmentsApi.list(attachmentsForReceiptId.value)).attachments
  } catch (error) {
    report(error)
  }
}

async function removeAttachment(file: Attachment) {
  if (!attachmentsForReceiptId.value) return
  try {
    await attachmentsApi.remove(attachmentsForReceiptId.value, file.id)
    await Promise.all([loadAttachments(), loadPurchases()])
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

async function cancelReceipt(receipt: PurchaseReceipt) {
  if (!window.confirm(t('purchases.cancelReceiptConfirm', { receipt: receipt.receiptId }))) return
  try {
    await purchasesApi.cancelReceipt(receipt.receiptId)
    flash.success(t('purchases.receiptCancelled'))
    await Promise.all([loadArticles(), loadPurchases()])
  } catch (error) {
    report(error)
  }
}
</script>

<template>
  <main class="page-shell transaction-page operational-page purchases-page">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('purchases.eyebrow') }}</p>
        <h1>{{ t('purchases.title') }}</h1>
      </div>
      <div class="purchase-title-actions">
        <button v-if="canManage" class="secondary-button" type="button" @click="openRefill">
          {{ t('purchases.refill') }}
        </button>
        <div class="receipt-preview">
          <span>{{ t('sales.receiptId') }}</span>
          <strong>{{ receiptId || t('sales.receiptLoading') }}</strong>
        </div>
      </div>
    </div>

    <section v-if="canManage" class="transaction-layout">
      <aside class="selection-panel product-panel">
        <h2>{{ t('sales.articles') }}</h2>
        <p class="panel-hint">{{ t('sales.articlesHint') }}</p>
        <div class="button-list">
          <button
            v-for="article in purchasableArticles"
            :key="article.id"
            type="button"
            class="selection-button"
            :class="{ active: article.id === selectedArticleId }"
            @click="selectArticle(article)"
          >
            <span>{{ article.name }}</span>
            <small>{{ t('sales.inStock', { count: article.total_stock }) }}</small>
          </button>
          <p v-if="!loading && !purchasableArticles.length" class="muted">{{ t('sales.noArticles') }}</p>
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
        <fieldset class="price-mode-switch">
          <legend>{{ t('purchases.priceMode') }}</legend>
          <button type="button" :class="{ active: priceMode === 'unit' }" @click="changePriceMode('unit')">
            {{ t('purchases.pricePerItem') }}
          </button>
          <button type="button" :class="{ active: priceMode === 'basket' }" @click="changePriceMode('basket')">
            {{ t('purchases.basketPrice') }}
          </button>
        </fieldset>
        <label v-if="priceMode === 'unit'">
          {{ t('purchases.unitCost') }}
          <input v-model="unitCostInput" inputmode="decimal" :disabled="!selectedVariant" @change="onUnitCostChanged" />
        </label>
        <div class="purchase-line-controls">
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
                <small v-if="priceMode === 'unit'">{{ line.quantity }} × {{ format(line.unitCostCents) }}</small>
                <small v-else>{{ t('common.quantity') }}: {{ line.quantity }}</small>
              </span>
              <b v-if="priceMode === 'unit'">{{ format(line.quantity * line.unitCostCents) }}</b>
              <button class="icon-button" type="button" @click="removeLine(index)">×</button>
            </div>
          </div>
        </section>

        <label v-if="priceMode === 'basket'" class="basket-price-field">
          {{ t('purchases.basketPriceWithoutShipping') }}
          <input v-model="basketPriceInput" inputmode="decimal" />
          <small class="muted">{{ t('purchases.basketPriceHint') }}</small>
        </label>

        <div class="field-grid two-columns purchase-tax-row">
          <AppToggle v-model="pricesIncludeVat" :label="t('purchases.priceIncludesVat')" />
          <label>
            {{ t('purchases.vatRate') }}
            <input v-model="vatRateInput" inputmode="decimal" />
          </label>
        </div>

        <label>{{ t('purchases.shippingCost') }}<input v-model="shippingCostInput" inputmode="decimal" /></label>

        <div class="total-box purchase-total-box">
          <span>{{ t('purchases.netTotal') }}</span>
          <strong>{{ format(cartNetCents) }}</strong>
          <span>{{ t('purchases.grossTotal') }}</span>
          <strong>{{ format(cartGrossCents) }}</strong>
        </div>

        <div class="field-grid two-columns">
          <label>{{ t('common.date') }}<input v-model="purchasedOn" type="date" /></label>
          <label>{{ t('purchases.supplier') }}<input v-model="supplier" /></label>
        </div>
        <label>{{ t('purchases.invoiceReference') }}<input v-model="invoiceReference" /></label>
        <label class="purchase-invoice-picker">
          {{ t('purchases.invoiceFile') }}
          <input
            ref="receiptInvoiceInput"
            type="file"
            accept=".pdf,.jpg,.jpeg,.png,application/pdf,image/jpeg,image/png"
            multiple
            @change="chooseReceiptInvoices"
          />
          <ul v-if="receiptInvoices.length" class="attachment-file-list pending-invoice-list">
            <li v-for="(file, index) in receiptInvoices" :key="`${file.name}-${file.size}-${index}`" class="attachment-file-row">
              <span>{{ file.name }}</span>
              <button class="compact-button danger-button" type="button" @click.stop.prevent="removeReceiptInvoice(index)">
                {{ t('common.delete') }}
              </button>
            </li>
          </ul>
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
        <div class="purchase-history-toolbar data-toolbar">
          <DateRangeFilter
            v-model:from="dateFrom"
            v-model:to="dateTo"
            exportable
            @export="exportVisible"
          />
          <label class="table-filter">
            {{ t('common.filter') }}
            <input v-model="filter" type="search" />
          </label>
        </div>
      </div>

      <TableSkeleton v-if="loading" :label="t('common.loading')" :columns="5" />
      <EmptyState v-else-if="purchasesLoadFailed" tone="error" :title="t('errors.generic')">
        <button class="secondary-button" type="button" @click="loadPurchases(true)">{{ t('common.retry') }}</button>
      </EmptyState>
      <p v-else-if="purchases.length && !visibleReceipts.length" class="muted">{{ t('purchases.empty') }}</p>
      <EmptyState v-else-if="!visibleReceipts.length" :title="t('purchases.empty')" />
      <div v-else class="purchase-receipt-list">
        <details
          v-for="receipt in visibleReceipts"
          :key="receipt.receiptId"
          class="purchase-receipt-card"
          :class="{ cancelled: receipt.isCancelled }"
        >
          <summary>
            <span class="receipt-summary-main">
              <span class="receipt-id-with-file">
                <code>{{ receipt.receiptId }}</code>
                <span v-if="receipt.hasAttachment" class="attachment-indicator" :title="t('purchases.hasAttachment')" :aria-label="t('purchases.hasAttachment')">📎</span>
              </span>
              <small>{{ receipt.purchasedOn }} · {{ receipt.supplier || t('purchases.noSupplier') }}</small>
              <em v-if="receipt.isCancelled" class="cancelled-badge">{{ t('purchases.cancelledLabel') }}</em>
            </span>
            <span>{{ t('purchases.positionCount', { count: receipt.positions.length }) }}</span>
            <strong>{{ format(receipt.totalCostCents) }}</strong>
            <button
              v-if="purchaseEditingEnabled && canManage && !receipt.isCancelled"
              class="compact-button"
              type="button"
              @click.stop.prevent="startEdit(receipt)"
            >
              {{ t('purchases.edit') }}
            </button>
            <span class="receipt-chevron" aria-hidden="true">⌄</span>
          </summary>
          <div class="receipt-details">
            <div class="receipt-meta">
              <span><b>{{ t('purchases.supplier') }}:</b> {{ receipt.supplier || '—' }}</span>
              <span><b>{{ t('purchases.invoiceReference') }}:</b> {{ receipt.invoiceReference || '—' }}</span>
              <span><b>{{ t('purchases.priceMode') }}:</b> {{ t(receipt.priceMode === 'basket' ? 'purchases.basketPrice' : 'purchases.pricePerItem') }}</span>
              <span><b>{{ t('purchases.shippingCost') }}:</b> {{ format(receipt.shippingCostCents) }}</span>
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
              <button class="secondary-button" type="button" @click="openAttachments(receipt.receiptId)">
                {{ t('purchases.invoiceAndAttachments') }}
              </button>
              <button
                v-if="canManage && !receipt.isCancelled"
                class="secondary-button danger-button"
                type="button"
                @click="cancelReceipt(receipt)"
              >
                {{ t('purchases.cancelReceipt') }}
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
      multiple
      hidden
      :disabled="attachmentBusy"
      @change="onFilesChosen"
    />

    <AppDialog v-if="refillOpen" :label="t('purchases.refillTitle')" @close="refillOpen = false">
      <div class="stack-form refill-dialog">
        <div>
          <h2>{{ t('purchases.refillTitle') }}</h2>
          <p class="muted">{{ t('purchases.refillHint') }}</p>
        </div>
        <TableSkeleton v-if="refillLoading" :label="t('common.loading')" :columns="5" />
        <p v-else-if="!refillRows.length" class="muted">{{ t('purchases.refillEmpty') }}</p>
        <div v-else class="refill-list">
          <div v-for="(row, index) in refillRows" :key="row.variant_id" class="refill-row">
            <span class="refill-product">
              <strong>{{ row.article_name }}</strong>
              <small>{{ row.variant_label || '—' }}</small>
            </span>
            <span class="refill-stock">
              <small>{{ t('purchases.currentStock') }}</small>
              <b>{{ row.on_hand }}</b>
            </span>
            <span class="refill-stock">
              <small>{{ t('purchases.targetStock') }}</small>
              <b>{{ row.target_stock }}</b>
            </span>
            <label>
              {{ t('common.quantity') }}
              <input v-model.number="row.quantity" type="number" min="1" step="1" inputmode="numeric" />
            </label>
            <label>
              {{ t('purchases.unitCost') }}
              <input v-model="row.unitCostInput" inputmode="decimal" :placeholder="t('purchases.missingLastCost')" />
            </label>
            <button class="icon-button danger-button" type="button" :aria-label="t('purchases.removeSuggestion')" @click="removeRefillRow(index)">×</button>
          </div>
        </div>
        <div class="dialog-actions">
          <button class="secondary-button" type="button" @click="refillOpen = false">{{ t('common.cancel') }}</button>
          <button class="primary-button" type="button" :disabled="refillLoading || !refillRows.length" @click="takeRefillAsCart">
            {{ t('purchases.takeAsCart') }}
          </button>
        </div>
      </div>
    </AppDialog>

    <AppDialog v-if="editingReceipt" :label="t('purchases.editTitle')" :dismissible="!busy" @close="editingReceipt = null">
      <div class="stack-form">
        <div>
          <p class="eyebrow"><code>{{ editingReceipt.receiptId }}</code></p>
          <h2>{{ t('purchases.editTitle') }}</h2>
          <p class="muted">{{ t('purchases.editHint') }}</p>
        </div>
        <div class="field-grid two-columns">
          <label>{{ t('common.date') }}<input v-model="editPurchasedOn" type="date" /></label>
          <label>{{ t('purchases.supplier') }}<input v-model="editSupplier" /></label>
        </div>
        <label>{{ t('purchases.invoiceReference') }}<input v-model="editInvoiceReference" /></label>
        <div class="field-grid two-columns">
          <AppToggle v-model="editPricesIncludeVat" :label="t('purchases.priceIncludesVat')" />
          <label>{{ t('purchases.vatRate') }}<input v-model="editVatRateInput" inputmode="decimal" /></label>
        </div>
        <label>{{ t('purchases.shippingCost') }}<input v-model="editShippingCostInput" inputmode="decimal" /></label>
        <fieldset class="price-mode-switch">
          <legend>{{ t('purchases.priceMode') }}</legend>
          <button type="button" :class="{ active: editPriceMode === 'unit' }" @click="changeEditPriceMode('unit')">{{ t('purchases.pricePerItem') }}</button>
          <button type="button" :class="{ active: editPriceMode === 'basket' }" @click="changeEditPriceMode('basket')">{{ t('purchases.basketPrice') }}</button>
        </fieldset>
        <div class="edit-purchase-lines">
          <div v-for="line in editLines" :key="line.id" class="edit-purchase-line">
            <strong>{{ line.label }}</strong>
            <label>{{ t('common.quantity') }}<input v-model.number="line.quantity" type="number" min="1" /></label>
            <label v-if="editPriceMode === 'unit'">{{ t('purchases.unitCost') }}<input v-model="line.unitCostInput" inputmode="decimal" /></label>
          </div>
        </div>
        <label v-if="editPriceMode === 'basket'">
          {{ t('purchases.basketPriceWithoutShipping') }}
          <input v-model="editBasketPriceInput" inputmode="decimal" />
        </label>
        <div class="dialog-actions">
          <button class="secondary-button" type="button" @click="editingReceipt = null">{{ t('common.cancel') }}</button>
          <button class="primary-button" type="button" :disabled="busy" @click="saveReceiptEdit">{{ t('common.save') }}</button>
        </div>
      </div>
    </AppDialog>

    <AppDialog v-if="attachmentsForReceiptId" :label="t('purchases.invoiceAndAttachments')" @close="closeAttachments">
      <div class="stack-form">
        <div>
          <p class="eyebrow"><code>{{ attachmentsForReceiptId }}</code></p>
          <h2>{{ t('purchases.invoiceAndAttachments') }}</h2>
          <p class="muted">{{ t('purchases.attachmentsHint') }}</p>
        </div>

        <p v-if="!attachments.length" class="muted">{{ t('purchases.noAttachments') }}</p>
        <ul v-else class="attachment-file-list">
          <li v-for="file in attachments" :key="file.id" class="attachment-file-row">
            <a :href="attachmentsApi.fileUrl(attachmentsForReceiptId, file.id)">
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

        <div v-if="failedUploadFiles.length" class="notice error">
          <p>{{ t('purchases.attachmentPartialFailure', { count: failedUploadFiles.length }) }}</p>
          <ul class="attachment-file-list">
            <li v-for="(file, index) in failedUploadFiles" :key="`${file.name}-${file.size}-${index}`" class="attachment-file-row">
              {{ file.name }}
            </li>
          </ul>
          <button class="secondary-button" type="button" :disabled="attachmentBusy" @click="retryFailedUploads">
            {{ t('common.retry') }}
          </button>
        </div>

        <div class="dialog-actions">
          <button v-if="canManage" class="secondary-button" type="button" :disabled="attachmentBusy" @click="pickAttachment">
            {{ t('purchases.addAttachments') }}
          </button>
          <button class="primary-button" type="button" :disabled="attachmentBusy" @click="closeAttachments">
            {{ t('common.close') }}
          </button>
        </div>
      </div>
    </AppDialog>
  </main>
</template>

<style scoped>
.purchase-title-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  align-items: center;
  justify-content: flex-end;
}

.price-mode-switch {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 6px;
  padding: 0;
  border: 0;
}

.price-mode-switch legend {
  grid-column: 1 / -1;
  margin-bottom: 3px;
  color: var(--text-muted);
  font-size: .82rem;
  font-weight: 700;
}

.price-mode-switch > button,
.price-mode-switch > label {
  min-height: 44px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-control);
  background: var(--surface-muted);
  color: var(--text);
}

.price-mode-switch > button.active {
  border-color: var(--accent);
  background: var(--surface-selected);
}

.price-mode-switch > label {
  display: flex;
  gap: 8px;
  align-items: center;
  padding: 8px 12px;
}

.basket-price-field {
  padding: 12px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-control);
  background: var(--surface-inset);
}

.refill-list {
  display: grid;
  gap: 8px;
  max-height: min(60vh, 560px);
  overflow: auto;
}

.refill-row {
  display: grid;
  grid-template-columns: minmax(160px, 1.5fr) 74px 74px 100px 140px 44px;
  gap: 10px;
  align-items: end;
  padding: 10px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-control);
  background: var(--surface-muted);
}

.refill-product,
.refill-stock {
  display: grid;
  gap: 2px;
}

.refill-product small,
.refill-stock small {
  color: var(--text-muted);
}

.receipt-id-with-file {
  display: flex;
  gap: 8px;
  align-items: center;
}

.purchase-line-controls {
  display: flex;
  align-items: flex-end;
}

.attachment-indicator { font-size: 1rem; }

.edit-purchase-lines { display: grid; gap: 10px; }
.edit-purchase-line {
  display: grid;
  grid-template-columns: minmax(180px, 1fr) 110px 140px;
  gap: 10px;
  align-items: end;
}

.purchase-history-toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  align-items: flex-end;
  justify-content: flex-end;
}
.purchase-receipt-list {
  display: grid;
  gap: 12px;
}

.purchase-receipt-card {
  overflow: hidden;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-panel);
  background: var(--surface-panel);
}

.purchase-receipt-card:not(.cancelled) summary:hover {
  background: var(--surface-hover);
}

.purchase-receipt-card[open] {
  border-color: var(--border-default);
  background: var(--surface-raised);
}

.purchase-receipt-card.cancelled {
  opacity: .72;
  border-color: color-mix(in srgb, var(--danger) 50%, var(--border));
  background: color-mix(in srgb, var(--danger) 7%, var(--surface-panel));
}

.cancelled-badge {
  width: fit-content;
  padding: 2px 7px;
  border-radius: 999px;
  color: var(--danger);
  border: 1px solid color-mix(in srgb, var(--danger) 45%, var(--border));
  font-size: .72rem;
  font-style: normal;
  font-weight: 700;
}

.purchase-receipt-card summary {
  display: grid;
  grid-template-columns: minmax(180px, 1fr) auto auto auto auto;
  gap: 20px;
  align-items: center;
  padding: 16px 18px;
  cursor: pointer;
  list-style: none;
  transition: background .12s ease;
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
  color: var(--text-secondary);
}

.receipt-summary-main small {
  color: var(--muted);
}

.purchase-receipt-card summary > strong {
  font-size: 1.08rem;
  font-variant-numeric: tabular-nums;
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
  .edit-purchase-line { grid-template-columns: 1fr; }

  .refill-row {
    grid-template-columns: 1fr 1fr;
  }

  .refill-product {
    grid-column: 1 / -1;
  }

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
