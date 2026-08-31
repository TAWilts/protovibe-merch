<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { platformApi } from '@/api/endpoints'
import type { AuditEntry, BandSummary } from '@/api/types'
import { useFlashStore } from '@/stores/flash'

/**
 * The cross-band activity trail.
 *
 * The grant filter is the important one: it answers "what exactly did support
 * do while they were inside our data", which is the question a band will ask.
 */
const { t, d } = useI18n()
const flash = useFlashStore()

const entries = ref<AuditEntry[]>([])
const bands = ref<BandSummary[]>([])
const loading = ref(true)
const filters = ref({ band_id: '', action: '', grant_id: '' })

onMounted(async () => {
  try {
    bands.value = (await platformApi.bands(true)).bands
  } catch {
    // The band names are a convenience; the trail still reads without them.
  }
  await load()
})

async function load() {
  loading.value = true
  try {
    const params: Record<string, string> = {}
    for (const [key, value] of Object.entries(filters.value)) {
      if (value.trim()) params[key] = value.trim()
    }
    entries.value = (await platformApi.audit(params)).entries
  } catch {
    flash.error(t('errors.generic'))
  } finally {
    loading.value = false
  }
}

function details(entry: AuditEntry): string {
  const keys = Object.keys(entry.details ?? {})
  if (!keys.length) return '—'
  return keys.map((key) => `${key}: ${JSON.stringify(entry.details[key])}`).join(', ')
}
</script>

<template>
  <main class="page-shell">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('platform.eyebrow') }}</p>
        <h1>{{ t('platform.audit.title') }}</h1>
        <p class="page-intro">{{ t('platform.audit.intro') }}</p>
      </div>
    </div>

    <section class="table-section">
      <div class="section-heading ledger-heading">
        <div><h2>{{ t('platform.audit.filters') }}</h2></div>
        <div class="ledger-actions">
          <label class="table-filter">
            {{ t('platform.support.band') }}
            <select v-model="filters.band_id" @change="load">
              <option value="">{{ t('platform.audit.allBands') }}</option>
              <option v-for="band in bands" :key="band.id" :value="String(band.id)">
                {{ band.name }}
              </option>
            </select>
          </label>
          <label class="table-filter">
            {{ t('platform.audit.action') }}
            <input v-model="filters.action" type="search" placeholder="sale." @change="load" />
          </label>
          <label class="table-filter">
            {{ t('platform.audit.grant') }}
            <input v-model="filters.grant_id" type="search" inputmode="numeric" @change="load" />
          </label>
        </div>
      </div>

      <p v-if="loading" class="muted">{{ t('common.loading') }}</p>
      <p v-else-if="!entries.length" class="muted">{{ t('platform.audit.empty') }}</p>
      <div v-else class="table-scroll">
        <table>
          <thead>
            <tr>
              <th>{{ t('common.date') }}</th>
              <th>{{ t('platform.support.band') }}</th>
              <th>{{ t('platform.audit.user') }}</th>
              <th>{{ t('platform.audit.action') }}</th>
              <th>{{ t('platform.audit.entity') }}</th>
              <th>{{ t('platform.audit.details') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="entry in entries" :key="entry.id" :class="{ 'support-row': entry.acting_grant_id }">
              <td>{{ d(new Date(entry.created_at), 'short') }}</td>
              <td>{{ entry.band_name || '—' }}</td>
              <td>
                {{ entry.username || '—' }}
                <!-- Anything done under a grant is marked, because that is the
                     line a band cares about most. -->
                <small v-if="entry.acting_grant_id">
                  {{ t('platform.audit.viaGrant', { id: entry.acting_grant_id }) }}
                </small>
              </td>
              <td><code>{{ entry.action }}</code></td>
              <td>{{ entry.entity_type }}<template v-if="entry.entity_id"> #{{ entry.entity_id }}</template></td>
              <td class="details-cell">{{ details(entry) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  </main>
</template>

<style scoped>
.support-row {
  background: color-mix(in srgb, var(--warning) 8%, transparent);
}

td small {
  display: block;
  color: var(--warning);
}

.details-cell {
  max-width: 32rem;
  color: var(--muted);
  font-size: 0.86rem;
  word-break: break-word;
}
</style>
