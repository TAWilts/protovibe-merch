<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { ApiError } from '@/api/client'
import { platformApi } from '@/api/endpoints'
import type { BandRegistrationRequest, BandRegistrationStatus } from '@/api/types'
import { useFlashStore } from '@/stores/flash'

type Draft = {
  band_name: string
  band_slug: string
  admin_username: string
  contact_email: string
  rejection_note: string
}

const { t, d } = useI18n()
const flash = useFlashStore()
const requests = ref<BandRegistrationRequest[]>([])
const filter = ref<BandRegistrationStatus | ''>('pending')
const loading = ref(true)
const busyID = ref<number | null>(null)
const drafts = reactive<Record<number, Draft>>({})

onMounted(load)

function draftFor(request: BandRegistrationRequest): Draft {
  if (!drafts[request.id]) {
    drafts[request.id] = {
      band_name: request.band_name,
      band_slug: request.band_slug,
      admin_username: request.admin_username,
      contact_email: request.contact_email,
      rejection_note: '',
    }
  }
  return drafts[request.id]
}

async function load() {
  loading.value = true
  try {
    requests.value = (await platformApi.registrationRequests(filter.value)).requests
    for (const request of requests.value) draftFor(request)
  } catch (error) {
    report(error)
  } finally {
    loading.value = false
  }
}

function report(error: unknown) {
  flash.error(error instanceof ApiError
    ? t(`errors.${error.detailCode ?? 'generic'}`, error.message)
    : t('errors.network'))
}

async function approve(request: BandRegistrationRequest) {
  if (busyID.value !== null) return
  if (!window.confirm(t('platform.registrations.approveConfirm', { reference: request.reference }))) return
  busyID.value = request.id
  try {
    const draft = draftFor(request)
    await platformApi.approveRegistration(request.id, {
      band_name: draft.band_name.trim(),
      band_slug: draft.band_slug.trim().toLowerCase(),
      admin_username: draft.admin_username.trim(),
      contact_email: draft.contact_email.trim(),
    })
    flash.success(t('platform.registrations.approved'))
    await load()
  } catch (error) {
    report(error)
  } finally {
    busyID.value = null
  }
}

async function reject(request: BandRegistrationRequest) {
  if (busyID.value !== null) return
  if (!window.confirm(t('platform.registrations.rejectConfirm', { reference: request.reference }))) return
  busyID.value = request.id
  try {
    await platformApi.rejectRegistration(request.id, draftFor(request).rejection_note.trim())
    flash.success(t('platform.registrations.rejected'))
    await load()
  } catch (error) {
    report(error)
  } finally {
    busyID.value = null
  }
}
</script>

