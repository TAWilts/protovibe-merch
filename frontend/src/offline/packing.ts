import { ApiError } from '@/api/client'
import { packingApi } from '@/api/endpoints'
import type {
  PackingBag,
  PackingItem,
  PackingOperation,
  PackingOperationType,
  PackingPhoto,
  PackingSnapshot,
  PackingStatus,
} from '@/api/types'
import { deviceId } from './outbox'
import {
  offlineDB,
  packingNamespace,
  type PackingNamespace,
  type PackingQueueRecord,
} from './database'

export interface PackingSyncOutcome {
  snapshot: PackingSnapshot
  transmitted: number
  remaining: number
  conflicts: number
  messages: string[]
  authenticationRequired: boolean
}

const PERMANENT_STATUSES = new Set([400, 403, 404, 409, 422, 507])
let syncing = false
const authenticationPaused = new Set<string>()

function clone(snapshot: PackingSnapshot): PackingSnapshot {
  return structuredClone(snapshot)
}

function statusResolved(status: PackingStatus) {
  return status === 'packed' || status === 'stays_here'
}

function recomputeBag(bag: PackingBag) {
  if (bag.status === 'stays_here') return
  bag.status = bag.items.length === 0 || bag.items.every((item) => statusResolved(item.status))
    ? 'packed'
    : 'open'
}

export function applyPackingOperation(
  current: PackingSnapshot,
  operation: PackingOperation,
): PackingSnapshot {
  const snapshot = clone(current)
  const bag = operation.bag_id
    ? snapshot.bags.find((entry) => entry.id === operation.bag_id)
    : undefined
  const item = operation.item_id
    ? snapshot.bags.flatMap((entry) => entry.items).find((entry) => entry.id === operation.item_id)
    : undefined

  switch (operation.type) {
    case 'create_bag':
      snapshot.bags.push({
        id: operation.bag_id!, name: operation.name!, status: 'open',
        position: snapshot.bags.length * 10, photos: [], items: [],
      })
      break
    case 'rename_bag':
      if (bag) bag.name = operation.name!
      break
    case 'delete_bag':
      snapshot.bags = snapshot.bags.filter((entry) => entry.id !== operation.bag_id)
      break
    case 'reorder_bags': {
      const order = new Map(operation.order_ids?.map((id, index) => [id, index]) ?? [])
      snapshot.bags.sort((a, b) => (order.get(a.id) ?? 999999) - (order.get(b.id) ?? 999999))
      snapshot.bags.forEach((entry, index) => { entry.position = index * 10 })
      break
    }
    case 'create_item':
      if (bag) {
        bag.items.push({
          id: operation.item_id!, bag_id: bag.id, name: operation.name!, status: 'open',
          position: bag.items.length * 10, photos: [],
        })
        if (bag.status !== 'stays_here') bag.status = 'open'
      }
      break
    case 'rename_item':
      if (item) item.name = operation.name!
      break
    case 'delete_item':
      for (const parent of snapshot.bags) {
        parent.items = parent.items.filter((entry) => entry.id !== operation.item_id)
        recomputeBag(parent)
      }
      break
    case 'reorder_items':
      if (bag) {
        const order = new Map(operation.order_ids?.map((id, index) => [id, index]) ?? [])
        bag.items.sort((a, b) => (order.get(a.id) ?? 999999) - (order.get(b.id) ?? 999999))
        bag.items.forEach((entry, index) => { entry.position = index * 10 })
      }
      break
    case 'set_item_status':
      if (item) {
        item.status = operation.status!
        const parent = snapshot.bags.find((entry) => entry.id === item.bag_id)
        if (parent) recomputeBag(parent)
      }
      break
    case 'set_bag_status':
      if (bag) {
        const previous = bag.status
        bag.status = operation.status!
        if (operation.status === 'packed') {
          bag.items.forEach((entry) => {
            if (entry.status !== 'stays_here') entry.status = 'packed'
          })
        } else if (operation.status === 'open') {
          if (previous === 'stays_here') recomputeBag(bag)
          else {
            bag.items.forEach((entry) => {
              if (entry.status === 'packed') entry.status = 'open'
            })
          }
        }
      }
      break
    case 'delete_photo':
      for (const parent of snapshot.bags) {
        parent.photos = parent.photos.filter((photo) => photo.id !== operation.photo_id)
        parent.items.forEach((entry) => {
          entry.photos = entry.photos.filter((photo) => photo.id !== operation.photo_id)
        })
      }
      break
    case 'reset':
      snapshot.generation++
      snapshot.bags.forEach((entry) => {
        entry.status = 'open'
        entry.items.forEach((child) => { child.status = 'open' })
      })
      break
  }
  return snapshot
}

