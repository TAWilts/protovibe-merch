<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch, type CSSProperties } from 'vue'
import { useI18n } from 'vue-i18n'

import analyticsImage from '@/assets/landing/interactive-merch-manager/A-analytics.png'
import packingListImage from '@/assets/landing/interactive-merch-manager/B-packliste.png'
import salesImage from '@/assets/landing/interactive-merch-manager/C-verkauf.png'
import offlineImage from '@/assets/landing/interactive-merch-manager/D-offline.png'
import slideshowImage from '@/assets/landing/interactive-merch-manager/E-diashow.png'
import expensesImage from '@/assets/landing/interactive-merch-manager/F-ausgaben.png'
import backgroundImage from '@/assets/landing/interactive-merch-manager/hintergrund.png'
import {
  INTERACTIVE_MERCH_HOTSPOTS,
  INTERACTIVE_MERCH_VIEWBOX,
  type InteractiveMerchHotspot,
  type InteractiveMerchHotspotId,
} from '@/content/landing/interactiveMerchManagerHotspots'

const { locale, t } = useI18n()

const viewport = ref<HTMLElement | null>(null)
const canvas = ref<HTMLElement | null>(null)
const callout = ref<HTMLElement | null>(null)
const selectedId = ref<InteractiveMerchHotspotId>('C')
const previewedId = ref<InteractiveMerchHotspotId | null>(null)
const focusedId = ref<InteractiveMerchHotspotId | null>(null)
const connectorPath = ref('')
let resizeObserver: ResizeObserver | null = null

const activeId = computed(() => previewedId.value ?? selectedId.value)
const activeHotspot = computed(
  () => INTERACTIVE_MERCH_HOTSPOTS.find((hotspot) => hotspot.id === activeId.value)!,
)
const activeTitle = computed(() => t(`landing.interactive.items.${activeId.value}.title`))
const activeDescription = computed(() => t(`landing.interactive.items.${activeId.value}.description`))

const artwork = [
  { id: 'A', source: analyticsImage },
  { id: 'B', source: packingListImage },
  { id: 'C', source: salesImage },
  { id: 'D', source: offlineImage },
  { id: 'E', source: slideshowImage },
  { id: 'F', source: expensesImage },
] as const

function percent(value: number, total: number) {
  return `${(value / total) * 100}%`
}

function hotspotStyle(hotspot: InteractiveMerchHotspot): CSSProperties {
  const [x, y, width, height] = hotspot.hit
  return {
    left: percent(x, INTERACTIVE_MERCH_VIEWBOX.width),
    top: percent(y, INTERACTIVE_MERCH_VIEWBOX.height),
    width: percent(width, INTERACTIVE_MERCH_VIEWBOX.width),
    height: percent(height, INTERACTIVE_MERCH_VIEWBOX.height),
    zIndex: hotspot.priority,
    '--pin-x': percent(hotspot.anchor[0] - x, width),
    '--pin-y': percent(hotspot.anchor[1] - y, height),
  } as CSSProperties
}

function hotspotLabel(id: InteractiveMerchHotspotId) {
  return `${t(`landing.interactive.items.${id}.title`)}: ${t(`landing.interactive.items.${id}.description`)}`
}

function calculateConnector() {
  const canvasRect = canvas.value?.getBoundingClientRect()
  const calloutRect = callout.value?.getBoundingClientRect()
  if (!canvasRect?.width || !canvasRect.height || !calloutRect) {
    connectorPath.value = ''
    return
  }

  const scaleX = INTERACTIVE_MERCH_VIEWBOX.width / canvasRect.width
  const scaleY = INTERACTIVE_MERCH_VIEWBOX.height / canvasRect.height
  const left = (calloutRect.left - canvasRect.left) * scaleX
  const right = (calloutRect.right - canvasRect.left) * scaleX
  const top = (calloutRect.top - canvasRect.top) * scaleY
  const bottom = (calloutRect.bottom - canvasRect.top) * scaleY
  const centerX = (left + right) / 2
  const centerY = (top + bottom) / 2
  const [x, y] = activeHotspot.value.anchor

  if (x >= left && x <= right) {
    connectorPath.value = `M${x} ${y} V${centerY}`
  } else if (y >= top && y <= bottom) {
    connectorPath.value = `M${x} ${y} H${centerX}`
  } else {
    connectorPath.value = `M${x} ${y} V${centerY} H${centerX}`
  }
}

function preview(id: InteractiveMerchHotspotId) {
  previewedId.value = id
}

function stopPointerPreview(id: InteractiveMerchHotspotId) {
  if (focusedId.value !== id && previewedId.value === id) previewedId.value = null
}

function focus(id: InteractiveMerchHotspotId) {
  focusedId.value = id
  previewedId.value = id
}

function blur(id: InteractiveMerchHotspotId) {
  if (focusedId.value === id) focusedId.value = null
  if (previewedId.value === id) previewedId.value = null
}

function select(hotspot: InteractiveMerchHotspot) {
  selectedId.value = hotspot.id
  if (window.innerWidth <= 760) scrollToHotspot(hotspot)
}

