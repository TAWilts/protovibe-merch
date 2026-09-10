<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

import type { Role } from '@/api/types'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()
const flash = useFlashStore()
const router = useRouter()
const { t } = useI18n()
const now = ref(Date.now())
const busy = ref(false)
const timer = window.setInterval(() => { now.value = Date.now() }, 30_000)
onBeforeUnmount(() => window.clearInterval(timer))

const remaining = computed(() => {
  const expires = Date.parse(session.identity?.sandbox?.expires_at ?? '')
  if (!Number.isFinite(expires)) return '–'
  const minutes = Math.max(0, Math.ceil((expires - now.value) / 60_000))
  if (minutes < 60) return t('sandbox.minutes', { count: minutes })
  return t('sandbox.hours', { count: Math.ceil(minutes / 60) })
})

const role = computed({
  get: () => session.user?.role ?? 'band_admin',
  set: (value: Role) => void changeRole(value),
})

async function changeRole(value: Role) {
  if (busy.value || !['seller', 'member', 'manager', 'band_admin'].includes(value)) return
  busy.value = true
  try {
    await session.setSandboxRole(value as 'seller' | 'member' | 'manager' | 'band_admin')
    await router.replace({ name: 'sandbox-sales' })
  } catch {
    flash.error(t('errors.network'))
  } finally {
    busy.value = false
  }
}

async function reset() {
  if (busy.value || !window.confirm(t('sandbox.resetConfirm'))) return
  busy.value = true
  try {
    await session.resetSandbox()
    await router.replace({ name: 'sandbox-sales' })
    flash.success(t('sandbox.resetDone'))
  } catch {
    flash.error(t('errors.network'))
  } finally {
    busy.value = false
  }
}

async function leave() {
  if (busy.value) return
  busy.value = true
  try {
    const restored = await session.leaveSandbox()
    await router.replace(restored
      ? { name: session.capabilities?.is_platform_staff ? 'platform-dashboard' : 'sales' }
      : { name: 'landing' })
  } catch {
    flash.error(t('errors.network'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <aside class="sandbox-banner" aria-label="Sandbox">
    <div class="sandbox-message">
      <strong>{{ t('sandbox.banner') }}</strong>
      <span>{{ t('sandbox.remaining', { time: remaining }) }}</span>
    </div>
    <label>
      <span>{{ t('sandbox.role') }}</span>
      <select v-model="role" :disabled="busy">
        <option value="seller">{{ t('sandbox.roles.seller') }}</option>
        <option value="member">{{ t('sandbox.roles.member') }}</option>
        <option value="manager">{{ t('sandbox.roles.manager') }}</option>
        <option value="band_admin">{{ t('sandbox.roles.band_admin') }}</option>
      </select>
    </label>
    <button class="secondary-button" type="button" :disabled="busy" @click="reset">
      {{ t('sandbox.reset') }}
    </button>
    <button class="primary-button" type="button" :disabled="busy" @click="leave">
      {{ t('sandbox.leave') }}
    </button>
  </aside>
</template>

<style scoped>
.sandbox-banner {
  position: sticky;
  z-index: 70;
  top: 0;
  min-height: 54px;
  padding: 8px clamp(12px, 3vw, 28px);
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  border-bottom: 2px solid #f2b94b;
  color: #241700;
  background: #f2b94b;
  box-shadow: var(--shadow-sm);
}

.sandbox-message { margin-right: auto; display: grid; line-height: 1.2; }
.sandbox-message strong { letter-spacing: .035em; }
.sandbox-message span { font-size: .78rem; }
.sandbox-banner label { display: flex; align-items: center; gap: 7px; font-size: .78rem; font-weight: 750; }
.sandbox-banner select { min-height: 36px; border-color: #5f450b; color: #241700; background: #ffe2a1; }
.sandbox-banner button { min-height: 36px; }
.sandbox-banner .primary-button { color: #fff; background: #382706; }
.sandbox-banner .secondary-button { border-color: #5f450b; color: #382706; background: transparent; }

@media (max-width: 760px) {
  .sandbox-banner { position: relative; flex-wrap: wrap; justify-content: flex-start; }
  .sandbox-message { width: 100%; }
  .sandbox-banner label { margin-right: auto; }
}
</style>
