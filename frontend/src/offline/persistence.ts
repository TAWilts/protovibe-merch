/**
 * Asks the browser to protect IndexedDB and caches from automatic eviction.
 *
 * This is strictly best effort: unsupported browsers, denied requests and
 * browser-specific failures must never delay or break normal application use.
 */
export async function requestPersistentStorage(): Promise<boolean | undefined> {
  const storage = navigator.storage
  if (typeof storage?.persist !== 'function') return undefined
  try {
    return await storage.persist()
  } catch {
    return false
  }
}
