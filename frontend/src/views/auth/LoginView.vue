<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { authApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import { useSessionStore } from '@/stores/session'
import type { LoginResponse } from '@/api/types'

/**
 * Sign-in, ported from _old/templates/login.html.
 *
 * The single secret field accepts either a password or the one-time setup code
 * an administrator handed out, exactly as the original did — a new account
 * should not have to know which of the two it is holding.
 *
 * The band slug is new: usernames are unique per band now, so the form has to
 * say which band is signing in. Platform accounts leave it empty.
 */
const session = useSessionStore()
const router = useRouter()
const route = useRoute()
const { t } = useI18n()

const band = ref('')
const username = ref('')
const secret = ref('')
const error = ref('')
const busy = ref(false)

/** The step the server asked for after the password was accepted. */
const step = ref<'credentials' | 'mfa' | 'password-setup' | 'mfa-enrollment'>('credentials')
const pendingToken = ref('')
const code = ref('')
const newPassword = ref('')
const enrollmentSecret = ref('')
const enrollmentUri = ref('')
const enrollmentQr = ref('')
const recoveryCodes = ref<string[]>([])

function fail(err: unknown) {
  if (err instanceof ApiError) {
    error.value = t(`errors.${err.detailCode ?? 'generic'}`, t('errors.generic'))
    return
  }
  error.value = t('errors.network')
}

/** Adopts a finished session and moves on to wherever the user was heading. */
function complete(response: LoginResponse) {
  if (!response.session) return false
  session.adopt(response.session, response.csrf_token)
  const target = (route.query.next as string) || '/sales'
  router.replace(target)
  return true
}

async function submitCredentials() {
  busy.value = true
  error.value = ''
  try {
    const response = await authApi.login(band.value.trim(), username.value.trim(), secret.value)
    if (complete(response)) return

    pendingToken.value = response.pending_token ?? ''
    if (response.needs_mfa) step.value = 'mfa'
    else if (response.needs_password_setup) step.value = 'password-setup'
    else if (response.needs_mfa_enrollment) await beginEnrollment()
  } catch (err) {
    fail(err)
  } finally {
    busy.value = false
  }
}

async function submitMfa() {
  busy.value = true
  error.value = ''
  try {
    complete(await authApi.completeMfa(pendingToken.value, code.value))
  } catch (err) {
    fail(err)
  } finally {
    busy.value = false
  }
}

async function submitPasswordSetup() {
  busy.value = true
  error.value = ''
  try {
    const response = await authApi.completePasswordSetup(pendingToken.value, newPassword.value)
    if (complete(response)) return
    pendingToken.value = response.pending_token ?? ''
    if (response.needs_mfa_enrollment) await beginEnrollment()
  } catch (err) {
    fail(err)
  } finally {
    busy.value = false
  }
}

async function beginEnrollment() {
  step.value = 'mfa-enrollment'
  try {
    const started = await authApi.startEnrollment(pendingToken.value)
    enrollmentSecret.value = started.secret
    enrollmentUri.value = started.otpauth_uri
    enrollmentQr.value = started.otpauth_qr
  } catch (err) {
    fail(err)
  }
}

async function submitEnrollment() {
  busy.value = true
  error.value = ''
  try {
    const response = await authApi.confirmEnrollment(code.value, pendingToken.value)
    recoveryCodes.value = response.recovery_codes
    // The session exists now, but the recovery codes are shown exactly once,
    // so the redirect waits for an explicit acknowledgement.
    if (response.csrf_token && response.session) {
      session.adopt(response.session, response.csrf_token)
    }
  } catch (err) {
    fail(err)
  } finally {
    busy.value = false
  }
}

function finishEnrollment() {
  router.replace((route.query.next as string) || '/sales')
}
</script>

<template>
  <main class="login-page">
    <section class="login-card">
      <div class="brand brand-login">
        <span class="brand-mark">P</span><span>{{ t('app.name') }}</span>
      </div>

      <template v-if="recoveryCodes.length">
        <h1>{{ t('auth.recoveryTitle') }}</h1>
        <p>{{ t('auth.recoveryIntro') }}</p>
        <ul class="recovery-code-list">
          <li v-for="entry in recoveryCodes" :key="entry"><code>{{ entry }}</code></li>
        </ul>
        <button class="primary-button full-width" type="button" @click="finishEnrollment">
          {{ t('auth.recoverySaved') }}
        </button>
      </template>

      <template v-else-if="step === 'credentials'">
        <h1>{{ t('auth.signIn') }}</h1>
        <p>{{ t('auth.intro') }}</p>
        <div v-if="error" class="flash error">{{ error }}</div>
        <form class="stack-form" @submit.prevent="submitCredentials">
          <label>
            {{ t('auth.band') }}
            <input v-model="band" autocomplete="organization" :placeholder="t('auth.bandHint')" />
          </label>
          <label>
            {{ t('auth.username') }}
            <input v-model="username" autocomplete="username" required autofocus />
          </label>
          <label>
            {{ t('auth.secret') }}
            <input v-model="secret" type="password" autocomplete="current-password" required />
          </label>
          <button class="primary-button full-width" type="submit" :disabled="busy">
            {{ t('auth.signIn') }}
          </button>
        </form>
      </template>

      <template v-else-if="step === 'mfa'">
        <h1>{{ t('auth.mfaTitle') }}</h1>
        <p>{{ t('auth.mfaIntro') }}</p>
        <div v-if="error" class="flash error">{{ error }}</div>
        <form class="stack-form" @submit.prevent="submitMfa">
          <label>
            {{ t('auth.code') }}
            <input v-model="code" inputmode="numeric" autocomplete="one-time-code" required autofocus />
          </label>
          <button class="primary-button full-width" type="submit" :disabled="busy">
            {{ t('auth.confirm') }}
          </button>
        </form>
      </template>

      <template v-else-if="step === 'password-setup'">
        <h1>{{ t('auth.setupTitle') }}</h1>
        <p>{{ t('auth.setupIntro') }}</p>
        <div v-if="error" class="flash error">{{ error }}</div>
        <form class="stack-form" @submit.prevent="submitPasswordSetup">
          <label>
            {{ t('auth.newPassword') }}
            <input v-model="newPassword" type="password" autocomplete="new-password" required autofocus />
          </label>
          <button class="primary-button full-width" type="submit" :disabled="busy">
            {{ t('common.save') }}
          </button>
        </form>
      </template>

      <template v-else>
        <h1>{{ t('auth.enrollTitle') }}</h1>
        <p>{{ t('auth.enrollIntro') }}</p>
        <div v-if="error" class="flash error">{{ error }}</div>
        <img v-if="enrollmentQr" class="mfa-qr" :src="enrollmentQr" :alt="t('auth.enrollQrAlt')" />
        <p v-if="enrollmentSecret" class="mfa-secret">
          <code>{{ enrollmentSecret }}</code>
        </p>
        <form class="stack-form" @submit.prevent="submitEnrollment">
          <label>
            {{ t('auth.code') }}
            <input v-model="code" inputmode="numeric" autocomplete="one-time-code" required />
          </label>
          <button class="primary-button full-width" type="submit" :disabled="busy">
            {{ t('auth.confirm') }}
          </button>
        </form>
      </template>
    </section>
  </main>
</template>

<style scoped>
.recovery-code-list {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 8px;
  margin: 0 0 24px;
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
  letter-spacing: 0.04em;
}

.mfa-secret {
  margin: 0 0 18px;
}
</style>
