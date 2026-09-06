import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import type { BookSalePayload } from '@/api/endpoints'
import { count, enqueue, failed, remove, type QueuedSale } from '@/offline/outbox'
import { synchronize } from '@/offline/sync'

/**
 * Tracks the connection and the offline sales queue.
 *
 * The rule the whole feature rests on: a sale is never lost. If the network is
 * gone it goes into the queue, and the queue is flushed the moment the
 * connection returns — as often as needed, because transmission is idempotent.
 */
export const useOfflineStore = defineStore('offline', () => {
  const online = ref(navigator.onLine)
  const queued = ref(0)
  const conflicts = ref<QueuedSale[]>([])
  const syncing = ref(false)
  const lastSyncAt = ref<Date | null>(null)

  const hasQueue = computed(() => queued.value > 0)
  const hasConflicts = computed(() => conflicts.value.length > 0)

  async function refresh() {
    queued.value = await count()
    conflicts.value = await failed()
  }

  async function sync() {
    if (syncing.value || !online.value) return
    syncing.value = true
    try {
      await synchronize()
      lastSyncAt.value = new Date()
    } finally {
      syncing.value = false
      await refresh()
    }
  }

  /** Queues a sale that could not be sent, returning its event ID. */
  async function queue(payload: BookSalePayload): Promise<string> {
    const entry = await enqueue(payload)
    await refresh()
    return entry.eventId
  }

  /** Discards a rejected entry after the seller dealt with it. */
  async function discard(eventId: string) {
    await remove(eventId)
    await refresh()
  }

  /** Wires the browser's connection events. */
  function start() {
    window.addEventListener('online', () => {
      online.value = true
      void sync()
    })
    window.addEventListener('offline', () => {
      online.value = false
    })
    void refresh()
    void sync()
  }

  return {
    online, queued, conflicts, syncing, lastSyncAt,
    hasQueue, hasConflicts,
    refresh, sync, queue, discard, start,
  }
})
