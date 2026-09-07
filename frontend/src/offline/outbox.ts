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

/** Adds a sale to the queue. */
export async function enqueue(payload: BookSalePayload): Promise<QueuedSale> {
  const entry: QueuedSale = {
    eventId: crypto.randomUUID(),
    payload,
    createdAt: new Date().toISOString(),
    attempts: 0,
  }
  const instance = await offlineDB()
  await instance.put('sales', entry)
  return entry
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
