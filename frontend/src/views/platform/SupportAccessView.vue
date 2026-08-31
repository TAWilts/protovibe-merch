<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { platformApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { BandSummary, SupportGrant } from '@/api/types'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'

/**
 * The platform side of the support-access workflow.
 *
 * Nothing on this page grants access by itself. A request is an ask; the band
 * decides; and activating still needs a fresh authenticator code, so an
 * approval granted this morning is worthless from a stolen laptop tonight.
 */
const { t, d } = useI18n()
const flash = useFlashStore()
const session = useSessionStore()

const bands = ref<BandSummary[]>([])
const grants = ref<SupportGrant[]>([])
const loading = ref(true)
const busy = ref(false)

const form = ref({
  bandId: 0,
  reason: '',
  scope: 'read_only' as 'read_only' | 'read_write',
  durationMinutes: 60,
})

/** The code entered when actually starting a granted window. */
const activating = ref<{ grantId: number; code: string } | null>(null)

const activeGrant = computed(() => session.supportGrant)
const canRequest = computed(
  () => form.value.bandId > 0 && form.value.reason.trim().length >= 5 && !busy.value,
)

onMounted(load)

async function load() {
  loading.value = true
  try {
    const [bandList, grantList] = await Promise.all([platformApi.bands(), platformApi.grants()])
    bands.value = bandList.bands
    grants.value = grantList.grants
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

async function request() {
  if (!canRequest.value) return
  busy.value = true
  try {
    await platformApi.requestAccess({
      band_id: form.value.bandId,
      reason: form.value.reason.trim(),
      scope: form.value.scope,
      duration_seconds: form.value.durationMinutes * 60,
    })
    flash.success(t('platform.support.requested'))
    form.value.reason = ''
    await load()
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

async function activate() {
  if (!activating.value || busy.value) return
  busy.value = true
  try {
    await platformApi.activateAccess(activating.value.grantId, activating.value.code)
    flash.success(t('platform.support.activated'))
    activating.value = null
    // The session now carries the grant, so the identity has to be refreshed
    // for the banner and the band scope to appear.
    await session.restore()
    await load()
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

async function revoke(grant: SupportGrant) {
  try {
    await platformApi.revokeAccess(grant.id)
    flash.success(t('platform.support.revoked'))
    await session.restore()
    await load()
  } catch (error) {
    report(error)
  }
}

function bandName(id: number): string {
  return bands.value.find((band) => band.id === id)?.name ?? `#${id}`
}

function formatDate(value: string | null): string {
  return value ? d(new Date(value), 'short') : '—'
}

function statusClass(status: string): string {
  if (status === 'active') return 'warning'
  if (status === 'approved') return 'success'
  if (status === 'denied' || status === 'revoked') return 'danger'
  return ''
}
</script>

<template>
  <main class="page-shell">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('platform.eyebrow') }}</p>
        <h1>{{ t('platform.support.title') }}</h1>
        <p class="page-intro">{{ t('platform.support.intro') }}</p>
      </div>
    </div>

    <section v-if="activeGrant" class="table-section">
      <div class="section-heading">
        <div>
          <h2>{{ t('platform.support.currentlyActive') }}</h2>
          <p>{{ t('platform.support.currentlyActiveHint', { band: bandName(0) }) }}</p>
        </div>
        <div class="active-grant-actions">
          <!-- The way in belongs here, not only as a text link in the header:
               an open grant is useless if nobody finds the door it opens. -->
          <RouterLink class="primary-button" :to="{ name: 'sales' }">
            {{ t('platform.toGrantedBand', { band: session.band?.name ?? '' }) }}
          </RouterLink>
          <button
            class="danger-button"
            type="button"
            @click="revoke({ id: activeGrant.id } as SupportGrant)"
          >{{ t('platform.support.end') }}</button>
        </div>
      </div>
    </section>

    <section class="table-section">
      <div class="section-heading">
        <div>
          <h2>{{ t('platform.support.request') }}</h2>
          <p>{{ t('platform.support.requestHint') }}</p>
        </div>
      </div>
      <form class="stack-form" @submit.prevent="request">
        <div class="field-grid two-columns">
          <label>
            {{ t('platform.support.band') }}
            <select v-model.number="form.bandId">
              <option :value="0">{{ t('platform.support.pickBand') }}</option>
              <option v-for="band in bands" :key="band.id" :value="band.id">{{ band.name }}</option>
            </select>
          </label>
          <label>
            {{ t('platform.support.scope') }}
            <select v-model="form.scope">
              <option value="read_only">{{ t('support.readOnly') }}</option>
              <option value="read_write">{{ t('support.readWrite') }}</option>
            </select>
          </label>
        </div>
        <label>
          {{ t('platform.support.reason') }}
          <textarea v-model="form.reason" rows="2" :placeholder="t('platform.support.reasonHint')" />
        </label>
        <label>
          {{ t('platform.support.duration') }}
          <select v-model.number="form.durationMinutes">
            <option :value="15">15 min</option>
            <option :value="60">60 min</option>
            <option :value="240">4 h</option>
            <option :value="1440">24 h</option>
          </select>
        </label>
        <button class="primary-button" type="submit" :disabled="!canRequest">
          {{ t('platform.support.send') }}
        </button>
      </form>
    </section>

    <section class="table-section">
      <div class="section-heading">
        <div><h2>{{ t('platform.support.history') }}</h2></div>
      </div>
      <p v-if="loading" class="muted">{{ t('common.loading') }}</p>
      <p v-else-if="!grants.length" class="muted">{{ t('platform.support.empty') }}</p>
      <div v-else class="table-scroll">
        <table>
          <thead>
            <tr>
              <th>{{ t('platform.support.band') }}</th>
              <th>{{ t('platform.support.reason') }}</th>
              <th>{{ t('platform.support.scope') }}</th>
              <th>{{ t('platform.bands.status') }}</th>
              <th>{{ t('platform.support.decidedBy') }}</th>
              <th>{{ t('platform.support.expires') }}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="grant in grants" :key="grant.id">
              <td>{{ bandName(grant.band_id) }}</td>
              <td>{{ grant.reason }}</td>
              <td>{{ grant.scope === 'read_only' ? t('support.readOnly') : t('support.readWrite') }}</td>
              <td>
                <span class="status" :class="statusClass(grant.status)">
                  {{ t(`platform.support.status.${grant.status}`) }}
                </span>
              </td>
              <td>{{ grant.decided_by_username || '—' }}</td>
              <td>{{ formatDate(grant.expires_at) }}</td>
              <td>
                <button
                  v-if="grant.status === 'approved'"
                  class="compact-button"
                  type="button"
                  @click="activating = { grantId: grant.id, code: '' }"
                >{{ t('platform.support.start') }}</button>
                <button
                  v-else-if="grant.status === 'active'"
                  class="compact-button danger-button"
                  type="button"
                  @click="revoke(grant)"
                >{{ t('platform.support.end') }}</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- Activation needs a fresh code, not just the earlier approval. -->
    <dialog v-if="activating" class="confirmation-dialog" open>
      <form class="stack-form" @submit.prevent="activate">
        <div>
          <p class="eyebrow">{{ t('platform.support.startEyebrow') }}</p>
          <h2>{{ t('platform.support.startTitle') }}</h2>
          <p>{{ t('platform.support.startIntro') }}</p>
        </div>
        <label>
          {{ t('auth.code') }}
          <input v-model="activating.code" inputmode="numeric" autocomplete="one-time-code" required />
        </label>
        <div class="dialog-actions">
          <button class="secondary-button" type="button" @click="activating = null">
            {{ t('common.cancel') }}
          </button>
          <button class="primary-button" type="submit" :disabled="busy">
            {{ t('platform.support.start') }}
          </button>
        </div>
      </form>
    </dialog>
  </main>
</template>

<style scoped>
.active-grant-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  align-items: center;
}

.active-grant-actions .primary-button {
  display: inline-flex;
  align-items: center;
  text-decoration: none;
}
</style>
