import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import SalesView from './SalesView.vue'

const { assortment, book } = vi.hoisted(() => ({ assortment: vi.fn(), book: vi.fn() }))

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
  }),
}))
vi.mock('@/api/endpoints', () => ({
  catalogueApi: {
    assortment,
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
})
