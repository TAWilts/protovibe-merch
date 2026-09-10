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
})
