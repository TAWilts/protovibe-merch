import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import PurchasesView from './PurchasesView.vue'

const {
  catalogueList,
  purchasesList,
  createPurchase,
  updateReceipt,
  refillSuggestions,
  attachmentList,
  attachmentUpload,
  attachmentRemove,
  downloadCsv,
  routeLeaveGuards,
} = vi.hoisted(() => ({
  catalogueList: vi.fn(),
  purchasesList: vi.fn(),
  createPurchase: vi.fn(),
  updateReceipt: vi.fn(),
  refillSuggestions: vi.fn(),
  attachmentList: vi.fn(),
  attachmentUpload: vi.fn(),
  attachmentRemove: vi.fn(),
  downloadCsv: vi.fn(),
  routeLeaveGuards: [] as Array<() => boolean>,
}))

vi.mock('vue-router', () => ({
  onBeforeRouteLeave: (guard: () => boolean) => routeLeaveGuards.push(guard),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'de' } }),
}))
vi.mock('@/stores/flash', () => ({
  useFlashStore: () => ({ success: vi.fn(), error: vi.fn() }),
}))
vi.mock('@/stores/session', () => ({
  useSessionStore: () => ({ capabilities: { can_manage_purchases: true } }),
}))
vi.mock('@/utils/csvDownload', () => ({
  datedFilename: (base: string) => `${base}.csv`,
  downloadCsv,
}))
vi.mock('@/api/endpoints', () => ({
  catalogueApi: { list: catalogueList },
  salesApi: { receiptPreview: vi.fn().mockResolvedValue({ receipt_id: 'E-2' }) },
  purchasesApi: {
    list: purchasesList.mockResolvedValue({
      editing_enabled: true,
      account_holders: [
        { id: 18, username: 'Kim' },
      ],
      purchases: [{
        id: 1,
        receipt_id: 'E-20260907-001',
        variant_id: 11,
        article_name: 'Testshirt',
        variant_label: 'Größe: M',
        quantity: 2,
        unit_cost_cents: 900,
        total_cost_cents: 1800,
        price_mode: 'unit',
        purchased_on: '2026-09-07',
        supplier: 'Druckerei',
        invoice_reference: 'R-1',
        has_invoice_file: false,
        has_receipt_attachment: true,
        prices_include_vat: true,
        vat_rate_basis_points: 1900,
        shipping_cost_cents: 500,
        account_holder_user_id: 17,
        account_holder_username: 'Alex',
        comment: '',
        is_cancelled: false,
        cancelled_by_username: '',
        created_by_username: 'manager',
      }],
    }),
    lastCost: vi.fn(),
    create: createPurchase,
    updateReceipt,
    cancelReceipt: vi.fn(),
    refillSuggestions,
  },
  attachmentsApi: {
    invoiceUrl: (id: number) => `/invoice/${id}`,
    fileUrl: vi.fn(),
    list: attachmentList,
    upload: attachmentUpload,
    remove: attachmentRemove,
  },
}))

