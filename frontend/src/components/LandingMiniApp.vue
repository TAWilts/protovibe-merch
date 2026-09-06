<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { marketingLocale } from '@/i18n'

type DemoTab = 'sale' | 'inventory' | 'balances' | 'slideshow'

interface DemoProduct {
  id: number
  name: string
  variant: string
  priceCents: number
  purchaseCents: number
  stock: number
  initialStock: number
  image: string
}

const { t } = useI18n()

const tab = ref<DemoTab>('sale')
const products = reactive<DemoProduct[]>([
  {
    id: 1,
    name: 'Tour Shirt',
    variant: 'M · Black',
    priceCents: 2500,
    purchaseCents: 900,
    stock: 8,
    initialStock: 8,
    image: '/demo-products/shirt.jpg',
  },
  {
    id: 2,
    name: 'Vinyl',
    variant: 'Black',
    priceCents: 2200,
    purchaseCents: 1100,
    stock: 6,
    initialStock: 6,
    image: '/demo-products/vinyl.jpg',
  },
  {
    id: 3,
    name: 'Tote Bag',
    variant: 'Natural',
    priceCents: 1500,
    purchaseCents: 500,
    stock: 12,
    initialStock: 12,
    image: '/demo-products/tote.jpg',
  },
  {
    id: 4,
    name: 'Hoodie',
    variant: 'L · Black',
    priceCents: 4500,
    purchaseCents: 1900,
    stock: 4,
    initialStock: 4,
    image: '/demo-products/hoodie.jpg',
  },
])

const basket = reactive<Record<number, number>>({})
const completedSales = ref(0)
const extraRevenueCents = ref(0)
const lastReceipt = ref('')
const slideshowIndex = ref(0)

const baselineRevenueCents = 62800
const baselinePurchaseCostCents = 28700
const baselineSoldUnits = 31

const tabs: DemoTab[] = ['sale', 'inventory', 'balances', 'slideshow']

const basketLines = computed(() =>
  products
    .filter((product) => (basket[product.id] ?? 0) > 0)
    .map((product) => ({
      product,
      quantity: basket[product.id] ?? 0,
      totalCents: product.priceCents * (basket[product.id] ?? 0),
    })),
)

const basketTotalCents = computed(() =>
  basketLines.value.reduce((sum, line) => sum + line.totalCents, 0),
)

const soldUnits = computed(() =>
  products.reduce((sum, product) => sum + product.initialStock - product.stock, 0),
)

const stockUnits = computed(() =>
  products.reduce((sum, product) => sum + product.stock, 0),
)

const revenueCents = computed(() => baselineRevenueCents + extraRevenueCents.value)
const profitCents = computed(() => revenueCents.value - baselinePurchaseCostCents)
const activeSlide = computed(() => products[slideshowIndex.value % products.length])

function formatMoney(cents: number) {
  return new Intl.NumberFormat(
    marketingLocale() === 'en' ? 'en-GB' : 'de-DE',
    { style: 'currency', currency: 'EUR' },
  ).format(cents / 100)
}

function add(product: DemoProduct) {
  const current = basket[product.id] ?? 0
  if (current >= product.stock) return
  basket[product.id] = current + 1
  lastReceipt.value = ''
}

function remove(product: DemoProduct) {
  const current = basket[product.id] ?? 0
  if (current <= 1) {
    delete basket[product.id]
  } else {
    basket[product.id] = current - 1
  }
}

function bookSale() {
  if (!basketLines.value.length) return
  for (const line of basketLines.value) {
    line.product.stock -= line.quantity
  }
  extraRevenueCents.value += basketTotalCents.value
  completedSales.value += 1
  lastReceipt.value = `VK-DEMO-${String(completedSales.value).padStart(3, '0')}`
  for (const key of Object.keys(basket)) delete basket[Number(key)]
}

function reset() {
  for (const product of products) product.stock = product.initialStock
  for (const key of Object.keys(basket)) delete basket[Number(key)]
  completedSales.value = 0
  extraRevenueCents.value = 0
  lastReceipt.value = ''
  slideshowIndex.value = 0
  tab.value = 'sale'
}

function nextSlide() {
  slideshowIndex.value = (slideshowIndex.value + 1) % products.length
}

function previousSlide() {
  slideshowIndex.value = (slideshowIndex.value - 1 + products.length) % products.length
}

let slideshowTimer: number | undefined

