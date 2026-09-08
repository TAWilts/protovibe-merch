<script setup lang="ts">
withDefaults(defineProps<{
  height?: string
}>(), {
  height: '1rem',
})
</script>

<template>
  <span class="skeleton-block" :style="{ height }" aria-hidden="true"></span>
</template>
