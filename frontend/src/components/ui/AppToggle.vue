<script setup lang="ts">
import { useI18n } from 'vue-i18n'

defineProps<{
  label: string
  disabled?: boolean
}>()

const model = defineModel<boolean>({ required: true })
const { t } = useI18n()

function toggle() {
  model.value = !model.value
}
</script>

<template>
  <button
    class="app-toggle"
    :class="{ 'is-on': model }"
    type="button"
    :aria-pressed="model"
    :disabled="disabled"
    @click="toggle"
    @keydown.space.prevent="toggle"
  >
    <span class="app-toggle-label">{{ label }}</span>
    <span class="app-toggle-state" aria-hidden="true">{{ model ? t('common.on') : t('common.off') }}</span>
  </button>
</template>

<style scoped>
.app-toggle {
  display: inline-flex;
  min-height: 44px;
  max-width: 100%;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 7px 8px 7px 12px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-control);
  color: var(--text-primary);
  background: var(--surface-raised);
  font: inherit;
  font-size: .84rem;
  font-weight: 680;
  text-align: left;
  cursor: pointer;
  transition: border-color .15s, background .15s, transform .12s;
}

.app-toggle:hover:not(:disabled) {
  border-color: var(--accent);
  background: var(--surface-hover);
}

.app-toggle.is-on {
  border-color: var(--accent-hover);
  background: var(--surface-selected);
}

.app-toggle:disabled {
  opacity: .48;
  cursor: not-allowed;
}

.app-toggle-label {
  min-width: 0;
  line-height: 1.3;
}

.app-toggle-state {
  min-width: 3.4em;
  padding: 4px 7px;
  border: 1px solid var(--border-strong);
  border-radius: 999px;
  color: var(--text-secondary);
  background: var(--surface-inset);
  font-size: .68rem;
  font-weight: 800;
  letter-spacing: .08em;
  text-align: center;
}

.is-on .app-toggle-state {
  border-color: var(--accent-hover);
  color: var(--on-accent);
  background: var(--accent);
}

@media (max-width: 700px) {
  .app-toggle {
    width: 100%;
  }
}

@media (prefers-reduced-motion: reduce) {
  .app-toggle { transition-duration: .01ms; }
}
</style>
