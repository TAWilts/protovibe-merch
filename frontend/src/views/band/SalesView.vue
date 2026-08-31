<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { catalogueApi, photosApi, salesApi, type BookSalePayload } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type {
  Article,
  BasketLine,
  PaymentQRAvailability,
  PaymentQRIntent,
  SaleEvent,
  Variant,
} from '@/api/types'
import { useMoney, parseAmount } from '@/composables/useMoney'
import { useFlashStore } from '@/stores/flash'
import { useOfflineStore } from '@/stores/offline'
import { useSessionStore } from '@/stores/session'
import PaymentMethodIcon from '@/components/PaymentMethodIcon.vue'

/**
 * The point of sale, ported from _old/templates/sales.html.
 *
 * The three-column layout is the signature of the original: articles on the
 * left, the article's own options in the middle, payment and basket on the
 * right. Options are entirely generic — the page never knows that "Farbe" or
 * "Größe" exist, it just renders whatever columns the article defines.
 */
const route = useRoute()
const { t } = useI18n()
const { format } = useMoney()
const flash = useFlashStore()
const session = useSessionStore()
const offline = useOfflineStore()

const articles = ref<Article[]>([])
const paymentMethods = ref<string[]>([])
const events = ref<SaleEvent[]>([])
const selectedEventId = ref<number>(0)
const receiptId = ref('')
const loading = ref(true)

const selectedArticleId = ref<number | null>(null)
/** The chosen value per option group, keyed by group ID. */
const chosenValues = ref<Record<number, number>>({})
const quantity = ref(1)
const unitPriceInput = ref('')

const basket = ref<BasketLine[]>([])
const paymentMethod = ref('Bar')
const amountGivenInput = ref('')
const soldBy = ref('')
const comment = ref('')
const busy = ref(false)
/**
 * The extra sections start open on a desktop and collapsed on a small screen,
 * the way _old/static/sales.js:98 did it. A plain <details> is closed
 * everywhere, which hid the payment fields from anyone selling on a laptop.
 */
/**
 * The same terminal serves two counters.
 *
 * At the stand the goods go with the customer. An order is the other case: the
 * size was out, or it came in by message during the week — the money may
 * already be there, the parcel is not. Both write the same sale; the only
 * difference is whether it was handed over, and that is what puts it on the
 * shipment list.
 */
const isOrder = computed(() => route.name === 'orders')

/** The sheet holding everything a sale carries beyond its items. */
const sheetOpen = ref(false)

/** The address a parcel needs. Empty until a shipment is actually booked. */
const shipOpen = ref(false)
const shipName = ref('')
const shipAddress = ref('')
const shipPayLater = ref(false)

const shipReady = computed(
  () => shipName.value.trim() !== '' && shipAddress.value.trim() !== '',
)

/**
 * Whether anything in the sheet deviates from a plain cash sale. It drives a
 * marker on the button, so a seller can see that something is set without
 * opening it — otherwise a forgotten "not paid" is invisible until the sale
 * turns up in the wrong worklist.
 */
const sheetTouched = computed(
  () => selectedEventId.value !== 0 || comment.value.trim() !== '',
)

/**
 * The till claims exactly the space left below whatever is above it.
 *
 * A fixed header height would be wrong the moment a support-access or
 * maintenance banner appears — the page would grow a scrollbar and the booking
 * button would slide off the bottom, which is the one thing this layout exists
 * to prevent. So the offset is measured.
 */
const tillEl = ref<HTMLElement | null>(null)

function measureTill() {
  const element = tillEl.value
  if (!element) return
  element.style.setProperty('--till-offset', `${Math.round(element.getBoundingClientRect().top)}px`)
}
/** null while the inline "new event" row is hidden. */
const newEventName = ref<string | null>(null)

/**
 * The payment code flow. Showing a code is deliberately not a sale: the server
 * only reserves the receipt number, and nothing is booked until the seller has
 * seen the money arrive and confirms.
 */
const qrAvailability = ref<PaymentQRAvailability>({ paypal: false, bank: false })
const qrIntent = ref<PaymentQRIntent | null>(null)
const qrBusy = ref(false)

/** Which payment method the chosen code stands for. */
const qrMethodFor = (method: string) => (method === 'PayPal' ? 'PayPal' : 'Überweisung')

const qrOffered = computed(() => {
  if (paymentMethod.value === 'PayPal') return qrAvailability.value.paypal
  if (paymentMethod.value === 'Überweisung') return qrAvailability.value.bank
  return false
})

/**
 * A stand with forty articles is a scroll; typing two letters is faster than
 * hunting. The filter is deliberately not debounced — the list is already in
 * memory, and a delay is the one thing a queue notices.
 */
const articleFilter = ref('')

const visibleArticles = computed(() => {
  const needle = articleFilter.value.trim().toLowerCase()
  if (!needle) return articles.value
  return articles.value.filter((article) => article.name.toLowerCase().includes(needle))
})

const selectedArticle = computed(
  () => articles.value.find((article) => article.id === selectedArticleId.value) ?? null,
)

