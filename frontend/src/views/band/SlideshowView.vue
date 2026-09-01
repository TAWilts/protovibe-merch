<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { catalogueApi, photosApi, type CollageMode } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { Article, Photo } from '@/api/types'
import { useMoney } from '@/composables/useMoney'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'
import { fitImageSize } from '@/utils/imageFit'

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
const settingsSaving = ref(false)
const collagePrices = ref(true)
const collageInterval = ref(8)
const collageModes = ref<CollageMode[]>(['scroll', 'reveal', 'filmstrip'])
const articles = ref<Article[]>([])
const uploadTarget = ref('')

const canManage = computed(() => session.capabilities?.can_manage_slideshow ?? false)
const selected = computed(() => gallery.value.filter((photo) => photo.include_in_slideshow))

/** Playback state. */
const playing = ref(false)
const playbackMode = ref<'slide' | 'collage'>('slide')
const activeCollageMode = ref<CollageMode>('reveal')
const current = ref<Photo | null>(null)
const collagePhotos = ref<Photo[]>([])
const stage = ref<HTMLElement | null>(null)
const currentImage = ref<HTMLImageElement | null>(null)
const fittedSlideSize = ref<{ width: string; height: string } | null>(null)
const changeSeconds = ref(8)
const animationSpeed = ref(1)
const direction = ref<'left' | 'right' | 'top' | 'bottom'>('left')
const animationSeconds = computed(() => Math.max(0.2, 2.5 / animationSpeed.value))
const oppositeDirection = computed(() => ({
  left: 'right', right: 'left', top: 'bottom', bottom: 'top',
}[direction.value]))

let bag: Photo[] = []
let timer: number | undefined
let previousPhotoId: number | null = null
let productSlidesSinceCollage = 0
let viewportFitFrame: number | null = null

const productPhotos = computed(() => selected.value.filter((photo) => photo.article_name))
const collageDurationSeconds = computed(() => {
  if (activeCollageMode.value === 'scroll') return Math.max(12, changeSeconds.value * 2.4)
  return Math.max(8, changeSeconds.value * 1.5)
})
const collageColumns = computed(() => Math.max(
  1,
  Math.min(5, Math.ceil(Math.sqrt(collagePhotos.value.length * 1.6))),
))
const filmstripRows = computed(() => {
  const midpoint = Math.ceil(collagePhotos.value.length / 2)
  return [collagePhotos.value.slice(0, midpoint), collagePhotos.value.slice(midpoint)]
    .filter((row) => row.length)
})
const collageStageStyle = computed(() => ({
  '--collage-columns': String(collageColumns.value),
  '--scroll-columns': String(Math.min(3, collageColumns.value)),
  '--collage-duration': `${collageDurationSeconds.value}s`,
}))
const collageModeOptions: CollageMode[] = ['scroll', 'reveal', 'filmstrip']

const uploadVariants = computed(() => articles.value.flatMap((article) => article.variants
  .filter((variant) => variant.is_active)
  .map((variant) => ({
    id: variant.id,
    label: `${article.name} — ${variantLabel(article, variant.option_value_ids) || t('slideshow.defaultVariant')}`,
    offered: article.is_offered && variant.is_offered,
  }))))

onMounted(() => {
  document.addEventListener('fullscreenchange', onFullscreenChange)
  window.addEventListener('resize', scheduleImageFit)
  window.addEventListener('orientationchange', scheduleImageFit)
  window.visualViewport?.addEventListener('resize', scheduleImageFit)
  load()
})
onUnmounted(() => {
  document.removeEventListener('fullscreenchange', onFullscreenChange)
  window.removeEventListener('resize', scheduleImageFit)
  window.removeEventListener('orientationchange', scheduleImageFit)
  window.visualViewport?.removeEventListener('resize', scheduleImageFit)
  if (viewportFitFrame !== null) window.cancelAnimationFrame(viewportFitFrame)
  stop()
})

