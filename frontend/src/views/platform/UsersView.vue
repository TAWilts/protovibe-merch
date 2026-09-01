<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { platformApi, profileApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { PlatformUser, Role } from '@/api/types'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'

const { t, d } = useI18n()
const flash = useFlashStore()
const session = useSessionStore()

const users = ref<PlatformUser[]>([])
const roles = ref<Role[]>([])
const loading = ref(true)
const busy = ref(false)
const newUser = ref({ username: '', contactEmail: '', role: 'support_admin' as Role })
const issuedCode = ref<{ username: string; code: string } | null>(null)
const reauth = ref<{ password: string; code: string; action: () => Promise<void> } | null>(null)
const reauthedUntil = ref(0)
const needsCode = computed(() => session.capabilities?.sensitive_action_mfa_required ?? false)

onMounted(load)

async function load() {
  loading.value = true
  try {
    const result = await platformApi.users()
    users.value = result.users
    roles.value = result.assignable_roles
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

async function guarded(action: () => Promise<void>) {
  if (Date.now() < reauthedUntil.value) {
    await run(action)
    return
  }
  reauth.value = { password: '', code: '', action }
}

async function confirmReauth() {
  if (!reauth.value || busy.value) return
  busy.value = true
  try {
    const result = await profileApi.reauth(reauth.value.password, reauth.value.code)
    reauthedUntil.value = Date.now() + result.valid_for_seconds * 1000
    const action = reauth.value.action
    reauth.value = null
    await run(action)
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

async function run(action: () => Promise<void>) {
  try {
    await action()
    await load()
  } catch (error) {
    report(error)
    await load()
  }
}

async function createUser() {
  const username = newUser.value.username.trim()
  if (!username) return
  await guarded(async () => {
    const result = await platformApi.createUser(
      username, newUser.value.contactEmail.trim(), newUser.value.role,
    )
    issuedCode.value = { username: result.username, code: result.setup_code }
    newUser.value = { username: '', contactEmail: '', role: 'support_admin' }
  })
}

function changeRole(user: PlatformUser, role: Role) {
  void guarded(() => platformApi.changeUserRole(user.id, role))
}

function setActive(user: PlatformUser, active: boolean) {
  void guarded(() => platformApi.setUserActive(user.id, active))
}

function resetPassword(user: PlatformUser) {
  void guarded(async () => {
    const result = await platformApi.resetUserPassword(user.id)
    issuedCode.value = { username: result.username, code: result.setup_code }
  })
}

function resetMfa(user: PlatformUser) {
  void guarded(() => platformApi.resetUserMfa(user.id))
}
</script>

<template>
  <main class="page-shell">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('platform.eyebrow') }}</p>
        <h1>{{ t('platform.users.title') }}</h1>
        <p>{{ t('platform.users.intro') }}</p>
      </div>
    </div>

    <section class="table-section">
      <form class="field-grid three-columns" @submit.prevent="createUser">
        <label>{{ t('auth.username') }}<input v-model="newUser.username" required /></label>
        <label>{{ t('platform.users.email') }}<input v-model="newUser.contactEmail" type="email" /></label>
        <label>
          {{ t('administration.users.role') }}
          <select v-model="newUser.role">
            <option value="support_admin">Support-Admin</option>
            <option value="system_admin">System-Admin</option>
          </select>
        </label>
        <button class="primary-button" type="submit">{{ t('administration.users.create') }}</button>
      </form>
    </section>

    <p v-if="loading" class="muted">{{ t('common.loading') }}</p>
    <section v-else class="table-section">
      <div class="table-scroll">
        <table>
          <thead><tr>
            <th>{{ t('auth.username') }}</th><th>{{ t('platform.users.email') }}</th>
            <th>{{ t('administration.users.role') }}</th><th>{{ t('administration.users.state') }}</th>
            <th>{{ t('profile.lastLogin') }}</th><th></th>
          </tr></thead>
          <tbody>
            <tr v-for="user in users" :key="user.id">
              <td><strong>{{ user.username }}</strong> <small v-if="user.is_self">({{ t('platform.users.you') }})</small></td>
              <td>{{ user.contact_email || '—' }}</td>
              <td>
                <select :value="user.role" :disabled="user.is_self" @change="changeRole(user, ($event.target as HTMLSelectElement).value as Role)">
                  <option v-for="role in roles" :key="role" :value="role">
                    {{ role === 'system_admin' ? 'System-Admin' : 'Support-Admin' }}
                  </option>
                </select>
              </td>
              <td>{{ user.must_set_password ? t('administration.users.awaitingSetup') : user.is_active ? t('administration.users.active') : t('administration.users.inactive') }}</td>
              <td>{{ user.last_login_at ? d(new Date(user.last_login_at), 'short') : '—' }}</td>
              <td class="row-actions">
                <button class="compact-button" type="button" @click="resetPassword(user)">{{ t('administration.users.resetPassword') }}</button>
                <button v-if="user.mfa_enabled" class="compact-button" type="button" @click="resetMfa(user)">{{ t('administration.users.resetMfa') }}</button>
                <button v-if="!user.is_self" class="compact-button" type="button" @click="setActive(user, !user.is_active)">
                  {{ user.is_active ? t('administration.users.deactivate') : t('administration.users.activate') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <dialog v-if="issuedCode" class="confirmation-dialog" open>
      <div class="stack-form">
        <h2>{{ t('administration.users.codeFor', { user: issuedCode.username }) }}</h2>
        <p>{{ t('administration.users.codeHint') }}</p>
        <code class="setup-code">{{ issuedCode.code }}</code>
        <button class="primary-button" type="button" @click="issuedCode = null">{{ t('common.close') }}</button>
      </div>
    </dialog>

    <dialog v-if="reauth" class="confirmation-dialog" open>
      <form class="stack-form" @submit.prevent="confirmReauth">
        <h2>{{ t('administration.users.confirmTitle') }}</h2>
        <p>{{ t('administration.users.confirmIntro') }}</p>
        <label>{{ t('administration.support.password') }}<input v-model="reauth.password" type="password" required /></label>
        <label v-if="needsCode">{{ t('auth.code') }}<input v-model="reauth.code" inputmode="numeric" required /></label>
        <button class="primary-button" type="submit" :disabled="busy">{{ t('common.confirm') }}</button>
        <button class="secondary-button" type="button" @click="reauth = null">{{ t('common.cancel') }}</button>
      </form>
    </dialog>
  </main>
</template>

<style scoped>
.row-actions { display: flex; flex-wrap: wrap; gap: 6px; }
.setup-code { display: block; padding: 14px; font-size: 1.2rem; text-align: center; }
</style>
