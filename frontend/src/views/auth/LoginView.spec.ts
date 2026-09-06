import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import LoginView from './LoginView.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/i18n', () => ({ marketingLocale: () => 'de', setMarketingLocale: vi.fn() }))
vi.mock('@/api/endpoints', () => ({ authApi: {} }))
vi.mock('@/stores/session', () => ({ useSessionStore: () => ({ adopt: vi.fn() }) }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: { band: 'ready-band', username: 'band-admin' } }),
  useRouter: () => ({ replace: vi.fn() }),
  RouterLink: { props: ['to'], template: '<a><slot /></a>' },
}))

describe('LoginView handover link', () => {
  it('prefills band and username from the query string', () => {
    const wrapper = mount(LoginView)
    expect((wrapper.get('input[autocomplete="organization"]').element as HTMLInputElement).value).toBe('ready-band')
    expect((wrapper.get('input[autocomplete="username"]').element as HTMLInputElement).value).toBe('band-admin')
  })
})
