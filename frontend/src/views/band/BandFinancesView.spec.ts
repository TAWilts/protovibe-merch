import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import BandFinancesView from './BandFinancesView.vue'

const { bandLedger, createBandEntry, attachmentList, attachmentUpload, attachmentRemove } = vi.hoisted(() => ({
  bandLedger: vi.fn(),
  createBandEntry: vi.fn(),
  attachmentList: vi.fn(),
  attachmentUpload: vi.fn(),
  attachmentRemove: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'de' } }),
}))
vi.mock('@/stores/flash', () => ({
  useFlashStore: () => ({ success: vi.fn(), error: vi.fn() }),
}))
vi.mock('@/stores/session', () => ({
  useSessionStore: () => ({
    capabilities: { can_create_band_finances: true, can_manage_band_finances: false },
  }),
}))
vi.mock('@/api/endpoints', () => ({
  reportsApi: {
    bandLedger,
    createBandEntry,
    updateBandEntry: vi.fn(),
    settleBandEntry: vi.fn(),
    cancelBandEntry: vi.fn(),
  },
  bandFinanceAttachmentsApi: {
    list: attachmentList,
    upload: attachmentUpload,
    remove: attachmentRemove,
    fileUrl: (transactionId: number, attachmentId: number) => `/finance/${transactionId}/${attachmentId}`,
  },
}))

const transaction = {
  id: 7,
  transaction_type: 'income' as const,
  transaction_on: '2026-09-09',
  category: 'Gage',
  description: 'Sommerfest',
  amount_cents: 10000,
  is_settled: true,
  is_asset: false,
  settled_by_username: 'member',
  is_cancelled: false,
  created_by_username: 'member',
  attachments: [{ id: 70, original_filename: 'vertrag.pdf', size_bytes: 123 }],
}

const ledger = {
  entries: [transaction],
  categories: [],
  suggested_categories: ['Gage'],
  suggested_income_categories: ['Gage'],
  suggested_expense_categories: ['Equipment'],
  income_cents: 10000,
  expense_cents: 0,
  balance_cents: 10000,
  open_income_cents: 0,
  open_expense_cents: 0,
}

function field(wrapper: ReturnType<typeof mount>, labelText: string) {
  const label = wrapper.findAll('label').find((entry) => entry.text().includes(labelText))
  if (!label) throw new Error(`label ${labelText} not found`)
  return label.find('input, textarea, select')
}

describe('BandFinancesView attachments', () => {
  beforeEach(() => {
    bandLedger.mockReset().mockResolvedValue(ledger)
    createBandEntry.mockReset().mockResolvedValue({ ...transaction, id: 8, attachments: [] })
    attachmentList.mockReset().mockResolvedValue({ attachments: transaction.attachments })
    attachmentUpload.mockReset().mockResolvedValue({ id: 80, original_filename: 'beleg.pdf', size_bytes: 20 })
    attachmentRemove.mockReset().mockResolvedValue(undefined)
  })

  it('shows attachment counts and opens the attachment dialog for members', async () => {
    const wrapper = mount(BandFinancesView, {
      global: { stubs: { DateRangeFilter: true, RecurringBandFinances: true } },
    })
    await flushPromises()

    const button = wrapper.get('.attachment-button')
    expect(button.text()).toContain('1')
    await button.trigger('click')
    await flushPromises()

    expect(attachmentList).toHaveBeenCalledWith(7)
    expect(wrapper.text()).toContain('vertrag.pdf')
    expect(wrapper.get('a[href="/finance/7/70"]').exists()).toBe(true)
  })

  it('keeps multiple selected files local and uploads them after creating the booking', async () => {
    const wrapper = mount(BandFinancesView, {
      global: { stubs: { DateRangeFilter: true, RecurringBandFinances: true } },
    })
    await flushPromises()

    const fileInput = wrapper.get('.finance-file-picker input[type="file"]')
    const files = [
      new File(['one'], 'eins.pdf', { type: 'application/pdf' }),
      new File(['two'], 'zwei.pdf', { type: 'application/pdf' }),
    ]
    Object.defineProperty(fileInput.element, 'files', { configurable: true, value: files })
    await fileInput.trigger('change')
    expect(wrapper.text()).toContain('eins.pdf')
    expect(wrapper.text()).toContain('zwei.pdf')

    await field(wrapper, 'bandFinances.description').setValue('Neue Gage')
    await field(wrapper, 'bandFinances.amount').setValue('25,00')
    await wrapper.get('.stack-form').trigger('submit')
    await flushPromises()

    expect(createBandEntry).toHaveBeenCalledWith(expect.objectContaining({
      description: 'Neue Gage', amount_cents: 2500,
    }))
    expect(attachmentUpload.mock.calls).toEqual([
      [8, files[0]],
      [8, files[1]],
    ])
  })
})