function scrollToHotspot(hotspot: InteractiveMerchHotspot) {
  if (!viewport.value || !canvas.value || typeof viewport.value.scrollTo !== 'function') return
  const sceneWidth = canvas.value.getBoundingClientRect().width
  const center = ((hotspot.hit[0] + hotspot.hit[2] / 2) / INTERACTIVE_MERCH_VIEWBOX.width) * sceneWidth
  const reduceMotion = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false
  viewport.value.scrollTo({
    left: Math.max(0, center - viewport.value.clientWidth / 2),
    behavior: reduceMotion ? 'auto' : 'smooth',
  })
}

watch(
  [activeId, () => locale.value],
  async () => {
    await nextTick()
    calculateConnector()
  },
  { flush: 'post' },
)

onMounted(async () => {
  await nextTick()
  calculateConnector()
  if (typeof ResizeObserver !== 'undefined') {
    resizeObserver = new ResizeObserver(calculateConnector)
    if (canvas.value) resizeObserver.observe(canvas.value)
    if (callout.value) resizeObserver.observe(callout.value)
  }
  window.addEventListener('resize', calculateConnector)
})

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  window.removeEventListener('resize', calculateConnector)
})
</script>

<template>
  <section id="features" class="landing-section interactive-merch-section">
    <div class="interactive-heading">
      <p class="interactive-kicker">{{ t('landing.interactive.kicker') }}</p>
      <h2>{{ t('landing.interactive.title') }}</h2>
      <p>{{ t('landing.interactive.lead') }}</p>
    </div>

    <div class="interactive-experience">
      <div ref="viewport" class="interactive-viewport">
        <div ref="canvas" class="interactive-canvas">
          <div
            class="interactive-scene"
            role="img"
            :aria-label="t('landing.interactive.sceneDescription')"
          >
            <img class="art-layer art-background" :src="backgroundImage" alt="" draggable="false" />
            <img
              v-for="layer in artwork"
              :key="layer.id"
              class="art-layer"
              :class="{ 'is-highlighted': activeId === layer.id }"
              :data-layer="layer.id"
              :src="layer.source"
              alt=""
              draggable="false"
            />
          </div>

          <svg
            class="connector-layer"
            :viewBox="`0 0 ${INTERACTIVE_MERCH_VIEWBOX.width} ${INTERACTIVE_MERCH_VIEWBOX.height}`"
            aria-hidden="true"
          >
            <path data-connector :d="connectorPath" />
          </svg>

          <div class="hotspot-layer">
            <button
              v-for="hotspot in INTERACTIVE_MERCH_HOTSPOTS"
              :key="hotspot.id"
              type="button"
              class="interactive-hotspot"
              :data-hotspot="hotspot.id"
              :data-slug="hotspot.slug"
              :style="hotspotStyle(hotspot)"
              :aria-label="hotspotLabel(hotspot.id)"
              :aria-pressed="activeId === hotspot.id"
              aria-describedby="interactive-merch-instructions"
              @mouseenter="preview(hotspot.id)"
              @mouseleave="stopPointerPreview(hotspot.id)"
              @focus="focus(hotspot.id)"
              @blur="blur(hotspot.id)"
              @click="select(hotspot)"
            >
              <span class="hotspot-marker" aria-hidden="true"></span>
            </button>
          </div>
        </div>
      </div>

      <article ref="callout" class="interactive-callout" aria-live="polite" aria-atomic="true">
        <p class="callout-kicker">Merch Manager</p>
        <h3>{{ activeTitle }}</h3>
        <p>{{ activeDescription }}</p>
      </article>
    </div>

    <p id="interactive-merch-instructions" class="sr-only">
      {{ t('landing.interactive.instructions') }}
    </p>
  </section>
</template>

<style scoped>
.interactive-merch-section {
  padding: 105px 0;
}

.interactive-heading {
  max-width: 760px;
  margin-bottom: 48px;
}

