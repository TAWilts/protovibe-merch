import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import EmptyState from './EmptyState.vue'
import TableSkeleton from './TableSkeleton.vue'

describe('shared UI states', () => {
  it('renders a practical empty state with an optional next action', () => {
    const wrapper = mount(EmptyState, {
      props: { title: 'Noch keine Artikel', description: 'Lege den ersten Artikel an.' },
      slots: { default: '<button>Artikel anlegen</button>' },
    })

    expect(wrapper.text()).toContain('Noch keine Artikel')
    expect(wrapper.text()).toContain('Lege den ersten Artikel an.')
    expect(wrapper.get('button').text()).toBe('Artikel anlegen')
  })

  it('announces loading while keeping decorative skeleton rows hidden', () => {
    const wrapper = mount(TableSkeleton, {
      props: { label: 'Einkäufe werden geladen', rows: 3, columns: 4 },
    })

    expect(wrapper.attributes('role')).toBe('status')
    expect(wrapper.get('.visually-hidden').text()).toBe('Einkäufe werden geladen')
    expect(wrapper.findAll('.table-skeleton-row')).toHaveLength(3)
    expect(wrapper.get('.table-skeleton-row').findAll('.skeleton-block')).toHaveLength(4)
  })
})
