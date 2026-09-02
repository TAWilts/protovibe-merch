import { describe, expect, it } from 'vitest'

import router from './index'

describe('public landing route', () => {
  it('keeps the landing page public while preserving the band shell for existing URLs', () => {
    const landing = router.resolve('/')
    expect(landing.name).toBe('landing')
    expect(landing.meta.public).toBe(true)

    const sales = router.resolve('/sales')
    expect(sales.name).toBe('sales')
    expect(sales.matched).toHaveLength(2)
    expect(sales.matched[0].path).toBe('/app')
  })
})