/** The active option columns of the selected article, in the band's order. */
const optionGroups = computed(() =>
  (selectedArticle.value?.option_groups ?? [])
    .filter((group) => group.is_active)
    .map((group) => ({ ...group, values: group.values.filter((value) => value.is_active) })),
)

/**
 * The variant matching the current option selection.
 *
 * Matching is by set of value IDs rather than by order, so reordering the
 * option columns never breaks the lookup.
 */
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

const basketTotalCents = computed(() =>
  basket.value.reduce((sum, line) => sum + line.unitPriceCents * line.quantity, 0),
)

const amountGivenCents = computed(() => parseAmount(amountGivenInput.value))

/**
 * What the customer handed over beyond the amount due.
 *
 * The same figure is either change to hand back or a donation to keep, and the
 * server has no idea which: it books every surplus as a donation. So the till
 * asks, and keeping it is the default — at this stand a customer who hands
 * over more usually means it. Handing it back is one tap, and the amount is
 * stated in the warning colour so the switch is never silent.
 */
const surplusCents = computed(() => {
  if (amountGivenCents.value === null) return 0
  return Math.max(0, amountGivenCents.value - basketTotalCents.value)
})

const surplusMode = ref<'change' | 'donation'>('donation')

/** Only a kept surplus reaches the server as a donation. */
const donationCents = computed(() =>
  surplusMode.value === 'donation' ? surplusCents.value : 0,
)

/**
 * The product picture while selling, as the original had it. It follows the
 * seller's own preference: at a busy stand the photos are a help, on a small
 * phone they are in the way.
 */
const variantPhotos = computed(() => {
  if (!session.user?.show_variant_photos) return []
  return (selectedVariant.value?.photo_ids ?? []).map((id) => photosApi.fileUrl(id))
})



const canAddToCart = computed(
  () => selectedVariant.value !== null && quantity.value > 0 && parseAmount(unitPriceInput.value) !== null,
)
const canBook = computed(() => basket.value.length > 0 && !busy.value)

onMounted(async () => {
  measureTill()
  window.addEventListener('resize', measureTill)
  window.addEventListener('orientationchange', measureTill)
  soldBy.value = session.user?.username ?? ''
  await Promise.all([
    loadAssortment(),
    loadEvents(),
    refreshReceiptPreview(),
    loadPaymentQrAvailability(),
  ])
  loading.value = false
  // Banners appear after the identity is known, which moves the till down.
  requestAnimationFrame(measureTill)
})

onUnmounted(() => {
  window.removeEventListener('resize', measureTill)
  window.removeEventListener('orientationchange', measureTill)
})

async function loadAssortment() {
  try {
    const result = await catalogueApi.assortment()
    articles.value = result.articles
    paymentMethods.value = result.payment_methods
    if (result.payment_methods.length && !result.payment_methods.includes(paymentMethod.value)) {
      paymentMethod.value = result.payment_methods[0]
    }
  } catch {
    flash.error(t('errors.generic'))
  }
}

async function loadPaymentQrAvailability() {
  try {
    qrAvailability.value = await salesApi.paymentQrAvailability()
  } catch {
    // Not being able to offer a code is not an error worth interrupting a
    // sale for; the cash flow keeps working.
    qrAvailability.value = { paypal: false, bank: false }
  }
}

async function loadEvents() {
  try {
    const result = await salesApi.events()
    events.value = result.events
    selectedEventId.value = result.selected_event_id
  } catch {
    // A missing event list must never block selling.
  }
}

async function refreshReceiptPreview() {
  try {
    receiptId.value = (await salesApi.receiptPreview()).receipt_id
  } catch {
    receiptId.value = ''
  }
}

function selectArticle(article: Article) {
  selectedArticleId.value = article.id
  chosenValues.value = {}
  // Pre-select the first value of every column so a single-option article is
  // immediately sellable without extra clicks.
  for (const group of article.option_groups.filter((entry) => entry.is_active)) {
    const first = group.values.find((value) => value.is_active)
    if (first) chosenValues.value[group.id] = first.id
  }
}

function chooseValue(groupId: number, valueId: number) {
  chosenValues.value = { ...chosenValues.value, [groupId]: valueId }
}

// Selecting a variant fills in its catalogue price, which the seller may then
// override for this sale only.
watch(selectedVariant, (variant) => {
  if (variant) {
    unitPriceInput.value = (variant.sale_price_cents / 100).toFixed(2).replace('.', ',')
  }
})

/**
 * The stepper is the only way to change the amount on a tablet, so it must
 * never leave the field in a state the basket cannot use: clearing a number
 * input yields NaN, which would silently disable the confirm button.
 */
/**
 * Creating a gig from the sales page is the only way to get one at all: there
 * is no other screen for sale events, so without this the picker stays empty
 * forever on a fresh band.
 */
/**
 * Books a sale that is not handed over. The server turns "not received" into a
 * pending shipment and demands the address, which is why it is asked for here
 * rather than left to the extra fields.
 */
async function bookShipment() {
  if (!shipReady.value || busy.value) return
  await book({
    ...salePayload(),
    is_received: false,
    is_paid: !shipPayLater.value,
    amount_given_cents: null,
    customer_name: shipName.value.trim(),
    customer_address: shipAddress.value.trim(),
  })
  shipOpen.value = false
}

