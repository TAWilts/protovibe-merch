import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/api/client'
import SalesView from './SalesView.vue'

const {
  assortment, catalogueList, book, bookHistorical, createEvent,
  createPaymentQrIntent, events, queue, offlineState, sessionState, route, routerReplace,
  routeLeaveGuards, prepareSale, preparedSalePayload,
  loadSalesAssortment, saveSalesAssortment,
} = vi.hoisted(() => {
  const queuedSale = vi.fn()
  const prepare = vi.fn()
  const payload = vi.fn()
  return {
    assortment: vi.fn(),
    catalogueList: vi.fn(),
    book: vi.fn(),
    bookHistorical: vi.fn(),
    createEvent: vi.fn(),
    createPaymentQrIntent: vi.fn(),
    events: vi.fn(),
    queue: queuedSale,
    prepareSale: prepare,
    preparedSalePayload: payload,
    loadSalesAssortment: vi.fn(),
    saveSalesAssortment: vi.fn(),
    offlineState: {
      online: true,
      queue: queuedSale,
      queuePrepared: queuedSale,
      conflicts: [] as Array<Record<string, unknown>>,
    },
    sessionState: {
      user: { username: 'seller', show_variant_photos: true },
      band: { id: 12 },
      featureFlags: { payment_qr: true, offline_sales: true },
      capabilities: { can_access_member_workflows: true, can_manage_purchases: true },
    },
    route: { name: 'sales', query: {} as Record<string, string> },
    routerReplace: vi.fn(),
    routeLeaveGuards: [] as Array<() => boolean>,
  }
})

vi.mock('vue-router', () => ({
  useRoute: () => route,
  useRouter: () => ({ replace: routerReplace }),
  onBeforeRouteLeave: (guard: () => boolean) => routeLeaveGuards.push(guard),
}))
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
vi.mock('@/offline/outbox', () => ({
  deviceId: vi.fn().mockResolvedValue('desktop-1'),
  prepareSale,
  preparedSalePayload,
}))
vi.mock('@/offline/assortment', () => ({ loadSalesAssortment, saveSalesAssortment }))
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

const originalScrollIntoView = HTMLElement.prototype.scrollIntoView

afterEach(() => {
  vi.unstubAllGlobals()
  if (originalScrollIntoView) {
    Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', {
      configurable: true,
      value: originalScrollIntoView,
    })
  } else {
    Reflect.deleteProperty(HTMLElement.prototype, 'scrollIntoView')
  }
})

