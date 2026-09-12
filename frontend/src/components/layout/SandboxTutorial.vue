<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { useSessionStore } from '@/stores/session'
import {
  nextSandboxTutorialStep,
  sandboxExplorationSteps,
  sandboxTutorialComplete,
  sandboxTutorialSteps,
  type SandboxExplorationStep,
  type SandboxTutorialStep,
} from '@/utils/sandboxTutorial'

type DisplayStep = SandboxTutorialStep | SandboxExplorationStep

const session = useSessionStore()
const { t } = useI18n()
const previewedKey = ref<string | null>(null)
const selectedKey = ref<string | null>(null)

const sandbox = computed(() => session.identity?.sandbox)
const currentStep = computed(() => nextSandboxTutorialStep(sandbox.value))
const optionalPhase = computed(
  () => Boolean(sandbox.value?.tutorial_visible) && sandboxTutorialComplete(sandbox.value),
)
const displaySteps = computed<readonly DisplayStep[]>(() =>
  optionalPhase.value ? sandboxExplorationSteps : sandboxTutorialSteps,
)
const requestedHelpKey = computed(
  () =>
    previewedKey.value
    ?? selectedKey.value
    ?? currentStep.value?.key
    ?? sandboxExplorationSteps[0].key,
)
const helpStep = computed<DisplayStep | null>(() => {
  const steps = displaySteps.value
  return steps.find((step) => step.key === requestedHelpKey.value) ?? steps[0] ?? null
})
const helpTarget = computed(() => {
  const step = helpStep.value
  if (!step?.route) return null
  if (!optionalPhase.value && step.key !== currentStep.value?.key) return null
  return { name: step.route }
})

function stepState(step: DisplayStep): 'done' | 'current' | 'upcoming' | 'optional' {
  if (optionalPhase.value) return 'optional'
  if (sandbox.value?.tutorial_state[step.key as SandboxTutorialStep['key']] === true) return 'done'
  if (step.key === currentStep.value?.key) return 'current'
  return 'upcoming'
}

function selectStep(key: string) {
  selectedKey.value = selectedKey.value === key ? null : key
}
</script>

<template>
  <aside v-if="sandbox?.tutorial_visible" class="sandbox-tutorial">
    <header class="sandbox-tutorial-heading">
      <div>
        <strong>
          {{ t(optionalPhase ? 'sandbox.tutorial.optionalTitle' : 'sandbox.tutorial.title') }}
        </strong>
        <span>
          {{ t(optionalPhase ? 'sandbox.tutorial.optionalHint' : 'sandbox.tutorial.hint') }}
        </span>
      </div>
      <button class="text-button" type="button" @click="session.setSandboxTutorial(false)">
        {{ t(optionalPhase ? 'sandbox.tutorial.hide' : 'sandbox.tutorial.skip') }}
      </button>
    </header>

    <ol
      class="sandbox-tutorial-timeline"
      :class="{ 'is-optional': optionalPhase }"
      :aria-label="t(optionalPhase ? 'sandbox.tutorial.optionalTimeline' : 'sandbox.tutorial.timeline')"
    >
      <li
        v-for="step in displaySteps"
        :key="step.key"
        class="sandbox-timeline-item"
        :class="[`is-${stepState(step)}`, { 'shows-help': helpStep?.key === step.key }]"
      >
        <button
          type="button"
          :aria-expanded="helpStep?.key === step.key"
          aria-controls="sandbox-tutorial-help"
          @mouseenter="previewedKey = step.key"
          @mouseleave="previewedKey = null"
          @focus="previewedKey = step.key"
          @blur="previewedKey = null"
          @click="selectStep(step.key)"
        >
          <span class="sandbox-timeline-marker" aria-hidden="true">
            {{ stepState(step) === 'done' ? '✓' : '' }}
          </span>
          <span class="sandbox-timeline-label">{{ t(`sandbox.tutorial.${step.key}`) }}</span>
          <span class="visually-hidden">
            {{ t(`sandbox.tutorial.state.${stepState(step)}`) }}
          </span>
        </button>
      </li>
    </ol>

    <section
      v-if="helpStep"
      id="sandbox-tutorial-help"
      class="sandbox-tutorial-help"
      aria-live="polite"
    >
      <div>
        <strong>{{ t(`sandbox.tutorial.${helpStep.key}`) }}</strong>
        <p>{{ t(`sandbox.tutorial.${helpStep.key}Description`) }}</p>
      </div>
      <RouterLink v-if="helpTarget" class="button secondary" :to="helpTarget">
        {{ t('sandbox.tutorial.openStep') }}
      </RouterLink>
    </section>
  </aside>
</template>