/** Builds the request body once, so the code and the booking agree exactly. */
function salePayload(): BookSalePayload {
  return {
    items: basket.value.map((line) => ({
      variant_id: line.variantId,
      quantity: line.quantity,
      unit_price_cents: line.unitPriceCents,
    })),
    payment_method: paymentMethod.value,
    // A sale at the stand is money taken and goods handed over. The flags stay
    // in the payload because the server and the worklists are built on them;
    // a sale that is settled later is corrected under "Offene Vorgänge".
    is_paid: true,
    is_received: true,
    // Handing the surplus back means the band kept the amount due, and that is
    // what the server must record — anything more becomes a donation there.
    amount_given_cents:
      surplusMode.value === 'donation' ? amountGivenCents.value : basketTotalCents.value,
    customer_name: '',
    customer_address: '',
    event_name: events.value.find((event) => event.id === selectedEventId.value)?.name ?? '',
    sold_by: soldBy.value.trim(),
    comment: comment.value.trim(),
    receipt_id: receiptId.value,
  }
}

async function showPaymentQr() {
  if (!canBook.value || qrBusy.value) return
  qrBusy.value = true
  try {
    qrIntent.value = await salesApi.createPaymentQrIntent({
      method: qrMethodFor(paymentMethod.value),
      sale: salePayload(),
      description: basket.value.map((line) => `${line.quantity}× ${line.label}`).join(', '),
    })
  } catch (error) {
    reportError(error)
  } finally {
    qrBusy.value = false
  }
}

/** The customer walked away: release the reservation so the number is free. */
async function cancelPaymentQr() {
  const intent = qrIntent.value
  qrIntent.value = null
  if (!intent) return
  try {
    await salesApi.cancelPaymentQrIntent(intent.token)
  } catch {
    // The reservation expires on its own; a failed cancel must not block the
    // seller from carrying on.
  }
}

/** The money arrived. Now — and only now — the sale is booked. */
async function confirmPaymentQr() {
  const intent = qrIntent.value
  if (!intent || qrBusy.value) return
  qrBusy.value = true
  try {
    await book({ ...salePayload(), receipt_id: intent.receipt_id, payment_qr_intent_token: intent.token })
    qrIntent.value = null
  } finally {
    qrBusy.value = false
  }
}

function reportError(error: unknown) {
  flash.error(
    error instanceof ApiError
      ? t(`errors.${error.detailCode ?? 'generic'}`, t('errors.generic'))
      : t('errors.network'),
  )
}

async function createEvent() {
  const name = (newEventName.value ?? '').trim()
  if (!name || busy.value) return
  busy.value = true
  try {
    const event = await salesApi.createEvent(name)
    events.value = [event, ...events.value.filter((entry) => entry.id !== event.id)]
    selectedEventId.value = event.id
    newEventName.value = null
  } catch (error) {
    reportError(error)
  } finally {
    busy.value = false
  }
}

function stepQuantity(delta: number) {
  const current = Number.isFinite(quantity.value) ? quantity.value : 1
  quantity.value = Math.max(1, current + delta)
}

function normalizeQuantity() {
  if (!Number.isFinite(quantity.value) || quantity.value < 1) quantity.value = 1
}

function addToCart() {
  const variant = selectedVariant.value
  const price = parseAmount(unitPriceInput.value)
  if (!variant || price === null || !selectedArticle.value) return

  // The article and option selection deliberately stay put, so adding several
  // sizes of the same shirt needs no reselection.
  const existing = basket.value.find(
    (line) => line.variantId === variant.id && line.unitPriceCents === price,
  )
  if (existing) {
    existing.quantity += quantity.value
  } else {
    basket.value.push({
      variantId: variant.id,
      articleId: selectedArticle.value.id,
      label: variantLabel.value,
      quantity: quantity.value,
      unitPriceCents: price,
      onHand: variant.on_hand,
    })
  }
  quantity.value = 1
}

/**
 * Corrects a position's amount. Going below one removes it: the line no longer
 * describes anything, and making the seller reach for a second control to
 * finish the thought would be the slower path.
 */
function stepLine(index: number, delta: number) {
  const line = basket.value[index]
  if (!line) return
  const next = line.quantity + delta
  if (next < 1) {
    removeLine(index)
    return
  }
  line.quantity = next
}

function removeLine(index: number) {
  basket.value.splice(index, 1)
}

async function book(override?: BookSalePayload) {
  if (!override && !canBook.value) return
  busy.value = true

  const payload: BookSalePayload = override ?? salePayload()

  try {
    const result = await salesApi.book(payload)
    flash.success(t('sales.booked', { receipt: result.receipt_id }))
    resetAfterSale()
    await Promise.all([loadAssortment(), refreshReceiptPreview()])
  } catch (error) {
    // A rejected sale is the seller's to fix; a missing connection is not.
    // The second case is queued rather than lost, which is the whole point of
    // taking the app to a gig.
    if (error instanceof ApiError) {
      flash.error(t(`errors.${error.detailCode ?? 'generic'}`, t('errors.generic')))
    } else {
      await offline.queue(payload)
      flash.success(t('sales.queuedOffline'))
      resetAfterSale()
    }
  } finally {
    busy.value = false
  }
}

