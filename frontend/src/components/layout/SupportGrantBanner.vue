<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import { useSessionStore } from '@/stores/session'

/**
 * A persistent, unmissable notice shown while platform support is operating on
 * a band's data.
 *
 * Both sides see it: the support admin, so they never forget whose data they
 * are in, and the band, so an approved access window is never invisible.
 */
const session = useSessionStore()
const { t, d } = useI18n()

const grant = computed(() => session.supportGrant)
const expiresAt = computed(() =>
  grant.value ? new Date(grant.value.expires_at) : null,
)
</script>

<template>
  <div v-if="grant" class="support-grant-banner" role="status">
    <strong>{{ t('support.active') }}</strong>
    <span>
      {{ t('support.detail', { user: grant.username, reason: grant.reason }) }}
    </span>
    <span class="support-grant-scope">
      {{ grant.scope === 'read_only' ? t('support.readOnly') : t('support.readWrite') }}
    </span>
    <span v-if="expiresAt">{{ t('support.until', { time: d(expiresAt, 'time') }) }}</span>
  </div>
</template>

<style scoped>
.support-grant-banner {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  padding: 12px 28px;
  background: color-mix(in srgb, var(--warning) 20%, var(--panel));
  border-bottom: 1px solid var(--warning);
  color: var(--text);
  font-size: 0.9rem;
}

.support-grant-scope {
  padding: 2px 10px;
  border: 1px solid var(--warning);
  border-radius: 999px;
  font-weight: 650;
}
</style>
