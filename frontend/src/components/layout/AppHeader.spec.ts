import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import AppHeader from './AppHeader.vue'

const { session, route, routerReplace, offlineState } = vi.hoisted(() => {
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
      identity: null as any,
      isSandbox: false,
      isDevelopment: false,
      enterSandbox: vi.fn(),
      setSandboxTutorial: vi.fn(),
      logout: vi.fn(),
    },
    route: { name: 'sales' },
    routerReplace: vi.fn(),
    offlineState: {
      online: true,
      hasQueue: false,
      syncing: false,
      queued: 0,
      conflicts: [] as unknown[],
      sync: vi.fn(),
    },
  }
})

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({
  useRoute: () => route,
  useRouter: () => ({ replace: routerReplace }),
  RouterLink: { template: '<a><slot /></a>' },
}))
vi.mock('@/stores/session', () => ({ useSessionStore: () => session }))
vi.mock('@/stores/offline', () => ({
  useOfflineStore: () => offlineState,
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
    session.capabilities.can_manage_articles = false
    session.identity = null
    session.isSandbox = false
    session.isDevelopment = false
    offlineState.online = true
    offlineState.queued = 0
    offlineState.conflicts = []
    session.logout.mockReset().mockResolvedValue(true)
    routerReplace.mockReset().mockResolvedValue(undefined)
  })

  it('places packing between balances and administration for members', () => {
    const wrapper = mount(AppHeader)
    const text = wrapper.text()

    expect(text.indexOf('nav.balances')).toBeLessThan(text.indexOf('nav.packingList'))
    expect(text.indexOf('nav.packingList')).toBeLessThan(text.indexOf('nav.administration'))
  })

  it('keeps the global sync chip visible on the packing-list route', () => {
    route.name = 'packing-list'
    const wrapper = mount(AppHeader)
    expect(wrapper.find('.offline-sync-status').exists()).toBe(true)
  })

  it('prioritises permanently rejected offline sales in the global status', () => {
    offlineState.conflicts = [{ eventId: 'failed-sale' }]

    const wrapper = mount(AppHeader)

    expect(wrapper.get('.offline-sync-status').classes()).toContain('has-conflicts')
    expect(wrapper.get('.offline-sync-label').text()).toBe('sync.conflicts')
  })

  it('labels a development instance as the testsuite', () => {
    session.isDevelopment = true
    const wrapper = mount(AppHeader)

    expect(wrapper.get('.brand-mark').text()).toBe('T')
    expect(wrapper.get('.brand-copy strong').text()).toBe('app.testName')
  })

  it('locks every sandbox tab except the current tutorial task', () => {
    session.isSandbox = true
    session.capabilities.can_manage_articles = true
    session.identity = {
      sandbox: {
        tutorial_visible: true,
        tutorial_state: { catalogue: false, purchase: false, sale: false, balance: false },
      },
    }

    const wrapper = mount(AppHeader)
    const navigation = wrapper.get('.main-nav')

    expect(navigation.findAll('a').map((link) => link.text())).toEqual(['nav.articles'])
    expect(navigation.findAll('.sandbox-nav-locked').map((link) => link.text())).toContain('nav.sales')
    expect(navigation.findAll('.sandbox-nav-locked').map((link) => link.text())).toContain('nav.purchases')
    expect(navigation.findAll('.sandbox-nav-locked').map((link) => link.text())).toContain('nav.balances')
  })

  it('unlocks the sandbox navigation when the tutorial is skipped', () => {
    session.isSandbox = true
    session.capabilities.can_manage_articles = true
    session.identity = {
      sandbox: {
        tutorial_visible: false,
        tutorial_state: { catalogue: false, purchase: false, sale: false, balance: false },
      },
    }

    const wrapper = mount(AppHeader)

    expect(wrapper.find('.sandbox-nav-locked').exists()).toBe(false)
    expect(wrapper.get('.main-nav').findAll('a').length).toBeGreaterThan(1)
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

  it.each(['articles', 'purchases'])('logs out from %s and replaces the protected route with login', async (routeName) => {
    route.name = routeName
    const wrapper = mount(AppHeader)

    await wrapper.findAll('.account-popover button').at(-1)!.trigger('click')

    expect(session.logout).toHaveBeenCalledOnce()
    expect(routerReplace).toHaveBeenCalledWith({ name: 'login' })
  })
})
