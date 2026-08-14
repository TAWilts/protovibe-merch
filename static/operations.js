/* Status controls for the shipment and payment work queues.
 *
 * Rows move in-place after a successful change.  A browser reload may restore
 * select values by their old DOM position (especially in Firefox), which can
 * make values appear to shift to the next row after a sale changes queue.
 * Moving the confirmed row locally avoids that form-state restoration entirely.
 */
(function () {
  "use strict";
  const feedback = document.getElementById("operations-feedback");

  function showError(message) {
    feedback.textContent = message;
    feedback.hidden = !message;
  }

  function removeEmptyState(body) {
    body.querySelector("[data-empty-state]")?.remove();
  }

  function addEmptyStateIfNeeded(body) {
    if (body.querySelector("[data-sale-row]")) return;
    const emptyRow = document.createElement("tr");
    emptyRow.dataset.emptyState = "1";
    const cell = document.createElement("td");
    cell.colSpan = Number(body.dataset.emptyColspan) || 1;
    cell.className = "empty-cell";
    cell.textContent = body.dataset.emptyMessage || "Keine Einträge.";
    emptyRow.append(cell);
    body.append(emptyRow);
  }

  function insertInLedgerOrder(row, targetBody) {
    // The server sorts every queue by date and then descending sale ID.  Use
    // the same ordering when a row crosses a queue boundary without reload.
    const candidateRows = [...targetBody.querySelectorAll("[data-sale-row]")];
    const rowDate = row.dataset.soldOn || "";
    const rowId = Number(row.dataset.saleId);
    const firstLaterRow = candidateRows.find((candidate) => {
      const candidateDate = candidate.dataset.soldOn || "";
      if (candidateDate !== rowDate) return candidateDate < rowDate;
      return Number(candidate.dataset.saleId) < rowId;
    });
    if (firstLaterRow) targetBody.insertBefore(row, firstLaterRow);
    else targetBody.append(row);
  }

  function moveRowToQueue(select, targetBodyId) {
    const row = select.closest("[data-sale-row]");
    const sourceBody = row?.parentElement;
    const targetBody = document.getElementById(targetBodyId);
    if (!row || !sourceBody || !targetBody) return;
    removeEmptyState(targetBody);
    insertInLedgerOrder(row, targetBody);
    addEmptyStateIfNeeded(sourceBody);
  }

  async function saveStatus(select, url, payload, onSaved) {
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
      select.disabled = false;
      select.dataset.previousValue = select.value;
      onSaved();
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
        { delivery_status: select.value },
        () => moveRowToQueue(
          select,
          select.value === "received" ? "delivered-goods-body" : "current-shipments-body"
        )
      );
    });
  });

  document.querySelectorAll("[data-payment-status]").forEach((select) => {
    select.dataset.previousValue = select.value;
    select.addEventListener("change", () => {
      saveStatus(
        select,
        `/api/sales/${select.dataset.saleId}/payment-status`,
        { is_paid: select.value === "true" },
        () => moveRowToQueue(
          select,
          select.value === "true" ? "paid-follow-up-sales-body" : "unpaid-sales-body"
        )
      );
    });
  });
})();
