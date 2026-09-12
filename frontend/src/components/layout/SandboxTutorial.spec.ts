import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import SandboxTutorial from './SandboxTutorial.vue'

const { session } = vi.hoisted(() => ({
  session: {
    identity: {
      sandbox: {
        tutorial_visible: true,
        tutorial_state: { catalogue: false, purchase: false, sale: false, balance: false },
      },
    },
    setSandboxTutorial: vi.fn(),
  },
}))

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({
  RouterLink: {
    props: ['to'],
    template: '<a class="router-link-stub" :data-name="to && to.name"><slot /></a>',
  },
}))
vi.mock('@/stores/session', () => ({ useSessionStore: () => session }))

describe('SandboxTutorial', () => {
  beforeEach(() => {
    session.identity.sandbox.tutorial_visible = true
    session.identity.sandbox.tutorial_state = {
      catalogue: false,
      purchase: false,
      sale: false,
      balance: false,
    }
    session.setSandboxTutorial.mockReset()
  })

  it('shows the complete required journey and highlights the current step', async () => {
    const wrapper = mount(SandboxTutorial)
    const items = wrapper.findAll('.sandbox-timeline-item')

    expect(items).toHaveLength(4)
    expect(items.map((item) => item.get('.sandbox-timeline-label').text())).toEqual([
      'sandbox.tutorial.catalogue',
      'sandbox.tutorial.purchase',
      'sandbox.tutorial.sale',
      'sandbox.tutorial.balance',
    ])
    expect(items[0].classes()).toContain('is-current')
    expect(items[1].classes()).toContain('is-upcoming')
    expect(wrapper.get('.sandbox-tutorial-help strong').text()).toBe('sandbox.tutorial.catalogue')
    expect(wrapper.get('.sandbox-tutorial-help p').text()).toBe(
      'sandbox.tutorial.catalogueDescription',
    )
    expect(wrapper.get('.router-link-stub').attributes('data-name')).toBe('sandbox-articles')
    expect(wrapper.get('.sandbox-tutorial-heading button').text()).toBe(
      'sandbox.tutorial.skip',
    )

    await wrapper.get('.sandbox-tutorial-heading button').trigger('click')
    expect(session.setSandboxTutorial).toHaveBeenCalledWith(false)
  })

  it.each([
    [
      { catalogue: true, purchase: false, sale: false, balance: false },
      'purchase',
      1,
    ],
    [
      { catalogue: true, purchase: true, sale: false, balance: false },
      'sale',
      2,
    ],
    [
      { catalogue: true, purchase: true, sale: true, balance: false },
      'balance',
      3,
    ],
  ])('marks completed tasks and the next step for progress %o', (tutorialState, expectedStep, currentIndex) => {
    session.identity.sandbox.tutorial_state = tutorialState

    const wrapper = mount(SandboxTutorial)
    const items = wrapper.findAll('.sandbox-timeline-item')

    expect(items[currentIndex].classes()).toContain('is-current')
    expect(items.slice(0, currentIndex).every((item) => item.classes().includes('is-done'))).toBe(true)
    expect(wrapper.get('.sandbox-tutorial-help strong').text()).toBe(
      `sandbox.tutorial.${expectedStep}`,
    )
  })

  it('previews help on hover or focus and keeps clicked help selected', async () => {
    const wrapper = mount(SandboxTutorial)
    const buttons = wrapper.findAll('.sandbox-timeline-item button')

    await buttons[2].trigger('mouseenter')
    expect(wrapper.get('.sandbox-tutorial-help strong').text()).toBe('sandbox.tutorial.sale')
    expect(wrapper.find('.router-link-stub').exists()).toBe(false)

    await buttons[2].trigger('mouseleave')
    expect(wrapper.get('.sandbox-tutorial-help strong').text()).toBe('sandbox.tutorial.catalogue')

    await buttons[2].trigger('click')
    await buttons[2].trigger('mouseleave')
    expect(wrapper.get('.sandbox-tutorial-help p').text()).toBe(
      'sandbox.tutorial.saleDescription',
    )

    await buttons[3].trigger('focus')
    expect(wrapper.get('.sandbox-tutorial-help strong').text()).toBe('sandbox.tutorial.balance')
    await buttons[3].trigger('blur')
    expect(wrapper.get('.sandbox-tutorial-help strong').text()).toBe('sandbox.tutorial.sale')
  })

  it('shows five unlocked suggestions after the required journey is complete', async () => {
    session.identity.sandbox.tutorial_state = {
      catalogue: true,
      purchase: true,
      sale: true,
      balance: true,
    }

    const wrapper = mount(SandboxTutorial)
    const items = wrapper.findAll('.sandbox-timeline-item')

    expect(items).toHaveLength(5)
    expect(items.map((item) => item.get('.sandbox-timeline-label').text())).toEqual([
      'sandbox.tutorial.shipping',
      'sandbox.tutorial.cancellation',
      'sandbox.tutorial.slideshow',
      'sandbox.tutorial.packing',
      'sandbox.tutorial.roles',
    ])
    expect(items.every((item) => item.classes().includes('is-optional'))).toBe(true)
    expect(wrapper.get('.sandbox-tutorial-heading strong').text()).toBe(
      'sandbox.tutorial.optionalTitle',
    )
    expect(wrapper.get('.sandbox-tutorial-heading button').text()).toBe(
      'sandbox.tutorial.hide',
    )
    expect(wrapper.get('.sandbox-tutorial-help p').text()).toBe(
      'sandbox.tutorial.shippingDescription',
    )
    expect(wrapper.get('.router-link-stub').attributes('data-name')).toBe('sandbox-sales')

    await items[2].get('button').trigger('click')
    expect(wrapper.get('.sandbox-tutorial-help p').text()).toBe(
      'sandbox.tutorial.slideshowDescription',
    )
    expect(wrapper.get('.router-link-stub').attributes('data-name')).toBe('sandbox-slideshow')

    await items[4].get('button').trigger('click')
    expect(wrapper.get('.sandbox-tutorial-help p').text()).toBe(
      'sandbox.tutorial.rolesDescription',
    )
    expect(wrapper.find('.router-link-stub').exists()).toBe(false)
  })
})
