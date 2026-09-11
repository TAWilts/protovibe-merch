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
    <header class="sandbox-tutorial-heading">
      <div>
        <strong>{{ t('sandbox.tutorial.title') }}</strong>
        <span>{{ t('sandbox.tutorial.hint') }}</span>
      </div>
      <button class="text-button" type="button" @click="session.setSandboxTutorial(false)">
        {{ t('sandbox.tutorial.hide') }}
      </button>
    </header>
    <ol class="sandbox-tutorial-tasks">
      <li v-for="step in steps" :key="step.key" :class="{ done: state?.[step.key] }">
        <RouterLink :to="{ name: step.route }">
          <span class="sandbox-task-marker" aria-hidden="true">{{ state?.[step.key] ? '✓' : '○' }}</span>
          <span class="sandbox-task-copy">
            <strong>{{ t(`sandbox.tutorial.${step.key}`) }}</strong>
            <span>{{ t(`sandbox.tutorial.${step.key}Description`) }}</span>
          </span>
        </RouterLink>
      </li>
    </ol>
  </aside>
</template>

<style scoped>
.sandbox-tutorial {
  max-width: 1500px;
  margin: 14px auto 0;
  padding: 16px clamp(16px, 2vw, 24px) 20px;
  display: grid;
  gap: 16px;
  border: 1px solid color-mix(in srgb, #f2b94b 55%, var(--border-default));
  border-radius: var(--radius-panel);
  background: color-mix(in srgb, #f2b94b 8%, var(--surface-panel));
}
.sandbox-tutorial-heading { display: flex; align-items: start; justify-content: space-between; gap: 18px; }
.sandbox-tutorial-heading > div { display: grid; gap: 2px; }
.sandbox-tutorial-heading span { color: var(--muted); font-size: .78rem; }
.sandbox-tutorial-heading button { white-space: nowrap; }
.sandbox-tutorial-tasks { margin: 0; padding: 0; display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; list-style: none; }
.sandbox-tutorial-tasks li a { height: 100%; padding: 13px 14px; display: flex; align-items: flex-start; gap: 10px; border: 1px solid var(--border-subtle); border-radius: var(--radius-control); color: var(--text); background: var(--surface-subtle); text-decoration: none; }
.sandbox-task-marker { width: 1.2rem; flex: 0 0 1.2rem; color: var(--accent-bright); font-weight: 850; line-height: 1.35; text-align: center; }
.sandbox-task-copy { display: grid; gap: 6px; }
.sandbox-task-copy strong { font-size: .9rem; }
.sandbox-task-copy > span { color: var(--text-secondary); font-size: .79rem; line-height: 1.45; }
.sandbox-tutorial-tasks li.done a { border-color: var(--success-border); background: var(--success-soft); }
.sandbox-tutorial-tasks li.done .sandbox-task-marker,
.sandbox-tutorial-tasks li.done .sandbox-task-copy strong { color: var(--success-text); }
@media (max-width: 760px) {
  .sandbox-tutorial-heading { align-items: flex-start; flex-direction: column; gap: 8px; }
  .sandbox-tutorial-tasks { grid-template-columns: 1fr; }
}
</style>
