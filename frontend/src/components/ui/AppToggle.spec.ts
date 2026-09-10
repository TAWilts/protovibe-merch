import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import AppToggle from './AppToggle.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const global = {
  mocks: { $t: (key: string) => key },
  stubs: {},
}

describe('AppToggle', () => {
  it('supports v-model in both directions and exposes text plus ARIA state', async () => {
    const wrapper = mount(AppToggle, {
      props: { modelValue: false, label: 'Preis inkl. MwSt.', 'onUpdate:modelValue': (value: boolean) => wrapper.setProps({ modelValue: value }) },
      global,
    })
    const button = wrapper.get('button')
    expect(button.text()).toContain('Preis inkl. MwSt.')
    expect(button.text()).toContain('common.off')
    expect(button.attributes('aria-pressed')).toBe('false')

    await button.trigger('click')
    expect(wrapper.props('modelValue')).toBe(true)
    expect(button.attributes('aria-pressed')).toBe('true')
    expect(button.text()).toContain('common.on')

    await button.trigger('click')
    expect(wrapper.props('modelValue')).toBe(false)
  })

  it('is a keyboard-operable button and handles Space', async () => {
    const wrapper = mount(AppToggle, { props: { modelValue: false, label: 'Option' }, global })
    const button = wrapper.get('button')
    expect(button.element.tagName).toBe('BUTTON')
    expect(button.attributes('type')).toBe('button')
    await button.trigger('keydown', { key: ' ' })
    expect(wrapper.emitted('update:modelValue')).toEqual([[true]])
  })

  it('does not change while disabled', async () => {
    const wrapper = mount(AppToggle, { props: { modelValue: false, label: 'Option', disabled: true }, global })
    const button = wrapper.get('button')
    expect(button.attributes()).toHaveProperty('disabled')
    await button.trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })
})
