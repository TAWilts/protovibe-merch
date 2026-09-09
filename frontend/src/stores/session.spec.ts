import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/api/client'
import type { Identity } from '@/api/types'
import { useSessionStore } from './session'

const { me, featureVisibility } = vi.hoisted(() => ({ me: vi.fn(), featureVisibility: vi.fn() }))
vi.mock('@/api/endpoints', () => ({
  authApi: { me, logout: vi.fn() },
  profileApi: { featureVisibility },
}))

const identity = {
  user: {
    id: 7, username: 'thomas', role: 'member', ui_theme: 'aurora', ui_language: 'de',
    show_packing_list: true, show_product_palette: true,
  },
  band: { id: 12, slug: 'band', name: 'Band', feature_flags: { packing_list: true } },
  capabilities: { can_access_band_workflows: true },
} as Identity

describe('offline session identity', () => {
  beforeEach(() => {
    localStorage.clear()
    me.mockReset()
    featureVisibility.mockReset()
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

  it('updates and caches the current user feature visibility', async () => {
    featureVisibility.mockResolvedValue({ show_packing_list: false, show_product_palette: true })
    const session = useSessionStore()
    session.adopt(identity)

    await session.setFeatureVisibility({ show_packing_list: false })

    expect(session.user?.show_packing_list).toBe(false)
    expect(JSON.parse(localStorage.getItem('protovibe.offline-identity.v1')!).user.show_packing_list).toBe(false)
  })
})
