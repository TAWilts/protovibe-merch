/* Verkauf mit Warenkorb.
 *
 * Der Browser hält nur den noch unbestätigten Warenkorb.  Beim Speichern
 * validiert der Server jede Position erneut und schreibt sie unter derselben
 * Beleg-ID als einzelne, für Bestand und Versand nachvollziehbare Ledgerzeile.
 */
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
    soldBy: $("sold-by"),
    contactFields: $("contact-fields"),
    customerName: $("customer-name"),
    customerAddress: $("customer-address"),
    method: $("payment-method"),
    soldOn: $("sold-on"),
    eventName: $("event-name"),
    comment: $("comment"),
    unitPrice: $("unit-price"),
    quantity: $("quantity"),
    minus: $("quantity-minus"),
    plus: $("quantity-plus"),
    amountDue: $("amount-due"),
    amountGiven: $("amount-given"),
    donation: $("donation-preview"),
    addCart: $("add-cart-item"),
    cartItems: $("cart-items"),
    cartItemCount: $("cart-item-count"),
    stockWarning: $("stock-warning"),
    confirm: $("confirm-sale"),
    error: $("sale-error"),
    dialog: $("success-dialog"),
    dialogReceipt: $("success-receipt"),
    dialogMessage: $("success-message"),
    closeDialog: $("close-success"),
  };
  let currentVariant = null;
  const cartItems = [];

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
        ui.unitPrice.disabled = false;
        ui.unitPrice.value = window.MerchTransaction.centsToInput(variant.sale_price_cents);
      } else {
        ui.selectedCard.hidden = true;
        ui.unitPrice.value = "";
        ui.unitPrice.disabled = true;
      }
      updateSummary();
    },
  });

  function variantForId(variantId) {
    for (const article of articles) {
      const variant = (article.variants || []).find((candidate) => Number(candidate.id) === Number(variantId));
      if (variant) return variant;
    }
    return null;
  }

  function quantity() {
    const value = Math.max(1, Math.floor(Number(ui.quantity.value) || 1));
    ui.quantity.value = value;
    return value;
  }

  function showError(message) {
    ui.error.textContent = message;
    ui.error.hidden = !message;
  }

  function showStockWarning(message) {
    ui.stockWarning.textContent = message;
    ui.stockWarning.hidden = !message;
  }

  function updateContactFields() {
    const needsContact = !ui.received.checked || !ui.paid.checked;
    ui.contactFields.hidden = !needsContact;
    ui.customerName.required = needsContact;
    ui.customerAddress.required = needsContact;
  }

  function updatePaidFields() {
    ui.amountGiven.disabled = !ui.paid.checked;
    if (!ui.paid.checked) ui.amountGiven.value = "";
  }

  function cartTotalCents() {
    return cartItems.reduce((total, item) => total + item.quantity * item.unitPriceCents, 0);
  }

  function quantitiesForStockWarning() {
    const quantities = new Map();
    // Before the first add, still show the familiar warning for the currently
    // selected direct hand-over.  Afterwards the basket is decisive.
    const candidates = cartItems.length
      ? cartItems
      : currentVariant
        ? [{ variantId: currentVariant.id, quantity: quantity() }]
        : [];
    candidates.forEach((item) => {
      const identifier = Number(item.variantId);
      quantities.set(identifier, (quantities.get(identifier) || 0) + Number(item.quantity));
    });
    return quantities;
  }

  function updateStockWarning() {
    if (!ui.received.checked) {
      showStockWarning("");
      return;
    }
    const shortages = [];
    const minimumStockWarnings = [];
    quantitiesForStockWarning().forEach((wanted, variantId) => {
      const variant = variantForId(variantId);
      if (!variant) return;
      const currentStock = Number(variant.stock);
      const stockAfterSale = currentStock - wanted;
      if (wanted > currentStock) {
        shortages.push(`${variant.label} (Bestand: ${variant.stock})`);
      }
      const configuredMinimum = variant.minimum_stock;
      const minimumStock = Number(configuredMinimum);
      if (
        configuredMinimum !== null && configuredMinimum !== undefined && configuredMinimum !== "" &&
        Number.isFinite(minimumStock) && stockAfterSale <= minimumStock
      ) {
        minimumStockWarnings.push(
          `${variant.label} (nach Verkauf: ${stockAfterSale}, Mindestbestand: ${minimumStock})`
        );
      }
    });
    const messages = [];
    if (shortages.length) {
      messages.push(
        `Warnung: Laut Bestand sind folgende Artikel nicht ausreichend verfügbar: ${shortages.join(", ")}. Der Verkauf wird trotzdem gespeichert.`
      );
    }
    if (minimumStockWarnings.length) {
      messages.push(`Mindestbestandswarnung: ${minimumStockWarnings.join(", ")}.`);
    }
    showStockWarning(messages.join(" "));
  }

  function updateSummary() {
    const dueCents = cartTotalCents();
    const unitPriceCents = window.MerchTransaction.moneyInputToCents(ui.unitPrice.value);
    const givenCents = window.MerchTransaction.inputToCents(ui.amountGiven.value);
    const donationCents = ui.paid.checked ? Math.max(0, givenCents - dueCents) : 0;
    ui.amountDue.textContent = window.MerchTransaction.centsToEuro(dueCents);
    ui.donation.textContent = window.MerchTransaction.centsToEuro(donationCents);
    ui.addCart.disabled = !currentVariant || unitPriceCents === null;
    ui.confirm.disabled = cartItems.length === 0;
    updateStockWarning();
    if (!ui.error.dataset.serverError) showError("");
    return { dueCents, unitPriceCents };
  }

  function renderCart() {
    ui.cartItems.replaceChildren();
    if (!cartItems.length) {
      const empty = document.createElement("p");
      empty.className = "muted";
      empty.textContent = "Noch keine Artikel hinzugefügt.";
      ui.cartItems.append(empty);
    } else {
      cartItems.forEach((item, index) => {
        const variant = variantForId(item.variantId);
        const row = document.createElement("div");
        row.className = "cart-item";
        const copy = document.createElement("div");
        const label = document.createElement("strong");
        label.textContent = variant?.label || item.label;
        const details = document.createElement("small");
        details.textContent = `${item.quantity} × ${window.MerchTransaction.centsToEuro(item.unitPriceCents)}`;
        copy.append(label, details);
        const total = document.createElement("span");
        total.className = "cart-item-total";
        total.textContent = window.MerchTransaction.centsToEuro(item.quantity * item.unitPriceCents);
        const remove = document.createElement("button");
        remove.type = "button";
        remove.className = "cart-remove-button";
        remove.dataset.cartIndex = String(index);
        remove.setAttribute("aria-label", `${variant?.label || item.label} aus dem Warenkorb entfernen`);
        remove.textContent = "×";
        row.append(copy, total, remove);
        ui.cartItems.append(row);
      });
    }
    ui.cartItemCount.textContent = `${cartItems.length} Artikel`;
    updateSummary();
  }

  function addCurrentItem() {
    const { unitPriceCents } = updateSummary();
    if (!currentVariant) {
      showError("Bitte Artikel und alle Optionen auswählen.");
      return;
    }
    if (unitPriceCents === null) {
      showError("Bitte einen gültigen Preis pro Stück eintragen.");
      return;
    }
    const currentQuantity = quantity();
    // The same variant may legitimately appear twice with different prices,
    // for example when only one of several shirts receives a discount.
    const existing = cartItems.find(
      (item) => Number(item.variantId) === Number(currentVariant.id) && item.unitPriceCents === unitPriceCents
    );
    if (existing) {
      existing.quantity += currentQuantity;
    } else {
      cartItems.push({
        variantId: Number(currentVariant.id),
        quantity: currentQuantity,
        unitPriceCents,
        label: currentVariant.label,
      });
    }
    // Keep article and option selection exactly as it is.  Only the quantity
    // returns to one, so the next add is an intentional extra copy.
    ui.quantity.value = 1;
    ui.error.dataset.serverError = "";
    showError("");
    renderCart();
  }

  async function loadReceiptPreview() {
    if (window.MerchOffline?.isOffline()) {
      ui.receipt.textContent = "Offline – wird beim Synchronisieren vergeben";
      return;
    }
    try {
      const response = await fetch("/api/receipt-preview?kind=sale");
      const body = await response.json();
      ui.receipt.textContent = body.receipt_id || "—";
    } catch (_) {
      ui.receipt.textContent = "Nicht verfügbar";
    }
  }

  function applySaleStockUpdate(response) {
    // The page deliberately stays open after a sale.  Update all affected
    // variants locally so the article list immediately reflects the cart.
    const updates = Array.isArray(response.items) ? response.items : [response];
    const affectedArticles = new Set();
    updates.forEach((update) => {
      const variantId = Number(update.variant_id);
      const remainingStock = Number(update.stock_after_sale);
      const article = articles.find((candidate) =>
        (candidate.variants || []).some((variant) => Number(variant.id) === variantId)
      );
      if (!article || !Number.isFinite(remainingStock)) return;
      const variant = article.variants.find((candidate) => Number(candidate.id) === variantId);
      variant.stock = remainingStock;
      affectedArticles.add(article);
    });
    affectedArticles.forEach((article) => {
      article.total_stock = article.variants.reduce((total, variant) => total + Number(variant.stock || 0), 0);
      const articleButton = ui.articleButtons.querySelector(`[data-article-id="${article.id}"]`);
      const stockLabel = articleButton?.querySelector("small");
      if (stockLabel) stockLabel.textContent = `${article.total_stock} auf Lager`;
    });
    if (currentVariant) {
      const freshVariant = variantForId(currentVariant.id);
      if (freshVariant) ui.selectedStock.textContent = `${freshVariant.stock} Stück verfügbar`;
    }
  }

  function localOfflineStockUpdate(payload) {
    // The server will return authoritative stock values after synchronization.
    // Until then, decrement this device's cached values so a second offline
    // sale still gets an honest local stock/minimum-stock warning.
    const remainingByVariant = new Map();
    const updates = (payload.items || []).map((item) => {
      const variant = variantForId(item.variant_id);
      const previous = remainingByVariant.has(Number(item.variant_id))
        ? remainingByVariant.get(Number(item.variant_id))
        : Number(variant?.stock || 0);
      const remaining = previous - Number(item.quantity || 0);
      remainingByVariant.set(Number(item.variant_id), remaining);
      return { variant_id: Number(item.variant_id), stock_after_sale: remaining };
    });
    applySaleStockUpdate({ items: updates });
  }

  function showConfirmedSale(receiptId, message) {
    ui.dialogReceipt.textContent = receiptId;
    ui.dialogMessage.replaceChildren(
      document.createTextNode(message),
      document.createTextNode(" "),
      ui.dialogReceipt,
      document.createTextNode(".")
    );
    ui.dialog.showModal();
  }

  async function confirmSale() {
    const { dueCents } = updateSummary();
    if (!cartItems.length) return showError("Bitte mindestens einen Artikel zum Warenkorb hinzufügen.");
    if ((!ui.received.checked || !ui.paid.checked) && (!ui.customerName.value.trim() || !ui.customerAddress.value.trim())) {
      return showError("Bei nicht bezahlten oder noch nicht erhaltenen Artikeln sind Name und Adresse erforderlich.");
    }
    const givenCents = window.MerchTransaction.inputToCents(ui.amountGiven.value);
    if (ui.paid.checked && ui.amountGiven.value.trim() && givenCents < dueCents) {
      return showError("Der gegebene Betrag ist kleiner als der Verkaufspreis. Bitte „Bezahlt“ entfernen oder Betrag korrigieren.");
    }
    showError("");
    ui.error.dataset.serverError = "";
    ui.confirm.disabled = true;
    ui.confirm.textContent = "Speichert …";
    const salePayload = {
      receipt_id: ui.receipt.textContent,
      items: cartItems.map((item) => ({
        variant_id: item.variantId,
        quantity: item.quantity,
        unit_price: window.MerchTransaction.centsToInput(item.unitPriceCents),
      })),
      is_paid: ui.paid.checked,
      is_received: ui.received.checked,
      payment_method: ui.method.value,
      sold_on: ui.soldOn.value,
      amount_given: ui.amountGiven.value.trim(),
      customer_name: ui.customerName.value.trim(),
      customer_address: ui.customerAddress.value.trim(),
      event_name: ui.eventName.value.trim(),
      sold_by: ui.soldBy.value.trim(),
      comment: ui.comment.value.trim(),
    };
    const offline = window.MerchOffline;
    let requestPayload = salePayload;
    let responseReceived = false;
    try {
      if (offline) requestPayload = await offline.prepareSale(salePayload);
      const response = await fetch("/api/sales", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": window.MERCH_APP.csrfToken },
        body: JSON.stringify(requestPayload),
      });
      responseReceived = true;
      const body = await response.json();
      if (!response.ok || !body.ok) throw new Error(body.error || "Der Kauf konnte nicht gespeichert werden.");
      if (offline) await offline.acknowledgeSale(requestPayload.client_event_id);
      applySaleStockUpdate(body);
      showConfirmedSale(body.receipt_id, body.duplicate ? "Offline-Verkauf bereits synchronisiert als" : "Der Verkauf wurde mit der Beleg-ID");
    } catch (error) {
      // Only a missing/ambiguous response is queued. A clear 4xx/5xx response
      // remains visible as an error instead of silently turning a rejected
      // sale into an offline booking.
      const ambiguousResponse = !responseReceived || error instanceof SyntaxError;
      if (offline && ambiguousResponse && requestPayload.client_event_id) {
        try {
          await offline.queueSale(requestPayload);
          localOfflineStockUpdate(requestPayload);
          showConfirmedSale(
            `Offline-${requestPayload.client_event_id.slice(0, 8)}`,
            "Der Verkauf wurde lokal vorgemerkt und erhält beim Synchronisieren seine Beleg-ID"
          );
          return;
        } catch (queueError) {
          error = queueError;
        }
      }
      ui.error.dataset.serverError = "1";
      showError(error.message);
      updateSummary();
    } finally {
      ui.confirm.textContent = "Kauf bestätigen";
      if (!ui.dialog.open) updateSummary();
    }
  }

  function resetSaleForm() {
    cartItems.length = 0;
    selector.clear();
    currentVariant = null;
    ui.paid.checked = true;
    ui.received.checked = true;
    ui.method.value = "Bar";
    ui.quantity.value = 1;
    ui.amountGiven.value = "";
    ui.customerName.value = "";
    ui.customerAddress.value = "";
    // A merch stand normally records many sales for the same event and with
    // the same seller.  Keep both fields across confirmation; the optional
    // free-text comment still resets.
    ui.comment.value = "";
    ui.error.dataset.serverError = "";
    showError("");
    updateContactFields();
    updatePaidFields();
    renderCart();
    loadReceiptPreview();
  }

  ui.minus.addEventListener("click", () => { ui.quantity.value = Math.max(1, quantity() - 1); updateSummary(); });
  ui.plus.addEventListener("click", () => { ui.quantity.value = quantity() + 1; updateSummary(); });
  ui.quantity.addEventListener("input", updateSummary);
  ui.unitPrice.addEventListener("input", updateSummary);
  ui.amountGiven.addEventListener("input", updateSummary);
  ui.paid.addEventListener("change", () => { updatePaidFields(); updateContactFields(); updateSummary(); });
  ui.received.addEventListener("change", () => { updateContactFields(); updateSummary(); });
  ui.addCart.addEventListener("click", addCurrentItem);
  ui.cartItems.addEventListener("click", (event) => {
    const button = event.target.closest("[data-cart-index]");
    if (!button) return;
    const index = Number(button.dataset.cartIndex);
    if (!Number.isInteger(index) || index < 0 || index >= cartItems.length) return;
    cartItems.splice(index, 1);
    ui.error.dataset.serverError = "";
    renderCart();
  });
  ui.confirm.addEventListener("click", confirmSale);
  ui.closeDialog.addEventListener("click", () => ui.dialog.close());
  ui.dialog.addEventListener("close", resetSaleForm);
  window.addEventListener("merch-offline-sale-synced", (event) => {
    if (event.detail?.ok) applySaleStockUpdate(event.detail);
  });

  updateContactFields();
  updatePaidFields();
  renderCart();
  loadReceiptPreview();
})();