describe('PurchasesView receipt header', () => {
  beforeEach(() => {
    catalogueList.mockReset().mockResolvedValue({ articles: [] })
    purchasesList.mockClear()
    createPurchase.mockReset().mockResolvedValue({ receipt_id: 'E-2', purchase_ids: [1], total_cost_cents: 1000 })
    updateReceipt.mockReset().mockResolvedValue({ receipt_id: 'E-20260907-001', purchase_ids: [1], total_cost_cents: 1800 })
    refillSuggestions.mockReset().mockResolvedValue({ items: [] })
    attachmentList.mockReset().mockResolvedValue({ attachments: [] })
    attachmentUpload.mockReset().mockResolvedValue({ id: 2, original_filename: 'rechnung.pdf', size_bytes: 10 })
    attachmentRemove.mockReset().mockResolvedValue(undefined)
    downloadCsv.mockReset()
    routeLeaveGuards.length = 0
  })

  it('shows attachments and exposes enabled editing without expanding first', async () => {
    const wrapper = mount(PurchasesView)
    await flushPromises()

    const summary = wrapper.get('.purchase-receipt-card summary')
    expect(summary.find('.attachment-indicator').exists()).toBe(true)
    const edit = summary.findAll('button').find((entry) => entry.text() === 'purchases.edit')
    expect(edit).toBeDefined()
    await edit!.trigger('click')
    expect(wrapper.get('.confirmation-dialog').text()).toContain('purchases.editTitle')
    expect(wrapper.text()).toContain('purchases.paidBy: Alex')

    const holderSelect = wrapper.get('.confirmation-dialog select')
    expect(holderSelect.text()).toContain('Alex')
    expect(holderSelect.text()).toContain('Kim')
    expect((holderSelect.element as HTMLSelectElement).value).toBe('17')
    await holderSelect.setValue('18')
    await wrapper.findAll('.confirmation-dialog button')
      .find((entry) => entry.text() === 'common.save')!.trigger('click')
    await flushPromises()

    expect(updateReceipt).toHaveBeenCalledWith(
      'E-20260907-001',
      expect.objectContaining({ account_holder_user_id: 18 }),
    )
  })

  it('hides no-reorder variants but keeps withdrawn reorderable variants', async () => {
    catalogueList.mockResolvedValue({
      articles: [
        {
          id: 1,
          name: 'Teilweise nachbestellbar',
          option_groups: [{
            id: 10,
            name: 'Ausgabe',
            position: 0,
            is_active: true,
            values: [
              { id: 101, value: 'Standard', position: 0, is_active: true },
              { id: 102, value: 'Deluxe', position: 1, is_active: true },
            ],
          }],
          variants: [
            { id: 11, option_value_ids: [101], combination_key: '101', no_reorder: true, is_active: true, is_offered: true },
            { id: 12, option_value_ids: [102], combination_key: '102', no_reorder: false, is_active: false, is_offered: false },
          ],
        },
        {
          id: 2,
          name: 'Komplett ausgemustert',
          option_groups: [],
          variants: [{ id: 21, option_value_ids: [], combination_key: '', no_reorder: true, is_active: true, is_offered: true }],
        },
      ],
    })

    const wrapper = mount(PurchasesView)
    await flushPromises()

    expect(wrapper.text()).toContain('Teilweise nachbestellbar')
    expect(wrapper.text()).not.toContain('Komplett ausgemustert')
    const articleButton = wrapper.findAll('button').find((entry) => entry.text().includes('Teilweise nachbestellbar'))
    expect(articleButton).toBeDefined()
    await articleButton!.trigger('click')
    expect(wrapper.text()).toContain('Deluxe')
    expect(wrapper.text()).not.toContain('Standard')
  })

  it('books a basket total without requiring unit prices', async () => {
    catalogueList.mockResolvedValue({
      articles: [{
        id: 1,
        name: 'Vinyl-Paket',
        total_stock: 0,
        option_groups: [],
        variants: [{
          id: 11, option_value_ids: [], combination_key: '', no_reorder: false,
          is_active: true, is_offered: true, on_hand: 0,
        }],
      }],
    })
    const wrapper = mount(PurchasesView)
    await flushPromises()

    const holderSelect = wrapper.get('.sale-details select')
    expect((holderSelect.element as HTMLSelectElement).selectedIndex).toBe(0)
    expect(holderSelect.text()).toContain('bandFinances.bandCash')
    await holderSelect.setValue('18')
    await wrapper.get('.selection-button').trigger('click')
    const basketMode = wrapper.findAll('.price-mode-switch button')
      .find((entry) => entry.text() === 'purchases.basketPrice')
    await basketMode!.trigger('click')
    await wrapper.findAll('button').find((entry) => entry.text() === 'purchases.addPosition')!.trigger('click')
    await wrapper.get('.basket-price-field input').setValue('100,00')
    await wrapper.findAll('button').find((entry) => entry.text() === 'purchases.book')!.trigger('click')
    await flushPromises()

    expect(createPurchase).toHaveBeenCalledWith(expect.objectContaining({
      price_mode: 'basket',
      goods_total_cents: 10000,
      account_holder_user_id: 18,
      items: [{ variant_id: 11, quantity: 1, unit_cost_cents: 0 }],
    }))
    expect((wrapper.get('.sale-details select').element as HTMLSelectElement).selectedIndex).toBe(0)
  })

  it('exports the receipt payer in the filtered purchase CSV', async () => {
    const wrapper = mount(PurchasesView)
    await flushPromises()

    const filter = wrapper.get('.table-filter input')
    await filter.setValue('Alex')
    expect(wrapper.find('.purchase-receipt-card').exists()).toBe(true)
    await filter.setValue('Kim')
    expect(wrapper.find('.purchase-receipt-card').exists()).toBe(false)
    await filter.setValue('Alex')

    await wrapper.get('.date-range-filter .secondary-button').trigger('click')

    expect(downloadCsv).toHaveBeenCalledTimes(1)
    const [filename, header, rows] = downloadCsv.mock.calls[0]!
    expect(filename).toBe('einkaeufe.csv')
    const holderColumn = header.indexOf('purchases.paidBy')
    expect(holderColumn).toBeGreaterThan(-1)
    expect(rows[0][holderColumn]).toBe('Alex')
  })

  it('warns before leaving with an unfinished purchase basket', async () => {
    catalogueList.mockResolvedValue({
      articles: [{
        id: 1,
        name: 'Vinyl-Paket',
        total_stock: 0,
        option_groups: [],
        variants: [{
          id: 11, option_value_ids: [], combination_key: '', no_reorder: false,
          is_active: true, is_offered: true, on_hand: 0,
        }],
      }],
    })
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    const wrapper = mount(PurchasesView)
    await flushPromises()

    await wrapper.get('.selection-button').trigger('click')
    await wrapper.findAll('.price-mode-switch button')
      .find((entry) => entry.text() === 'purchases.basketPrice')!.trigger('click')
    await wrapper.findAll('button')
      .find((entry) => entry.text() === 'purchases.addPosition')!.trigger('click')

    expect(routeLeaveGuards).toHaveLength(1)
    expect(routeLeaveGuards[0]!()).toBe(false)
    expect(confirm).toHaveBeenCalledWith('purchases.unfinishedLeave')
  })

  it('keeps multiple selected invoice files and uploads them after booking', async () => {
    catalogueList.mockResolvedValue({
      articles: [{
        id: 1,
        name: 'Vinyl-Paket',
        total_stock: 0,
        option_groups: [],
        variants: [{
          id: 11, option_value_ids: [], combination_key: '', no_reorder: false,
          is_active: true, is_offered: true, on_hand: 0,
        }],
      }],
    })
    const wrapper = mount(PurchasesView)
    await flushPromises()

    await wrapper.get('.selection-button').trigger('click')
    await wrapper.findAll('.price-mode-switch button')
      .find((entry) => entry.text() === 'purchases.basketPrice')!.trigger('click')
    await wrapper.findAll('button')
      .find((entry) => entry.text() === 'purchases.addPosition')!.trigger('click')
    await wrapper.get('.basket-price-field input').setValue('100,00')

    const files = [
      new File(['one'], 'rechnung-1.pdf', { type: 'application/pdf' }),
      new File(['two'], 'entfernen.pdf', { type: 'application/pdf' }),
      new File(['three'], 'rechnung-2.pdf', { type: 'application/pdf' }),
    ]
    const fileInput = wrapper.get('.purchase-invoice-picker input[type="file"]')
    Object.defineProperty(fileInput.element, 'files', { configurable: true, value: files })
    await fileInput.trigger('change')
    expect(wrapper.findAll('.pending-invoice-list li')).toHaveLength(3)

    await wrapper.findAll('.pending-invoice-list button')[1].trigger('click')
    expect(wrapper.findAll('.pending-invoice-list li')).toHaveLength(2)
    await wrapper.findAll('button').find((entry) => entry.text() === 'purchases.book')!.trigger('click')
    await flushPromises()

    expect(attachmentUpload.mock.calls).toEqual([
      ['E-2', files[0]],
      ['E-2', files[2]],
    ])
  })

  it('keeps successful uploads and retries only failed invoice files', async () => {
    catalogueList.mockResolvedValue({
      articles: [{
        id: 1,
        name: 'Vinyl-Paket',
        total_stock: 0,
        option_groups: [],
        variants: [{
          id: 11, option_value_ids: [], combination_key: '', no_reorder: false,
          is_active: true, is_offered: true, on_hand: 0,
        }],
      }],
    })
    attachmentUpload
      .mockResolvedValueOnce({ id: 2, original_filename: 'rechnung-1.pdf', size_bytes: 10 })
      .mockRejectedValueOnce(new Error('upload failed'))
      .mockResolvedValue({ id: 3, original_filename: 'rechnung-2.pdf', size_bytes: 10 })
    const wrapper = mount(PurchasesView)
    await flushPromises()

    await wrapper.get('.selection-button').trigger('click')
    await wrapper.findAll('.price-mode-switch button')
      .find((entry) => entry.text() === 'purchases.basketPrice')!.trigger('click')
    await wrapper.findAll('button')
      .find((entry) => entry.text() === 'purchases.addPosition')!.trigger('click')
    await wrapper.get('.basket-price-field input').setValue('100,00')

    const files = [
      new File(['one'], 'rechnung-1.pdf', { type: 'application/pdf' }),
      new File(['two'], 'rechnung-2.pdf', { type: 'application/pdf' }),
    ]
    const fileInput = wrapper.get('.purchase-invoice-picker input[type="file"]')
    Object.defineProperty(fileInput.element, 'files', { configurable: true, value: files })
    await fileInput.trigger('change')
    await wrapper.findAll('button').find((entry) => entry.text() === 'purchases.book')!.trigger('click')
    await flushPromises()

    const failureNotice = wrapper.get('.confirmation-dialog .notice.error')
    expect(failureNotice.text()).toContain('rechnung-2.pdf')
    expect(attachmentUpload).toHaveBeenCalledTimes(2)

    await failureNotice.get('button').trigger('click')
    await flushPromises()
    expect(attachmentUpload).toHaveBeenCalledTimes(3)
    expect(attachmentUpload.mock.calls[2]).toEqual(['E-2', files[1]])
    expect(wrapper.find('.confirmation-dialog .notice.error').exists()).toBe(false)
  })

  it('uploads several files selected in the existing receipt dialog', async () => {
    const wrapper = mount(PurchasesView)
    await flushPromises()

    await wrapper.findAll('.receipt-actions button')
      .find((entry) => entry.text() === 'purchases.invoiceAndAttachments')!.trigger('click')
    await flushPromises()

    const files = [
      new File(['one'], 'nachtrag-1.pdf', { type: 'application/pdf' }),
      new File(['two'], 'nachtrag-2.pdf', { type: 'application/pdf' }),
    ]
    const fileInput = wrapper.get('input[type="file"][hidden]')
    Object.defineProperty(fileInput.element, 'files', { configurable: true, value: files })
    await fileInput.trigger('change')
    await flushPromises()

    expect(attachmentUpload.mock.calls).toEqual([
      ['E-20260907-001', files[0]],
      ['E-20260907-001', files[1]],
    ])
  })

  it('turns refill suggestions into a validated unit-price basket', async () => {
    refillSuggestions.mockResolvedValue({ items: [{
      article_id: 1,
      variant_id: 11,
      article_name: 'Shirt',
      variant_label: 'Größe: M',
      on_hand: 2,
      target_stock: 7,
      suggested_quantity: 5,
      last_unit_cost_cents: 800,
    }] })
    const wrapper = mount(PurchasesView)
    await flushPromises()

    await wrapper.findAll('.purchase-title-actions button')
      .find((entry) => entry.text() === 'purchases.refill')!.trigger('click')
    await flushPromises()
    expect(wrapper.get('.refill-row input[type="number"]').element).toHaveProperty('value', '5')
    await wrapper.findAll('.confirmation-dialog button')
      .find((entry) => entry.text() === 'purchases.takeAsCart')!.trigger('click')

    expect(wrapper.find('.confirmation-dialog').exists()).toBe(false)
    expect(wrapper.get('.cart-item').text()).toContain('Shirt')
    expect(wrapper.get('.cart-item').text()).toContain('5 ×')
  })
})
