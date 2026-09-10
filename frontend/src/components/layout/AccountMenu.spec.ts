import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import AccountMenu from './AccountMenu.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({
  RouterLink: { props: ['to'], template: '<a href="#"><slot /></a>' },
}))

describe('AccountMenu', () => {
  it('keeps profile and logout keyboard-accessible in a compact account menu', async () => {
    const wrapper = mount(AccountMenu, {
      props: { username: 'thomas', roleLabel: 'Band-Admin', sandboxAvailable: true },
      attachTo: document.body,
    })

    await wrapper.get('summary').trigger('click')
    expect(wrapper.get('details').attributes('open')).toBeDefined()
    expect(wrapper.get('.account-popover').text()).toContain('thomas')
    expect(wrapper.get('.account-popover').text()).toContain('Band-Admin')

    await wrapper.findAll('.account-popover button').at(-1)!.trigger('click')
    expect(wrapper.emitted('logout')).toHaveLength(1)
    wrapper.unmount()
  })

  it('offers tutorial restart and permanent deletion inside the sandbox', async () => {
    const wrapper = mount(AccountMenu, {
      props: { username: 'Demo', roleLabel: 'Manager', sandbox: true },
    })
    expect(wrapper.text()).toContain('sandbox.tutorial.restart')
    expect(wrapper.text()).toContain('sandbox.discard')
    expect(wrapper.text()).not.toContain('accountMenu.profile')
    expect(wrapper.text()).not.toContain('common.logout')
    const actions = wrapper.findAll('.account-popover button')
    await actions[0].trigger('click')
    expect(wrapper.emitted('tutorial')).toHaveLength(1)
    await actions[1].trigger('click')
    expect(wrapper.emitted('discardSandbox')).toHaveLength(1)
  })
})
