import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import ProfileView from './ProfileView.vue'

const { get, personalization, session } = vi.hoisted(() => ({
  get: vi.fn(),
  personalization: vi.fn(),
  session: {
    band: { name: 'Testband' },
    capabilities: {
      role_label: 'Member',
      sensitive_action_mfa_required: false,
      is_platform_staff: false,
      mfa_required: false,
    },
    restore: vi.fn(),
    adopt: vi.fn(),
  },
}))

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/stores/session', () => ({ useSessionStore: () => session }))
vi.mock('@/stores/flash', () => ({ useFlashStore: () => ({ success: vi.fn(), error: vi.fn() }) }))
vi.mock('@/api/endpoints', () => ({
  profileApi: {
    get,
    personalization,
    reauth: vi.fn(),
    telemetry: vi.fn(),
    changePassword: vi.fn(),
    changeUsername: vi.fn(),
    changeContactEmail: vi.fn(),
    startMfa: vi.fn(),
    confirmMfa: vi.fn(),
    disableMfa: vi.fn(),
    regenerateRecoveryCodes: vi.fn(),
  },
}))

describe('ProfileView theme selection', () => {
  beforeEach(() => {
    session.restore.mockReset().mockResolvedValue(undefined)
    get.mockReset().mockResolvedValue({
      profile: {
        user: {
          id: 4,
          username: 'member',
          role: 'member',
          ui_theme: 'midnight',
          ui_language: 'de',
          show_variant_photos: true,
          show_packing_list: true,
          show_product_palette: true,
          telemetry_enabled: false,
          telemetry_decided: true,
          mfa_enabled: false,
          contact_email: '',
        },
      },
      available_themes: ['aurora', 'ocean', 'sunset', 'forest', 'midnight'],
      available_languages: ['de', 'en'],
      last_login_at: null,
      mfa_enrolled_at: null,
      recovery_codes_left: 0,
    })
    personalization.mockReset().mockImplementation(async (payload: { ui_theme?: string }) => ({
      ui_theme: payload.ui_theme ?? 'midnight',
      ui_language: 'de',
      show_variant_photos: true,
    }))
  })

  it('keeps the selected theme in sync and allows selecting midnight again', async () => {
    const wrapper = mount(ProfileView)
    await flushPromises()
    const select = wrapper.get('select')

    await select.setValue('ocean')
    await flushPromises()
    expect((select.element as HTMLSelectElement).value).toBe('ocean')
    expect(document.documentElement.dataset.theme).toBe('ocean')

    await select.setValue('midnight')
    await flushPromises()
    expect((select.element as HTMLSelectElement).value).toBe('midnight')
    expect(document.documentElement.dataset.theme).toBe('midnight')
  })
})
