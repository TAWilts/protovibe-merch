<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const props = withDefaults(defineProps<{
  from: string
  to: string
  exportable?: boolean
}>(), {
  exportable: false,
})

const emit = defineEmits<{
  'update:from': [value: string]
  'update:to': [value: string]
  export: []
}>()

const { locale } = useI18n()
const german = computed(() => locale.value.toLowerCase().startsWith('de'))
const labels = computed(() => german.value
  ? { from: 'Von', to: 'Bis', clear: 'Zeitraum l\u00f6schen', exportCsv: 'CSV exportieren' }
  : { from: 'From', to: 'To', clear: 'Zeitraum l\u00f6schen', exportCsv: 'Export CSV' })

function valueOf(event: Event) {
  return (event.target as HTMLInputElement).value
}

function clear() {
  emit('update:from', '')
  emit('update:to', '')
}
</script>

<template>
  <div class="date-range-filter">
    <label>
      <span>{{ labels.from }}</span>
      <input
        type="date"
        :value="props.from"
        :max="props.to || undefined"
        @input="emit('update:from', valueOf($event))"
      />
    </label>
    <label>
      <span>{{ labels.to }}</span>
      <input
        type="date"
        :value="props.to"
        :min="props.from || undefined"
        @input="emit('update:to', valueOf($event))"
      />
    </label>
    <button
      v-if="props.from || props.to"
      class="compact-button"
      type="button"
      @click="clear"
    >
      {{ labels.clear }}
    </button>
    <button
      v-if="props.exportable"
      class="secondary-button"
      type="button"
      @click="emit('export')"
    >
      {{ labels.exportCsv }}
    </button>
  </div>
</template>

<style scoped>
.date-range-filter {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: flex-end;
}

.date-range-filter label {
  display: grid;
  gap: 4px;
}

.date-range-filter label span {
  color: var(--muted);
  font-size: 0.74rem;
  font-weight: 650;
}

.date-range-filter input {
  width: 9.6rem;
  max-width: 100%;
}

@media (max-width: 600px) {
  .date-range-filter {
    width: 100%;
  }

  .date-range-filter label {
    flex: 1 1 8.5rem;
  }

  .date-range-filter input {
    width: 100%;
  }
}
</style>