<script setup lang="ts">
withDefaults(defineProps<{
  label: string
  rows?: number
  columns?: number
}>(), {
  rows: 5,
  columns: 5,
})
</script>

<template>
  <div
    class="table-skeleton"
    :style="{ '--skeleton-columns': columns }"
    role="status"
    aria-live="polite"
  >
    <span class="visually-hidden">{{ label }}</span>
    <div class="table-skeleton-head" aria-hidden="true">
      <span v-for="column in columns" :key="column" class="skeleton-block"></span>
    </div>
    <div v-for="row in rows" :key="row" class="table-skeleton-row" aria-hidden="true">
      <span v-for="column in columns" :key="column" class="skeleton-block"></span>
    </div>
  </div>
</template>
