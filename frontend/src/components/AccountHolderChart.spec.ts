import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import AccountHolderChart from './AccountHolderChart.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'de' } }),
}))

describe('AccountHolderChart', () => {
  it('renders three exact series values and an accessible data table', () => {
    const wrapper = mount(AccountHolderChart, {
      props: {
        points: [
          {
            account_holder_user_id: null,
            account_holder_username: '',
            income_cents: 1000,
            expense_cents: 0,
            difference_cents: 1000,
          },
          {
            account_holder_user_id: 8,
            account_holder_username: 'Alex',
            income_cents: 0,
            expense_cents: 2500,
            difference_cents: -2500,
          },
        ],
      },
    })

    expect(wrapper.findAll('.income-bar')).toHaveLength(2)
    expect(wrapper.findAll('.expense-bar')).toHaveLength(2)
    expect(wrapper.findAll('.difference-bar')).toHaveLength(2)
    expect(wrapper.findAll('.difference-negative-bar')).toHaveLength(1)
    expect(wrapper.findAll('.difference-positive-bar')).toHaveLength(1)
    const zeroLineY = Number(wrapper.get('.zero-line').attributes('y1'))
    for (const bar of wrapper.findAll('rect')) {
      expect(Number(bar.attributes('y'))).toBeLessThan(zeroLineY)
    }
    expect(wrapper.findAll('.bar-value').map((node) => node.text())).toEqual([
      '10,00 €', '0,00 €', '10,00 €', '0,00 €', '25,00 €', '-25,00 €',
    ])
    expect(wrapper.get('table').text()).toContain('bandFinances.bandCash')
    expect(wrapper.get('table').text()).toContain('Alex')
    expect(wrapper.get('.account-holder-chart-scroll').attributes('tabindex')).toBe('0')
  })
})
