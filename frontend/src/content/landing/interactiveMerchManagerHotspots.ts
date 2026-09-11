export const INTERACTIVE_MERCH_VIEWBOX = {
  width: 1774,
  height: 887,
} as const

export type InteractiveMerchHotspotId = 'A' | 'B' | 'C' | 'D' | 'E' | 'F'

export interface InteractiveMerchHotspot {
  id: InteractiveMerchHotspotId
  slug: string
  hit: readonly [x: number, y: number, width: number, height: number]
  anchor: readonly [x: number, y: number]
  priority: number
}

export const INTERACTIVE_MERCH_HOTSPOTS: readonly InteractiveMerchHotspot[] = [
  { id: 'A', slug: 'analytics', hit: [38, 351, 290, 365], anchor: [300, 391], priority: 3 },
  { id: 'B', slug: 'packing-list', hit: [328, 400, 311, 243], anchor: [602, 438], priority: 2 },
  { id: 'C', slug: 'sales', hit: [647, 162, 591, 574], anchor: [706, 320], priority: 4 },
  { id: 'D', slug: 'offline', hit: [1141, 305, 88, 129], anchor: [1205, 367], priority: 6 },
  { id: 'E', slug: 'slideshow', hit: [999, 596, 194, 122], anchor: [1170, 624], priority: 5 },
  { id: 'F', slug: 'expenses', hit: [1251, 236, 523, 410], anchor: [1320, 410], priority: 1 },
] as const

