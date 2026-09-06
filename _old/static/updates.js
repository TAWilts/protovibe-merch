/*
 * Lightweight release notification for administrators.
 *
 * The app never installs an update itself.  This script only asks the local
 * Flask endpoint for its cached comparison with the newest GitHub release and
 * updates a small header indicator plus the optional Updates page.
 */
(() => {
  const indicator = document.querySelector("[data-update-indicator]");
  if (!indicator) return;

  const endpoint = indicator.dataset.updateStatusUrl;
  const currentVersion = indicator.dataset.currentVersion;
  const panel = document.querySelector("[data-update-panel]");
  const checkButton = document.querySelector("[data-update-check]");
  const currentVersionElement = document.querySelector("[data-update-current-version]");
  const latestVersionElement = document.querySelector("[data-update-latest-version]");
  const publishedAtElement = document.querySelector("[data-update-published-at]");
  const messageElement = document.querySelector("[data-update-message]");
  const releaseLink = document.querySelector("[data-update-release]");

  function formatTimestamp(timestamp) {
    if (!timestamp) return "GitHub-Releases";
    const date = new Date(timestamp);
    if (Number.isNaN(date.getTime())) return "GitHub-Releases";
    return `Veröffentlicht: ${new Intl.DateTimeFormat("de-DE", { dateStyle: "medium" }).format(date)}`;
  }

  function renderStatus(status) {
    const isUpdateAvailable = Boolean(status.update_available);
    indicator.classList.toggle("update-available", isUpdateAvailable);
    indicator.textContent = isUpdateAvailable && status.latest_version ? `Update ${status.latest_version}` : currentVersion;
    indicator.title = status.message || `Installierte Version ${currentVersion}`;

    if (!panel) return;
    panel.dataset.updateState = status.state || "unavailable";
    if (currentVersionElement) currentVersionElement.textContent = status.current_version || currentVersion;
    if (latestVersionElement) latestVersionElement.textContent = status.latest_version || "Nicht verfügbar";
    if (publishedAtElement) publishedAtElement.textContent = formatTimestamp(status.published_at);
    if (messageElement) messageElement.textContent = status.message || "Der Update-Status konnte nicht ermittelt werden.";
    if (releaseLink) {
      const showLink = Boolean(status.release_url);
      releaseLink.hidden = !showLink;
      if (showLink) releaseLink.href = status.release_url;
    }
  }

  async function checkForUpdates(force = false) {
    if (checkButton) {
      checkButton.disabled = true;
      checkButton.textContent = "Prüfe …";
    }
    try {
      const suffix = force ? "?force=1" : "";
      const response = await fetch(`${endpoint}${suffix}`, {
        headers: { Accept: "application/json" },
        credentials: "same-origin",
      });
      if (!response.ok) throw new Error("Update status request failed");
      renderStatus(await response.json());
    } catch (_) {
      renderStatus({
        state: "unavailable",
        current_version: currentVersion,
        update_available: false,
        message: "Die Update-Prüfung ist derzeit nicht erreichbar. Die App läuft unverändert weiter.",
      });
    } finally {
      if (checkButton) {
        checkButton.disabled = false;
        checkButton.textContent = "Jetzt nach Updates suchen";
      }
    }
  }

  if (checkButton) checkButton.addEventListener("click", () => checkForUpdates(true));
  // This is reached after a successful login because the first authenticated
  // page uses base.html.  The server cache prevents repeated GitHub requests.
  checkForUpdates(false);
})();
