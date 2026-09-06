<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { useOfflineStore } from '@/stores/offline'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'
import { ApiError } from '@/api/client'
import SupportMessageDialog from '@/components/SupportMessageDialog.vue'

/**
 * The sticky application header, ported from _old/templates/base.html.
 *
 * Navigation is filtered by the capabilities the server sent. That is a display
 * convenience only — every route is enforced again on the server, so hiding a
 * link is never the actual protection.
 */
const session = useSessionStore()
const offline = useOfflineStore()
const flash = useFlashStore()
const router = useRouter()
const route = useRoute()
const { t } = useI18n()

const caps = computed(() => session.capabilities)
const flags = computed(() => session.featureFlags)
const platformOnly = computed(
  () => caps.value?.can_access_system_administration && !caps.value?.can_access_band_workflows,
)
/** A live support grant stands in for the band role in the navigation. */
const viaGrant = computed(() => session.supportGrant !== null)
const posExitPrompt = ref<{ password: string; code: string } | null>(null)
const posBusy = ref(false)

interface NavLink {
  name: string
  label: string
  visible: boolean
  /** Restricted links stay visible but inert while POS mode is active, so the
   *  seller can see what exists without being able to reach it. */
  posRestricted?: boolean
}

const links = computed<NavLink[]>(() => {
  const c = caps.value
  if (!c) return []
  const grant = viaGrant.value
  return [
    { name: 'sales', label: t('nav.sales'), visible: c.can_access_band_workflows || grant },
    // Orders stay routable for a future API-backed release, but are no longer
    // advertised as a current workflow.
    { name: 'history', label: t('nav.history'), visible: c.can_access_member_workflows || grant },
    { name: 'operations', label: t('nav.operations'), visible: c.can_access_member_workflows || grant },
    { name: 'slideshow', label: t('nav.slideshow'), visible: (c.can_access_band_workflows || grant) && flags.value?.slideshow !== false },
    { name: 'articles', label: t('nav.articles'), visible: c.can_manage_articles || grant, posRestricted: true },
    { name: 'purchases', label: t('nav.purchases'), visible: c.can_access_member_workflows || grant, posRestricted: true },
    { name: 'band-finances', label: t('nav.bandFinances'), visible: (c.can_access_member_workflows || grant) && flags.value?.band_finances !== false, posRestricted: true },
    { name: 'balances', label: t('nav.balances'), visible: c.can_access_member_workflows || grant, posRestricted: true },
    { name: 'administration', label: t('nav.administration'), visible: c.can_access_band_administration, posRestricted: true },
  ].filter((link) => link.visible)
})

/** The divider separates selling from managing, as in the original. */
const dividerAfter = 'slideshow'

function isActive(name: string) {
  return route.name === name
}

async function signOut() {
  await session.logout()
  router.push({ name: 'login' })
}

function report(error: unknown) {
  flash.error(
    error instanceof ApiError
      ? t(`errors.${error.detailCode ?? 'generic'}`, error.message)
      : t('errors.network'),
  )
}

async function togglePOSMode() {
  if (session.posMode) {
    posExitPrompt.value = { password: '', code: '' }
    return
  }
  try {
    await session.setPosMode(true)
  } catch (error) {
    report(error)
  }
}

async function leavePOSMode() {
  if (!posExitPrompt.value || posBusy.value) return
  posBusy.value = true
  try {
    await session.setPosMode(false, posExitPrompt.value.password, posExitPrompt.value.code)
    posExitPrompt.value = null
  } catch (error) {
    report(error)
  } finally {
    posBusy.value = false
  }
}
</script>

