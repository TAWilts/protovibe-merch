import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import SalesView from './SalesView.vue'

const { book } = vi.hoisted(() => ({ book: vi.fn() }))

vi.mock('vue-router', () => ({ useRoute: () => ({ name: 'sales' }) }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'de' } }),
}))
vi.mock('@/stores/flash', () => ({
  useFlashStore: () => ({ success: vi.fn(), error: vi.fn() }),
}))
vi.mock('@/stores/offline', () => ({
  useOfflineStore: () => ({ queue: vi.fn() }),
}))
vi.mock('@/stores/session', () => ({
  useSessionStore: () => ({
    user: { username: 'seller', show_variant_photos: false },
    featureFlags: { payment_qr: true, offline_sales: true },
  }),
}))
vi.mock('@/api/endpoints', () => ({
  catalogueApi: {
    assortment: vi.fn().mockResolvedValue({
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
    }),
  },
  photosApi: { fileUrl: (id: number) => `/photos/${id}` },
  salesApi: {
    events: vi.fn().mockResolvedValue({ events: [], selected_event_id: 0 }),
    receiptPreview: vi.fn().mockResolvedValue({ receipt_id: 'V-1' }),
    paymentQrAvailability: vi.fn().mockResolvedValue({ paypal: false, bank: false }),
    createEvent: vi.fn(),
    createPaymentQrIntent: vi.fn(),
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

describe('SalesView checkout', () => {
  beforeEach(() => {
    book.mockReset().mockResolvedValue({ receipt_id: 'V-1', sale_ids: [1] })
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
    await mobileCartToggle.trigger('click')
    expect(mobileCartToggle.attributes('aria-expanded')).toBe('true')
    expect(wrapper.get('.till-rail').classes()).toContain('is-mobile-open')

    await button(wrapper, 'sales.paymentDetails').trigger('click')
    expect(wrapper.find('.checkout-step-2').exists()).toBe(true)

    await button(wrapper, 'common.confirm').trigger('click')
    expect(wrapper.find('.checkout-step-3').exists()).toBe(true)

    await button(wrapper, 'sales.book').trigger('click')
    await flushPromises()

    expect(book).toHaveBeenCalledOnce()
    expect(wrapper.find('.checkout-step-1').exists()).toBe(true)
    expect(wrapper.text()).toContain('sales.cartEmpty')
  })
})
