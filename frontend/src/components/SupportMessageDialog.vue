<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { supportApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import { useFlashStore } from '@/stores/flash'

const { t } = useI18n()
const flash = useFlashStore()

const dialog = ref<HTMLDialogElement | null>(null)
const messageType = ref<'issue' | 'question'>('issue')
const senderEmail = ref('')
const subject = ref('')
const body = ref('')
const sending = ref(false)

function open() {
  dialog.value?.showModal()
}

function close() {
  if (!sending.value) dialog.value?.close()
}

async function send() {
  if (sending.value) return
  sending.value = true
  try {
    await supportApi.send({
      message_type: messageType.value,
      sender_email: senderEmail.value.trim(),
      subject: subject.value.trim(),
      body: body.value.trim(),
    })
    flash.success(t('supportMessage.sent'))
    subject.value = ''
    body.value = ''
    dialog.value?.close()
  } catch (error) {
    flash.error(
      error instanceof ApiError
        ? t(`errors.${error.detailCode ?? 'generic'}`, t('errors.generic'))
        : t('errors.network'),
    )
  } finally {
    sending.value = false
  }
}
</script>

<template>
  <button
    class="support-message-button"
    type="button"
    :title="t('supportMessage.buttonTitle')"
    :aria-label="t('supportMessage.buttonTitle')"
    @click="open"
  >
    <span aria-hidden="true">✉</span>
  </button>

  <dialog ref="dialog" class="confirmation-dialog support-message-dialog" @cancel.prevent="close">
    <form class="stack-form" @submit.prevent="send">
      <div>
        <p class="eyebrow">{{ t('supportMessage.eyebrow') }}</p>
        <h2>{{ t('supportMessage.title') }}</h2>
        <p class="muted">{{ t('supportMessage.intro') }}</p>
      </div>

      <label>
        {{ t('supportMessage.type') }}
        <select v-model="messageType" required>
          <option value="issue">{{ t('supportMessage.issue') }}</option>
          <option value="question">{{ t('supportMessage.question') }}</option>
        </select>
      </label>
      <label>
        {{ t('supportMessage.email') }}
        <input v-model="senderEmail" type="email" maxlength="254" autocomplete="email" required />
      </label>
      <label>
        {{ t('supportMessage.subject') }}
        <input v-model="subject" maxlength="120" required />
      </label>
      <label>
        {{ t('supportMessage.body') }}
        <textarea v-model="body" rows="7" maxlength="4000" required></textarea>
      </label>

      <div class="dialog-actions">
        <button class="secondary-button" type="button" :disabled="sending" @click="close">
          {{ t('common.cancel') }}
        </button>
        <button class="primary-button" type="submit" :disabled="sending">
          {{ sending ? t('supportMessage.sending') : t('supportMessage.send') }}
        </button>
      </div>
    </form>
  </dialog>
</template>

<style scoped>
.support-message-button {
  display: grid;
  width: 34px;
  height: 34px;
  place-items: center;
  padding: 0;
  border: 1px solid var(--border);
  border-radius: 999px;
  color: var(--text);
  background: var(--panel-raised);
  cursor: pointer;
  font-size: 1rem;
}

.support-message-button:hover {
  border-color: var(--accent);
  color: var(--accent-bright);
}

.support-message-dialog {
  width: min(560px, calc(100vw - 32px));
}
</style>
