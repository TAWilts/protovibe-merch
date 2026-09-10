import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import SandboxBanner from './SandboxBanner.vue'

const { session, replace, flash } = vi.hoisted(() => ({
  session: {
    identity: { sandbox: { expires_at: new Date(Date.now() + 3_600_000).toISOString() } },
    user: { role: 'band_admin' },
    capabilities: { is_platform_staff: false },
    setSandboxRole: vi.fn(), resetSandbox: vi.fn(), leaveSandbox: vi.fn(),
  },
  replace: vi.fn(),
  flash: { success: vi.fn(), error: vi.fn() },
}))

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({ useRouter: () => ({ replace }) }))
vi.mock('@/stores/session', () => ({ useSessionStore: () => session }))
vi.mock('@/stores/flash', () => ({ useFlashStore: () => flash }))

describe('SandboxBanner', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    session.leaveSandbox.mockResolvedValue(true)
    session.setSandboxRole.mockResolvedValue(undefined)
    session.resetSandbox.mockResolvedValue(undefined)
    vi.spyOn(window, 'confirm').mockReturnValue(true)
  })

  it('is permanently labelled and exposes role, reset and leave actions', async () => {
    const wrapper = mount(SandboxBanner)
    expect(wrapper.text()).toContain('sandbox.banner')
    await wrapper.get('select').setValue('member')
    await flushPromises()
    expect(session.setSandboxRole).toHaveBeenCalledWith('member')

    await wrapper.findAll('button')[0].trigger('click')
    await flushPromises()
    expect(session.resetSandbox).toHaveBeenCalledOnce()

    await wrapper.findAll('button')[1].trigger('click')
    await flushPromises()
    expect(session.leaveSandbox).toHaveBeenCalledOnce()
    expect(replace).toHaveBeenLastCalledWith({ name: 'sales' })
  })
})
