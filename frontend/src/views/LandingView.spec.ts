import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import LandingView from './LandingView.vue'

const { config, create, status, claim, session, setMarketingLocale } = vi.hoisted(() => ({
  config: vi.fn(),
  create: vi.fn(),
  status: vi.fn(),
  claim: vi.fn(),
  session: {
    isAuthenticated: false,
    capabilities: null as null | { is_platform_staff: boolean; can_access_band_workflows: boolean },
    supportGrant: null,
  },
  setMarketingLocale: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, d: (value: unknown) => String(value) }),
}))
vi.mock('@/i18n', () => ({
  marketingLocale: () => 'de',
  setMarketingLocale,
}))
vi.mock('vue-router', () => ({
  RouterLink: { props: ['to'], template: '<a><slot /></a>' },
}))
vi.mock('@/stores/session', () => ({
  useSessionStore: () => session,
}))
vi.mock('@/api/endpoints', () => ({ registrationApi: { config, create, status, claim } }))

describe('LandingView registration', () => {
  beforeEach(() => {
    window.localStorage.clear()
    window.history.replaceState(null, '', '/')
    config.mockReset().mockResolvedValue({ registration_enabled: true })
    create.mockReset()
    status.mockReset()
    claim.mockReset()
    setMarketingLocale.mockClear()
    session.isAuthenticated = false
    session.capabilities = null
  })

  it('stores the secret status token and renders a pending request', async () => {
    create.mockResolvedValue({
      reference: 'REG-TEST', status: 'pending', expires_at: '2026-10-01T00:00:00Z',
      status_url: 'https://merch.example.org/#registration=secret-token',
    })
    status.mockResolvedValue({
      reference: 'REG-TEST', status: 'pending', band_name: 'Example Band',
      band_slug: 'example-band', admin_username: 'merch-admin', contact_email: 'band@example.org',
      decision_note: '', credentials_available: false, credentials_retrieved: false,
      credentials_available_until: null, expires_at: '2026-10-01T00:00:00Z',
    })

    const wrapper = mount(LandingView)
    await flushPromises()
    await wrapper.get('input[autocomplete="organization"]').setValue('Example Band')
    await wrapper.get('.slug-field input').setValue('example-band')
    await wrapper.get('input[autocomplete="username"]').setValue('merch-admin')
    await wrapper.get('input[type="email"]').setValue('band@example.org')
    await wrapper.get('.privacy-check input').setValue(true)
    await wrapper.get('.registration-form').trigger('submit')
    await flushPromises()

    expect(create).toHaveBeenCalledWith(expect.objectContaining({
      band_name: 'Example Band', band_slug: 'example-band', privacy_accepted: true,
    }))
    expect(status).toHaveBeenCalledWith('secret-token')
    expect(window.localStorage.getItem('merch-registration-token')).toBe('secret-token')
    expect(wrapper.find('.status-panel').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('setup_code')
  })

  it('claims approved credentials once and forgets the resume token', async () => {
    window.history.replaceState(null, '', '/#registration=approved-token')
    status.mockResolvedValue({
      reference: 'REG-READY', status: 'approved', band_name: 'Ready Band',
      band_slug: 'ready-band', admin_username: 'band-admin', contact_email: 'band@example.org',
      decision_note: '', credentials_available: true, credentials_retrieved: false,
      credentials_available_until: '2026-09-16T00:00:00Z', expires_at: '2026-09-16T00:00:00Z',
    })
    claim.mockResolvedValue({
      band_slug: 'ready-band', username: 'band-admin', setup_code: 'ABCD-EFGH-JKLM',
      setup_code_expires_at: '2026-09-16T00:00:00Z',
    })

    const wrapper = mount(LandingView)
    await flushPromises()
    await wrapper.get('.status-panel .landing-button-primary').trigger('click')
    await flushPromises()

    expect(claim).toHaveBeenCalledWith('approved-token')
    expect(wrapper.get('.setup-code').text()).toContain('ABCD-EFGH-JKLM')
    expect(window.localStorage.getItem('merch-registration-token')).toBeNull()
    expect(window.location.hash).toBe('')
  })

  it('shows the friendly disabled state instead of the request form', async () => {
    config.mockResolvedValue({ registration_enabled: false })
    const wrapper = mount(LandingView)
    await flushPromises()

    expect(wrapper.find('.registration-unavailable').exists()).toBe(true)
    expect(wrapper.find('.registration-form').exists()).toBe(false)
  })

  it('switches the marketing language and sends platform staff to administration', async () => {
    session.isAuthenticated = true
    session.capabilities = { is_platform_staff: true, can_access_band_workflows: false }
    const wrapper = mount(LandingView)
    await flushPromises()

    expect(wrapper.get('.landing-actions').text()).toContain('landing.nav.toAdmin')
    await wrapper.findAll('.landing-locale button')[1].trigger('click')
    expect(setMarketingLocale).toHaveBeenLastCalledWith('en')
  })
})
