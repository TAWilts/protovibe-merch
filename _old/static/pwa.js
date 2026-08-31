/* Register the narrow PWA shell only for an authenticated browser.
 *
 * A service worker is deliberately not used to cache administration/profile
 * pages. Its only private document cache is the last successful /verkauf
 * page, which enables an already prepared sales device to work at a venue.
 */
(function () {
  "use strict";
  if (!("serviceWorker" in navigator) || !window.isSecureContext) return;

  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/service-worker.js", { scope: "/" }).catch(() => {
      // The normal online app must keep working even if the browser/host does
      // not permit PWA registration (for example plain HTTP on a NAS IP).
    });
  });

  document.querySelectorAll("[data-offline-logout]").forEach((form) => {
    form.addEventListener("submit", (event) => {
      if (event.defaultPrevented) return;
      navigator.serviceWorker.controller?.postMessage({ type: "CLEAR_OFFLINE_SHELL" });
    });
  });
})();