<template>
  <header v-if="session.isAuthenticated" class="app-header">
    <RouterLink class="brand" :to="platformOnly ? { name: 'platform-bands' } : { name: 'sales' }">
      <span class="brand-mark">P</span>
      <span>{{ t('app.name') }}</span>
    </RouterLink>

    <nav class="main-nav" :aria-label="t('nav.label')">
      <template v-for="link in links" :key="link.name">
        <span
          v-if="link.posRestricted && session.posMode"
          class="pos-restricted-nav"
          aria-disabled="true"
        >{{ link.label }}</span>
        <RouterLink
          v-else
          :to="{ name: link.name }"
          :class="{ active: isActive(link.name) }"
        >{{ link.label }}</RouterLink>
        <span
          v-if="link.name === dividerAfter"
          class="main-nav-divider"
          aria-hidden="true"
        ></span>
      </template>

      <template v-if="caps?.can_access_system_administration">
        <span class="main-nav-divider" aria-hidden="true"></span>
        <RouterLink :to="{ name: 'platform-bands' }">
          {{ t('nav.systemAdministration') }}
        </RouterLink>
      </template>
    </nav>

    <div class="user-menu">
      <SupportMessageDialog v-if="caps?.can_access_band_workflows" />
      <button
        v-if="caps?.can_access_band_workflows && flags?.offline_sales !== false"
        class="pos-mode-button"
        :class="{ 'is-active': session.posMode }"
        type="button"
        :aria-pressed="session.posMode"
        @click="togglePOSMode"
      >
        {{ t('nav.posMode') }}
      </button>

      <!-- The sync state is always visible while selling: a seller at a stand
           must be able to tell at a glance whether their sales have landed. -->
      <button
        v-if="caps?.can_access_band_workflows && flags?.offline_sales !== false"
        class="offline-sync-status"
        :class="{ 'is-offline': !offline.online, 'has-queue': offline.hasQueue }"
        type="button"
        :disabled="!offline.online || offline.syncing"
        @click="offline.sync()"
      >
        <span class="offline-sync-label">
          <template v-if="!offline.online">{{ t('sync.offline', { count: offline.queued }) }}</template>
          <template v-else-if="offline.syncing">{{ t('sync.syncing') }}</template>
          <template v-else-if="offline.hasQueue">{{ t('sync.pending', { count: offline.queued }) }}</template>
          <template v-else>{{ t('sync.online') }}</template>
        </span>
      </button>

      <span class="user-identity">
        <RouterLink class="user-profile-link" :to="{ name: 'profile' }">
          {{ session.user?.username }} · {{ caps?.role_label }}
        </RouterLink>
      </span>

      <button class="text-button" type="button" @click="signOut">
        {{ t('common.logout') }}
      </button>
    </div>
  </header>

  <dialog v-if="posExitPrompt" class="confirmation-dialog" open>
    <form @submit.prevent="leavePOSMode">
      <p class="eyebrow">{{ t('posExit.eyebrow') }}</p>
      <h2>{{ t('posExit.title') }}</h2>
      <p>{{ t('posExit.intro') }}</p>
      <label>
        {{ t('posExit.password') }}
        <input
          v-model="posExitPrompt.password"
          type="password"
          autocomplete="current-password"
          required
          autofocus
        />
      </label>
      <label v-if="caps?.sensitive_action_mfa_required">
        {{ t('posExit.code') }}
        <input
          v-model="posExitPrompt.code"
          inputmode="numeric"
          autocomplete="one-time-code"
          required
        />
      </label>
      <div class="dialog-actions">
        <button class="secondary-button" type="button" :disabled="posBusy" @click="posExitPrompt = null">
          {{ t('common.cancel') }}
        </button>
        <button class="primary-button" type="submit" :disabled="posBusy">
          {{ t('posExit.leave') }}
        </button>
      </div>
    </form>
  </dialog>
</template>

<style scoped>
.offline-sync-status {
  min-height: 31px;
  padding: 5px 10px;
  border: 1px solid var(--border);
  border-radius: 7px;
  background: rgba(255, 255, 255, 0.025);
  color: var(--muted);
  font: inherit;
  font-size: 0.76rem;
  font-weight: 650;
}

.offline-sync-status::before {
  width: 7px;
  height: 7px;
  flex: 0 0 auto;
  border-radius: 50%;
  content: "";
  background: var(--success);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--success) 16%, transparent);
}

.offline-sync-status.has-queue {
  color: var(--warning);
  border-color: var(--warning);
}

.offline-sync-status.is-offline {
  color: var(--danger);
  border-color: var(--danger);
}

.offline-sync-status.has-queue::before {
  background: var(--warning);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--warning) 16%, transparent);
}

.offline-sync-status.is-offline::before {
  background: var(--danger);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--danger) 16%, transparent);
}

/*
 * On a phone the connection state is a traffic light, not another navigation
 * label. Keep the text available to assistive technology while only the
 * coloured marker occupies header space.
 */
@media (max-width: 700px) {
  .offline-sync-status {
    position: relative;
    width: 34px;
    min-width: 34px;
    min-height: 34px;
    padding: 5px;
    justify-content: center;
  }

  .offline-sync-label {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
    clip-path: inset(50%);
  }
}
</style>