describe('SalesView checkout', () => {
  beforeEach(() => {
    book.mockReset().mockResolvedValue({ receipt_id: 'V-1', sale_ids: [1] })
    bookHistorical.mockReset().mockResolvedValue({ receipt_id: 'V-20260827-001', sale_ids: [2] })
    createEvent.mockReset()
    queue.mockReset()
    prepareSale.mockReset().mockImplementation(async (payload) => ({
      eventId: 'sale-event-1',
      deviceId: 'desktop-1',
      createdAt: '2026-09-12T12:00:00.000Z',
      payload,
    }))
    preparedSalePayload.mockReset().mockImplementation((prepared) => ({
      ...prepared.payload,
      client_event_id: prepared.eventId,
      client_device_id: prepared.deviceId,
      client_created_at: prepared.createdAt,
    }))
    loadSalesAssortment.mockReset().mockResolvedValue(null)
    saveSalesAssortment.mockReset().mockResolvedValue(undefined)
    offlineState.online = true
    offlineState.conflicts = []
    sessionState.capabilities.can_manage_purchases = true
    route.query = {}
    routerReplace.mockReset().mockResolvedValue(undefined)
    routeLeaveGuards.length = 0
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

  it('stores every successfully loaded live assortment for the current band', async () => {
    const wrapper = mount(SalesView)
    await flushPromises()

    expect(saveSalesAssortment).toHaveBeenCalledWith(12, expect.objectContaining({
      payment_methods: ['Bar'],
      articles: [expect.objectContaining({ name: 'Testshirt' })],
    }))
    expect(wrapper.find('.offline-assortment-note').exists()).toBe(false)
  })

  it('reopens sales from the current band’s cached assortment after a network failure', async () => {
    assortment.mockRejectedValueOnce(new TypeError('network unavailable'))
    loadSalesAssortment.mockResolvedValueOnce({
      bandId: 12,
      savedAt: '2026-09-12T12:00:00.000Z',
      paymentMethods: ['Bar'],
      articles: [{
        id: 2,
        name: 'Offline Shirt',
        total_stock: 4,
        option_groups: [],
        variants: [{
          id: 22,
          combination_key: '',
          option_value_ids: [],
          sale_price_cents: 2200,
          on_hand: 4,
          photo_ids: [],
        }],
      }],
    })

    const wrapper = mount(SalesView)
    await flushPromises()

    expect(loadSalesAssortment).toHaveBeenCalledWith(12)
    expect(wrapper.text()).toContain('Offline Shirt')
    expect(wrapper.get('.offline-assortment-note').text()).toContain('sales.offlineAssortment')
    expect(wrapper.find('.offline-not-ready').exists()).toBe(false)
  })

  it('blocks an offline cold start until this band has been prepared once', async () => {
    assortment.mockRejectedValueOnce(new TypeError('network unavailable'))
    loadSalesAssortment.mockResolvedValueOnce(null)

    const wrapper = mount(SalesView)
    await flushPromises()

    expect(wrapper.get('.offline-not-ready').text()).toContain('sales.offlineNotReadyTitle')
    expect(wrapper.text()).not.toContain('sales.specialEntry')
  })

  it('does not hide an explicit server rejection behind cached data', async () => {
    assortment.mockRejectedValueOnce(new ApiError(403, 'forbidden'))
    loadSalesAssortment.mockResolvedValueOnce({
      bandId: 12,
      savedAt: '2026-09-12T12:00:00.000Z',
      paymentMethods: ['Bar'],
      articles: [],
    })

    const wrapper = mount(SalesView)
    await flushPromises()

    expect(loadSalesAssortment).not.toHaveBeenCalled()
    expect(wrapper.get('.offline-not-ready').text()).toContain('sales.assortmentServerError')
  })

  it('keeps permanently rejected offline sales visibly flagged for intervention', async () => {
    offlineState.conflicts = [{
      eventId: 'failed-sale',
      attempts: 2,
      lastError: 'Bestand wurde zwischenzeitlich geändert',
      failedPermanently: true,
    }]

    const wrapper = mount(SalesView)
    await flushPromises()

    expect(wrapper.get('.offline-sale-conflicts').text()).toContain('sales.offlineConflictsTitle')
    expect(wrapper.get('.offline-sale-conflicts').text()).toContain('Bestand wurde zwischenzeitlich geändert')
  })

  it('warns before leaving with an unfinished sales basket', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    const wrapper = mount(SalesView)
    await flushPromises()

    await button(wrapper, 'Testshirt').trigger('click')
    await button(wrapper, 'sales.addToCart').trigger('click')

    expect(routeLeaveGuards).toHaveLength(1)
    expect(routeLeaveGuards[0]!()).toBe(false)
    expect(confirm).toHaveBeenCalledWith('sales.unfinishedLeave')
  })

  it('books Spende/Sonstiges as a separate stock-neutral receipt', async () => {
    const wrapper = mount(SalesView)
    await flushPromises()

    await button(wrapper, 'sales.specialEntry').trigger('click')
    expect(wrapper.text()).toContain('sales.specialEntryTitle')
    const typeSelect = wrapper.find('.till-compose select')
    expect(typeSelect.exists()).toBe(true)
    await typeSelect.setValue('misc_income')
    await field(wrapper, 'sales.specialDescription').setValue('Pfandbecher')
    await field(wrapper, 'sales.specialAmount').setValue('12,50')
    await button(wrapper, 'sales.addToCart').trigger('click')
    await button(wrapper, 'sales.paymentDetails').trigger('click')

    expect(wrapper.text()).not.toContain('sales.shipmentTitle')
    expect(wrapper.text()).not.toContain('sales.amountActuallyPaid')
    await button(wrapper, 'common.confirm').trigger('click')
    await button(wrapper, 'sales.book').trigger('click')
    await flushPromises()

    expect(book).toHaveBeenCalledWith(expect.objectContaining({
      items: [{ line_type: 'misc_income', description: 'Pfandbecher', amount_cents: 1250 }],
      amount_given_cents: 1250,
      shipping_cost_cents: 0,
      is_paid: true,
      is_received: true,
    }))
    expect(queue).not.toHaveBeenCalled()
  })

  it('queues a network-uncertain sale with the exact identity used for its first request', async () => {
    book.mockRejectedValueOnce(new Error('offline'))
    const wrapper = mount(SalesView)
    await flushPromises()

    await button(wrapper, 'sales.specialEntry').trigger('click')
    await field(wrapper, 'sales.specialAmount').setValue('5,00')
    await button(wrapper, 'sales.addToCart').trigger('click')
    await button(wrapper, 'sales.paymentDetails').trigger('click')
    await button(wrapper, 'common.confirm').trigger('click')
    await button(wrapper, 'sales.book').trigger('click')
    await flushPromises()

    expect(book).toHaveBeenCalledWith(expect.objectContaining({
      client_event_id: 'sale-event-1',
      client_device_id: 'desktop-1',
      client_created_at: '2026-09-12T12:00:00.000Z',
    }))
    expect(queue).toHaveBeenCalledWith(expect.objectContaining({
      eventId: 'sale-event-1',
      deviceId: 'desktop-1',
      createdAt: '2026-09-12T12:00:00.000Z',
      payload: expect.objectContaining({
        items: [{ line_type: 'donation', description: 'Spende', amount_cents: 500 }],
        amount_given_cents: 500,
      }),
    }))
  })

  it('adds gross shipping costs to a sale booked for shipping', async () => {
    const wrapper = mount(SalesView)
    await flushPromises()
    await button(wrapper, 'Testshirt').trigger('click')
    await button(wrapper, 'sales.addToCart').trigger('click')
    await button(wrapper, 'sales.paymentDetails').trigger('click')

    await button(wrapper, 'sales.bookShipment').trigger('click')
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
    expect(wrapper.find('.order-only-hint').exists()).toBe(false)

    await button(wrapper, 'sales.addToCart').trigger('click')
    expect(wrapper.findAll('.stock-sale-warning').length).toBeGreaterThan(0)
    await button(wrapper, 'sales.paymentDetails').trigger('click')
    await button(wrapper, 'common.confirm').trigger('click')
    expect(wrapper.get('.stock-sale-warning').text()).toContain('sales.stockWarning')

    await button(wrapper, 'sales.book').trigger('click')
    await flushPromises()
    expect(book).toHaveBeenCalledOnce()
  })

  it('labels an empty on-demand variant instead of showing the generic stock warning', async () => {
    assortment.mockResolvedValueOnce({
      payment_methods: ['Bar'],
      articles: [{
        id: 1,
        name: 'Shirt auf Bestellung',
        total_stock: 0,
        option_groups: [],
        variants: [{
          id: 11,
          combination_key: '',
          option_value_ids: [],
          sale_price_cents: 2000,
          target_stock: 0,
          is_offered: true,
          no_reorder: false,
          on_hand: 0,
          photo_ids: [],
        }],
      }],
    })

    const wrapper = mount(SalesView)
    await flushPromises()
    await button(wrapper, 'Shirt auf Bestellung').trigger('click')

    expect(wrapper.get('.order-only-hint').text()).toContain('sales.onlyOnOrder')
    expect(wrapper.find('.stock-sale-warning').exists()).toBe(false)
    expect(wrapper.get('.till-stock').attributes('title')).toBe('sales.onlyOnOrder')
  })

  it('shows paused articles and variants without allowing them into the cart', async () => {
    assortment.mockResolvedValueOnce({
      payment_methods: ['Bar'],
      articles: [{
        id: 1,
        name: 'Pausiertes Shirt',
        is_active: true,
        is_offered: false,
        configuration_complete: true,
        total_stock: 5,
        option_groups: [],
        variants: [{
          id: 11,
          combination_key: '',
          option_value_ids: [],
          sale_price_cents: 2000,
          target_stock: 10,
          is_offered: false,
          no_reorder: false,
          is_active: true,
          on_hand: 5,
          photo_ids: [],
        }],
      }],
    })

    const wrapper = mount(SalesView)
    await flushPromises()

    expect(wrapper.get('.paused-state').text()).toBe('articles.stockModes.paused.label')
    await button(wrapper, 'Pausiertes Shirt').trigger('click')

    expect(wrapper.get('.paused-variant-hint').text()).toBe('sales.pausedVariant')
    expect(wrapper.get('.till-stock').classes()).toContain('is-paused')
    expect(wrapper.get('.till-stock').attributes('title')).toBe('sales.pausedVariant')
    expect(wrapper.get('.till-add').attributes('disabled')).toBeDefined()
  })

  it('scrolls from an article to its options and back after adding on mobile', async () => {
    const scrollIntoView = vi.fn()
    Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', {
      configurable: true,
      value: scrollIntoView,
    })
    vi.stubGlobal('matchMedia', vi.fn((query: string) => ({
      matches: query === '(max-width: 700px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })))

    const wrapper = mount(SalesView)
    await flushPromises()
    await button(wrapper, 'Testshirt').trigger('click')
    await flushPromises()

    expect(scrollIntoView).toHaveBeenNthCalledWith(1, { behavior: 'smooth', block: 'start' })
    expect(scrollIntoView.mock.instances[0]).toBe(wrapper.get('.till-variant').element)

    await button(wrapper, 'sales.addToCart').trigger('click')
    await flushPromises()

    expect(scrollIntoView).toHaveBeenNthCalledWith(2, { behavior: 'smooth', block: 'start' })
    expect(scrollIntoView.mock.instances[1]).toBe(wrapper.get('.till-articles').element)
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
        { id: 4, name: 'Keine Variante', is_active: true, is_offered: true, configuration_complete: true, total_stock: 1, option_groups: [], variants: [{ id: 14, combination_key: '', option_value_ids: [], sale_price_cents: 100, on_hand: 1, photo_ids: [], is_active: true, is_offered: false, no_reorder: true }] },
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

  it('shows historical controls only when an online manager entered through articles', async () => {
    let wrapper = mount(SalesView)
    await flushPromises()
    expect(wrapper.find('.historical-mode-note').exists()).toBe(false)
    wrapper.unmount()

    route.query = { mode: 'historical' }
    sessionState.capabilities.can_manage_purchases = false
    wrapper = mount(SalesView)
    await flushPromises()
    expect(wrapper.find('.historical-mode-note').exists()).toBe(false)
    expect(routerReplace).toHaveBeenCalledWith({ name: 'sales' })
    wrapper.unmount()

    routerReplace.mockClear()
    sessionState.capabilities.can_manage_purchases = true
    offlineState.online = false
    wrapper = mount(SalesView)
    await flushPromises()
    expect(wrapper.find('.historical-mode-note').exists()).toBe(false)
    expect(routerReplace).toHaveBeenCalledWith({ name: 'sales' })
  })

  it('books a historical event against a non-offered current variant without the offline queue', async () => {
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
          is_active: true,
          is_offered: false,
        }],
      }],
    })

    route.query = { mode: 'historical' }
    const wrapper = mount(SalesView)
    await flushPromises()

    expect(catalogueList).toHaveBeenCalledWith(true)
    expect(wrapper.text()).toContain('Altes Shirt')
    expect(wrapper.text()).toContain('sales.historicalNotOffered')
    await button(wrapper, 'Altes Shirt').trigger('click')
    expect(wrapper.text()).toContain('sales.historicalNotOfferedVariant')
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

  it('books historical misc income with the historical event metadata', async () => {
    events.mockResolvedValue({ events: [{ id: 7, name: 'Archivfestival' }], selected_event_id: 0 })
    catalogueList.mockResolvedValue({ articles: [] })
    route.query = { mode: 'historical' }
    const wrapper = mount(SalesView)
    await flushPromises()

    await button(wrapper, 'sales.specialEntry').trigger('click')
    await wrapper.get('.till-compose select').setValue('misc_income')
    await field(wrapper, 'sales.specialDescription').setValue('Pfand')
    await field(wrapper, 'sales.specialAmount').setValue('9,00')
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
      items: [{ line_type: 'misc_income', description: 'Pfand', amount_cents: 900 }],
      sale_event_id: 7,
      sold_on: '2026-08-27',
      amount_given_cents: 900,
    }))
    expect(queue).not.toHaveBeenCalled()
  })

  it('uses only the current option structure instead of resurrecting retired default variants', async () => {
    catalogueList.mockResolvedValue({
      articles: [
        {
          id: 2, name: 'Cap', is_active: true, is_offered: true,
          configuration_complete: true, total_stock: 8,
          option_groups: [
            { id: 20, name: 'Größe', position: 0, is_active: false, values: [
              { id: 201, value: 'S', position: 0, is_active: false },
            ] },
          ],
          variants: [
            { id: 21, combination_key: '', option_value_ids: [], sale_price_cents: 1800, on_hand: 8, photo_ids: [], is_active: true, is_offered: true },
            { id: 22, combination_key: '201', option_value_ids: [201], sale_price_cents: 1800, on_hand: 0, photo_ids: [], is_active: false, is_offered: false },
          ],
        },
        {
          id: 3, name: 'CD', is_active: true, is_offered: true,
          configuration_complete: true, total_stock: 52,
          option_groups: [
            { id: 30, name: 'Ausgabe', position: 0, is_active: true, values: [
              { id: 301, value: 'Deluxe', position: 0, is_active: true },
              { id: 302, value: 'Single', position: 1, is_active: true },
            ] },
            { id: 31, name: 'Größe', position: 1, is_active: true, values: [
              { id: 311, value: 'XXL', position: 0, is_active: false },
            ] },
          ],
          variants: [
            { id: 31, combination_key: '301', option_value_ids: [301], sale_price_cents: 1200, on_hand: 20, photo_ids: [], is_active: true, is_offered: true },
            { id: 32, combination_key: '302', option_value_ids: [302], sale_price_cents: 800, on_hand: 32, photo_ids: [], is_active: true, is_offered: true },
            { id: 33, combination_key: '311', option_value_ids: [311], sale_price_cents: 800, on_hand: 0, photo_ids: [], is_active: false, is_offered: false },
          ],
        },
      ],
    })
    route.query = { mode: 'historical' }
    const wrapper = mount(SalesView)
    await flushPromises()

    await button(wrapper, 'Cap').trigger('click')
    expect(wrapper.findAll('.option-group')).toHaveLength(0)
    expect(wrapper.get('.till-chosen').text()).toContain('Cap')

    await button(wrapper, 'CD').trigger('click')
    const groups = wrapper.findAll('.option-group')
    expect(groups).toHaveLength(1)
    expect(groups[0]!.text()).toContain('Ausgabe')
    expect(groups[0]!.text()).toContain('Deluxe')
    expect(groups[0]!.text()).toContain('Single')
    expect(wrapper.text()).not.toContain('XXL')
  })

  it('creates historical events without changing the shared live selection', async () => {
    catalogueList.mockResolvedValue({ articles: [{
      id: 1, name: 'Testshirt', is_active: true, is_offered: true,
      configuration_complete: true, total_stock: 12, option_groups: [],
      variants: [{ id: 11, combination_key: '', option_value_ids: [], sale_price_cents: 2000, on_hand: 12, photo_ids: [], is_active: true, is_offered: true }],
    }] })
    createEvent.mockResolvedValue({ id: 9, name: 'Alter Gig' })
    route.query = { mode: 'historical' }
    const wrapper = mount(SalesView)
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
    route.query = { mode: 'historical' }
    const wrapper = mount(SalesView)
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