onMounted(() => {
  slideshowTimer = window.setInterval(() => {
    if (tab.value === 'slideshow' && !document.hidden) nextSlide()
  }, 2800)
})

onBeforeUnmount(() => {
  if (slideshowTimer !== undefined) window.clearInterval(slideshowTimer)
})
</script>

<template>
  <section class="mini-app-section">
    <div class="mini-app-heading">
      <div>
        <p class="mini-kicker">{{ t('landing.miniApp.kicker') }}</p>
        <h2>{{ t('landing.miniApp.title') }}</h2>
        <p>{{ t('landing.miniApp.lead') }}</p>
      </div>
      <button type="button" class="mini-reset" @click="reset">
        ↺ {{ t('landing.miniApp.reset') }}
      </button>
    </div>

    <div class="mini-app-window">
      <div class="mini-topbar">
        <div class="mini-brand">
          <span>M</span>
          <div>
            <strong>Merch Manager</strong>
            <small>{{ t('landing.miniApp.demoBand') }}</small>
          </div>
        </div>
        <span class="mini-local-note">● {{ t('landing.miniApp.localOnly') }}</span>
      </div>

      <nav class="mini-tabs" :aria-label="t('landing.miniApp.navigation')">
        <button
          v-for="entry in tabs"
          :key="entry"
          type="button"
          :class="{ active: tab === entry }"
          @click="tab = entry"
        >
          {{ t(`landing.miniApp.tabs.${entry}`) }}
        </button>
      </nav>

      <div class="mini-stage">
        <div v-if="tab === 'sale'" class="sale-demo">
          <div class="product-picker">
            <div class="mini-section-head">
              <div>
                <small>{{ t('landing.miniApp.sale.choose') }}</small>
                <strong>{{ t('landing.miniApp.sale.products') }}</strong>
              </div>
              <span>{{ stockUnits }} {{ t('landing.miniApp.pieces') }}</span>
            </div>

            <div class="product-cards">
              <button
                v-for="product in products"
                :key="product.id"
                class="product-card"
                type="button"
                :disabled="product.stock <= 0 || (basket[product.id] ?? 0) >= product.stock"
                @click="add(product)"
              >
                <img :src="product.image" alt="" />
                <span>
                  <strong>{{ product.name }}</strong>
                  <small>{{ product.variant }}</small>
                </span>
                <span class="product-price">
                  {{ formatMoney(product.priceCents) }}
                  <small>{{ product.stock }} {{ t('landing.miniApp.inStock') }}</small>
                </span>
              </button>
            </div>
          </div>

          <aside class="demo-basket">
            <div class="mini-section-head">
              <div>
                <small>{{ t('landing.miniApp.sale.current') }}</small>
                <strong>{{ t('landing.miniApp.sale.cart') }}</strong>
              </div>
              <span>{{ basketLines.reduce((sum, line) => sum + line.quantity, 0) }}</span>
            </div>

            <div v-if="basketLines.length" class="basket-lines">
              <div v-for="line in basketLines" :key="line.product.id" class="basket-line">
                <div>
                  <strong>{{ line.product.name }}</strong>
                  <small>{{ line.product.variant }}</small>
                </div>
                <div class="basket-quantity">
                  <button type="button" @click="remove(line.product)">−</button>
                  <span>{{ line.quantity }}</span>
                  <button type="button" @click="add(line.product)">+</button>
                </div>
                <strong>{{ formatMoney(line.totalCents) }}</strong>
              </div>
            </div>
            <p v-else class="basket-empty">{{ t('landing.miniApp.sale.empty') }}</p>

            <div class="basket-total">
              <span>{{ t('landing.miniApp.sale.total') }}</span>
              <strong>{{ formatMoney(basketTotalCents) }}</strong>
            </div>
            <div class="payment-pills">
              <span class="active">Cash</span><span>PayPal</span><span>Bank</span>
            </div>
            <button
              class="book-demo-sale"
              type="button"
              :disabled="!basketLines.length"
              @click="bookSale"
            >
              {{ t('landing.miniApp.sale.book') }}
            </button>
            <div v-if="lastReceipt" class="sale-success" aria-live="polite">
              <span>✓</span>
              <div>
                <strong>{{ t('landing.miniApp.sale.success') }}</strong>
                <small>{{ lastReceipt }}</small>
              </div>
            </div>
          </aside>
        </div>

        <div v-else-if="tab === 'inventory'" class="inventory-view">
          <div class="metric-strip">
            <article>
              <small>{{ t('landing.miniApp.inventory.stock') }}</small>
              <strong>{{ stockUnits }}</strong>
            </article>
            <article>
              <small>{{ t('landing.miniApp.inventory.sold') }}</small>
              <strong>{{ baselineSoldUnits + soldUnits }}</strong>
            </article>
            <article>
              <small>{{ t('landing.miniApp.inventory.low') }}</small>
              <strong>{{ products.filter((product) => product.stock <= 4).length }}</strong>
            </article>
          </div>

          <div class="inventory-table">
            <div class="inventory-row inventory-header">
              <span>{{ t('landing.miniApp.inventory.article') }}</span>
              <span>{{ t('landing.miniApp.inventory.variant') }}</span>
              <span>{{ t('landing.miniApp.inventory.level') }}</span>
              <span>{{ t('landing.miniApp.inventory.status') }}</span>
            </div>
            <div v-for="product in products" :key="product.id" class="inventory-row">
              <span><strong>{{ product.name }}</strong></span>
              <span>{{ product.variant }}</span>
              <span class="stock-meter">
                <i><b :style="{ width: `${Math.max(4, product.stock / product.initialStock * 100)}%` }"></b></i>
                <strong>{{ product.stock }}</strong>
              </span>
              <span>
                <em :class="{ warning: product.stock <= 4 }">
                  {{ product.stock <= 4
                    ? t('landing.miniApp.inventory.reorder')
                    : t('landing.miniApp.inventory.ok') }}
                </em>
              </span>
            </div>
          </div>
        </div>

        <div v-else-if="tab === 'balances'" class="balances-view">
          <div class="balance-cards">
            <article>
              <span>{{ t('landing.miniApp.balances.revenue') }}</span>
              <strong>{{ formatMoney(revenueCents) }}</strong>
              <small>+ {{ formatMoney(extraRevenueCents) }} {{ t('landing.miniApp.balances.demo') }}</small>
            </article>
            <article>
              <span>{{ t('landing.miniApp.balances.cost') }}</span>
              <strong>{{ formatMoney(baselinePurchaseCostCents) }}</strong>
              <small>{{ t('landing.miniApp.balances.purchases') }}</small>
            </article>
            <article class="highlight">
              <span>{{ t('landing.miniApp.balances.balance') }}</span>
              <strong>{{ formatMoney(profitCents) }}</strong>
              <small>{{ t('landing.miniApp.balances.current') }}</small>
            </article>
          </div>

          <div class="balance-chart">
            <div class="chart-copy">
              <small>{{ t('landing.miniApp.balances.trend') }}</small>
              <strong>{{ t('landing.miniApp.balances.liveHint') }}</strong>
            </div>
            <div class="chart-bars" aria-hidden="true">
              <i style="--bar: 34%"></i>
              <i style="--bar: 52%"></i>
              <i style="--bar: 43%"></i>
              <i style="--bar: 69%"></i>
              <i style="--bar: 58%"></i>
              <i style="--bar: 83%"></i>
              <i :style="{ '--bar': `${Math.min(100, 70 + completedSales * 8)}%` }"></i>
            </div>
          </div>
        </div>

        <div v-else class="slideshow-view">
          <div class="slideshow-screen">
            <img :src="activeSlide.image" :alt="activeSlide.name" />
            <div class="slideshow-caption">
              <span>{{ t('landing.miniApp.slideshow.product') }}</span>
              <strong>{{ activeSlide.name }}</strong>
              <small>{{ activeSlide.variant }}</small>
              <b>{{ formatMoney(activeSlide.priceCents) }}</b>
            </div>
            <button class="slide-arrow previous" type="button" @click="previousSlide">‹</button>
            <button class="slide-arrow next" type="button" @click="nextSlide">›</button>
          </div>
          <div class="slide-dots">
            <button
              v-for="(product, index) in products"
              :key="product.id"
              type="button"
              :class="{ active: index === slideshowIndex }"
              :aria-label="product.name"
              @click="slideshowIndex = index"
            ></button>
          </div>
          <p>{{ t('landing.miniApp.slideshow.hint') }}</p>
        </div>
      </div>
    </div>

    <p class="mini-disclaimer">{{ t('landing.miniApp.disclaimer') }}</p>
  </section>
