/* Dynamic article option and variant editor.
 *
 * The option grid remains the source for the future Cartesian combinations.
 * The variant table is deliberately rebuilt from that same in-browser state
 * whenever an option changes, so it never lags one save behind the editor.
 */
(function () {
  "use strict";

  const dataNode = document.getElementById("article-editor-data");
  const form = document.getElementById("article-form");
  if (!dataNode || !form) return;

  const source = JSON.parse(dataNode.textContent);
  const grid = document.getElementById("option-grid");
  const thead = grid.querySelector("thead");
  const tbody = grid.querySelector("tbody");
  const hiddenInput = document.getElementById("options-json");
  const addColumnButton = document.getElementById("add-option-column");
  const addRowButton = document.getElementById("add-option-row");
  const variantBody = document.getElementById("variant-price-body");
  const newVariantHint = document.getElementById("new-variant-hint");
  const minimumStockForAll = document.getElementById("minimum-stock-for-all");
  const applyMinimumStockButton = document.getElementById("apply-minimum-stock-to-all");
  const applyMinimumStockValue = document.getElementById("apply-minimum-stock-to-all-value");
  const defaultSalePrice = form.elements.namedItem("default_sale_price");
  const defaultPurchasePrice = form.elements.namedItem("default_purchase_price");
  if (!variantBody || !minimumStockForAll || !applyMinimumStockButton || !applyMinimumStockValue) return;

  let draftValueSequence = 0;

  function draftIdentity() {
    draftValueSequence += 1;
    return `draft-${draftValueSequence}`;
  }

  function savedValueId(value) {
    const candidate = Number(value?.id);
    return Number.isInteger(candidate) && candidate > 0 ? candidate : null;
  }

  function newDraftValue(value = "") {
    return { id: null, value, clientId: draftIdentity() };
  }

  let groups = (source.option_groups || []).map((group) => ({
    id: group.id,
    name: group.name,
    values: (group.values || []).map((value) => {
      const id = savedValueId(value);
      return {
        id,
        value: value.value,
        clientId: id === null ? draftIdentity() : `saved-${id}`,
      };
    }),
  }));

  function canonical(input) {
    return input
      .map((group) => ({
        id: group.id || null,
        name: String(group.name || "").trim(),
        values: (group.values || [])
          .map((value) => ({ id: value.id || null, value: String(value.value || "").trim() }))
          .filter((value) => value.value),
      }))
      .filter((group) => group.name);
  }

  const originalSignature = JSON.stringify(canonical(groups));

  function maximumRows() {
    return Math.max(1, ...groups.map((group) => group.values.length));
  }

  function button(label, className, title) {
    const node = document.createElement("button");
    node.type = "button";
    node.className = className;
    node.textContent = label;
    node.title = title || label;
    return node;
  }

  function ensureValue(groupIndex, valueIndex) {
    const values = groups[groupIndex].values;
    while (values.length <= valueIndex) values.push(newDraftValue());
    return values[valueIndex];
  }

  function savedCombinationKey(ids) {
    return (ids || [])
      .map((id) => Number(id))
      .filter((id) => Number.isInteger(id))
      .sort((left, right) => left - right)
      .map((id) => `saved:${id}`)
      .join("|");
  }

  function previewCombinationKey(values) {
    return values
      .map((value) => {
        const id = savedValueId(value);
        return { id, key: id === null ? `draft:${value.clientId}` : `saved:${id}` };
      })
      .sort((left, right) => {
        if (left.id !== null && right.id !== null) return left.id - right.id;
        return left.key.localeCompare(right.key);
      })
      .map((value) => value.key)
      .join("|");
  }

  function centsToInput(cents) {
    return ((Number(cents) || 0) / 100).toFixed(2).replace(".", ",");
  }

  function minimumStockInputValue(value) {
    return value === null || value === undefined ? "" : String(value);
  }

  function parseMinimumStock(value) {
    const raw = String(value ?? "").trim();
    return /^\d+$/.test(raw) ? Number(raw) : null;
  }

  const variantStateByKey = new Map();
  (source.variants || []).forEach((variant) => {
    variantStateByKey.set(savedCombinationKey(variant.option_value_ids), {
      id: Number(variant.id),
      stock: Number(variant.stock || 0),
      salePrice: centsToInput(variant.sale_price_cents),
      purchasePrice: centsToInput(variant.default_purchase_price_cents),
      minimumStock: minimumStockInputValue(variant.minimum_stock),
      noReorder: Boolean(variant.no_reorder),
    });
  });

  function activeGroupsForPreview() {
    return groups
      .map((group) => ({
        name: String(group.name || "").trim(),
        values: (group.values || [])
          .map((value) => ({ ...value, value: String(value.value || "").trim() }))
          .filter((value) => value.value),
      }))
      .filter((group) => group.name);
  }

  function previewCombinations() {
    const activeGroups = activeGroupsForPreview();
    if (!activeGroups.length) return [{ key: "", label: "Standardvariante" }];
    if (activeGroups.some((group) => !group.values.length)) return [];

    let combinations = [{ values: [], labels: [] }];
    activeGroups.forEach((group) => {
      combinations = combinations.flatMap((combination) =>
        group.values.map((value) => ({
          values: [...combination.values, value],
          labels: [...combination.labels, `${group.name}: ${value.value}`],
        }))
      );
    });
    return combinations.map((combination) => ({
      key: previewCombinationKey(combination.values),
      label: combination.labels.join(" · "),
    }));
  }

  function defaultMoneyInput(input) {
    return input && typeof input.value === "string" && input.value.trim() ? input.value : "0,00";
  }

  function stateForCombination(combination) {
    const known = variantStateByKey.get(combination.key);
    if (known) return known;
    const fresh = {
      id: null,
      stock: 0,
      salePrice: defaultMoneyInput(defaultSalePrice),
      purchasePrice: defaultMoneyInput(defaultPurchasePrice),
      minimumStock: applyMinimumStockValue.value,
      noReorder: false,
    };
    variantStateByKey.set(combination.key, fresh);
    return fresh;
  }

  function syncVariantStateFromTable() {
    variantBody.querySelectorAll("tr[data-variant-key]").forEach((row) => {
      const state = variantStateByKey.get(row.dataset.variantKey);
      if (!state) return;
      const sale = row.querySelector('[data-variant-field="sale-price"]');
      const purchase = row.querySelector('[data-variant-field="purchase-price"]');
      const minimum = row.querySelector('[data-variant-field="minimum-stock"]');
      const noReorder = row.querySelector('[data-variant-field="no-reorder"]');
      if (sale) state.salePrice = sale.value;
      if (purchase) state.purchasePrice = purchase.value;
      if (minimum) state.minimumStock = minimum.value;
      if (noReorder) state.noReorder = noReorder.checked;
    });
  }

  function minimumStockWarning(state) {
    const threshold = parseMinimumStock(state.minimumStock);
    return threshold !== null && Number(state.stock) <= threshold;
  }

  function renderWarningCell(cell, state) {
    cell.replaceChildren();
    if (!minimumStockWarning(state)) {
      cell.textContent = "—";
      return;
    }
    const badge = document.createElement("span");
    badge.className = "status warning";
    badge.textContent = "Grenzwert erreicht";
    cell.append(badge);
  }

  function variantInput({ className, type = "text", value = "", name = "", field, disabled = false, checked = false }) {
    const input = document.createElement("input");
    input.type = type;
    input.className = className;
    input.dataset.variantField = field;
    if (name) input.name = name;
    if (type === "checkbox") {
      input.checked = checked;
    } else {
      input.value = value;
    }
    if (type === "number") {
      input.min = "0";
      input.step = "1";
      input.inputMode = "numeric";
      input.placeholder = "—";
    }
    if (field === "sale-price" || field === "purchase-price") input.inputMode = "decimal";
    input.disabled = disabled;
    if (disabled) input.title = "Neue Varianten können nach dem ersten Speichern individuell angepasst werden.";
    return input;
  }

  function cellWith(node) {
    const cell = document.createElement("td");
    cell.append(node);
    return cell;
  }

  function renderVariantTable({ syncFromInputs = true } = {}) {
    // The normal live-preview path must retain edits that are still only in
    // input fields. A bulk minimum-stock action has already updated the state
    // itself, however, so reading the old DOM values once more would undo it.
    if (syncFromInputs) syncVariantStateFromTable();
    variantBody.replaceChildren();
    const combinations = previewCombinations();
    const hasNewVariants = combinations.some((combination) => !stateForCombination(combination).id);
    newVariantHint.hidden = !hasNewVariants;

    if (!combinations.length) {
      const row = document.createElement("tr");
      const cell = document.createElement("td");
      cell.colSpan = 7;
      cell.className = "empty-cell";
      cell.textContent = "Ergänze mindestens einen Wert je Option. Dann zeigt die Tabelle sofort die entstehenden Varianten.";
      row.append(cell);
      variantBody.append(row);
      return;
    }

    combinations.forEach((combination) => {
      const state = stateForCombination(combination);
      const savedVariant = Boolean(state.id);
      const row = document.createElement("tr");
      row.dataset.variantKey = combination.key;
      row.classList.toggle("low-stock-row", minimumStockWarning(state));

      const labelCell = document.createElement("td");
      const label = document.createElement("strong");
      label.textContent = combination.label;
      labelCell.append(label);
      if (!savedVariant) {
        const note = document.createElement("small");
        note.className = "table-subline";
        note.textContent = "Neu – wird beim Speichern angelegt";
        labelCell.append(note);
      }

      const stockCell = document.createElement("td");
      stockCell.className = "stock-cell";
      if (Number(state.stock) === 0) stockCell.classList.add("out-of-stock");
      stockCell.textContent = String(state.stock);

      const saleInput = variantInput({
        className: "inline-money",
        value: state.salePrice,
        name: savedVariant ? `sale_price_${state.id}` : "",
        field: "sale-price",
        disabled: !savedVariant,
      });
      const purchaseInput = variantInput({
        className: "inline-money",
        value: state.purchasePrice,
        name: savedVariant ? `purchase_price_${state.id}` : "",
        field: "purchase-price",
        disabled: !savedVariant,
      });
      const minimumInput = variantInput({
        className: "inline-number",
        type: "number",
        value: state.minimumStock,
        name: savedVariant ? `minimum_stock_${state.id}` : "",
        field: "minimum-stock",
        disabled: !savedVariant,
      });
      const warningCell = document.createElement("td");
      warningCell.dataset.minimumWarning = "";
      renderWarningCell(warningCell, state);
      const noReorderInput = variantInput({
        className: "",
        type: "checkbox",
        name: savedVariant ? `no_reorder_${state.id}` : "",
        field: "no-reorder",
        disabled: !savedVariant,
        checked: state.noReorder,
      });

      row.append(
        labelCell,
        stockCell,
        cellWith(saleInput),
        cellWith(purchaseInput),
        cellWith(minimumInput),
        warningCell,
        cellWith(noReorderInput)
      );
      variantBody.append(row);
    });
  }

  function render() {
    thead.replaceChildren();
    tbody.replaceChildren();
    const headingRow = document.createElement("tr");
    const indexHead = document.createElement("th");
    indexHead.textContent = "Werte";
    headingRow.append(indexHead);

    groups.forEach((group, groupIndex) => {
      const header = document.createElement("th");
      const wrapper = document.createElement("div");
      wrapper.className = "option-heading";
      const input = document.createElement("input");
      input.value = group.name;
      input.placeholder = "Option, z. B. Farbe";
      input.addEventListener("input", () => {
        groups[groupIndex].name = input.value;
        renderVariantTable();
      });
      const remove = button("×", "icon-button", "Option löschen");
      remove.addEventListener("click", () => {
        groups.splice(groupIndex, 1);
        render();
      });
      wrapper.append(input, remove);
      header.append(wrapper);
      headingRow.append(header);
    });
    thead.append(headingRow);

    const rows = maximumRows();
    for (let rowIndex = 0; rowIndex < rows; rowIndex += 1) {
      const row = document.createElement("tr");
      const number = document.createElement("td");
      number.textContent = String(rowIndex + 1);
      row.append(number);
      groups.forEach((group, groupIndex) => {
        const cell = document.createElement("td");
        const wrapper = document.createElement("div");
        wrapper.className = "value-cell";
        const value = ensureValue(groupIndex, rowIndex);
        const input = document.createElement("input");
        input.value = value.value;
        input.placeholder = "Wert";
        input.addEventListener("input", () => {
          ensureValue(groupIndex, rowIndex).value = input.value;
          renderVariantTable();
        });
        const remove = button("×", "icon-button", "Wert löschen");
        remove.addEventListener("click", () => {
          if (rowIndex < groups[groupIndex].values.length) groups[groupIndex].values.splice(rowIndex, 1);
          render();
        });
        wrapper.append(input, remove);
        cell.append(wrapper);
        row.append(cell);
      });
      tbody.append(row);
    }
    renderVariantTable();
  }

  addColumnButton.addEventListener("click", () => {
    const rows = maximumRows();
    groups.push({ id: null, name: "Neue Option", values: Array.from({ length: rows }, () => newDraftValue()) });
    render();
  });
  addRowButton.addEventListener("click", () => {
    groups.forEach((group) => group.values.push(newDraftValue()));
    render();
  });

  variantBody.addEventListener("input", (event) => {
    const input = event.target;
    if (!(input instanceof HTMLInputElement)) return;
    const row = input.closest("tr[data-variant-key]");
    if (!row) return;
    const state = variantStateByKey.get(row.dataset.variantKey);
    if (!state) return;
    if (input.dataset.variantField === "sale-price") state.salePrice = input.value;
    if (input.dataset.variantField === "purchase-price") state.purchasePrice = input.value;
    if (input.dataset.variantField === "minimum-stock") {
      state.minimumStock = input.value;
      row.classList.toggle("low-stock-row", minimumStockWarning(state));
      renderWarningCell(row.querySelector("[data-minimum-warning]"), state);
    }
  });
  variantBody.addEventListener("change", (event) => {
    const input = event.target;
    if (!(input instanceof HTMLInputElement) || input.dataset.variantField !== "no-reorder") return;
    const row = input.closest("tr[data-variant-key]");
    const state = row && variantStateByKey.get(row.dataset.variantKey);
    if (state) state.noReorder = input.checked;
  });

  applyMinimumStockButton.addEventListener("click", () => {
    const threshold = parseMinimumStock(minimumStockForAll.value);
    if (threshold === null) {
      minimumStockForAll.setCustomValidity("Bitte einen ganzen Mindestbestand ab 0 eingeben.");
      minimumStockForAll.reportValidity();
      return;
    }
    minimumStockForAll.setCustomValidity("");
    syncVariantStateFromTable();
    applyMinimumStockValue.value = String(threshold);
    variantStateByKey.forEach((state) => {
      state.minimumStock = String(threshold);
    });
    renderVariantTable({ syncFromInputs: false });
  });
  minimumStockForAll.addEventListener("input", () => {
    // Editing the value does not silently overwrite individual entries.  The
    // administrator must explicitly press the button again.
    applyMinimumStockValue.value = "";
    minimumStockForAll.setCustomValidity("");
  });

  form.addEventListener("submit", (event) => {
    syncVariantStateFromTable();
    const payload = canonical(groups);
    const changed = JSON.stringify(payload) !== originalSignature;
    if (changed) {
      const accepted = window.confirm(
        "Du veränderst bestehende Artikeloptionen. Umbenannte Optionen werden rückwirkend in alten Einkäufen und Verkäufen angezeigt; gelöschte Optionen sind nur noch historisch sichtbar. Änderungen speichern?"
      );
      if (!accepted) {
        event.preventDefault();
        return;
      }
    }
    hiddenInput.value = JSON.stringify(payload);
  });

  render();
})();
