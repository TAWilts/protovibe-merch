<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { useSessionStore } from '@/stores/session'
import FlashStack from '@/components/FlashStack.vue'
import SupportGrantBanner from './SupportGrantBanner.vue'
import AccountMenu from './AccountMenu.vue'

/**
 * The admin center's own shell.
 *
 * It is deliberately a separate surface from the band app: a platform account
 * has no band data at all unless a support grant is live, so showing it the
 * band navigation would only offer doors that are locked.
 */
const session = useSessionStore()
const route = useRoute()
const router = useRouter()
const { t } = useI18n()

const isSystemAdmin = computed(() => session.capabilities?.is_system_admin ?? false)
const links = computed(() => [
  { name: 'platform-dashboard', label: 'Dashboard', systemOnly: false },
  { name: 'platform-bands', label: 'platform.nav.bands', systemOnly: false },
  { name: 'platform-registrations', label: 'platform.nav.registrations', systemOnly: true },
  { name: 'platform-users', label: 'platform.nav.users', systemOnly: true },
  { name: 'platform-support', label: 'platform.nav.support', systemOnly: false },
  { name: 'platform-messages', label: 'platform.nav.messages', systemOnly: false },
  { name: 'platform-audit', label: 'platform.nav.audit', systemOnly: false },
  { name: 'platform-telemetry', label: 'nav.statistics', systemOnly: true },
  { name: 'platform-backups', label: 'platform.nav.backups', systemOnly: false },
  { name: 'platform-settings', label: 'platform.nav.settings', systemOnly: false },
].filter((link) => !link.systemOnly || isSystemAdmin.value))

async function signOut() {
  await session.logout()
  router.push({ name: 'login' })
}
</script>

<template>
  <SupportGrantBanner />

  <header class="app-header platform-header">
    <RouterLink class="brand" :to="{ name: 'platform-dashboard' }">
      <span class="brand-mark">P</span>
      <span class="brand-copy">
        <strong>{{ t('platform.title') }}</strong>
        <small>{{ t('platform.context') }}</small>
      </span>
    </RouterLink>

    <nav class="main-nav" :aria-label="t('nav.label')">
      <RouterLink
        v-for="link in links"
        :key="link.name"
        :to="{ name: link.name }"
        :class="{ active: route.name === link.name }"
        :aria-current="route.name === link.name ? 'page' : undefined"
      >{{ link.name === 'platform-dashboard' ? link.label : t(link.label) }}</RouterLink>
    </nav>

    <div class="user-menu">
      <!-- A live grant is the whole point of the support flow, so it has to
           open this door too: a platform account never has
           can_access_band_workflows, and without the grant check the link
           stayed hidden exactly when it was needed. -->
      <RouterLink
        v-if="session.capabilities?.can_access_band_workflows || session.supportGrant"
        :class="session.supportGrant ? 'grant-link' : 'text-button'"
        :to="{ name: 'sales' }"
      >{{ session.supportGrant ? t('platform.toGrantedBand', { band: session.band?.name ?? '' }) : t('platform.toBandApp') }}</RouterLink>
      <AccountMenu
        :username="session.user?.username ?? ''"
        :role-label="session.capabilities?.role_label ?? ''"
        @logout="signOut"
      />
    </div>
  </header>

  <FlashStack />

  <div v-if="!isSystemAdmin" class="platform-role-note">
    {{ t('platform.supportAdminNote') }}
  </div>

  <span id="main-content" class="main-content-anchor" tabindex="-1"></span>
  <RouterView />
</template>

<style scoped>
/* A different accent line makes it unmistakable which surface you are on. */
.platform-header {
  border-bottom: 2px solid var(--accent);
  background: color-mix(in srgb, var(--accent) 5%, var(--surface-raised));
}

.platform-header .brand-mark { border-radius: var(--radius-small); }

/* While a grant is open this is the one thing an operator is looking for, so
   it must not read as another "Abmelden" next to it. */
.grant-link {
  padding: 5px 12px;
  border: 1px solid var(--warning);
  border-radius: 999px;
  color: var(--text);
  background: color-mix(in srgb, var(--warning) 18%, transparent);
  font-weight: 650;
  text-decoration: none;
  white-space: nowrap;
}

.grant-link:hover {
  background: color-mix(in srgb, var(--warning) 30%, transparent);
}

.platform-role-note {
  max-width: 1500px;
  margin: 18px auto -8px;
  padding: 10px 28px;
  color: var(--muted);
  font-size: 0.88rem;
}
</style>
