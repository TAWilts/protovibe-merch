import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import NotFoundView from './NotFoundView.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/session', () => ({
  useSessionStore: () => ({ isAuthenticated: false, capabilities: null }),
}))
vi.mock('vue-router', () => ({
  RouterLink: { props: ['to'], template: '<a href="#"><slot /></a>' },
}))

describe('NotFoundView accessibility target', () => {
  it('provides the skip-link destination and a useful return action', () => {
    const wrapper = mount(NotFoundView)

    expect(wrapper.get('main').attributes()).toMatchObject({ id: 'main-content', tabindex: '-1' })
    expect(wrapper.get('.not-found-card').text()).toContain('notFound.message')
    expect(wrapper.get('.primary-button').text()).toBe('notFound.backHome')
  })
})
