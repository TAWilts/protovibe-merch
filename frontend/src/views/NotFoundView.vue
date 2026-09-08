<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { useSessionStore } from '@/stores/session'

const session = useSessionStore()
const { t } = useI18n()
const destination = computed(() => {
  if (!session.isAuthenticated) return { name: 'landing' }
  return { name: session.capabilities?.is_platform_staff ? 'platform-dashboard' : 'sales' }
})
const destinationLabel = computed(() => session.isAuthenticated
  ? t('notFound.backToApp')
  : t('notFound.backHome'))
</script>

<template>
  <main id="main-content" class="page-shell not-found-page" tabindex="-1">
    <section class="empty-state not-found-card">
      <p class="eyebrow">404</p>
      <h1>{{ t('notFound.title') }}</h1>
      <p>{{ t('notFound.message') }}</p>
      <RouterLink class="primary-button" :to="destination">{{ destinationLabel }}</RouterLink>
    </section>
  </main>
</template>

<style scoped>
.not-found-page { min-height: 100vh; display: grid; place-items: center; }
.not-found-card { width: min(560px, 100%); margin: 0; border-style: solid; }
.not-found-card .eyebrow { margin-bottom: 10px; }
</style>
