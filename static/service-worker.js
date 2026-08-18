/* Offline shell for the sales view.
 *
 * The actual accounting database is never copied into the browser. The worker
 * only keeps the latest authenticated sales screen and static code so that the
 * local outbox in IndexedDB can collect sales until the server is reachable.
 */
const STATIC_CACHE = "protovibe-merch-static-v1.4.6";
const USER_CACHE = "protovibe-merch-sales-v1.4.6";
const STATIC_ASSETS = [
  "/static/app.css",
  "/static/transaction.js",
  "/static/sales.js",
  "/static/offline-sales.js",
  "/static/pwa.js",
  "/static/table-filters.js",
  "/static/pwa-icon.svg",
  "/static/manifest.webmanifest",
];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(STATIC_CACHE).then((cache) => cache.addAll(STATIC_ASSETS)).then(() => self.skipWaiting())
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys().then((keys) => Promise.all(
      keys
        .filter((key) => key.startsWith("protovibe-merch-") && ![STATIC_CACHE, USER_CACHE].includes(key))
        .map((key) => caches.delete(key))
    )).then(() => self.clients.claim())
  );
});

function salesCacheRequest() {
  return new Request(new URL("/verkauf", self.location.origin).href);
}

async function cacheLatestSalesPage(request) {
  try {
    const response = await fetch(request);
    const finalUrl = new URL(response.url);
    if (response.ok && finalUrl.origin === self.location.origin && finalUrl.pathname === "/verkauf") {
      const cache = await caches.open(USER_CACHE);
      await cache.put(salesCacheRequest(), response.clone());
    }
    return response;
  } catch (_) {
    const cached = await caches.match(salesCacheRequest());
    if (cached) return cached;
    return new Response(
      "<h1>Verkauf noch nicht für Offline vorbereitet</h1><p>Öffne die Verkaufsansicht einmal online, bevor du ohne Empfang arbeitest.</p>",
      { status: 503, headers: { "Content-Type": "text/html; charset=utf-8" } }
    );
  }
}

async function cacheFirst(request) {
  const cached = await caches.match(request);
  if (cached) return cached;
  const response = await fetch(request);
  if (response.ok) {
    const cache = await caches.open(STATIC_CACHE);
    cache.put(request, response.clone());
  }
  return response;
}

self.addEventListener("fetch", (event) => {
  const request = event.request;
  if (request.method !== "GET") return;
  const url = new URL(request.url);
  if (url.origin !== self.location.origin) return;
  if (url.pathname === "/verkauf" && request.mode === "navigate") {
    event.respondWith(cacheLatestSalesPage(request));
    return;
  }
  if (url.pathname.startsWith("/static/")) event.respondWith(cacheFirst(request));
});

self.addEventListener("message", (event) => {
  if (event.data?.type === "CLEAR_OFFLINE_SHELL") {
    event.waitUntil(caches.delete(USER_CACHE));
  }
});
