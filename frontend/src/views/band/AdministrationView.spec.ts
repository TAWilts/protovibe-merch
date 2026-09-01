import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import AdministrationView from './AdministrationView.vue'

const { paymentQrSettings, savePaymentQrSettings } = vi.hoisted(() => ({
  paymentQrSettings: vi.fn(),
  savePaymentQrSettings: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
    d: (value: unknown) => String(value),
  }),
}))
vi.mock('@/stores/flash', () => ({
  useFlashStore: () => ({ success: vi.fn(), error: vi.fn() }),
}))
vi.mock('@/stores/session', () => ({
  useSessionStore: () => ({
    featureFlags: { payment_qr: true },
    capabilities: { sensitive_action_mfa_required: false },
  }),
}))
vi.mock('@/api/endpoints', () => ({
  bandAdminApi: { grants: vi.fn().mockResolvedValue({ grants: [] }) },
  bandUsersApi: {
    list: vi.fn().mockResolvedValue({ users: [], assignable_roles: [] }),
    paymentQrSettings,
    savePaymentQrSettings,
  },
}))

describe('AdministrationView payment QR settings', () => {
  beforeEach(() => {
    paymentQrSettings.mockReset().mockResolvedValue({
      paypal_me_url: '',
      bank_account_holder: '',
      bank_iban: '',
      bank_bic: '',
    })
    savePaymentQrSettings.mockReset().mockImplementation(async (payload) => payload)
  })

  it('keeps the PayPal.Me prefix fixed and submits only the account name as a full URL', async () => {
    const wrapper = mount(AdministrationView)
    await flushPromises()

    const paypal = wrapper.get('.paypal-me-input')
    expect(paypal.text()).toContain('https://paypal.me/')
    expect(wrapper.findAll('.admin-payment-qr-settings input')).toHaveLength(4)

    await paypal.get('input').setValue('protovibe')
    await wrapper.get('.admin-payment-qr-settings form').trigger('submit')
    await flushPromises()

    expect(savePaymentQrSettings).toHaveBeenCalledWith({
      paypal_me_url: 'https://paypal.me/protovibe',
      bank_account_holder: '',
      bank_iban: '',
      bank_bic: '',
    })
  })
})
