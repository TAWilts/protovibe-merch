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
  const photoStrings = window.MERCH_APP?.photoStrings || {
    caption: "Produktfoto dieser Variante",
    fallback: "Foto einer ähnlichen Variante: {label}",
  };
  const ui = {
    receipt: $("receipt-preview"),
    optionGroups: $("option-groups"),
    articleButtons: $("article-buttons"),
    selectedCard: $("selected-variant-card"),
    selectedLabel: $("selected-variant-label"),
    selectedStock: $("selected-variant-stock"),
    photoPreview: $("variant-photo-preview"),
    photoCaption: $("variant-photo-caption"),
    photoList: $("variant-photo-preview-list"),
    paid: $("is-paid"),
    received: $("is-received"),
    soldBy: $("sold-by"),
    contactDetails: $("sale-contact-details"),
    contactFields: $("contact-fields"),
    customerName: $("customer-name"),
    customerAddress: $("customer-address"),
    method: $("payment-method"),
    soldOn: $("sold-on"),
    saleEvent: $("sale-event"),
    saleEventDialog: $("sale-event-dialog"),
    saleEventForm: $("sale-event-form"),
    saleEventName: $("sale-event-name"),
    saleEventError: $("sale-event-error"),
    cancelSaleEvent: $("cancel-sale-event"),
    saveSaleEvent: $("save-sale-event"),
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
  const mobileCollapsedDetails = Array.from(root.querySelectorAll("[data-mobile-collapsed]"));
  let selectedSaleEventValue = ui.saleEvent?.value || "";
  let saleEventBusy = false;
  let saleEventRefreshPromise = null;

  function initializeResponsiveDetails() {
    const isMobile = window.matchMedia("(max-width: 760px)").matches;
    mobileCollapsedDetails.forEach((details) => {
      details.open = !isMobile;
    });
  }

  function renderVariantPhotos(variant) {
    if (!ui.photoPreview || !ui.photoCaption || !ui.photoList) return;
    const photos = Array.isArray(variant?.display_photos) ? variant.display_photos : [];
    ui.photoPreview.hidden = !photos.length;
    ui.photoList.replaceChildren();
    if (!photos.length) return;
    photos.forEach((photo) => {
      const image = document.createElement("img");
      image.src = photo.url || `/api/variantenfotos/${photo.id}`;
      image.alt = `${variant.label}: ${photo.original_filename || photoStrings.caption}`;
      image.loading = "eager";
      ui.photoList.append(image);
    });
    ui.photoCaption.textContent = variant.photo_is_fallback
      ? photoStrings.fallback.replace("{label}", variant.photo_source_label || "")
      : photoStrings.caption;
  }

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
      renderVariantPhotos(variant);
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

  function showSaleEventError(message) {
    if (!ui.saleEventError) return;
    ui.saleEventError.textContent = message;
    ui.saleEventError.hidden = !message;
  }

  function selectedSaleEventPayload() {
    const confirmedValue = selectedSaleEventValue;
    if (!ui.saleEvent || !confirmedValue || confirmedValue === "__new__") {
      return { event_id: null, event_name: "" };
    }
    const eventId = Number(confirmedValue);
    const option = Array.from(ui.saleEvent.options).find((candidate) => candidate.value === confirmedValue);
    if (!Number.isInteger(eventId) || eventId <= 0 || !option) {
      return { event_id: null, event_name: "" };
    }
    return { event_id: eventId, event_name: option.textContent.trim() };
  }

  function renderSaleEvents(body, selectedEventId = body.current_event_id) {
    if (!ui.saleEvent) return;
    const desiredValue = selectedEventId === null || selectedEventId === undefined
      ? ""
      : String(selectedEventId);
    const fragment = document.createDocumentFragment();
    const none = document.createElement("option");
    none.value = "";
    none.textContent = "Keine Veranstaltung";
    fragment.append(none);
    (body.events || []).forEach((event) => {
      const option = document.createElement("option");
      option.value = String(event.id);
      option.textContent = event.name;
      fragment.append(option);
    });
    const create = document.createElement("option");
    create.value = "__new__";
    create.textContent = "Neue Veranstaltung …";
    fragment.append(create);
    ui.saleEvent.replaceChildren(fragment);
    ui.saleEvent.value = desiredValue;
    if (ui.saleEvent.value !== desiredValue) ui.saleEvent.value = "";
    selectedSaleEventValue = ui.saleEvent.value;
    ui.saleEvent.dataset.currentEventId = body.current_event_id ?? "";
  }

  function setSaleEventBusy(busy) {
    saleEventBusy = busy;
    if (ui.saleEvent) ui.saleEvent.disabled = busy;
    ui.confirm.disabled = busy || cartItems.length === 0;
  }

  async function saleEventApi(url, options = {}) {
    const response = await fetch(url, {
      ...options,
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": window.MERCH_APP.csrfToken,
        ...(options.headers || {}),
      },
    });
    const body = await response.json();
    if (!response.ok || !body.ok) throw new Error(body.error || "Die Veranstaltung konnte nicht gespeichert werden.");
    return body;
  }

  async function refreshSaleEvents({ showFailure = false } = {}) {
    if (!ui.saleEvent || saleEventBusy) return null;
    if (saleEventRefreshPromise) return saleEventRefreshPromise;
    const preserveEmptyChoice = selectedSaleEventValue === "";
    saleEventRefreshPromise = saleEventApi("/api/sale-events", { cache: "no-store" })
      .then((body) => {
        // A deliberately blank event remains local to the current sale. Every
        // actual event tracks the shared default, including on pages that were
        // already open when another user changed it.
        renderSaleEvents(body, preserveEmptyChoice ? null : body.current_event_id);
        return body;
      })
      .catch((error) => {
        if (showFailure) showError(error instanceof Error ? error.message : "Die Veranstaltungen konnten nicht aktualisiert werden.");
        return null;
      })
      .finally(() => {
        saleEventRefreshPromise = null;
      });
    return saleEventRefreshPromise;
  }

  function openSaleEventDialog() {
    if (!ui.saleEventDialog || !ui.saleEventName) return;
    ui.saleEvent.value = selectedSaleEventValue;
    showSaleEventError("");
    ui.saleEventName.value = "";
    ui.saleEventDialog.showModal();
    ui.saleEventName.focus();
  }

  function closeSaleEventDialog() {
    if (!ui.saleEventDialog) return;
    ui.saleEventDialog.close();
  }

  async function changeSaleEvent() {
    if (!ui.saleEvent) return;
    const nextValue = ui.saleEvent.value;
    if (nextValue === "__new__") {
      openSaleEventDialog();
      return;
    }
    if (!nextValue) {
      selectedSaleEventValue = "";
      return;
    }
    const eventId = Number(nextValue);
    if (!Number.isInteger(eventId) || eventId <= 0) {
      ui.saleEvent.value = selectedSaleEventValue;
      return;
    }
    setSaleEventBusy(true);
    try {
      const body = await saleEventApi(`/api/sale-events/${eventId}/select`, { method: "POST" });
      renderSaleEvents(body, body.current_event_id);
    } catch (error) {
      ui.saleEvent.value = selectedSaleEventValue;
      showError(error.message);
    } finally {
      setSaleEventBusy(false);
    }
  }

  async function saveSaleEvent(event) {
    event.preventDefault();
    const name = ui.saleEventName?.value.trim() || "";
    if (!name) {
      showSaleEventError("Bitte einen Namen für die Veranstaltung eingeben.");
      ui.saleEventName?.focus();
      return;
    }
    showSaleEventError("");
    ui.saveSaleEvent.disabled = true;
    try {
      const body = await saleEventApi("/api/sale-events", {
        method: "POST",
        body: JSON.stringify({ name }),
      });
      renderSaleEvents(body, body.event?.id ?? body.current_event_id);
      closeSaleEventDialog();
    } catch (error) {
      showSaleEventError(error.message);
    } finally {
      ui.saveSaleEvent.disabled = false;
    }
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
    if (needsContact && ui.contactDetails) ui.contactDetails.open = true;
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
    ui.confirm.disabled = saleEventBusy || cartItems.length === 0;
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
    if (saleEventBusy) return;
    const offline = window.MerchOffline;
    if (!offline?.isOffline()) {
      // Fetch immediately before saving, so a tab that stayed open while a
      // colleague selected another event cannot silently book to an old one.
      ui.confirm.disabled = true;
      await refreshSaleEvents();
    }
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
    const selectedEvent = selectedSaleEventPayload();
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
      event_id: selectedEvent.event_id,
      // Keep the former free-text field in the request.  Queued clients from
      // before the dropdown and a restored browser outbox remain compatible.
      event_name: selectedEvent.event_name,
      sold_by: ui.soldBy.value.trim(),
      comment: ui.comment.value.trim(),
    };
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
    // A merch stand normally records many sales for the same globally chosen
    // event and the same seller. Keep both choices across confirmation; only
    // the optional free-text comment resets.
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
  ui.saleEvent?.addEventListener("change", changeSaleEvent);
  ui.saleEventForm?.addEventListener("submit", saveSaleEvent);
  ui.cancelSaleEvent?.addEventListener("click", closeSaleEventDialog);
  ui.saleEventDialog?.addEventListener("cancel", () => {
    if (ui.saleEvent) ui.saleEvent.value = selectedSaleEventValue;
  });
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
  const refreshVisibleSaleEvents = () => {
    if (
      document.visibilityState === "visible"
      && document.activeElement !== ui.saleEvent
      && !ui.saleEventDialog?.open
      && !window.MerchOffline?.isOffline()
    ) refreshSaleEvents();
  };
  window.addEventListener("focus", refreshVisibleSaleEvents);
  document.addEventListener("visibilitychange", refreshVisibleSaleEvents);
  window.setInterval(refreshVisibleSaleEvents, 15000);
  window.addEventListener("merch-offline-sale-synced", (event) => {
    if (event.detail?.ok) applySaleStockUpdate(event.detail);
  });

  initializeResponsiveDetails();
  updateContactFields();
  updatePaidFields();
  renderCart();
  loadReceiptPreview();
})();