function cssPixels(value: string) {
  const parsed = Number.parseFloat(value)
  return Number.isFinite(parsed) ? parsed : 0
}

/** Mirrors the predecessor's visual-viewport measurement. */
function availableStageSize() {
  const element = stage.value
  if (!element) return null
  const style = window.getComputedStyle(element)
  const visualWidth = Number(window.visualViewport?.width) || element.clientWidth
  const visualHeight = Number(window.visualViewport?.height) || element.clientHeight
  const stageWidth = Math.min(element.clientWidth || visualWidth, visualWidth)
  const stageHeight = Math.min(element.clientHeight || visualHeight, visualHeight)
  return {
    width: Math.max(1, stageWidth - cssPixels(style.paddingLeft) - cssPixels(style.paddingRight)),
    height: Math.max(1, stageHeight - cssPixels(style.paddingTop) - cssPixels(style.paddingBottom)),
  }
}

function fitCurrentImage() {
  const image = currentImage.value
  const available = availableStageSize()
  if (!image || !available) return
  const fitted = fitImageSize(image.naturalWidth, image.naturalHeight, available.width, available.height)
  if (!fitted) return
  fittedSlideSize.value = { width: `${fitted.width}px`, height: `${fitted.height}px` }
}

function fitCollageImages() {
  stage.value?.querySelectorAll<HTMLImageElement>('.slideshow-card-image img').forEach((image) => {
    const container = image.parentElement
    if (!container) return
    const style = window.getComputedStyle(container)
    const fitted = fitImageSize(
      image.naturalWidth,
      image.naturalHeight,
      container.clientWidth - cssPixels(style.paddingLeft) - cssPixels(style.paddingRight),
      container.clientHeight - cssPixels(style.paddingTop) - cssPixels(style.paddingBottom),
    )
    if (!fitted) return
    image.style.width = `${fitted.width}px`
    image.style.height = `${fitted.height}px`
  })
}

function scheduleImageFit() {
  if (!playing.value) return
  if (viewportFitFrame !== null) window.cancelAnimationFrame(viewportFitFrame)
  viewportFitFrame = window.requestAnimationFrame(() => {
    viewportFitFrame = null
    fitCurrentImage()
    fitCollageImages()
  })
}

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
    // POS sessions may view and run the slideshow, but the management-only
    // article endpoint is intentionally blocked there. Only curators need the
    // catalogue for assigning uploads to variants.
    const catalogueRequest = canManage.value
      ? catalogueApi.list()
      : Promise.resolve({ articles: [] as Article[] })
    const [all, show, catalogue] = await Promise.all([
      photosApi.list(), photosApi.slideshow(), catalogueRequest,
    ])
    gallery.value = all.photos
    collagePrices.value = show.collage_show_prices
    collageInterval.value = show.collage_interval
    collageModes.value = [...show.collage_modes]
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

function toggleCollageMode(mode: CollageMode, enabled: boolean) {
  if (enabled) {
    if (!collageModes.value.includes(mode)) collageModes.value.push(mode)
    return
  }
  if (collageModes.value.length === 1) {
    // Re-render the controlled checkbox so it visibly remains selected.
    collageModes.value = [...collageModes.value]
    flash.error(t('slideshow.oneCollageModeRequired'))
    return
  }
  collageModes.value = collageModes.value.filter((entry) => entry !== mode)
}

