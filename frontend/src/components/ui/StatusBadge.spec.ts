import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import StatusBadge from './StatusBadge.vue'

describe('StatusBadge', () => {
  it('exposes a consistent semantic tone without changing its label', () => {
    const wrapper = mount(StatusBadge, {
      props: { tone: 'warning' },
      slots: { default: 'Needs attention' },
    })

    expect(wrapper.text()).toBe('Needs attention')
    expect(wrapper.attributes('data-tone')).toBe('warning')
    expect(wrapper.get('.app-status-marker').attributes('aria-hidden')).toBe('true')
  })
})
