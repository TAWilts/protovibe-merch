<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { supportApi } from '@/api/endpoints'
import { useSessionStore } from '@/stores/session'

type Announcement = { text: string; level: 'info' | 'warning' | 'critical' }

const { t } = useI18n()
const router = useRouter()
const session = useSessionStore()
const announcement = ref<Announcement | null>(null)
const maintenance = ref<{ message: string } | null>(null)
let refreshTimer: number | undefined

async function refresh() {
  try {
    const status = await supportApi.announcement()
    announcement.value = status.announcement
    maintenance.value = status.maintenance
  } catch {
    // Page requests still report operational failures. A temporary status
    // refresh failure must not replace the page with another error.
  }
}

function refreshWhenVisible() {
  if (document.visibilityState === 'visible') refresh()
}

async function logout() {
  await session.logout()
  await router.push({ name: 'login' })
}

onMounted(() => {
  refresh()
  refreshTimer = window.setInterval(refresh, 30_000)
  document.addEventListener('visibilitychange', refreshWhenVisible)
})

onUnmounted(() => {
  window.clearInterval(refreshTimer)
  document.removeEventListener('visibilitychange', refreshWhenVisible)
})
</script>

<template>
  <aside
    v-if="announcement && !maintenance"
    class="system-announcement"
    :class="`is-${announcement.level}`"
    role="status"
  >
    <strong>{{ t('systemStatus.announcement') }}</strong>
    <span>{{ announcement.text }}</span>
  </aside>

  <section v-if="maintenance" class="maintenance-screen" role="alert" aria-live="assertive">
    <div class="maintenance-card">
      <p class="eyebrow">{{ t('systemStatus.maintenanceEyebrow') }}</p>
      <h1>{{ t('systemStatus.maintenanceTitle') }}</h1>
      <p>{{ maintenance.message || t('systemStatus.maintenanceFallback') }}</p>
      <p class="muted">{{ t('systemStatus.maintenanceHint') }}</p>
      <button class="secondary-button" type="button" @click="logout">
        {{ t('common.logout') }}
      </button>
    </div>
  </section>
</template>

<style scoped>
.system-announcement {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  gap: 8px 14px;
  padding: 10px 24px;
  border-bottom: 1px solid var(--border);
  color: var(--text);
  background: color-mix(in srgb, var(--accent) 22%, var(--panel));
}

.system-announcement.is-warning {
  background: color-mix(in srgb, var(--warning) 20%, var(--panel));
}

.system-announcement.is-critical {
  background: color-mix(in srgb, var(--danger) 24%, var(--panel));
}

.maintenance-screen {
  position: fixed;
  inset: 0;
  z-index: 1000;
  display: grid;
  place-items: center;
  padding: 24px;
  background:
    radial-gradient(circle at 50% 35%, rgba(116, 70, 138, 0.24), transparent 42%),
    var(--bg);
}

.maintenance-card {
  display: grid;
  gap: 16px;
  width: min(560px, 100%);
  padding: clamp(24px, 5vw, 52px);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--panel);
  box-shadow: var(--shadow);
  text-align: center;
}

.maintenance-card h1,
.maintenance-card p {
  margin: 0;
}

.maintenance-card button {
  justify-self: center;
}
</style>
