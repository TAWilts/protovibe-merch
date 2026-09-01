import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import AppHeader from './AppHeader.vue'

const { setPosMode, session } = vi.hoisted(() => {
  const setPosMode = vi.fn()
  return {
    setPosMode,
    session: {
      isAuthenticated: true,
      posMode: true,
      user: { username: 'seller' },
      capabilities: {
        role_label: 'Verkäufer',
        can_access_band_workflows: true,
        can_access_member_workflows: false,
        can_access_system_administration: false,
        can_manage_articles: false,
        can_access_band_administration: false,
        sensitive_action_mfa_required: false,
      },
      featureFlags: { offline_sales: true, slideshow: true },
      supportGrant: null,
      setPosMode,
      logout: vi.fn(),
    },
  }
})

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ name: 'sales' }),
  useRouter: () => ({ push: vi.fn() }),
  RouterLink: { template: '<a><slot /></a>' },
}))
vi.mock('@/stores/session', () => ({ useSessionStore: () => session }))
vi.mock('@/stores/offline', () => ({
  useOfflineStore: () => ({ online: true, hasQueue: false, syncing: false, queued: 0, sync: vi.fn() }),
}))
vi.mock('@/stores/flash', () => ({
  useFlashStore: () => ({ success: vi.fn(), error: vi.fn() }),
}))
vi.mock('@/components/SupportMessageDialog.vue', () => ({ default: { template: '<span />' } }))

describe('AppHeader POS exit', () => {
  beforeEach(() => setPosMode.mockReset().mockResolvedValue(undefined))

  it('asks for the current password before leaving POS mode', async () => {
    const wrapper = mount(AppHeader)

    await wrapper.get('.pos-mode-button').trigger('click')
    expect(wrapper.find('.confirmation-dialog').exists()).toBe(true)
    expect(setPosMode).not.toHaveBeenCalled()

    await wrapper.get('.confirmation-dialog input[type="password"]').setValue('richtiges-passwort')
    await wrapper.get('.confirmation-dialog form').trigger('submit')
    await flushPromises()

    expect(setPosMode).toHaveBeenCalledOnce()
    expect(setPosMode).toHaveBeenCalledWith(false, 'richtiges-passwort', '')
    expect(wrapper.find('.confirmation-dialog').exists()).toBe(false)
  })
})
