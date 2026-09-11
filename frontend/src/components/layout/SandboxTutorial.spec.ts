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
  RouterLink: { props: ['to'], template: '<a><slot /></a>' },
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

  it('shows only the first outstanding task and labels the exit as skip', async () => {
    const wrapper = mount(SandboxTutorial)

    expect(wrapper.findAll('.sandbox-tutorial-tasks li')).toHaveLength(1)
    expect(wrapper.get('.sandbox-task-copy strong').text()).toBe('sandbox.tutorial.catalogue')
    expect(wrapper.get('.sandbox-task-copy > span').text()).toBe('sandbox.tutorial.catalogueDescription')
    expect(wrapper.get('.sandbox-tutorial-heading button').text()).toBe('sandbox.tutorial.hide')

    await wrapper.get('.sandbox-tutorial-heading button').trigger('click')
    expect(session.setSandboxTutorial).toHaveBeenCalledWith(false)
  })

  it.each([
    [{ catalogue: true, purchase: false, sale: false, balance: false }, 'purchase'],
    [{ catalogue: true, purchase: true, sale: false, balance: false }, 'sale'],
    [{ catalogue: true, purchase: true, sale: true, balance: false }, 'balance'],
  ])('shows the next step for progress %o', (tutorialState, expectedStep) => {
    session.identity.sandbox.tutorial_state = tutorialState

    const wrapper = mount(SandboxTutorial)

    expect(wrapper.findAll('.sandbox-tutorial-tasks li')).toHaveLength(1)
    expect(wrapper.get('.sandbox-task-copy strong').text()).toBe(`sandbox.tutorial.${expectedStep}`)
    expect(wrapper.get('.sandbox-task-copy > span').text()).toBe(`sandbox.tutorial.${expectedStep}Description`)
  })

  it('disappears after all guided tasks are complete', () => {
    session.identity.sandbox.tutorial_state = {
      catalogue: true,
      purchase: true,
      sale: true,
      balance: true,
    }

    expect(mount(SandboxTutorial).find('.sandbox-tutorial').exists()).toBe(false)
  })
})
