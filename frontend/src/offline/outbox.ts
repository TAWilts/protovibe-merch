import type { BookSalePayload } from '@/api/endpoints'
import { offlineDB, type QueuedSaleRecord } from './database'

/**
 * The offline sales queue.
 *
 * A sale made without a connection is written here first and transmitted
 * later. Each entry carries a durable client event ID generated at the moment
 * of the sale; the server records that ID and replays its original answer on a
 * retry, so a phone can synchronise as often as it likes without double-booking.
 */

/** A durable sale whose event ID makes repeated transmission idempotent. */
export type QueuedSale = QueuedSaleRecord

/**
 * Stable metadata allocated before the first delivery attempt.
 *
 * The base payload deliberately stays separate from the transport fields. The
 * latter are added only at the API boundary and retained beside the payload in
 * IndexedDB if the response becomes uncertain.
 */
export interface PreparedSale {
  eventId: string
  deviceId: string
  createdAt: string
  payload: BookSalePayload
}

/**
 * Returns this device's stable identifier, creating one on first use.
 *
 * It is only used to attribute a queued sale to the phone it was made on,
 * which helps when two devices sold at the same stand.
 */
export async function deviceId(): Promise<string> {
  const instance = await offlineDB()
  const existing = await instance.get('meta', 'device-id')
  if (existing) return existing

  const generated = crypto.randomUUID()
  await instance.put('meta', generated, 'device-id')
  return generated
}

/** Allocates the idempotency envelope before any request reaches the server. */
export async function prepareSale(payload: BookSalePayload): Promise<PreparedSale> {
  return {
    eventId: payload.client_event_id ?? crypto.randomUUID(),
    deviceId: payload.client_device_id ?? await deviceId(),
    createdAt: payload.client_created_at ?? new Date().toISOString(),
    payload,
  }
}

/** Builds the exact request used by both the initial delivery and every retry. */
export function preparedSalePayload(prepared: PreparedSale): BookSalePayload {
  return {
    ...prepared.payload,
    client_event_id: prepared.eventId,
    client_device_id: prepared.deviceId,
    client_created_at: prepared.createdAt,
  }
}

/** Persists an already prepared sale without replacing its identity. */
export async function enqueuePrepared(prepared: PreparedSale): Promise<QueuedSale> {
  const entry: QueuedSale = {
    eventId: prepared.eventId,
    deviceId: prepared.deviceId,
    payload: prepared.payload,
    createdAt: prepared.createdAt,
    attempts: 0,
  }
  const instance = await offlineDB()
  await instance.put('sales', entry)
  return entry
}

/** Adds a sale that has not yet been prepared to the queue. */
export async function enqueue(payload: BookSalePayload): Promise<QueuedSale> {
  return enqueuePrepared(await prepareSale(payload))
}

/** Returns the queued sales, oldest first, so they book in the order made. */
export async function pending(): Promise<QueuedSale[]> {
  const instance = await offlineDB()
  const all = await instance.getAllFromIndex('sales', 'by-created')
  return all.filter((entry) => !entry.failedPermanently)
}

/** Returns the entries the server rejected outright, which need a person. */
export async function failed(): Promise<QueuedSale[]> {
  const instance = await offlineDB()
  const all = await instance.getAll('sales')
  return all.filter((entry) => entry.failedPermanently)
}

export async function remove(eventId: string): Promise<void> {
  const instance = await offlineDB()
  await instance.delete('sales', eventId)
}

export async function markAttempt(
  eventId: string,
  error?: string,
  permanent = false,
): Promise<void> {
  const instance = await offlineDB()
  const entry = await instance.get('sales', eventId)
  if (!entry) return

  entry.attempts += 1
  entry.lastError = error
  entry.failedPermanently = permanent
  await instance.put('sales', entry)
}

export async function count(): Promise<number> {
  return (await pending()).length
}
