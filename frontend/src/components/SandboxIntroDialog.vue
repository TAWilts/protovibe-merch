<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

import AppDialog from '@/components/ui/AppDialog.vue'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()
const flash = useFlashStore()
const router = useRouter()
const { t } = useI18n()
const busy = ref(false)

async function dismiss() {
  if (busy.value) return
  busy.value = true
  try {
    await session.markSandboxIntroSeen()
  } catch {
    flash.error(t('errors.network'))
  } finally {
    busy.value = false
  }
}

async function start() {
  if (busy.value) return
  busy.value = true
  try {
    await session.markSandboxIntroSeen()
    await session.enterSandbox()
    await router.push({ name: 'sandbox-sales' })
  } catch {
    flash.error(t('errors.network'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AppDialog
    v-if="session.isAuthenticated && !session.isSandbox && session.identity?.sandbox_available && session.user?.telemetry_decided !== false && session.user?.sandbox_intro_seen === false"
    class="sandbox-intro-dialog"
    :label="t('sandbox.invite.title')"
    :dismissible="false"
  >
    <p class="eyebrow">{{ t('sandbox.badge') }}</p>
    <h2>{{ t('sandbox.invite.title') }}</h2>
    <p>{{ t('sandbox.invite.text') }}</p>
    <ul>
      <li>{{ t('sandbox.invite.isolated') }}</li>
      <li>{{ t('sandbox.invite.expires') }}</li>
      <li>{{ t('sandbox.invite.roles') }}</li>
    </ul>
    <div class="button-group">
      <button class="primary-button" type="button" :disabled="busy" @click="start">{{ t('sandbox.start') }}</button>
      <button class="secondary-button" type="button" :disabled="busy" @click="dismiss">{{ t('sandbox.invite.skip') }}</button>
    </div>
  </AppDialog>
</template>

<style scoped>
.sandbox-intro-dialog { width: min(580px, calc(100vw - 36px)); padding: 30px; border: 1px solid #f2b94b; border-radius: var(--radius-overlay); color: var(--text); background: var(--surface-raised); box-shadow: var(--shadow-overlay); }
.sandbox-intro-dialog::backdrop { background: rgb(var(--shadow-color) / .72); backdrop-filter: blur(8px); }
.sandbox-intro-dialog h2 { margin-top: 5px; }
.sandbox-intro-dialog li { margin: 8px 0; }
.sandbox-intro-dialog .button-group { margin-top: 24px; }
</style>
