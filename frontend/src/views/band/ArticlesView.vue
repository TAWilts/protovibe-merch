<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { catalogueApi, importApi, photosApi, type ImportKind } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { Article, ImportPreview, Photo } from '@/api/types'
import { useMoney, parseAmount } from '@/composables/useMoney'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'

/**
 * Article management, ported from _old/templates/articles.html.
 *
 * The option columns are entirely generic: the page never knows that "Farbe"
 * or "Größe" exist, it edits whatever columns the band defined. Removing a
 * value does not delete it — the server deactivates it so historic receipts
 * keep resolving their names, and renaming one applies retroactively.
 */
const { t } = useI18n()
const { format } = useMoney()
const flash = useFlashStore()
const session = useSessionStore()

const articles = ref<Article[]>([])
const loading = ref(true)
const busy = ref(false)
const selectedId = ref<number | null>(null)

/** The editable copy of the selected article's configuration. */
interface DraftValue {
  id: number
  value: string
}
interface DraftGroup {
  id: number
  name: string
  values: DraftValue[]
}
const draft = ref<{
  name: string
  salePrice: string
  purchasePrice: string
  isOffered: boolean
  groups: DraftGroup[]
}>({ name: '', salePrice: '', purchasePrice: '', isOffered: true, groups: [] })

const newArticleName = ref('')

const selected = computed(
  () => articles.value.find((article) => article.id === selectedId.value) ?? null,
)

/** Only variants the configuration still implies are editable; the rest are
 *  retired and shown read-only so their history stays visible. */
const activeVariants = computed(() => selected.value?.variants.filter((v) => v.is_active) ?? [])
const retiredVariants = computed(() => selected.value?.variants.filter((v) => !v.is_active) ?? [])

onMounted(load)

/**
 * Reloads the catalogue.
 *
 * `silent` is for a refresh after an inline edit. The loading state swaps the
 * whole editor for a one-line placeholder, which collapses the page and makes
 * the browser clamp the scroll position to the top — so every price typed in
 * the variant table threw the seller back to the start of the page. A silent
 * refresh also leaves the draft alone, so a half-typed article name survives a
 * variant save.
 */
async function load(silent = false) {
  if (!silent) loading.value = true
  try {
    articles.value = (await catalogueApi.list()).articles
    if (silent) return
    if (selectedId.value === null && articles.value.length) {
      select(articles.value[0].id)
    } else if (selectedId.value !== null) {
      select(selectedId.value)
    }
  } catch {
    flash.error(t('errors.generic'))
  } finally {
    if (!silent) loading.value = false
  }
}

function toInput(cents: number) {
  return (cents / 100).toFixed(2).replace('.', ',')
}

/**
 * Renders a variant as "Farbe: Schwarz · Größe: M".
 *
 * The label is built from the article's own option columns rather than shown
 * as the stored combination key, which is an internal identifier and tells the
 * band nothing. Retired values are included so a parked variant stays readable.
 */
function variantLabel(article: Article, optionValueIds: number[]): string {
  const parts: { position: number; text: string }[] = []
  for (const group of article.option_groups) {
    const value = group.values.find((entry) => optionValueIds.includes(entry.id))
    if (value) {
      parts.push({ position: group.position, text: `${group.name}: ${value.value}` })
    }
  }
  parts.sort((a, b) => a.position - b.position)
  return parts.map((part) => part.text).join(' · ')
}

function select(id: number) {
  selectedId.value = id
  const article = articles.value.find((entry) => entry.id === id)
  if (!article) return

  draft.value = {
    name: article.name,
    salePrice: toInput(article.default_sale_price_cents),
    purchasePrice: toInput(article.default_purchase_price_cents),
    isOffered: article.is_offered,
    groups: article.option_groups
      .filter((group) => group.is_active)
      .map((group) => ({
        id: group.id,
        name: group.name,
        values: group.values
          .filter((value) => value.is_active)
          .map((value) => ({ id: value.id, value: value.value })),
      })),
  }
}