export async function loadPackingSnapshot(context: PackingNamespace): Promise<PackingSnapshot | null> {
  const db = await offlineDB()
  return (await db.get('packing_snapshots', packingNamespace(context)))?.snapshot ?? null
}

export async function savePackingSnapshot(context: PackingNamespace, snapshot: PackingSnapshot) {
  const db = await offlineDB()
  await db.put('packing_snapshots', {
    namespace: packingNamespace(context), snapshot, savedAt: new Date().toISOString(),
  })
}

export async function rememberPackingContext(context: PackingNamespace) {
  const db = await offlineDB()
  await db.put('meta', JSON.stringify(context), 'last-packing-context')
}

export async function lastPackingContext(): Promise<PackingNamespace | null> {
  const raw = await (await offlineDB()).get('meta', 'last-packing-context')
  if (!raw) return null
  try { return JSON.parse(raw) as PackingNamespace } catch { return null }
}

async function queueEntries(context: PackingNamespace, includeFailed = false) {
  const db = await offlineDB()
  const namespace = packingNamespace(context)
  const all = await db.getAllFromIndex(
    'packing_queue', 'by-namespace-created',
    IDBKeyRange.bound([namespace, ''], [namespace, '\uffff']),
  )
  return all.filter((entry) => includeFailed || !entry.failedPermanently)
}

export async function packingQueueCount(context?: PackingNamespace): Promise<number> {
  if (context) return (await queueEntries(context)).length
  const all = await (await offlineDB()).getAll('packing_queue')
  return all.filter((entry) => !entry.failedPermanently).length
}

export async function packingFailures(context: PackingNamespace): Promise<PackingQueueRecord[]> {
  return (await queueEntries(context, true)).filter((entry) => entry.failedPermanently)
}

export async function resolvePackingFailure(key: string, retry: boolean): Promise<void> {
  const db = await offlineDB()
  const entry = await db.get('packing_queue', key)
  if (!entry) return
  if (!retry) {
    await db.delete('packing_queue', key)
    return
  }
  entry.failedPermanently = false
  entry.lastError = undefined
  await db.put('packing_queue', entry)
}

export async function createPackingOperation(
  context: PackingNamespace,
  snapshot: PackingSnapshot,
  type: PackingOperationType,
  payload: Partial<PackingOperation> = {},
): Promise<{ operation: PackingOperation; snapshot: PackingSnapshot }> {
  const operation: PackingOperation = {
    event_id: crypto.randomUUID(),
    device_id: await deviceId(),
    client_created_at: new Date().toISOString(),
    base_generation: snapshot.generation,
    type,
    ...payload,
  }
  const next = applyPackingOperation(snapshot, operation)
  const namespace = packingNamespace(context)
  const entry: PackingQueueRecord = {
    key: `${namespace}:${operation.event_id}`, namespace, eventId: operation.event_id,
    createdAt: operation.client_created_at, kind: 'operation', operation, attempts: 0,
  }
  const db = await offlineDB()
  const tx = db.transaction(['packing_queue', 'packing_snapshots'], 'readwrite')
  await tx.objectStore('packing_queue').put(entry)
  await tx.objectStore('packing_snapshots').put({ namespace, snapshot: next, savedAt: new Date().toISOString() })
  await tx.done
  return { operation, snapshot: next }
}