async function saveCollageSettings() {
  if (settingsSaving.value) return
  if (!Number.isInteger(collageInterval.value) || collageInterval.value < 1 || collageInterval.value > 100) {
    flash.error(t('slideshow.invalidCollageInterval'))
    return
  }
  settingsSaving.value = true
  try {
    await photosApi.saveSlideshowSettings({
      collage_show_prices: collagePrices.value,
      collage_interval: collageInterval.value,
      collage_modes: collageModes.value,
    })
    flash.success(t('slideshow.settingsSaved'))
  } catch (error) {
    report(error)
  } finally {
    settingsSaving.value = false
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
}

const directions: Array<'left' | 'right' | 'top' | 'bottom'> = ['left', 'right', 'top', 'bottom']

function advance() {
  if (!playing.value || !selected.value.length) return
  if (productSlidesSinceCollage >= collageInterval.value && productPhotos.value.length > 1) {
    showCollage()
    return
  }
  if (!bag.length) refillBag()
  const next = bag.pop()
  if (!next) return
  fittedSlideSize.value = null
  current.value = next
  previousPhotoId = next.id
  if (next.article_name) productSlidesSinceCollage++
  playbackMode.value = 'slide'
  direction.value = directions[Math.floor(Math.random() * directions.length)]
  nextTick(scheduleImageFit)
  timer = window.setTimeout(advance, changeSeconds.value * 1000)
}

function showCollage() {
  collagePhotos.value = [...productPhotos.value]
  for (let i = collagePhotos.value.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1))
    ;[collagePhotos.value[i], collagePhotos.value[j]] = [collagePhotos.value[j], collagePhotos.value[i]]
  }
  activeCollageMode.value = collageModes.value[Math.floor(Math.random() * collageModes.value.length)] ?? 'reveal'
  productSlidesSinceCollage = 0
  playbackMode.value = 'collage'
  nextTick(scheduleImageFit)
  timer = window.setTimeout(advance, collageDurationSeconds.value * 1000)
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
  scheduleImageFit()
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
  previousPhotoId = null
  productSlidesSinceCollage = 0
  current.value = null
  fittedSlideSize.value = null
  collagePhotos.value = []
  if (viewportFitFrame !== null) window.cancelAnimationFrame(viewportFitFrame)
  viewportFitFrame = null
  document.removeEventListener('keydown', onSlideshowExit)
  if (leaveFullscreen && document.fullscreenElement === stage.value) {
    document.exitFullscreen?.().catch(() => {})
  }
}

function onFullscreenChange() {
  if (!playing.value) return
  if (document.fullscreenElement !== stage.value) {
    stop(false)
    return
  }
  scheduleImageFit()
}