.interactive-kicker {
  margin: 0 0 15px;
  color: var(--landing-accent-bright, #e58bea);
  font-size: 0.72rem;
  font-weight: 850;
  letter-spacing: 0.14em;
  text-transform: uppercase;
}

.interactive-heading h2 {
  margin: 0;
  color: var(--landing-text, #f4f4f6);
  font-size: clamp(2.2rem, 5vw, 4.4rem);
  line-height: 1;
  letter-spacing: -0.06em;
}

.interactive-heading > p:last-child {
  max-width: 670px;
  margin: 18px 0 0;
  color: var(--landing-muted, #aea3b7);
  line-height: 1.65;
}

.interactive-experience {
  position: relative;
  overflow: hidden;
  border: 1px solid var(--landing-line, #3b3f48);
  border-radius: 26px;
  background: #f9f9fe;
  box-shadow: 0 32px 90px rgba(0, 0, 0, 0.28);
  isolation: isolate;
}

.interactive-viewport {
  overflow: hidden;
}

.interactive-canvas {
  position: relative;
  width: 100%;
  aspect-ratio: 1774 / 887;
  overflow: hidden;
  background: #f9f9fe;
}

.interactive-scene,
.connector-layer,
.hotspot-layer {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
}

.interactive-scene {
  z-index: 1;
  overflow: hidden;
  background: #f9f9fe;
}

.art-layer {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  object-fit: fill;
  opacity: 0.32;
  pointer-events: none;
  transition: opacity 190ms ease;
  user-select: none;
  -webkit-user-drag: none;
}

.art-background {
  opacity: 0.55;
}

.art-layer.is-highlighted {
  opacity: 1;
}

.connector-layer {
  z-index: 5;
  overflow: visible;
  pointer-events: none;
}

.connector-layer path {
  fill: none;
  stroke: var(--landing-accent, #d56cdb);
  stroke-width: 8;
  stroke-linecap: round;
  stroke-linejoin: round;
  filter: drop-shadow(0 4px 3px rgba(83, 36, 99, 0.2));
  transition: d 180ms ease;
}

.hotspot-layer {
  z-index: 8;
  pointer-events: none;
}

.interactive-hotspot {
  position: absolute;
  padding: 0;
  border: 2px solid transparent;
  border-radius: 18px;
  background: transparent;
  cursor: pointer;
  pointer-events: auto;
  touch-action: manipulation;
}

.interactive-hotspot:focus-visible {
  border-color: #e58bea;
  outline: 4px solid rgba(213, 108, 219, 0.3);
  outline-offset: 3px;
}

.hotspot-marker {
  position: absolute;
  top: var(--pin-y);
  left: var(--pin-x);
  width: clamp(17px, 1.65vw, 25px);
  height: clamp(17px, 1.65vw, 25px);
  border: 3px solid #fff;
  border-radius: 50%;
  background: var(--landing-accent, #d56cdb);
  box-shadow: 0 0 0 5px rgba(213, 108, 219, 0.24), 0 5px 14px rgba(83, 36, 99, 0.25);
  pointer-events: none;
  transform: translate(-50%, -50%);
  transition: transform 180ms ease, box-shadow 180ms ease;
}

.interactive-hotspot:hover .hotspot-marker,
.interactive-hotspot:focus-visible .hotspot-marker,
.interactive-hotspot[aria-pressed='true'] .hotspot-marker {
  box-shadow: 0 0 0 9px rgba(213, 108, 219, 0.3), 0 7px 18px rgba(83, 36, 99, 0.28);
  transform: translate(-50%, -50%) scale(1.18);
}

.interactive-hotspot[aria-pressed='true'] .hotspot-marker::after {
  position: absolute;
  inset: 4px;
  border-radius: inherit;
  background: #241c27;
  content: '';
}

.interactive-callout {
  position: absolute;
  z-index: 10;
  top: 6.5%;
  left: 20.5%;
  width: min(25rem, 27%);
  min-width: 260px;
  padding: clamp(15px, 1.5vw, 24px);
  border: 1px solid #55405c;
  border-left: 6px solid var(--landing-accent, #d56cdb);
  border-radius: 18px;
  color: #f4f4f6;
  background: #241c27;
  box-shadow: 0 18px 55px rgba(62, 27, 76, 0.24);
}

.callout-kicker {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0 0 7px;
  color: var(--landing-accent-bright, #e58bea);
  font-size: 0.7rem;
  font-weight: 850;
  letter-spacing: 0.12em;
  text-transform: uppercase;
}

.callout-kicker::before {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: var(--landing-accent, #d56cdb);
  content: '';
}

.interactive-callout h3 {
  margin: 0;
  color: var(--landing-accent-bright, #e58bea);
  font-size: clamp(1.18rem, 2vw, 2rem);
  line-height: 1.06;
  letter-spacing: -0.04em;
}

.interactive-callout > p:last-child {
  margin: 10px 0 0;
  color: #e0d5e5;
  font-size: clamp(0.84rem, 1vw, 1rem);
  line-height: 1.5;
}

.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}

@media (max-width: 1050px) and (min-width: 761px) {
  .interactive-callout {
    top: 3%;
    left: 19%;
    width: 30%;
    min-width: 230px;
  }

  .interactive-callout > p:last-child {
    margin-top: 6px;
    line-height: 1.35;
  }
}

@media (max-width: 760px) {
  .interactive-experience {
    overflow: visible;
    border-radius: 18px;
    background: transparent;
  }

  .interactive-viewport {
    overflow-x: auto;
    border-radius: 18px;
    overscroll-behavior-x: contain;
    scrollbar-width: thin;
  }

  .interactive-canvas {
    width: 980px;
    max-width: none;
  }

  .connector-layer {
    display: none;
  }

  .interactive-callout {
    position: relative;
    inset: auto;
    width: auto;
    min-width: 0;
    margin: 14px 0 0;
    padding: 18px;
    border-radius: 14px;
  }

  .interactive-callout h3 {
    font-size: 1.35rem;
  }

  .hotspot-marker {
    width: 28px;
    height: 28px;
  }
}

@media (max-width: 620px) {
  .interactive-merch-section {
    padding: 72px 0;
  }
}

@media (prefers-reduced-motion: reduce) {
  .art-layer,
  .connector-layer path,
  .hotspot-marker {
    transition: none;
  }
}
</style>
