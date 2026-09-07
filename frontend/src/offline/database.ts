import { openDB, type DBSchema, type IDBPDatabase } from 'idb'

import type { BookSalePayload } from '@/api/endpoints'
import type { PackingOperation, PackingSnapshot } from '@/api/types'

export interface QueuedSaleRecord {
  eventId: string
  payload: BookSalePayload
  createdAt: string
  attempts: number
  lastError?: string
  failedPermanently?: boolean
}

export interface PackingNamespace {
  bandId: number
  userId: number
}

export interface PackingQueueRecord {
  key: string
  namespace: string
  eventId: string
  createdAt: string
  kind: 'operation' | 'photo'
  operation?: PackingOperation
  photo?: {
    photoId: string
    bagId?: string
    itemId?: string
    filename: string
    blob: Blob
  }
  attempts: number
  lastError?: string
  failedPermanently?: boolean
}

export interface PackingSnapshotRecord {
  namespace: string
  snapshot: PackingSnapshot
  savedAt: string
}

export interface PackingPhotoCacheRecord {
  key: string
  namespace: string
  photoId: string
  blob: Blob
}

export interface OfflineSchema extends DBSchema {
  sales: {
    key: string
    value: QueuedSaleRecord
    indexes: { 'by-created': string }
  }
  meta: { key: string; value: string }
  packing_snapshots: { key: string; value: PackingSnapshotRecord }
  packing_queue: {
    key: string
    value: PackingQueueRecord
    indexes: { 'by-namespace-created': [string, string] }
  }
  packing_photo_cache: {
    key: string
    value: PackingPhotoCacheRecord
    indexes: { 'by-namespace': string }
  }
}

const DB_NAME = 'merch-offline'
const DB_VERSION = 2
let connection: Promise<IDBPDatabase<OfflineSchema>> | null = null

export function offlineDB() {
  if (!connection) {
    connection = openDB<OfflineSchema>(DB_NAME, DB_VERSION, {
      upgrade(instance, oldVersion) {
        if (oldVersion < 1) {
          const sales = instance.createObjectStore('sales', { keyPath: 'eventId' })
          sales.createIndex('by-created', 'createdAt')
          instance.createObjectStore('meta')
        }
        if (oldVersion < 2) {
          instance.createObjectStore('packing_snapshots', { keyPath: 'namespace' })
          const queue = instance.createObjectStore('packing_queue', { keyPath: 'key' })
          queue.createIndex('by-namespace-created', ['namespace', 'createdAt'])
          const photos = instance.createObjectStore('packing_photo_cache', { keyPath: 'key' })
          photos.createIndex('by-namespace', 'namespace')
        }
      },
      terminated() { connection = null },
    })
  }
  return connection
}

export function packingNamespace(value: PackingNamespace): string {
  return `${value.bandId}:${value.userId}`
}
