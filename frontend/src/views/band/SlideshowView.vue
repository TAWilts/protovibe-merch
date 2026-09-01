<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { catalogueApi, photosApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { Article, Photo } from '@/api/types'
import { useMoney } from '@/composables/useMoney'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'

/**
 * The shop display, ported from _old/templates/slideshow.html.
 *
 * The gallery is the curation surface; "Produktpalette zeigen" is the thing
 * that runs on a tablet propped up on the merch table. The rotation draws from
 * a shuffled bag so no picture repeats before the others have had a turn —
 * plain random would show the same shirt twice in a row often enough to look
 * broken.
 */
const { t } = useI18n()
const { format } = useMoney()
const flash = useFlashStore()
const session = useSessionStore()

const gallery = ref<Photo[]>([])
const loading = ref(true)
const uploading = ref(false)
const collagePrices = ref(true)
const articles = ref<Article[]>([])
const uploadTarget = ref('')

const canManage = computed(() => session.capabilities?.can_manage_slideshow ?? false)
const selected = computed(() => gallery.value.filter((photo) => photo.include_in_slideshow))

/** Playback state. */
const playing = ref(false)
const playbackMode = ref<'slide' | 'collage'>('slide')
const current = ref<Photo | null>(null)
const collagePhotos = ref<Photo[]>([])
const stage = ref<HTMLElement | null>(null)
const changeSeconds = ref(8)
const animationSpeed = ref(1)
const direction = ref<'left' | 'right' | 'top' | 'bottom'>('left')
const animationSeconds = computed(() => Math.max(0.2, 2.5 / animationSpeed.value))
const oppositeDirection = computed(() => ({
  left: 'right', right: 'left', top: 'bottom', bottom: 'top',
}[direction.value]))

let bag: Photo[] = []
let cyclePhotos: Photo[] = []
let timer: number | undefined
let previousPhotoId: number | null = null

const uploadVariants = computed(() => articles.value.flatMap((article) => article.variants
  .filter((variant) => variant.is_active)
  .map((variant) => ({
    id: variant.id,
    label: `${article.name} — ${variantLabel(article, variant.option_value_ids) || t('slideshow.defaultVariant')}`,
    offered: article.is_offered && variant.is_offered,
  }))))

onMounted(() => {
  document.addEventListener('fullscreenchange', onFullscreenChange)
  load()
})
onUnmounted(() => {
  document.removeEventListener('fullscreenchange', onFullscreenChange)
  stop()
})

/**
 * Reloads the list.
 *
 * `silent` is for a refresh after an inline change: the loading state replaces
 * the whole list with a one-line placeholder, which collapses the page and
 * makes the browser clamp the scroll position back to the top.
 */
async function load(silent = false) {
  if (!silent) loading.value = true
  try {
    const [all, show, catalogue] = await Promise.all([
      photosApi.list(), photosApi.slideshow(), catalogueApi.list(),
    ])
    gallery.value = all.photos
    collagePrices.value = show.collage_show_prices
    articles.value = catalogue.articles
  } catch {
    flash.error(t('errors.generic'))
  } finally {
    if (!silent) loading.value = false
  }
}

function report(error: unknown) {
  flash.error(
    error instanceof ApiError
      ? t(`errors.${error.detailCode ?? 'generic'}`, error.message)
      : t('errors.network'),
  )
}

async function upload(event: Event) {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  if (!files.length) return
  if (!uploadTarget.value) {
    flash.error(t('slideshow.targetRequired'))
    input.value = ''
    return
  }

  uploading.value = true
  let failures = 0
  const variantId = uploadTarget.value === 'other' ? undefined : Number(uploadTarget.value)
  for (const file of files) {
    try {
      await photosApi.upload(file, variantId)
    } catch (error) {
      failures++
      report(error)
    }
  }
  if (failures === 0) {
    flash.success(t('slideshow.uploaded'))
  }
  await load(true)
  uploading.value = false
  input.value = ''
}

function variantLabel(article: Article, valueIds: number[]) {
  return article.option_groups
    .filter((group) => group.is_active)
    .sort((left, right) => left.position - right.position)
    .map((group) => {
      const value = group.values.find((entry) => valueIds.includes(entry.id))
      return value ? `${group.name}: ${value.value}` : ''
    })
    .filter(Boolean)
    .join(' · ')
}

async function toggleInclude(photo: Photo) {
  try {
    await photosApi.update(photo.id, { include_in_slideshow: !photo.include_in_slideshow })
    await load(true)
  } catch (error) {
    report(error)
  }
}

async function togglePrice(photo: Photo) {
  try {
    await photosApi.update(photo.id, { show_price: !photo.show_price })
    await load(true)
  } catch (error) {
    report(error)
  }
}

async function remove(photo: Photo) {
  try {
    await photosApi.remove(photo.id)
    flash.success(t('slideshow.removed'))
    await load(true)
  } catch (error) {
    report(error)
  }
}

async function setCollagePrices(value: boolean) {
  try {
    await photosApi.setCollagePrices(value)
    collagePrices.value = value
  } catch (error) {
    report(error)
  }
}

/** Refills the bag so every picture is shown once per pass. */
function refillBag() {
  bag = [...selected.value]
  for (let i = bag.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1))
    ;[bag[i], bag[j]] = [bag[j], bag[i]]
  }
  // `pop()` takes the last entry. Keep the first picture of a new pass from
  // repeating the final picture of the previous pass.
  if (bag.length > 1 && bag[bag.length - 1]?.id === previousPhotoId) {
    const replacement = Math.floor(Math.random() * (bag.length - 1))
    ;[bag[replacement], bag[bag.length - 1]] = [bag[bag.length - 1], bag[replacement]]
  }
  cyclePhotos = [...bag].reverse()
}