function resetAfterSale() {
  basket.value = []
  amountGivenInput.value = ''
  comment.value = ''
  quantity.value = 1
  surplusMode.value = 'donation'
  shipName.value = ''
  shipAddress.value = ''
  shipPayLater.value = false
  // sold_by deliberately survives, so a stand run by one person does not have
  // to retype it for every sale.
}
</script>

<template>
  <main ref="tillEl" class="till" :class="{ 'has-note': isOrder }">
    <h1 class="visually-hidden">{{ isOrder ? t('sales.ordersTitle') : t('sales.title') }}</h1>

    <!-- An order is rarer and easier to get wrong than a counter sale, so this
         one says out loud which counter you are standing at. -->
    <p v-if="isOrder" class="till-mode-note">{{ t('sales.ordersHint') }}</p>

    <section class="till-column till-articles">
      <header class="till-column-head">
        <h2>{{ t('sales.articles') }}</h2>
        <input
          v-model="articleFilter"
          class="till-filter"
          type="search"
          :placeholder="t('sales.filterArticles')"
        />
      </header>
      <div class="till-scroll">
        <div class="button-list">
          <button
            v-for="article in visibleArticles"
            :key="article.id"
            type="button"
            class="selection-button"
            :class="{ selected: article.id === selectedArticleId }"
            @click="selectArticle(article)"
          >
            <span>{{ article.name }}</span>
            <small>{{ article.total_stock }}</small>
          </button>
          <p v-if="!loading && !visibleArticles.length" class="muted">{{ t('sales.noArticles') }}</p>
        </div>
      </div>
    </section>

    <section class="till-column till-variant">
      <div class="till-scroll">
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
                @click="chooseValue(group.id, value.id)"
              >
                {{ value.value }}
              </button>
            </div>
          </div>
        </div>

        <!-- One line, not a card: it confirms what is about to be added, and
             that is all it has to do. Stock never blocks a sale; the seller is
             only told about it. -->
        <p v-if="selectedVariant" class="till-chosen">
          <strong>{{ variantLabel }}</strong>
          <span
            class="till-stock"
            :class="{ 'is-empty': selectedVariant.on_hand <= 0 }"
            :title="selectedVariant.on_hand <= 0 ? t('sales.stockWarning') : ''"
          >{{ t('sales.inStock', { count: selectedVariant.on_hand }) }}</span>
        </p>

        <div v-if="variantPhotos.length" class="variant-photo-preview">
          <img
            v-for="(url, index) in variantPhotos"
            :key="url"
            :src="url"
            :alt="t('sales.variantPhotoAlt', { variant: variantLabel, index: index + 1 })"
            loading="lazy"
          />
        </div>
      </div>

      <!-- Anchored: price, amount and "add" never scroll away from the
           variant they belong to. -->
      <footer class="till-compose">
        <label class="till-price">
          {{ t('sales.unitPrice') }}
          <input v-model="unitPriceInput" inputmode="decimal" :disabled="!selectedVariant" />
        </label>
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
        <button
          class="secondary-button till-add"
          type="button"
          :disabled="!canAddToCart"
          @click="addToCart"
        >{{ t('sales.addToCart') }}</button>
      </footer>
    </section>

    <!-- The receipt rail. It is the sale itself: what is on it, what it costs,
         and the one control that books it — always in the same place, never
         pushed off screen by anything above it. -->
    <aside class="till-column till-rail">
      <header class="till-rail-head">
        <span>{{ t('sales.receiptId') }}</span>
        <strong>{{ receiptId || t('sales.receiptLoading') }}</strong>
      </header>

      <div class="till-methods" role="group" :aria-label="t('sales.paymentMethod')">
        <button
          v-for="method in paymentMethods"
          :key="method"
          type="button"
          class="option-choice till-method"
          :class="{ selected: paymentMethod === method }"
          @click="paymentMethod = method"
        >
          <PaymentMethodIcon :method="method" />
          <span>{{ method }}</span>
        </button>
      </div>

      <div class="till-scroll till-lines" aria-live="polite">
          <div v-if="!basket.length" class="till-empty">{{ t('sales.cartEmpty') }}</div>
          <div v-for="(line, index) in basket" :key="`${line.variantId}-${index}`" class="till-line">
            <span class="till-line-label">{{ line.label }}</span>
            <b>{{ format(line.quantity * line.unitPriceCents) }}</b>
            <small class="till-line-unit">{{ t('sales.perUnit', { price: format(line.unitPriceCents) }) }}</small>

            <!-- Correcting a miscount is the most common fix at a stand, so it
                 happens on the line itself. At one, taking one away is the same
                 as removing the position, and the icon says so. -->
            <span class="till-line-stepper">
              <button
                type="button"
                :class="{ 'is-remove': line.quantity <= 1 }"
                :aria-label="line.quantity <= 1 ? t('sales.removeLine') : t('common.decrease')"
                @click="stepLine(index, -1)"
              >
                <svg
                  v-if="line.quantity <= 1"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="1.8"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  aria-hidden="true"
                  focusable="false"
                >
                  <path d="M4 7h16M10 4h4M9.5 7v11M14.5 7v11" />
                  <path d="M6 7l1 12.5A1.5 1.5 0 0 0 8.5 21h7a1.5 1.5 0 0 0 1.5-1.5L18 7" />
                </svg>
                <span v-else aria-hidden="true">−</span>
              </button>
              <span class="till-line-qty">{{ line.quantity }}</span>
              <button
                type="button"
                :aria-label="t('common.increase')"
                @click="stepLine(index, 1)"
              >+</button>
            </span>
          </div>


      </div>

      <!-- The foot never scrolls: the total and the booking action are the two
           things a seller must always be able to see and reach. -->
      <footer class="till-foot">
        <div class="till-total">
          <span>{{ t('sales.total') }}</span>
          <strong>{{ format(basketTotalCents) }}</strong>
        </div>
        <!-- Counting cash happens here, not behind a dialog: a customer hands
             over more than the total often enough that hiding the field would
             cost a tap on most sales, and the surplus is the band's donation. -->
        <div v-if="!isOrder" class="till-given">
          <label>
            <span>{{ t('sales.amountGiven') }}</span>
            <input
              v-model="amountGivenInput"
              inputmode="decimal"
              :placeholder="format(basketTotalCents)"
            />
          </label>
          <div v-if="surplusCents > 0" class="till-surplus">
            <p class="till-change" :class="{ 'is-donation': surplusMode === 'donation' }">
              <span>{{ surplusMode === 'donation' ? t('sales.donationLabel') : t('sales.changeLabel') }}</span>
              <strong>{{ format(surplusCents) }}</strong>
            </p>
            <button
              class="compact-button till-surplus-toggle"
              type="button"
              @click="surplusMode = surplusMode === 'donation' ? 'change' : 'donation'"
            >
              {{ surplusMode === 'donation' ? t('sales.giveChange') : t('sales.keepAsDonation') }}
            </button>
          </div>
        </div>
        <button
          class="secondary-button full-width till-sheet-open"
          :class="{ 'is-set': sheetTouched }"
          type="button"
          @click="sheetOpen = true"
        >{{ t('sales.saleDetails') }}</button>

        <!-- Showing a code first is the normal order for a transfer or PayPal:
             the money has to arrive before the sale is booked. -->
        <button
          v-if="qrOffered"
          class="secondary-button full-width"
          type="button"
          :disabled="!canBook || qrBusy"
          @click="showPaymentQr"
        >
          {{ t('sales.showPaymentQr') }}
        </button>
        <button
          v-if="!isOrder"
          class="primary-button full-width till-book"
          type="button"
          :disabled="!canBook"
          @click="book()"
        >{{ t('sales.book') }}</button>

        <!-- Two outcomes, two buttons. Which one it is decides where the sale
             lands afterwards, and that is too consequential for a checkbox
             somebody has to remember to read. -->
        <button
          :class="isOrder ? 'primary-button full-width till-book' : 'secondary-button full-width'"
          type="button"
          :disabled="!canBook"
          @click="shipOpen = true"
        >{{ isOrder ? t('sales.createOrder') : t('sales.bookShipment') }}</button>
      </footer>
    </aside>


    <!-- Everything a sale can carry beyond its items. It is not part of the
         receipt — a receipt is what was sold — so it lives one tap away
         instead of between the last line and the total. -->
    <dialog v-if="sheetOpen" class="till-sheet confirmation-dialog" open>
      <div class="till-sheet-head">
        <h2>{{ t('sales.saleDetails') }}</h2>
        <button class="compact-button" type="button" @click="sheetOpen = false">
          {{ t('common.close') }}
        </button>
      </div>

        <section class="till-sheet-group">
            <div class="field-grid">
              <label>
                {{ t('sales.event') }}
                <span class="event-picker">
                  <select v-model.number="selectedEventId">
                    <option :value="0">{{ t('sales.noEvent') }}</option>
                    <option v-for="event in events" :key="event.id" :value="event.id">{{ event.name }}</option>
                  </select>
                  <button
                    class="compact-button"
                    type="button"
                    :aria-label="t('sales.newEvent')"
                    @click="newEventName = ''"
                    v-if="newEventName === null"
                  >+</button>
                </span>
              </label>
            </div>

            <div v-if="newEventName !== null" class="new-event-row">
              <input
                v-model="newEventName"
                :placeholder="t('sales.newEventPlaceholder')"
                @keyup.enter="createEvent"
              />
              <button class="secondary-button" type="button" :disabled="busy" @click="createEvent">
                {{ t('sales.createEvent') }}
              </button>
              <button class="compact-button" type="button" @click="newEventName = null">
                {{ t('common.cancel') }}
              </button>
            </div>

            <label>{{ t('sales.soldBy') }}<input v-model="soldBy" /></label>

            <label>{{ t('common.comment') }}<textarea v-model="comment" rows="2" /></label>
        </section>
    </dialog>

    <dialog v-if="shipOpen" class="confirmation-dialog till-ship" open>
      <form class="stack-form" @submit.prevent="bookShipment">
        <div>
          <p class="eyebrow">{{ format(basketTotalCents) }}</p>
          <h2>{{ t('sales.shipmentTitle') }}</h2>
          <p>{{ t('sales.shipmentIntro') }}</p>
        </div>

        <label>
          {{ t('sales.customerName') }}
          <input v-model="shipName" autocomplete="name" required autofocus />
        </label>
        <label>
          {{ t('sales.customerAddress') }}
          <textarea v-model="shipAddress" rows="3" autocomplete="street-address" required />
        </label>
        <label class="checkbox-row">
          <input v-model="shipPayLater" type="checkbox" />
          <span>{{ t('sales.payLater') }}</span>
        </label>

        <div class="dialog-actions">
          <button class="secondary-button" type="button" @click="shipOpen = false">
            {{ t('common.cancel') }}
          </button>
          <button class="primary-button" type="submit" :disabled="!shipReady || busy">
            {{ isOrder ? t('sales.createOrder') : t('sales.bookShipment') }}
          </button>
        </div>
      </form>
    </dialog>

    <dialog v-if="qrIntent" class="payment-qr-dialog confirmation-dialog" open>
      <div>
        <p class="eyebrow">{{ qrIntent.method }}</p>
        <h2>{{ t('sales.paymentQrTitle') }}</h2>
        <p>{{ t('sales.paymentQrIntro') }}</p>
      </div>
      <div class="payment-qr-image-wrap">
        <img :src="qrIntent.image_data_uri" :alt="t('sales.paymentQrAlt')" />
      </div>
      <div class="payment-qr-summary">
        <span>{{ t('sales.receiptId') }} {{ qrIntent.receipt_id }}</span>
        <strong>{{ format(qrIntent.amount_cents) }}</strong>
      </div>
      <!-- Read aloud when a camera refuses to focus. -->
      <p class="muted payment-qr-hint">{{ qrIntent.payload_hint }}</p>
      <div class="dialog-actions payment-qr-actions">
        <button class="payment-qr-cancel" type="button" :disabled="qrBusy" @click="cancelPaymentQr">
          {{ t('sales.paymentQrCancel') }}
        </button>
        <button class="payment-qr-confirm" type="button" :disabled="qrBusy" @click="confirmPaymentQr">
          {{ t('sales.paymentQrConfirm') }}
        </button>
      </div>
    </dialog>
  </main>
