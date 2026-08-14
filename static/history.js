/* Expandable receipt history and deliberate cancellation workflow.
 *
 * Both a complete shopping basket and an individual ledger item use the same
 * three-second safety delay.  The scope travels explicitly to the API, so a
 * cancellation can never accidentally affect the next history row.
 */
(function () {
  "use strict";
  const dialog = document.getElementById("cancel-sale-dialog");
  if (!dialog) return;

  const title = document.getElementById("cancel-sale-title");
  const description = document.getElementById("cancel-sale-description");
  const receipt = document.getElementById("cancel-sale-receipt");
  const error = document.getElementById("cancel-sale-error");
  const closeButton = document.getElementById("close-cancel-dialog");
  const confirmButton = document.getElementById("confirm-cancel-sale");
  const CONFIRMATION_SECONDS = 3;
  let pendingSaleId = null;
  let pendingScope = "item";
  let countdownTimer = null;

  function showError(message) {
    error.textContent = message;
    error.hidden = !message;
  }

  function stopCountdown() {
    if (countdownTimer !== null) window.clearInterval(countdownTimer);
    countdownTimer = null;
  }

  function startCountdown() {
    stopCountdown();
    let remainingSeconds = CONFIRMATION_SECONDS;
    confirmButton.disabled = true;
    confirmButton.textContent = `Stornieren (${remainingSeconds})`;
    countdownTimer = window.setInterval(() => {
      remainingSeconds -= 1;
      if (remainingSeconds <= 0) {
        stopCountdown();
        confirmButton.disabled = false;
        confirmButton.textContent = "Stornierung bestätigen";
      } else {
        confirmButton.textContent = `Stornieren (${remainingSeconds})`;
      }
    }, 1000);
  }

  function setDescription(scope) {
    const isReceipt = scope === "receipt";
    title.textContent = isReceipt ? "Warenkorb stornieren?" : "Artikel stornieren?";
    receipt.textContent = receipt.dataset.value || "";
    description.replaceChildren(
      document.createTextNode(
        isReceipt
          ? "Der gesamte Warenkorb "
          : "Der ausgewählte Artikel aus dem Warenkorb "
      ),
      receipt,
      document.createTextNode(
        " bleibt in der Historie sichtbar, zählt danach aber nicht mehr für Bestand, Bilanzen oder offene Vorgänge."
      )
    );
  }

  function openDialog(button) {
    pendingSaleId = Number(button.dataset.saleId);
    pendingScope = button.dataset.cancelScope === "receipt" ? "receipt" : "item";
    receipt.dataset.value = button.dataset.receiptId || "";
    setDescription(pendingScope);
    showError("");
    dialog.showModal();
    startCountdown();
  }

  document.addEventListener("click", (event) => {
    const toggle = event.target.closest("[data-cart-toggle]");
    if (toggle) {
      const target = document.getElementById(toggle.dataset.target);
      if (!target) return;
      const isExpanded = toggle.getAttribute("aria-expanded") === "true";
      toggle.setAttribute("aria-expanded", String(!isExpanded));
      toggle.querySelector("span").textContent = isExpanded ? "▸" : "▾";
      target.hidden = isExpanded;
      return;
    }
    const cancelButton = event.target.closest("[data-cancel-sale]");
    if (cancelButton) openDialog(cancelButton);
  });

  closeButton.addEventListener("click", () => dialog.close());
  dialog.addEventListener("close", () => {
    stopCountdown();
    pendingSaleId = null;
    pendingScope = "item";
    receipt.dataset.value = "";
    showError("");
  });

  confirmButton.addEventListener("click", async () => {
    if (!pendingSaleId || confirmButton.disabled) return;
    confirmButton.disabled = true;
    showError("");
    try {
      const response = await fetch(`/api/sales/${pendingSaleId}/cancel`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": window.MERCH_APP.csrfToken },
        body: JSON.stringify({ scope: pendingScope }),
      });
      const body = await response.json();
      if (!response.ok || !body.ok) throw new Error(body.error || "Verkauf konnte nicht storniert werden.");
      dialog.close();
      // Reload is safe here: this page has no editable status controls whose
      // state a browser could restore onto a different ledger row.
      window.location.reload();
    } catch (requestError) {
      showError(requestError.message);
      confirmButton.disabled = false;
      confirmButton.textContent = "Stornierung bestätigen";
    }
  });
})();
