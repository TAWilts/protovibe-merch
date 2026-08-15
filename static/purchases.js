/* Einkauf mit Warenkorb.
 *
 * Eine Position bleibt ein eigener Lager-/Bilanzsatz, mehrere Positionen
 * teilen sich aber die Beleg-ID des Einkaufs. Dadurch kann eine Rechnung
 * entweder präzise an einer Position oder am gesamten Warenkorb hängen.
 */
(function () {
  "use strict";
  const root = document.getElementById("purchase-app");
  if (!root) return;

  const articles = JSON.parse(document.getElementById("purchase-articles-data").textContent);
  const receipts = JSON.parse(document.getElementById("purchase-history-data").textContent);
  const purchases = receipts.flatMap((receipt) =>
    (receipt.items || []).map((item) => ({
      ...item,
      receipt_id: item.receipt_id || receipt.receipt_id,
      purchased_on: item.purchased_on || receipt.purchased_on,
    }))
  );
  const purchasesById = new Map(
    purchases.map((purchase) => [Number(purchase.purchase_id || purchase.id), purchase])
  );
  const receiptsById = new Map(receipts.map((receipt) => [String(receipt.receipt_id), receipt]));
  const $ = (id) => document.getElementById(id);
  const ui = {
    receipt: $("purchase-receipt-preview"),
    optionGroups: $("purchase-option-groups"),
    articleButtons: $("purchase-article-buttons"),
    selectedCard: $("purchase-selected-variant-card"),
    selectedLabel: $("purchase-selected-variant-label"),
    date: $("purchased-on"),
    unitCost: $("unit-cost"),
    defaultCostHint: $("default-cost-hint"),
    supplier: $("supplier"),
    invoice: $("invoice-reference"),
    comment: $("purchase-comment"),
    itemInvoiceInput: $("purchase-invoice-file"),
    itemInvoiceDropzone: $("purchase-invoice-dropzone"),
    itemInvoiceStatus: $("purchase-invoice-file-status"),
    quantity: $("purchase-quantity"),
    minus: $("purchase-quantity-minus"),
    plus: $("purchase-quantity-plus"),
    total: $("purchase-total"),
    addCart: $("add-purchase-cart-item"),
    cartItems: $("purchase-cart-items"),
    cartItemCount: $("purchase-cart-item-count"),
    cartTotal: $("purchase-cart-total"),
    cartInvoiceInput: $("purchase-cart-invoice-files"),
    cartInvoiceDropzone: $("purchase-cart-invoice-dropzone"),
    cartInvoiceStatus: $("purchase-cart-invoice-status"),
    cartInvoiceList: $("purchase-cart-invoice-list"),
    confirm: $("confirm-purchase"),
    error: $("purchase-error"),
    dialog: $("purchase-success-dialog"),
    dialogReceipt: $("purchase-success-receipt"),
    closeDialog: $("purchase-close-success"),
    editDialog: $("purchase-edit-dialog"),
    editReceipt: $("purchase-edit-receipt"),
    editDate: $("purchase-edit-date"),
    editUnitCost: $("edit-unit-cost"),
    editVariant: $("edit-purchase-variant"),
    editSupplier: $("edit-supplier"),
    editInvoice: $("edit-invoice-reference"),
    editInvoiceInput: $("edit-invoice-file"),
    editInvoiceDropzone: $("edit-invoice-dropzone"),
    editInvoiceStatus: $("edit-invoice-file-status"),
    editExistingInvoice: $("edit-existing-invoice"),
    editComment: $("edit-purchase-comment"),
    editQuantity: $("edit-purchase-quantity"),
    editError: $("purchase-edit-error"),
    closeEdit: $("close-purchase-edit-dialog"),
    saveEdit: $("save-purchase-edit"),
    itemDeleteDialog: $("purchase-item-delete-dialog"),
    itemDeleteLabel: $("purchase-item-delete-label"),
    itemDeleteError: $("purchase-item-delete-error"),
    closeItemDelete: $("close-purchase-item-delete-dialog"),
    confirmItemDelete: $("confirm-purchase-item-delete"),
    cartDeleteDialog: $("purchase-cart-delete-dialog"),
    cartDeleteReceipt: $("purchase-cart-delete-receipt"),
    cartDeleteError: $("purchase-cart-delete-error"),
    closeCartDelete: $("close-purchase-cart-delete-dialog"),
    confirmCartDelete: $("confirm-purchase-cart-delete"),
  };
  const CONFIRMATION_SECONDS = 3;
  const INVOICE_MAX_BYTES = 10 * 1024 * 1024;
  const INVOICE_EXTENSIONS = new Set(["pdf", "png", "jpg", "jpeg"]);
  const activeVariants = articles
    .flatMap((article) => article.variants || [])
    .sort((first, second) => String(first.label).localeCompare(String(second.label), "de"));
  const cartItems = [];
  let currentVariant = null;
  let currentItemInvoiceFile = null;
  let cartInvoiceFiles = [];
  let editInvoiceFile = null;
  let pendingEditPurchaseId = null;
  let pendingItemDeletePurchaseId = null;
  let pendingCartDeleteReceiptId = null;
  let editCountdownTimer = null;
  let itemDeleteCountdownTimer = null;
  let cartDeleteCountdownTimer = null;

  const selector = window.MerchTransaction.setupVariantSelector({
    articles,
    buttonContainer: ui.articleButtons,
    optionContainer: ui.optionGroups,
    onVariantChanged(variant) {
      currentVariant = variant;
      ui.selectedCard.hidden = !variant;
      if (variant) {
        ui.selectedLabel.textContent = variant.label;
        setDefaultCost(variant);
      } else {
        ui.unitCost.value = "";
        ui.unitCost.disabled = true;
        ui.defaultCostHint.textContent = "Nach Auswahl wird der Standard-Einkaufspreis übernommen."
      }
      updateSummary();
    },
  });

  function quantity() {
    const value = Math.max(1, Math.floor(Number(ui.quantity.value) || 1));
    ui.quantity.value = value;
    return value;
  }

  function cartTotalCents() {
    return cartItems.reduce((total, item) => total + item.quantity * item.unitCostCents, 0);
  }

  function showError(message) {
    ui.error.textContent = message;
    ui.error.hidden = !message;
  }

  function showDialogError(element, message) {
    element.textContent = message;
    element.hidden = !message;
  }

  function updateSummary() {
    const unitCost = window.MerchTransaction.moneyInputToCents(ui.unitCost.value);
    ui.total.textContent = window.MerchTransaction.centsToEuro((unitCost ?? 0) * quantity());
    ui.addCart.disabled = !currentVariant || unitCost === null;
    ui.cartTotal.textContent = window.MerchTransaction.centsToEuro(cartTotalCents());
    ui.confirm.disabled = cartItems.length === 0;
    return { quantity: quantity(), unitCost };
  }

  function readableFileSize(bytes) {
    return (Number(bytes || 0) / (1024 * 1024)).toLocaleString("de-DE", { maximumFractionDigits: 1 }) + " MB";
  }

  function invoiceFileError(file) {
    const extension = String(file?.name || "").split(".").pop().toLowerCase();
    if (!INVOICE_EXTENSIONS.has(extension)) return "Bitte nur eine PDF-, PNG- oder JPG-Datei auswählen.";
    if (file.size > INVOICE_MAX_BYTES) return "Die Rechnungsdatei darf höchstens 10 MB groß sein.";
    return "";
  }

  function configureSingleInvoiceDropzone(config) {
    const { input, dropzone, status, emptyText, onFileChanged } = config;
    function setFile(file) {
      const error = file ? invoiceFileError(file) : "";
      if (error) {
        input.value = "";
        onFileChanged(null);
        status.textContent = error;
        status.classList.add("file-status-error");
        return;
      }
      onFileChanged(file || null);
      status.classList.remove("file-status-error");
      status.textContent = file ? file.name + " · " + readableFileSize(file.size) : emptyText;
    }

    input.addEventListener("change", () => setFile(input.files?.[0] || null));
    dropzone.addEventListener("click", (event) => {
      if (event.target !== input) input.click();
    });
    dropzone.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      input.click();
    });
    ["dragenter", "dragover"].forEach((eventName) => {
      dropzone.addEventListener(eventName, (event) => {
        event.preventDefault();
        dropzone.classList.add("dragging");
      });
    });
    ["dragleave", "drop"].forEach((eventName) => {
      dropzone.addEventListener(eventName, (event) => {
        event.preventDefault();
        dropzone.classList.remove("dragging");
      });
    });
    dropzone.addEventListener("drop", (event) => setFile(event.dataTransfer?.files?.[0] || null));
    return {
      reset() {
        input.value = "";
        setFile(null);
      },
    };
  }

  const itemInvoiceDropzone = configureSingleInvoiceDropzone({
    input: ui.itemInvoiceInput,
    dropzone: ui.itemInvoiceDropzone,
    status: ui.itemInvoiceStatus,
    emptyText: "Keine Datei für diese Position ausgewählt.",
    onFileChanged(file) { currentItemInvoiceFile = file; },
  });
  const editInvoiceDropzone = configureSingleInvoiceDropzone({
    input: ui.editInvoiceInput,
    dropzone: ui.editInvoiceDropzone,
    status: ui.editInvoiceStatus,
    emptyText: "Keine neue Datei ausgewählt.",
    onFileChanged(file) { editInvoiceFile = file; },
  });

  function renderCartInvoiceFiles(message = "") {
    ui.cartInvoiceList.replaceChildren();
    cartInvoiceFiles.forEach((file, index) => {
      const row = document.createElement("div");
      row.className = "attachment-file-row";
      const label = document.createElement("span");
      label.textContent = file.name + " · " + readableFileSize(file.size);
      const remove = document.createElement("button");
      remove.type = "button";
      remove.className = "cart-remove-button";
      remove.dataset.cartInvoiceIndex = String(index);
      remove.setAttribute("aria-label", file.name + " entfernen");
      remove.textContent = "×";
      row.append(label, remove);
      ui.cartInvoiceList.append(row);
    });
    ui.cartInvoiceStatus.classList.toggle("file-status-error", Boolean(message));
    ui.cartInvoiceStatus.textContent = message || (
      cartInvoiceFiles.length
        ? cartInvoiceFiles.length + " Datei" + (cartInvoiceFiles.length === 1 ? "" : "en") + " am Warenkorb ausgewählt."
        : "Keine Rechnung am Warenkorb ausgewählt."
    );
  }

  function addCartInvoiceFiles(files) {
    const selectedFiles = Array.from(files || []);
    if (!selectedFiles.length) return;
    const error = selectedFiles.map(invoiceFileError).find(Boolean);
    if (error) {
      renderCartInvoiceFiles(error);
      return;
    }
    cartInvoiceFiles = [...cartInvoiceFiles, ...selectedFiles];
    ui.cartInvoiceInput.value = "";
    renderCartInvoiceFiles();
  }

  function configureCartInvoiceDropzone() {
    ui.cartInvoiceInput.addEventListener("change", () => addCartInvoiceFiles(ui.cartInvoiceInput.files));
    ui.cartInvoiceDropzone.addEventListener("click", (event) => {
      if (event.target !== ui.cartInvoiceInput) ui.cartInvoiceInput.click();
    });
    ui.cartInvoiceDropzone.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      ui.cartInvoiceInput.click();
    });
    ["dragenter", "dragover"].forEach((eventName) => {
      ui.cartInvoiceDropzone.addEventListener(eventName, (event) => {
        event.preventDefault();
        ui.cartInvoiceDropzone.classList.add("dragging");
      });
    });
    ["dragleave", "drop"].forEach((eventName) => {
      ui.cartInvoiceDropzone.addEventListener(eventName, (event) => {
        event.preventDefault();
        ui.cartInvoiceDropzone.classList.remove("dragging");
      });
    });
    ui.cartInvoiceDropzone.addEventListener("drop", (event) => addCartInvoiceFiles(event.dataTransfer?.files));
  }

  function setDefaultCost(variant) {
    ui.unitCost.disabled = false;
    ui.unitCost.value = window.MerchTransaction.centsToInput(variant.default_purchase_price_cents);
    ui.defaultCostHint.textContent = "Standard-Einkaufspreis wurde übernommen; bei Bedarf einfach überschreiben.";
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

  function renderCart() {
    ui.cartItems.replaceChildren();
    if (!cartItems.length) {
      const empty = document.createElement("p");
      empty.className = "muted";
      empty.textContent = "Noch keine Artikel hinzugefügt.";
      ui.cartItems.append(empty);
    } else {
      cartItems.forEach((item, index) => {
        const row = document.createElement("div");
        row.className = "cart-item";
        const copy = document.createElement("div");
        const title = document.createElement("strong");
        title.textContent = item.label;
        const details = document.createElement("small");
        const invoiceHint = item.invoiceFile ? " · Rechnung an Position" : "";
        const supplierHint = item.supplier ? " · " + item.supplier : "";
        details.textContent = item.quantity + " × " + window.MerchTransaction.centsToEuro(item.unitCostCents) + supplierHint + invoiceHint;
        copy.append(title, details);
        const total = document.createElement("span");
        total.className = "cart-item-total";
        total.textContent = window.MerchTransaction.centsToEuro(item.quantity * item.unitCostCents);
        const remove = document.createElement("button");
        remove.type = "button";
        remove.className = "cart-remove-button";
        remove.dataset.purchaseCartIndex = String(index);
        remove.setAttribute("aria-label", item.label + " aus dem Warenkorb entfernen");
        remove.textContent = "×";
        row.append(copy, total, remove);
        ui.cartItems.append(row);
      });
    }
    ui.cartItemCount.textContent = cartItems.length + " Artikel";
    updateSummary();
  }

  function addCurrentItem() {
    const summary = updateSummary();
    if (!currentVariant) return showError("Bitte Artikel und alle Optionen auswählen.");
    if (summary.unitCost === null) return showError("Bitte einen gültigen Preis pro Stück eintragen.");
    cartItems.push({
      variantId: Number(currentVariant.id),
      quantity: summary.quantity,
      unitCostCents: summary.unitCost,
      label: currentVariant.label,
      supplier: ui.supplier.value.trim(),
      invoiceReference: ui.invoice.value.trim(),
      comment: ui.comment.value.trim(),
      invoiceFile: currentItemInvoiceFile,
    });
    ui.quantity.value = 1;
    ui.invoice.value = "";
    ui.comment.value = "";
    itemInvoiceDropzone.reset();
    showError("");
    renderCart();
  }

  function createPurchaseFormData() {
    const formData = new FormData();
    formData.set("receipt_id", ui.receipt.textContent);
    formData.set("purchased_on", ui.date.value);
    formData.set("items", JSON.stringify(cartItems.map((item) => ({
      variant_id: item.variantId,
      quantity: item.quantity,
      unit_cost: window.MerchTransaction.centsToInput(item.unitCostCents),
      supplier: item.supplier,
      invoice_reference: item.invoiceReference,
      comment: item.comment,
    }))));
    cartItems.forEach((item, index) => {
      if (item.invoiceFile) formData.set("item_invoice_" + index, item.invoiceFile, item.invoiceFile.name);
    });
    cartInvoiceFiles.forEach((file) => formData.append("cart_invoice_files", file, file.name));
    return formData;
  }

  async function confirmPurchase() {
    if (!cartItems.length) return showError("Bitte mindestens einen Artikel zum Warenkorb hinzufügen.");
    showError("");
    ui.confirm.disabled = true;
    ui.confirm.textContent = "Speichert …";
    try {
      const response = await fetch("/api/purchases", {
        method: "POST",
        headers: { "X-CSRF-Token": window.MERCH_APP.csrfToken },
        body: createPurchaseFormData(),
      });
      const body = await response.json();
      if (!response.ok || !body.ok) throw new Error(body.error || "Der Einkauf konnte nicht gespeichert werden.");
      ui.dialogReceipt.textContent = body.receipt_id;
      ui.dialog.showModal();
    } catch (error) {
      showError(error.message);
      updateSummary();
    } finally {
      ui.confirm.textContent = "Warenkorb bestätigen";
      if (!ui.dialog.open) updateSummary();
    }
  }

  function stopTimer(timer) {
    if (timer !== null) window.clearInterval(timer);
  }

  function startCountdown(button, waitingPrefix, readyText, setTimer) {
    let remaining = CONFIRMATION_SECONDS;
    button.disabled = true;
    button.textContent = waitingPrefix + " (" + remaining + ")";
    const timer = window.setInterval(() => {
      remaining -= 1;
      if (remaining <= 0) {
        window.clearInterval(timer);
        setTimer(null);
        button.disabled = false;
        button.textContent = readyText;
      } else {
        button.textContent = waitingPrefix + " (" + remaining + ")";
      }
    }, 1000);
    setTimer(timer);
  }

  function populateEditVariantSelect(purchase) {
    ui.editVariant.replaceChildren();
    const currentVariantId = Number(purchase.variant_id);
    if (!activeVariants.some((variant) => Number(variant.id) === currentVariantId)) {
      const archived = document.createElement("option");
      archived.value = String(currentVariantId);
      archived.textContent = purchase.label + " (nicht mehr aktiv)";
      ui.editVariant.append(archived);
    }
    activeVariants.forEach((variant) => {
      const option = document.createElement("option");
      option.value = String(variant.id);
      option.textContent = variant.label;
      ui.editVariant.append(option);
    });
    ui.editVariant.value = String(currentVariantId);
  }

  function renderExistingInvoice(purchase) {
    ui.editExistingInvoice.replaceChildren();
    if (!purchase.invoice_file_path) {
      ui.editExistingInvoice.hidden = true;
      return;
    }
    const link = document.createElement("a");
    link.className = "invoice-link";
    link.href = "/api/purchases/" + (purchase.purchase_id || purchase.id) + "/invoice";
    link.target = "_blank";
    link.rel = "noopener";
    link.textContent = "Vorhandene Rechnung an dieser Position öffnen";
    ui.editExistingInvoice.append(link);
    ui.editExistingInvoice.hidden = false;
  }

  function openEditDialog(purchaseId) {
    const purchase = purchasesById.get(Number(purchaseId));
    if (!purchase) return;
    pendingEditPurchaseId = Number(purchase.purchase_id || purchase.id);
    ui.editReceipt.textContent = purchase.receipt_id;
    ui.editDate.textContent = purchase.purchased_on;
    ui.editUnitCost.value = window.MerchTransaction.centsToInput(purchase.unit_cost_cents);
    ui.editSupplier.value = purchase.supplier || "";
    ui.editInvoice.value = purchase.invoice_reference || "";
    ui.editComment.value = purchase.comment || "";
    ui.editQuantity.value = purchase.quantity;
    populateEditVariantSelect(purchase);
    editInvoiceDropzone.reset();
    renderExistingInvoice(purchase);
    showDialogError(ui.editError, "");
    ui.editDialog.showModal();
    startCountdown(ui.saveEdit, "Speichern", "Änderungen speichern", (timer) => { editCountdownTimer = timer; });
  }

  function editPurchaseFormData() {
    const formData = new FormData();
    formData.set("variant_id", ui.editVariant.value);
    formData.set("quantity", String(Math.max(1, Math.floor(Number(ui.editQuantity.value) || 1))));
    formData.set("unit_cost", ui.editUnitCost.value.trim());
    formData.set("supplier", ui.editSupplier.value.trim());
    formData.set("invoice_reference", ui.editInvoice.value.trim());
    formData.set("comment", ui.editComment.value.trim());
    if (editInvoiceFile) formData.set("invoice_file", editInvoiceFile, editInvoiceFile.name);
    return formData;
  }

  async function savePurchaseEdit() {
    if (!pendingEditPurchaseId || ui.saveEdit.disabled) return;
    if (!ui.editUnitCost.value.trim()) return showDialogError(ui.editError, "Bitte den Preis pro Stück eintragen.");
    showDialogError(ui.editError, "");
    ui.saveEdit.disabled = true;
    ui.saveEdit.textContent = "Speichert …";
    try {
      const response = await fetch("/api/purchases/" + pendingEditPurchaseId, {
        method: "PATCH",
        headers: { "X-CSRF-Token": window.MERCH_APP.csrfToken },
        body: editPurchaseFormData(),
      });
      const body = await response.json();
      if (!response.ok || !body.ok) throw new Error(body.error || "Die Position konnte nicht bearbeitet werden.");
      ui.editDialog.close();
      window.location.reload();
    } catch (error) {
      showDialogError(ui.editError, error.message);
      ui.saveEdit.disabled = false;
      ui.saveEdit.textContent = "Änderungen speichern";
    }
  }

  function openItemDeleteDialog(purchaseId) {
    const purchase = purchasesById.get(Number(purchaseId));
    if (!purchase) return;
    pendingItemDeletePurchaseId = Number(purchase.purchase_id || purchase.id);
    ui.itemDeleteLabel.textContent = purchase.label || purchase.article_name || purchase.receipt_id;
    showDialogError(ui.itemDeleteError, "");
    ui.itemDeleteDialog.showModal();
    startCountdown(ui.confirmItemDelete, "Entfernen", "Position entfernen", (timer) => { itemDeleteCountdownTimer = timer; });
  }

  async function deletePurchaseItem() {
    if (!pendingItemDeletePurchaseId || ui.confirmItemDelete.disabled) return;
    showDialogError(ui.itemDeleteError, "");
    ui.confirmItemDelete.disabled = true;
    ui.confirmItemDelete.textContent = "Entfernt …";
    try {
      const response = await fetch("/api/purchases/" + pendingItemDeletePurchaseId, {
        method: "DELETE",
        headers: { "X-CSRF-Token": window.MERCH_APP.csrfToken },
      });
      const body = await response.json();
      if (!response.ok || !body.ok) throw new Error(body.error || "Die Position konnte nicht entfernt werden.");
      ui.itemDeleteDialog.close();
      window.location.reload();
    } catch (error) {
      showDialogError(ui.itemDeleteError, error.message);
      ui.confirmItemDelete.disabled = false;
      ui.confirmItemDelete.textContent = "Position entfernen";
    }
  }

  function openCartDeleteDialog(receiptId) {
    if (!receiptsById.has(String(receiptId))) return;
    pendingCartDeleteReceiptId = String(receiptId);
    ui.cartDeleteReceipt.textContent = pendingCartDeleteReceiptId;
    showDialogError(ui.cartDeleteError, "");
    ui.cartDeleteDialog.showModal();
    startCountdown(ui.confirmCartDelete, "Löschen", "Warenkorb endgültig löschen", (timer) => { cartDeleteCountdownTimer = timer; });
  }

  async function deletePurchaseCart() {
    if (!pendingCartDeleteReceiptId || ui.confirmCartDelete.disabled) return;
    showDialogError(ui.cartDeleteError, "");
    ui.confirmCartDelete.disabled = true;
    ui.confirmCartDelete.textContent = "Löscht …";
    try {
      const response = await fetch(
        "/api/purchase-receipts/" + encodeURIComponent(pendingCartDeleteReceiptId),
        { method: "DELETE", headers: { "X-CSRF-Token": window.MERCH_APP.csrfToken } }
      );
      const body = await response.json();
      if (!response.ok || !body.ok) throw new Error(body.error || "Der Warenkorb konnte nicht gelöscht werden.");
      ui.cartDeleteDialog.close();
      window.location.reload();
    } catch (error) {
      showDialogError(ui.cartDeleteError, error.message);
      ui.confirmCartDelete.disabled = false;
      ui.confirmCartDelete.textContent = "Warenkorb endgültig löschen";
    }
  }

  function detailRowForReceipt(receiptId) {
    return Array.from(document.querySelectorAll("[data-purchase-cart-details]")).find(
      (row) => row.dataset.purchaseCartDetails === String(receiptId)
    );
  }

  function toggleReceiptDetails(receiptId, button) {
    const details = detailRowForReceipt(receiptId);
    if (!details) return;
    const willOpen = details.hidden;
    details.hidden = !willOpen;
    button.setAttribute("aria-expanded", String(willOpen));
    button.textContent = willOpen ? "▾" : "▸";
  }

  function receiptAttachmentStatus(receiptId) {
    return Array.from(document.querySelectorAll("[data-receipt-attachment-status]")).find(
      (element) => element.dataset.receiptId === String(receiptId)
    );
  }

  function showReceiptAttachmentStatus(receiptId, message, isError = false) {
    const status = receiptAttachmentStatus(receiptId);
    if (!status) return;
    status.textContent = message;
    status.classList.toggle("file-status-error", isError);
  }

  async function uploadReceiptAttachments(receiptId, files) {
    const selectedFiles = Array.from(files || []);
    if (!selectedFiles.length) return;
    const error = selectedFiles.map(invoiceFileError).find(Boolean);
    if (error) return showReceiptAttachmentStatus(receiptId, error, true);
    showReceiptAttachmentStatus(receiptId, "Datei wird angehängt …");
    const formData = new FormData();
    selectedFiles.forEach((file) => formData.append("cart_invoice_files", file, file.name));
    try {
      const response = await fetch(
        "/api/purchase-receipts/" + encodeURIComponent(receiptId) + "/attachments",
        { method: "POST", headers: { "X-CSRF-Token": window.MERCH_APP.csrfToken }, body: formData }
      );
      const body = await response.json();
      if (!response.ok || !body.ok) throw new Error(body.error || "Der Anhang konnte nicht gespeichert werden.");
      window.location.reload();
    } catch (uploadError) {
      showReceiptAttachmentStatus(receiptId, uploadError.message, true);
    }
  }

  document.addEventListener("click", (event) => {
    const itemRemove = event.target.closest("[data-purchase-cart-index]");
    if (itemRemove) {
      const index = Number(itemRemove.dataset.purchaseCartIndex);
      if (Number.isInteger(index) && index >= 0 && index < cartItems.length) {
        cartItems.splice(index, 1);
        showError("");
        renderCart();
      }
      return;
    }
    const invoiceRemove = event.target.closest("[data-cart-invoice-index]");
    if (invoiceRemove) {
      const index = Number(invoiceRemove.dataset.cartInvoiceIndex);
      if (Number.isInteger(index) && index >= 0 && index < cartInvoiceFiles.length) {
        cartInvoiceFiles.splice(index, 1);
        renderCartInvoiceFiles();
      }
      return;
    }
    const toggle = event.target.closest("[data-purchase-cart-toggle]");
    if (toggle) return toggleReceiptDetails(toggle.dataset.receiptId, toggle);
    const edit = event.target.closest("[data-edit-purchase]");
    if (edit) return openEditDialog(edit.dataset.purchaseId);
    const itemDelete = event.target.closest("[data-delete-purchase]");
    if (itemDelete) return openItemDeleteDialog(itemDelete.dataset.purchaseId);
    const cartDelete = event.target.closest("[data-delete-purchase-cart]");
    if (cartDelete) return openCartDeleteDialog(cartDelete.dataset.receiptId);
    const receiptDropzone = event.target.closest("[data-receipt-attachment-dropzone]");
    if (receiptDropzone && !event.target.matches("[data-receipt-attachment-input]")) {
      receiptDropzone.querySelector("[data-receipt-attachment-input]")?.click();
    }
  });

  document.addEventListener("keydown", (event) => {
    const receiptDropzone = event.target.closest?.("[data-receipt-attachment-dropzone]");
    if (!receiptDropzone || (event.key !== "Enter" && event.key !== " ")) return;
    event.preventDefault();
    receiptDropzone.querySelector("[data-receipt-attachment-input]")?.click();
  });

  document.addEventListener("change", (event) => {
    const input = event.target.closest?.("[data-receipt-attachment-input]");
    if (!input) return;
    uploadReceiptAttachments(input.dataset.receiptId, input.files);
    input.value = "";
  });

  ["dragenter", "dragover"].forEach((eventName) => {
    document.addEventListener(eventName, (event) => {
      const dropzone = event.target.closest?.("[data-receipt-attachment-dropzone]");
      if (!dropzone) return;
      event.preventDefault();
      dropzone.classList.add("dragging");
    });
  });
  ["dragleave", "drop"].forEach((eventName) => {
    document.addEventListener(eventName, (event) => {
      const dropzone = event.target.closest?.("[data-receipt-attachment-dropzone]");
      if (!dropzone) return;
      event.preventDefault();
      dropzone.classList.remove("dragging");
      if (eventName === "drop") uploadReceiptAttachments(dropzone.dataset.receiptId, event.dataTransfer?.files);
    });
  });

  ui.minus.addEventListener("click", () => { ui.quantity.value = Math.max(1, quantity() - 1); updateSummary(); });
  ui.plus.addEventListener("click", () => { ui.quantity.value = quantity() + 1; updateSummary(); });
  ui.quantity.addEventListener("input", updateSummary);
  ui.unitCost.addEventListener("input", updateSummary);
  ui.addCart.addEventListener("click", addCurrentItem);
  ui.confirm.addEventListener("click", confirmPurchase);
  ui.closeDialog.addEventListener("click", () => window.location.reload());
  ui.closeEdit.addEventListener("click", () => ui.editDialog.close());
  ui.closeItemDelete.addEventListener("click", () => ui.itemDeleteDialog.close());
  ui.closeCartDelete.addEventListener("click", () => ui.cartDeleteDialog.close());
  ui.saveEdit.addEventListener("click", savePurchaseEdit);
  ui.confirmItemDelete.addEventListener("click", deletePurchaseItem);
  ui.confirmCartDelete.addEventListener("click", deletePurchaseCart);
  ui.editDialog.addEventListener("close", () => {
    stopTimer(editCountdownTimer);
    editCountdownTimer = null;
    pendingEditPurchaseId = null;
    editInvoiceFile = null;
    showDialogError(ui.editError, "");
  });
  ui.itemDeleteDialog.addEventListener("close", () => {
    stopTimer(itemDeleteCountdownTimer);
    itemDeleteCountdownTimer = null;
    pendingItemDeletePurchaseId = null;
    showDialogError(ui.itemDeleteError, "");
  });
  ui.cartDeleteDialog.addEventListener("close", () => {
    stopTimer(cartDeleteCountdownTimer);
    cartDeleteCountdownTimer = null;
    pendingCartDeleteReceiptId = null;
    showDialogError(ui.cartDeleteError, "");
  });

  configureCartInvoiceDropzone();
  renderCartInvoiceFiles();
  renderCart();
  loadReceiptPreview();
})();
