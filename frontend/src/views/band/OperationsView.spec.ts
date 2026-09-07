import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import OperationsView from './OperationsView.vue'

const { queues, markPaid, updateShippingCost } = vi.hoisted(() => ({
  queues: vi.fn(), markPaid: vi.fn(), updateShippingCost: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'de' } }),
}))
vi.mock('@/stores/flash', () => ({
  useFlashStore: () => ({ success: vi.fn(), error: vi.fn() }),
}))
vi.mock('@/api/endpoints', () => ({
  operationsApi: {
    queues,
    markPaid,
    updateShippingCost,
    setDeliveryStatus: vi.fn(),
  },
}))

function position(id: number, name: string, amount: number) {
  return {
    id,
    receipt_id: 'V-20260901-001',
    sold_on: '2026-09-01',
    payment_method: 'Überweisung',
    customer_name: 'Alex Muster',
    customer_address: 'Musterweg 1',
    event_name: '',
    comment: '',
    variant_id: id,
    article_name: name,
    variant_label: 'M',
    quantity: 1,
    unit_price_cents: amount,
    amount_due_cents: amount,
    shipping_cost_cents: 0,
    discount_cents: 0,
    amount_given_cents: null,
    donation_cents: 0,
    is_paid: false,
    payment_follow_up: false,
    is_received: true,
    delivery_status: 'not_applicable',
    is_cancelled: false,
  }
}

describe('OperationsView payment baskets', () => {
  beforeEach(() => {
    markPaid.mockReset().mockResolvedValue(undefined)
    updateShippingCost.mockReset().mockResolvedValue({
      receipt_id: 'V-20260901-001', sale_ids: [11], old_shipping_cost_cents: 500,
      shipping_cost_cents: 700, total_due_cents: 2500, total_paid_cents: 2300,
      discount_cents: 200, donation_cents: 0,
    })
    queues.mockReset()
      .mockResolvedValueOnce({
        open_shipments: [],
        delivered_shipments: [],
        open_payments: [position(11, 'Shirt', 1800), position(12, 'Hoodie', 4200)],
        settled_payments: [],
      })
      .mockResolvedValue({
        open_shipments: [], delivered_shipments: [], open_payments: [], settled_payments: [],
      })
  })

  it('renders one receipt basket and settles it with one action', async () => {
    const wrapper = mount(OperationsView)
    await flushPromises()

    expect(wrapper.findAll('.payment-card')).toHaveLength(1)
    expect(wrapper.get('.payment-card').text()).toContain('Shirt')
    expect(wrapper.get('.payment-card').text()).toContain('Hoodie')
    expect(wrapper.get('.payment-card').text()).toContain('60,00')

    await wrapper.get('.payment-card button').trigger('click')
    await flushPromises()

    expect(markPaid).toHaveBeenCalledOnce()
    expect(markPaid).toHaveBeenCalledWith(11)
  })

  it('edits the gross shipping cost for an open receipt', async () => {
    const shipment = position(11, 'Shirt', 2300)
    shipment.shipping_cost_cents = 500
    shipment.amount_given_cents = 2300
    shipment.is_paid = true
    shipment.is_received = false
    shipment.delivery_status = 'pending'
    queues.mockReset()
      .mockResolvedValueOnce({ open_shipments: [shipment], delivered_shipments: [], open_payments: [], settled_payments: [] })
      .mockResolvedValue({ open_shipments: [], delivered_shipments: [], open_payments: [], settled_payments: [] })

    const wrapper = mount(OperationsView)
    await flushPromises()
    expect(wrapper.get('.shipping-cost-row').text()).toContain('5,00')
    await wrapper.get('.shipping-cost-row button').trigger('click')
    await wrapper.get('.confirmation-dialog input').setValue('7,00')
    await wrapper.get('.confirmation-dialog form').trigger('submit')
    await flushPromises()

    expect(updateShippingCost).toHaveBeenCalledWith('V-20260901-001', 700)
  })
})
