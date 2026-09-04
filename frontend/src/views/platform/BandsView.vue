<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { platformApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { BandSummary } from '@/api/types'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'

/**
 * The tenant list: every band, what it holds, and the levers an operator has.
 *
 * Deactivating and deleting are separated on purpose. Deactivating stops
 * sign-ins and touches no data; deleting only starts a grace period. Neither
 * removes a single booking, because a band's season is not something to lose
 * to a misclick.
 */
const { t, d } = useI18n()
const flash = useFlashStore()
const session = useSessionStore()

const bands = ref<BandSummary[]>([])
const loading = ref(true)
const includeDeleted = ref(false)
const filter = ref('')

const canManage = computed(() => session.capabilities?.is_system_admin ?? false)

const newBand = ref({ slug: '', name: '', contact_email: '' })
const creating = ref(false)

const gibibyte = 1024 ** 3
const globalQuotaGb = ref('5')
const quotaPrompt = ref<{ band: BandSummary; inherit: boolean; quotaGb: string } | null>(null)
const quotaBusy = ref(false)

/**
 * Handing a band its first administrator. A band with no account cannot be
 * reached any other way — only a band admin may create accounts, and support
 * access needs a band admin to approve it.
 */
const adminPrompt = ref<{ band: BandSummary; username: string } | null>(null)
/** A setup code is shown exactly once, right after it is issued. */
const issuedCode = ref<{ band: string; slug: string; username: string; code: string } | null>(null)
const copied = ref(false)
const busy = ref(false)

const visible = computed(() => {
  const needle = filter.value.trim().toLowerCase()
  if (!needle) return bands.value
  return bands.value.filter((band) =>
    `${band.name} ${band.slug} ${band.contact_email}`.toLowerCase().includes(needle),
  )
})

onMounted(load)

async function load() {
  loading.value = true
  try {
    const payload = await platformApi.bands(includeDeleted.value)
    bands.value = payload.bands
    globalQuotaGb.value = bytesToGiBInput(payload.default_storage_quota_bytes)
  } catch {
    flash.error(t('errors.generic'))
  } finally {
    loading.value = false
  }
}

function report(error: unknown) {
  flash.error(
    error instanceof ApiError
      ? t(`errors.${error.detailCode ?? 'generic'}`, error.message)
      : t('errors.network'),
  )
}

async function create() {
  if (creating.value) return
  creating.value = true
  try {
    await platformApi.createBand({
      slug: newBand.value.slug.trim(),
      name: newBand.value.name.trim(),
      contact_email: newBand.value.contact_email.trim(),
    })
    flash.success(t('platform.bands.created'))
    newBand.value = { slug: '', name: '', contact_email: '' }
    await load()
  } catch (error) {
    report(error)
  } finally {
    creating.value = false
  }
}

async function createBandAdmin() {
  if (!adminPrompt.value || busy.value) return
  const { band } = adminPrompt.value
  const username = adminPrompt.value.username.trim()
  if (!username) return

  busy.value = true
  try {
    const created = await platformApi.createBandAdmin(band.id, username)
    adminPrompt.value = null
    copied.value = false
    issuedCode.value = {
      band: band.name, slug: band.slug, username: created.username, code: created.setup_code,
    }
    await load()
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

/**
 * The three values the new admin needs are useless one at a time — they have
 * to be passed on together, and the dialog is the only time the code is ever
 * visible. So the button copies the whole set, not just the code.
 */
async function copyHandover() {
  if (!issuedCode.value) return
  const { slug, username, code } = issuedCode.value
  try {
    await navigator.clipboard.writeText(
      `${t('platform.bands.slug')}: ${slug}\n${t('platform.bands.adminUsername')}: ${username}\n${t('platform.bands.adminCode')}: ${code}`,
    )
    copied.value = true
  } catch {
    // A denied clipboard is no reason to lose the code: it stays on screen.
    flash.error(t('platform.bands.copyFailed'))
  }
}

async function run(action: () => Promise<unknown>, message: string) {
  try {
    await action()
    flash.success(message)
    await load()
  } catch (error) {
    report(error)
  }
}

function formatBytes(bytes: number): string {
  if (bytes <= 0) return bytes === 0 ? '0 B' : '—'
  const units = ['B', 'kB', 'MB', 'GB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}

function bytesToGiBInput(bytes: number): string {
  if (bytes <= 0) return '0'
  return (bytes / gibibyte).toFixed(2).replace(/\.00$/, '').replace(/(\.\d)0$/, '$1')
}

function quotaBytes(value: string): number | null {
  const parsed = Number(value.replace(',', '.'))
  if (!Number.isFinite(parsed) || parsed <= 0) return null
  return Math.round(parsed * gibibyte)
}

function quotaPercent(band: BandSummary): number {
  if (band.effective_storage_quota_bytes <= 0) return 0
  return Math.min(100, Math.round((band.storage_bytes / band.effective_storage_quota_bytes) * 100))
}

function openQuota(band: BandSummary) {
  quotaPrompt.value = {
    band,
    inherit: band.storage_quota_bytes === 0,
    quotaGb: bytesToGiBInput(
      band.storage_quota_bytes > 0 ? band.storage_quota_bytes : band.effective_storage_quota_bytes,
    ),
  }
}

async function saveBandQuota() {
  if (!quotaPrompt.value || quotaBusy.value) return
  const bytes = quotaPrompt.value.inherit ? 0 : quotaBytes(quotaPrompt.value.quotaGb)
  if (bytes === null) {
    flash.error(t('platform.bands.quotaInvalid'))
    return
  }
  quotaBusy.value = true
  try {
    await platformApi.updateBand(quotaPrompt.value.band.id, { storage_quota_bytes: bytes })
    quotaPrompt.value = null
    flash.success(t('platform.bands.quotaSaved'))
    await load()
  } catch (error) {
    report(error)
  } finally {
    quotaBusy.value = false
  }
}

async function saveGlobalQuota(applyToAll: boolean) {
  if (quotaBusy.value) return
  const bytes = quotaBytes(globalQuotaGb.value)
  if (bytes === null) {
    flash.error(t('platform.bands.quotaInvalid'))
    return
  }
  if (applyToAll && !window.confirm(t('platform.bands.quotaApplyAllConfirm'))) return

  quotaBusy.value = true
  try {
    await platformApi.setStorageQuotaForAll(bytes, applyToAll)
    flash.success(t(applyToAll ? 'platform.bands.quotaAppliedAll' : 'platform.bands.quotaSaved'))
    await load()
  } catch (error) {
    report(error)
  } finally {
    quotaBusy.value = false
  }
}

function formatDate(value: string | null): string {
  return value ? d(new Date(value), 'short') : '—'
}
</script>

<template>
  <main class="page-shell">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('platform.eyebrow') }}</p>
        <h1>{{ t('platform.bands.title') }}</h1>
      </div>
      <div class="ledger-actions">
        <label class="table-filter">
          {{ t('common.filter') }}
          <input v-model="filter" type="search" />
        </label>
        <label class="checkbox-row">
          <input v-model="includeDeleted" type="checkbox" @change="load" />
          <span>{{ t('platform.bands.showDeleted') }}</span>
        </label>
      </div>
    </div>

    <section v-if="canManage" class="table-section">
      <div class="section-heading">
        <div>
          <h2>{{ t('platform.bands.new') }}</h2>
          <p>{{ t('platform.bands.newHint') }}</p>
        </div>
      </div>
      <form class="stack-form" @submit.prevent="create">
        <div class="field-grid two-columns">
          <label>{{ t('platform.bands.slug') }}<input v-model="newBand.slug" required /></label>
          <label>{{ t('platform.bands.name') }}<input v-model="newBand.name" required /></label>
        </div>
        <label>{{ t('platform.bands.contact') }}<input v-model="newBand.contact_email" type="email" /></label>
        <button class="primary-button" type="submit" :disabled="creating">
          {{ t('platform.bands.create') }}
        </button>
      </form>
    </section>

    <section v-if="canManage" class="table-section quota-settings">
      <div class="section-heading">
        <div>
          <h2>{{ t('platform.bands.quotaTitle') }}</h2>
          <p>{{ t('platform.bands.quotaHint') }}</p>
        </div>
      </div>
      <div class="quota-global-form">
        <label>
          {{ t('platform.bands.quotaGb') }}
          <div class="quota-input">
            <input v-model="globalQuotaGb" type="text" inputmode="decimal" />
            <span>GB</span>
          </div>
        </label>
        <div class="quota-global-actions">
          <button class="secondary-button" type="button" :disabled="quotaBusy" @click="saveGlobalQuota(false)">
            {{ t('platform.bands.quotaSaveDefault') }}
          </button>
          <button class="primary-button" type="button" :disabled="quotaBusy" @click="saveGlobalQuota(true)">
            {{ t('platform.bands.quotaApplyAll') }}
          </button>
        </div>
      </div>
    </section>

    <section class="table-section">
      <p v-if="loading" class="muted">{{ t('common.loading') }}</p>
      <p v-else-if="!visible.length" class="muted">{{ t('platform.bands.empty') }}</p>

      <div v-else class="table-scroll">
        <table>
          <thead>
            <tr>
              <th>{{ t('platform.bands.band') }}</th>
              <th class="numeric">{{ t('platform.bands.users') }}</th>
              <th class="numeric">{{ t('platform.bands.articles') }}</th>
              <th class="numeric">{{ t('platform.bands.sales') }}</th>
              <th>{{ t('platform.bands.storage') }}</th>
              <th>{{ t('platform.bands.lastActivity') }}</th>
              <th>{{ t('platform.bands.lastBackup') }}</th>
              <th>{{ t('platform.bands.status') }}</th>
              <th v-if="canManage"></th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="band in visible"
              :key="band.id"
              :class="{ 'cancelled-row': !!band.deleted_at }"
            >
              <td>
                <strong>{{ band.name }}</strong>
                <small><code>{{ band.slug }}</code> · {{ band.contact_email || '—' }}</small>
              </td>
              <td class="numeric">{{ band.user_count }}</td>
              <td class="numeric">{{ band.article_count }}</td>
              <td class="numeric">{{ band.sale_count }}</td>
              <td class="storage-cell">
                <strong :class="{ 'quota-over': band.storage_bytes > band.effective_storage_quota_bytes }">
                  {{ formatBytes(band.storage_bytes) }} / {{ formatBytes(band.effective_storage_quota_bytes) }}
                </strong>
                <progress :value="quotaPercent(band)" max="100" :title="`${quotaPercent(band)} %`" />
                <small>
                  {{ band.storage_quota_bytes === 0 ? t('platform.bands.quotaGlobal') : t('platform.bands.quotaIndividual') }}
                  <span v-if="band.storage_bytes > band.effective_storage_quota_bytes">
                    · {{ t('platform.bands.quotaExceeded') }}
                  </span>
                </small>
                <button v-if="canManage" class="compact-button" type="button" @click="openQuota(band)">
                  {{ t('platform.bands.quotaEdit') }}
                </button>
              </td>
              <td>{{ formatDate(band.last_activity_at) }}</td>
              <td>{{ formatDate(band.last_backup_at) }}</td>
              <td>
                <span v-if="band.deleted_at" class="status danger">{{ t('platform.bands.deleted') }}</span>
                <span v-else-if="!band.is_active" class="status warning">{{ t('platform.bands.inactive') }}</span>
                <span v-else class="status success">{{ t('platform.bands.active') }}</span>
                <span v-if="band.active_grant_id" class="status warning">
                  {{ t('platform.bands.supportActive') }}
                </span>
              </td>
              <td v-if="canManage" class="band-actions">
                <button
                  v-if="band.deleted_at"
                  class="compact-button"
                  type="button"
                  @click="run(() => platformApi.restoreBand(band.id), t('platform.bands.restored'))"
                >{{ t('platform.bands.restore') }}</button>
                <template v-else>
                  <button
                    v-if="band.is_active"
                    class="compact-button"
                    type="button"
                    @click="run(() => platformApi.deactivateBand(band.id), t('platform.bands.deactivated'))"
                  >{{ t('platform.bands.deactivate') }}</button>
                  <button
                    v-else
                    class="compact-button"
                    type="button"
                    @click="run(() => platformApi.activateBand(band.id), t('platform.bands.activated'))"
                  >{{ t('platform.bands.activate') }}</button>
                  <button
                    class="compact-button"
                    type="button"
                    @click="run(() => platformApi.revokeBandSessions(band.id), t('platform.bands.sessionsRevoked'))"
                  >{{ t('platform.bands.revokeSessions') }}</button>
                  <button
                    class="compact-button"
                    type="button"
                    @click="adminPrompt = { band, username: '' }"
                  >{{ t('platform.bands.addAdmin') }}</button>
                  <button
                    class="compact-button danger-button"
                    type="button"
                    @click="run(() => platformApi.deleteBand(band.id), t('platform.bands.deletedMessage'))"
                  >{{ t('common.delete') }}</button>
                </template>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-if="canManage" class="muted">{{ t('platform.bands.deleteHint') }}</p>
    </section>

    <dialog v-if="quotaPrompt" class="confirmation-dialog" open>
      <form class="stack-form" @submit.prevent="saveBandQuota">
        <div>
          <p class="eyebrow">{{ quotaPrompt.band.name }}</p>
          <h2>{{ t('platform.bands.quotaEditTitle') }}</h2>
        </div>
        <label class="checkbox-row">
          <input v-model="quotaPrompt.inherit" type="checkbox" />
          <span>{{ t('platform.bands.quotaInherit') }}</span>
        </label>
        <label v-if="!quotaPrompt.inherit">
          {{ t('platform.bands.quotaGb') }}
          <div class="quota-input">
            <input v-model="quotaPrompt.quotaGb" type="text" inputmode="decimal" required />
            <span>GB</span>
          </div>
        </label>
        <div class="dialog-actions">
          <button class="secondary-button" type="button" @click="quotaPrompt = null">{{ t('common.cancel') }}</button>
          <button class="primary-button" type="submit" :disabled="quotaBusy">{{ t('common.save') }}</button>
        </div>
      </form>
    </dialog>

    <dialog v-if="adminPrompt" class="confirmation-dialog" open>
      <form class="stack-form" @submit.prevent="createBandAdmin">
        <div>
          <p class="eyebrow">{{ adminPrompt.band.name }}</p>
          <h2>{{ t('platform.bands.addAdmin') }}</h2>
          <p>{{ t('platform.bands.addAdminIntro') }}</p>
        </div>
        <label>
          {{ t('platform.bands.adminUsername') }}
          <input v-model="adminPrompt.username" autocomplete="off" required autofocus />
        </label>
        <div class="dialog-actions">
          <button class="secondary-button" type="button" @click="adminPrompt = null">
            {{ t('common.cancel') }}
          </button>
          <button class="primary-button" type="submit" :disabled="busy">{{ t('common.confirm') }}</button>
        </div>
      </form>
    </dialog>

    <dialog v-if="issuedCode" class="confirmation-dialog" open>
      <div class="stack-form">
        <div>
          <p class="eyebrow">{{ issuedCode.band }}</p>
          <h2>{{ t('platform.bands.adminCreated', { user: issuedCode.username }) }}</h2>
        </div>
        <dl class="handover">
          <div>
            <dt>{{ t('platform.bands.slug') }}</dt>
            <dd><code>{{ issuedCode.slug }}</code></dd>
          </div>
          <div>
            <dt>{{ t('platform.bands.adminUsername') }}</dt>
            <dd><code>{{ issuedCode.username }}</code></dd>
          </div>
          <div>
            <dt>{{ t('platform.bands.adminCode') }}</dt>
            <dd><code>{{ issuedCode.code }}</code></dd>
          </div>
        </dl>
        <p class="muted">{{ t('platform.bands.adminCodeHint') }}</p>
        <div class="dialog-actions">
          <button class="secondary-button" type="button" @click="copyHandover">
            {{ copied ? t('platform.bands.copied') : t('platform.bands.copy') }}
          </button>
          <button class="primary-button" type="button" @click="issuedCode = null">
            {{ t('common.close') }}
          </button>
        </div>
      </div>
    </dialog>
  </main>
</template>

<style scoped>
.handover {
  display: grid;
  gap: 8px;
  margin: 0;
}

.handover > div {
  display: grid;
  grid-template-columns: minmax(90px, 0.4fr) minmax(0, 1fr);
  gap: 12px;
  align-items: baseline;
}

.handover dt {
  color: var(--muted);
  font-size: 0.78rem;
  font-weight: 700;
}

.handover dd {
  min-width: 0;
  margin: 0;
  overflow-wrap: anywhere;
}

.numeric {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.quota-global-form,
.quota-global-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  align-items: end;
}

.quota-global-form {
  justify-content: space-between;
}

.quota-input {
  display: flex;
  align-items: center;
  gap: 8px;
}

.quota-input input {
  max-width: 9rem;
}

.storage-cell {
  min-width: 180px;
}

.storage-cell strong,
.storage-cell small,
.storage-cell progress {
  display: block;
}

.storage-cell progress {
  width: 100%;
  margin: 5px 0;
}

.storage-cell .compact-button {
  margin-top: 6px;
}

.quota-over {
  color: var(--danger);
}

td small {
  display: block;
  color: var(--muted);
}

.band-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.checkbox-row {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 8px;
}

.checkbox-row input[type='checkbox'] {
  width: 1.05rem;
  height: 1.05rem;
  accent-color: var(--accent);
}
</style>
