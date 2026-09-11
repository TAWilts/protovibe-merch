<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'

import { catalogueApi, photosApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { Article, Photo } from '@/api/types'
import AppDialog from '@/components/ui/AppDialog.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import TableSkeleton from '@/components/ui/TableSkeleton.vue'
import { useMoney, parseAmount } from '@/composables/useMoney'
import { usePendingChangesGuard } from '@/composables/usePendingChangesGuard'
import { useFlashStore } from '@/stores/flash'
import { useOfflineStore } from '@/stores/offline'
import { useSessionStore } from '@/stores/session'
import {
  changesForStockMode,
  commonStockMode,
  stockModeForVariant,
  stockModeOffers,
  stockModes,
  targetStockEditable,
  type StockMode,
} from '@/utils/stockMode'

/**
 * Article management, ported from _old/templates/articles.html.
 *
 * The option columns are entirely generic: the page never knows that "Farbe"
 * or "Größe" exist, it edits whatever columns the band defined. Once the
 * first variant grid has been generated, its stored options may be renamed,
 * reordered and extended, but no longer removed.
 */
const { t } = useI18n()
const { format } = useMoney()
const flash = useFlashStore()
const session = useSessionStore()
const offline = useOfflineStore()
const router = useRouter()

const articles = ref<Article[]>([])
const loading = ref(true)
const loadFailed = ref(false)
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
  groups: DraftGroup[]
}>({ name: '', salePrice: '', groups: [] })
const salePriceError = ref('')

const newArticleName = ref('')

const selected = computed(
  () => articles.value.find((article) => article.id === selectedId.value) ?? null,
)

/** Only variants the configuration still implies are editable; the rest are
 *  retired and shown read-only so their history stays visible. */
const activeVariants = computed(() => selected.value?.variants.filter((v) => v.is_active) ?? [])
const retiredVariants = computed(() => selected.value?.variants.filter((v) => !v.is_active) ?? [])
const allVariantsMode = computed(() => commonStockMode(activeVariants.value))
// This remains true after a variant grid has existed, even if legacy data has
// since retired every variant. That keeps the UI aligned with the backend's
// structural-removal guard.
const configurationLocked = computed(
  () => selected.value?.configuration_locked ?? selected.value?.configuration_complete ?? false,
)
const unfinishedCreatedArticleId = ref<number | null>(null)
const hasUnfinishedNewArticle = computed(() => {
  const id = unfinishedCreatedArticleId.value
  return id !== null && articles.value.some(
    (article) => article.id === id && !article.configuration_complete,
  )
})

