<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { photosApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { Photo } from '@/api/types'
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

const canManage = computed(() => session.capabilities?.can_manage_slideshow ?? false)
const selected = computed(() => gallery.value.filter((photo) => photo.include_in_slideshow))

/** Playback state. */
const playing = ref(false)
const currentIndex = ref(0)
const changeSeconds = ref(6)
const animationSeconds = ref(1.2)
const direction = ref<'left' | 'right' | 'top' | 'bottom'>('left')

let bag: Photo[] = []
let timer: number | undefined

const current = computed<Photo | null>(() => selected.value[currentIndex.value] ?? null)

onMounted(load)
onUnmounted(stop)

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
    const [all, show] = await Promise.all([photosApi.list(), photosApi.slideshow()])
    gallery.value = all.photos
    collagePrices.value = show.collage_show_prices
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

  uploading.value = true
  try {
    for (const file of files) {
      const form = new FormData()
      form.append('file', file)
      await fetch('/api/v1/photos', {
        method: 'POST',
        body: form,
        credentials: 'include',
        headers: csrfHeader(),
      }).then(async (response) => {
        if (!response.ok) {
          const body = await response.json().catch(() => ({}))
          throw new ApiError(response.status, body.message ?? 'upload failed', body.code)
        }
      })
    }
    flash.success(t('slideshow.uploaded'))
    await load(true)
  } catch (error) {
    report(error)
  } finally {
    uploading.value = false
    input.value = ''
  }
}

/** The upload uses fetch directly for multipart, so it needs the token itself. */
function csrfHeader(): Record<string, string> {
  const match = document.cookie.match(/(?:^|; )merch_csrf=([^;]*)/)
  return match ? { 'X-CSRF-Token': decodeURIComponent(match[1]) } : {}
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
}

const directions: Array<'left' | 'right' | 'top' | 'bottom'> = ['left', 'right', 'top', 'bottom']

function advance() {
  if (!selected.value.length) return
  if (!bag.length) refillBag()

  const next = bag.pop()
  if (!next) return
  currentIndex.value = selected.value.findIndex((photo) => photo.id === next.id)
  direction.value = directions[Math.floor(Math.random() * directions.length)]
}

function start() {
  if (!selected.value.length) {
    flash.error(t('slideshow.noneSelected'))
    return
  }
  refillBag()
  advance()
  playing.value = true
  timer = window.setInterval(advance, changeSeconds.value * 1000)
  document.addEventListener('keydown', stop)
}

function stop() {
  playing.value = false
  window.clearInterval(timer)
  timer = undefined
  document.removeEventListener('keydown', stop)
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
          <input v-model.number="changeSeconds" type="range" min="2" max="20" step="1" />
        </label>
        <label>
          {{ t('slideshow.animationSpeed', { seconds: animationSeconds }) }}
          <input v-model.number="animationSeconds" type="range" min="0.3" max="3" step="0.1" />
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
      <input type="file" accept="image/*" multiple :disabled="uploading" @change="upload" />
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
            <label class="checkbox-row">
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
  <div v-else class="slideshow-stage" @click="stop">
    <figure
      v-if="current"
      :key="current.id"
      class="slideshow-frame"
      :class="`from-${direction}`"
      :style="{ '--animation-seconds': `${animationSeconds}s` }"
    >
      <img :src="photosApi.fileUrl(current.id)" :alt="current.original_filename" />
      <figcaption v-if="current.article_name">
        <strong>{{ current.article_name }}</strong>
        <span v-if="current.variant_label">{{ current.variant_label }}</span>
        <b v-if="current.show_price && current.sale_price_cents">
          {{ format(current.sale_price_cents) }}
        </b>
      </figcaption>
    </figure>
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
  background: var(--bg);
  cursor: pointer;
  overflow: hidden;
}

.slideshow-frame {
  display: grid;
  place-items: center;
  gap: 18px;
  margin: 0;
  max-width: 92vw;
  max-height: 88vh;
}

.slideshow-frame img {
  max-width: 92vw;
  max-height: 70vh;
  border-radius: 18px;
  box-shadow: var(--shadow);
}

.slideshow-frame figcaption {
  display: grid;
  place-items: center;
  gap: 6px;
  text-align: center;
  /* The caption follows the picture offset, as in the original. */
  animation: caption-in var(--animation-seconds) ease-out both;
  animation-delay: calc(var(--animation-seconds) * 0.25);
}

.slideshow-frame figcaption strong {
  font-size: clamp(1.6rem, 4vw, 2.6rem);
  letter-spacing: -0.04em;
}

.slideshow-frame figcaption span {
  color: var(--muted);
  font-size: clamp(1rem, 2vw, 1.3rem);
}

.slideshow-frame figcaption b {
  color: var(--accent-bright);
  font-size: clamp(1.4rem, 3vw, 2rem);
  font-weight: 900;
}

.slideshow-frame.from-left img { animation: frame-in-left var(--animation-seconds) ease-out both; }
.slideshow-frame.from-right img { animation: frame-in-right var(--animation-seconds) ease-out both; }
.slideshow-frame.from-top img { animation: frame-in-top var(--animation-seconds) ease-out both; }
.slideshow-frame.from-bottom img { animation: frame-in-bottom var(--animation-seconds) ease-out both; }

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
@keyframes caption-in {
  from { opacity: 0; transform: translateY(1.4rem); }
  to { opacity: 1; transform: none; }
}

.slideshow-hint {
  position: absolute;
  bottom: 24px;
  color: var(--muted);
  font-size: 0.86rem;
}

@media (prefers-reduced-motion: reduce) {
  .slideshow-frame img,
  .slideshow-frame figcaption {
    animation: none;
  }
}
</style>
