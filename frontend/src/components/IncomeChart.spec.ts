import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import IncomeChart from './IncomeChart.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'de' } }),
}))

describe('IncomeChart', () => {
  it('labels every positive and zero bar with its exact value', () => {
    const wrapper = mount(IncomeChart, {
      props: {
        points: [
          { date: '2026-09-10', income_cents: 1250, sale_count: 2 },
          { date: '2026-09-11', income_cents: 0, sale_count: 0 },
        ],
      },
    })

    expect(wrapper.findAll('rect')).toHaveLength(2)
    expect(wrapper.get('.zero-line').exists()).toBe(true)
    expect(wrapper.findAll('.bar-value').map((node) => node.text())).toEqual([
      '12,50 €', '0,00 €',
    ])
    expect(wrapper.get('.income-chart-scroll').attributes('tabindex')).toBe('0')
    expect(wrapper.get('table').text()).toContain('2026-09-10')
    expect(wrapper.get('table').text()).toContain('12,50')
  })
})