const directions: Array<'left' | 'right' | 'top' | 'bottom'> = ['left', 'right', 'top', 'bottom']

function advance() {
  if (!playing.value || !selected.value.length) return
  if (!bag.length && cyclePhotos.length) {
    const seen = new Set<string>()
    collagePhotos.value = cyclePhotos.filter((photo) => photo.article_name).filter((photo) => {
      const key = photo.article_name || String(photo.variant_id)
      if (seen.has(key)) return false
      seen.add(key)
      return true
    }).slice(0, 5)
    cyclePhotos = []
    if (collagePhotos.value.length) {
      playbackMode.value = 'collage'
      timer = window.setTimeout(advance, changeSeconds.value * 1000)
      return
    }
  }
  if (!bag.length) refillBag()
  const next = bag.pop()
  if (!next) return
  current.value = next
  previousPhotoId = next.id
  playbackMode.value = 'slide'
  direction.value = directions[Math.floor(Math.random() * directions.length)]
  timer = window.setTimeout(advance, changeSeconds.value * 1000)
}

async function start() {
  if (!selected.value.length) {
    flash.error(t('slideshow.noneSelected'))
    return
  }
  refillBag()
  playing.value = true
  advance()
  await nextTick()
  stage.value?.focus()
  stage.value?.requestFullscreen?.().catch(() => {})
  document.addEventListener('keydown', onSlideshowExit)
}

function onSlideshowExit() {
  stop()
}

function stop(leaveFullscreen = true) {
  if (!playing.value) return
  playing.value = false
  window.clearTimeout(timer)
  timer = undefined
  bag = []
  cyclePhotos = []
  previousPhotoId = null
  current.value = null
  collagePhotos.value = []
  document.removeEventListener('keydown', onSlideshowExit)
  if (leaveFullscreen && document.fullscreenElement === stage.value) {
    document.exitFullscreen?.().catch(() => {})
  }
}

function onFullscreenChange() {
  if (playing.value && document.fullscreenElement !== stage.value) stop(false)
}

interface CollagePlacement { x: number; y: number; rotation: number }
const collageLayouts: Record<number, CollagePlacement[]> = {
  1: [{ x: 19, y: 8, rotation: -2 }],
  2: [{ x: 7, y: 13, rotation: -8 }, { x: 48, y: 29, rotation: 7 }],
  3: [
    { x: 7, y: 8, rotation: -8 }, { x: 51, y: 12, rotation: 7 },
    { x: 28, y: 45, rotation: -3 },
  ],
  4: [
    { x: 4, y: 9, rotation: -9 }, { x: 52, y: 6, rotation: 8 },
    { x: 8, y: 48, rotation: 6 }, { x: 54, y: 47, rotation: -7 },
  ],
  5: [
    { x: 3, y: 9, rotation: -9 }, { x: 53, y: 5, rotation: 8 },
    { x: 7, y: 49, rotation: 6 }, { x: 56, y: 47, rotation: -7 },
    { x: 30, y: 26, rotation: -2 },
  ],
}

function collageStyle(index: number) {
  const layout = collageLayouts[collagePhotos.value.length] ?? collageLayouts[5]
  const item = layout[index] ?? collageLayouts[5][index]
  return {
    '--collage-x': `${item.x}%`, '--collage-y': `${item.y}%`,
    '--collage-rotation': `${item.rotation}deg`, '--collage-index': index,
    '--animation-seconds': `${animationSeconds.value}s`,
  }
}
</script>