</template>

<style scoped>
.mini-app-section {
  position: relative;
  z-index: 1;
  width: min(1180px, calc(100% - 40px));
  margin: 0 auto;
  padding: 110px 0 70px;
  color: #f8f4fb;
}

.mini-app-heading {
  display: flex;
  justify-content: space-between;
  gap: 28px;
  align-items: end;
  margin-bottom: 28px;
}

.mini-app-heading > div {
  max-width: 720px;
}

.mini-kicker {
  margin: 0 0 12px;
  color: #f19bff;
  font-size: .73rem;
  font-weight: 820;
  letter-spacing: .15em;
  text-transform: uppercase;
}

.mini-app-heading h2 {
  margin: 0;
  font-size: clamp(2.2rem, 5vw, 4.5rem);
  line-height: 1;
  letter-spacing: -.055em;
}

.mini-app-heading p:last-child,
.mini-disclaimer {
  color: #b8adbf;
  line-height: 1.65;
}

.mini-reset {
  flex: 0 0 auto;
  padding: 9px 13px;
  border: 1px solid rgba(255,255,255,.14);
  border-radius: 10px;
  color: #f8f4fb;
  background: rgba(255,255,255,.05);
  cursor: pointer;
}

.mini-app-window {
  overflow: hidden;
  border: 1px solid rgba(255,255,255,.13);
  border-radius: 22px;
  background: rgba(15, 10, 20, .84);
  box-shadow: 0 32px 90px rgba(0,0,0,.35);
}

