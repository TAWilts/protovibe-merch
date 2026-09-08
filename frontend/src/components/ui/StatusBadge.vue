<script setup lang="ts">
withDefaults(defineProps<{
  tone?: 'neutral' | 'success' | 'warning' | 'danger'
}>(), {
  tone: 'neutral',
})
</script>

<template>
  <span class="app-status-badge" :data-tone="tone">
    <span class="app-status-marker" aria-hidden="true"></span>
    <slot />
  </span>
</template>

<style scoped>
.app-status-badge {
  width: fit-content;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  border: 1px solid var(--border-default);
  border-radius: 999px;
  color: var(--text-secondary);
  background: var(--surface-subtle);
  font-size: .72rem;
  font-weight: 740;
  line-height: 1.2;
  white-space: nowrap;
}

.app-status-marker {
  width: 6px;
  height: 6px;
  flex: 0 0 auto;
  border-radius: 50%;
  background: currentColor;
}

.app-status-badge[data-tone='success'] { color: var(--success-text); border-color: var(--success-border); background: var(--success-soft); }
.app-status-badge[data-tone='warning'] { color: var(--warning-text); border-color: var(--warning-border); background: var(--warning-soft); }
.app-status-badge[data-tone='danger'] { color: var(--danger-text); border-color: var(--danger-border); background: var(--danger-soft); }
</style>