</template>

<style scoped>
/*
 * The till.
 *
 * This page is not a document, it is an instrument: three fixed zones that
 * fill the screen exactly once, where only the inner lists scroll. A seller
 * with a queue must never scroll the page to reach the button that books, and
 * the total must never leave the screen.
 *
 * Vertical rhythm is driven by --till-gap so the whole thing tightens on a
 * short screen instead of overflowing.
 */
.till {
  --till-gap: 14px;
  display: grid;
  grid-template-columns: minmax(210px, 0.72fr) minmax(300px, 1.35fr) minmax(290px, 0.95fr);
  gap: var(--till-gap);
  align-items: stretch;
  width: 100%;
  max-width: 1600px;
  margin: 0 auto;
  padding: var(--till-gap);
  /* The header is sticky; the till claims exactly the rest of the viewport. */
  height: calc(100dvh - var(--till-offset, 66px));
}

.till-column {
  display: flex;
  flex-direction: column;
  /* Without this a flex child refuses to shrink and the inner scroll never
     engages — the page grows instead. */
  min-height: 0;
  padding: 14px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--panel);
}

.till-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  overscroll-behavior: contain;
  -webkit-overflow-scrolling: touch;
}

/* The note claims a row of its own; the columns share whatever is left, so the
   till keeps filling the screen exactly once. */
