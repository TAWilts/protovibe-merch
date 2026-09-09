import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import PurchasesView from './PurchasesView.vue'

const { catalogueList } = vi.hoisted(() => ({ catalogueList: vi.fn() }))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'de' } }),
}))
vi.mock('@/stores/flash', () => ({
  useFlashStore: () => ({ success: vi.fn(), error: vi.fn() }),
}))
vi.mock('@/stores/session', () => ({
  useSessionStore: () => ({ capabilities: { can_manage_purchases: true } }),
}))
vi.mock('@/api/endpoints', () => ({
  catalogueApi: { list: catalogueList },
  salesApi: { receiptPreview: vi.fn().mockResolvedValue({ receipt_id: 'E-2' }) },
  purchasesApi: {
    list: vi.fn().mockResolvedValue({
      editing_enabled: true,
      purchases: [{
        id: 1,
        receipt_id: 'E-20260907-001',
        variant_id: 11,
        article_name: 'Testshirt',
        variant_label: 'Größe: M',
        quantity: 2,
        unit_cost_cents: 900,
        total_cost_cents: 1800,
        purchased_on: '2026-09-07',
        supplier: 'Druckerei',
        invoice_reference: 'R-1',
        has_invoice_file: false,
        has_receipt_attachment: true,
        prices_include_vat: true,
        vat_rate_basis_points: 1900,
        shipping_cost_cents: 500,
        comment: '',
        is_cancelled: false,
        cancelled_by_username: '',
        created_by_username: 'manager',
      }],
    }),
    lastCost: vi.fn(),
    create: vi.fn(),
    updateReceipt: vi.fn(),
    cancelReceipt: vi.fn(),
  },
  attachmentsApi: {
    invoiceUrl: (id: number) => `/invoice/${id}`,
    fileUrl: vi.fn(),
    list: vi.fn().mockResolvedValue({ attachments: [] }),
    upload: vi.fn(),
    remove: vi.fn(),
  },
}))

describe('PurchasesView receipt header', () => {
  beforeEach(() => {
    catalogueList.mockReset().mockResolvedValue({ articles: [] })
  })

  it('shows attachments and exposes enabled editing without expanding first', async () => {
    const wrapper = mount(PurchasesView)
    await flushPromises()

    const summary = wrapper.get('.purchase-receipt-card summary')
    expect(summary.find('.attachment-indicator').exists()).toBe(true)
    const edit = summary.findAll('button').find((entry) => entry.text() === 'purchases.edit')
    expect(edit).toBeDefined()
    await edit!.trigger('click')
    expect(wrapper.get('.confirmation-dialog').text()).toContain('purchases.editTitle')
  })

  it('hides no-reorder variants but keeps withdrawn reorderable variants', async () => {
    catalogueList.mockResolvedValue({
      articles: [
        {
          id: 1,
          name: 'Teilweise nachbestellbar',
          option_groups: [{
            id: 10,
            name: 'Ausgabe',
            position: 0,
            is_active: true,
            values: [
              { id: 101, value: 'Standard', position: 0, is_active: true },
              { id: 102, value: 'Deluxe', position: 1, is_active: true },
            ],
          }],
          variants: [
            { id: 11, option_value_ids: [101], combination_key: '101', no_reorder: true, is_active: true, is_offered: true },
            { id: 12, option_value_ids: [102], combination_key: '102', no_reorder: false, is_active: false, is_offered: false },
          ],
        },
        {
          id: 2,
          name: 'Komplett ausgemustert',
          option_groups: [],
          variants: [{ id: 21, option_value_ids: [], combination_key: '', no_reorder: true, is_active: true, is_offered: true }],
        },
      ],
    })

    const wrapper = mount(PurchasesView)
    await flushPromises()

    expect(wrapper.text()).toContain('Teilweise nachbestellbar')
    expect(wrapper.text()).not.toContain('Komplett ausgemustert')
    const articleButton = wrapper.findAll('button').find((entry) => entry.text().includes('Teilweise nachbestellbar'))
    expect(articleButton).toBeDefined()
    await articleButton!.trigger('click')
    expect(wrapper.text()).toContain('Deluxe')
    expect(wrapper.text()).not.toContain('Standard')
  })
})
