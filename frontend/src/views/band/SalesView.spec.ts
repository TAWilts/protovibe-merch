import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import SalesView from './SalesView.vue'

const { assortment, book, createPaymentQrIntent, events, deleteEvent } = vi.hoisted(() => ({
  assortment: vi.fn(),
  book: vi.fn(),
  createPaymentQrIntent: vi.fn(),
  events: vi.fn(),
  deleteEvent: vi.fn(),
}))

vi.mock('vue-router', () => ({ useRoute: () => ({ name: 'sales' }) }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (
      params?.variant ? `${key}:${params.variant}` : key
    ),
    locale: { value: 'de' },
  }),
}))
vi.mock('@/stores/flash', () => ({
  useFlashStore: () => ({ success: vi.fn(), error: vi.fn() }),
}))
vi.mock('@/stores/offline', () => ({
  useOfflineStore: () => ({ queue: vi.fn() }),
}))
vi.mock('@/stores/session', () => ({
  useSessionStore: () => ({
    user: { username: 'seller', show_variant_photos: true },
    featureFlags: { payment_qr: true, offline_sales: true },
    capabilities: { can_access_member_workflows: true },
  }),
}))
vi.mock('@/api/endpoints', () => ({
  catalogueApi: {
    assortment,
  },
  photosApi: { fileUrl: (id: number) => `/photos/${id}` },
  salesApi: {
    events,
    receiptPreview: vi.fn().mockResolvedValue({ receipt_id: 'V-1' }),
    paymentQrAvailability: vi.fn().mockResolvedValue({ paypal: false, bank: false }),
    createEvent: vi.fn(),
    deleteEvent,
    createPaymentQrIntent,
    cancelPaymentQrIntent: vi.fn(),
    book,
  },
}))

function button(wrapper: ReturnType<typeof mount>, text: string) {
  const found = wrapper.findAll('button').find(
    (entry) => entry.text().includes(text) && entry.attributes('disabled') === undefined,
  )
  if (!found) throw new Error(`button ${text} not found`)
  return found
}

function field(wrapper: ReturnType<typeof mount>, labelText: string) {
  const label = wrapper.findAll('label').find((entry) => entry.text().includes(labelText))
  if (!label) throw new Error(`label ${labelText} not found`)
  const input = label.find('input, textarea')
  if (!input.exists()) throw new Error(`field ${labelText} not found`)
  return input
}