function collageCardStyle(index: number) {
  return {
    '--collage-index': index,
    '--collage-side': index % 4,
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
      <form v-if="canManage" class="collage-settings" @submit.prevent="saveCollageSettings">
        <label>
          {{ t('slideshow.collageInterval') }}
          <input v-model.number="collageInterval" type="number" min="1" max="100" step="1" />
          <small>{{ t('slideshow.collageIntervalHint') }}</small>
        </label>
        <fieldset>
          <legend>{{ t('slideshow.collageModesTitle') }}</legend>
          <label v-for="mode in collageModeOptions" :key="mode" class="checkbox-row">
            <input
              type="checkbox"
              :checked="collageModes.includes(mode)"
              @change="toggleCollageMode(mode, ($event.target as HTMLInputElement).checked)"
            />
            <span>
              <strong>{{ t(`slideshow.collageModes.${mode}.title`) }}</strong>
              <small>{{ t(`slideshow.collageModes.${mode}.hint`) }}</small>
            </span>
          </label>
        </fieldset>
        <label class="checkbox-row">
          <input v-model="collagePrices" type="checkbox" />
          <span>{{ t('slideshow.collagePrices') }}</span>
        </label>
        <button class="secondary-button" type="submit" :disabled="settingsSaving">
          {{ settingsSaving ? t('common.loading') : t('common.save') }}
        </button>
      </form>
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
      :style="[{ '--animation-seconds': `${animationSeconds}s` }, fittedSlideSize ?? {}]"
    >
      <div class="slideshow-frame" :class="`from-${direction}`">
        <img
          ref="currentImage"
          :src="photosApi.fileUrl(current.id)"
          :alt="current.original_filename"
          @load="scheduleImageFit"
        />
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
      :class="`mode-${activeCollageMode}`"
      :data-count="collagePhotos.length"
      :style="collageStageStyle"
      :aria-label="t('slideshow.collage')"
    >
      <div v-if="activeCollageMode !== 'filmstrip'" class="slideshow-collage-grid">
        <figure
          v-for="(photo, index) in collagePhotos"
          :key="photo.id"
          class="slideshow-collage-card"
          :style="collageCardStyle(index)"
        >
          <div class="slideshow-card-image">
            <img
              :src="photosApi.fileUrl(photo.id)"
              :alt="photo.original_filename"
              @load="scheduleImageFit"
            />
          </div>
          <figcaption>
            <strong>{{ photo.article_name }}</strong>
            <span>{{ photo.variant_label || t('slideshow.defaultVariant') }}</span>
            <b v-if="collagePrices && photo.show_price">{{ format(photo.sale_price_cents) }}</b>
          </figcaption>
        </figure>
      </div>
      <div v-else class="slideshow-filmstrip">
        <div v-for="(row, rowIndex) in filmstripRows" :key="rowIndex" class="slideshow-filmstrip-row">
          <figure
            v-for="(photo, index) in row"
            :key="photo.id"
            class="slideshow-collage-card"
            :style="collageCardStyle(index + rowIndex * row.length)"
          >
            <div class="slideshow-card-image">
              <img
                :src="photosApi.fileUrl(photo.id)"
                :alt="photo.original_filename"
                @load="scheduleImageFit"
              />
            </div>
            <figcaption>
              <strong>{{ photo.article_name }}</strong>
              <span>{{ photo.variant_label || t('slideshow.defaultVariant') }}</span>
              <b v-if="collagePrices && photo.show_price">{{ format(photo.sale_price_cents) }}</b>
            </figcaption>
          </figure>
        </div>
      </div>
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
  object-fit: contain;
  object-position: center;
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

.collage-settings {
  display: grid;
  grid-template-columns: minmax(180px, 0.45fr) minmax(280px, 1fr);
  gap: 14px 22px;
  align-items: start;
  margin-top: 18px;
  padding-top: 18px;
  border-top: 1px solid var(--border);
}

.collage-settings > label:first-child small,
.collage-settings fieldset small {
  display: block;
  color: var(--muted);
  font-weight: 400;
}

.collage-settings fieldset {
  display: grid;
  grid-row: span 2;
  gap: 10px;
  margin: 0;
  padding: 0;
  border: 0;
}

.collage-settings legend {
  margin-bottom: 8px;
  font-weight: 700;
}

.collage-settings .checkbox-row span {
  display: grid;
  gap: 2px;
}

.collage-settings button {
  justify-self: start;
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
  padding: clamp(10px, 1.8vw, 32px) clamp(10px, 1.8vw, 32px) clamp(38px, 5vw, 64px);
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
  width: min(94vw, 1680px);
  height: min(86dvh, 980px);
  min-height: 260px;
  overflow: hidden;
}

.slideshow-collage-grid {
  display: grid;
  grid-template-columns: repeat(var(--collage-columns), minmax(0, 1fr));
  gap: clamp(8px, 1.2vw, 18px);
  width: 100%;
}

.mode-reveal .slideshow-collage-grid {
  grid-auto-rows: minmax(0, 1fr);
  height: 100%;
}

.mode-scroll .slideshow-collage-grid {
  grid-template-columns: repeat(var(--scroll-columns), minmax(0, 1fr));
  grid-auto-rows: minmax(250px, 36dvh);
  animation: collage-scroll var(--collage-duration) linear both;
}

.slideshow-collage-card {
  position: relative;
  display: grid;
  grid-template-rows: minmax(0, 1fr) auto;
  min-width: 0;
  min-height: 0;
  margin: 0;
  overflow: hidden;
  border: 1px solid rgba(255, 255, 255, 0.18);
  border-radius: 10px;
  background: #101015;
  box-shadow: 0 22px 52px rgba(0, 0, 0, 0.56);
}

.slideshow-card-image {
  display: grid;
  min-width: 0;
  min-height: 0;
  place-items: center;
  overflow: hidden;
  padding: clamp(3px, 0.5vw, 8px);
}

.slideshow-card-image img {
  display: block;
  width: auto;
  height: auto;
  max-width: 100%;
  max-height: 100%;
  object-fit: contain;
  object-position: center;
}

.slideshow-collage-card figcaption {
  display: grid;
  padding: 8px 10px;
  border-top: 1px solid rgba(255, 255, 255, 0.12);
  color: #fff;
  background: rgba(22, 18, 27, 0.95);
}

.slideshow-collage-card figcaption span { color: rgba(255, 255, 255, 0.75); font-size: 0.78rem; }
.slideshow-collage-card figcaption b { color: var(--accent-bright); }

.mode-reveal .slideshow-collage-card {
  animation-duration: var(--animation-seconds);
  animation-delay: calc(var(--collage-index) * 0.11s);
  animation-fill-mode: both;
  animation-timing-function: cubic-bezier(.16, .84, .22, 1);
}

.mode-reveal .slideshow-collage-card:nth-child(4n + 1) { animation-name: collage-from-left; }
.mode-reveal .slideshow-collage-card:nth-child(4n + 2) { animation-name: collage-from-top; }
.mode-reveal .slideshow-collage-card:nth-child(4n + 3) { animation-name: collage-from-right; }
.mode-reveal .slideshow-collage-card:nth-child(4n + 4) { animation-name: collage-from-bottom; }

.slideshow-filmstrip {
  display: grid;
  grid-template-rows: repeat(2, minmax(0, 1fr));
  gap: clamp(10px, 1.8vh, 20px);
  height: 100%;
  overflow: hidden;
}

.slideshow-filmstrip-row {
  display: flex;
  gap: clamp(8px, 1.2vw, 18px);
  width: max-content;
  min-height: 0;
}

.slideshow-filmstrip-row .slideshow-collage-card {
  width: clamp(240px, 31vw, 520px);
  flex: 0 0 auto;
}

.slideshow-filmstrip-row:nth-child(odd) {
  animation: filmstrip-left var(--collage-duration) linear both;
}

.slideshow-filmstrip-row:nth-child(even) {
  animation: filmstrip-right var(--collage-duration) linear both;
}

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
@keyframes collage-scroll {
  from { transform: translateY(88dvh); }
  to { transform: translateY(calc(-100% + 2dvh)); }
}
@keyframes collage-from-left { from { opacity: 0; transform: translateX(-30vw) scale(0.88); } }
@keyframes collage-from-right { from { opacity: 0; transform: translateX(30vw) scale(0.88); } }
@keyframes collage-from-top { from { opacity: 0; transform: translateY(-28vh) scale(0.88); } }
@keyframes collage-from-bottom { from { opacity: 0; transform: translateY(28vh) scale(0.88); } }
@keyframes filmstrip-left {
  from { transform: translateX(70vw); }
  to { transform: translateX(calc(-100% + 24vw)); }
}
@keyframes filmstrip-right {
  from { transform: translateX(calc(-100% + 24vw)); }
  to { transform: translateX(70vw); }
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
  .slideshow-collage-card,
  .slideshow-collage-grid,
  .slideshow-filmstrip-row {
    animation: none;
  }

  .slideshow-collage { overflow: auto; }
}

@media (max-width: 700px) {
  .slideshow-upload-controls { grid-template-columns: 1fr; }
  .collage-settings { grid-template-columns: 1fr; }
  .collage-settings fieldset { grid-row: auto; }
  .slideshow-slide { width: 100%; height: 100%; }
  .slideshow-copy { right: 9px !important; bottom: 8px !important; left: auto !important; top: auto !important; }
  .slideshow-collage { width: 100%; height: min(74dvh, 700px); }
  .mode-reveal .slideshow-collage-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .mode-scroll .slideshow-collage-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .slideshow-collage-card figcaption span { display: none; }
}
</style>