<template>
  <main v-if="!playing" class="page-shell">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('slideshow.eyebrow') }}</p>
        <h1>{{ t('slideshow.title') }}</h1>
        <p class="page-intro">{{ t('slideshow.intro') }}</p>
      </div>
      <button class="primary-button large-button" type="button" @click="start">
        {{ t('slideshow.start') }}
      </button>
    </div>

    <section class="table-section">
      <div class="section-heading">
        <div>
          <h2>{{ t('slideshow.playback') }}</h2>
          <p>{{ t('slideshow.playbackHint') }}</p>
        </div>
      </div>
      <div class="field-grid two-columns">
        <label>
          {{ t('slideshow.changeRate', { seconds: changeSeconds }) }}
          <input v-model.number="changeSeconds" type="range" min="5" max="20" step="0.5" />
        </label>
        <label>
          {{ t('slideshow.animationSpeed', { speed: animationSpeed }) }}
          <input v-model.number="animationSpeed" type="range" min="0.1" max="2" step="0.1" />
        </label>
      </div>
      <label v-if="canManage" class="checkbox-row">
        <input
          type="checkbox"
          :checked="collagePrices"
          @change="setCollagePrices(($event.target as HTMLInputElement).checked)"
        />
        <span>{{ t('slideshow.collagePrices') }}</span>
      </label>
    </section>

    <section v-if="canManage" class="table-section">
      <div class="section-heading">
        <div>
          <h2>{{ t('slideshow.upload') }}</h2>
          <p>{{ t('slideshow.uploadHint') }}</p>
        </div>
      </div>
      <div class="slideshow-upload-controls">
        <label>
          {{ t('slideshow.target') }}
          <select v-model="uploadTarget" :disabled="uploading">
            <option value="">{{ t('slideshow.chooseTarget') }}</option>
            <option value="other">{{ t('slideshow.other') }}</option>
            <option v-for="variant in uploadVariants" :key="variant.id" :value="String(variant.id)">
              {{ variant.label }}{{ variant.offered ? '' : ` · ${t('slideshow.notOffered')}` }}
            </option>
          </select>
          <small>{{ t('slideshow.targetHint') }}</small>
        </label>
        <label>
          {{ t('slideshow.files') }}
          <input
            type="file"
            accept=".jpg,.jpeg,.png,.webp,image/jpeg,image/png,image/webp"
            multiple
            :disabled="uploading"
            @change="upload"
          />
        </label>
      </div>
    </section>

    <section class="table-section">
      <div class="section-heading">
        <div>
          <h2>{{ t('slideshow.gallery') }}</h2>
          <p>{{ t('slideshow.selectedCount', { selected: selected.length, total: gallery.length }) }}</p>
        </div>
      </div>

      <p v-if="loading" class="muted">{{ t('common.loading') }}</p>
      <p v-else-if="!gallery.length" class="muted">{{ t('slideshow.empty') }}</p>

      <div v-else class="photo-grid">
        <figure v-for="photo in gallery" :key="photo.id" class="photo-card">
          <img :src="photosApi.fileUrl(photo.id)" :alt="photo.original_filename" loading="lazy" />
          <figcaption>
            <strong>{{ photo.article_name || t('slideshow.other') }}</strong>
            <small>{{ photo.variant_label || photo.original_filename }}</small>
          </figcaption>
          <div v-if="canManage" class="photo-actions">
            <label class="checkbox-row">
              <input
                type="checkbox"
                :checked="photo.include_in_slideshow"
                @change="toggleInclude(photo)"
              />
              <span>{{ t('slideshow.include') }}</span>
            </label>
            <label v-if="photo.article_name" class="checkbox-row">
              <input type="checkbox" :checked="photo.show_price" @change="togglePrice(photo)" />
              <span>{{ t('slideshow.showPrice') }}</span>
            </label>
            <button class="compact-button danger-button" type="button" @click="remove(photo)">
              {{ t('common.delete') }}
            </button>
          </div>
        </figure>
      </div>
    </section>
  </main>

  <!-- Any click or key stops the display, so a tablet can be reclaimed at once. -->
  <div v-else ref="stage" class="slideshow-stage" tabindex="-1" @click="stop()">
    <figure
      v-if="playbackMode === 'slide' && current"
      :key="current.id"
      class="slideshow-slide"
      :style="{ '--animation-seconds': `${animationSeconds}s` }"
    >
      <div class="slideshow-frame" :class="`from-${direction}`">
        <img :src="photosApi.fileUrl(current.id)" :alt="current.original_filename" />
      </div>
      <figcaption v-if="current.article_name" class="slideshow-copy" :class="`from-${oppositeDirection}`">
        <strong>{{ current.article_name }}</strong>
        <span v-if="current.variant_label">{{ current.variant_label }}</span>
        <b v-if="current.show_price">
          {{ format(current.sale_price_cents) }}
        </b>
      </figcaption>
    </figure>
    <section
      v-else-if="playbackMode === 'collage'"
      class="slideshow-collage"
      :data-count="collagePhotos.length"
      :aria-label="t('slideshow.collage')"
    >
      <figure
        v-for="(photo, index) in collagePhotos"
        :key="photo.id"
        class="slideshow-collage-card"
        :style="collageStyle(index)"
      >
        <img :src="photosApi.fileUrl(photo.id)" :alt="photo.original_filename" />
        <figcaption>
          <strong>{{ photo.article_name }}</strong>
          <span>{{ photo.variant_label || t('slideshow.defaultVariant') }}</span>
          <b v-if="collagePrices && photo.show_price">{{ format(photo.sale_price_cents) }}</b>
        </figcaption>
      </figure>
    </section>
    <p class="slideshow-hint">{{ t('slideshow.stopHint') }}</p>
  </div>
