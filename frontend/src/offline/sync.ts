import { ApiError, request } from '@/api/client'
import type { SaleResult } from '@/api/types'

import { deviceId, markAttempt, pending, remove } from './outbox'

/**
 * Transmits queued sales.
 *
 * Retries are safe by construction: the server stores each event ID with a
 * fingerprint of its payload and replays the original answer, so sending the
 * same sale twice can never book it twice.
 */

export interface SyncOutcome {
  transmitted: number
  remaining: number
  conflicts: number
}

/** Status codes the server uses for problems a retry will not fix. */
const PERMANENT_STATUSES = new Set([400, 403, 404, 409, 422])

let running = false

export async function synchronize(): Promise<SyncOutcome> {
  // One run at a time: two concurrent flushes would send the same entry twice
  // and, while that is safe, it wastes a bad connection.
  if (running) {
    return { transmitted: 0, remaining: await (await pending()).length, conflicts: 0 }
  }
  running = true

  let transmitted = 0
  let conflicts = 0

  try {
    const device = await deviceId()
    for (const entry of await pending()) {
      try {
        await request<SaleResult>('/sales', {
          method: 'POST',
          body: {
            ...entry.payload,
            client_event_id: entry.eventId,
            client_device_id: device,
            client_created_at: entry.createdAt,
          },
        })
        // A replayed answer counts as settled too; the sale exists either way.
        await remove(entry.eventId)
        transmitted++
      } catch (error) {
        if (error instanceof ApiError && PERMANENT_STATUSES.has(error.status)) {
          // Prices or variants changed while the device was offline. The entry
          // stays visible with its reason rather than being silently dropped
          // or booked as something the seller did not agree to.
          await markAttempt(entry.eventId, error.message, true)
          conflicts++
          continue
        }
        await markAttempt(entry.eventId, error instanceof Error ? error.message : 'network')
        // A network error means the connection is gone again; stop here and
        // keep the rest queued in order.
        break
      }
    }
  } finally {
    running = false
  }

  return { transmitted, remaining: (await pending()).length, conflicts }
}
