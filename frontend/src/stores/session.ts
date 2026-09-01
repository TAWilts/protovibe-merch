import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { authApi } from '@/api/endpoints'
import { ApiError, setCsrfToken } from '@/api/client'
import type { Identity } from '@/api/types'
import { setLocale, type Locale } from '@/i18n'

/**
 * The session store is the single source of truth for who is signed in.
 *
 * Capabilities come from the server and only drive what the UI renders; every
 * route enforces the same rights again on the server, so a tampered client
 * gains nothing.
 */
export const useSessionStore = defineStore('session', () => {
  const identity = ref<Identity | null>(null)
  const loading = ref(false)
  const ready = ref(false)

  const user = computed(() => identity.value?.user ?? null)
  const band = computed(() => identity.value?.band ?? null)
  const featureFlags = computed(() => identity.value?.band?.feature_flags ?? null)
  const capabilities = computed(() => identity.value?.capabilities ?? null)
  const posMode = computed(() => identity.value?.pos_mode ?? false)
  const supportGrant = computed(() => identity.value?.support_grant ?? null)
  const isAuthenticated = computed(() => identity.value !== null)

  /** Applies the personal theme and language the account chose. */
  function applyPreferences(next: Identity | null) {
    const theme = next?.user.ui_theme ?? 'aurora'
    document.documentElement.dataset.theme = theme
    const language = (next?.user.ui_language ?? 'de') as Locale
    setLocale(language)
  }

  function adopt(next: Identity | null, csrfToken?: string) {
    identity.value = next
    if (csrfToken) {
      setCsrfToken(csrfToken)
    }
    applyPreferences(next)
  }

  /**
   * Restores the session on a page load. A missing session is a normal state,
   * not an error, so it resolves to signed-out rather than throwing.
   */
  async function restore() {
    loading.value = true
    try {
      adopt(await authApi.me())
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        adopt(null)
      } else {
        throw error
      }
    } finally {
      loading.value = false
      ready.value = true
    }
  }

  async function logout() {
    try {
      await authApi.logout()
    } finally {
      adopt(null)
      setCsrfToken('')
    }
  }

  async function setPosMode(enabled: boolean, password = '', code = '') {
    const result = await authApi.setPosMode(enabled, password, code)
    if (identity.value) {
      identity.value = { ...identity.value, pos_mode: result.pos_mode }
    }
  }

  return {
    identity,
    user,
    band,
    featureFlags,
    capabilities,
    posMode,
    supportGrant,
    isAuthenticated,
    loading,
    ready,
    adopt,
    restore,
    logout,
    setPosMode,
  }
})