export async function queuePackingPhoto(
  context: PackingNamespace,
  snapshot: PackingSnapshot,
  input: { bagId?: string; itemId?: string; file: File },
): Promise<PackingSnapshot> {
  if (input.file.size > 10 * 1024 * 1024) throw new Error('packing photo exceeds 10 MB')
  if (!['image/jpeg', 'image/png', 'image/webp'].includes(input.file.type)) {
    throw new Error('packing photo has an unsupported type')
  }
  const namespace = packingNamespace(context)
  const eventId = crypto.randomUUID()
  const photoId = crypto.randomUUID()
  const photo: PackingPhoto = {
    id: photoId, bag_id: input.bagId, item_id: input.itemId,
    original_filename: input.file.name, size_bytes: input.file.size, position: 0,
  }
  const next = clone(snapshot)
  if (input.bagId) {
    const owner = next.bags.find((entry) => entry.id === input.bagId)
    if (owner) { photo.position = owner.photos.length * 10; owner.photos.push(photo) }
  } else if (input.itemId) {
    const owner = next.bags.flatMap((entry) => entry.items).find((entry) => entry.id === input.itemId)
    if (owner) { photo.position = owner.photos.length * 10; owner.photos.push(photo) }
  }
  const entry: PackingQueueRecord = {
    key: `${namespace}:${eventId}`, namespace, eventId, createdAt: new Date().toISOString(),
    kind: 'photo', attempts: 0,
    photo: { photoId, bagId: input.bagId, itemId: input.itemId, filename: input.file.name, blob: input.file },
  }
  const db = await offlineDB()
  const tx = db.transaction(['packing_queue', 'packing_snapshots', 'packing_photo_cache'], 'readwrite')
  await tx.objectStore('packing_queue').put(entry)
  await tx.objectStore('packing_snapshots').put({ namespace, snapshot: next, savedAt: new Date().toISOString() })
  await tx.objectStore('packing_photo_cache').put({ key: `${namespace}:${photoId}`, namespace, photoId, blob: input.file })
  await tx.done
  return next
}

async function markFailure(entry: PackingQueueRecord, error: unknown, permanent: boolean) {
  entry.attempts++
  entry.lastError = error instanceof Error ? error.message : String(error)
  entry.failedPermanently = permanent
  await (await offlineDB()).put('packing_queue', entry)
}

async function overlayQueue(context: PackingNamespace, server: PackingSnapshot) {
  let result = server
  for (const entry of await queueEntries(context, true)) {
    if (entry.operation) result = applyPackingOperation(result, entry.operation)
    if (entry.photo) {
      const photo: PackingPhoto = {
        id: entry.photo.photoId, bag_id: entry.photo.bagId, item_id: entry.photo.itemId,
        original_filename: entry.photo.filename, size_bytes: entry.photo.blob.size, position: 0,
      }
      const owner = entry.photo.bagId
        ? result.bags.find((bag) => bag.id === entry.photo!.bagId)
        : result.bags.flatMap((bag) => bag.items).find((item) => item.id === entry.photo!.itemId)
      if (owner && !owner.photos.some((existing) => existing.id === photo.id)) {
        photo.position = owner.photos.length * 10
        owner.photos.push(photo)
      }
    }
  }
  return result
}

