import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/api/client'
import type { Identity } from '@/api/types'
import { useSessionStore } from './session'

const { me } = vi.hoisted(() => ({ me: vi.fn() }))
vi.mock('@/api/endpoints', () => ({
  authApi: { me, logout: vi.fn(), setPosMode: vi.fn() },
}))

const identity = {
  user: { id: 7, username: 'thomas', role: 'member', ui_theme: 'aurora', ui_language: 'de' },
  band: { id: 12, slug: 'band', name: 'Band', feature_flags: { packing_list: true } },
  capabilities: { can_access_band_workflows: true },
  pos_mode: false,
} as Identity

describe('offline session identity', () => {
  beforeEach(() => {
    localStorage.clear()
    me.mockReset()
    setActivePinia(createPinia())
  })

  it('uses a previously confirmed band identity only when the server is unreachable', async () => {
    const online = useSessionStore()
    online.adopt(identity)
    expect(localStorage.getItem('protovibe.offline-identity.v1')).not.toContain('password')

    setActivePinia(createPinia())
    me.mockRejectedValueOnce(new TypeError('network unavailable'))
    const offline = useSessionStore()
    await offline.restore()

    expect(offline.identity?.user.id).toBe(7)
    expect(offline.offlineIdentity).toBe(true)
  })

  it('does not bypass a definitive unauthenticated server response', async () => {
    localStorage.setItem('protovibe.offline-identity.v1', JSON.stringify(identity))
    me.mockRejectedValueOnce(new ApiError(401, 'expired'))

    const session = useSessionStore()
    await session.restore()
    expect(session.identity).toBeNull()
    expect(localStorage.getItem('protovibe.offline-identity.v1')).toBeNull()
  })
})
