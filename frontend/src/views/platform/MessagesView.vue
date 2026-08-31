<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { platformApi } from '@/api/endpoints'
import type { SupportMessage } from '@/api/types'
import { useFlashStore } from '@/stores/flash'

/** The cross-band support inbox. */
const { t, d } = useI18n()
const flash = useFlashStore()

const messages = ref<SupportMessage[]>([])
const loading = ref(true)
const openOnly = ref(true)

const visible = computed(() => messages.value)

onMounted(load)

async function load() {
  loading.value = true
  try {
    messages.value = (await platformApi.messages(openOnly.value)).messages
  } catch {
    flash.error(t('errors.generic'))
  } finally {
    loading.value = false
  }
}

async function resolve(message: SupportMessage, resolved: boolean) {
  try {
    await platformApi.resolveMessage(message.id, resolved)
    await load()
  } catch {
    flash.error(t('errors.generic'))
  }
}
</script>

<template>
  <main class="page-shell">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('platform.eyebrow') }}</p>
        <h1>{{ t('platform.messages.title') }}</h1>
      </div>
      <label class="checkbox-row">
        <input v-model="openOnly" type="checkbox" @change="load" />
        <span>{{ t('platform.messages.openOnly') }}</span>
      </label>
    </div>

    <section class="table-section">
      <p v-if="loading" class="muted">{{ t('common.loading') }}</p>
      <p v-else-if="!visible.length" class="muted">{{ t('platform.messages.empty') }}</p>

      <article v-for="message in visible" :key="message.id" class="message-card">
        <div class="message-head">
          <div>
            <p class="eyebrow">
              {{ message.band_name || '—' }} ·
              {{ t(`platform.messages.types.${message.message_type}`) }}
            </p>
            <h2>{{ message.subject }}</h2>
            <p class="muted">
              {{ message.sender_username }}
              <template v-if="message.sender_email"> · {{ message.sender_email }}</template>
              · {{ d(new Date(message.created_at), 'short') }}
            </p>
          </div>
          <button
            class="compact-button"
            type="button"
            @click="resolve(message, !message.is_resolved)"
          >
            {{ message.is_resolved ? t('platform.messages.reopen') : t('platform.messages.resolve') }}
          </button>
        </div>
        <p class="message-body">{{ message.body }}</p>
      </article>
    </section>
  </main>
</template>

<style scoped>
.message-card {
  padding: 16px 0;
  border-bottom: 1px solid var(--border);
}

.message-head {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 18px;
}

.message-head h2 {
  margin: 2px 0 4px;
  font-size: 1.05rem;
}

.message-body {
  margin: 10px 0 0;
  white-space: pre-wrap;
}

.checkbox-row {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 8px;
}

.checkbox-row input[type='checkbox'] {
  width: 1.05rem;
  height: 1.05rem;
  accent-color: var(--accent);
}
</style>
