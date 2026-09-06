<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { platformApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { BackupRun, BandSummary } from '@/api/types'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'

/**
 * The backup history and the manual trigger.
 *
 * A run is synchronous on purpose: an operator clicking "back up now" before a
 * risky change needs to know it actually finished, not that it was queued.
 */
const { t, d } = useI18n()
const flash = useFlashStore()
const session = useSessionStore()

const runs = ref<BackupRun[]>([])
const bands = ref<BandSummary[]>([])
const loading = ref(true)
const running = ref(false)
const selectedBandId = ref(0)

const canRun = computed(() => session.capabilities?.is_system_admin ?? false)

onMounted(async () => {
  try {
    bands.value = (await platformApi.bands()).bands
  } catch {
    // The list is a convenience; the history still reads without it.
  }
  await load()
})

/** The run a restore is being confirmed for; null while the dialog is closed. */
const restoring = ref<BackupRun | null>(null)
const busy = ref(false)

/**
 * Replaces a band's data with the state the backup captured. Destructive
 * enough to demand an explicit confirmation, and the server takes a safety
 * point before it touches anything.
 */
async function restore() {
  if (!restoring.value || busy.value) return
  busy.value = true
  try {
    const result = await platformApi.restoreBackup(restoring.value.id)
    flash.success(t('platform.backups.restored', { id: result.safety_run.id }))
    restoring.value = null
    await load()
  } catch (error) {
    flash.error(
      error instanceof ApiError
        ? t(`errors.${error.detailCode ?? 'generic'}`, error.message)
        : t('errors.network'),
    )
  } finally {
    busy.value = false
  }
}

/** Drops runs past the retention window the instance is configured for. */
async function prune() {
  if (busy.value) return
  busy.value = true
  try {
    const result = await platformApi.pruneBackups()
    flash.success(t('platform.backups.pruned', { count: result.removed }))
    await load()
  } catch (error) {
    flash.error(
      error instanceof ApiError
        ? t(`errors.${error.detailCode ?? 'generic'}`, error.message)
        : t('errors.network'),
    )
  } finally {
    busy.value = false
  }
}

async function load() {
  loading.value = true
  try {
    runs.value = (await platformApi.backups()).runs
  } catch {
    flash.error(t('errors.generic'))
  } finally {
    loading.value = false
  }
}

async function run() {
  if (running.value) return
  running.value = true
  try {
    await platformApi.runBackup(selectedBandId.value || undefined)
    flash.success(t('platform.backups.done'))
    await load()
  } catch (error) {
    flash.error(
      error instanceof ApiError
        ? t(`errors.${error.detailCode ?? 'generic'}`, error.message)
        : t('errors.network'),
    )
  } finally {
    running.value = false
  }
}

function bandName(id: number | null): string {
  if (id === null) return t('platform.backups.wholeInstance')
  return bands.value.find((band) => band.id === id)?.name ?? `#${id}`
}

function formatBytes(bytes: number): string {
  if (bytes <= 0) return '—'
  const units = ['B', 'kB', 'MB', 'GB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}
</script>

<template>
  <main class="page-shell">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('platform.eyebrow') }}</p>
        <h1>{{ t('platform.backups.title') }}</h1>
        <p class="page-intro">{{ t('platform.backups.intro') }}</p>
      </div>
    </div>

    <section v-if="canRun" class="table-section">
      <div class="section-heading">
        <div>
          <h2>{{ t('platform.backups.runNow') }}</h2>
          <p>{{ t('platform.backups.runNowHint') }}</p>
        </div>
      </div>
      <form class="stack-form" @submit.prevent="run">
        <label>
          {{ t('platform.support.band') }}
          <select v-model.number="selectedBandId">
            <option :value="0">{{ t('platform.backups.wholeInstance') }}</option>
            <option v-for="band in bands" :key="band.id" :value="band.id">{{ band.name }}</option>
          </select>
        </label>
        <button class="primary-button" type="submit" :disabled="running">
          {{ running ? t('platform.backups.running') : t('platform.backups.start') }}
        </button>
      </form>
    </section>

    <section class="table-section">
      <div class="section-heading">
        <div><h2>{{ t('platform.backups.history') }}</h2></div>
        <button v-if="canRun" class="secondary-button" type="button" :disabled="busy" @click="prune">
          {{ t('platform.backups.prune') }}
        </button>
      </div>
      <p v-if="loading" class="muted">{{ t('common.loading') }}</p>
      <p v-else-if="!runs.length" class="muted">{{ t('platform.backups.empty') }}</p>
      <div v-else class="table-scroll">
        <table>
          <thead>
            <tr>
              <th>{{ t('common.date') }}</th>
              <th>{{ t('platform.support.band') }}</th>
              <th>{{ t('platform.backups.trigger') }}</th>
              <th>{{ t('platform.bands.status') }}</th>
              <th class="numeric">{{ t('platform.backups.size') }}</th>
              <th v-if="canRun"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="entry in runs" :key="entry.id">
              <td>{{ d(new Date(entry.started_at), 'short') }}</td>
              <td>{{ bandName(entry.band_id) }}</td>
              <td>{{ t(`platform.backups.triggers.${entry.trigger}`) }}</td>
              <td>
                <span
                  class="status"
                  :class="{ success: entry.status === 'succeeded', danger: entry.status === 'failed' }"
                >{{ t(`platform.backups.status.${entry.status}`) }}</span>
                <small v-if="entry.error">{{ entry.error }}</small>
              </td>
              <td class="numeric">{{ formatBytes(entry.size_bytes) }}</td>
              <td v-if="canRun" class="run-actions">
                <a
                  v-if="entry.status === 'succeeded'"
                  class="compact-button"
                  :href="`/api/v1/platform/backups/${entry.id}/download`"
                >{{ t('platform.backups.download') }}</a>
                <!-- Only a per-band dump can be restored: it is the only one
                     that carries a band's data without the control plane. -->
                <button
                  v-if="entry.status === 'succeeded' && entry.band_id !== null"
                  class="compact-button danger-button"
                  type="button"
                  @click="restoring = entry"
                >{{ t('platform.backups.restore') }}</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <dialog v-if="restoring" class="confirmation-dialog" open>
      <div class="stack-form">
        <div>
          <p class="eyebrow">{{ bandName(restoring.band_id) }}</p>
          <h2>{{ t('platform.backups.restoreTitle') }}</h2>
          <p>
            {{ t('platform.backups.restoreIntro', {
              date: d(new Date(restoring.started_at), 'short'),
            }) }}
          </p>
          <p class="muted">{{ t('platform.backups.restoreSafety') }}</p>
        </div>
        <div class="dialog-actions">
          <button class="secondary-button" type="button" @click="restoring = null">
            {{ t('common.cancel') }}
          </button>
          <button class="danger-button" type="button" :disabled="busy" @click="restore">
            {{ t('platform.backups.restoreConfirm') }}
          </button>
        </div>
      </div>
    </dialog>
  </main>
</template>

<style scoped>
.run-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.numeric {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

td small {
  display: block;
  color: var(--danger);
  font-size: 0.82rem;
}
</style>
