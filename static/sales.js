/* Verkauf screen behaviour.  Server-side validation remains authoritative; this
 * script only provides a fast, touch-friendly UI and a useful total preview. */
(function () {
  "use strict";
  const root = document.getElementById("sales-app");
  if (!root) return;

  const articles = JSON.parse(document.getElementById("articles-data").textContent);
  const $ = (id) => document.getElementById(id);
  const ui = {
    receipt: $("receipt-preview"),
    optionGroups: $("option-groups"),
    articleButtons: $("article-buttons"),
    selectedCard: $("selected-variant-card"),
    selectedLabel: $("selected-variant-label"),
    selectedStock: $("selected-variant-stock"),
    paid: $("is-paid"),
    received: $("is-received"),
    contactFields: $("contact-fields"),
    customerName: $("customer-name"),
    customerAddress: $("customer-address"),
    method: $("payment-method"),
    soldOn: $("sold-on"),
    eventName: $("event-name"),
    comment: $("comment"),
    quantity: $("quantity"),
    minus: $("quantity-minus"),
    plus: $("quantity-plus"),
    amountDue: $("amount-due"),
    amountGiven: $("amount-given"),
    donation: $("donation-preview"),
    confirm: $("confirm-sale"),
    error: $("sale-error"),
    dialog: $("success-dialog"),
    dialogReceipt: $("success-receipt"),
    closeDialog: $("close-success"),
  };
  let currentVariant = null;

  const selector = window.MerchTransaction.setupVariantSelector({
    articles,
    buttonContainer: ui.articleButtons,
    optionContainer: ui.optionGroups,
    onVariantChanged(variant) {
      currentVariant = variant;
      if (variant) {
        ui.selectedCard.hidden = false;
        ui.selectedLabel.textContent = variant.label;
        ui.selectedStock.textContent = `${variant.stock} Stück verfügbar`;
      } else {
        ui.selectedCard.hidden = true;
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

  function updateContactFields() {
    const needsContact = !ui.received.checked;
    ui.contactFields.hidden = !needsContact;
    ui.customerName.required = needsContact;
    ui.customerAddress.required = needsContact;
  }

  function updatePaidFields() {
    ui.amountGiven.disabled = !ui.paid.checked;
    if (!ui.paid.checked) ui.amountGiven.value = "";
  }

  function updateSummary() {
    const itemCount = quantity();
    const dueCents = currentVariant ? itemCount * Number(currentVariant.sale_price_cents) : 0;
    const givenCents = window.MerchTransaction.inputToCents(ui.amountGiven.value);
    const donationCents = ui.paid.checked ? Math.max(0, givenCents - dueCents) : 0;
    ui.amountDue.textContent = window.MerchTransaction.centsToEuro(dueCents);
    ui.donation.textContent = window.MerchTransaction.centsToEuro(donationCents);
    const hasEnoughStock = currentVariant && itemCount <= Number(currentVariant.stock);
    ui.confirm.disabled = !hasEnoughStock;
    if (currentVariant && !hasEnoughStock) {
      showError(`Nur noch ${currentVariant.stock} Stück auf Lager.`);
    } else if (!ui.error.dataset.serverError) {
      showError("");
    }
    return { itemCount, dueCents };
  }

  async function loadReceiptPreview() {
    try {
      const response = await fetch("/api/receipt-preview?kind=sale");
      const body = await response.json();
      ui.receipt.textContent = body.receipt_id || "—";
    } catch (_) {
      ui.receipt.textContent = "Nicht verfügbar";
    }
  }

  function applySaleStockUpdate(response) {
    // Update the in-memory selector data after a confirmed sale.  The page
    // deliberately stays open, so a reload merely to refresh the number on the
    // article button would be disruptive at a merch stand.  The server returns
    // the authoritative remaining stock for exactly this purpose.

    const soldVariantId = Number(response.variant_id);
    const remainingStock = Number(response.stock_after_sale);
    const article = articles.find((candidate) =>
      (candidate.variants || []).some((variant) => Number(variant.id) === soldVariantId)
    );
    if (!article || !Number.isFinite(remainingStock)) return;

    const variant = article.variants.find((candidate) => Number(candidate.id) === soldVariantId);
    variant.stock = remainingStock;
    article.total_stock = article.variants.reduce((total, candidate) => total + Number(candidate.stock || 0), 0);

    const articleButton = ui.articleButtons.querySelector(`[data-article-id="${article.id}"]`);
    const stockLabel = articleButton?.querySelector("small");
    if (stockLabel) stockLabel.textContent = `${article.total_stock} auf Lager`;
    if (currentVariant && Number(currentVariant.id) === soldVariantId) {
      ui.selectedStock.textContent = `${remainingStock} Stück verfügbar`;
    }
  }

  async function confirmSale() {
    const { itemCount, dueCents } = updateSummary();
    if (!currentVariant) return showError("Bitte Artikel und alle Optionen auswählen.");
    if (!ui.received.checked && (!ui.customerName.value.trim() || !ui.customerAddress.value.trim())) {
      return showError("Bei noch nicht erhaltenen Artikeln sind Name und Adresse erforderlich.");
    }
    const givenCents = window.MerchTransaction.inputToCents(ui.amountGiven.value);
    if (ui.paid.checked && ui.amountGiven.value.trim() && givenCents < dueCents) {
      return showError("Der gegebene Betrag ist kleiner als der Verkaufspreis. Bitte „Bezahlt“ entfernen oder Betrag korrigieren.");
    }
    showError("");
    ui.error.dataset.serverError = "";
    ui.confirm.disabled = true;
    ui.confirm.textContent = "Speichert …";
    try {
      const response = await fetch("/api/sales", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": window.MERCH_APP.csrfToken },
        body: JSON.stringify({
          receipt_id: ui.receipt.textContent,
          variant_id: currentVariant.id,
          quantity: itemCount,
          is_paid: ui.paid.checked,
          is_received: ui.received.checked,
          payment_method: ui.method.value,
          sold_on: ui.soldOn.value,
          amount_given: ui.amountGiven.value.trim(),
          customer_name: ui.customerName.value.trim(),
          customer_address: ui.customerAddress.value.trim(),
          event_name: ui.eventName.value.trim(),
          comment: ui.comment.value.trim(),
        }),
      });
      const body = await response.json();
      if (!response.ok || !body.ok) throw new Error(body.error || "Der Kauf konnte nicht gespeichert werden.");
      applySaleStockUpdate(body);
      ui.dialogReceipt.textContent = body.receipt_id;
      ui.dialog.showModal();
    } catch (error) {
      ui.error.dataset.serverError = "1";
      showError(error.message);
      updateSummary();
    } finally {
      ui.confirm.textContent = "Kauf bestätigen";
      if (!ui.dialog.open) updateSummary();
    }
  }

  function resetSaleForm() {
    selector.clear();
    currentVariant = null;
    ui.paid.checked = true;
    ui.received.checked = true;
    ui.method.value = "Bar";
    ui.quantity.value = 1;
    ui.amountGiven.value = "";
    ui.customerName.value = "";
    ui.customerAddress.value = "";
    // A merch stand normally records many sales for the same event.  Keep this
    // field across confirmation; the optional free-text comment still resets.
    ui.comment.value = "";
    ui.error.dataset.serverError = "";
    showError("");
    updateContactFields();
    updatePaidFields();
    updateSummary();
    loadReceiptPreview();
  }

  ui.minus.addEventListener("click", () => { ui.quantity.value = Math.max(1, quantity() - 1); updateSummary(); });
  ui.plus.addEventListener("click", () => { ui.quantity.value = quantity() + 1; updateSummary(); });
  ui.quantity.addEventListener("input", updateSummary);
  ui.amountGiven.addEventListener("input", updateSummary);
  ui.paid.addEventListener("change", () => { updatePaidFields(); updateSummary(); });
  ui.received.addEventListener("change", updateContactFields);
  ui.confirm.addEventListener("click", confirmSale);
  ui.closeDialog.addEventListener("click", () => ui.dialog.close());
  ui.dialog.addEventListener("close", resetSaleForm);

  updateContactFields();
  updatePaidFields();
  updateSummary();
  loadReceiptPreview();
})();