<template>
  <main class="page-shell">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('platform.eyebrow') }}</p>
        <h1>{{ t('platform.registrations.title') }}</h1>
        <p class="page-intro">{{ t('platform.registrations.intro') }}</p>
      </div>
      <label class="table-filter">
        {{ t('platform.registrations.filter') }}
        <select v-model="filter" @change="load">
          <option value="">{{ t('platform.registrations.all') }}</option>
          <option value="pending">{{ t('platform.registrations.status.pending') }}</option>
          <option value="approved">{{ t('platform.registrations.status.approved') }}</option>
          <option value="rejected">{{ t('platform.registrations.status.rejected') }}</option>
          <option value="expired">{{ t('platform.registrations.status.expired') }}</option>
        </select>
      </label>
    </div>

    <p v-if="loading" class="muted">{{ t('common.loading') }}</p>
    <section v-else-if="!requests.length" class="table-section empty-inbox">
      <strong>{{ t('platform.registrations.empty') }}</strong>
    </section>
    <div v-else class="registration-inbox">
      <article v-for="request in requests" :key="request.id" class="registration-request">
        <header>
          <div>
            <p class="eyebrow">{{ request.reference }}</p>
            <h2>{{ request.requested_band_name }}</h2>
          </div>
          <span class="status" :class="{
            success: request.status === 'approved',
            warning: request.status === 'pending',
            danger: request.status === 'rejected' || request.status === 'expired',
          }">{{ t(`platform.registrations.status.${request.status}`) }}</span>
        </header>

        <div class="request-meta">
          <span>{{ t('platform.registrations.received') }} <strong>{{ d(new Date(request.created_at), 'short') }}</strong></span>
          <span>{{ t('platform.registrations.expires') }} <strong>{{ d(new Date(request.expires_at), 'short') }}</strong></span>
          <span>{{ t('platform.registrations.consent') }} <strong>{{ d(new Date(request.privacy_accepted_at), 'short') }}</strong></span>
        </div>

        <form v-if="request.status === 'pending'" class="stack-form" @submit.prevent="approve(request)">
          <div class="field-grid two-columns">
            <label>{{ t('platform.registrations.bandName') }}<input v-model="draftFor(request).band_name" maxlength="200" required /></label>
            <label>{{ t('platform.registrations.bandSlug') }}<input v-model="draftFor(request).band_slug" maxlength="64" required /></label>
            <label>{{ t('platform.registrations.adminUsername') }}<input v-model="draftFor(request).admin_username" minlength="3" maxlength="150" required /></label>
            <label>{{ t('platform.registrations.contactEmail') }}<input v-model="draftFor(request).contact_email" type="email" maxlength="254" required /></label>
          </div>
          <details class="requested-values">
            <summary>{{ t('platform.registrations.originalValues') }}</summary>
            <dl>
              <div><dt>{{ t('platform.registrations.bandName') }}</dt><dd>{{ request.requested_band_name }}</dd></div>
              <div><dt>{{ t('platform.registrations.bandSlug') }}</dt><dd><code>{{ request.requested_band_slug }}</code></dd></div>
              <div><dt>{{ t('platform.registrations.adminUsername') }}</dt><dd><code>{{ request.requested_admin_username }}</code></dd></div>
              <div><dt>{{ t('platform.registrations.contactEmail') }}</dt><dd>{{ request.requested_contact_email }}</dd></div>
            </dl>
          </details>
          <label>{{ t('platform.registrations.rejectionNote') }}<textarea v-model="draftFor(request).rejection_note" maxlength="1000" :placeholder="t('platform.registrations.rejectionNoteHint')"></textarea></label>
          <div class="request-actions">
            <button class="secondary-button danger-button" type="button" :disabled="busyID !== null" @click="reject(request)">
              {{ t('platform.registrations.reject') }}
            </button>
            <button class="primary-button" type="submit" :disabled="busyID !== null">
              {{ t('platform.registrations.approve') }}
            </button>
          </div>
        </form>

        <div v-else class="decision-summary">
          <dl>
            <div><dt>{{ t('platform.registrations.bandName') }}</dt><dd>{{ request.band_name }}</dd></div>
            <div><dt>{{ t('platform.registrations.bandSlug') }}</dt><dd><code>{{ request.band_slug }}</code></dd></div>
            <div><dt>{{ t('platform.registrations.adminUsername') }}</dt><dd><code>{{ request.admin_username }}</code></dd></div>
            <div><dt>{{ t('platform.registrations.decidedBy') }}</dt><dd>{{ request.decided_by_username || '—' }}</dd></div>
          </dl>
          <p v-if="request.decision_note" class="notice">{{ request.decision_note }}</p>
          <p v-if="request.claimed_at" class="muted">{{ t('platform.registrations.claimedAt', { date: d(new Date(request.claimed_at), 'short') }) }}</p>
        </div>
      </article>
    </div>
  </main>
</template>

<style scoped>
.registration-inbox {
  display: grid;
  gap: 16px;
}

.registration-request {
  padding: 22px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: rgba(26, 22, 34, .9);
  box-shadow: var(--shadow);
}

.registration-request > header {
  display: flex;
  align-items: start;
  justify-content: space-between;
  gap: 20px;
  margin-bottom: 14px;
}

.registration-request h2 {
  margin: 3px 0 0;
  font-size: 1.25rem;
}

.request-meta {
  margin-bottom: 20px;
  padding: 10px 0;
  display: flex;
  flex-wrap: wrap;
  gap: 9px 22px;
  border-top: 1px solid rgba(255,255,255,.07);
  border-bottom: 1px solid rgba(255,255,255,.07);
  color: var(--muted);
  font-size: .74rem;
}

.request-meta strong { color: var(--text); }
.request-actions { display: flex; justify-content: flex-end; gap: 9px; }
.requested-values { border: 1px solid var(--border); border-radius: 9px; background: rgba(255,255,255,.025); }
.requested-values summary { padding: 10px 12px; color: var(--muted); cursor: pointer; font-size: .78rem; font-weight: 720; }
.requested-values dl, .decision-summary dl { margin: 0; padding: 0 12px 12px; display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px 18px; }
.requested-values dl > div, .decision-summary dl > div { display: grid; gap: 2px; }
.requested-values dt, .decision-summary dt { color: var(--muted); font-size: .7rem; font-weight: 700; }
.requested-values dd, .decision-summary dd { margin: 0; overflow-wrap: anywhere; font-size: .84rem; }
.decision-summary { display: grid; gap: 12px; }
.decision-summary dl { padding: 0; }
.empty-inbox { min-height: 180px; display: grid; place-items: center; color: var(--muted); }

@media (max-width: 650px) {
  .registration-request { padding: 16px; }
  .field-grid.two-columns, .requested-values dl, .decision-summary dl { grid-template-columns: 1fr; }
  .request-actions { flex-direction: column-reverse; }
  .request-actions button { width: 100%; }
}
</style>
