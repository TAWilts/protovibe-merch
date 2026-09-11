import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import router from './index'
import { useSessionStore } from '@/stores/session'

describe('public landing route', () => {
  beforeEach(async () => {
    setActivePinia(createPinia())
    const session = useSessionStore()
    session.ready = true
    session.clear()
    await router.replace('/')
  })

  it('keeps the landing page public while preserving the band shell for existing URLs', () => {
    const landing = router.resolve('/')
    expect(landing.name).toBe('landing')
    expect(landing.meta.public).toBe(true)

    const sales = router.resolve('/sales')
    expect(sales.name).toBe('sales')
    expect(sales.matched).toHaveLength(2)
    expect(sales.matched[0].path).toBe('/app')
  })

  it('redirects an unauthenticated protected route to login without a loop', async () => {
    await router.push('/articles')

    expect(router.currentRoute.value.name).toBe('login')
    expect(router.currentRoute.value.query.next).toBe('/articles')

    await router.replace({ name: 'login' })
    expect(router.currentRoute.value.name).toBe('login')
  })

  it('keeps the full sandbox in its own route namespace', () => {
    const sales = router.resolve('/sandbox/sales')
    const balances = router.resolve('/sandbox/balances')
    const administration = router.resolve('/sandbox/administration')
    expect(sales.name).toBe('sandbox-sales')
    expect(balances.name).toBe('sandbox-balances')
    expect(administration.name).toBe('sandbox-administration')
    expect(sales.matched[0].meta.sandbox).toBe(true)
    expect(sales.matched[0].path).toBe('/sandbox')
  })

  it('redirects sandbox navigation to the first outstanding tutorial task', async () => {
    const session = useSessionStore()
    session.adopt({
      environment: 'production',
      user: {
        id: 1,
        username: 'demo',
        role: 'band_admin',
        ui_theme: 'aurora',
        ui_language: 'de',
        show_variant_photos: true,
        show_packing_list: true,
        show_product_palette: true,
        telemetry_enabled: false,
        telemetry_decided: true,
        mfa_enabled: false,
        contact_email: '',
        sandbox_intro_seen: true,
      },
      capabilities: {
        role: 'band_admin',
        role_label: 'Band-Admin',
        is_band_admin: true,
        is_support_admin: false,
        is_system_admin: false,
        is_platform_staff: false,
        can_access_band_workflows: true,
        can_access_member_workflows: true,
        can_manage_purchases: true,
        can_create_band_finances: true,
        can_manage_band_finances: true,
        can_manage_articles: true,
        can_manage_slideshow: true,
        can_use_packing_list: true,
        can_manage_packing_list: true,
        can_access_band_administration: true,
        can_access_system_administration: false,
        can_manage_platform_staff: false,
        can_manage_updates: false,
        mfa_required: false,
        mfa_enabled: false,
        sensitive_action_mfa_required: false,
      },
      sandbox: {
        id: 1,
        expires_at: '2026-09-12T00:00:00Z',
        demo_role: 'band_admin',
        template_version: 2,
        tutorial_state: { catalogue: false, purchase: false, sale: false, balance: false },
        tutorial_visible: true,
        storage_used_bytes: 0,
        storage_quota_bytes: 25 * 1024 * 1024,
      },
    })

    await router.push('/sandbox/sales')
    expect(router.currentRoute.value.name).toBe('sandbox-articles')

    session.identity!.sandbox!.tutorial_state.catalogue = true
    await router.push('/sandbox/sales')
    expect(router.currentRoute.value.name).toBe('sandbox-purchases')

    session.identity!.sandbox!.tutorial_visible = false
    await router.push('/sandbox/sales')
    expect(router.currentRoute.value.name).toBe('sandbox-sales')
  })
})
