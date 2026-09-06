<script setup lang="ts">
import { RouterView } from 'vue-router'

import TelemetryConsentDialog from '@/components/TelemetryConsentDialog.vue'
</script>

<template>
  <RouterView />
  <TelemetryConsentDialog />
</template>
