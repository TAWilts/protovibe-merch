import { mount } from '@vue/test-utils'
import { ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'

import BalanceTable from './BalanceTable.vue'
import type { BalanceRow, StockMode } from '@/api/types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, locale: ref('de') }) }))

function row(id: number, stockMode: StockMode): BalanceRow {
  return {
    variant_id: id, article_id: 1, article_name: `Artikel ${id}`, variant_label: 'M',
    purchased: 2, sold: 1, on_hand: 1, minimum_stock: 1, below_minimum: true,
    purchase_cost_cents: 1000, revenue_cents: 2000, collected_cents: 2000,
    discount_cents: 0, donation_cents: 0, sale_price_cents: 2000,
    stock_mode: stockMode, is_offered: true, is_available_for_sale: true,
    no_reorder: false, is_active: true,
  }
}

describe('BalanceTable stock mode', () => {
  it('replaces the two technical booleans with one sortable status column', async () => {
    const modes: StockMode[] = ['stocked', 'on_demand', 'clearance', 'paused', 'discontinued']
    const wrapper = mount(BalanceTable, {
      props: { rows: modes.map((mode, index) => row(index + 1, mode)), grouped: false, sortKey: null, sortDirection: 'default', emptyMessage: '' },
    })

    const headings = wrapper.findAll('th').map((cell) => cell.text())
    expect(headings).toContain('articles.stockMode')
    expect(headings).not.toContain('balances.reorder')
    expect(headings).not.toContain('balances.offered')
    for (const mode of modes) expect(wrapper.text()).toContain(`articles.stockModes.${mode}.label`)

    await wrapper.findAll('th').find((cell) => cell.text() === 'articles.stockMode')!.get('button').trigger('click')
    expect(wrapper.emitted('sort')).toContainEqual(['stock_mode'])
  })
})
