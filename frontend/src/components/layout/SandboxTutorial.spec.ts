import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import SandboxTutorial from './SandboxTutorial.vue'

const { session } = vi.hoisted(() => ({
  session: {
    identity: {
      sandbox: {
        tutorial_visible: true,
        tutorial_state: { catalogue: true, purchase: false, sale: false, balance: false },
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
  beforeEach(() => session.setSandboxTutorial.mockReset())

  it('shows a concrete description for every sandbox task', async () => {
    const wrapper = mount(SandboxTutorial)

    expect(wrapper.findAll('.sandbox-tutorial-tasks li')).toHaveLength(4)
    expect(wrapper.findAll('.sandbox-task-copy > span').map((entry) => entry.text())).toEqual([
      'sandbox.tutorial.catalogueDescription',
      'sandbox.tutorial.purchaseDescription',
      'sandbox.tutorial.saleDescription',
      'sandbox.tutorial.balanceDescription',
    ])
    expect(wrapper.findAll('.sandbox-tutorial-tasks li')[0]!.classes()).toContain('done')

    await wrapper.get('.sandbox-tutorial-heading button').trigger('click')
    expect(session.setSandboxTutorial).toHaveBeenCalledWith(false)
  })
})
