import type { Variant } from '@/api/types'

export type StockMode = 'stocked' | 'on_demand' | 'clearance' | 'paused' | 'discontinued'
export type DisplayStockMode = StockMode | 'mixed'

type StockFields = Pick<Variant, 'is_offered' | 'no_reorder' | 'target_stock'>

export interface StockModeChanges {
  is_offered: boolean
  no_reorder: boolean
  target_stock?: number
  clear_target_stock?: boolean
}

export const stockModes: StockMode[] = [
  'stocked',
  'on_demand',
  'clearance',
  'paused',
  'discontinued',
]

/** Derives the UI mode from the persisted catalogue fields. */
export function stockModeForVariant(variant: StockFields): StockMode {
  if (!variant.is_offered && variant.no_reorder) return 'discontinued'
  if (!variant.is_offered) return 'paused'
  if (variant.no_reorder) return 'clearance'
  if (variant.target_stock === 0) return 'on_demand'
  return 'stocked'
}

/** Produces only persisted catalogue fields; stock_mode itself is never saved. */
export function changesForStockMode(variant: StockFields, mode: StockMode): StockModeChanges {
  switch (mode) {
    case 'stocked':
      return {
        is_offered: true,
        no_reorder: false,
        ...(variant.target_stock === 0 ? { clear_target_stock: true } : {}),
      }
    case 'on_demand':
      return { is_offered: true, no_reorder: false, target_stock: 0 }
    case 'clearance':
      return { is_offered: true, no_reorder: true }
    case 'paused':
      return { is_offered: false, no_reorder: false }
    case 'discontinued':
      return { is_offered: false, no_reorder: true }
  }
}

export function commonStockMode(variants: StockFields[]): DisplayStockMode {
  const first = variants[0]
  if (!first) return 'mixed'
  const mode = stockModeForVariant(first)
  return variants.every((variant) => stockModeForVariant(variant) === mode) ? mode : 'mixed'
}

export function stockModeOffers(mode: StockMode): boolean {
  return mode === 'stocked' || mode === 'on_demand' || mode === 'clearance'
}

export function targetStockEditable(mode: StockMode): boolean {
  return mode === 'stocked' || mode === 'paused'
}
