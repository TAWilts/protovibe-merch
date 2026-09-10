<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { useSessionStore } from '@/stores/session'

const session = useSessionStore()
const { t } = useI18n()
const state = computed(() => session.identity?.sandbox?.tutorial_state)
const steps = computed(() => [
  { key: 'catalogue', route: 'sandbox-articles' },
  { key: 'purchase', route: 'sandbox-purchases' },
  { key: 'sale', route: 'sandbox-sales' },
  { key: 'balance', route: 'sandbox-balances' },
] as const)
</script>

<template>
  <aside v-if="session.identity?.sandbox?.tutorial_visible" class="sandbox-tutorial">
    <div>
      <strong>{{ t('sandbox.tutorial.title') }}</strong>
      <span>{{ t('sandbox.tutorial.hint') }}</span>
    </div>
    <ol>
      <li v-for="step in steps" :key="step.key" :class="{ done: state?.[step.key] }">
        <RouterLink :to="{ name: step.route }">
          <span aria-hidden="true">{{ state?.[step.key] ? '✓' : '○' }}</span>
          {{ t(`sandbox.tutorial.${step.key}`) }}
        </RouterLink>
      </li>
    </ol>
    <button class="text-button" type="button" @click="session.setSandboxTutorial(false)">
      {{ t('sandbox.tutorial.hide') }}
    </button>
  </aside>
</template>

<style scoped>
.sandbox-tutorial {
  max-width: 1500px;
  margin: 14px auto 0;
  padding: 12px clamp(16px, 2vw, 24px);
  display: flex;
  align-items: center;
  gap: 18px;
  border: 1px solid color-mix(in srgb, #f2b94b 55%, var(--border-default));
  border-radius: var(--radius-panel);
  background: color-mix(in srgb, #f2b94b 8%, var(--surface-panel));
}
.sandbox-tutorial > div { display: grid; min-width: 180px; }
.sandbox-tutorial > div span { color: var(--muted); font-size: .78rem; }
.sandbox-tutorial ol { margin: 0; padding: 0; display: flex; flex-wrap: wrap; gap: 8px; list-style: none; }
.sandbox-tutorial li a { padding: 6px 9px; display: flex; gap: 6px; border-radius: 999px; color: var(--text); background: var(--surface-subtle); text-decoration: none; font-size: .8rem; }
.sandbox-tutorial li.done a { color: var(--success-text); background: var(--success-soft); }
.sandbox-tutorial > button { margin-left: auto; white-space: nowrap; }
@media (max-width: 900px) { .sandbox-tutorial { align-items: flex-start; flex-direction: column; gap: 10px; } .sandbox-tutorial > button { margin-left: 0; } }
</style>
