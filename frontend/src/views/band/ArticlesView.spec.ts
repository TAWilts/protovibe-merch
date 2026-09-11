import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import ArticlesView from './ArticlesView.vue'

const { list, create, save, removeIncomplete, routerPush, routeLeaveGuards } = vi.hoisted(() => ({
  list: vi.fn(),
  create: vi.fn(),
  save: vi.fn(),
  removeIncomplete: vi.fn(),
  routerPush: vi.fn(),
  routeLeaveGuards: [] as Array<() => boolean>,
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'de' } }),
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
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: routerPush }),
  onBeforeRouteLeave: (guard: () => boolean) => routeLeaveGuards.push(guard),
}))
vi.mock('@/api/endpoints', () => ({
  catalogueApi: { list, create, save, removeIncomplete },
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
  target_stock: null,
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
  configuration_locked: true,
  total_stock: 0,
  option_groups: [],
  variants,
}

const draftArticle = {
  ...confirmedArticle,
  name: 'Neuer Artikel',
  default_sale_price_cents: 0,
  configuration_complete: false,
  configuration_locked: false,
  variants: [],
}

const configuredOptionsArticle = {
  ...confirmedArticle,
  option_groups: [{
    id: 21,
    name: 'Größe',
    position: 0,
    is_active: true,
    values: [
      { id: 31, value: 'M', position: 0, is_active: true },
      { id: 32, value: 'L', position: 1, is_active: true },
    ],
  }],
  variants: [
    { ...variants[0], option_value_ids: [31], combination_key: '31' },
    { ...variants[1], option_value_ids: [32], combination_key: '32' },
  ],
}

describe('ArticlesView variant generation', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    list.mockReset()
    create.mockReset()
    save.mockReset().mockResolvedValue(confirmedArticle)
    removeIncomplete.mockReset().mockResolvedValue(undefined)
    routerPush.mockReset().mockResolvedValue(undefined)
    routeLeaveGuards.length = 0
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
    expect((wrapper.get('.sale-price-field input').element as HTMLInputElement).value).toBe('')
    expect(wrapper.find('.minimum-for-all').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('articles.retired')
  })

  it('warns before leaving a newly created article with unsaved configuration', async () => {
    list
      .mockResolvedValueOnce({ articles: [] })
      .mockResolvedValue({ articles: [draftArticle] })
    create.mockResolvedValue(draftArticle)
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    const wrapper = mount(ArticlesView)
    await flushPromises()

    await wrapper.get('.article-create-panel input').setValue('Neuer Artikel')
    await wrapper.get('.article-create-panel form').trigger('submit')
    await flushPromises()

    expect(routeLeaveGuards).toHaveLength(1)
    expect(routeLeaveGuards[0]!()).toBe(false)
    expect(confirm).toHaveBeenCalledWith('articles.unfinishedLeave')

    await wrapper.get('.sale-price-field input').setValue('25,00')
    await wrapper.get('.article-form-actions .primary-button').trigger('click')
    await flushPromises()
    confirm.mockClear()

    expect(routeLeaveGuards[0]!()).toBe(true)
    expect(confirm).not.toHaveBeenCalled()
  })

  it('requires a sale price when confirming a new article', async () => {
    list.mockResolvedValue({ articles: [draftArticle] })

    const wrapper = mount(ArticlesView)
    await flushPromises()

    await wrapper.get('.article-form-actions .primary-button').trigger('click')
    await flushPromises()

    expect(save).not.toHaveBeenCalled()
    expect(wrapper.get('#article-sale-price-error').text()).toBe('articles.salePriceRequired')
    expect(wrapper.get('.sale-price-field input').attributes('aria-invalid')).toBe('true')

    await wrapper.get('.sale-price-field input').setValue('25,00')
    expect(wrapper.find('#article-sale-price-error').exists()).toBe(false)
    await wrapper.get('.article-form-actions .primary-button').trigger('click')
    await flushPromises()

    expect(save).toHaveBeenCalledWith(1, expect.objectContaining({
      default_sale_price_cents: 2500,
    }))
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

  it('applies a numeric target stock value to every active variant', async () => {
    list.mockResolvedValue({ articles: [confirmedArticle] })
    const wrapper = mount(ArticlesView)
    await flushPromises()

    await wrapper.get('.target-for-all input').setValue('12')
    await wrapper.get('.target-for-all').trigger('submit')
    await flushPromises()

    expect(save).toHaveBeenCalledWith(1, {
      variants: [
        { id: 11, target_stock: 12 },
        { id: 12, target_stock: 12 },
      ],
    })
  })

  it('applies one stock mode to active variants in a single catalogue save', async () => {
    const retired = { ...variants[0], id: 99, is_active: false }
    list.mockResolvedValue({ articles: [{ ...confirmedArticle, variants: [...variants, retired] }] })
    const wrapper = mount(ArticlesView)
    await flushPromises()

    await wrapper.get('.stock-mode-for-all select').setValue('clearance')
    await flushPromises()

    expect(save).toHaveBeenCalledTimes(1)
    expect(save).toHaveBeenCalledWith(1, {
      is_offered: true,
      variants: [
        { id: 11, is_offered: true, no_reorder: true },
        { id: 12, is_offered: true, no_reorder: true },
      ],
    })
    expect(JSON.stringify(save.mock.calls[0])).not.toContain('99')
  })

  it('shows mixed modes and reopens the article when one variant becomes offered', async () => {
    const mixedArticle = {
      ...confirmedArticle,
      is_offered: false,
      variants: [
        { ...variants[0], is_offered: false, no_reorder: false, target_stock: 8 },
        { ...variants[1], is_offered: false, no_reorder: true, target_stock: 4 },
      ],
    }
    list.mockResolvedValue({ articles: [mixedArticle] })
    const wrapper = mount(ArticlesView)
    await flushPromises()

    expect((wrapper.get('.stock-mode-for-all select').element as HTMLSelectElement).value).toBe('mixed')
    await wrapper.findAll('.stock-mode-cell select')[0]!.setValue('stocked')
    await flushPromises()

    expect(save).toHaveBeenCalledWith(1, {
      is_offered: true,
      variants: [{ id: 11, is_offered: true, no_reorder: false }],
    })
  })

  it('locks the target field for on-demand variants', async () => {
    list.mockResolvedValue({
      articles: [{
        ...confirmedArticle,
        variants: [{ ...variants[0], target_stock: 0 }],
      }],
    })
    const wrapper = mount(ArticlesView)
    await flushPromises()

    const target = wrapper.get('tbody tr .numeric:nth-child(5) input')
    expect(target.attributes()).toHaveProperty('disabled')
    expect(wrapper.text()).toContain('articles.stockModes.on_demand.targetHint')
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

  it('opens legacy drafts with missing option values and deletes them after confirmation', async () => {
    const legacyDraft = {
      ...draftArticle,
      id: 9,
      name: 'zSonstiges',
      option_groups: [{ id: 90, name: 'Option', position: 0, is_active: true, values: null }],
    }
    list.mockResolvedValue({ articles: [legacyDraft] })
    vi.spyOn(window, 'confirm').mockReturnValue(true)

    const wrapper = mount(ArticlesView)
    await flushPromises()

    expect(wrapper.text()).toContain('zSonstiges')
    expect(wrapper.text()).toContain('articles.deleteIncomplete')
    const deleteButton = wrapper.findAll('button').find((entry) => entry.text() === 'articles.deleteIncomplete')
    expect(deleteButton).toBeDefined()
    await deleteButton!.trigger('click')
    await flushPromises()

    expect(removeIncomplete).toHaveBeenCalledWith(9)
    expect(wrapper.text()).not.toContain('zSonstiges')
  })

  it('allows structural deletion before the first save but protects persisted options afterwards', async () => {
    list.mockResolvedValue({ articles: [{ ...configuredOptionsArticle, configuration_locked: false, configuration_complete: false, variants: [] }] })
    let wrapper = mount(ArticlesView)
    await flushPromises()

    expect(wrapper.findAll('.option-editor')).toHaveLength(1)
    await wrapper.get('.option-editor-head .danger-button').trigger('click')
    expect(wrapper.findAll('.option-editor')).toHaveLength(0)
    wrapper.unmount()

    list.mockResolvedValue({ articles: [configuredOptionsArticle] })
    wrapper = mount(ArticlesView)
    await flushPromises()

    expect(wrapper.text()).toContain('articles.optionsLockedHint')
    expect(wrapper.find('.option-editor-head .danger-button').exists()).toBe(false)
    expect(wrapper.find('[aria-label="articles.discardUnsavedValue"]').exists()).toBe(false)

    await wrapper.get('.option-editor-values > .compact-button').trigger('click')
    expect(wrapper.find('[aria-label="articles.discardUnsavedValue"]').exists()).toBe(true)
    await wrapper.get('[aria-label="articles.discardUnsavedValue"]').trigger('click')
    expect(wrapper.find('[aria-label="articles.discardUnsavedValue"]').exists()).toBe(false)
  })

  it('explains the first value of a later option and rehydrates its server ids after saving', async () => {
    list.mockResolvedValue({ articles: [configuredOptionsArticle] })
    const savedWithColour = {
      ...configuredOptionsArticle,
      option_groups: [
        ...configuredOptionsArticle.option_groups,
        {
          id: 22,
          name: 'Farbe',
          position: 1,
          is_active: true,
          values: [{ id: 41, value: 'Schwarz', position: 0, is_active: true }],
        },
      ],
    }
    save.mockResolvedValue(savedWithColour)
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)

    const wrapper = mount(ArticlesView)
    await flushPromises()

    await wrapper.get('.article-form-group-head .secondary-button').trigger('click')
    const newEditor = wrapper.findAll('.option-editor')[1]!
    await newEditor.get('.option-editor-head input').setValue('Farbe')
    await newEditor.get('.option-value-input input').setValue('Schwarz')

    expect(newEditor.text()).toContain('articles.newOptionExistingHint')
    expect(newEditor.text()).toContain('articles.existingConfiguration')
    expect(newEditor.text()).toContain('articles.discardUnsavedOption')

    await wrapper.get('.article-form-actions .primary-button').trigger('click')
    await flushPromises()

    expect(confirm).toHaveBeenCalledWith('articles.newOptionConfirm')
    expect(save.mock.calls[0]?.[1]).toMatchObject({
      option_groups: [
        { id: 21, name: 'Größe', values: [{ id: 31, value: 'M' }, { id: 32, value: 'L' }] },
        { id: 0, name: 'Farbe', values: [{ id: 0, value: 'Schwarz' }] },
      ],
    })

    await wrapper.get('.article-form-actions .primary-button').trigger('click')
    await flushPromises()

    expect(confirm).toHaveBeenCalledTimes(1)
    expect(save.mock.calls[1]?.[1]).toMatchObject({
      option_groups: [
        { id: 21 },
        { id: 22, values: [{ id: 41, value: 'Schwarz' }] },
      ],
    })
  })

  it('does not silently omit an incomplete option from the save payload', async () => {
    list.mockResolvedValue({ articles: [configuredOptionsArticle] })
    const wrapper = mount(ArticlesView)
    await flushPromises()

    await wrapper.get('.article-form-group-head .secondary-button').trigger('click')
    await wrapper.get('.article-form-actions .primary-button').trigger('click')
    await flushPromises()

    expect(save).not.toHaveBeenCalled()
  })
})
