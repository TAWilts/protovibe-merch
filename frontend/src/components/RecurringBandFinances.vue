<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { reportsApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { RecurringBandTransaction } from '@/api/types'
import { parseAmount, useMoney } from '@/composables/useMoney'
import { useFlashStore } from '@/stores/flash'

defineProps<{ suggestedCategories: string[] }>()
const emit = defineEmits<{ changed: [] }>()

const { t } = useI18n()
const { format } = useMoney()
const flash = useFlashStore()

const rules = ref<RecurringBandTransaction[]>([])
const busy = ref(false)
const form = ref({
  transaction_type: 'expense' as 'income' | 'expense',
  start_on: new Date().toISOString().slice(0, 10),
  category: '',
  description: '',
  amount: '',
  interval_value: 1,
  interval_unit: 'month' as 'day' | 'week' | 'month' | 'year',
})

onMounted(load)

function report(error: unknown) {
  flash.error(
    error instanceof ApiError
      ? t(`errors.${error.detailCode ?? 'generic'}`, error.message)
      : t('errors.network'),
  )
}

async function load() {
  try {
    rules.value = (await reportsApi.recurringBandEntries()).recurring
  } catch (error) {
    report(error)
  }
}

async function createRule() {
  const amount = parseAmount(form.value.amount)
  if (
    busy.value ||
    amount === null ||
    amount <= 0 ||
    form.value.interval_value < 1 ||
    !form.value.category.trim() ||
    !form.value.description.trim()
  ) return

  busy.value = true
  try {
    await reportsApi.createRecurringBandEntry({
      transaction_type: form.value.transaction_type,
      start_on: form.value.start_on,
      category: form.value.category.trim(),
      description: form.value.description.trim(),
      amount_cents: amount,
      interval_value: Math.trunc(form.value.interval_value),
      interval_unit: form.value.interval_unit,
    })
    form.value.description = ''
    form.value.amount = ''
    flash.success(t('bandFinances.recurring.saved'))
    await load()
    emit('changed')
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

async function setActive(rule: RecurringBandTransaction, active: boolean) {
  try {
    await reportsApi.setRecurringBandEntryActive(rule.id, active)
    await load()
    emit('changed')
  } catch (error) {
    report(error)
  }
}

async function deleteRule(rule: RecurringBandTransaction) {
  if (!window.confirm(t('bandFinances.recurring.deleteConfirm'))) return
  try {
    await reportsApi.deleteRecurringBandEntry(rule.id)
    flash.success(t('bandFinances.recurring.deleted'))
    await load()
    emit('changed')
  } catch (error) {
    report(error)
  }
}
</script>

<template>
  <section class="table-section">
    <div class="section-heading">
      <div>
        <h2>{{ t('bandFinances.recurring.title') }}</h2>
        <p>{{ t('bandFinances.recurring.hint') }}</p>
      </div>
    </div>

    <form class="stack-form" @submit.prevent="createRule">
      <div class="field-grid two-columns">
        <label>
          {{ t('bandFinances.type') }}
          <select v-model="form.transaction_type">
            <option value="income">{{ t('bandFinances.income') }}</option>
            <option value="expense">{{ t('bandFinances.expense') }}</option>
          </select>
        </label>
        <label>
          {{ t('bandFinances.recurring.firstDate') }}
          <input v-model="form.start_on" type="date" required />
        </label>
      </div>

      <div class="field-grid two-columns">
        <label>
          {{ t('bandFinances.category') }}
          <input v-model="form.category" list="recurring-band-categories" required />
          <datalist id="recurring-band-categories">
            <option v-for="entry in suggestedCategories" :key="entry" :value="entry" />
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

      <div class="recurrence-row">
        <span>{{ t('bandFinances.recurring.every') }}</span>
        <input v-model.number="form.interval_value" type="number" min="1" step="1" required />
        <select v-model="form.interval_unit">
          <option value="day">{{ t('bandFinances.recurring.units.day') }}</option>
          <option value="week">{{ t('bandFinances.recurring.units.week') }}</option>
          <option value="month">{{ t('bandFinances.recurring.units.month') }}</option>
          <option value="year">{{ t('bandFinances.recurring.units.year') }}</option>
        </select>
      </div>

      <button class="primary-button" type="submit" :disabled="busy">
        {{ t('bandFinances.recurring.create') }}
      </button>
    </form>

    <div v-if="rules.length" class="table-scroll recurring-list">
      <table>
        <thead>
          <tr>
            <th>{{ t('bandFinances.description') }}</th>
            <th>{{ t('bandFinances.recurring.interval') }}</th>
            <th>{{ t('bandFinances.recurring.next') }}</th>
            <th class="numeric">{{ t('bandFinances.amount') }}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="rule in rules" :key="rule.id" :class="{ 'cancelled-row': !rule.is_active }">
            <td>
              <strong>{{ rule.description }}</strong>
              <small>{{ rule.category }}</small>
            </td>
            <td>
              {{ t('bandFinances.recurring.intervalLabel', {
                count: rule.interval_value,
                unit: t(`bandFinances.recurring.units.${rule.interval_unit}`),
              }) }}
            </td>
            <td>{{ rule.next_run_on }}</td>
            <td class="numeric" :class="rule.transaction_type">
              {{ rule.transaction_type === 'expense' ? '−' : '+' }}{{ format(rule.amount_cents) }}
            </td>
            <td>
              <div class="recurring-actions">
                <button
                  class="compact-button"
                  :class="rule.is_active ? 'secondary-button' : 'primary-button'"
                  type="button"
                  @click="setActive(rule, !rule.is_active)"
                >
                  {{ rule.is_active
                    ? t('bandFinances.recurring.pause')
                    : t('bandFinances.recurring.resume') }}
                </button>
                <button
                  class="compact-button danger-button"
                  type="button"
                  @click="deleteRule(rule)"
                >
                  {{ t('bandFinances.recurring.delete') }}
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-else class="muted">{{ t('bandFinances.recurring.empty') }}</p>
  </section>
</template>

<style scoped>
.recurrence-row {
  display: grid;
  grid-template-columns: max-content minmax(80px, 120px) minmax(140px, 220px);
  gap: 10px;
  align-items: center;
}

.recurring-list {
  margin-top: 22px;
}

.recurring-list td:first-child {
  display: grid;
  gap: 2px;
}

.recurring-list small {
  color: var(--muted);
}

.recurring-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

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

@media (max-width: 640px) {
  .recurrence-row {
    grid-template-columns: 1fr;
  }
}
</style>
