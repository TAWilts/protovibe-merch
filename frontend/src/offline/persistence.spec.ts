import { afterEach, describe, expect, it, vi } from 'vitest'

import { requestPersistentStorage } from './persistence'

const originalStorage = Object.getOwnPropertyDescriptor(navigator, 'storage')

function setStorage(value: Partial<StorageManager> | undefined) {
  Object.defineProperty(navigator, 'storage', { configurable: true, value })
}

afterEach(() => {
  vi.restoreAllMocks()
  if (originalStorage) Object.defineProperty(navigator, 'storage', originalStorage)
  else Reflect.deleteProperty(navigator, 'storage')
})

describe('persistent offline storage', () => {
  it('continues when the persistence API is unavailable', async () => {
    setStorage(undefined)
    await expect(requestPersistentStorage()).resolves.toBeUndefined()
  })

  it('treats a denied request as a normal best-effort result', async () => {
    setStorage({ persist: vi.fn().mockResolvedValue(false) })
    await expect(requestPersistentStorage()).resolves.toBe(false)
  })

  it('absorbs browser failures instead of breaking application startup', async () => {
    setStorage({ persist: vi.fn().mockRejectedValue(new Error('blocked')) })
    await expect(requestPersistentStorage()).resolves.toBe(false)
  })

  it('reports a granted persistence request', async () => {
    setStorage({ persist: vi.fn().mockResolvedValue(true) })
    await expect(requestPersistentStorage()).resolves.toBe(true)
  })
})
