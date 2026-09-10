import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import AppShell from './AppShell.vue'
import PlatformShell from './PlatformShell.vue'

const { session, routerReplace } = vi.hoisted(() => ({
  session: {
    user: { username: 'admin' },
    band: { name: 'Band' },
    capabilities: {
      role_label: 'Admin',
      is_system_admin: true,
      can_access_band_workflows: true,
    },
    supportGrant: null,
    logout: vi.fn(),
  },
  routerReplace: vi.fn(),
}))

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({
  RouterLink: { name: 'RouterLink', props: ['to'], template: '<a><slot /></a>' },
  RouterView: { name: 'RouterView', template: '<div data-router-view />' },
  useRoute: () => ({ name: 'platform-dashboard' }),
  useRouter: () => ({ replace: routerReplace }),
}))
vi.mock('@/stores/session', () => ({ useSessionStore: () => session }))

beforeEach(() => {
  session.logout.mockReset().mockResolvedValue(true)
  routerReplace.mockReset().mockResolvedValue(undefined)
})

describe.each([
  ['band shell', AppShell],
  ['platform shell', PlatformShell],
])('%s accessibility target', (_name, component) => {
  it('places a focusable target directly before the unchanged router outlet', () => {
    const wrapper = mount(component, {
      global: {
        stubs: {
          AppHeader: true,
          FlashStack: true,
          SupportGrantBanner: true,
          SystemStatusBanner: true,
        },
      },
    })
    const target = wrapper.get('#main-content')

    expect(target.attributes('tabindex')).toBe('-1')
    expect(target.element.nextElementSibling?.hasAttribute('data-router-view')).toBe(true)
  })
})

describe('platform shell logout', () => {
  it('replaces the current admin view with login', async () => {
    const wrapper = mount(PlatformShell, {
      global: {
        stubs: {
          FlashStack: true,
          SupportGrantBanner: true,
          RouterView: true,
        },
      },
    })
    await wrapper.get('.account-popover button').trigger('click')

    expect(session.logout).toHaveBeenCalledOnce()
    expect(routerReplace).toHaveBeenCalledWith({ name: 'login' })
  })
})
