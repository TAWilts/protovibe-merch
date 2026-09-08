<script setup lang="ts">
import { RouterView } from 'vue-router'
import { useI18n } from 'vue-i18n'

import TelemetryConsentDialog from '@/components/TelemetryConsentDialog.vue'

const { t } = useI18n()
</script>

<template>
  <a class="skip-link" href="#main-content">{{ t('common.skipToContent') }}</a>
  <RouterView />
  <TelemetryConsentDialog />
</template>