</template>

<style scoped>
.photo-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 16px;
}

.photo-card {
  margin: 0;
  padding: 10px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--panel-raised);
}

.photo-card img {
  width: 100%;
  aspect-ratio: 4 / 3;
  object-fit: cover;
  border-radius: 10px;
  background: var(--input-bg);
}

.photo-card figcaption {
  display: grid;
  gap: 2px;
  margin: 8px 0;
}

.photo-card figcaption small {
  color: var(--muted);
}

.photo-actions {
  display: grid;
  gap: 6px;
}

.slideshow-upload-controls {
  display: grid;
  grid-template-columns: minmax(240px, 1fr) minmax(240px, 1fr);
  gap: 14px;
}

.slideshow-upload-controls small {
  color: var(--muted);
}

.checkbox-row {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 8px;
  font-size: 0.86rem;
}

.checkbox-row input[type='checkbox'] {
  width: 1rem;
  height: 1rem;
  accent-color: var(--accent);
}

/* The full-screen display. */
.slideshow-stage {
  position: fixed;
  inset: 0;
  z-index: 100;
  display: grid;
  place-items: center;
  padding: clamp(12px, 2vw, 32px);
  background:
    radial-gradient(circle at 50% 42%, rgba(91, 48, 112, 0.22), transparent 48%),
    #08080b;
  cursor: pointer;
  overflow: hidden;
}

.slideshow-slide {
  position: relative;
  display: grid;
  width: min(calc(100vw - 5vw), 2200px);
  height: min(calc(100dvh - 5vh), 1300px);
  margin: 0;
  place-items: center;
}

.slideshow-frame {
  display: grid;
  width: 100%;
  height: 100%;
  place-items: center;
}

.slideshow-frame img {
  display: block;
  width: 100%;
  height: 100%;
  max-width: 100%;
  max-height: 100%;
  border: 3px solid #000;
  border-radius: 5px;
  background: #000;
  box-shadow: 0 28px 75px rgba(0, 0, 0, 0.58);
  object-fit: contain;
}

.slideshow-copy {
  position: absolute;
  z-index: 2;
  display: grid;
  gap: 3px;
  max-width: min(78vw, 560px);
  padding: clamp(12px, 2vw, 24px) clamp(15px, 2.6vw, 31px);
  border: 1px solid rgba(255, 255, 255, 0.2);
  border-radius: 7px;
  color: #fff;
  background: linear-gradient(135deg, rgba(14, 14, 18, 0.9), rgba(23, 18, 28, 0.64));
  box-shadow: 0 18px 46px rgba(0, 0, 0, 0.42);
  backdrop-filter: blur(9px);
}

.slideshow-copy strong {
  font-size: clamp(1.35rem, 3vw, 3.1rem);
  line-height: 1.04;
  letter-spacing: -0.04em;
}

.slideshow-copy span {
  color: rgba(255, 255, 255, 0.79);
  font-size: clamp(0.78rem, 1.35vw, 1.2rem);
}

.slideshow-copy b {
  margin-top: 3px;
  color: var(--accent-bright);
  font-size: clamp(1.15rem, 2.2vw, 2.3rem);
  font-weight: 900;
}

