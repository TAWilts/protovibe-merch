import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { Identity } from '@/api/types'
import { usePackingStore } from './packing'

const { synchronizePacking } = vi.hoisted(() => ({ synchronizePacking: vi.fn() }))
vi.mock('@/api/endpoints', () => ({ packingApi: { photoUrl: vi.fn((id: string) => `/photos/${id}`) } }))
vi.mock('@/offline/packing', () => ({
  createPackingOperation: vi.fn(),
  loadPackingSnapshot: vi.fn().mockResolvedValue(null),
  packingPhotoBlob: vi.fn().mockResolvedValue(null),
  packingFailures: vi.fn().mockResolvedValue([]),
  packingQueueCount: vi.fn().mockResolvedValue(0),
  queuePackingPhoto: vi.fn(),
  rememberPackingContext: vi.fn().mockResolvedValue(undefined),
  resumePackingSynchronization: vi.fn(),
  resolvePackingFailure: vi.fn(),
  savePackingSnapshot: vi.fn(),
  synchronizePacking,
}))

const identity = {
  user: { id: 4 },
  band: { id: 9 },
} as Identity

describe('packing synchronization cadence', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    synchronizePacking.mockReset().mockResolvedValue({
      snapshot: { revision: 0, generation: 1, bags: [] }, transmitted: 0,
      remaining: 0, conflicts: 0, messages: [], authenticationRequired: false,
    })
  })

  it('uses a ten-second fallback poll while keeping initial synchronization immediate', async () => {
    const interval = vi.spyOn(window, 'setInterval')
    const store = usePackingStore()

    await store.activate(identity)

    expect(synchronizePacking).toHaveBeenCalledOnce()
    expect(interval).toHaveBeenCalledWith(expect.any(Function), 10_000)
    store.deactivatePage()
    interval.mockRestore()
  })
})
