<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { reportsApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { BandLedger } from '@/api/types'
import { useMoney, parseAmount } from '@/composables/useMoney'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'

/**
 * The band's own ledger, ported from _old/templates/band_finances.html.
 *
 * It stays deliberately separate from the merch books: a gig fee must never
 * change a historic merch balance, and a merch reorder must never look like a
 * band expense. Only the headline figure on the balances page adds them up.
 */
const { t } = useI18n()
const { format } = useMoney()
const flash = useFlashStore()
const session = useSessionStore()

const ledger = ref<BandLedger | null>(null)
const loading = ref(true)
const busy = ref(false)

const canManage = computed(() => session.capabilities?.can_manage_band_finances ?? false)

const form = ref({
  transaction_type: 'income' as 'income' | 'expense',
  transaction_on: new Date().toISOString().slice(0, 10),
  category: '',
  description: '',
  amount: '',
})

const amountCents = computed(() => parseAmount(form.value.amount))
const canSubmit = computed(
  () =>
    !busy.value &&
    !!form.value.category.trim() &&
    !!form.value.description.trim() &&
    amountCents.value !== null &&
    amountCents.value > 0,
)

onMounted(load)

async function load() {
  loading.value = true
  try {
    ledger.value = await reportsApi.bandLedger()
  } catch {
    flash.error(t('errors.generic'))
  } finally {
    loading.value = false
  }
}

function report(error: unknown) {
  flash.error(
    error instanceof ApiError
      ? t(`errors.${error.detailCode ?? 'generic'}`, t('errors.generic'))
      : t('errors.network'),
  )
}

async function submit() {
  if (!canSubmit.value || amountCents.value === null) return
  busy.value = true
  try {
    await reportsApi.createBandEntry({
      transaction_type: form.value.transaction_type,
      transaction_on: form.value.transaction_on,
      category: form.value.category.trim(),
      description: form.value.description.trim(),
      amount_cents: amountCents.value,
    })
    flash.success(t('bandFinances.saved'))
    form.value.description = ''
    form.value.amount = ''
    await load()
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

async function cancelEntry(id: number) {
  try {
    await reportsApi.cancelBandEntry(id)
    flash.success(t('bandFinances.cancelled'))
    await load()
  } catch (error) {
    report(error)
  }
}
</script>

<template>
  <main class="page-shell">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('bandFinances.eyebrow') }}</p>
        <h1>{{ t('bandFinances.title') }}</h1>
      </div>
    </div>

    <p v-if="loading" class="muted">{{ t('common.loading') }}</p>

    <template v-else-if="ledger">
      <section class="metric-grid band-finance-metrics">
        <article class="metric-card">
          <span>{{ t('bandFinances.income') }}</span>
          <strong>{{ format(ledger.income_cents) }}</strong>
        </article>
        <article class="metric-card">
          <span>{{ t('bandFinances.expense') }}</span>
          <strong>{{ format(ledger.expense_cents) }}</strong>
        </article>
        <article class="metric-card">
          <span>{{ t('bandFinances.balance') }}</span>
          <strong>{{ format(ledger.balance_cents) }}</strong>
        </article>
      </section>

      <section v-if="canManage" class="table-section">
        <div class="section-heading">
          <div>
            <h2>{{ t('bandFinances.newEntry') }}</h2>
            <p>{{ t('bandFinances.newEntryHint') }}</p>
          </div>
        </div>
        <form class="stack-form" @submit.prevent="submit">
          <div class="field-grid two-columns">
            <label>
              {{ t('bandFinances.type') }}
              <select v-model="form.transaction_type">
                <option value="income">{{ t('bandFinances.income') }}</option>
                <option value="expense">{{ t('bandFinances.expense') }}</option>
              </select>
            </label>
            <label>
              {{ t('common.date') }}
              <input v-model="form.transaction_on" type="date" />
            </label>
          </div>
          <div class="field-grid two-columns">
            <label>
              {{ t('bandFinances.category') }}
              <input v-model="form.category" list="band-categories" required />
              <datalist id="band-categories">
                <option v-for="entry in ledger.suggested_categories" :key="entry" :value="entry" />
              </datalist>
            </label>
            <label>
              {{ t('bandFinances.amount') }}
              <input v-model="form.amount" inputmode="decimal" placeholder="0,00" required />
            </label>
          </div>
          <label>
            {{ t('bandFinances.description') }}
            <input v-model="form.description" required />
          </label>
          <button class="primary-button" type="submit" :disabled="!canSubmit">
            {{ t('common.save') }}
          </button>
        </form>
      </section>

      <section v-if="ledger.categories.length" class="table-section">
        <div class="section-heading"><div><h2>{{ t('bandFinances.byCategory') }}</h2></div></div>
        <div class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{{ t('bandFinances.category') }}</th>
                <th class="numeric">{{ t('bandFinances.income') }}</th>
                <th class="numeric">{{ t('bandFinances.expense') }}</th>
                <th class="numeric">{{ t('bandFinances.balance') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="entry in ledger.categories" :key="entry.category">
                <td>{{ entry.category }}</td>
                <td class="numeric">{{ format(entry.income_cents) }}</td>
                <td class="numeric">{{ format(entry.expense_cents) }}</td>
                <td class="numeric">{{ format(entry.balance_cents) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="table-section">
        <div class="section-heading ledger-heading">
          <div>
            <h2>{{ t('bandFinances.allEntries') }}</h2>
            <p>{{ t('bandFinances.entryCount', { count: ledger.entries.length }) }}</p>
          </div>
        </div>
        <p v-if="!ledger.entries.length" class="muted">{{ t('bandFinances.empty') }}</p>
        <div v-else class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{{ t('common.date') }}</th>
                <th>{{ t('bandFinances.category') }}</th>
                <th>{{ t('bandFinances.description') }}</th>
                <th class="numeric">{{ t('bandFinances.amount') }}</th>
                <th v-if="canManage"></th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="entry in ledger.entries"
                :key="entry.id"
                :class="{ 'cancelled-row': entry.is_cancelled }"
              >
                <td>{{ entry.transaction_on }}</td>
                <td>{{ entry.category }}</td>
                <td>{{ entry.description }}</td>
                <td class="numeric" :class="entry.transaction_type">
                  {{ entry.transaction_type === 'expense' ? '−' : '+' }}{{ format(entry.amount_cents) }}
                </td>
                <td v-if="canManage">
                  <button
                    v-if="!entry.is_cancelled"
                    class="compact-button danger-button"
                    type="button"
                    @click="cancelEntry(entry.id)"
                  >
                    {{ t('bandFinances.cancel') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>
  </main>
</template>

<style scoped>
.numeric {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.numeric.income {
  color: var(--success);
}

.numeric.expense {
  color: var(--danger);
}
</style>
