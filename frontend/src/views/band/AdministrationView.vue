<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { bandAdminApi, bandUsersApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { BandUser, PaymentQRSettings, Role, SupportGrant } from '@/api/types'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'

/**
 * Band administration.
 *
 * The support-access decisions live here because they are the band's, not the
 * platform's. Approving hands an outsider a key to the band's books, so it
 * needs the same fresh password confirmation as deleting an account.
 */
const { t, d } = useI18n()
const flash = useFlashStore()
const session = useSessionStore()

const grants = ref<SupportGrant[]>([])
const loading = ref(true)
const busy = ref(false)

/** The step-up confirmation an approval requires. */
const confirming = ref<{ grantId: number; password: string; code: string } | null>(null)

/**
 * Where the band's payment codes send the money. Nothing here is secret — an
 * IBAN is printed on every invoice anyway — but only a band admin may change
 * who gets paid.
 */
const paymentQr = ref<PaymentQRSettings | null>(null)
const paymentQrSaving = ref(false)
const paypalMeUsername = computed({
  get: () => {
    const value = paymentQr.value?.paypal_me_url.trim() ?? ''
    return value.replace(/^https:\/\/paypal\.me\//i, '')
  },
  set: (value: string) => {
    if (!paymentQr.value) return
    const username = value.trim().replace(/^https:\/\/paypal\.me\//i, '').replace(/^\/+/, '')
    paymentQr.value.paypal_me_url = username ? `https://paypal.me/${username}` : ''
  },
})

/** Account management. */
const users = ref<BandUser[]>([])
const assignableRoles = ref<Role[]>([])
const newUser = ref({ username: '', role: 'seller' as Role })
/** A setup code is shown exactly once, right after it is issued. */
const issuedCode = ref<{ username: string; code: string } | null>(null)
/** The step-up window every account change needs. */
const reauthPrompt = ref<{ password: string; code: string; then: () => Promise<void> } | null>(null)
const reauthedUntil = ref(0)

const pending = computed(() => grants.value.filter((grant) => grant.status === 'pending'))
const active = computed(() => grants.value.filter((grant) => grant.status === 'active'))
const past = computed(() =>
  grants.value.filter((grant) => !['pending', 'active'].includes(grant.status)),
)
const needsCode = computed(() => session.capabilities?.sensitive_action_mfa_required ?? false)

onMounted(load)

async function load() {
  loading.value = true
  try {
    const [grantList, userList] = await Promise.all([
      bandAdminApi.grants(),
      bandUsersApi.list(),
    ])
    grants.value = grantList.grants
    users.value = userList.users
    assignableRoles.value = userList.assignable_roles
    paymentQr.value = session.featureFlags?.payment_qr === false
      ? null
      : await bandUsersApi.paymentQrSettings()
  } catch {
    flash.error(t('errors.generic'))
  } finally {
    loading.value = false
  }
}

/**
 * Runs an account action, asking for a password confirmation first when the
 * step-up window has lapsed. Every change here is sensitive enough to warrant
 * it — the server enforces the same rule.
 */
async function guarded(action: () => Promise<void>) {
  if (Date.now() < reauthedUntil.value) {
    await run(action)
    return
  }
  reauthPrompt.value = { password: '', code: '', then: action }
}

async function run(action: () => Promise<void>) {
  try {
    await action()
    await load()
  } catch (error) {
    report(error)
  }
}

async function confirmReauth() {
  if (!reauthPrompt.value || busy.value) return
  busy.value = true
  try {
    const result = await bandAdminApi.reauth(reauthPrompt.value.password, reauthPrompt.value.code)
    reauthedUntil.value = Date.now() + result.valid_for_seconds * 1000
    const action = reauthPrompt.value.then
    reauthPrompt.value = null
    await run(action)
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

async function createUser() {
  const username = newUser.value.username.trim()
  if (!username) return
  await guarded(async () => {
    const created = await bandUsersApi.create(username, newUser.value.role)
    issuedCode.value = { username: created.username, code: created.setup_code }
    newUser.value.username = ''
  })
}

async function resetPassword(user: BandUser) {
  await guarded(async () => {
    const result = await bandUsersApi.resetPassword(user.id)
    issuedCode.value = { username: result.username, code: result.setup_code }
  })
}

async function savePaymentQr() {
  if (!paymentQr.value || paymentQrSaving.value) return
  paymentQrSaving.value = true
  try {
    paymentQr.value = await bandUsersApi.savePaymentQrSettings(paymentQr.value)
    flash.success(t('administration.paymentQr.saved'))
  } catch (error) {
    report(error)
  } finally {
    paymentQrSaving.value = false
  }
}

function report(error: unknown) {
  flash.error(
    error instanceof ApiError
      ? t(`errors.${error.detailCode ?? 'generic'}`, error.message)
      : t('errors.network'),
  )
}

async function approve() {
  if (!confirming.value || busy.value) return
  busy.value = true
  try {
    // The confirmation and the approval are two calls: the window it opens is
    // the same one every other sensitive action uses.
    await bandAdminApi.reauth(confirming.value.password, confirming.value.code)
    await bandAdminApi.approve(confirming.value.grantId)
    flash.success(t('administration.support.approved'))
    confirming.value = null
    await load()
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

async function deny(grant: SupportGrant) {
  try {
    await bandAdminApi.deny(grant.id)
    flash.success(t('administration.support.denied'))
    await load()
  } catch (error) {
    report(error)
  }
}

async function revoke(grant: SupportGrant) {
  try {
    await bandAdminApi.revoke(grant.id)
    flash.success(t('administration.support.revoked'))
    await load()
  } catch (error) {
    report(error)
  }
}

function formatDate(value: string | null): string {
  return value ? d(new Date(value), 'short') : '—'
}

function durationLabel(seconds: number): string {
  if (seconds >= 3600) return `${Math.round(seconds / 3600)} h`
  return `${Math.round(seconds / 60)} min`
}
</script>

<template>
  <main class="page-shell">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('administration.eyebrow') }}</p>
        <h1>{{ t('administration.title') }}</h1>
      </div>
    </div>

    <section v-if="paymentQr" class="table-section admin-payment-qr-settings">
      <div class="section-heading">
        <div>
          <h2>{{ t('administration.paymentQr.title') }}</h2>
          <p>{{ t('administration.paymentQr.hint') }}</p>
        </div>
      </div>
      <form class="stack-form" @submit.prevent="savePaymentQr">
        <label>
          {{ t('administration.paymentQr.paypal') }}
          <span class="paypal-me-input">
            <span aria-hidden="true">https://paypal.me/</span>
            <input
              v-model="paypalMeUsername"
              type="text"
              inputmode="text"
              autocomplete="off"
              pattern="[A-Za-z0-9._-]+"
              placeholder="deinname"
            />
          </span>
          <small>{{ t('administration.paymentQr.paypalHint') }}</small>
        </label>
        <div class="field-grid two-columns">
          <label>
            {{ t('administration.paymentQr.holder') }}
            <input v-model="paymentQr.bank_account_holder" />
          </label>
          <label>
            {{ t('administration.paymentQr.iban') }}
            <input v-model="paymentQr.bank_iban" autocomplete="off" />
          </label>
        </div>
        <label>
          {{ t('administration.paymentQr.bic') }}
          <input v-model="paymentQr.bank_bic" autocomplete="off" />
        </label>
        <p class="muted">{{ t('administration.paymentQr.remittanceHint') }}</p>
        <button class="primary-button" type="submit" :disabled="paymentQrSaving">
          {{ t('common.save') }}
        </button>
      </form>
    </section>

    <section class="table-section">
      <div class="section-heading">
        <div>
          <h2>{{ t('administration.support.title') }}</h2>
          <p>{{ t('administration.support.intro') }}</p>
        </div>
      </div>

      <p v-if="loading" class="muted">{{ t('common.loading') }}</p>

      <template v-else>
        <div v-if="pending.length" class="grant-list">
          <article v-for="grant in pending" :key="grant.id" class="grant-card pending">
            <div>
              <p class="eyebrow">{{ t('administration.support.pending') }}</p>
              <h3>{{ grant.requested_by_username }}</h3>
              <p class="grant-reason">{{ grant.reason }}</p>
              <p class="muted">
                {{ grant.scope === 'read_only' ? t('support.readOnly') : t('support.readWrite') }}
                · {{ durationLabel(grant.requested_duration_seconds) }}
                · {{ formatDate(grant.created_at) }}
              </p>
            </div>
            <div class="grant-actions">
              <button
                class="primary-button"
                type="button"
                @click="confirming = { grantId: grant.id, password: '', code: '' }"
              >{{ t('administration.support.approve') }}</button>
              <button class="secondary-button" type="button" @click="deny(grant)">
                {{ t('administration.support.deny') }}
              </button>
            </div>
          </article>
        </div>
        <p v-else class="muted">{{ t('administration.support.noPending') }}</p>

        <div v-if="active.length" class="grant-list">
          <article v-for="grant in active" :key="grant.id" class="grant-card active">
            <div>
              <p class="eyebrow">{{ t('administration.support.active') }}</p>
              <h3>{{ grant.requested_by_username }}</h3>
              <p class="grant-reason">{{ grant.reason }}</p>
              <p class="muted">{{ t('administration.support.until', { time: formatDate(grant.expires_at) }) }}</p>
            </div>
            <div class="grant-actions">
              <button class="danger-button" type="button" @click="revoke(grant)">
                {{ t('administration.support.revoke') }}
              </button>
            </div>
          </article>
        </div>

        <div v-if="past.length" class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{{ t('common.date') }}</th>
                <th>{{ t('platform.support.request') }}</th>
                <th>{{ t('platform.support.reason') }}</th>
                <th>{{ t('platform.bands.status') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="grant in past" :key="grant.id">
                <td>{{ formatDate(grant.created_at) }}</td>
                <td>{{ grant.requested_by_username }}</td>
                <td>{{ grant.reason }}</td>
                <td>{{ t(`platform.support.status.${grant.status}`) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </section>

    <section class="table-section">
      <div class="section-heading">
        <div>
          <h2>{{ t('administration.users.title') }}</h2>
          <p>{{ t('administration.users.intro') }}</p>
        </div>
      </div>

      <!-- The code is shown once and never retrievable, so it stays on screen
           until the admin dismisses it. -->
      <div v-if="issuedCode" class="setup-code-card">
        <p class="eyebrow">{{ t('administration.users.codeFor', { user: issuedCode.username }) }}</p>
        <code>{{ issuedCode.code }}</code>
        <p class="muted">{{ t('administration.users.codeHint') }}</p>
        <button class="secondary-button" type="button" @click="issuedCode = null">
          {{ t('common.close') }}
        </button>
      </div>

      <form class="stack-form" @submit.prevent="createUser">
        <div class="field-grid two-columns">
          <label>
            {{ t('auth.username') }}
            <input v-model="newUser.username" required />
          </label>
          <label>
            {{ t('administration.users.role') }}
            <select v-model="newUser.role">
              <option v-for="role in assignableRoles" :key="role" :value="role">
                {{ t(`administration.users.roles.${role}`) }}
              </option>
            </select>
          </label>
        </div>
        <button class="primary-button" type="submit" :disabled="busy">
          {{ t('administration.users.create') }}
        </button>
      </form>

      <div v-if="users.length" class="table-scroll">
        <table>
          <thead>
            <tr>
              <th>{{ t('auth.username') }}</th>
              <th>{{ t('administration.users.role') }}</th>
              <th>{{ t('administration.users.state') }}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="user in users" :key="user.id" :class="{ 'cancelled-row': !user.is_active }">
              <td>
                <strong>{{ user.username }}</strong>
                <small v-if="user.is_self">{{ t('administration.users.you') }}</small>
              </td>
              <td>
                <select
                  class="status-select"
                  :value="user.role"
                  :disabled="user.is_self"
                  @change="guarded(() => bandUsersApi.changeRole(user.id, ($event.target as HTMLSelectElement).value as Role))"
                >
                  <option v-for="role in assignableRoles" :key="role" :value="role">
                    {{ t(`administration.users.roles.${role}`) }}
                  </option>
                </select>
              </td>
              <td>
                <span v-if="user.must_set_password" class="status warning">
                  {{ t('administration.users.awaitingSetup') }}
                </span>
                <span v-else-if="!user.is_active" class="status danger">
                  {{ t('administration.users.inactive') }}
                </span>
                <span v-else class="status success">{{ t('administration.users.active') }}</span>
                <span v-if="user.mfa_enabled" class="status">2FA</span>
              </td>
              <td class="user-actions">
                <button class="compact-button" type="button" @click="resetPassword(user)">
                  {{ t('administration.users.resetPassword') }}
                </button>
                <button
                  v-if="user.mfa_enabled"
                  class="compact-button"
                  type="button"
                  @click="guarded(() => bandUsersApi.resetMfa(user.id))"
                >{{ t('administration.users.resetMfa') }}</button>
                <button
                  v-if="!user.is_self"
                  class="compact-button"
                  type="button"
                  @click="guarded(() => bandUsersApi.setActive(user.id, !user.is_active))"
                >
                  {{ user.is_active ? t('administration.users.deactivate') : t('administration.users.activate') }}
                </button>
                <button
                  v-if="!user.is_self"
                  class="compact-button danger-button"
                  type="button"
                  @click="guarded(() => bandUsersApi.remove(user.id))"
                >{{ t('common.delete') }}</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="muted">{{ t('administration.users.deleteHint') }}</p>
    </section>

    <dialog v-if="reauthPrompt" class="confirmation-dialog" open>
      <form class="stack-form" @submit.prevent="confirmReauth">
        <div>
          <p class="eyebrow">{{ t('administration.users.confirmEyebrow') }}</p>
          <h2>{{ t('administration.users.confirmTitle') }}</h2>
          <p>{{ t('administration.users.confirmIntro') }}</p>
        </div>
        <label>
          {{ t('administration.support.password') }}
          <input v-model="reauthPrompt.password" type="password" autocomplete="current-password" required />
        </label>
        <label v-if="needsCode">
          {{ t('auth.code') }}
          <input v-model="reauthPrompt.code" inputmode="numeric" autocomplete="one-time-code" />
        </label>
        <div class="dialog-actions">
          <button class="secondary-button" type="button" @click="reauthPrompt = null">
            {{ t('common.cancel') }}
          </button>
          <button class="primary-button" type="submit" :disabled="busy">{{ t('common.confirm') }}</button>
        </div>
      </form>
    </dialog>

    <dialog v-if="confirming" class="confirmation-dialog" open>
      <form class="stack-form" @submit.prevent="approve">
        <div>
          <p class="eyebrow">{{ t('administration.support.confirmEyebrow') }}</p>
          <h2>{{ t('administration.support.confirmTitle') }}</h2>
          <p>{{ t('administration.support.confirmIntro') }}</p>
        </div>
        <label>
          {{ t('auth.username') }}
          <input :value="session.user?.username" disabled />
        </label>
        <label>
          {{ t('administration.support.password') }}
          <input v-model="confirming.password" type="password" autocomplete="current-password" required />
        </label>
        <label v-if="needsCode">
          {{ t('auth.code') }}
          <input v-model="confirming.code" inputmode="numeric" autocomplete="one-time-code" />
        </label>
        <div class="dialog-actions">
          <button class="secondary-button" type="button" @click="confirming = null">
            {{ t('common.cancel') }}
          </button>
          <button class="primary-button" type="submit" :disabled="busy">
            {{ t('administration.support.approve') }}
          </button>
        </div>
      </form>
    </dialog>
  </main>
</template>

<style scoped>
.setup-code-card {
  display: grid;
  gap: 8px;
  justify-items: start;
  margin-bottom: 18px;
  padding: 16px;
  border: 1px solid var(--accent);
  border-radius: var(--radius);
  background: var(--panel-raised);
}

.setup-code-card code {
  padding: 10px 14px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--input-bg);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 1.2rem;
  letter-spacing: 0.08em;
}

.user-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

td small {
  display: block;
  color: var(--muted);
}

.grant-list {
  display: grid;
  gap: 12px;
  margin-bottom: 18px;
}

.grant-card {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 18px;
  padding: 16px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--panel-raised);
}

.grant-card.pending {
  border-color: var(--accent);
}

.grant-card.active {
  border-color: var(--warning);
}

.grant-card h3 {
  margin: 2px 0 6px;
}

.grant-reason {
  margin: 0 0 6px;
}

.grant-actions {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
</style>
