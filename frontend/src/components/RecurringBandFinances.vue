<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { reportsApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { BandFinanceAccountHolder, RecurringBandTransaction } from '@/api/types'
import AppToggle from '@/components/ui/AppToggle.vue'
import { parseAmount, useMoney } from '@/composables/useMoney'
import { useFlashStore } from '@/stores/flash'

const props = defineProps<{
  incomeCategories: string[]
  expenseCategories: string[]
  accountHolders: BandFinanceAccountHolder[]
}>()
const emit = defineEmits<{ changed: [] }>()

const { t } = useI18n()
const { format } = useMoney()
const flash = useFlashStore()

const rules = ref<RecurringBandTransaction[]>([])
const busy = ref(false)
const form = ref({
  transaction_type: 'expense' as 'income' | 'expense',
  start_on: new Date().toISOString().slice(0, 10),
  category: 'Equipment',
  description: '',
  amount: '',
  account_holder_user_id: null as number | null,
  is_settled: true,
  is_asset: true,
  interval_value: 1,
  interval_unit: 'month' as 'day' | 'week' | 'month' | 'year',
})

const categoriesForType = computed(() =>
  form.value.transaction_type === 'income' ? props.incomeCategories : props.expenseCategories,
)

watch(() => form.value.transaction_type, (type) => {
  const categories = categoriesForType.value
  if (!categories.includes(form.value.category)) {
    form.value.category = categories[0] ?? 'Sonstiges'
  }
  if (type === 'income') form.value.is_asset = false
})

watch(() => form.value.category, (category) => {
  if (form.value.transaction_type === 'expense') {
    form.value.is_asset = category === 'Equipment'
  }
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
      account_holder_user_id: form.value.account_holder_user_id,
      is_settled: form.value.is_settled,
      is_asset: form.value.transaction_type === 'expense' && form.value.is_asset,
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
          <select v-model="form.category" required>
            <option v-for="entry in categoriesForType" :key="entry" :value="entry">
              {{ entry }}
            </option>
          </select>
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

      <label>
        {{ form.transaction_type === 'income'
          ? t('bandFinances.receivedBy')
          : t('bandFinances.paidBy') }}
        <select v-model="form.account_holder_user_id">
          <option :value="null">{{ t('bandFinances.bandCash') }}</option>
          <option v-for="holder in props.accountHolders" :key="holder.id" :value="holder.id">
            {{ holder.username }}
          </option>
        </select>
      </label>

      <AppToggle
        v-if="form.transaction_type === 'expense'"
        class="checkbox-row settlement-checkbox"
        v-model="form.is_asset"
        :label="t('bandFinances.asset')"
      />
      <p v-if="form.transaction_type === 'expense'" class="muted settlement-hint">
        {{ t('bandFinances.assetHint') }}
      </p>

      <AppToggle
        v-model="form.is_settled"
        class="checkbox-row settlement-checkbox"
        :label="form.transaction_type === 'income'
            ? t('bandFinances.recurring.autoReceived')
            : t('bandFinances.recurring.autoPaid')"
      />
      <p v-if="!form.is_settled" class="muted settlement-hint">
        {{ t('bandFinances.recurring.openHint') }}
      </p>

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
            <th>{{ t('bandFinances.accountHolder') }}</th>
            <th class="numeric">{{ t('bandFinances.amount') }}</th>
            <th>{{ t('bandFinances.status') }}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="rule in rules"
            :key="rule.id"
            :class="{
              'cancelled-row': !rule.is_active,
              'unsettled-rule': rule.is_active && !rule.is_settled,
            }"
          >
            <td>
              <strong>{{ rule.description }}</strong>
              <small>
                {{ rule.category }}
                <template v-if="rule.is_asset"> · {{ t('bandFinances.assetShort') }}</template>
              </small>
            </td>
            <td>
              {{ t('bandFinances.recurring.intervalLabel', {
                count: rule.interval_value,
                unit: t(`bandFinances.recurring.units.${rule.interval_unit}`),
              }) }}
            </td>
            <td>{{ rule.next_run_on }}</td>
            <td>{{ rule.account_holder_username || t('bandFinances.bandCash') }}</td>
            <td class="numeric" :class="rule.transaction_type">
              {{ rule.transaction_type === 'expense' ? '−' : '+' }}{{ format(rule.amount_cents) }}
            </td>
            <td>
              {{ rule.transaction_type === 'income'
                ? (rule.is_settled ? t('bandFinances.received') : t('bandFinances.notReceived'))
                : (rule.is_settled ? t('bandFinances.paid') : t('bandFinances.notPaid')) }}
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

.unsettled-rule {
  background: color-mix(in srgb, var(--warning) 9%, transparent);
  box-shadow: inset 4px 0 var(--warning);
}

.stack-form .settlement-checkbox {
  display: inline-flex;
  flex-direction: row;
  flex-wrap: nowrap;
  align-items: center;
  align-self: flex-start;
  gap: 10px;
  width: fit-content;
  max-width: 100%;
  margin: 4px 0 0;
  padding: 9px 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius-control);
  color: var(--text);
  background: color-mix(in srgb, var(--panel-raised) 82%, transparent);
  cursor: pointer;
}

.stack-form .settlement-checkbox input[type='checkbox'] {
  width: 18px;
  height: 18px;
  min-width: 18px;
  margin: 0;
  padding: 0;
  flex: 0 0 auto;
  accent-color: var(--accent);
}

.stack-form .settlement-checkbox span {
  line-height: 1.35;
}

.settlement-hint {
  max-width: 720px;
  margin: -6px 0 0 12px;
  line-height: 1.4;
}

@media (max-width: 640px) {
  .recurrence-row {
    grid-template-columns: 1fr;
  }
}
</style>
