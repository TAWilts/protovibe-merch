import 'fake-indexeddb/auto'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { enqueuePrepared, pending } from './outbox'
import { synchronize } from './sync'

const { request } = vi.hoisted(() => ({ request: vi.fn() }))

vi.mock('@/api/client', () => ({
  ApiError: class ApiError extends Error {
    constructor(readonly status: number, message: string) { super(message) }
  },
  request,
}))

describe('offline sale synchronization', () => {
  beforeEach(() => request.mockReset().mockResolvedValue({ receipt_id: 'V-1' }))

  it('sends the same envelope that was used before the response became uncertain', async () => {
    const prepared = {
      eventId: '33333333-3333-4333-8333-333333333333',
      deviceId: 'festival-tablet',
      createdAt: '2026-09-12T12:00:00.000Z',
      payload: {
        items: [{ variant_id: 7, quantity: 2 }],
        payment_method: 'Bar',
        is_paid: true,
        is_received: true,
      },
    }
    await enqueuePrepared(prepared)

    await synchronize()

    expect(request).toHaveBeenCalledWith('/sales', expect.objectContaining({
      method: 'POST',
      body: expect.objectContaining({
        client_event_id: prepared.eventId,
        client_device_id: prepared.deviceId,
        client_created_at: prepared.createdAt,
      }),
    }))
    expect((await pending()).some((entry) => entry.eventId === prepared.eventId)).toBe(false)
  })
})
