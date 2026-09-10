import { describe, expect, it } from 'vitest'

import {
  changesForStockMode,
  commonStockMode,
  stockModeForVariant,
  targetStockEditable,
  type StockMode,
} from './stockMode'

function variant(is_offered: boolean, no_reorder: boolean, target_stock: number | null) {
  return { is_offered, no_reorder, target_stock }
}

describe('stock modes', () => {
  it.each([
    [variant(true, false, 10), 'stocked'],
    [variant(true, false, null), 'stocked'],
    [variant(true, false, 0), 'on_demand'],
    [variant(true, true, 10), 'clearance'],
    [variant(false, false, 10), 'paused'],
    [variant(false, true, null), 'discontinued'],
  ] as const)('derives persisted fields as %s', (fields, expected) => {
    expect(stockModeForVariant(fields)).toBe(expected)
  })

  it('clears only a zero target when changing on demand to stocked', () => {
    expect(changesForStockMode(variant(true, false, 0), 'stocked')).toEqual({
      is_offered: true,
      no_reorder: false,
      clear_target_stock: true,
    })
    expect(changesForStockMode(variant(true, false, 12), 'stocked')).toEqual({
      is_offered: true,
      no_reorder: false,
    })
  })

  it.each([
    ['on_demand', { is_offered: true, no_reorder: false, target_stock: 0 }],
    ['clearance', { is_offered: true, no_reorder: true }],
    ['paused', { is_offered: false, no_reorder: false }],
    ['discontinued', { is_offered: false, no_reorder: true }],
  ] satisfies [StockMode, object][])('maps %s without erasing preserved targets', (mode, expected) => {
    expect(changesForStockMode(variant(true, false, 8), mode)).toEqual(expected)
  })

  it('reports mixed modes and limits target editing to meaningful modes', () => {
    expect(commonStockMode([variant(true, false, null), variant(false, false, 4)])).toBe('mixed')
    expect(commonStockMode([variant(false, false, 1), variant(false, false, 4)])).toBe('paused')
    expect(targetStockEditable('stocked')).toBe(true)
    expect(targetStockEditable('paused')).toBe(true)
    expect(targetStockEditable('on_demand')).toBe(false)
    expect(targetStockEditable('clearance')).toBe(false)
    expect(targetStockEditable('discontinued')).toBe(false)
  })
})