describe('SalesView checkout', () => {
  beforeEach(() => {
    book.mockReset().mockResolvedValue({ receipt_id: 'V-1', sale_ids: [1] })
    createPaymentQrIntent.mockReset()
    deleteEvent.mockReset().mockResolvedValue(undefined)
    events.mockReset().mockResolvedValue({ events: [], selected_event_id: 0 })
    assortment.mockReset().mockResolvedValue({
      payment_methods: ['Bar'],
      articles: [{
        id: 1,
        name: 'Testshirt',
        total_stock: 12,
        option_groups: [],
        variants: [{
          id: 11,
          combination_key: '',
          option_value_ids: [],
          sale_price_cents: 2000,
          on_hand: 12,
          photo_ids: [],
        }],
      }],
    })
  })

  it('moves from basket to payment and confirmation, then starts a fresh sale', async () => {
    const wrapper = mount(SalesView)
    await flushPromises()

    await button(wrapper, 'Testshirt').trigger('click')
    await flushPromises()
    await button(wrapper, 'sales.addToCart').trigger('click')

    expect(wrapper.find('.checkout-step-1').exists()).toBe(true)
    const mobileCartToggle = wrapper.get('.mobile-cart-toggle')
    expect(mobileCartToggle.attributes('aria-expanded')).toBe('false')
    expect(wrapper.get('.till-rail').classes()).not.toContain('is-mobile-open')
    expect(wrapper.get('.mobile-cart-checkout').attributes('disabled')).toBeUndefined()
    await mobileCartToggle.trigger('click')
    expect(mobileCartToggle.attributes('aria-expanded')).toBe('true')
    expect(wrapper.get('.till-rail').classes()).toContain('is-mobile-open')

    await button(wrapper, 'sales.paymentDetails').trigger('click')
    expect(wrapper.find('.checkout-step-2').exists()).toBe(true)

    await button(wrapper, 'common.confirm').trigger('click')
    expect(wrapper.find('.checkout-step-3').exists()).toBe(true)
    expect(wrapper.get('.checkout-sale-id').text()).toContain('sales.saleId')
    expect(wrapper.get('.checkout-sale-id').text()).toContain('V-1')

    await button(wrapper, 'sales.book').trigger('click')
    await flushPromises()

    expect(book).toHaveBeenCalledOnce()
    expect(wrapper.find('.checkout-step-1').exists()).toBe(true)
    expect(wrapper.text()).toContain('sales.cartEmpty')
  })

  it('shows the closest photographed variant with a fallback hint', async () => {
    assortment.mockResolvedValueOnce({
      payment_methods: ['Bar'],
      articles: [{
        id: 1,
        name: 'Testshirt',
        total_stock: 12,
        option_groups: [
          {
            id: 1,
            name: 'Größe',
            position: 1,
            is_active: true,
            values: [
              { id: 1, value: 'S', position: 1, is_active: true },
              { id: 2, value: 'M', position: 2, is_active: true },
            ],
          },
          {
            id: 2,
            name: 'Farbe',
            position: 2,
            is_active: true,
            values: [
              { id: 10, value: 'Rot', position: 1, is_active: true },
              { id: 11, value: 'Blau', position: 2, is_active: true },
            ],
          },
        ],
        variants: [
          { id: 11, combination_key: '1|10', option_value_ids: [1, 10], sale_price_cents: 2000, on_hand: 4, photo_ids: [], is_offered: true },
          { id: 12, combination_key: '1|11', option_value_ids: [1, 11], sale_price_cents: 2000, on_hand: 4, photo_ids: [99], is_offered: true },
          { id: 13, combination_key: '2|11', option_value_ids: [2, 11], sale_price_cents: 2000, on_hand: 4, photo_ids: [100], is_offered: true },
        ],
      }],
    })

    const wrapper = mount(SalesView)
    await flushPromises()
    await button(wrapper, 'Testshirt').trigger('click')
    await flushPromises()

    expect(wrapper.get('.variant-photo-list img').attributes('src')).toBe('/photos/99')
    expect(wrapper.get('.variant-photo-caption').text()).toBe(
      'sales.variantPhotoFallback:Testshirt — Größe: S · Farbe: Blau',
    )

    await button(wrapper, 'Blau').trigger('click')
    expect(wrapper.get('.variant-photo-caption').text()).toBe('sales.variantPhotoExact')
  })

  it('allows PayPal sales without configured QR data and explains where to add it', async () => {
    assortment.mockResolvedValueOnce({
      payment_methods: ['PayPal'],
      articles: [{
        id: 1,
        name: 'Testshirt',
        total_stock: 12,
        option_groups: [],
        variants: [{
          id: 11,
          combination_key: '',
          option_value_ids: [],
          sale_price_cents: 2000,
          on_hand: 12,
          photo_ids: [],
        }],
      }],
    })

    const wrapper = mount(SalesView)
    await flushPromises()
    await button(wrapper, 'Testshirt').trigger('click')
    await button(wrapper, 'sales.addToCart').trigger('click')
    await button(wrapper, 'sales.paymentDetails').trigger('click')

    expect(wrapper.text()).toContain('sales.paymentQrSetupHint')
    await button(wrapper, 'common.confirm').trigger('click')
    expect(wrapper.find('.checkout-step-3').exists()).toBe(true)
    expect(createPaymentQrIntent).not.toHaveBeenCalled()

    await button(wrapper, 'sales.book').trigger('click')
    await flushPromises()
    expect(book).toHaveBeenCalledOnce()
  })

  it('adds gross shipping costs to a sale booked for shipping', async () => {
    const wrapper = mount(SalesView)
    await flushPromises()
    await button(wrapper, 'Testshirt').trigger('click')
    await button(wrapper, 'sales.addToCart').trigger('click')
    await button(wrapper, 'sales.paymentDetails').trigger('click')

    await field(wrapper, 'sales.bookShipment').setValue(true)
    await field(wrapper, 'sales.customerName').setValue('Alex Muster')
    await field(wrapper, 'sales.customerAddress').setValue('Musterweg 1')
    await field(wrapper, 'sales.shippingCostGross').setValue('4,99')

    await button(wrapper, 'common.confirm').trigger('click')
    expect(wrapper.text()).toContain('sales.shippingCostGross')
    await button(wrapper, 'sales.book').trigger('click')
    await flushPromises()

    expect(book).toHaveBeenCalledWith(expect.objectContaining({
      is_received: false,
      shipping_cost_cents: 499,
      customer_name: 'Alex Muster',
      customer_address: 'Musterweg 1',
    }))
  })

  it('warns visibly for sold-out variants without blocking the sale', async () => {
    assortment.mockResolvedValueOnce({
      payment_methods: ['Bar'],
      articles: [{
        id: 1,
        name: 'Ausverkauftes Shirt',
        total_stock: 0,
        option_groups: [],
        variants: [{
          id: 11,
          combination_key: '',
          option_value_ids: [],
          sale_price_cents: 2000,
          on_hand: 0,
          photo_ids: [],
        }],
      }],
    })

    const wrapper = mount(SalesView)
    await flushPromises()
    await button(wrapper, 'Ausverkauftes Shirt').trigger('click')
    expect(wrapper.get('.stock-sale-warning').text()).toContain('sales.stockWarning')

    await button(wrapper, 'sales.addToCart').trigger('click')
    expect(wrapper.findAll('.stock-sale-warning').length).toBeGreaterThan(0)
    await button(wrapper, 'sales.paymentDetails').trigger('click')
    await button(wrapper, 'common.confirm').trigger('click')
    expect(wrapper.get('.stock-sale-warning').text()).toContain('sales.stockWarning')

    await button(wrapper, 'sales.book').trigger('click')
    await flushPromises()
    expect(book).toHaveBeenCalledOnce()
  })

  it('requires an explicit confirmation before booking a discount', async () => {
    const wrapper = mount(SalesView)
    await flushPromises()
    await button(wrapper, 'Testshirt').trigger('click')
    await button(wrapper, 'sales.addToCart').trigger('click')
    await button(wrapper, 'sales.paymentDetails').trigger('click')
    await field(wrapper, 'sales.amountActuallyPaid').setValue('10,00')

    await button(wrapper, 'common.confirm').trigger('click')
    expect(wrapper.get('.confirmation-dialog').text()).toContain('sales.discountConfirmTitle')
    expect(wrapper.find('.checkout-step-2').exists()).toBe(true)

    await button(wrapper, 'sales.confirmDiscount').trigger('click')
    expect(wrapper.find('.checkout-step-3').exists()).toBe(true)
    await button(wrapper, 'sales.book').trigger('click')
    await flushPromises()

    expect(book).toHaveBeenCalledWith(expect.objectContaining({
      amount_given_cents: 1000,
      discount_confirmed: true,
    }))
  })

  it('hides inactive and withdrawn articles and articles without an offered variant', async () => {
    assortment.mockResolvedValueOnce({
      payment_methods: ['Bar'],
      articles: [
        { id: 1, name: 'Aktiv', is_active: true, is_offered: true, configuration_complete: true, total_stock: 1, option_groups: [], variants: [{ id: 11, combination_key: '', option_value_ids: [], sale_price_cents: 100, on_hand: 1, photo_ids: [], is_active: true, is_offered: true }] },
        { id: 2, name: 'Nicht anbieten', is_active: true, is_offered: false, configuration_complete: true, total_stock: 1, option_groups: [], variants: [{ id: 12, combination_key: '', option_value_ids: [], sale_price_cents: 100, on_hand: 1, photo_ids: [], is_active: true, is_offered: true }] },
        { id: 3, name: 'Inaktiv', is_active: false, is_offered: true, configuration_complete: true, total_stock: 1, option_groups: [], variants: [{ id: 13, combination_key: '', option_value_ids: [], sale_price_cents: 100, on_hand: 1, photo_ids: [], is_active: true, is_offered: true }] },
        { id: 4, name: 'Keine Variante', is_active: true, is_offered: true, configuration_complete: true, total_stock: 1, option_groups: [], variants: [{ id: 14, combination_key: '', option_value_ids: [], sale_price_cents: 100, on_hand: 1, photo_ids: [], is_active: true, is_offered: false }] },
      ],
    })

    const wrapper = mount(SalesView)
    await flushPromises()
    expect(wrapper.text()).toContain('Aktiv')
    expect(wrapper.text()).not.toContain('Nicht anbieten')
    expect(wrapper.text()).not.toContain('Inaktiv')
    expect(wrapper.text()).not.toContain('Keine Variante')
  })

  it('deletes the selected event only after confirmation', async () => {
    events.mockResolvedValueOnce({
      events: [{ id: 7, name: 'Sommerfest', last_selected_at: '2026-09-07' }],
      selected_event_id: 7,
    })
    const wrapper = mount(SalesView)
    await flushPromises()
    await button(wrapper, 'Testshirt').trigger('click')
    await button(wrapper, 'sales.addToCart').trigger('click')
    await button(wrapper, 'sales.paymentDetails').trigger('click')

    await wrapper.get('button[aria-label="sales.deleteEvent"]').trigger('click')
    expect(wrapper.get('.confirmation-dialog').text()).toContain('sales.eventDeleteTitle')
    await wrapper.get('.confirmation-dialog .danger-button').trigger('click')
    await flushPromises()

    expect(deleteEvent).toHaveBeenCalledWith(7)
    expect(wrapper.find('button[aria-label="sales.deleteEvent"]').exists()).toBe(false)
  })
})