/**
 * Variant photos. The original managed them here, next to the variant they
 * belong to; the Vue port had only the slideshow page, which is about a
 * different job entirely.
 */
const photosFor = ref<{ id: number; label: string } | null>(null)
const variantPhotos = ref<Photo[]>([])
const photoInput = ref<HTMLInputElement | null>(null)
const photoUploading = ref(false)
const minimumForAll = ref('')

async function openPhotos(variantId: number, label: string) {
  photosFor.value = { id: variantId, label }
  variantPhotos.value = []
  await loadPhotos()
}

async function loadPhotos() {
  if (!photosFor.value) return
  try {
    const all = await photosApi.list()
    variantPhotos.value = all.photos.filter((photo) => photo.variant_id === photosFor.value?.id)
  } catch (error) {
    report(error)
  }
}

async function onPhotoChosen(event: Event) {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  input.value = ''
  if (!files.length || !photosFor.value || photoUploading.value) return
  photoUploading.value = true
  let failures = 0
  const variantId = photosFor.value.id
  for (const file of files) {
    try {
      await photosApi.upload(file, variantId)
    } catch (error) {
      failures++
      report(error)
    }
  }
  await Promise.all([loadPhotos(), load(true)])
  photoUploading.value = false
  if (failures === 0) {
    flash.success(t('articles.photos.uploaded', { count: files.length }))
    photosFor.value = null
  }
}

async function removePhoto(photo: Photo) {
  try {
    await photosApi.remove(photo.id)
    await loadPhotos()
  } catch (error) {
    report(error)
  }
}

/**
 * CSV import, ported from _old/templates/articles.html:52.
 *
 * The preview always runs first. An import creates missing articles, options
 * and variants as a side effect, so the band has to see what it is about to
 * gain before a single row is written.
 */
const importKind = ref<ImportKind>('einkaeufe')
const importFile = ref<File | null>(null)
const importPreview = ref<ImportPreview | null>(null)
const importing = ref(false)

function onImportFile(event: Event) {
  const input = event.target as HTMLInputElement
  importFile.value = input.files?.[0] ?? null
  importPreview.value = null
}

async function previewImport() {
  if (!importFile.value || importing.value) return
  importing.value = true
  try {
    importPreview.value = await importApi.preview(importKind.value, importFile.value)
  } catch (error) {
    importPreview.value = null
    report(error)
  } finally {
    importing.value = false
  }
}

async function applyImport() {
  if (!importFile.value || !importPreview.value || importing.value) return
  importing.value = true
  try {
    const result = await importApi.apply(importKind.value, importFile.value)
    flash.success(t('articles.import.done', {
      rows: result.row_count, receipt: result.receipt_id,
    }))
    importFile.value = null
    importPreview.value = null
    await load(true)
  } catch (error) {
    report(error)
  } finally {
    importing.value = false
  }
}

function report(error: unknown) {
  flash.error(
    error instanceof ApiError
      ? t(`errors.${error.detailCode ?? 'generic'}`, t('errors.generic'))
      : t('errors.network'),
  )
}