usePendingChangesGuard(hasUnfinishedNewArticle, () => t('articles.unfinishedLeave'))

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
  if (!silent) loadFailed.value = false
  try {
    articles.value = (await catalogueApi.list()).articles
    if (silent) return
    if (selectedId.value === null && articles.value.length) {
      select(articles.value[0].id)
    } else if (selectedId.value !== null) {
      select(selectedId.value)
    }
  } catch {
    if (!silent) loadFailed.value = true
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
    const value = (group.values ?? []).find((entry) => optionValueIds.includes(entry.id))
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

  salePriceError.value = ''
  draft.value = {
    name: article.name,
    // Drafts are created with a zero sentinel in the database. Keep their
    // price field genuinely empty until the manager enters a value. Once an
    // article has been confirmed, an explicit zero remains visible as 0,00.
    salePrice: !article.configuration_complete && article.default_sale_price_cents === 0
      ? ''
      : toInput(article.default_sale_price_cents),
    groups: article.option_groups
      .filter((group) => group.is_active)
      .map((group) => ({
        id: group.id,
        name: group.name,
        values: (group.values ?? [])
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
const targetForAll = ref('')

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
      defer_variants: true,
    })
    newArticleName.value = ''
    flash.success(t('articles.created'))
    selectedId.value = created.id
    unfinishedCreatedArticleId.value = created.id
    await load()
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

async function removeIncompleteArticle() {
  const article = selected.value
  if (!article || article.configuration_complete || busy.value) return
  if (!window.confirm(t('articles.deleteIncompleteConfirm', { name: article.name }))) return
  busy.value = true
  try {
    await catalogueApi.removeIncomplete(article.id)
    articles.value = articles.value.filter((entry) => entry.id !== article.id)
    if (unfinishedCreatedArticleId.value === article.id) unfinishedCreatedArticleId.value = null
    selectedId.value = null
    if (articles.value.length) select(articles.value[0].id)
    flash.success(t('articles.deleted'))
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

function startHistoricalSales() {
  if (!offline.online) {
    flash.error(t('sales.historicalOffline'))
    return
  }
  void router.push({ name: 'sales', query: { mode: 'historical' } })
}

// A new value or group gets id 0, which is how the server tells "add this"
// from "update that".
function addValue(group: DraftGroup) {
  group.values.push({ id: 0, value: '' })
}

function removeValue(group: DraftGroup, index: number) {
  const value = group.values[index]
  if (configurationLocked.value && value?.id !== 0) return
  group.values.splice(index, 1)
}

function moveItem<T>(items: T[], index: number, delta: number) {
  const target = index + delta
  if (target < 0 || target >= items.length) return
  const [item] = items.splice(index, 1)
  if (item === undefined) return
  items.splice(target, 0, item)
}

function moveValue(group: DraftGroup, index: number, delta: number) {
  moveItem(group.values, index, delta)
}

function addGroup() {
  draft.value.groups.push({ id: 0, name: '', values: [{ id: 0, value: '' }] })
}

function removeGroup(index: number) {
  const group = draft.value.groups[index]
  if (configurationLocked.value && group?.id !== 0) return
  draft.value.groups.splice(index, 1)
}

function moveGroup(index: number, delta: number) {
  moveItem(draft.value.groups, index, delta)
}

async function save() {
  if (!selected.value || busy.value) return

  const rawSalePrice = draft.value.salePrice.trim()
  if (!rawSalePrice) {
    salePriceError.value = t('articles.salePriceRequired')
    flash.error(salePriceError.value)
    return
  }

  const sale = parseAmount(rawSalePrice)
  if (sale === null) {
    salePriceError.value = t('articles.invalidPrice')
    flash.error(salePriceError.value)
    return
  }
  salePriceError.value = ''

  const preparedGroups = draft.value.groups.map((group) => ({
    id: group.id,
    name: group.name.trim(),
    values: group.values.map((value) => ({ id: value.id, value: value.value.trim() })),
  }))
  if (preparedGroups.some((group) => !group.name || group.values.length === 0 || group.values.some((value) => !value.value))) {
    flash.error(t('articles.incompleteOptions'))
    return
  }

  const newGroups = configurationLocked.value
    ? preparedGroups.filter((group) => group.id === 0)
    : []
  if (newGroups.length) {
    const mappings = newGroups
      .map((group) => `${group.name}: ${group.values[0]?.value ?? ''}`)
      .join('\n')
    if (!window.confirm(t('articles.newOptionConfirm', { mappings }))) return
  }

  busy.value = true

  try {
    const saved = await catalogueApi.save(selected.value.id, {
      name: draft.value.name.trim(),
      default_sale_price_cents: sale,
      option_groups: preparedGroups,
    })
    const index = articles.value.findIndex((article) => article.id === saved.id)
    if (index >= 0) articles.value.splice(index, 1, saved)
    if (saved.configuration_complete && unfinishedCreatedArticleId.value === saved.id) {
      unfinishedCreatedArticleId.value = null
    }
    select(saved.id)
    flash.success(t('articles.saved'))
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
  // Vue number inputs may assign a number even when the ref started as a
  // string. Normalising first avoids calling trim() on a number and silently
  // aborting the form submission in the browser.
  const parsed = Number(String(minimumForAll.value).trim())
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

function isStockMode(value: string): value is StockMode {
  return stockModes.includes(value as StockMode)
}

function stockModeHint(mode: StockMode) {
  return t(`articles.stockModes.${mode}.hint`)
}

async function applyVariantStockMode(variant: Article['variants'][number], rawMode: string) {
  if (!selected.value || !isStockMode(rawMode)) return
  const mode = rawMode
  const payload: Record<string, unknown> = {
    variants: [{ id: variant.id, ...changesForStockMode(variant, mode) }],
  }
  // Offering one variant must also reopen its article. Pausing one variant is
  // deliberately not allowed to hide all of its siblings.
  if (!selected.value.is_offered && stockModeOffers(mode)) payload.is_offered = true
  try {
    await catalogueApi.save(selected.value.id, payload)
    await load(true)
  } catch (error) {
    report(error)
  }
}

async function applyStockModeToAll(rawMode: string) {
  if (!selected.value || busy.value || !isStockMode(rawMode)) return
  const mode = rawMode
  busy.value = true
  try {
    await catalogueApi.save(selected.value.id, {
      is_offered: stockModeOffers(mode),
      variants: activeVariants.value.map((variant) => ({
        id: variant.id,
        ...changesForStockMode(variant, mode),
      })),
    })
    flash.success(t('articles.stockModeApplied', { count: activeVariants.value.length }))
    await load(true)
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

function onTargetChange(variantId: number, raw: string) {
  const trimmed = raw.trim()
  if (trimmed === '') {
    saveVariant(variantId, { clear_target_stock: true })
    return
  }
  const parsed = Number(trimmed)
  if (!Number.isInteger(parsed) || parsed < 0) {
    flash.error(t('articles.invalidTarget'))
    return
  }
  saveVariant(variantId, { target_stock: parsed })
}

async function applyTargetToAll() {
  if (!selected.value || busy.value) return
  const parsed = Number(String(targetForAll.value).trim())
  if (!Number.isInteger(parsed) || parsed < 0) {
    flash.error(t('articles.invalidTarget'))
    return
  }
  busy.value = true
  try {
    await catalogueApi.save(selected.value.id, {
      variants: activeVariants.value.map((variant) => ({ id: variant.id, target_stock: parsed })),
    })
    flash.success(t('articles.targetApplied', { count: activeVariants.value.length }))
    await load(true)
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="page-shell operational-page articles-page">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('articles.eyebrow') }}</p>
        <h1>{{ t('articles.title') }}</h1>
      </div>
      <button
        v-if="session.capabilities?.can_manage_purchases"
        class="secondary-button"
        type="button"
        :disabled="!offline.online"
        :title="!offline.online ? t('sales.historicalOffline') : undefined"
        @click="startHistoricalSales"
      >
        {{ t('sales.historicalEnter') }}
      </button>
    </div>

    <section class="article-create-panel">
      <form class="inline-form" @submit.prevent="createArticle">
        <input v-model="newArticleName" :placeholder="t('articles.newPlaceholder')" />
        <button class="primary-button" type="submit" :disabled="!newArticleName.trim() || busy">
          {{ t('articles.create') }}
        </button>
      </form>
    </section>

    <TableSkeleton v-if="loading" :label="t('common.loading')" :rows="4" :columns="3" />
    <EmptyState v-else-if="loadFailed" tone="error" :title="t('errors.generic')">
      <button class="secondary-button" type="button" @click="load()">{{ t('common.retry') }}</button>
    </EmptyState>
    <EmptyState v-else-if="!articles.length" :title="t('articles.empty')" />

    <section v-else class="article-layout">
      <aside class="selection-panel">
        <h2>{{ t('sales.articles') }}</h2>
        <div class="button-list">
          <button
            v-for="article in articles"
            :key="article.id"
            type="button"
            class="selection-button"
            :class="{ selected: article.id === selectedId }"
            :aria-pressed="article.id === selectedId"
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
            <label class="sale-price-field">
              {{ t('articles.defaultSalePrice') }}
              <input
                v-model="draft.salePrice"
                inputmode="decimal"
                :aria-invalid="salePriceError ? 'true' : undefined"
                :aria-describedby="salePriceError ? 'article-sale-price-error' : undefined"
                @input="salePriceError = ''"
              />
              <small
                v-if="salePriceError"
                id="article-sale-price-error"
                class="field-error"
                role="alert"
              >{{ salePriceError }}</small>
            </label>
          </div>

          <div class="article-form-group">
            <div class="article-form-group-head">
              <h3>{{ t('articles.options') }}</h3>
              <button class="secondary-button" type="button" @click="addGroup">
                {{ t('articles.addOption') }}
              </button>
            </div>
            <p class="muted">{{ t('articles.optionsHint') }}</p>
            <p v-if="configurationLocked" class="notice option-lock-hint">
              {{ t('articles.optionsLockedHint') }}
            </p>

            <div v-for="(group, groupIndex) in draft.groups" :key="groupIndex" class="option-editor">
              <div class="option-editor-head">
                <div class="option-reorder" :aria-label="t('articles.reorderOption')">
                  <button
                    class="icon-button"
                    type="button"
                    :disabled="groupIndex === 0"
                    :title="t('articles.moveOptionUp')"
                    :aria-label="t('articles.moveOptionUp')"
                    @click="moveGroup(groupIndex, -1)"
                  >↑</button>
                  <button
                    class="icon-button"
                    type="button"
                    :disabled="groupIndex === draft.groups.length - 1"
                    :title="t('articles.moveOptionDown')"
                    :aria-label="t('articles.moveOptionDown')"
                    @click="moveGroup(groupIndex, 1)"
                  >↓</button>
                </div>
                <input v-model="group.name" :placeholder="t('articles.optionName')" />
                <button
                  v-if="!configurationLocked || group.id === 0"
                  class="compact-button"
                  :class="{ 'danger-button': !configurationLocked }"
                  type="button"
                  @click="removeGroup(groupIndex)"
                >
                  {{ configurationLocked ? t('articles.discardUnsavedOption') : t('common.delete') }}
                </button>
              </div>
              <p v-if="configurationLocked && group.id === 0" class="notice new-option-notice">
                {{ t('articles.newOptionExistingHint') }}
              </p>
              <div class="option-editor-values">
                <span v-for="(value, valueIndex) in group.values" :key="valueIndex" class="option-value-input">
                  <span class="value-reorder" :aria-label="t('articles.reorderValue')">
                    <button
                      class="icon-button"
                      type="button"
                      :disabled="valueIndex === 0"
                      :title="t('articles.moveValueEarlier')"
                      :aria-label="t('articles.moveValueEarlier')"
                      @click="moveValue(group, valueIndex, -1)"
                    >←</button>
                    <button
                      class="icon-button"
                      type="button"
                      :disabled="valueIndex === group.values.length - 1"
                      :title="t('articles.moveValueLater')"
                      :aria-label="t('articles.moveValueLater')"
                      @click="moveValue(group, valueIndex, 1)"
                    >→</button>
                  </span>
                  <span
                    v-if="configurationLocked && group.id === 0 && valueIndex === 0"
                    class="existing-configuration-badge"
                  >{{ t('articles.existingConfiguration') }}</span>
                  <input v-model="value.value" :placeholder="t('articles.optionValue')" />
                  <button
                    v-if="!configurationLocked || value.id === 0"
                    class="icon-button"
                    type="button"
                    :title="configurationLocked ? t('articles.discardUnsavedValue') : t('common.delete')"
                    :aria-label="configurationLocked ? t('articles.discardUnsavedValue') : t('common.delete')"
                    @click="removeValue(group, valueIndex)"
                  >×</button>
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
            <button
              v-if="!selected.configuration_complete"
              class="danger-button"
              type="button"
              :disabled="busy"
              @click="removeIncompleteArticle"
            >
              {{ t('articles.deleteIncomplete') }}
            </button>
          </footer>
        </section>

        <section v-if="selected.variants.length" class="table-section">
          <div class="section-heading">
            <div>
              <h2>{{ t('articles.variants') }}</h2>
              <p>{{ t('articles.variantsHint') }}</p>
            </div>
          </div>
          <div class="variant-bulk-controls">
            <strong>{{ t('articles.forAllVariants') }}</strong>
            <label class="stock-mode-for-all">
              {{ t('articles.stockMode') }}
              <select
                :value="allVariantsMode"
                :disabled="busy || !activeVariants.length"
                @change="applyStockModeToAll(($event.target as HTMLSelectElement).value)"
              >
                <option v-if="allVariantsMode === 'mixed'" value="mixed" disabled>
                  {{ t('articles.stockModes.mixed.label') }}
                </option>
                <option v-for="mode in stockModes" :key="mode" :value="mode">
                  {{ t(`articles.stockModes.${mode}.label`) }}
                </option>
              </select>
              <small v-if="allVariantsMode !== 'mixed'">{{ stockModeHint(allVariantsMode) }}</small>
              <small v-else>{{ t('articles.stockModes.mixed.hint') }}</small>
            </label>
            <form class="minimum-for-all" @submit.prevent="applyMinimumToAll">
              <label>
                {{ t('articles.minimumForAll') }}
                <input v-model="minimumForAll" type="number" min="0" step="1" inputmode="numeric" />
              </label>
              <button class="secondary-button" type="submit" :disabled="busy || !activeVariants.length">
                {{ t('articles.applyToAll') }}
              </button>
            </form>
            <form class="minimum-for-all target-for-all" @submit.prevent="applyTargetToAll">
              <label>
                {{ t('articles.targetForAll') }}
                <input v-model="targetForAll" type="number" min="0" step="1" inputmode="numeric" />
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
                  <th class="numeric">{{ t('articles.targetStock') }}</th>
                  <th>{{ t('articles.stockMode') }}</th>
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
                  <td class="numeric">
                    <input
                      class="cell-input"
                      :value="variant.target_stock ?? ''"
                      inputmode="numeric"
                      :placeholder="t('articles.noTarget')"
                      :disabled="!targetStockEditable(stockModeForVariant(variant))"
                      :title="!targetStockEditable(stockModeForVariant(variant)) ? stockModeHint(stockModeForVariant(variant)) : undefined"
                      @change="onTargetChange(variant.id, ($event.target as HTMLInputElement).value)"
                    />
                    <small v-if="stockModeForVariant(variant) === 'on_demand'" class="stock-mode-inline-hint">
                      {{ t('articles.stockModes.on_demand.targetHint') }}
                    </small>
                  </td>
                  <td class="stock-mode-cell">
                    <select
                      :value="stockModeForVariant(variant)"
                      :disabled="busy"
                      :aria-label="`${t('articles.stockMode')}: ${variantLabel(selected, variant.option_value_ids) || t('articles.variant')}`"
                      :title="stockModeHint(stockModeForVariant(variant))"
                      @change="applyVariantStockMode(variant, ($event.target as HTMLSelectElement).value)"
                    >
                      <option v-for="mode in stockModes" :key="mode" :value="mode">
                        {{ t(`articles.stockModes.${mode}.label`) }}
                      </option>
                    </select>
                    <small>{{ stockModeHint(stockModeForVariant(variant)) }}</small>
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

    <AppDialog v-if="photosFor" :label="t('articles.photos.title')" :dismissible="!photoUploading" @close="photosFor = null">
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
    </AppDialog>

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

.article-layout > .selection-panel {
  position: sticky;
  top: 86px;
}

.article-create-panel {
  display: flex;
  justify-content: flex-start;
  margin-bottom: 14px;
}

.article-create-panel .inline-form {
  width: min(100%, 620px);
}

.article-create-panel input {
  flex: 1 1 auto;
  min-width: 0;
}

.article-layout .selection-button.selected {
  box-shadow: inset 3px 0 var(--accent);
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
  border-radius: var(--radius-control);
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

.sale-price-field .field-error {
  color: var(--danger-text);
  font-size: .76rem;
  font-weight: 650;
}

.sale-price-field input[aria-invalid='true'] {
  border-color: var(--danger);
  box-shadow: 0 0 0 3px var(--danger-soft);
}

.variant-bulk-controls {
  display: grid;
  grid-template-columns: minmax(12rem, 1.2fr) repeat(2, minmax(13rem, 1fr));
  align-items: end;
  gap: 14px;
  margin: 0 0 18px;
  padding: 14px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-control);
  background: var(--surface-subtle);
}

.variant-bulk-controls > strong {
  grid-column: 1 / -1;
  color: var(--text-primary);
  font-size: .82rem;
}

.stock-mode-for-all small,
.stock-mode-cell small,
.stock-mode-inline-hint {
  display: block;
  color: var(--muted);
  font-size: .7rem;
  line-height: 1.3;
}

.stock-mode-cell {
  min-width: 13rem;
}

.stock-mode-cell select {
  min-width: 11rem;
}

.stock-mode-cell small {
  max-width: 18rem;
  margin-top: 5px;
}

.cell-input:disabled {
  opacity: .58;
  cursor: not-allowed;
}

.stock-mode-inline-hint {
  width: 8rem;
  margin-top: 4px;
  text-align: left;
  white-space: normal;
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
  border-radius: var(--radius-control);
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

  .article-layout > .selection-panel {
    position: static;
  }
}

.article-editor {
  display: grid;
  gap: 18px;
  min-width: 0;
}

.article-form {
  border-color: var(--border-default);
  background: var(--surface-raised);
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
  margin-top: 10px;
  padding: 14px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-control);
  background: var(--surface-subtle);
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

.option-reorder,
.value-reorder {
  display: inline-flex;
  flex: 0 0 auto;
  gap: 3px;
}

.option-reorder .icon-button,
.value-reorder .icon-button {
  width: 28px;
  height: 28px;
  min-width: 28px;
}

.option-reorder .icon-button:disabled,
.value-reorder .icon-button:disabled {
  opacity: 0.32;
  cursor: default;
}

.option-editor-values {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}

.option-lock-hint,
.new-option-notice {
  margin-top: 10px;
}

.existing-configuration-badge {
  padding: 3px 7px;
  border: 1px solid var(--warning-border);
  border-radius: 999px;
  background: var(--warning-soft);
  color: var(--warning-text);
  font-size: 0.72rem;
  font-weight: 700;
  white-space: nowrap;
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

  .variant-bulk-controls {
    grid-template-columns: minmax(0, 1fr);
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
