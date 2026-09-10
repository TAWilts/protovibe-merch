import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import LoginView from './LoginView.vue'

const { login, routeQuery } = vi.hoisted(() => ({
  login: vi.fn(),
  routeQuery: { value: {} as Record<string, string> },
}))

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/i18n', () => ({ marketingLocale: () => 'de', setMarketingLocale: vi.fn() }))
vi.mock('@/api/endpoints', () => ({ authApi: { login } }))
vi.mock('@/stores/session', () => ({ useSessionStore: () => ({ adopt: vi.fn() }) }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: routeQuery.value }),
  useRouter: () => ({ replace: vi.fn() }),
  RouterLink: { props: ['to'], template: '<a><slot /></a>' },
}))

describe('LoginView handover link', () => {
  beforeEach(() => {
    localStorage.clear()
    routeQuery.value = { band: 'ready-band', username: 'band-admin' }
    login.mockReset().mockResolvedValue({ needs_mfa: true, pending_token: 'pending' })
  })

  it('prefills band and username from the query string', () => {
    const wrapper = mount(LoginView)
    expect(wrapper.get('main').attributes()).toMatchObject({ id: 'main-content', tabindex: '-1' })
    expect(wrapper.find('.login-context').exists()).toBe(true)
    expect(wrapper.find('.login-card').exists()).toBe(true)
    expect((wrapper.get('input[autocomplete="organization"]').element as HTMLInputElement).value).toBe('ready-band')
    expect((wrapper.get('input[autocomplete="username"]').element as HTMLInputElement).value).toBe('band-admin')
  })

  it('loads remembered names while query parameters retain priority', () => {
    localStorage.setItem('protovibe.remembered-login.v1', JSON.stringify({ band: 'saved-band', username: 'saved-user' }))
    routeQuery.value = { username: 'link-user' }
    const wrapper = mount(LoginView)

    expect((wrapper.get('input[autocomplete="organization"]').element as HTMLInputElement).value).toBe('saved-band')
    expect((wrapper.get('input[autocomplete="username"]').element as HTMLInputElement).value).toBe('link-user')
    expect(wrapper.get('.remember-login').attributes('aria-pressed')).toBe('true')
  })

  it('stores only band and username after accepted credentials and clears them when disabled', async () => {
    const wrapper = mount(LoginView)
    await wrapper.get('input[type="password"]').setValue('top-secret')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    const stored = localStorage.getItem('protovibe.remembered-login.v1') ?? ''
    expect(stored).toContain('ready-band')
    expect(stored).toContain('band-admin')
    expect(stored).not.toContain('top-secret')

    wrapper.unmount()
    const rememberedWrapper = mount(LoginView)
    await rememberedWrapper.get('.remember-login').trigger('click')
    await flushPromises()
    expect(localStorage.getItem('protovibe.remembered-login.v1')).toBeNull()
  })
})