.till.has-note {
  grid-template-rows: auto minmax(0, 1fr);
}

.till-mode-note {
  grid-column: 1 / -1;
  margin: 0;
  padding: 10px 14px;
  border: 1px solid var(--accent-dark);
  border-radius: 10px;
  background: var(--selection-hover);
  color: var(--text);
  font-size: 0.88rem;
}

.till-column-head {
  display: grid;
  gap: 10px;
  margin-bottom: 12px;
}

.till-column-head h2 {
  margin: 0;
  font-size: 0.82rem;
  font-weight: 700;
  letter-spacing: 0.14em;
  text-transform: uppercase;
  color: var(--muted);
}

.till-filter {
  width: 100%;
}

/* --- articles ---------------------------------------------------------- */

.till-articles .button-list {
  max-height: none;
  gap: 8px;
}

/* The stock figure is a number, not a sentence: it is read in a glance while
   the other hand is holding a shirt. */
.till-articles .selection-button small {
  min-width: 2.4ch;
  padding: 3px 8px;
  border-radius: 999px;
  background: var(--panel-muted);
  font-variant-numeric: tabular-nums;
  font-weight: 700;
  text-align: center;
}

/* --- variant ----------------------------------------------------------- */

/*
 * The option groups own the middle column's height instead of sitting at the
 * top of it. A tablet had 400px of nothing between the last option and the
 * price field; spending it on bigger targets is the cheapest speed there is —
 * a larger tile is a faster, more certain tap.
 */
.till-variant .till-scroll {
  display: flex;
  flex-direction: column;
}

.option-groups {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 18px;
  min-height: 0;
}

.option-group {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-height: 0;
}

