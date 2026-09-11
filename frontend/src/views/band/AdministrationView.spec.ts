import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import AdministrationView from './AdministrationView.vue'

const { paymentQrSettings, savePaymentQrSettings, grants, listUsers, events, createEvent, renameEvent, selectEvent, deleteEvent, setFeatureVisibility, flashSuccess, session } = vi.hoisted(() => ({
  paymentQrSettings: vi.fn(),
  savePaymentQrSettings: vi.fn(),
  grants: vi.fn(),
  listUsers: vi.fn(),
  events: vi.fn(),
  createEvent: vi.fn(),
  renameEvent: vi.fn(),
  selectEvent: vi.fn(),
  deleteEvent: vi.fn(),
  setFeatureVisibility: vi.fn(),
  flashSuccess: vi.fn(),
  session: {
    featureFlags: { payment_qr: true },
    user: { show_packing_list: true, show_product_palette: true },
    capabilities: { is_band_admin: true, can_access_member_workflows: true, sensitive_action_mfa_required: false },
    setFeatureVisibility: vi.fn(),
  },
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
    d: (value: unknown) => String(value),
  }),
}))
vi.mock('@/stores/flash', () => ({
  useFlashStore: () => ({ success: flashSuccess, error: vi.fn() }),
}))
vi.mock('@/stores/session', () => ({
  useSessionStore: () => session,
}))
vi.mock('@/api/endpoints', () => ({
  bandAdminApi: { grants },
  bandUsersApi: {
    list: listUsers,
    paymentQrSettings,
    savePaymentQrSettings,
  },
  salesApi: { events, createEvent, renameEvent, selectEvent, deleteEvent },
}))

describe('AdministrationView payment QR settings', () => {
  beforeEach(() => {
    session.capabilities.is_band_admin = true
    session.capabilities.can_access_member_workflows = true
    session.user.show_packing_list = true
    session.user.show_product_palette = true
    session.setFeatureVisibility = setFeatureVisibility
    setFeatureVisibility.mockReset().mockResolvedValue({ show_packing_list: false, show_product_palette: true })
    flashSuccess.mockReset()
    grants.mockReset().mockResolvedValue({ grants: [] })
    listUsers.mockReset().mockResolvedValue({ users: [], assignable_roles: [] })
    events.mockReset().mockResolvedValue({ events: [], selected_event_id: 0 })
    createEvent.mockReset().mockResolvedValue({ id: 2, name: 'Club', is_selected: false })
    renameEvent.mockReset().mockResolvedValue({ id: 1, name: 'Neuer Name', is_selected: false })
    selectEvent.mockReset().mockResolvedValue({ id: 1, name: 'Gig', is_selected: true })
    deleteEvent.mockReset().mockResolvedValue(undefined)
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

  it('shows members only the event panel and never requests admin-only data', async () => {
    session.capabilities.is_band_admin = false
    events.mockResolvedValue({
      events: [{ id: 1, name: 'Gig', is_selected: false }], selected_event_id: 0,
    })

    const wrapper = mount(AdministrationView)
    await flushPromises()

    expect(wrapper.text()).toContain('Gig')
    expect(wrapper.find('.admin-payment-qr-settings').exists()).toBe(false)
    expect(grants).not.toHaveBeenCalled()
    expect(listUsers).not.toHaveBeenCalled()

    const eventRow = wrapper.get('.event-admin-list article')
    await eventRow.findAll('button')[0]!.trigger('click')
    await flushPromises()
    expect(selectEvent).toHaveBeenCalledWith(1)
  })

  it('lets members create, rename and delete events from administration', async () => {
    session.capabilities.is_band_admin = false
    events.mockResolvedValue({
      events: [{ id: 1, name: 'Gig', is_selected: false }], selected_event_id: 0,
    })
    const wrapper = mount(AdministrationView)
    await flushPromises()

    await wrapper.get('.event-admin-panel .primary-button').trigger('click')
    await wrapper.get('.confirmation-dialog input').setValue('Neues Event')
    await wrapper.get('.confirmation-dialog form').trigger('submit')
    await flushPromises()
    expect(createEvent).toHaveBeenCalledWith('Neues Event', false)

    await wrapper.get('.event-admin-list article').findAll('button')[1]!.trigger('click')
    await wrapper.get('.confirmation-dialog input').setValue('Umbenannt')
    await wrapper.get('.confirmation-dialog form').trigger('submit')
    await flushPromises()
    expect(renameEvent).toHaveBeenCalledWith(1, 'Umbenannt')

    await wrapper.get('.event-admin-list article').findAll('button')[2]!.trigger('click')
    await wrapper.get('.confirmation-dialog .danger-button').trigger('click')
    await flushPromises()
    expect(deleteEvent).toHaveBeenCalledWith(1)
  })

  it('reports when renaming merged two active events', async () => {
    session.capabilities.is_band_admin = false
    events.mockResolvedValue({
      events: [{ id: 1, name: 'Gig', is_selected: false }], selected_event_id: 0,
    })
    renameEvent.mockResolvedValue({ id: 2, name: 'Festival', is_selected: false, merged: true })
    const wrapper = mount(AdministrationView)
    await flushPromises()

    await wrapper.get('.event-admin-list article').findAll('button')[1]!.trigger('click')
    await wrapper.get('.confirmation-dialog input').setValue('Festival')
    await wrapper.get('.confirmation-dialog form').trigger('submit')
    await flushPromises()

    expect(flashSuccess).toHaveBeenCalledWith('administration.events.merged')
  })

  it('lets members hide personal modules without changing band feature flags', async () => {
    session.capabilities.is_band_admin = false
    const wrapper = mount(AdministrationView)
    await flushPromises()

    const options = wrapper.findAll('.personal-feature-panel .app-toggle')
    expect(options).toHaveLength(2)
    await options[0]!.trigger('click')
    await flushPromises()

    expect(setFeatureVisibility).toHaveBeenCalledWith({ show_packing_list: false })
  })

  it('places the role explanation beside the accounts heading with a scalable info icon', async () => {
    listUsers.mockResolvedValue({ users: [], assignable_roles: ['seller', 'member'] })
    const wrapper = mount(AdministrationView)
    await flushPromises()

    const heading = wrapper.get('.user-admin-panel .users-heading-title')
    const help = heading.get('.role-help')
    expect(heading.text()).toContain('administration.users.title')
    expect(help.get('svg').attributes('viewBox')).toBe('0 0 24 24')
    expect(wrapper.find('.role-field .role-help').exists()).toBe(false)

    await help.get('summary').trigger('click')
    expect(help.attributes()).toHaveProperty('open')
    expect(help.get('.role-help-panel').text()).toContain('administration.users.roleHelpIntro')
  })
})
