<script setup lang="ts">
import { useFlashStore } from '@/stores/flash'

const flash = useFlashStore()
</script>

<template>
  <section v-if="flash.messages.length" class="flash-stack" aria-live="polite">
    <div
      v-for="message in flash.messages"
      :key="message.id"
      class="flash"
      :class="message.kind"
      @click="flash.dismiss(message.id)"
    >
      {{ message.text }}
    </div>
  </section>
</template>
