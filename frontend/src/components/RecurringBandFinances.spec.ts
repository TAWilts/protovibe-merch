import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import RecurringBandFinances from './RecurringBandFinances.vue'

const { recurringBandEntries, createRecurringBandEntry } = vi.hoisted(() => ({
  recurringBandEntries: vi.fn(),
  createRecurringBandEntry: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'de' } }),
}))
vi.mock('@/stores/flash', () => ({
  useFlashStore: () => ({ success: vi.fn(), error: vi.fn() }),
}))
vi.mock('@/api/endpoints', () => ({
  reportsApi: {
    recurringBandEntries,
    createRecurringBandEntry,
    setRecurringBandEntryActive: vi.fn(),
    deleteRecurringBandEntry: vi.fn(),
  },
}))

function field(wrapper: ReturnType<typeof mount>, labelText: string) {
  const label = wrapper.findAll('label').find((entry) => entry.text().includes(labelText))
  if (!label) throw new Error(`label ${labelText} not found`)
  return label.find('input, textarea, select')
}

describe('RecurringBandFinances', () => {
  beforeEach(() => {
    recurringBandEntries.mockReset().mockResolvedValue({
      recurring: [{
        id: 5,
        transaction_type: 'expense',
        start_on: '2026-09-01',
        next_run_on: '2026-10-01',
        category: 'Equipment',
        description: 'Privates Abo',
        amount_cents: 1200,
        account_holder_user_id: 8,
        account_holder_username: 'Alex',
        is_settled: true,
        is_asset: false,
        interval_value: 1,
        interval_unit: 'month',
        is_active: true,
      }],
    })
    createRecurringBandEntry.mockReset().mockResolvedValue({})
  })

  it('selects and displays the account holder for recurring bookings', async () => {
    const wrapper = mount(RecurringBandFinances, {
      props: {
        incomeCategories: ['Gage'],
        expenseCategories: ['Equipment'],
        accountHolders: [{ id: 8, username: 'Alex' }, { id: 9, username: 'Kim' }],
      },
    })
    await flushPromises()

    expect(wrapper.get('tbody').text()).toContain('Alex')
    await field(wrapper, 'bandFinances.paidBy').setValue('9')
    await field(wrapper, 'bandFinances.description').setValue('Monatsabo')
    await field(wrapper, 'bandFinances.amount').setValue('12,50')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(createRecurringBandEntry).toHaveBeenCalledWith(expect.objectContaining({
      account_holder_user_id: 9,
      amount_cents: 1250,
    }))
  })
})
