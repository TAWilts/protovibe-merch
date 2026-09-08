import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import App from './App.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({ RouterView: { template: '<main id="main-content" tabindex="-1" />' } }))
vi.mock('@/components/TelemetryConsentDialog.vue', () => ({ default: { template: '<div data-telemetry-dialog />' } }))

describe('App accessibility entry point', () => {
  it('renders a localized skip link without changing the router outlet', () => {
    const wrapper = mount(App)
    const skipLink = wrapper.get('.skip-link')

    expect(skipLink.text()).toBe('common.skipToContent')
    expect(skipLink.attributes('href')).toBe('#main-content')
    expect(wrapper.get('main').attributes()).toMatchObject({ id: 'main-content', tabindex: '-1' })
  })
})