.option-group h3 {
  margin: 0 0 8px;
  font-size: 0.82rem;
  font-weight: 700;
  letter-spacing: 0.12em;
  text-transform: uppercase;
  color: var(--muted);
}

.option-choices {
  display: grid;
  flex: 1;
  grid-template-columns: repeat(auto-fit, minmax(112px, 1fr));
  gap: 10px;
  /* Rows share the height rather than clustering at the top. */
  grid-auto-rows: minmax(56px, 1fr);
  align-content: stretch;
}

.option-choices .option-choice {
  display: grid;
  place-items: center;
  padding: 10px;
  font-size: 1.05rem;
  font-weight: 650;
}

.till-chosen {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  justify-content: space-between;
  gap: 10px;
  margin: 16px 0 0;
  padding: 12px 14px;
  border: 1px solid var(--accent-dark);
  border-radius: 10px;
  background: var(--selection-hover);
}

.till-chosen strong {
  font-size: 1.02rem;
}

.till-stock {
  padding: 3px 10px;
  border-radius: 999px;
  background: rgba(255, 255, 255, 0.07);
  color: var(--muted);
  font-size: 0.8rem;
  font-variant-numeric: tabular-nums;
  font-weight: 700;
  white-space: nowrap;
}

.till-stock.is-empty {
  background: color-mix(in srgb, var(--warning) 22%, transparent);
  color: var(--warning);
}

.till-compose {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 10px 12px;
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--border);
}

.till-price input {
  font-variant-numeric: tabular-nums;
  font-size: 1.15rem;
  font-weight: 700;
}

.till-add {
  grid-column: 1 / -1;
  min-height: 52px;
  font-size: 1.02rem;
}

/* --- the receipt rail --------------------------------------------------- */

.till-rail {
  position: relative;
  background: var(--option-bg);
}

/* The torn edge. The rail is a receipt, and saying so once at the top is
   enough — nothing else on the page is decorated. */
.till-rail::before {
  content: '';
  position: absolute;
  top: -1px;
  right: -1px;
  left: -1px;
  height: 7px;
  background:
    repeating-linear-gradient(
      -45deg,
      var(--border) 0 6px,
      transparent 6px 12px
    );
  border-radius: var(--radius) var(--radius) 0 0;
}

/* The method decides whether a payment code can be offered at all, so it sits
   on the rail rather than three taps deep in the extra fields. */
.till-methods {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 12px;
}

.till-methods .option-choice {
  display: grid;
  flex: 1 1 0;
  gap: 4px;
  justify-items: center;
  min-width: 72px;
  padding: 9px 8px;
  font-size: 0.78rem;
  line-height: 1.15;
  text-align: center;
}

/* The label stays: an icon alone is a guess, and a wrong payment method is a
   wrong ledger entry. The glyph only makes the row scannable. */
.till-method span {
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 100%;
}

.till-method:not(.selected) :deep(.payment-icon) {
  color: var(--muted);
}

.till-rail-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 10px;
  margin: 6px 0 12px;
  color: var(--muted);
  font-size: 0.74rem;
  letter-spacing: 0.1em;
  text-transform: uppercase;
}

.till-rail-head strong {
  color: var(--text);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 0.92rem;
  letter-spacing: 0;
  text-transform: none;
}

/*
 * A sale is one to three positions, so the rail has height to spare — and
 * spending it here is what makes the controls easy to hit. The line is two
 * rows: what it is and what it costs on top, the price per unit and the
 * amount control below, where nothing has to be squeezed to fit beside a
 * label.
 */
.till-line {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  gap: 8px 14px;
  padding: 16px 0;
  border-bottom: 1px dashed var(--border);
}

.till-line-stepper {
  display: grid;
  grid-template-columns: 54px auto 54px;
  align-items: center;
  justify-self: end;
  border: 1px solid var(--border);
  border-radius: 10px;
  overflow: hidden;
}

.till-line-stepper button {
  display: grid;
  place-items: center;
  min-height: 50px;
  padding: 0;
  border: 0;
  color: var(--text);
  background: var(--panel-muted);
  font-size: 1.3rem;
  line-height: 1;
  touch-action: manipulation;
  user-select: none;
}

