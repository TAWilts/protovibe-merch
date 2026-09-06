/// <reference lib="webworker" />
import { precacheAndRoute } from 'workbox-precaching'

declare const self: ServiceWorkerGlobalScope

/**
 * A deliberately small service worker.
 *
 * It caches the application shell and the last successful sales view, and
 * nothing else. Administration, balances and profile pages are never stored:
 * a device taken to a gig should carry the till, not the band's books.
 *
 * API requests are never cached either. A stale catalogue would let a seller
 * book against prices that no longer exist; the offline queue is the answer to
 * a missing connection, not a cache.
 */
precacheAndRoute(self.__WB_MANIFEST)

const SALES_CACHE = 'merch-sales-view-v1'
const SALES_PATH = '/sales'

self.addEventListener('install', () => {
  void self.skipWaiting()
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    (async () => {
      // Drop any cache from an earlier version of this worker.
      const names = await caches.keys()
      await Promise.all(
        names.filter((name) => name.startsWith('merch-sales-view-') && name !== SALES_CACHE)
          .map((name) => caches.delete(name)),
      )
      await self.clients.claim()
    })(),
  )
})

self.addEventListener('fetch', (event) => {
  const request = event.request
  if (request.method !== 'GET') return

  const url = new URL(request.url)
  if (url.origin !== self.location.origin) return

  // The API is always live. Serving a cached catalogue would invite booking
  // against prices that have since changed.
  if (url.pathname.startsWith('/api/')) return

  // Only the sales document is kept, and only after it loaded successfully.
  if (request.mode === 'navigate' && url.pathname === SALES_PATH) {
    event.respondWith(
      (async () => {
        try {
          const response = await fetch(request)
          if (response.ok) {
            const cache = await caches.open(SALES_CACHE)
            await cache.put(SALES_PATH, response.clone())
          }
          return response
        } catch {
          const cached = await caches.match(SALES_PATH)
          if (cached) return cached
          throw new Error('offline and no cached sales view')
        }
      })(),
    )
  }
})
