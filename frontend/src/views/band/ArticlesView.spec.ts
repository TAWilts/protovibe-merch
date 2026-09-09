import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import ArticlesView from './ArticlesView.vue'

const { list, create, save, routerPush } = vi.hoisted(() => ({
  list: vi.fn(),
  create: vi.fn(),
  save: vi.fn(),
  routerPush: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('@/stores/flash', () => ({
  useFlashStore: () => ({ success: vi.fn(), error: vi.fn() }),
}))
vi.mock('@/stores/session', () => ({
  useSessionStore: () => ({
    featureFlags: { csv_import: true },
    capabilities: { can_manage_purchases: true },
  }),
}))
vi.mock('@/stores/offline', () => ({ useOfflineStore: () => ({ online: true }) }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: routerPush }) }))
vi.mock('@/api/endpoints', () => ({
  catalogueApi: { list, create, save },
  photosApi: {
    fileUrl: (id: number) => `/photos/${id}`,
    list: vi.fn().mockResolvedValue({ photos: [] }),
    upload: vi.fn(),
    remove: vi.fn(),
  },
  importApi: { preview: vi.fn(), apply: vi.fn() },
}))

const variants = [11, 12].map((id) => ({
  id,
  option_value_ids: [],
  combination_key: '',
  sale_price_cents: 2000,
  minimum_stock: null,
  is_offered: true,
  no_reorder: false,
  is_active: true,
  purchased: 0,
  sold: 0,
  on_hand: 0,
  below_minimum: false,
  photo_ids: [],
}))

const confirmedArticle = {
  id: 1,
  name: 'Testshirt',
  default_sale_price_cents: 2000,
  is_offered: true,
  is_active: true,
  configuration_complete: true,
  total_stock: 0,
  option_groups: [],
  variants,
}

const draftArticle = {
  ...confirmedArticle,
  name: 'Neuer Artikel',
  configuration_complete: false,
  variants: [],
}

describe('ArticlesView variant generation', () => {
  beforeEach(() => {
    list.mockReset()
    create.mockReset()
    save.mockReset().mockResolvedValue(confirmedArticle)
    routerPush.mockReset().mockResolvedValue(undefined)
  })

  it('keeps the variant result hidden until a new article is confirmed', async () => {
    list
      .mockResolvedValueOnce({ articles: [] })
      .mockResolvedValue({ articles: [draftArticle] })
    create.mockResolvedValue(draftArticle)

    const wrapper = mount(ArticlesView)
    await flushPromises()

    await wrapper.get('.article-create-panel input').setValue('Neuer Artikel')
    await wrapper.get('.article-create-panel form').trigger('submit')
    await flushPromises()

    expect(create).toHaveBeenCalledWith({
      name: 'Neuer Artikel',
      default_sale_price_cents: 0,
      defer_variants: true,
    })
    expect(wrapper.find('.minimum-for-all').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('articles.retired')
  })

  it('applies a numeric minimum stock value to every active variant', async () => {
    list.mockResolvedValue({ articles: [confirmedArticle] })

    const wrapper = mount(ArticlesView)
    await flushPromises()

    expect(wrapper.get('.selection-button').classes()).toContain('selected')
    expect(wrapper.get('.selection-button').attributes('aria-pressed')).toBe('true')

    await wrapper.get('.minimum-for-all input').setValue('7')
    await wrapper.get('.minimum-for-all').trigger('submit')
    await flushPromises()

    expect(save).toHaveBeenCalledWith(1, {
      variants: [
        { id: 11, minimum_stock: 7 },
        { id: 12, minimum_stock: 7 },
      ],
    })
  })

  it('does not expose the legacy CSV import even when its dormant flag is enabled', async () => {
    list.mockResolvedValue({ articles: [confirmedArticle] })
    const wrapper = mount(ArticlesView)
    await flushPromises()

    expect(wrapper.find('input[type="file"][accept*="csv"]').exists()).toBe(false)
    expect(wrapper.find('.transaction-import-panel').exists()).toBe(false)
  })

  it('keeps article creation above the catalogue and starts historical entry from the old header action', async () => {
    list.mockResolvedValue({ articles: [confirmedArticle] })
    const wrapper = mount(ArticlesView)
    await flushPromises()

    const createPanel = wrapper.get('.article-create-panel')
    const catalogue = wrapper.get('.article-layout')
    expect(createPanel.element.compareDocumentPosition(catalogue.element) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()

    await wrapper.get('.page-title-row .secondary-button').trigger('click')
    expect(routerPush).toHaveBeenCalledWith({ name: 'sales', query: { mode: 'historical' } })
  })
})
