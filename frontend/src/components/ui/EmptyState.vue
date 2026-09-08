<script setup lang="ts">
withDefaults(defineProps<{
  title: string
  description?: string
  compact?: boolean
  tone?: 'default' | 'error'
}>(), {
  description: '',
  compact: false,
  tone: 'default',
})
</script>

<template>
  <div class="app-empty-state" :class="[`is-${tone}`, { 'is-compact': compact }]">
    <div v-if="$slots.icon" class="app-empty-state-icon" aria-hidden="true"><slot name="icon" /></div>
    <div class="app-empty-state-copy">
      <strong>{{ title }}</strong>
      <p v-if="description">{{ description }}</p>
    </div>
    <div v-if="$slots.default" class="app-empty-state-actions"><slot /></div>
  </div>
</template>
