import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import SlideshowView from './SlideshowView.vue'

const { listPhotos, slideshow, listCatalogue } = vi.hoisted(() => ({
  listPhotos: vi.fn(),
  slideshow: vi.fn(),
  listCatalogue: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'de' } }),
}))
vi.mock('@/stores/flash', () => ({
  useFlashStore: () => ({ success: vi.fn(), error: vi.fn() }),
}))
vi.mock('@/stores/session', () => ({
  useSessionStore: () => ({ capabilities: { can_manage_slideshow: false } }),
}))
vi.mock('@/api/endpoints', () => ({
  catalogueApi: { list: listCatalogue },
  photosApi: {
    list: listPhotos,
    slideshow,
    fileUrl: (id: number) => `/photos/${id}`,
    update: vi.fn(),
    remove: vi.fn(),
    upload: vi.fn(),
    saveSlideshowSettings: vi.fn(),
  },
}))

describe('SlideshowView in POS mode', () => {
  beforeEach(() => {
    listCatalogue.mockReset()
    listPhotos.mockReset().mockResolvedValue({
      photos: [{
        id: 7,
        variant_id: 4,
        article_name: 'POS Shirt',
        variant_label: 'M · Schwarz',
        original_filename: 'shirt.jpg',
        position: 1,
        include_in_slideshow: true,
        show_price: true,
        sale_price_cents: 2000,
        size_bytes: 100,
        created_by_username: 'admin',
      }],
    })
    slideshow.mockReset().mockResolvedValue({
      photos: [],
      collage_show_prices: true,
      collage_interval: 8,
      collage_modes: ['reveal'],
    })
  })

  it('loads photos without the POS-restricted catalogue request and starts playback', async () => {
    const wrapper = mount(SlideshowView)
    await flushPromises()

    expect(listCatalogue).not.toHaveBeenCalled()
    expect(wrapper.get('.photo-card img').attributes('src')).toBe('/photos/7')

    await wrapper.get('.page-title-row .primary-button').trigger('click')
    await flushPromises()

    expect(wrapper.find('.page-shell').exists()).toBe(false)
    expect(wrapper.text()).toContain('POS Shirt')
  })
})
