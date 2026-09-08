<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'

defineProps<{
  username: string
  roleLabel: string
}>()

const emit = defineEmits<{ logout: [] }>()
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
      <RouterLink :to="{ name: 'profile' }" @click="close">{{ t('accountMenu.profile') }}</RouterLink>
      <button type="button" @click="emit('logout')">{{ t('common.logout') }}</button>
    </div>
  </details>
</template>
