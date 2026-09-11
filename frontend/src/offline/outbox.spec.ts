import 'fake-indexeddb/auto'
import { describe, expect, it, vi } from 'vitest'

import { enqueuePrepared, pending, prepareSale, preparedSalePayload } from './outbox'

describe('offline sale envelope', () => {
  it('retains one stable identity from the initial request through IndexedDB', async () => {
    vi.spyOn(crypto, 'randomUUID').mockReturnValueOnce('11111111-1111-4111-8111-111111111111')
      .mockReturnValueOnce('22222222-2222-4222-8222-222222222222')
    const payload = {
      items: [{ variant_id: 7, quantity: 2 }],
      payment_method: 'Bar',
      is_paid: true,
      is_received: true,
    }

    const prepared = await prepareSale(payload)
    const request = preparedSalePayload(prepared)
    await enqueuePrepared(prepared)

    expect(request).toMatchObject({
      client_event_id: prepared.eventId,
      client_device_id: prepared.deviceId,
      client_created_at: prepared.createdAt,
    })
    expect(await pending()).toContainEqual(expect.objectContaining({
      eventId: prepared.eventId,
      deviceId: prepared.deviceId,
      createdAt: prepared.createdAt,
      payload,
    }))
  })
})
