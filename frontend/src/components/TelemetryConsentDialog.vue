<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { profileApi } from '@/api/endpoints'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'

const { t } = useI18n()
const session = useSessionStore()
const flash = useFlashStore()
const choice = ref<boolean | null>(null)
const busy = ref(false)

const visible = computed(
  () => session.isAuthenticated && session.user?.telemetry_decided === false,
)

async function save() {
  if (busy.value || choice.value === null) return
  busy.value = true
  try {
    await profileApi.telemetry(choice.value)
    await session.restore(false)
  } catch {
    flash.error(t('errors.network'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div v-if="visible" class="telemetry-backdrop" role="presentation">
    <section
      class="telemetry-dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="telemetry-title"
      aria-describedby="telemetry-description"
    >
      <p class="eyebrow">{{ t('telemetryConsent.eyebrow') }}</p>
      <h2 id="telemetry-title">{{ t('telemetryConsent.title') }}</h2>
      <p id="telemetry-description">{{ t('telemetryConsent.intro') }}</p>

      <div class="telemetry-privacy">
        <strong>{{ t('telemetryConsent.anonymousTitle') }}</strong>
        <p>{{ t('telemetryConsent.anonymousText') }}</p>
      </div>

      <p class="muted">{{ t('telemetryConsent.collects') }}</p>

      <div class="telemetry-choices">
        <label :class="{ selected: choice === true }">
          <input v-model="choice" type="radio" :value="true" />
          <span>
            <strong>{{ t('telemetryConsent.allow') }}</strong>
            <small>{{ t('telemetryConsent.allowHint') }}</small>
          </span>
        </label>
        <label :class="{ selected: choice === false }">
          <input v-model="choice" type="radio" :value="false" />
          <span>
            <strong>{{ t('telemetryConsent.deny') }}</strong>
            <small>{{ t('telemetryConsent.denyHint') }}</small>
          </span>
        </label>
      </div>

      <button
        class="primary-button"
        type="button"
        :disabled="busy || choice === null"
        @click="save"
      >
        {{ t('telemetryConsent.save') }}
      </button>
      <small class="muted">{{ t('telemetryConsent.later') }}</small>
    </section>
  </div>
</template>

<style scoped>
.telemetry-backdrop {
  position: fixed;
  z-index: 10000;
  inset: 0;
  display: grid;
  place-items: center;
  padding: 18px;
  background: rgb(3 7 18 / 72%);
  backdrop-filter: blur(8px);
}

.telemetry-dialog {
  width: min(640px, 100%);
  max-height: calc(100vh - 36px);
  overflow: auto;
  padding: clamp(22px, 4vw, 36px);
  border: 1px solid var(--border);
  border-radius: 18px;
  background: var(--panel);
  box-shadow: 0 24px 80px rgb(0 0 0 / 35%);
}

.telemetry-dialog h2 {
  margin-top: 4px;
}

.telemetry-privacy {
  margin: 18px 0;
  padding: 14px 16px;
  border: 1px solid var(--border);
  border-radius: 12px;
  background: var(--input-bg);
}

.telemetry-privacy p {
  margin-bottom: 0;
}

.telemetry-choices {
  display: grid;
  gap: 10px;
  margin: 20px 0;
}

.telemetry-choices label {
  display: flex;
  gap: 12px;
  padding: 14px;
  border: 1px solid var(--border);
  border-radius: 12px;
  cursor: pointer;
}

.telemetry-choices label.selected {
  border-color: var(--accent);
  background: var(--input-bg);
}

.telemetry-choices input {
  width: 1.15rem;
  height: 1.15rem;
  margin-top: 3px;
  accent-color: var(--accent);
}

.telemetry-choices span {
  display: grid;
  gap: 4px;
}

.telemetry-choices small {
  color: var(--muted);
}

.telemetry-dialog > .muted:last-child {
  display: block;
  margin-top: 10px;
}
</style>
