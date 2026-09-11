<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { useOfflineStore } from '@/stores/offline'
import { useSessionStore } from '@/stores/session'
import { usePackingStore } from '@/stores/packing'
import { useFlashStore } from '@/stores/flash'
import SupportMessageDialog from '@/components/SupportMessageDialog.vue'
import AccountMenu from './AccountMenu.vue'

/**
 * The sticky application header, ported from _old/templates/base.html.
 *
 * Navigation is filtered by the capabilities the server sent. That is a display
 * convenience only — every route is enforced again on the server, so hiding a
 * link is never the actual protection.
 */
const session = useSessionStore()
const offline = useOfflineStore()
const packing = usePackingStore()
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
const combinedQueued = computed(() => offline.queued + packing.queued)
const combinedSyncing = computed(() => offline.syncing || packing.syncing)
const combinedOnline = computed(() => offline.online && packing.online)

function syncOfflineData() {
  void offline.sync()
  void packing.sync()
}

interface NavLink {
  name: string
  label: string
  visible: boolean
}

function personalFeatureVisible(preference: 'show_packing_list' | 'show_product_palette') {
  // Sellers cannot configure the list and therefore always retain the two
  // operational links. Members may hide them without disabling the modules.
  return caps.value?.can_access_member_workflows !== true || session.user?.[preference] !== false
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
    { name: 'slideshow', label: t('nav.slideshow'), visible: (c.can_access_band_workflows || grant) && personalFeatureVisible('show_product_palette') },
    { name: 'articles', label: t('nav.articles'), visible: c.can_manage_articles || grant },
    { name: 'purchases', label: t('nav.purchases'), visible: c.can_access_member_workflows || grant },
    { name: 'band-finances', label: t('nav.bandFinances'), visible: (c.can_access_member_workflows || grant) && flags.value?.band_finances !== false },
    { name: 'balances', label: t('nav.balances'), visible: c.can_access_member_workflows || grant },
    { name: 'packing-list', label: t('nav.packingList'), visible: (c.can_use_packing_list || grant) && personalFeatureVisible('show_packing_list') },
    { name: 'administration', label: t('nav.administration'), visible: c.can_access_member_workflows || c.can_access_band_administration },
  ].filter((link) => link.visible)
})

function routeName(name: string) {
  return session.isSandbox ? `sandbox-${name}` : name
}

/** The divider separates selling from managing, as in the original. */
const dividerAfter = 'slideshow'

function isActive(name: string) {
  return route.name === routeName(name)
}

async function signOut() {
  await session.logout()
  await router.replace({ name: 'login' })
}

async function startSandbox() {
  try {
    await session.enterSandbox()
    await router.push({ name: 'sandbox-sales' })
  } catch {
    flash.error(t('errors.network'))
  }
}

async function restartTutorial() {
  try {
    await session.setSandboxTutorial(true, true)
  } catch {
    flash.error(t('errors.network'))
  }
}

async function discardSandbox() {
  if (!window.confirm(t('sandbox.discardConfirm'))) return
  try {
    const restored = await session.discardSandbox()
    await router.replace(restored
      ? { name: session.capabilities?.is_platform_staff ? 'platform-dashboard' : 'sales' }
      : { name: 'landing' })
  } catch {
    flash.error(t('errors.network'))
  }
}

</script>

<template>
  <header v-if="session.isAuthenticated" class="app-header">
    <RouterLink class="brand" :to="platformOnly ? { name: 'platform-dashboard' } : { name: routeName('sales') }">
      <span class="brand-mark">{{ session.isDevelopment ? 'T' : 'P' }}</span>
      <span class="brand-copy">
        <strong>{{ t(session.isDevelopment ? 'app.testName' : 'app.name') }}</strong>
        <small v-if="session.band?.name">{{ session.band.name }}</small>
      </span>
    </RouterLink>

    <nav class="main-nav" :aria-label="t('nav.label')">
      <template v-for="link in links" :key="link.name">
        <RouterLink
          :to="{ name: routeName(link.name) }"
          :class="{ active: isActive(link.name) }"
          :aria-current="isActive(link.name) ? 'page' : undefined"
        >{{ link.label }}</RouterLink>
        <span
          v-if="link.name === dividerAfter"
          class="main-nav-divider"
          aria-hidden="true"
        ></span>
      </template>

      <template v-if="caps?.can_access_system_administration && !session.isSandbox">
        <span class="main-nav-divider" aria-hidden="true"></span>
        <RouterLink
          :to="{ name: 'platform-dashboard' }"
          :class="{ active: isActive('platform-dashboard') }"
          :aria-current="isActive('platform-dashboard') ? 'page' : undefined"
        >
          {{ t('nav.systemAdministration') }}
        </RouterLink>
      </template>
    </nav>

    <div class="user-menu">
      <SupportMessageDialog v-if="caps?.can_access_band_workflows && !session.isSandbox" />

      <!-- The sync state stays visible throughout the band's offline-capable
           workflows, including the packing list. -->
      <button
        v-if="caps?.can_access_band_workflows && !session.isSandbox"
        class="offline-sync-status"
        :class="{ 'is-offline': !combinedOnline, 'has-queue': combinedQueued > 0 }"
        type="button"
        :disabled="!combinedOnline || combinedSyncing"
        @click="syncOfflineData"
      >
        <span class="offline-sync-label">
          <template v-if="!combinedOnline">{{ t('sync.offline', { count: combinedQueued }) }}</template>
          <template v-else-if="combinedSyncing">{{ t('sync.syncing') }}</template>
          <template v-else-if="combinedQueued > 0">{{ t('sync.pending', { count: combinedQueued }) }}</template>
          <template v-else>{{ t('sync.online') }}</template>
        </span>
      </button>

      <AccountMenu
        :username="session.user?.username ?? ''"
        :role-label="caps?.role_label ?? ''"
        :sandbox="session.isSandbox"
        :sandbox-available="session.identity?.sandbox_available"
        @logout="signOut"
        @sandbox="startSandbox"
        @tutorial="restartTutorial"
        @discard-sandbox="discardSandbox"
      />
    </div>
  </header>

</template>

<style scoped>
.offline-sync-status {
  min-height: 31px;
  padding: 5px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-small);
  background: var(--surface-subtle);
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

  :deep(.account-summary-copy),
  :deep(.account-chevron) {
    display: none;
  }
}
</style>