async function createArticle() {
  const name = newArticleName.value.trim()
  if (!name || busy.value) return
  busy.value = true
  try {
    const created = await catalogueApi.create({
      name,
      default_sale_price_cents: 0,
      default_purchase_price_cents: 0,
    })
    newArticleName.value = ''
    flash.success(t('articles.created'))
    selectedId.value = created.id
    await load()
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

// A new value or group gets id 0, which is how the server tells "add this"
// from "update that".
function addValue(group: DraftGroup) {
  group.values.push({ id: 0, value: '' })
}

function removeValue(group: DraftGroup, index: number) {
  group.values.splice(index, 1)
}

function addGroup() {
  draft.value.groups.push({ id: 0, name: '', values: [{ id: 0, value: '' }] })
}

function removeGroup(index: number) {
  draft.value.groups.splice(index, 1)
}

async function save() {
  if (!selected.value || busy.value) return
  busy.value = true

  const sale = parseAmount(draft.value.salePrice)
  const purchase = parseAmount(draft.value.purchasePrice)
  if (sale === null || purchase === null) {
    flash.error(t('articles.invalidPrice'))
    busy.value = false
    return
  }

  try {
    await catalogueApi.save(selected.value.id, {
      name: draft.value.name.trim(),
      default_sale_price_cents: sale,
      default_purchase_price_cents: purchase,
      is_offered: draft.value.isOffered,
      option_groups: draft.value.groups
        .filter((group) => group.name.trim())
        .map((group) => ({
          id: group.id,
          name: group.name.trim(),
          values: group.values
            .filter((value) => value.value.trim())
            .map((value) => ({ id: value.id, value: value.value.trim() })),
        })),
    })
    flash.success(t('articles.saved'))
    await load(true)
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

/** Per-variant overrides are saved individually, so editing one price does not
 *  resend the whole option configuration. */
async function saveVariant(variantId: number, changes: Record<string, unknown>) {
  if (!selected.value) return
  try {
    await catalogueApi.save(selected.value.id, {
      variants: [{ id: variantId, ...changes }],
    })
    await load(true)
  } catch (error) {
    report(error)
  }
}

function onPriceChange(variantId: number, raw: string) {
  const cents = parseAmount(raw)
  if (cents === null) {
    flash.error(t('articles.invalidPrice'))
    return
  }
  saveVariant(variantId, { sale_price_cents: cents })
}

function onMinimumChange(variantId: number, raw: string) {
  const trimmed = raw.trim()
  if (trimmed === '') {
    // An empty field clears the warning; an explicit 0 means "warn only once
    // sold out", so the two must stay distinguishable.
    saveVariant(variantId, { clear_minimum_stock: true })
    return
  }
  const parsed = Number(trimmed)
  if (!Number.isInteger(parsed) || parsed < 0) {
    flash.error(t('articles.invalidMinimum'))
    return
  }
  saveVariant(variantId, { minimum_stock: parsed })
}

async function applyMinimumToAll() {
  if (!selected.value || busy.value) return
  const parsed = Number(minimumForAll.value.trim())
  if (!Number.isInteger(parsed) || parsed < 0) {
    flash.error(t('articles.invalidMinimum'))
    return
  }
  busy.value = true
  try {
    await catalogueApi.save(selected.value.id, {
      variants: activeVariants.value.map((variant) => ({ id: variant.id, minimum_stock: parsed })),
    })
    flash.success(t('articles.minimumApplied', { count: activeVariants.value.length }))
    await load(true)
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="page-shell">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('articles.eyebrow') }}</p>
        <h1>{{ t('articles.title') }}</h1>
      </div>
      <form class="inline-form" @submit.prevent="createArticle">
        <input v-model="newArticleName" :placeholder="t('articles.newPlaceholder')" />
        <button class="primary-button" type="submit" :disabled="!newArticleName.trim() || busy">
          {{ t('articles.create') }}
        </button>
      </form>
    </div>

    <p v-if="loading" class="muted">{{ t('common.loading') }}</p>
    <p v-else-if="!articles.length" class="muted">{{ t('articles.empty') }}</p>

    <section v-else class="article-layout">
      <aside class="selection-panel">
        <h2>{{ t('sales.articles') }}</h2>
        <div class="button-list">
          <button
            v-for="article in articles"
            :key="article.id"
            type="button"
            class="selection-button"
            :class="{ active: article.id === selectedId }"
            @click="select(article.id)"
          >
            <span>{{ article.name }}</span>
            <small>
              {{ t('sales.inStock', { count: article.total_stock }) }}
              <template v-if="!article.is_offered"> · {{ t('articles.withdrawn') }}</template>
              <template v-if="!article.configuration_complete"> · {{ t('articles.incomplete') }}</template>
            </small>
          </button>
        </div>
      </aside>

      <div v-if="selected" class="article-editor">
        <!-- Basics and options are one form with one save, so they are one
             card. Two cards read as two independent things, and the button at
             the foot of the second looked like it only saved that one. -->
        <section class="table-section article-form">
          <div class="section-heading">
            <div>
              <h2>{{ t('articles.configuration') }}</h2>
              <p>{{ t('articles.configurationHint') }}</p>
            </div>
          </div>

          <div class="article-form-group">
            <h3>{{ t('articles.basics') }}</h3>
            <label>{{ t('articles.name') }}<input v-model="draft.name" /></label>
            <label class="checkbox-row">
              <input v-model="draft.isOffered" type="checkbox" />
              <span>{{ t('articles.offered') }}</span>
            </label>
            <div class="field-grid two-columns">
              <label>{{ t('articles.defaultSalePrice') }}<input v-model="draft.salePrice" inputmode="decimal" /></label>
              <label>{{ t('articles.defaultPurchasePrice') }}<input v-model="draft.purchasePrice" inputmode="decimal" /></label>
            </div>
            <p v-if="!draft.isOffered" class="muted">{{ t('articles.withdrawnHint') }}</p>
          </div>

          <div class="article-form-group">
            <div class="article-form-group-head">
              <h3>{{ t('articles.options') }}</h3>
              <button class="secondary-button" type="button" @click="addGroup">
                {{ t('articles.addOption') }}
              </button>
            </div>
            <p class="muted">{{ t('articles.optionsHint') }}</p>

            <div v-for="(group, groupIndex) in draft.groups" :key="groupIndex" class="option-editor">
              <div class="option-editor-head">
                <input v-model="group.name" :placeholder="t('articles.optionName')" />
                <button class="compact-button danger-button" type="button" @click="removeGroup(groupIndex)">
                  {{ t('common.delete') }}
                </button>
              </div>
              <div class="option-editor-values">
                <span v-for="(value, valueIndex) in group.values" :key="valueIndex" class="option-value-input">
                  <input v-model="value.value" :placeholder="t('articles.optionValue')" />
                  <button class="icon-button" type="button" @click="removeValue(group, valueIndex)">×</button>
                </span>
                <button class="compact-button" type="button" @click="addValue(group)">
                  {{ t('common.add') }}
                </button>
              </div>
            </div>
          </div>

          <footer class="article-form-actions">
            <button class="primary-button" type="button" :disabled="busy" @click="save">
              {{ t('articles.saveConfiguration') }}
            </button>
          </footer>
        </section>

        <section class="table-section">
          <div class="section-heading">
            <div>
              <h2>{{ t('articles.variants') }}</h2>
              <p>{{ t('articles.variantsHint') }}</p>
            </div>
            <form class="minimum-for-all" @submit.prevent="applyMinimumToAll">
              <label>
                {{ t('articles.minimumForAll') }}
                <input v-model="minimumForAll" type="number" min="0" step="1" inputmode="numeric" />
              </label>
              <button class="secondary-button" type="submit" :disabled="busy || !activeVariants.length">
                {{ t('articles.applyToAll') }}
              </button>
            </form>
          </div>
          <div class="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>{{ t('articles.variant') }}</th>
                  <th class="numeric">{{ t('balances.onHand') }}</th>
                  <th class="numeric">{{ t('articles.salePrice') }}</th>
                  <th class="numeric">{{ t('balances.minimum') }}</th>
                  <th>{{ t('articles.offered') }}</th>
                  <th>{{ t('articles.reorder') }}</th>
                  <th>{{ t('articles.photos.column') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr
                  v-for="variant in activeVariants"
                  :key="variant.id"
                  :class="{ 'low-stock-row': variant.below_minimum }"
                >
                  <td>{{ variantLabel(selected, variant.option_value_ids) || '—' }}</td>
                  <td class="numeric" :class="{ 'out-of-stock': variant.on_hand <= 0 }">
                    {{ variant.on_hand }}
                  </td>
                  <td class="numeric">
                    <input
                      class="cell-input"
                      :value="toInput(variant.sale_price_cents)"
                      inputmode="decimal"
                      @change="onPriceChange(variant.id, ($event.target as HTMLInputElement).value)"
                    />
                  </td>
                  <td class="numeric">
                    <input
                      class="cell-input"
                      :value="variant.minimum_stock ?? ''"
                      inputmode="numeric"
                      :placeholder="t('articles.noWarning')"
                      @change="onMinimumChange(variant.id, ($event.target as HTMLInputElement).value)"
                    />
                  </td>
                  <td>
                    <input
                      type="checkbox"
                      :checked="variant.is_offered"
                      @change="saveVariant(variant.id, { is_offered: ($event.target as HTMLInputElement).checked })"
                    />
                  </td>
                  <td>
                    <input
                      type="checkbox"
                      :checked="!variant.no_reorder"
                      @change="saveVariant(variant.id, { no_reorder: !($event.target as HTMLInputElement).checked })"
                    />
                  </td>
                  <td>
                    <div class="variant-photo-cell">
                      <span v-if="variant.photo_ids.length" class="variant-photo-thumbnails">
                        <img
                          v-for="photoId in variant.photo_ids.slice(0, 3)"
                          :key="photoId"
                          :src="photosApi.fileUrl(photoId)"
                          alt=""
                          loading="lazy"
                        />
                      </span>
                      <button
                        class="compact-button"
                        type="button"
                        @click="openPhotos(variant.id, variantLabel(selected, variant.option_value_ids))"
                      >
                        {{ variant.photo_ids.length
                          ? t('articles.photos.count', { count: variant.photo_ids.length })
                          : t('articles.photos.add') }}
                      </button>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <details v-if="retiredVariants.length" class="retired-variants">
            <summary>{{ t('articles.retired', { count: retiredVariants.length }) }}</summary>
            <p class="muted">{{ t('articles.retiredHint') }}</p>
            <ul class="ranking-list">
              <li v-for="variant in retiredVariants" :key="variant.id">
                <span>{{ variantLabel(selected, variant.option_value_ids) || '—' }}</span>
                <b>{{ format(variant.sale_price_cents) }}</b>
              </li>
            </ul>
          </details>
        </section>
      </div>
    </section>
    <input
      ref="photoInput"
      type="file"
      accept=".jpg,.jpeg,.png,.webp,image/jpeg,image/png,image/webp"
      multiple
      hidden
      @change="onPhotoChosen"
    />

    <dialog v-if="photosFor" class="confirmation-dialog" open>
      <div class="stack-form">
        <div>
          <p class="eyebrow">{{ photosFor.label || '—' }}</p>
          <h2>{{ t('articles.photos.title') }}</h2>
          <p class="muted">{{ t('articles.photos.hint') }}</p>
        </div>

        <p v-if="!variantPhotos.length" class="muted">{{ t('articles.photos.none') }}</p>
        <div v-else class="variant-photo-manager">
          <figure v-for="photo in variantPhotos" :key="photo.id">
            <img :src="photosApi.fileUrl(photo.id)" :alt="photo.original_filename" loading="lazy" />
            <figcaption>
              <button class="compact-button danger-button" type="button" @click="removePhoto(photo)">
                {{ t('common.delete') }}
              </button>
            </figcaption>
          </figure>
        </div>

        <div class="dialog-actions">
          <button class="secondary-button" type="button" :disabled="photoUploading" @click="photoInput?.click()">
            {{ photoUploading ? t('articles.photos.uploading') : t('articles.photos.upload') }}
          </button>
          <button class="primary-button" type="button" :disabled="photoUploading" @click="photosFor = null; load(true)">
            {{ t('common.close') }}
          </button>
        </div>
      </div>
    </dialog>

    <section v-if="session.featureFlags?.csv_import !== false" class="table-section transaction-import-panel">
      <div class="section-heading">
        <div>
          <h2>{{ t('articles.import.title') }}</h2>
          <p>{{ t('articles.import.hint') }}</p>
        </div>
      </div>

      <div class="transaction-import-form">
        <div class="field-grid two-columns">
          <label>
            {{ t('articles.import.kind') }}
            <select v-model="importKind" @change="importPreview = null">
              <option value="einkaeufe">{{ t('articles.import.purchases') }}</option>
              <option value="verkaeufe">{{ t('articles.import.sales') }}</option>
            </select>
          </label>
          <label>
            {{ t('articles.import.file') }}
            <input type="file" accept=".csv,text/csv" @change="onImportFile" />
          </label>
        </div>

        <p class="muted">
          {{ t('articles.import.columns') }}
          <code>{{ importKind === 'einkaeufe'
            ? 'Anzahl; Artikel; Optionen; Einkaufspreis; Gekauft von'
            : 'Anzahl; Artikel; Optionen; Verkaufspreis; Verkauft an' }}</code>
        </p>

        <div class="form-actions">
          <button
            class="secondary-button"
            type="button"
            :disabled="!importFile || importing"
            @click="previewImport"
          >{{ t('articles.import.check') }}</button>
          <button
            class="primary-button"
            type="button"
            :disabled="!importPreview || importing"
            @click="applyImport"
          >{{ t('articles.import.apply') }}</button>
        </div>

        <dl v-if="importPreview" class="import-preview">
          <div>
            <dt>{{ t('articles.import.rows') }}</dt>
            <dd>{{ importPreview.row_count }} ({{ importPreview.total_quantity }} {{ t('common.quantity') }})</dd>
          </div>
          <div>
            <dt>{{ t('articles.import.sum') }}</dt>
            <dd>{{ format(importPreview.total_cents) }}</dd>
          </div>
          <div>
            <dt>{{ t('articles.import.newArticles') }}</dt>
            <dd>{{ importPreview.new_articles.join(', ') || '—' }}</dd>
          </div>
          <div>
            <dt>{{ t('articles.import.newOptions') }}</dt>
            <dd>{{ importPreview.new_option_values.join(', ') || '—' }}</dd>
          </div>
          <div>
            <dt>{{ t('articles.import.newVariants') }}</dt>
            <dd>{{ importPreview.new_variants }}</dd>
          </div>
        </dl>
      </div>
    </section>
  </main>
</template>

<style scoped>
/* One card, two labelled parts, one save. The rule between them separates the
   subjects without suggesting they are saved separately. */
.article-form-group + .article-form-group {
  margin-top: 22px;
  padding-top: 20px;
  border-top: 1px solid var(--border);
}

.article-form-group h3 {
  margin: 0 0 12px;
  font-size: 0.82rem;
  font-weight: 700;
  letter-spacing: 0.12em;
  text-transform: uppercase;
  color: var(--muted);
}

.article-form-group-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.article-form-group-head h3 {
  margin: 0;
}

.article-form-group-head + .muted {
  margin: 6px 0 14px;
}

.article-form-actions {
  margin-top: 22px;
  padding-top: 18px;
  border-top: 1px solid var(--border);
}

.variant-photo-manager {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
}

.variant-photo-manager figure {
  margin: 0;
  text-align: center;
}

.variant-photo-manager img {
  display: block;
  width: 120px;
  height: 120px;
  border: 1px solid var(--border);
  border-radius: 10px;
  object-fit: cover;
}

.variant-photo-manager figcaption {
  margin-top: 6px;
}

.minimum-for-all {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 8px;
}

.minimum-for-all label {
  gap: 4px;
  font-size: 0.76rem;
}

.minimum-for-all input {
  width: 7rem;
}

.variant-photo-cell,
.variant-photo-thumbnails {
  display: flex;
  align-items: center;
}

.variant-photo-cell {
  justify-content: flex-end;
  gap: 8px;
}

.variant-photo-thumbnails img {
  width: 34px;
  height: 34px;
  margin-left: -7px;
  border: 2px solid var(--panel);
  border-radius: 7px;
  object-fit: cover;
}

.variant-photo-thumbnails img:first-child {
  margin-left: 0;
}

.transaction-import-form {
  display: grid;
  gap: 12px;
}

.import-preview {
  display: grid;
  gap: 8px;
  margin: 4px 0 0;
  padding: 14px;
  border: 1px solid var(--border);
  border-radius: 10px;
}

.import-preview > div {
  display: grid;
  grid-template-columns: minmax(140px, 0.4fr) minmax(0, 1fr);
  gap: 12px;
}

.import-preview dt {
  color: var(--muted);
  font-size: 0.78rem;
  font-weight: 700;
}

.import-preview dd {
  min-width: 0;
  margin: 0;
  overflow-wrap: anywhere;
}

.form-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}


.article-layout {
  display: grid;
  grid-template-columns: minmax(210px, 0.8fr) minmax(320px, 3fr);
  gap: 18px;
  align-items: start;
}

@media (max-width: 900px) {
  .article-layout {
    grid-template-columns: minmax(0, 1fr);
  }

  .article-layout > * {
    min-width: 0;
  }
}

.article-editor {
  display: grid;
  gap: 18px;
  min-width: 0;
}

.article-editor > .table-section,
.article-form-group,
.option-editor {
  min-width: 0;
}

.inline-form {
  display: flex;
  gap: 10px;
}

.option-editor {
  padding: 14px 0;
  border-bottom: 1px solid var(--border);
}

.option-editor-head {
  display: flex;
  gap: 10px;
  align-items: center;
  margin-bottom: 10px;
}

.option-editor-head > input {
  flex: 1 1 auto;
  min-width: 0;
}

.option-editor-values {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}

.option-value-input {
  display: inline-flex;
  align-items: center;
  min-width: 0;
  max-width: 100%;
  gap: 4px;
}

.option-value-input input {
  width: 9rem;
  max-width: 100%;
  min-width: 0;
}

.cell-input {
  width: 6rem;
  text-align: right;
}

.numeric {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

/* The shared label styling stacks its text above the control, which looks
   wrong for a checkbox; these put the box and its text on one line. */
.checkbox-row {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 10px;
  margin: 4px 0 14px;
}

.checkbox-row input[type='checkbox'] {
  width: 1.05rem;
  height: 1.05rem;
  accent-color: var(--accent);
}

td input[type='checkbox'] {
  width: 1.05rem;
  height: 1.05rem;
  accent-color: var(--accent);
}

.retired-variants {
  margin-top: 16px;
}

/*
 * The editor is intentionally a real responsive layout rather than a clipped
 * desktop panel. Wide variant tables keep their own horizontal scroller while
 * forms and option controls reflow to the phone width.
 */
@media (max-width: 700px) {
  .page-title-row {
    flex-direction: column;
    align-items: stretch;
  }

  .inline-form {
    flex-wrap: wrap;
    width: 100%;
  }

  .inline-form input {
    flex: 1 1 12rem;
    min-width: 0;
  }

  .article-editor .section-heading {
    flex-direction: column;
    align-items: flex-start;
  }

  .field-grid.two-columns {
    grid-template-columns: minmax(0, 1fr);
  }

  .option-editor-head {
    flex-wrap: wrap;
  }

  .option-editor-head > input {
    flex: 1 1 12rem;
  }

  .option-editor-values {
    width: 100%;
    min-width: 0;
  }

  .option-value-input {
    flex: 1 1 10rem;
  }

  .option-value-input input {
    width: 100%;
  }

  .option-value-input .icon-button {
    flex: 0 0 auto;
  }

  .minimum-for-all {
    width: 100%;
  }

  .minimum-for-all label {
    flex: 1 1 9rem;
    min-width: 0;
  }

  .minimum-for-all input {
    width: 100%;
  }

  .table-scroll {
    width: 100%;
    max-width: 100%;
    min-width: 0;
  }
}
</style>