export async function cachePackingPhotos(context: PackingNamespace, snapshot: PackingSnapshot) {
  const db = await offlineDB()
  const namespace = packingNamespace(context)
  const photos = snapshot.bags.flatMap((bag) => [...bag.photos, ...bag.items.flatMap((item) => item.photos)])
  const wanted = new Set(photos.map((photo) => photo.id))
  for (const photo of photos) {
    const key = `${namespace}:${photo.id}`
    if (await db.get('packing_photo_cache', key)) continue
    try {
      const response = await fetch(packingApi.photoUrl(photo.id), { credentials: 'include' })
      if (response.ok) await db.put('packing_photo_cache', { key, namespace, photoId: photo.id, blob: await response.blob() })
    } catch { /* The metadata stays usable even if a weak connection loses an image. */ }
  }
  const cached = await db.getAllFromIndex('packing_photo_cache', 'by-namespace', namespace)
  await Promise.all(cached.filter((entry) => !wanted.has(entry.photoId)).map((entry) => db.delete('packing_photo_cache', entry.key)))
}

export async function packingPhotoBlob(context: PackingNamespace, photoId: string): Promise<Blob | null> {
  return (await (await offlineDB()).get('packing_photo_cache', `${packingNamespace(context)}:${photoId}`))?.blob ?? null
}

export async function synchronizePacking(context: PackingNamespace): Promise<PackingSyncOutcome> {
  const local = await loadPackingSnapshot(context) ?? { revision: 0, generation: 1, bags: [] }
  const namespace = packingNamespace(context)
  if (syncing || !navigator.onLine || authenticationPaused.has(namespace)) {
    return { snapshot: local, transmitted: 0, remaining: await packingQueueCount(context), conflicts: 0, messages: [], authenticationRequired: authenticationPaused.has(namespace) }
  }
  syncing = true
  let server: PackingSnapshot = local
  let transmitted = 0
  let conflicts = 0
  const messages: string[] = []
  let authenticationRequired = false
  let generationOverride: number | null = null
  try {
    for (const entry of await queueEntries(context)) {
      try {
        if (entry.operation) {
          const operation = { ...entry.operation }
          if (generationOverride !== null && (operation.type === 'set_bag_status' || operation.type === 'set_item_status')) {
            operation.base_generation = generationOverride
          }
          server = await packingApi.apply(operation)
          if (operation.type === 'reset') generationOverride = server.generation
        } else if (entry.photo) {
          server = await packingApi.uploadPhoto({
            eventId: entry.eventId, deviceId: await deviceId(), clientCreatedAt: entry.createdAt,
            photoId: entry.photo.photoId, bagId: entry.photo.bagId, itemId: entry.photo.itemId,
            file: entry.photo.blob, filename: entry.photo.filename,
          })
        }
        await (await offlineDB()).delete('packing_queue', entry.key)
        transmitted++
      } catch (error) {
        if (error instanceof ApiError && error.status === 401) {
          authenticationPaused.add(namespace)
          authenticationRequired = true
          messages.push('Die Sitzung ist abgelaufen. Die Packliste bleibt lokal gespeichert und wird nach der nächsten Anmeldung synchronisiert.')
          break
        }
        if (error instanceof ApiError && error.detailCode === 'packing_generation_conflict') {
          await (await offlineDB()).delete('packing_queue', entry.key)
          conflicts++
          messages.push('Die Packliste wurde zwischenzeitlich zurückgesetzt; eine alte Statusänderung wurde verworfen.')
          continue
        }
        if (error instanceof ApiError && PERMANENT_STATUSES.has(error.status)) {
          await markFailure(entry, error, true)
          conflicts++
          messages.push(error.message)
          continue
        }
        await markFailure(entry, error, false)
        break
      }
    }
    try { server = await packingApi.snapshot() } catch { /* Keep the last confirmed/local state. */ }
    server = await overlayQueue(context, server)
    await savePackingSnapshot(context, server)
    void cachePackingPhotos(context, server)
    return { snapshot: server, transmitted, remaining: await packingQueueCount(context), conflicts, messages, authenticationRequired }
  } finally {
    syncing = false
  }
}

export function resumePackingSynchronization(context: PackingNamespace) {
  authenticationPaused.delete(packingNamespace(context))
}
