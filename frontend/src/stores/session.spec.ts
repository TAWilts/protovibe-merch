import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError, getApiMode } from '@/api/client'
import type { Identity } from '@/api/types'
import { useSessionStore } from './session'

const { me, logout, featureVisibility, sandboxStart, registrationConfig } = vi.hoisted(() => ({
  me: vi.fn(),
  logout: vi.fn(),
  featureVisibility: vi.fn(),
  sandboxStart: vi.fn(),
  registrationConfig: vi.fn(),
}))
vi.mock('@/api/endpoints', () => ({
  authApi: { me, logout },
  profileApi: { featureVisibility },
  sandboxApi: { start: sandboxStart },
  registrationApi: { config: registrationConfig },
}))

const identity = {
  user: {
    id: 7, username: 'thomas', role: 'member', ui_theme: 'aurora', ui_language: 'de',
    show_packing_list: true, show_product_palette: true,
  },
  band: { id: 12, slug: 'band', name: 'Band', feature_flags: { packing_list: true } },
  capabilities: { can_access_band_workflows: true },
  environment: 'production',
} as Identity

describe('offline session identity', () => {
  beforeEach(() => {
    localStorage.clear()
    me.mockReset()
    logout.mockReset()
    featureVisibility.mockReset()
    sandboxStart.mockReset()
    registrationConfig.mockReset().mockResolvedValue({
      registration_enabled: true,
      sandbox_enabled: true,
      environment: 'production',
    })
    document.documentElement.removeAttribute('data-development')
    document.documentElement.removeAttribute('data-sandbox')
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

  it('applies development branding for signed-out and signed-in sessions', async () => {
    me.mockRejectedValueOnce(new ApiError(401, 'signed out'))
    registrationConfig.mockResolvedValueOnce({
      registration_enabled: true,
      sandbox_enabled: true,
      environment: 'development',
    })
    const signedOut = useSessionStore()
    await signedOut.restore()

    expect(signedOut.isDevelopment).toBe(true)
    expect(document.documentElement.hasAttribute('data-development')).toBe(true)
    expect(document.title).toBe('testsuite')

    setActivePinia(createPinia())
    const signedIn = useSessionStore()
    signedIn.adopt({ ...identity, environment: 'development' })

    expect(signedIn.isDevelopment).toBe(true)
    expect(document.documentElement.hasAttribute('data-development')).toBe(true)
    expect(document.title).toBe('testsuite')
  })

  it('updates and caches the current user feature visibility', async () => {
    featureVisibility.mockResolvedValue({ show_packing_list: false, show_product_palette: true })
    const session = useSessionStore()
    session.adopt(identity)

    await session.setFeatureVisibility({ show_packing_list: false })

    expect(session.user?.show_packing_list).toBe(false)
    expect(JSON.parse(localStorage.getItem('protovibe.offline-identity.v1')!).user.show_packing_list).toBe(false)
  })

  it('clears user, capabilities and cached identity after a successful logout', async () => {
    logout.mockResolvedValue(undefined)
    const session = useSessionStore()
    session.adopt(identity)

    await expect(session.logout()).resolves.toBe(true)

    expect(session.user).toBeNull()
    expect(session.capabilities).toBeNull()
    expect(session.band).toBeNull()
    expect(session.isAuthenticated).toBe(false)
    expect(localStorage.getItem('protovibe.offline-identity.v1')).toBeNull()
  })

  it('keeps the existing local-logout policy when the server cannot be reached', async () => {
    logout.mockRejectedValue(new TypeError('network unavailable'))
    const session = useSessionStore()
    session.adopt(identity)

    await expect(session.logout()).resolves.toBe(false)
    expect(session.identity).toBeNull()
  })

  it('enters the sandbox without overwriting the cached real identity', async () => {
    const session = useSessionStore()
    session.adopt(identity)
    const sandboxIdentity = {
      ...identity,
      user: { ...identity.user, id: 99, username: 'Demo' },
      band: { ...identity.band!, id: 77, slug: 'sandbox-demo', name: 'Demo Band' },
      sandbox: { id: 4, expires_at: '2026-09-11T00:00:00Z', demo_role: 'band_admin', template_version: 1, tutorial_state: { catalogue: false, purchase: false, sale: false, balance: false }, tutorial_visible: true, storage_used_bytes: 0, storage_quota_bytes: 26214400 },
    } as Identity
    sandboxStart.mockResolvedValue({ session: sandboxIdentity, csrf_token: 'csrf' })

    await session.enterSandbox()

    expect(session.isSandbox).toBe(true)
    expect(getApiMode()).toBe('sandbox')
    expect(JSON.parse(localStorage.getItem('protovibe.offline-identity.v1')!).user.id).toBe(7)
    expect(document.title).toContain('SANDBOX')
  })
})
