<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { profileApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { ProfilePayload } from '@/api/types'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'

/**
 * The personal account page.
 *
 * It is behind a step-up confirmation, as in the original: a laptop left open
 * at a merch table must not let a passer-by change the owner's password or
 * turn off their second factor.
 */
const { t } = useI18n()
const flash = useFlashStore()
const session = useSessionStore()
const router = useRouter()

const data = ref<ProfilePayload | null>(null)
const loading = ref(true)
const busy = ref(false)
/** Shown until the step-up window is open. */
const needsReauth = ref(false)
const reauth = ref({ password: '', code: '' })

const passwords = ref({ current: '', next: '' })
const username = ref('')
const contactEmail = ref('')
const enrollment = ref<{ secret: string; uri: string; qr: string } | null>(null)
const enrollmentCode = ref('')
const recoveryCodes = ref<string[]>([])
const disableCode = ref('')

const caps = computed(() => session.capabilities)
const needsCode = computed(() => caps.value?.sensitive_action_mfa_required ?? false)

onMounted(load)

async function load() {
  loading.value = true
  try {
    data.value = await profileApi.get()
    username.value = data.value.profile.user.username
    contactEmail.value = data.value.profile.user.contact_email
    needsReauth.value = false
  } catch (error) {
    if (error instanceof ApiError && error.detailCode === 'reauth_required') {
      needsReauth.value = true
    } else {
      flash.error(t('errors.generic'))
    }
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

async function confirmReauth() {
  busy.value = true
  try {
    await profileApi.reauth(reauth.value.password, reauth.value.code)
    reauth.value = { password: '', code: '' }
    await load()
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

async function saveTelemetry(enabled: boolean) {
  try {
    await profileApi.telemetry(enabled)
    if (data.value) {
      data.value.profile.user.telemetry_enabled = enabled
      data.value.profile.user.telemetry_decided = true
    }
    await session.restore(false)
    flash.success(t('profile.saved'))
  } catch (error) {
    report(error)
  }
}

async function savePersonalization(payload: Record<string, unknown>) {
  try {
    await profileApi.personalization(payload)
    // Applied immediately so the change is visible where it was made.
    if (typeof payload.ui_theme === 'string') {
      document.documentElement.dataset.theme = payload.ui_theme
    }
    await session.restore()
    flash.success(t('profile.saved'))
  } catch (error) {
    report(error)
  }
}

async function changePassword() {
  busy.value = true
  try {
    await profileApi.changePassword(passwords.value.current, passwords.value.next)
    // A password change signs the account out everywhere, which is what makes
    // it a real answer to a suspected leak.
    flash.success(t('profile.passwordChanged'))
    session.adopt(null)
    router.push({ name: 'login' })
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

async function changeUsername() {
  try {
    await profileApi.changeUsername(username.value.trim())
    await session.restore()
    flash.success(t('profile.saved'))
  } catch (error) {
    report(error)
  }
}

async function changeContactEmail() {
  try {
    await profileApi.changeContactEmail(contactEmail.value.trim())
    await session.restore()
    flash.success(t('profile.saved'))
  } catch (error) {
    report(error)
  }
}

async function startEnrollment() {
  try {
    const started = await profileApi.startMfa()
    enrollment.value = { secret: started.secret, uri: started.otpauth_uri, qr: started.otpauth_qr }
  } catch (error) {
    report(error)
  }
}

async function confirmEnrollment() {
  busy.value = true
  try {
    const result = await profileApi.confirmMfa(enrollmentCode.value)
    recoveryCodes.value = result.recovery_codes
    enrollment.value = null
    enrollmentCode.value = ''
    await session.restore()
    await load()
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

async function disableMfa() {
  try {
    await profileApi.disableMfa(disableCode.value)
    disableCode.value = ''
    await session.restore()
    await load()
    flash.success(t('profile.mfaDisabled'))
  } catch (error) {
    report(error)
  }
}

async function regenerateCodes() {
  try {
    recoveryCodes.value = (await profileApi.regenerateRecoveryCodes()).recovery_codes
    await load()
  } catch (error) {
    report(error)
  }
}
</script>

<template>
  <main class="page-shell">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('profile.eyebrow') }}</p>
        <h1>{{ t('profile.title') }}</h1>
      </div>
    </div>

    <section v-if="needsReauth" class="table-section">
      <div class="section-heading">
        <div>
          <h2>{{ t('profile.confirmTitle') }}</h2>
          <p>{{ t('profile.confirmIntro') }}</p>
        </div>
      </div>
      <form class="stack-form" @submit.prevent="confirmReauth">
        <label>
          {{ t('administration.support.password') }}
          <input v-model="reauth.password" type="password" autocomplete="current-password" required />
        </label>
        <label v-if="needsCode">
          {{ t('auth.code') }}
          <input v-model="reauth.code" inputmode="numeric" autocomplete="one-time-code" />
        </label>
        <button class="primary-button" type="submit" :disabled="busy">{{ t('common.confirm') }}</button>
      </form>
    </section>

    <p v-else-if="loading" class="muted">{{ t('common.loading') }}</p>

    <template v-else-if="data">
      <!-- Recovery codes are shown exactly once. -->
      <section v-if="recoveryCodes.length" class="table-section">
        <div class="section-heading">
          <div>
            <h2>{{ t('auth.recoveryTitle') }}</h2>
            <p>{{ t('auth.recoveryIntro') }}</p>
          </div>
        </div>
        <ul class="recovery-code-list">
          <li v-for="entry in recoveryCodes" :key="entry"><code>{{ entry }}</code></li>
        </ul>
        <button class="secondary-button" type="button" @click="recoveryCodes = []">
          {{ t('auth.recoverySaved') }}
        </button>
      </section>

      <section class="table-section">
        <div class="section-heading">
          <div><h2>{{ t('profile.account') }}</h2></div>
        </div>
        <dl class="account-facts">
          <dt>{{ t('profile.role') }}</dt><dd>{{ caps?.role_label }}</dd>
          <dt>{{ t('profile.band') }}</dt><dd>{{ session.band?.name ?? '—' }}</dd>
          <dt>{{ t('profile.lastLogin') }}</dt>
          <dd>{{ data.last_login_at ? new Date(data.last_login_at).toLocaleString() : '—' }}</dd>
        </dl>
        <form class="stack-form" @submit.prevent="changeUsername">
          <label>{{ t('auth.username') }}<input v-model="username" required /></label>
          <button class="secondary-button" type="submit">{{ t('common.save') }}</button>
        </form>
        <form v-if="caps?.is_platform_staff" class="stack-form" @submit.prevent="changeContactEmail">
          <label>{{ t('profile.contactEmail') }}<input v-model="contactEmail" type="email" required /></label>
          <p class="muted">{{ t('profile.contactEmailHint') }}</p>
          <button class="secondary-button" type="submit">{{ t('common.save') }}</button>
        </form>
      </section>

      <section class="table-section">
        <div class="section-heading">
          <div>
            <h2>{{ t('profile.personalization') }}</h2>
            <p>{{ t('profile.personalizationHint') }}</p>
          </div>
        </div>
        <div class="field-grid">
          <label>
            {{ t('profile.theme') }}
            <select
              :value="data.profile.user.ui_theme"
              @change="savePersonalization({ ui_theme: ($event.target as HTMLSelectElement).value })"
            >
              <option v-for="theme in data.available_themes" :key="theme" :value="theme">
                {{ t(`profile.themes.${theme}`) }}
              </option>
            </select>
          </label>
        </div>
        <label class="checkbox-row">
          <input
            type="checkbox"
            :checked="data.profile.user.show_variant_photos"
            @change="savePersonalization({ show_variant_photos: ($event.target as HTMLInputElement).checked })"
          />
          <span>{{ t('profile.showVariantPhotos') }}</span>
        </label>
      </section>

      <section class="table-section">
        <div class="section-heading">
          <div>
            <h2>{{ t('profile.telemetry.title') }}</h2>
            <p>{{ t('profile.telemetry.intro') }}</p>
          </div>
        </div>
        <div class="telemetry-note">
          <strong>{{ t('profile.telemetry.anonymousTitle') }}</strong>
          <p>{{ t('profile.telemetry.anonymousText') }}</p>
        </div>
        <label class="checkbox-row">
          <input
            type="checkbox"
            :checked="data.profile.user.telemetry_enabled"
            @change="saveTelemetry(($event.target as HTMLInputElement).checked)"
          />
          <span>{{ t('profile.telemetry.allow') }}</span>
        </label>
        <p class="muted">{{ t('profile.telemetry.stopHint') }}</p>
      </section>

      <section class="table-section">
        <div class="section-heading">
          <div>
            <h2>{{ t('profile.mfa') }}</h2>
            <p>{{ caps?.mfa_required ? t('profile.mfaRequired') : t('profile.mfaOptional') }}</p>
          </div>
        </div>

        <template v-if="!data.profile.user.mfa_enabled">
          <button v-if="!enrollment" class="primary-button" type="button" @click="startEnrollment">
            {{ t('profile.mfaEnable') }}
          </button>
          <form v-else class="stack-form" @submit.prevent="confirmEnrollment">
            <p>{{ t('auth.enrollIntro') }}</p>
            <img class="mfa-qr" :src="enrollment.qr" :alt="t('auth.enrollQrAlt')" />
            <p class="mfa-secret"><code>{{ enrollment.secret }}</code></p>
            <label>
              {{ t('auth.code') }}
              <input v-model="enrollmentCode" inputmode="numeric" autocomplete="one-time-code" required />
            </label>
            <button class="primary-button" type="submit" :disabled="busy">{{ t('auth.confirm') }}</button>
          </form>
        </template>

        <template v-else>
          <p class="muted">{{ t('profile.recoveryLeft', { count: data.recovery_codes_left }) }}</p>
          <button class="secondary-button" type="button" @click="regenerateCodes">
            {{ t('profile.newRecoveryCodes') }}
          </button>
          <form v-if="!caps?.mfa_required" class="stack-form" @submit.prevent="disableMfa">
            <label>
              {{ t('profile.mfaDisableLabel') }}
              <input v-model="disableCode" inputmode="numeric" autocomplete="one-time-code" required />
            </label>
            <button class="danger-button" type="submit">{{ t('profile.mfaDisable') }}</button>
          </form>
        </template>
      </section>

      <section class="table-section">
        <div class="section-heading">
          <div>
            <h2>{{ t('profile.password') }}</h2>
            <p>{{ t('profile.passwordHint') }}</p>
          </div>
        </div>
        <form class="stack-form" @submit.prevent="changePassword">
          <label>
            {{ t('administration.support.password') }}
            <input v-model="passwords.current" type="password" autocomplete="current-password" required />
          </label>
          <label>
            {{ t('auth.newPassword') }}
            <input v-model="passwords.next" type="password" autocomplete="new-password" required />
          </label>
          <button class="primary-button" type="submit" :disabled="busy">{{ t('common.save') }}</button>
        </form>
      </section>
    </template>
  </main>
</template>

<style scoped>
.account-facts {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 6px 18px;
  margin: 0 0 18px;
}

.account-facts dt {
  color: var(--muted);
}

.account-facts dd {
  margin: 0;
}

.recovery-code-list {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  gap: 8px;
  margin: 0 0 18px;
  padding: 0;
  list-style: none;
}

.recovery-code-list code,
.mfa-secret code {
  display: block;
  padding: 8px 10px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--input-bg);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  text-align: center;
}

.telemetry-note {
  margin-bottom: 16px;
  padding: 12px 14px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--input-bg);
}

.telemetry-note p {
  margin: 5px 0 0;
}

.checkbox-row {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 10px;
}

.checkbox-row input[type='checkbox'] {
  width: 1.05rem;
  height: 1.05rem;
  accent-color: var(--accent);
}
</style>
