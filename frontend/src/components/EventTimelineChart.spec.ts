import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import EventTimelineChart from './EventTimelineChart.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, locale: { value: 'de' } }) }))

describe('EventTimelineChart', () => {
  it('renders income and negative profit on opposite sides of the zero line', () => {
    const wrapper = mount(EventTimelineChart, {
      props: {
        points: [{
          key: 'club:2026-01-01', label: 'Clubnacht', date: '2026-01-01', quantity: 2,
          income_cents: 2000, profit_cents: -500,
        }],
      },
    })

    expect(wrapper.findAll('.income-bar')).toHaveLength(1)
    expect(wrapper.findAll('.profit-bar')).toHaveLength(1)
    expect(Number(wrapper.get('.profit-bar').attributes('y'))).toBeGreaterThan(Number(wrapper.get('.income-bar').attributes('y')))
    expect(wrapper.get('table').text()).toContain('Clubnacht')
    expect(wrapper.get('table').text()).toContain('-5,00')
  })
})
