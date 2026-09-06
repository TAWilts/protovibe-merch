/* Local outbox for sales captured without reception.
 *
 * Every sale receives its UUID before the first network attempt. If the server
 * commits but the browser loses the response, the exact same event is queued
 * and later retried. The server's sync_events ledger then returns its original
 * response instead of creating a second receipt.
 */
(function () {
  "use strict";
  const user = window.MERCH_APP.currentUser;
  const panel = document.getElementById("offline-sync-panel");
  const title = document.getElementById("offline-sync-title");
  const detail = document.getElementById("offline-sync-detail");
  if (!user || !window.indexedDB || !window.isSecureContext) {
    if (panel && title && detail) {
      panel.dataset.connection = "offline";
      title.textContent = "Offline-Modus benötigt HTTPS";
      detail.textContent = "Online-Verkäufe funktionieren normal. Für eine lokale Verkaufswarteschlange bitte die App über HTTPS öffnen.";
    }
    return;
  }

  const DATABASE_NAME = "protovibe-merch-offline";
  const DATABASE_VERSION = 1;
  const META_STORE = "meta";
  const OUTBOX_STORE = "sales_outbox";
  const RETRY_DELAYS_MS = [5000, 15000, 30000, 60000];
  let databasePromise = null;
  let pendingCount = 0;
  let syncing = false;
  let lastError = "";
  let retryTimer = null;
  let retryAttempt = 0;
  let retryAllowed = true;

  function randomUuid() {
    if (window.crypto?.randomUUID) return window.crypto.randomUUID();
    const bytes = new Uint8Array(16);
    window.crypto.getRandomValues(bytes);
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
  }

  function database() {
    if (databasePromise) return databasePromise;
    databasePromise = new Promise((resolve, reject) => {
      const open = window.indexedDB.open(DATABASE_NAME, DATABASE_VERSION);
      open.onupgradeneeded = () => {
        const db = open.result;
        if (!db.objectStoreNames.contains(META_STORE)) db.createObjectStore(META_STORE, { keyPath: "key" });
        if (!db.objectStoreNames.contains(OUTBOX_STORE)) db.createObjectStore(OUTBOX_STORE, { keyPath: "client_event_id" });
      };
      open.onsuccess = () => resolve(open.result);
      open.onerror = () => reject(open.error || new Error("Offline-Speicher konnte nicht geöffnet werden."));
    });
    return databasePromise;
  }

  async function readMeta(key) {
    const db = await database();
    return new Promise((resolve, reject) => {
      const request = db.transaction(META_STORE).objectStore(META_STORE).get(key);
      request.onsuccess = () => resolve(request.result?.value);
      request.onerror = () => reject(request.error);
    });
  }

  async function writeMeta(key, value) {
    const db = await database();
    return new Promise((resolve, reject) => {
      const transaction = db.transaction(META_STORE, "readwrite");
      transaction.objectStore(META_STORE).put({ key, value });
      transaction.oncomplete = () => resolve();
      transaction.onerror = () => reject(transaction.error);
    });
  }

  async function allOutboxEntries() {
    const db = await database();
    return new Promise((resolve, reject) => {
      const request = db.transaction(OUTBOX_STORE).objectStore(OUTBOX_STORE).getAll();
      request.onsuccess = () => resolve(request.result || []);
      request.onerror = () => reject(request.error);
    });
  }

  async function putOutboxEntry(entry) {
    const db = await database();
    return new Promise((resolve, reject) => {
      const transaction = db.transaction(OUTBOX_STORE, "readwrite");
      transaction.objectStore(OUTBOX_STORE).put(entry);
      transaction.oncomplete = () => resolve();
      transaction.onerror = () => reject(transaction.error);
    });
  }

  async function deleteOutboxEntry(eventId) {
    const db = await database();
    return new Promise((resolve, reject) => {
      const transaction = db.transaction(OUTBOX_STORE, "readwrite");
      transaction.objectStore(OUTBOX_STORE).delete(eventId);
      transaction.oncomplete = () => resolve();
      transaction.onerror = () => reject(transaction.error);
    });
  }

  async function deviceId() {
    let value = await readMeta("device_id");
    if (!value) {
      value = randomUuid();
      await writeMeta("device_id", value);
    }
    return value;
  }

  async function currentEntries() {
    const entries = await allOutboxEntries();
    return entries
      .filter((entry) => Number(entry.client_actor_id) === Number(user.id))
      .sort((left, right) => String(left.client_created_at).localeCompare(String(right.client_created_at)));
  }

  function renderStatus(message = "") {
    if (!panel || !title || !detail) return;
    const offline = !navigator.onLine;
    panel.dataset.connection = offline ? "offline" : "online";
    if (syncing) {
      title.textContent = "Synchronisiere Offline-Verkäufe …";
      detail.textContent = `${pendingCount} lokale Buchung(en) werden geprüft.`;
    } else if (offline) {
      title.textContent = "Offline-Modus aktiv";
      detail.textContent = pendingCount
        ? `${pendingCount} Verkauf/Verkäufe warten sicher auf Synchronisierung.`
        : "Neue Verkäufe werden lokal vorgemerkt und bei Netzverbindung übertragen.";
    } else if (pendingCount) {
      title.textContent = "Synchronisierung ausstehend";
      detail.textContent = message || `${pendingCount} lokale Verkauf/Verkäufe warten auf Übertragung.`;
    } else {
      title.textContent = "Online und synchron";
      detail.textContent = message || "Verkäufe werden direkt auf dem Server gesichert.";
    }
  }

  function cancelRetry() {
    if (retryTimer === null) return;
    window.clearTimeout(retryTimer);
    retryTimer = null;
  }

  function scheduleRetry(delay) {
    cancelRetry();
    if (!navigator.onLine || !pendingCount || syncing || !retryAllowed) return;
    const retryDelay = Number.isFinite(delay)
      ? delay
      : RETRY_DELAYS_MS[Math.min(retryAttempt, RETRY_DELAYS_MS.length - 1)];
    retryAttempt = Math.min(retryAttempt + 1, RETRY_DELAYS_MS.length - 1);
    retryTimer = window.setTimeout(() => {
      retryTimer = null;
      syncPending().catch(() => {});
    }, retryDelay);
  }

  async function refreshStatus(message = "") {
    try {
      pendingCount = (await currentEntries()).length;
      if (!message && lastError) message = lastError;
      renderStatus(message);
    } catch (error) {
      lastError = "Der lokale Offline-Speicher ist nicht verfügbar.";
      renderStatus(lastError);
    }
    return pendingCount;
  }

  async function prepareSale(payload) {
    return {
      ...payload,
      client_event_id: randomUuid(),
      client_device_id: await deviceId(),
      client_actor_id: Number(user.id),
      client_created_at: new Date().toISOString(),
    };
  }

  async function queueSale(payload) {
    await putOutboxEntry({ ...payload, queued_at: new Date().toISOString(), last_error: "" });
    lastError = "";
    retryAllowed = true;
    await refreshStatus();
    if (navigator.onLine) scheduleRetry(1000);
    return payload;
  }

  async function acknowledgeSale(eventId) {
    await deleteOutboxEntry(eventId);
    lastError = "";
    await refreshStatus();
    if (!pendingCount) {
      retryAttempt = 0;
      cancelRetry();
    }
  }

  async function syncPending() {
    if (syncing || !navigator.onLine) {
      await refreshStatus();
      return { synced: 0, pending: pendingCount };
    }
    cancelRetry();
    let entries;
    try {
      entries = await currentEntries();
    } catch (_) {
      lastError = "Der lokale Offline-Speicher ist nicht verfügbar.";
      await refreshStatus(lastError);
      return { synced: 0, pending: pendingCount };
    }
    if (!entries.length) {
      retryAttempt = 0;
      await refreshStatus();
      return { synced: 0, pending: 0 };
    }
    syncing = true;
    pendingCount = entries.length;
    lastError = "";
    renderStatus();
    let synced = 0;
    try {
      for (const entry of entries) {
        let response;
        let body;
        try {
          response = await fetch("/api/sales", {
            method: "POST",
            headers: { "Content-Type": "application/json", "X-CSRF-Token": window.MERCH_APP.csrfToken },
            body: JSON.stringify(entry),
          });
          body = await response.json();
        } catch (_) {
          lastError = "Die Verbindung ist erneut abgebrochen. Die verbleibenden Verkäufe bleiben lokal erhalten.";
          break;
        }
        if (!response.ok || !body.ok) {
          const error = body?.error || "Offline-Verkauf konnte noch nicht synchronisiert werden.";
          await putOutboxEntry({ ...entry, last_error: error });
          lastError = error;
          // The current login is required to protect the user binding. More
          // attempts with another account cannot fix this and only obscure the
          // actionable reason in the status panel.
          if (response.status === 401 || response.status === 403) {
            retryAllowed = false;
            break;
          }
          // Other client-side validation failures also need a correction on
          // the server or in a future version of the app; retrying them in a
          // tight loop would not make progress.
          if (response.status >= 400 && response.status < 500 && response.status !== 408 && response.status !== 429) {
            retryAllowed = false;
            break;
          }
          continue;
        }
        await deleteOutboxEntry(entry.client_event_id);
        synced += 1;
        window.dispatchEvent(new CustomEvent("merch-offline-sale-synced", { detail: body }));
      }
    } finally {
      syncing = false;
      await refreshStatus(synced ? `${synced} Offline-Verkauf/Verkäufe wurden übertragen.` : lastError);
      if (pendingCount && navigator.onLine && retryAllowed) {
        scheduleRetry();
      } else if (!pendingCount) {
        retryAttempt = 0;
        cancelRetry();
      }
    }
    return { synced, pending: pendingCount };
  }

  window.MerchOffline = {
    prepareSale,
    queueSale,
    acknowledgeSale,
    refreshStatus,
    syncPending,
    isOffline: () => !navigator.onLine,
  };

  window.addEventListener("online", () => {
    retryAllowed = true;
    retryAttempt = 0;
    cancelRetry();
    syncPending().catch(() => {});
  });
  window.addEventListener("offline", () => {
    cancelRetry();
    refreshStatus();
  });
  document.querySelectorAll("[data-offline-logout]").forEach((form) => {
    form.addEventListener("submit", (event) => {
      if (pendingCount && !window.confirm(
        "Es warten noch Offline-Verkäufe. Sie bleiben auf diesem Gerät gespeichert und können nur nach Anmeldung mit demselben Konto synchronisiert werden. Trotzdem abmelden?"
      )) event.preventDefault();
    });
  });
  refreshStatus().then(() => {
    if (navigator.onLine) syncPending().catch(() => {});
  });
})();
