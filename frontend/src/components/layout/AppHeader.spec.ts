import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import AppHeader from './AppHeader.vue'

const { session, route } = vi.hoisted(() => {
  return {
    session: {
      isAuthenticated: true,
      user: { username: 'member', show_packing_list: true, show_product_palette: true },
      capabilities: {
        role_label: 'Verkäufer',
        can_access_band_workflows: true,
        can_access_member_workflows: true,
        can_access_system_administration: false,
        can_manage_articles: false,
        can_use_packing_list: true,
        can_access_band_administration: false,
        sensitive_action_mfa_required: false,
      },
      featureFlags: { offline_sales: true, slideshow: true, packing_list: true },
      supportGrant: null,
      logout: vi.fn(),
    },
    route: { name: 'sales' },
  }
})

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({
  useRoute: () => route,
  useRouter: () => ({ push: vi.fn() }),
  RouterLink: { template: '<a><slot /></a>' },
}))
vi.mock('@/stores/session', () => ({ useSessionStore: () => session }))
vi.mock('@/stores/offline', () => ({
  useOfflineStore: () => ({ online: true, hasQueue: false, syncing: false, queued: 0, sync: vi.fn() }),
}))
vi.mock('@/stores/packing', () => ({
  usePackingStore: () => ({ online: true, syncing: false, queued: 0, sync: vi.fn() }),
}))
vi.mock('@/stores/flash', () => ({
  useFlashStore: () => ({ success: vi.fn(), error: vi.fn() }),
}))
vi.mock('@/components/SupportMessageDialog.vue', () => ({ default: { template: '<span />' } }))

describe('AppHeader navigation', () => {
  beforeEach(() => {
    route.name = 'sales'
    session.user.show_packing_list = true
    session.user.show_product_palette = true
    session.capabilities.can_access_member_workflows = true
  })

  it('places packing between balances and administration for members', () => {
    const wrapper = mount(AppHeader)
    const text = wrapper.text()

    expect(text.indexOf('nav.balances')).toBeLessThan(text.indexOf('nav.packingList'))
    expect(text.indexOf('nav.packingList')).toBeLessThan(text.indexOf('nav.administration'))
  })

  it('hides the global sync chip on the packing-list route', () => {
    route.name = 'packing-list'
    const wrapper = mount(AppHeader)
    expect(wrapper.find('.offline-sync-status').exists()).toBe(false)
  })

  it('honours personal feature visibility for members but not sellers', () => {
    session.user.show_packing_list = false
    session.user.show_product_palette = false
    let wrapper = mount(AppHeader)
    expect(wrapper.text()).not.toContain('nav.packingList')
    expect(wrapper.text()).not.toContain('nav.slideshow')
    wrapper.unmount()

    session.capabilities.can_access_member_workflows = false
    wrapper = mount(AppHeader)
    expect(wrapper.text()).toContain('nav.packingList')
    expect(wrapper.text()).toContain('nav.slideshow')
  })
})
