import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import PackingListView from './PackingListView.vue'

const { packing, session } = vi.hoisted(() => ({
  session: {
    identity: { user: { id: 4 }, band: { id: 9 } },
    band: { id: 9 }, posMode: false,
    capabilities: { can_manage_packing_list: false },
  },
  packing: {
    snapshot: {
      revision: 2, generation: 1,
      bags: [{
        id: '11111111-1111-4111-8111-111111111111', name: 'Stagerack', position: 0,
        status: 'open', photos: [],
        items: [{ id: '22222222-2222-4222-8222-222222222222', bag_id: '11111111-1111-4111-8111-111111111111', name: 'Laptop', position: 0, status: 'open', photos: [] }],
      }],
    },
    online: true, syncing: false, queued: 0, messages: [], conflicts: [], authenticationRequired: false, loading: false,
    prepare: vi.fn(), activate: vi.fn(), deactivatePage: vi.fn(), mutate: vi.fn(), addPhoto: vi.fn(), sync: vi.fn(),
    dismissMessage: vi.fn(), resolveConflict: vi.fn(), photoURL: vi.fn(() => ''),
  },
}))

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/stores/session', () => ({ useSessionStore: () => session }))
vi.mock('@/stores/packing', () => ({ usePackingStore: () => packing }))
vi.mock('@/stores/flash', () => ({ useFlashStore: () => ({ error: vi.fn() }) }))

describe('PackingListView', () => {
  beforeEach(() => {
    packing.mutate.mockReset().mockResolvedValue(undefined)
    packing.activate.mockReset().mockResolvedValue(undefined)
    session.capabilities.can_manage_packing_list = false
    session.posMode = false
  })

  it('lets sellers check items but hides all management actions', async () => {
    const wrapper = mount(PackingListView)
    await flushPromises()

    expect(wrapper.text()).toContain('Stagerack')
    expect(wrapper.text()).toContain('Laptop')
    expect(wrapper.text()).not.toContain('packing.addBag')
    await wrapper.get('.item-check input').trigger('change')
    expect(packing.mutate).toHaveBeenCalledWith('set_item_status', {
      item_id: '22222222-2222-4222-8222-222222222222', status: 'packed',
    })
  })

  it('shows structure and reset controls to members outside POS mode', async () => {
    session.capabilities.can_manage_packing_list = true
    const wrapper = mount(PackingListView)
    await flushPromises()

    expect(wrapper.text()).toContain('packing.addBag')
    await wrapper.get('.packing-toolbar .secondary-button:nth-of-type(2)').trigger('click')
    expect(wrapper.get('.confirmation-dialog').text()).toContain('packing.resetTitle')
  })
})