.mini-topbar {
  min-height: 68px;
  padding: 12px 18px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 18px;
  border-bottom: 1px solid rgba(255,255,255,.08);
  background: rgba(255,255,255,.025);
}

.mini-brand {
  display: flex;
  align-items: center;
  gap: 10px;
}

.mini-brand > span {
  width: 36px;
  height: 36px;
  display: grid;
  place-items: center;
  border-radius: 10px;
  color: #2d0d35;
  background: linear-gradient(135deg, #f5a8ff, #d95ff1);
  font-weight: 900;
}

.mini-brand div {
  display: grid;
}

.mini-brand small,
.mini-local-note,
.mini-section-head small,
.product-card small,
.basket-line small,
.balance-cards small,
.chart-copy small,
.slideshow-caption span,
.slideshow-caption small {
  color: #9f94aa;
}

.mini-local-note {
  font-size: .74rem;
}

.mini-local-note::first-letter {
  color: #72e6ae;
}

.mini-tabs {
  display: flex;
  overflow-x: auto;
  padding: 8px;
  gap: 4px;
  border-bottom: 1px solid rgba(255,255,255,.08);
}

.mini-tabs button {
  min-height: 38px;
  padding: 8px 14px;
  border: 0;
  border-radius: 9px;
  color: #aea4b5;
  background: transparent;
  font-weight: 760;
  white-space: nowrap;
  cursor: pointer;
}

.mini-tabs button.active {
  color: #fff;
  background: rgba(240,142,255,.13);
}

.mini-stage {
  min-height: 520px;
  padding: clamp(16px, 3vw, 30px);
  background:
    radial-gradient(circle at 88% 15%, rgba(129, 58, 163, .16), transparent 24rem),
    rgba(8, 5, 11, .38);
}

.sale-demo {
  display: grid;
  grid-template-columns: minmax(0, 1.45fr) minmax(300px, .72fr);
  gap: 20px;
}

.product-picker,
.demo-basket,
.inventory-view,
.balances-view {
  min-width: 0;
}

.mini-section-head {
  min-height: 48px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
}

.mini-section-head > div {
  display: grid;
  gap: 2px;
}

.product-cards {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

.product-card {
  padding: 10px;
  display: grid;
  grid-template-columns: 64px minmax(0, 1fr) auto;
  gap: 12px;
  align-items: center;
  border: 1px solid rgba(255,255,255,.09);
  border-radius: 13px;
  color: inherit;
  text-align: left;
  background: rgba(255,255,255,.035);
  cursor: pointer;
  transition: transform .16s, border-color .16s, background .16s;
}

.product-card:hover:not(:disabled) {
  transform: translateY(-2px);
  border-color: rgba(240,142,255,.35);
  background: rgba(240,142,255,.07);
}

.product-card:disabled {
  opacity: .45;
  cursor: not-allowed;
}

.product-card img {
  width: 64px;
  height: 64px;
  border-radius: 9px;
  object-fit: cover;
}

.product-card > span {
  min-width: 0;
  display: grid;
  gap: 3px;
}

.product-price {
  text-align: right;
}

.demo-basket {
  padding: 16px;
  border: 1px solid rgba(255,255,255,.09);
  border-radius: 15px;
  background: rgba(255,255,255,.035);
}

.basket-lines {
  display: grid;
  gap: 8px;
  margin: 12px 0;
}

.basket-line {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto auto;
  gap: 10px;
  align-items: center;
  padding: 9px 0;
  border-bottom: 1px solid rgba(255,255,255,.06);
}

.basket-line > div:first-child {
  display: grid;
}

.basket-quantity {
  display: flex;
  align-items: center;
  gap: 6px;
}

.basket-quantity button {
  width: 28px;
  height: 28px;
  border: 1px solid rgba(255,255,255,.1);
  border-radius: 8px;
  color: white;
  background: rgba(255,255,255,.05);
  cursor: pointer;
}

.basket-empty {
  min-height: 130px;
  margin: 0;
  display: grid;
  place-items: center;
  color: #82788a;
  text-align: center;
}

.basket-total {
  margin-top: 10px;
  padding: 14px 0;
  display: flex;
  justify-content: space-between;
  border-top: 1px solid rgba(255,255,255,.12);
  font-size: 1.05rem;
}

.basket-total strong {
  font-size: 1.35rem;
}

.payment-pills {
  display: flex;
  gap: 6px;
  margin-bottom: 12px;
}

.payment-pills span {
  padding: 5px 9px;
  border: 1px solid rgba(255,255,255,.08);
  border-radius: 999px;
  color: #94899c;
  font-size: .72rem;
}

.payment-pills span.active {
  color: #f8eafa;
  border-color: rgba(240,142,255,.25);
  background: rgba(240,142,255,.11);
}

.book-demo-sale {
  width: 100%;
  min-height: 45px;
  border: 0;
  border-radius: 10px;
  color: #2a0b32;
  background: linear-gradient(135deg, #f5a8ff, #d95ff1);
  font-weight: 850;
  cursor: pointer;
}

.book-demo-sale:disabled {
  opacity: .4;
  cursor: not-allowed;
}

.sale-success {
  margin-top: 10px;
  padding: 10px 12px;
  display: flex;
  gap: 10px;
  align-items: center;
  border: 1px solid rgba(82, 210, 145, .24);
  border-radius: 10px;
  background: rgba(82, 210, 145, .08);
}

.sale-success > span {
  color: #72e6ae;
  font-size: 1.3rem;
}

.sale-success div {
  display: grid;
}

.metric-strip,
.balance-cards {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 12px;
  margin-bottom: 20px;
}

.metric-strip article,
.balance-cards article {
  padding: 18px;
  display: grid;
  gap: 6px;
  border: 1px solid rgba(255,255,255,.09);
  border-radius: 14px;
  background: rgba(255,255,255,.035);
}

.metric-strip strong,
.balance-cards strong {
  font-size: clamp(1.45rem, 3vw, 2.15rem);
}

.balance-cards article.highlight {
  border-color: rgba(240,142,255,.24);
  background: rgba(240,142,255,.08);
}

.inventory-table {
  overflow: hidden;
  border: 1px solid rgba(255,255,255,.09);
  border-radius: 14px;
}

.inventory-row {
  min-height: 62px;
  padding: 10px 14px;
  display: grid;
  grid-template-columns: 1.1fr .9fr 1.2fr .65fr;
  gap: 14px;
  align-items: center;
  border-top: 1px solid rgba(255,255,255,.06);
}

.inventory-row:first-child {
  border-top: 0;
}

.inventory-header {
  min-height: 42px;
  color: #968b9e;
  background: rgba(255,255,255,.035);
  font-size: .72rem;
  font-weight: 800;
  text-transform: uppercase;
  letter-spacing: .06em;
}

.stock-meter {
  display: flex;
  gap: 10px;
  align-items: center;
}

.stock-meter i {
  height: 7px;
  flex: 1;
  overflow: hidden;
  border-radius: 999px;
  background: rgba(255,255,255,.08);
}

.stock-meter b {
  display: block;
  height: 100%;
  border-radius: inherit;
  background: linear-gradient(90deg, #b85bcd, #f19bff);
}

.inventory-row em {
  display: inline-flex;
  padding: 5px 8px;
  border-radius: 999px;
  color: #77dfac;
  background: rgba(82,210,145,.08);
  font-size: .72rem;
  font-style: normal;
}

.inventory-row em.warning {
  color: #ffc879;
  background: rgba(255,179,82,.1);
}

.balance-chart {
  min-height: 280px;
  padding: 22px;
  display: grid;
  grid-template-rows: auto 1fr;
  border: 1px solid rgba(255,255,255,.09);
  border-radius: 15px;
  background: rgba(255,255,255,.025);
}

.chart-copy {
  display: grid;
  gap: 4px;
}

.chart-bars {
  height: 190px;
  display: flex;
  align-items: end;
  gap: clamp(8px, 2vw, 22px);
  padding-top: 24px;
}

.chart-bars i {
  height: var(--bar);
  flex: 1;
  min-width: 16px;
  border-radius: 8px 8px 3px 3px;
  background: linear-gradient(to top, rgba(180,74,205,.55), #f19bff);
  box-shadow: 0 0 24px rgba(240,142,255,.09);
  transition: height .28s ease;
}

.slideshow-view {
  min-height: 470px;
  display: grid;
  place-items: center;
  align-content: center;
}

.slideshow-screen {
  position: relative;
  width: min(780px, 100%);
  aspect-ratio: 16 / 8.2;
  overflow: hidden;
  border: 1px solid rgba(255,255,255,.1);
  border-radius: 18px;
  background: #09060b;
  box-shadow: 0 25px 65px rgba(0,0,0,.32);
}

.slideshow-screen > img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  opacity: .76;
  transition: opacity .3s;
}

.slideshow-screen::after {
  position: absolute;
  inset: 0;
  content: '';
  background: linear-gradient(90deg, rgba(4,2,6,.88), transparent 62%);
  pointer-events: none;
}

.slideshow-caption {
  position: absolute;
  z-index: 2;
  top: 50%;
  left: clamp(22px, 6vw, 60px);
  display: grid;
  gap: 5px;
  transform: translateY(-50%);
}

.slideshow-caption strong {
  font-size: clamp(2rem, 6vw, 4rem);
  line-height: 1;
  letter-spacing: -.05em;
}

.slideshow-caption b {
  margin-top: 10px;
  color: #f2a0ff;
  font-size: clamp(1.45rem, 3vw, 2.2rem);
}

.slide-arrow {
  position: absolute;
  z-index: 3;
  top: 50%;
  width: 40px;
  height: 40px;
  border: 1px solid rgba(255,255,255,.18);
  border-radius: 50%;
  color: white;
  background: rgba(0,0,0,.35);
  font-size: 1.6rem;
  cursor: pointer;
  transform: translateY(-50%);
}

.slide-arrow.previous { left: 10px; }
.slide-arrow.next { right: 10px; }

.slide-dots {
  display: flex;
  gap: 7px;
  margin-top: 14px;
}

.slide-dots button {
  width: 8px;
  height: 8px;
  padding: 0;
  border: 0;
  border-radius: 999px;
  background: rgba(255,255,255,.18);
  cursor: pointer;
}

.slide-dots button.active {
  width: 24px;
  background: #ef97fc;
}

.slideshow-view > p {
  margin-bottom: 0;
  color: #9f94aa;
  text-align: center;
}

.mini-disclaimer {
  margin: 16px 0 0;
  font-size: .8rem;
  text-align: center;
}

@media (max-width: 860px) {
  .sale-demo {
    grid-template-columns: 1fr;
  }

  .product-cards {
    grid-template-columns: 1fr;
  }

  .mini-stage {
    min-height: 0;
  }
}

@media (max-width: 640px) {
  .mini-app-section {
    width: min(100% - 24px, 1180px);
    padding-top: 76px;
  }

  .mini-app-heading {
    align-items: start;
    flex-direction: column;
  }

  .mini-topbar {
    align-items: flex-start;
    flex-direction: column;
  }

  .metric-strip,
  .balance-cards {
    grid-template-columns: 1fr;
  }

  .inventory-table {
    border: 0;
    overflow: visible;
  }

  .inventory-header {
    display: none;
  }

  .inventory-row {
    grid-template-columns: 1fr 1fr;
    border: 1px solid rgba(255,255,255,.09);
    border-radius: 12px;
    margin-bottom: 8px;
  }

  .slideshow-screen {
    aspect-ratio: 4 / 3;
  }

  .slideshow-screen::after {
    background: linear-gradient(90deg, rgba(4,2,6,.86), rgba(4,2,6,.2));
  }
}
</style>