.slideshow-copy.from-left { bottom: clamp(16px, 3vw, 44px); left: clamp(12px, 3vw, 46px); }
.slideshow-copy.from-right { right: clamp(12px, 3vw, 46px); bottom: clamp(16px, 3vw, 44px); }
.slideshow-copy.from-top { top: clamp(12px, 3vw, 46px); left: clamp(12px, 3vw, 46px); }
.slideshow-copy.from-bottom { right: clamp(12px, 3vw, 46px); bottom: clamp(16px, 3vw, 44px); }

.slideshow-frame.from-left { animation: frame-in-left var(--animation-seconds) ease-out both; }
.slideshow-frame.from-right { animation: frame-in-right var(--animation-seconds) ease-out both; }
.slideshow-frame.from-top { animation: frame-in-top var(--animation-seconds) ease-out both; }
.slideshow-frame.from-bottom { animation: frame-in-bottom var(--animation-seconds) ease-out both; }
.slideshow-copy.from-left { animation: frame-in-left var(--animation-seconds) 0.2s ease-out both; }
.slideshow-copy.from-right { animation: frame-in-right var(--animation-seconds) 0.2s ease-out both; }
.slideshow-copy.from-top { animation: frame-in-top var(--animation-seconds) 0.2s ease-out both; }
.slideshow-copy.from-bottom { animation: frame-in-bottom var(--animation-seconds) 0.2s ease-out both; }

.slideshow-collage {
  --collage-width: min(36vw, 480px);
  position: relative;
  width: min(92vw, 1360px);
  height: min(78dvh, 900px);
  min-height: 250px;
}

.slideshow-collage[data-count='1'] { --collage-width: min(64vw, 860px); }
.slideshow-collage[data-count='2'] { --collage-width: min(43vw, 640px); }
.slideshow-collage[data-count='3'] { --collage-width: min(40vw, 560px); }

.slideshow-collage-card {
  position: absolute;
  top: var(--collage-y);
  left: var(--collage-x);
  width: var(--collage-width);
  aspect-ratio: 4 / 3;
  margin: 0;
  overflow: hidden;
  border: 2px solid #000;
  border-radius: 7px;
  background: #000;
  box-shadow: 0 22px 52px rgba(0, 0, 0, 0.56);
  transform: rotate(var(--collage-rotation));
  animation: collage-in var(--animation-seconds) calc(var(--collage-index) * 0.14s) both cubic-bezier(.16, .84, .22, 1);
}

.slideshow-collage-card img {
  width: 100%;
  height: 100%;
  object-fit: contain;
}

.slideshow-collage-card figcaption {
  position: absolute;
  right: 8px;
  bottom: 8px;
  left: 8px;
  display: grid;
  padding: 7px 9px;
  border: 1px solid rgba(255, 255, 255, 0.18);
  border-radius: 6px;
  color: #fff;
  background: rgba(14, 12, 18, 0.76);
  backdrop-filter: blur(7px);
}

.slideshow-collage-card figcaption span { color: rgba(255, 255, 255, 0.75); font-size: 0.78rem; }
.slideshow-collage-card figcaption b { color: var(--accent-bright); }

@keyframes frame-in-left {
  from { opacity: 0; transform: translateX(-14vw) scale(0.96); }
  to { opacity: 1; transform: none; }
}
@keyframes frame-in-right {
  from { opacity: 0; transform: translateX(14vw) scale(0.96); }
  to { opacity: 1; transform: none; }
}
@keyframes frame-in-top {
  from { opacity: 0; transform: translateY(-12vh) scale(0.96); }
  to { opacity: 1; transform: none; }
}
@keyframes frame-in-bottom {
  from { opacity: 0; transform: translateY(12vh) scale(0.96); }
  to { opacity: 1; transform: none; }
}
@keyframes collage-in {
  from { opacity: 0; transform: translateY(12vh) rotate(var(--collage-rotation)) scale(0.72); }
  to { opacity: 1; transform: rotate(var(--collage-rotation)) scale(1); }
}

.slideshow-hint {
  position: absolute;
  bottom: 24px;
  color: var(--muted);
  font-size: 0.86rem;
}

@media (prefers-reduced-motion: reduce) {
  .slideshow-frame,
  .slideshow-copy,
  .slideshow-collage-card {
    animation: none;
  }
}

@media (max-width: 700px) {
  .slideshow-upload-controls { grid-template-columns: 1fr; }
  .slideshow-slide { width: 100%; height: 100%; }
  .slideshow-copy { right: 9px !important; bottom: 8px !important; left: auto !important; top: auto !important; }
  .slideshow-collage { width: 100%; height: min(74dvh, 700px); }
}
</style>
