<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'

defineProps<{
  username: string
  roleLabel: string
  sandbox?: boolean
  sandboxAvailable?: boolean
}>()

const emit = defineEmits<{ logout: []; sandbox: []; tutorial: []; discardSandbox: [] }>()
const { t } = useI18n()
const menu = ref<HTMLDetailsElement | null>(null)

function close() {
  if (menu.value) menu.value.open = false
}

function onDocumentPointerDown(event: PointerEvent) {
  if (menu.value?.open && !menu.value.contains(event.target as Node)) close()
}

function onKeydown(event: KeyboardEvent) {
  if (event.key !== 'Escape' || !menu.value?.open) return
  event.preventDefault()
  close()
  menu.value.querySelector<HTMLElement>('summary')?.focus()
}

onMounted(() => document.addEventListener('pointerdown', onDocumentPointerDown))
onUnmounted(() => document.removeEventListener('pointerdown', onDocumentPointerDown))
</script>

<template>
  <details ref="menu" class="account-menu" @keydown="onKeydown">
    <summary :aria-label="t('accountMenu.open')">
      <span class="account-avatar" aria-hidden="true">{{ username.slice(0, 1).toUpperCase() }}</span>
      <span class="account-summary-copy">
        <strong>{{ username }}</strong>
        <small>{{ roleLabel }}</small>
      </span>
      <span class="account-chevron" aria-hidden="true">▾</span>
    </summary>
    <div class="account-popover">
      <div class="account-popover-heading">
        <strong>{{ username }}</strong>
        <span>{{ roleLabel }}</span>
      </div>
      <template v-if="sandbox">
        <button type="button" @click="emit('tutorial'); close()">{{ t('sandbox.tutorial.restart') }}</button>
        <button type="button" @click="emit('discardSandbox'); close()">{{ t('sandbox.discard') }}</button>
      </template>
      <template v-else>
        <RouterLink :to="{ name: 'profile' }" @click="close">{{ t('accountMenu.profile') }}</RouterLink>
        <button v-if="sandboxAvailable" type="button" @click="emit('sandbox'); close()">{{ t('sandbox.start') }}</button>
        <button type="button" @click="emit('logout')">{{ t('common.logout') }}</button>
      </template>
    </div>
  </details>
</template>
