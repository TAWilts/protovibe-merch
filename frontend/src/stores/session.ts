import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { authApi } from '@/api/endpoints'
import { ApiError, setCsrfToken } from '@/api/client'
import type { Identity } from '@/api/types'
import { setLocale } from '@/i18n'

/**
 * The session store is the single source of truth for who is signed in.
 *
 * Capabilities come from the server and only drive what the UI renders; every
 * route enforces the same rights again on the server, so a tampered client
 * gains nothing.
 */
export const useSessionStore = defineStore('session', () => {
  const OFFLINE_IDENTITY_KEY = 'protovibe.offline-identity.v1'
  const identity = ref<Identity | null>(null)
  const offlineIdentity = ref(false)
  const loading = ref(false)
  const ready = ref(false)

  const user = computed(() => identity.value?.user ?? null)
  const band = computed(() => identity.value?.band ?? null)
  const featureFlags = computed(() => identity.value?.band?.feature_flags ?? null)
  const capabilities = computed(() => identity.value?.capabilities ?? null)
  const posMode = computed(() => identity.value?.pos_mode ?? false)
  const supportGrant = computed(() => identity.value?.support_grant ?? null)
  const isAuthenticated = computed(() => identity.value !== null)

  /** Applies account preferences that are available inside the app. */
  function applyPreferences(next: Identity | null) {
    const theme = next?.user.ui_theme ?? 'aurora'
    document.documentElement.dataset.theme = theme
    // Marketing and login are bilingual, but the authenticated application
    // remains German until its English catalogue is complete.
    setLocale('de')
  }

  function adopt(
    next: Identity | null,
    csrfToken?: string,
    applyUserPreferences = true,
    fromOfflineCache = false,
  ) {
    identity.value = next
    offlineIdentity.value = fromOfflineCache
    if (next?.band && !fromOfflineCache) {
      // This deliberately stores only the already-public session identity.
      // Passwords, MFA secrets and recovery codes never enter this object.
      localStorage.setItem(OFFLINE_IDENTITY_KEY, JSON.stringify(next))
    } else if (!next) {
      localStorage.removeItem(OFFLINE_IDENTITY_KEY)
    }
    if (csrfToken) {
      setCsrfToken(csrfToken)
    }
    if (applyUserPreferences) applyPreferences(next)
  }

  function cachedOfflineIdentity(): Identity | null {
    try {
      const cached = JSON.parse(localStorage.getItem(OFFLINE_IDENTITY_KEY) ?? 'null') as Identity | null
      if (!cached?.band || !cached.user?.id || !cached.capabilities?.can_access_band_workflows) return null
      return cached
    } catch {
      return null
    }
  }

  /**
   * Restores the session on a page load. A missing session is a normal state,
   * not an error, so it resolves to signed-out rather than throwing.
   */
  async function restore(applyUserPreferences = true) {
    loading.value = true
    try {
      adopt(await authApi.me(), undefined, applyUserPreferences)
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        adopt(null, undefined, applyUserPreferences)
      } else {
        const cached = cachedOfflineIdentity()
        if (!cached) throw error
        adopt(cached, undefined, applyUserPreferences, true)
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
    offlineIdentity,
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
