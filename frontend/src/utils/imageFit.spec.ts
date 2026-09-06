import { describe, expect, it } from 'vitest'

import { fitImageSize } from './imageFit'

describe('fitImageSize', () => {
  it('uses the vertical limit for a portrait image', () => {
    expect(fitImageSize(1200, 2400, 1000, 800)).toEqual({ width: 400, height: 800 })
  })

  it('uses the horizontal limit for a landscape image', () => {
    expect(fitImageSize(2400, 1200, 900, 800)).toEqual({ width: 900, height: 450 })
  })

  it('rejects dimensions that cannot be measured', () => {
    expect(fitImageSize(0, 1200, 900, 800)).toBeNull()
  })
})
