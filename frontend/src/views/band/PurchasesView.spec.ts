import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import PurchasesView from './PurchasesView.vue'

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
  catalogueApi: { list: vi.fn().mockResolvedValue({ articles: [] }) },
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
})
