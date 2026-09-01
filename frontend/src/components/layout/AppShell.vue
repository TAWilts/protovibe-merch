<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, RouterView } from 'vue-router'
import { useI18n } from 'vue-i18n'

import AppHeader from './AppHeader.vue'
import SupportGrantBanner from './SupportGrantBanner.vue'
import SystemStatusBanner from './SystemStatusBanner.vue'
import FlashStack from '@/components/FlashStack.vue'
import { useSessionStore } from '@/stores/session'

/** The band-facing shell: header, support notice, flash messages, page. */
const session = useSessionStore()
const { t } = useI18n()

/** A platform account only reaches this shell through a live grant. */
const viaGrant = computed(
  () => session.supportGrant !== null && !session.capabilities?.can_access_band_workflows,
)
</script>

<template>
  <SystemStatusBanner />
  <SupportGrantBanner />
  <AppHeader />
  <p v-if="viaGrant" class="grant-return">
    <RouterLink :to="{ name: 'platform-bands' }">{{ t('platform.backToAdmin') }}</RouterLink>
  </p>
  <FlashStack />
  <RouterView />
</template>

<style scoped>
.grant-return {
  max-width: 1500px;
  margin: 14px auto -10px;
  padding: 0 28px;
  font-size: 0.88rem;
}

.grant-return a {
  color: var(--accent-bright);
  text-decoration: underline;
}
</style>
