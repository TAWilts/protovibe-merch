/* Einkauf screen behaviour.  Its option selector is the same generic component
 * used by Verkauf; only price/default and stock validation differ. */
(function () {
  "use strict";
  const root = document.getElementById("purchase-app");
  if (!root) return;

  const articles = JSON.parse(document.getElementById("purchase-articles-data").textContent);
  const $ = (id) => document.getElementById(id);
  const ui = {
    receipt: $("purchase-receipt-preview"),
    optionGroups: $("purchase-option-groups"),
    articleButtons: $("purchase-article-buttons"),
    selectedCard: $("purchase-selected-variant-card"),
    selectedLabel: $("purchase-selected-variant-label"),
    date: $("purchased-on"),
    unitCost: $("unit-cost"),
    lastCostHint: $("last-cost-hint"),
    supplier: $("supplier"),
    invoice: $("invoice-reference"),
    comment: $("purchase-comment"),
    quantity: $("purchase-quantity"),
    minus: $("purchase-quantity-minus"),
    plus: $("purchase-quantity-plus"),
    total: $("purchase-total"),
    confirm: $("confirm-purchase"),
    error: $("purchase-error"),
    dialog: $("purchase-success-dialog"),
    dialogReceipt: $("purchase-success-receipt"),
    closeDialog: $("purchase-close-success"),
  };
  let currentVariant = null;

  const selector = window.MerchTransaction.setupVariantSelector({
    articles,
    buttonContainer: ui.articleButtons,
    optionContainer: ui.optionGroups,
    onVariantChanged(variant) {
      currentVariant = variant;
      ui.selectedCard.hidden = !variant;
      if (variant) {
        ui.selectedLabel.textContent = variant.label;
        loadLastCost(variant.id);
      }
      updateSummary();
    },
  });

  function quantity() {
    const value = Math.max(1, Math.floor(Number(ui.quantity.value) || 1));
    ui.quantity.value = value;
    return value;
  }

  function showError(message) {
    ui.error.textContent = message;
    ui.error.hidden = !message;
  }

  function updateSummary() {
    const cost = window.MerchTransaction.inputToCents(ui.unitCost.value);
    ui.total.textContent = window.MerchTransaction.centsToEuro(cost * quantity());
    ui.confirm.disabled = !currentVariant || !ui.unitCost.value.trim();
    return { quantity: quantity(), unitCost: cost };
  }

  async function loadLastCost(variantId) {
    ui.lastCostHint.textContent = "Letzten Einkaufspreis wird geladen …";
    try {
      const response = await fetch(`/api/variants/${variantId}/last-purchase-price`);
      const body = await response.json();
      if (!response.ok || !body.ok || currentVariant?.id !== variantId) return;
      ui.unitCost.value = window.MerchTransaction.centsToInput(body.price_cents);
      ui.lastCostHint.textContent = "Letzter Einkaufspreis wurde übernommen; bei Bedarf einfach überschreiben.";
      updateSummary();
    } catch (_) {
      ui.lastCostHint.textContent = "Preis konnte nicht automatisch geladen werden.";
    }
  }

  async function loadReceiptPreview() {
    try {
      const response = await fetch("/api/receipt-preview?kind=purchase");
      const body = await response.json();
      ui.receipt.textContent = body.receipt_id || "—";
    } catch (_) {
      ui.receipt.textContent = "Nicht verfügbar";
    }
  }

  async function confirmPurchase() {
    const summary = updateSummary();
    if (!currentVariant) return showError("Bitte Artikel und alle Optionen auswählen.");
    if (!ui.unitCost.value.trim()) return showError("Bitte den Preis pro Stück eintragen.");
    showError("");
    ui.confirm.disabled = true;
    ui.confirm.textContent = "Speichert …";
    try {
      const response = await fetch("/api/purchases", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": window.MERCH_APP.csrfToken },
        body: JSON.stringify({
          receipt_id: ui.receipt.textContent,
          variant_id: currentVariant.id,
          quantity: summary.quantity,
          unit_cost: ui.unitCost.value.trim(),
          purchased_on: ui.date.value,
          supplier: ui.supplier.value.trim(),
          invoice_reference: ui.invoice.value.trim(),
          comment: ui.comment.value.trim(),
        }),
      });
      const body = await response.json();
      if (!response.ok || !body.ok) throw new Error(body.error || "Der Einkauf konnte nicht gespeichert werden.");
      ui.dialogReceipt.textContent = body.receipt_id;
      ui.dialog.showModal();
    } catch (error) {
      showError(error.message);
      updateSummary();
    } finally {
      ui.confirm.textContent = "Einkauf bestätigen";
      if (!ui.dialog.open) updateSummary();
    }
  }

  ui.minus.addEventListener("click", () => { ui.quantity.value = Math.max(1, quantity() - 1); updateSummary(); });
  ui.plus.addEventListener("click", () => { ui.quantity.value = quantity() + 1; updateSummary(); });
  ui.quantity.addEventListener("input", updateSummary);
  ui.unitCost.addEventListener("input", updateSummary);
  ui.confirm.addEventListener("click", confirmPurchase);
  // Refreshing after the confirmation gives the "Letzte Einkäufe" table its
  // new row and a fresh balance without risking duplicate form submission.
  ui.closeDialog.addEventListener("click", () => window.location.reload());

  updateSummary();
  loadReceiptPreview();
})();
