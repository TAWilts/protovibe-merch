import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import DashboardView from './DashboardView.vue'

const { api, session } = vi.hoisted(() => ({
  api: {
    bands: vi.fn(),
    grants: vi.fn(),
    messages: vi.fn(),
    audit: vi.fn(),
    backups: vi.fn(),
    settings: vi.fn(),
    registrationRequests: vi.fn(),
  },
  session: { capabilities: { is_system_admin: false } },
}))

vi.mock('@/api/endpoints', () => ({ platformApi: api }))
vi.mock('@/stores/session', () => ({ useSessionStore: () => session }))
vi.mock('@/stores/flash', () => ({ useFlashStore: () => ({ error: vi.fn() }) }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ d: () => 'date' }),
}))
vi.mock('vue-router', () => ({
  RouterLink: { props: ['to'], template: '<a href="#"><slot /></a>' },
}))

describe('platform dashboard hierarchy', () => {
  beforeEach(() => {
    api.bands.mockResolvedValue({ bands: [] })
    api.grants.mockResolvedValue({ grants: [] })
    api.messages.mockResolvedValue({ messages: [] })
    api.audit.mockResolvedValue({ entries: [] })
    api.backups.mockResolvedValue({ runs: [] })
    api.settings.mockResolvedValue({
      maintenance_enabled: false,
      maintenance_message: '',
      announcement_text: '',
      announcement_level: 'info',
      announcement_expires_at: null,
    })
    api.registrationRequests.mockClear()
  })

  it('keeps primary metrics prominent without loading protected registration data', async () => {
    const wrapper = mount(DashboardView)
    await flushPromises()

    expect(wrapper.findAll('.primary-metric')).toHaveLength(3)
    expect(wrapper.find('.attention-card').exists()).toBe(true)
    expect(wrapper.find('.system-card').exists()).toBe(true)
    expect(api.registrationRequests).not.toHaveBeenCalled()
  })
})
