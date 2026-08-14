/* Storno workflow for historic sale entries.
 *
 * A cancellation is deliberately a two-step action.  The customer must open
 * the dialog and then wait three full seconds before the final API action is
 * enabled, so a stray tap cannot remove a sale from balances accidentally.
 */
(function () {
  "use strict";
  const dialog = document.getElementById("cancel-sale-dialog");
  if (!dialog) return;

  const receipt = document.getElementById("cancel-sale-receipt");
  const error = document.getElementById("cancel-sale-error");
  const closeButton = document.getElementById("close-cancel-dialog");
  const confirmButton = document.getElementById("confirm-cancel-sale");
  const CONFIRMATION_SECONDS = 3;
  let pendingSaleId = null;
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

  function openDialog(button) {
    pendingSaleId = Number(button.dataset.saleId);
    receipt.textContent = button.dataset.receiptId || "";
    showError("");
    dialog.showModal();
    startCountdown();
  }

  document.querySelectorAll("[data-cancel-sale]").forEach((button) => {
    button.addEventListener("click", () => openDialog(button));
  });

  closeButton.addEventListener("click", () => dialog.close());
  dialog.addEventListener("close", () => {
    stopCountdown();
    pendingSaleId = null;
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
        body: JSON.stringify({}),
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
