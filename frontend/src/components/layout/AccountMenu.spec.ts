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
      props: { username: 'thomas', roleLabel: 'Band-Admin' },
      attachTo: document.body,
    })

    await wrapper.get('summary').trigger('click')
    expect(wrapper.get('details').attributes('open')).toBeDefined()
    expect(wrapper.get('.account-popover').text()).toContain('thomas')
    expect(wrapper.get('.account-popover').text()).toContain('Band-Admin')

    await wrapper.get('.account-popover button').trigger('click')
    expect(wrapper.emitted('logout')).toHaveLength(1)
    wrapper.unmount()
  })
})