<style scoped>
.sandbox-tutorial {
  max-width: 1500px;
  margin: 14px auto 0;
  padding: 16px clamp(16px, 2vw, 24px) 20px;
  display: grid;
  gap: 18px;
  border: 1px solid color-mix(in srgb, #f2b94b 55%, var(--border-default));
  border-radius: var(--radius-panel);
  background: color-mix(in srgb, #f2b94b 8%, var(--surface-panel));
}

.sandbox-tutorial-heading {
  display: flex;
  align-items: start;
  justify-content: space-between;
  gap: 18px;
}

.sandbox-tutorial-heading > div {
  display: grid;
  gap: 2px;
}

.sandbox-tutorial-heading span {
  color: var(--muted);
  font-size: .78rem;
}

.sandbox-tutorial-heading button {
  white-space: nowrap;
}

.sandbox-tutorial-timeline {
  margin: 0;
  padding: 0;
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  list-style: none;
}

.sandbox-tutorial-timeline.is-optional {
  grid-template-columns: repeat(5, minmax(0, 1fr));
}

.sandbox-timeline-item {
  min-width: 0;
  position: relative;
}

.sandbox-timeline-item:not(:last-child)::after {
  content: '';
  height: 2px;
  position: absolute;
  z-index: 0;
  top: 16px;
  left: 50%;
  right: -50%;
  background: var(--border-default);
}

.sandbox-timeline-item.is-done:not(:last-child)::after,
.sandbox-tutorial-timeline.is-optional .sandbox-timeline-item:not(:last-child)::after {
  background: color-mix(in srgb, #f2b94b 68%, var(--border-default));
}

.sandbox-timeline-item > button {
  width: 100%;
  min-height: 68px;
  padding: 0 8px;
  display: grid;
  position: relative;
  z-index: 1;
  justify-items: center;
  align-content: start;
  gap: 8px;
  border: 0;
  color: var(--text-secondary);
  background: transparent;
  font: inherit;
  cursor: pointer;
}

.sandbox-timeline-marker {
  width: 34px;
  height: 34px;
  display: grid;
  place-items: center;
  border: 2px solid var(--border-default);
  border-radius: 50%;
  color: var(--surface-panel);
  background: var(--surface-panel);
  font-size: .82rem;
  font-weight: 900;
}

.sandbox-timeline-label {
  max-width: 13rem;
  font-size: .78rem;
  font-weight: 750;
  line-height: 1.25;
  text-align: center;
}

.sandbox-timeline-item.is-done .sandbox-timeline-marker {
  border-color: #f2b94b;
  background: #f2b94b;
}

.sandbox-timeline-item.is-current .sandbox-timeline-marker,
.sandbox-timeline-item.is-optional .sandbox-timeline-marker,
.sandbox-timeline-item.shows-help .sandbox-timeline-marker {
  border-color: #f2b94b;
  box-shadow: 0 0 0 5px color-mix(in srgb, #f2b94b 16%, transparent);
}

.sandbox-timeline-item.is-current .sandbox-timeline-marker,
.sandbox-timeline-item.shows-help .sandbox-timeline-marker {
  background: color-mix(in srgb, #f2b94b 24%, var(--surface-panel));
}

.sandbox-timeline-item.is-done .sandbox-timeline-label,
.sandbox-timeline-item.is-current .sandbox-timeline-label,
.sandbox-timeline-item.is-optional .sandbox-timeline-label,
.sandbox-timeline-item.shows-help .sandbox-timeline-label {
  color: var(--text);
}

.sandbox-timeline-item.is-upcoming {
  opacity: .62;
}

.sandbox-timeline-item > button:hover .sandbox-timeline-label {
  color: var(--text);
}

.sandbox-timeline-item > button:focus-visible {
  border-radius: var(--radius-control);
  outline: 2px solid var(--accent-bright);
  outline-offset: 3px;
}

.sandbox-tutorial-help {
  min-height: 94px;
  padding: 14px 16px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 18px;
  border: 1px solid color-mix(in srgb, #f2b94b 42%, var(--border-subtle));
  border-radius: var(--radius-control);
  background: var(--surface-subtle);
}

.sandbox-tutorial-help > div {
  display: grid;
  gap: 5px;
}

.sandbox-tutorial-help strong {
  font-size: .88rem;
}

.sandbox-tutorial-help p {
  max-width: 75rem;
  margin: 0;
  color: var(--text-secondary);
  font-size: .79rem;
  line-height: 1.5;
}

.sandbox-tutorial-help a {
  flex: 0 0 auto;
  white-space: nowrap;
}

@media (max-width: 760px) {
  .sandbox-tutorial-heading {
    align-items: flex-start;
    flex-direction: column;
    gap: 8px;
  }

  .sandbox-tutorial-timeline,
  .sandbox-tutorial-timeline.is-optional {
    grid-template-columns: 1fr;
    gap: 0;
  }

  .sandbox-timeline-item:not(:last-child)::after {
    width: 2px;
    height: auto;
    top: 33px;
    right: auto;
    bottom: -1px;
    left: 16px;
  }

  .sandbox-timeline-item > button {
    min-height: 54px;
    padding: 7px 4px;
    grid-template-columns: 34px minmax(0, 1fr);
    align-items: center;
    justify-items: start;
    gap: 12px;
  }

  .sandbox-timeline-label {
    max-width: none;
    text-align: left;
  }

  .sandbox-tutorial-help {
    min-height: 0;
    align-items: stretch;
    flex-direction: column;
    gap: 12px;
  }

  .sandbox-tutorial-help a {
    width: 100%;
    justify-content: center;
  }
}
</style>
