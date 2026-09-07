import { describe, expect, it } from 'vitest'

import type { PackingOperation, PackingSnapshot } from '@/api/types'
import { applyPackingOperation } from './packing'

const bagId = '11111111-1111-4111-8111-111111111111'
const firstItem = '22222222-2222-4222-8222-222222222222'
const secondItem = '33333333-3333-4333-8333-333333333333'

function operation(type: PackingOperation['type'], values: Partial<PackingOperation> = {}): PackingOperation {
  return {
    event_id: crypto.randomUUID(), device_id: 'browser', client_created_at: new Date().toISOString(),
    base_generation: 4, type, ...values,
  }
}

function snapshot(): PackingSnapshot {
  return {
    revision: 8,
    generation: 4,
    bags: [{
      id: bagId, name: 'Stagerack', position: 0, status: 'open', photos: [],
      items: [
        { id: firstItem, bag_id: bagId, name: 'Laptop', position: 0, status: 'open', photos: [] },
        { id: secondItem, bag_id: bagId, name: 'Ersatz-In-Ear', position: 10, status: 'stays_here', photos: [] },
      ],
    }],
  }
}

describe('offline packing projection', () => {
  it('packs and reopens a complete bag without overwriting stays-here entries', () => {
    const packed = applyPackingOperation(snapshot(), operation('set_bag_status', { bag_id: bagId, status: 'packed' }))
    expect(packed.bags[0]?.items.map((item) => item.status)).toEqual(['packed', 'stays_here'])
    expect(packed.bags[0]?.status).toBe('packed')

    const reopened = applyPackingOperation(packed, operation('set_bag_status', { bag_id: bagId, status: 'open' }))
    expect(reopened.bags[0]?.items.map((item) => item.status)).toEqual(['open', 'stays_here'])
    expect(reopened.bags[0]?.status).toBe('open')
  })

  it('preserves child state while a whole bag stays here', () => {
    const initial = snapshot()
    initial.bags[0]!.items[0]!.status = 'packed'
    initial.bags[0]!.status = 'packed'

    const excluded = applyPackingOperation(initial, operation('set_bag_status', { bag_id: bagId, status: 'stays_here' }))
    const included = applyPackingOperation(excluded, operation('set_bag_status', { bag_id: bagId, status: 'open' }))
    expect(included.bags[0]?.items.map((item) => item.status)).toEqual(['packed', 'stays_here'])
    expect(included.bags[0]?.status).toBe('packed')
  })

  it('resets every status and advances the offline generation', () => {
    const initial = snapshot()
    initial.bags[0]!.status = 'stays_here'
    initial.bags[0]!.items[0]!.status = 'packed'

    const reset = applyPackingOperation(initial, operation('reset'))
    expect(reset.generation).toBe(5)
    expect(reset.bags[0]?.status).toBe('open')
    expect(reset.bags[0]?.items.every((item) => item.status === 'open')).toBe(true)
  })
})