.till-line-stepper button:hover { background: #523662; }
.till-line-stepper button:active { background: #5d3d6e; }

/* At one the button no longer subtracts, it deletes — so it stops looking
   like the rest of the stepper. */
.till-line-stepper button.is-remove {
  color: var(--danger);
}

.till-line-stepper button.is-remove:hover {
  background: rgba(242, 121, 131, 0.18);
}

.till-line-stepper svg {
  width: 21px;
  height: 21px;
}

.till-line-qty {
  min-width: 3ch;
  padding: 0 6px;
  font-size: 1.25rem;
  font-variant-numeric: tabular-nums;
  font-weight: 800;
  text-align: center;
}

.till-line-label {
  min-width: 0;
  font-size: 1.08rem;
  font-weight: 650;
  line-height: 1.3;
}

.till-line-unit {
  color: var(--muted);
  font-size: 0.86rem;
  font-variant-numeric: tabular-nums;
}

.till-line b {
  font-size: 1.2rem;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.till-empty {
  padding: 22px 0;
  color: var(--muted);
  text-align: center;
}

.till-foot {
  display: grid;
  gap: 10px;
  margin-top: 12px;
  padding-top: 12px;
  border-top: 2px solid var(--border);
}

/* The total is the largest type on the page. It is the number the customer
   is told and the one the seller checks before booking. */
.till-total {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
}

.till-total span {
  color: var(--muted);
  font-size: 0.78rem;
  letter-spacing: 0.12em;
  text-transform: uppercase;
}

.till-total strong {
  color: var(--text);
  font-size: clamp(1.9rem, 3.4vw, 2.6rem);
  font-variant-numeric: tabular-nums;
  font-weight: 800;
  letter-spacing: -0.03em;
  line-height: 1;
}

.till-given {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 10px 14px;
  align-items: end;
}

.till-given label {
  display: grid;
  gap: 5px;
}

.till-given label span {
  color: var(--muted);
  font-size: 0.78rem;
  letter-spacing: 0.1em;
  text-transform: uppercase;
}

.till-given input {
  font-size: 1.15rem;
  font-variant-numeric: tabular-nums;
  font-weight: 700;
}

/* The surplus is money the band keeps, so it is stated in the colour the app
   uses for that and never hidden behind a hover. */
.till-surplus {
  display: grid;
  gap: 6px;
  justify-items: end;
}

/* Change is the band's money leaving again, a donation is money it keeps; the
   colours say which before the seller reads the label. */
.till-change {
  display: grid;
  gap: 2px;
  margin: 0;
  color: var(--warning);
  text-align: right;
}

.till-change.is-donation {
  color: var(--success);
}

.till-surplus-toggle {
  white-space: nowrap;
}

.till-change span {
  font-size: 0.72rem;
  letter-spacing: 0.1em;
  text-transform: uppercase;
}

.till-change strong {
  font-size: 1.25rem;
  font-variant-numeric: tabular-nums;
  font-weight: 800;
}

.till-book {
  min-height: 62px;
  font-size: 1.12rem;
  letter-spacing: -0.01em;
}

/* A marker, not a badge with a number: the point is only "something here is
   not the default". */
.till-sheet-open.is-set::after {
  content: '';
  width: 9px;
  height: 9px;
  margin-left: 9px;
  border-radius: 50%;
  background: var(--accent-bright);
}

.till-sheet {
  width: min(680px, calc(100vw - 24px));
}

.till-ship {
  width: min(540px, calc(100vw - 24px));
}

.till-sheet-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 6px;
}

.till-sheet-head h2 {
  margin: 0;
}

.till-sheet-group + .till-sheet-group {
  margin-top: 20px;
  padding-top: 18px;
  border-top: 1px solid var(--border);
}

.till-sheet-group h3 {
  margin: 0 0 12px;
  font-size: 0.82rem;
  font-weight: 700;
  letter-spacing: 0.12em;
  text-transform: uppercase;
  color: var(--muted);
}

/* The shared label styling stacks its text above the control, which looks
   wrong for a checkbox; these put the box and its text on one line. */
.checkbox-row {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 10px;
}

.checkbox-row input[type='checkbox'] {
  width: 1.05rem;
  height: 1.05rem;
  accent-color: var(--accent);
}

/* --- touch ------------------------------------------------------------- */

@media (pointer: coarse) {
  .till { --till-gap: 12px; }
  .till-book { min-height: 68px; font-size: 1.18rem; }
  .till-add { min-height: 58px; }
  .till-line { padding: 18px 0; }
  .till-line-stepper { grid-template-columns: 62px auto 62px; }
  .till-line-stepper button { min-height: 58px; font-size: 1.45rem; }
  .till-line-stepper svg { width: 24px; height: 24px; }
  .till-line-qty { font-size: 1.35rem; }
}

/* --- portrait and small landscape --------------------------------------- */

/*
 * Below three columns the rail stops being a column and becomes the bottom of
 * the screen: total and booking in the thumb's reach, the line items one tap
 * away. Choosing an article and its options is a scroll again, which is the
 * right trade — that part is a search, the booking is not.
 */
@media (max-width: 1000px) {
  .till {
    grid-template-columns: minmax(0, 1fr) minmax(0, 1.25fr);
    height: auto;
    min-height: calc(100dvh - var(--till-offset, 66px));
    padding-bottom: calc(var(--till-gap) + 210px);
  }

  .till-articles .button-list { max-height: 46vh; }

  .till-rail {
    position: fixed;
    right: 0;
    bottom: 0;
    left: 0;
    z-index: 9;
    max-height: 62dvh;
    border-radius: var(--radius) var(--radius) 0 0;
    box-shadow: 0 -18px 40px rgba(0, 0, 0, 0.42);
  }

  .till-lines { max-height: 26dvh; }
}

@media (max-width: 700px) {
  .till { grid-template-columns: minmax(0, 1fr); }
  .till-articles .button-list { max-height: 34vh; }
}

/* A tablet lying flat has little height; keep the foot intact and let the
   lists give up their space instead. */
@media (max-height: 620px) {
  .till-total strong { font-size: 1.6rem; }
  .till-book { min-height: 54px; }
}
</style>
