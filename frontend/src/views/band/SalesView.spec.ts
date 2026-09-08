import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import SalesView from './SalesView.vue'

const {
  assortment, catalogueList, book, bookHistorical, createEvent,
  createPaymentQrIntent, events, queue, offlineState, sessionState,
} = vi.hoisted(() => {
  const queuedSale = vi.fn()
  return {
    assortment: vi.fn(),
    catalogueList: vi.fn(),
    book: vi.fn(),
    bookHistorical: vi.fn(),
    createEvent: vi.fn(),
    createPaymentQrIntent: vi.fn(),
    events: vi.fn(),
    queue: queuedSale,
    offlineState: { online: true, queue: queuedSale },
    sessionState: {
      user: { username: 'seller', show_variant_photos: true },
      featureFlags: { payment_qr: true, offline_sales: true },
      capabilities: { can_access_member_workflows: true, can_manage_purchases: true },
      posMode: false,
    },
  }
})

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
  useOfflineStore: () => offlineState,
}))
vi.mock('@/stores/session', () => ({
  useSessionStore: () => sessionState,
}))
vi.mock('@/offline/outbox', () => ({ deviceId: vi.fn().mockResolvedValue('desktop-1') }))
vi.mock('@/api/endpoints', () => ({
  catalogueApi: {
    assortment,
    list: catalogueList,
  },
  photosApi: { fileUrl: (id: number) => `/photos/${id}` },
  salesApi: {
    events,
    receiptPreview: vi.fn().mockResolvedValue({ receipt_id: 'V-1' }),
    paymentQrAvailability: vi.fn().mockResolvedValue({ paypal: false, bank: false }),
    createEvent,
    createPaymentQrIntent,
    cancelPaymentQrIntent: vi.fn(),
    book,
    bookHistorical,
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
    bookHistorical.mockReset().mockResolvedValue({ receipt_id: 'V-20260827-001', sale_ids: [2] })
    createEvent.mockReset()
    queue.mockReset()
    offlineState.online = true
    sessionState.capabilities.can_manage_purchases = true
    sessionState.posMode = false
    createPaymentQrIntent.mockReset()
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
    catalogueList.mockReset().mockResolvedValue({ articles: [] })

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

  it('keeps event deletion out of the sales wizard', async () => {
    events.mockResolvedValueOnce({
      events: [{ id: 7, name: 'Sommerfest', last_selected_at: '2026-09-07' }],
      selected_event_id: 7,
    })
    const wrapper = mount(SalesView)
    await flushPromises()
    await button(wrapper, 'Testshirt').trigger('click')
    await button(wrapper, 'sales.addToCart').trigger('click')
    await button(wrapper, 'sales.paymentDetails').trigger('click')

    expect(wrapper.find('button[aria-label="sales.deleteEvent"]').exists()).toBe(false)
  })

  it('only offers historical mode to online managers outside POS mode', async () => {
    sessionState.capabilities.can_manage_purchases = false
    let wrapper = mount(SalesView)
    await flushPromises()
    expect(wrapper.text()).not.toContain('sales.historicalEnter')
    wrapper.unmount()

    sessionState.capabilities.can_manage_purchases = true
    offlineState.online = false
    wrapper = mount(SalesView)
    await flushPromises()
    expect(wrapper.get('.historical-mode-note button').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('sales.historicalOffline')
    wrapper.unmount()

    offlineState.online = true
    sessionState.posMode = true
    wrapper = mount(SalesView)
    await flushPromises()
    expect(wrapper.text()).not.toContain('sales.historicalEnter')
  })

  it('books a historical event against withdrawn catalogue variants without the offline queue', async () => {
    events.mockResolvedValue({
      events: [{ id: 7, name: 'Archivfestival', last_selected_at: null }],
      selected_event_id: 0,
    })
    catalogueList.mockResolvedValue({
      articles: [{
        id: 2,
        name: 'Altes Shirt',
        is_active: true,
        is_offered: false,
        configuration_complete: true,
        total_stock: 0,
        option_groups: [],
        variants: [{
          id: 22,
          combination_key: '',
          option_value_ids: [],
          sale_price_cents: 1500,
          on_hand: 0,
          photo_ids: [],
          is_active: false,
          is_offered: false,
        }],
      }],
    })

    const wrapper = mount(SalesView)
    await flushPromises()
    await button(wrapper, 'sales.historicalEnter').trigger('click')
    await flushPromises()

    expect(catalogueList).toHaveBeenCalledWith(true)
    expect(wrapper.text()).toContain('Altes Shirt')
    expect(wrapper.text()).toContain('sales.historicalNotOffered')
    await button(wrapper, 'Altes Shirt').trigger('click')
    expect(wrapper.text()).toContain('sales.historicalWithdrawnVariant')
    await field(wrapper, 'sales.unitPrice').setValue('12,50')
    await button(wrapper, 'sales.addToCart').trigger('click')
    await button(wrapper, 'sales.paymentDetails').trigger('click')

    await field(wrapper, 'sales.historicalDate').setValue('2026-08-27')
    const eventSelect = wrapper.findAll('label').find((entry) => entry.text().includes('sales.event'))?.find('select')
    if (!eventSelect?.exists()) throw new Error('event selector not found')
    await eventSelect.setValue('7')
    await button(wrapper, 'common.confirm').trigger('click')
    await button(wrapper, 'sales.historicalBook').trigger('click')
    await flushPromises()

    expect(bookHistorical).toHaveBeenCalledWith(expect.objectContaining({
      items: [{ variant_id: 22, quantity: 1, unit_price_cents: 1250 }],
      sale_event_id: 7,
      sold_on: '2026-08-27',
      amount_given_cents: 1250,
      client_device_id: 'desktop-1',
    }))
    expect(queue).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('sales.historicalTitle')
    expect(wrapper.get('.till-rail-receipt-id').text()).toBe('sales.receiptLoading')
  })

  it('creates historical events without changing the shared live selection', async () => {
    catalogueList.mockResolvedValue({ articles: [{
      id: 1, name: 'Testshirt', is_active: true, is_offered: true,
      configuration_complete: true, total_stock: 12, option_groups: [],
      variants: [{ id: 11, combination_key: '', option_value_ids: [], sale_price_cents: 2000, on_hand: 12, photo_ids: [], is_active: true, is_offered: true }],
    }] })
    createEvent.mockResolvedValue({ id: 9, name: 'Alter Gig' })
    const wrapper = mount(SalesView)
    await flushPromises()
    await button(wrapper, 'sales.historicalEnter').trigger('click')
    await flushPromises()
    await button(wrapper, 'Testshirt').trigger('click')
    await button(wrapper, 'sales.addToCart').trigger('click')
    await button(wrapper, 'sales.paymentDetails').trigger('click')
    await wrapper.get('button[aria-label="sales.newEvent"]').trigger('click')
    await wrapper.get('.new-event-row input').setValue('Alter Gig')
    await button(wrapper, 'sales.createEvent').trigger('click')
    await flushPromises()

    expect(createEvent).toHaveBeenCalledWith('Alter Gig', false)
  })

  it('retains the exact historical retry after a lost response and never queues it', async () => {
    events.mockResolvedValue({ events: [{ id: 7, name: 'Archivfestival' }], selected_event_id: 0 })
    catalogueList.mockResolvedValue({ articles: [{
      id: 1, name: 'Testshirt', is_active: true, is_offered: true,
      configuration_complete: true, total_stock: 12, option_groups: [],
      variants: [{ id: 11, combination_key: '', option_value_ids: [], sale_price_cents: 2000, on_hand: 12, photo_ids: [], is_active: true, is_offered: true }],
    }] })
    bookHistorical.mockRejectedValueOnce(new Error('connection lost')).mockResolvedValueOnce({ receipt_id: 'V-1', sale_ids: [1] })
    const wrapper = mount(SalesView)
    await flushPromises()
    await button(wrapper, 'sales.historicalEnter').trigger('click')
    await flushPromises()
    await button(wrapper, 'Testshirt').trigger('click')
    await button(wrapper, 'sales.addToCart').trigger('click')
    await button(wrapper, 'sales.paymentDetails').trigger('click')
    await field(wrapper, 'sales.historicalDate').setValue('2026-08-27')
    const select = wrapper.find('select')
    await select.setValue('7')
    await button(wrapper, 'common.confirm').trigger('click')
    await button(wrapper, 'sales.historicalBook').trigger('click')
    await flushPromises()
    const firstPayload = bookHistorical.mock.calls[0][0]
    expect(wrapper.text()).toContain('sales.historicalRetry')

    await button(wrapper, 'sales.historicalRetry').trigger('click')
    await flushPromises()
    expect(bookHistorical.mock.calls[1][0]).toEqual(firstPayload)
    expect(queue).not.toHaveBeenCalled()
  })
})
