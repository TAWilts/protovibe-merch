import { describe, expect, it } from 'vitest'

import { parseAmount } from './useMoney'

describe('parseAmount', () => {
  it('accepts a dot decimal separator', () => {
    expect(parseAmount('28.02')).toBe(2802)
  })

  it('accepts German thousands plus decimals', () => {
    expect(parseAmount('1.000,02')).toBe(100002)
  })

  it('rejects malformed repeated decimal separators', () => {
    expect(parseAmount('1.000.02')).toBeNull()
  })
})
