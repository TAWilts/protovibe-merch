import 'fake-indexeddb/auto'
import { openDB } from 'idb'
import { describe, expect, it } from 'vitest'

import { offlineDB } from './database'

describe('offline database migration', () => {
  it('adds packing and assortment stores without losing queued sales from schema version 1', async () => {
    const legacy = await openDB('merch-offline', 1, {
      upgrade(db) {
        const sales = db.createObjectStore('sales', { keyPath: 'eventId' })
        sales.createIndex('by-created', 'createdAt')
        db.createObjectStore('meta')
      },
    })
    await legacy.put('sales', {
      eventId: 'existing-sale', payload: { items: [] }, createdAt: '2026-09-08T00:00:00Z', attempts: 0,
    })
    legacy.close()

    const upgraded = await offlineDB()
    expect(await upgraded.get('sales', 'existing-sale')).toMatchObject({ eventId: 'existing-sale' })
    expect([...upgraded.objectStoreNames]).toEqual(expect.arrayContaining([
      'sales', 'meta', 'packing_snapshots', 'packing_queue', 'packing_photo_cache', 'sales_assortments',
    ]))
  })
})
