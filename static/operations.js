/* Status controls for the shipment and payment work queues.
 *
 * The tables intentionally reload after a successful change instead of trying
 * to duplicate the server-side grouping rules in browser code.  That makes a
 * switch to "Erhalten" reliably move the row from current shipments to the
 * completed-goods table, and a switch to "Bezahlt" reliably removes it from
 * outstanding payments.
 */
(function () {
  "use strict";
  const feedback = document.getElementById("operations-feedback");

  function showError(message) {
    feedback.textContent = message;
    feedback.hidden = !message;
  }

  async function saveStatus(select, url, payload) {
    const previousValue = select.dataset.previousValue || select.value;
    select.disabled = true;
    showError("");
    try {
      const response = await fetch(url, {
        method: "PATCH",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": window.MERCH_APP.csrfToken },
        body: JSON.stringify(payload),
      });
      const body = await response.json();
      if (!response.ok || !body.ok) throw new Error(body.error || "Status konnte nicht gespeichert werden.");
      // A full refresh uses the authoritative grouping from the backend.
      window.location.reload();
    } catch (error) {
      select.value = previousValue;
      select.disabled = false;
      showError(error.message);
    }
  }

  document.querySelectorAll("[data-delivery-status]").forEach((select) => {
    select.dataset.previousValue = select.value;
    select.addEventListener("change", () => {
      saveStatus(
        select,
        `/api/sales/${select.dataset.saleId}/delivery-status`,
        { delivery_status: select.value }
      );
    });
  });

  document.querySelectorAll("[data-payment-status]").forEach((select) => {
    select.dataset.previousValue = select.value;
    select.addEventListener("change", () => {
      saveStatus(
        select,
        `/api/sales/${select.dataset.saleId}/payment-status`,
        { is_paid: select.value === "true" }
      );
    });
  });
})();
