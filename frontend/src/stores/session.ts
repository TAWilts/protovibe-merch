import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { authApi, profileApi } from '@/api/endpoints'
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
    let serverSucceeded = false
    try {
      await authApi.logout()
      serverSucceeded = true
    } catch {
      // The browser can no longer safely use this identity even when the
      // server cannot be reached. Keep the existing local-logout policy, but
      // report the remote outcome to callers instead of pretending it worked.
    } finally {
      clear()
    }
    return serverSucceeded
  }

  function clear() {
    adopt(null)
    setCsrfToken('')
  }

  async function setFeatureVisibility(payload: {
    show_packing_list?: boolean
    show_product_palette?: boolean
  }) {
    const result = await profileApi.featureVisibility(payload)
    if (identity.value) {
      identity.value = {
        ...identity.value,
        user: { ...identity.value.user, ...result },
      }
      if (identity.value.band) {
        localStorage.setItem(OFFLINE_IDENTITY_KEY, JSON.stringify(identity.value))
      }
    }
    return result
  }

  return {
    identity,
    offlineIdentity,
    user,
    band,
    featureFlags,
    capabilities,
    supportGrant,
    isAuthenticated,
    loading,
    ready,
    adopt,
    clear,
    restore,
    logout,
    setFeatureVisibility,
  }
})
