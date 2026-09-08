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
    vi.useRealTimers()
    packing.mutate.mockReset().mockResolvedValue(undefined)
    packing.activate.mockReset().mockResolvedValue(undefined)
    session.capabilities.can_manage_packing_list = false
    session.posMode = false
    packing.snapshot.bags[0]!.status = 'open'
    packing.snapshot.bags[0]!.items = [{
      id: '22222222-2222-4222-8222-222222222222', bag_id: '11111111-1111-4111-8111-111111111111',
      name: 'Laptop', position: 0, status: 'open', photos: [],
    }]
  })

  it('lets sellers check items but hides all management actions', async () => {
    const wrapper = mount(PackingListView)
    await flushPromises()

    expect(wrapper.text()).toContain('Stagerack')
    expect(wrapper.text()).toContain('Laptop')
    expect(wrapper.text()).not.toContain('packing.addBag')
    expect(wrapper.text()).not.toContain('packing.syncNow')
    await wrapper.get('.item-toggle').trigger('click')
    expect(packing.mutate).toHaveBeenCalledWith('set_item_status', {
      item_id: '22222222-2222-4222-8222-222222222222', status: 'packed',
    })
  })

  it('shows structure and reset controls to members outside POS mode', async () => {
    session.capabilities.can_manage_packing_list = true
    const wrapper = mount(PackingListView)
    await flushPromises()

    expect(wrapper.text()).toContain('packing.addBag')
    expect(wrapper.text()).toContain('packing.addItem')
    expect(wrapper.text()).toContain('packing.addBag')
    await wrapper.get('.packing-overview .secondary-button').trigger('click')
    expect(wrapper.get('.confirmation-dialog').text()).toContain('packing.resetTitle')
  })

  it('opens the item action menu after three seconds without toggling the item', async () => {
    vi.useFakeTimers()
    const wrapper = mount(PackingListView)
    await flushPromises()

    await wrapper.get('.item-toggle').trigger('pointerdown', { button: 0, clientX: 10, clientY: 10 })
    vi.advanceTimersByTime(2_999)
    await flushPromises()
    expect(wrapper.find('.confirmation-dialog').exists()).toBe(false)
    vi.advanceTimersByTime(1)
    await flushPromises()

    expect(wrapper.get('.confirmation-dialog').text()).toContain('packing.staysHere')
    expect(wrapper.get('.confirmation-dialog').text()).not.toContain('packing.rename')
    await wrapper.get('.item-toggle').trigger('click')
    expect(packing.mutate).not.toHaveBeenCalled()
  })

  it('toggles the complete active bag through its progress bar', async () => {
    const wrapper = mount(PackingListView)
    await flushPromises()

    await wrapper.get('.bag-progress-button').trigger('click')
    expect(packing.mutate).toHaveBeenCalledWith('set_bag_status', {
      bag_id: '11111111-1111-4111-8111-111111111111', status: 'packed',
    })
  })

  it('excludes stays-here entries from the progress denominator', async () => {
    packing.snapshot.bags[0]!.items = [
      { id: '22222222-2222-4222-8222-222222222222', bag_id: '11111111-1111-4111-8111-111111111111', name: 'Laptop', position: 0, status: 'packed', photos: [] },
      { id: '33333333-3333-4333-8333-333333333333', bag_id: '11111111-1111-4111-8111-111111111111', name: 'Reserve', position: 10, status: 'stays_here', photos: [] },
    ]
    const wrapper = mount(PackingListView)
    await flushPromises()

    expect(wrapper.get('.progress-track').attributes('aria-valuenow')).toBe('100')
    expect(wrapper.get('.overview-copy').text()).toContain('packing.staysShort')
  })
})
