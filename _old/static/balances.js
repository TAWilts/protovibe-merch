/* Balance visualisations and locally rendered balance tables.
 *
 * The server remains authoritative for all ledger totals. Sorting, filtering,
 * grouping and CSV creation are deliberately browser-local because they only
 * rearrange data that is already visible to the signed-in user.
 */
(function () {
  "use strict";
  const container = document.getElementById("income-chart");
  const source = document.getElementById("balance-daily-income-data");
  const empty = document.getElementById("income-chart-empty");
  if (!container || !source || !empty) return;

  let rows = [];
  try {
    rows = JSON.parse(source.textContent || "[]");
  } catch (_) {
    rows = [];
  }
  rows = rows.filter((row) => Number.isFinite(Number(row.income_cents)));
  if (!rows.length) {
    empty.hidden = false;
    return;
  }

  const namespace = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(namespace, "svg");
  const width = 760;
  const height = 270;
  const padding = { top: 20, right: 22, bottom: 44, left: 80 };
  const graphWidth = width - padding.left - padding.right;
  const graphHeight = height - padding.top - padding.bottom;
  const values = rows.map((row) => Math.max(0, Number(row.income_cents)));
  const maxValue = Math.max(...values, 1);
  const format = (cents) => window.MERCH_APP.moneyFormatter.format(cents / 100);
  const xFor = (index) => (
    padding.left + (rows.length === 1 ? graphWidth / 2 : (index / (rows.length - 1)) * graphWidth)
  );
  const yFor = (value) => padding.top + graphHeight - (value / maxValue) * graphHeight;
  const add = (tag, attributes = {}, text = null) => {
    const element = document.createElementNS(namespace, tag);
    Object.entries(attributes).forEach(([name, value]) => element.setAttribute(name, String(value)));
    if (text !== null) element.textContent = text;
    svg.append(element);
    return element;
  };

  svg.setAttribute("viewBox", "0 0 " + width + " " + height);
  svg.setAttribute("preserveAspectRatio", "none");
  svg.setAttribute("aria-hidden", "true");
  svg.classList.add("income-chart-svg");

  [0, 0.5, 1].forEach((fraction) => {
    const y = yFor(maxValue * fraction);
    add("line", { x1: padding.left, x2: width - padding.right, y1: y, y2: y, class: "income-grid-line" });
    add("text", { x: padding.left - 10, y: y + 4, "text-anchor": "end", class: "income-axis-label" }, format(maxValue * fraction));
  });

  const points = values.map((value, index) => String(xFor(index)) + "," + String(yFor(value))).join(" ");
  const areaPoints = String(padding.left) + "," + String(padding.top + graphHeight) + " " + points + " " + String(width - padding.right) + "," + String(padding.top + graphHeight);
  add("polygon", { points: areaPoints, class: "income-area" });
  add("polyline", { points, class: "income-line" });
  values.forEach((value, index) => {
    const dot = add("circle", { cx: xFor(index), cy: yFor(value), r: 4, class: "income-dot" });
    const title = document.createElementNS(namespace, "title");
    title.textContent = String(rows[index].date) + ": " + format(value);
    dot.append(title);
  });

  const first = rows[0];
  const last = rows[rows.length - 1];
  add("text", { x: xFor(0), y: height - 14, "text-anchor": "start", class: "income-axis-label income-date-label" }, first.date);
  if (rows.length > 1) {
    add("text", { x: xFor(rows.length - 1), y: height - 14, "text-anchor": "end", class: "income-axis-label income-date-label" }, last.date);
  }
  container.replaceChildren(svg);
})();

(function () {
  "use strict";

  const source = document.getElementById("balance-table-data");
  const filterInput = document.getElementById("balance-filter");
  const onlyPurchasedInput = document.getElementById("balance-only-purchased");
  const groupByArticleInput = document.getElementById("balance-group-by-article");
  const resultCount = document.getElementById("balance-filter-result-count");
  if (!source || !filterInput || !onlyPurchasedInput || !groupByArticleInput) return;

  let tableData = {};
  try {
    tableData = JSON.parse(source.textContent || "{}");
  } catch (_) {
    tableData = {};
  }
  const views = ["reorder", "obsolete"];
  const rowsByView = Object.fromEntries(
    views.map((view) => [view, Array.isArray(tableData[view]) ? tableData[view] : []])
  );
  const headers = [
    ["article_name", "Artikel"],
    ["purchased_quantity", "Gekauft"],
    ["sold_quantity", "Verkauft"],
    ["stock", "Aktueller Bestand"],
    ["minimum_stock", "Mindestbestand"],
    ["minimum_stock_warning", "Warnung"],
    ["no_reorder", "Nachbestellen"],
    ["is_available_for_sale", "Angeboten"],
    ["purchase_cost_cents", "Ausgaben"],
    ["revenue_cents", "Umsatz"],
    ["donation_cents", "Spenden"],
  ];
  const numericSortKeys = new Set([
    "purchased_quantity",
    "sold_quantity",
    "stock",
    "minimum_stock",
    "minimum_stock_warning",
    "no_reorder",
    "is_available_for_sale",
    "purchase_cost_cents",
    "revenue_cents",
    "donation_cents",
  ]);
  const emptyMessages = {
    reorder: "Keine Varianten entsprechen den aktuellen Filtern.",
    obsolete: "Keine obsoleten Varianten entsprechen den aktuellen Filtern.",
  };
  const categoryTitles = { reorder: "Artikelbilanz", obsolete: "Obsolet" };
  const collator = new Intl.Collator("de-DE", { numeric: true, sensitivity: "base" });
  const state = {
    filter: "",
    onlyPurchased: onlyPurchasedInput.checked,
    grouped: groupByArticleInput.checked,
    sort: Object.fromEntries(views.map((view) => [view, { key: null, direction: "default" }])),
  };

  function normalise(value) {
    return String(value || "")
      .toLocaleLowerCase("de-DE")
      .normalize("NFD")
      .replace(/[\u0300-\u036f]/g, "");
  }

  function money(cents) {
    return window.MERCH_APP.moneyFormatter.format((Number(cents) || 0) / 100);
  }

  function rowText(row) {
    return String(row.article_name || "") + " " + String(row.option_text || "") + " " + String(row.label || "");
  }

  function isVisible(row) {
    if (state.onlyPurchased && Number(row.purchased_quantity) <= 0) return false;
    return !state.filter || normalise(rowText(row)).includes(state.filter);
  }

  function sortableValue(row, key) {
    if (key === "article_name") return String(row.article_name || "") + "\u0000" + String(row.option_text || "");
    if (key === "minimum_stock") {
      return row.minimum_stock === null || row.minimum_stock === undefined ? null : Number(row.minimum_stock);
    }
    if (key === "minimum_stock_warning" || key === "no_reorder" || key === "is_available_for_sale") {
      return row[key] ? 1 : 0;
    }
    return Number(row[key]) || 0;
  }

  function sortedRows(view) {
    const sort = state.sort[view];
    const visibleRows = rowsByView[view].filter(isVisible).slice();
    if (!sort || sort.direction === "default" || !sort.key) return visibleRows;
    return visibleRows.sort((left, right) => {
      const leftValue = sortableValue(left, sort.key);
      const rightValue = sortableValue(right, sort.key);
      if (leftValue === null || rightValue === null) {
        if (leftValue === null && rightValue !== null) return 1;
        if (rightValue === null && leftValue !== null) return -1;
      }
      let comparison;
      if (numericSortKeys.has(sort.key)) {
        comparison = Number(leftValue) - Number(rightValue);
      } else {
        comparison = collator.compare(String(leftValue), String(rightValue));
      }
      if (comparison === 0) comparison = collator.compare(rowText(left), rowText(right));
      return sort.direction === "asc" ? comparison : -comparison;
    });
  }

  function groupsFor(rows) {
    const groups = [];
    const known = new Map();
    rows.forEach((row) => {
      const name = String(row.article_name || "Ohne Artikel");
      let group = known.get(name);
      if (!group) {
        group = { name, rows: [] };
        known.set(name, group);
        groups.push(group);
      }
      group.rows.push(row);
    });
    return groups;
  }

  function appendTextCell(tableRow, value, className = "") {
    const cell = document.createElement("td");
    if (className) cell.className = className;
    cell.textContent = String(value);
    tableRow.append(cell);
    return cell;
  }

  function appendStatusCell(tableRow, value, kind) {
    const cell = document.createElement("td");
    const status = document.createElement("span");
    status.className = "status " + kind;
    status.textContent = value;
    cell.append(status);
    tableRow.append(cell);
  }

  function createBalanceRow(row) {
    const tableRow = document.createElement("tr");
    if (row.minimum_stock_warning) tableRow.classList.add("low-stock-row");

    const articleCell = document.createElement("td");
    const articleName = document.createElement("strong");
    articleName.textContent = String(row.article_name || "");
    articleCell.append(articleName);
    if (row.option_text) {
      const options = document.createElement("small");
      options.className = "table-subline";
      options.textContent = String(row.option_text);
      articleCell.append(options);
    }
    if (!row.is_active) {
      const inactive = document.createElement("small");
      inactive.className = "table-subline muted";
      inactive.textContent = "Variante nicht mehr erhältlich";
      articleCell.append(inactive);
    }
    tableRow.append(articleCell);
    appendTextCell(tableRow, Number(row.purchased_quantity) || 0);
    appendTextCell(tableRow, Number(row.sold_quantity) || 0);
    appendTextCell(tableRow, Number(row.stock) || 0, "stock-cell " + (Number(row.stock) === 0 ? "out-of-stock" : ""));
    appendTextCell(tableRow, row.minimum_stock === null || row.minimum_stock === undefined ? "—" : row.minimum_stock);
    if (row.minimum_stock_warning) {
      appendStatusCell(tableRow, "Grenzwert erreicht", "warning");
    } else {
      appendTextCell(tableRow, "—");
    }
    appendStatusCell(tableRow, row.no_reorder ? "nein" : "ja", row.no_reorder ? "warning" : "good");
    appendStatusCell(tableRow, row.is_available_for_sale ? "ja" : "nein", row.is_available_for_sale ? "good" : "warning");
    appendTextCell(tableRow, money(row.purchase_cost_cents));
    appendTextCell(tableRow, money(row.revenue_cents));
    appendTextCell(tableRow, money(row.donation_cents));
    return tableRow;
  }

  function createEmptyRow(message) {
    const row = document.createElement("tr");
    const cell = document.createElement("td");
    cell.colSpan = headers.length;
    cell.className = "empty-cell";
    cell.textContent = message;
    row.append(cell);
    return row;
  }

  function createHeader(view) {
    const head = document.createElement("thead");
    const row = document.createElement("tr");
    headers.forEach(([key, label]) => {
      const cell = document.createElement("th");
      cell.dataset.balanceSortHeader = "";
      cell.dataset.balanceView = view;
      cell.dataset.balanceSortKey = key;
      const button = document.createElement("button");
      button.className = "balance-sort-button";
      button.type = "button";
      button.dataset.balanceSortKey = key;
      button.dataset.balanceView = view;
      button.append(document.createTextNode(label + " "));
      const icon = document.createElement("span");
      icon.dataset.balanceSortIcon = "";
      icon.setAttribute("aria-hidden", "true");
      icon.textContent = "";
      button.append(icon);
      cell.append(button);
      row.append(cell);
    });
    head.append(row);
    return head;
  }

  function fillBody(body, rows, emptyMessage) {
    body.replaceChildren();
    if (!rows.length) {
      body.append(createEmptyRow(emptyMessage));
      return;
    }
    rows.forEach((row) => body.append(createBalanceRow(row)));
  }

  function renderGrouped(view, rows) {
    const container = document.querySelector("[data-balance-grouped=\"" + view + "\"]");
    if (!container) return;
    container.replaceChildren();
    if (!rows.length) {
      const empty = document.createElement("p");
      empty.className = "empty-cell";
      empty.textContent = emptyMessages[view];
      container.append(empty);
      return;
    }
    groupsFor(rows).forEach((group) => {
      const section = document.createElement("section");
      section.className = "balance-article-group";
      const heading = document.createElement("div");
      heading.className = "balance-article-group-heading";
      const title = document.createElement("h3");
      title.textContent = group.name;
      const count = document.createElement("small");
      count.textContent = String(group.rows.length) + " " + (group.rows.length === 1 ? "Variante" : "Varianten");
      heading.append(title, count);
      const scroll = document.createElement("div");
      scroll.className = "table-scroll balance-group-table-scroll";
      const table = document.createElement("table");
      table.className = "balance-table";
      table.dataset.balanceTable = view;
      const body = document.createElement("tbody");
      fillBody(body, group.rows, emptyMessages[view]);
      table.append(createHeader(view), body);
      scroll.append(table);
      section.append(heading, scroll);
      container.append(section);
    });
  }

  function updateSortIndicators() {
    document.querySelectorAll("[data-balance-sort-header]").forEach((header) => {
      const view = header.dataset.balanceView;
      const key = header.dataset.balanceSortKey;
      const sort = state.sort[view];
      const active = sort && sort.direction !== "default" && sort.key === key;
      header.setAttribute("aria-sort", active ? (sort.direction === "asc" ? "ascending" : "descending") : "none");
      const icon = header.querySelector("[data-balance-sort-icon]");
      if (icon) icon.textContent = active ? (sort.direction === "asc" ? "↑" : "↓") : "";
    });
  }

  function renderView(view) {
    const rows = sortedRows(view);
    const flat = document.querySelector("[data-balance-flat=\"" + view + "\"]");
    const grouped = document.querySelector("[data-balance-grouped=\"" + view + "\"]");
    if (flat) flat.hidden = state.grouped;
    if (grouped) grouped.hidden = !state.grouped;
    if (state.grouped) {
      renderGrouped(view, rows);
    } else {
      const body = document.querySelector("[data-balance-rows=\"" + view + "\"]");
      if (body) fillBody(body, rows, emptyMessages[view]);
    }
    return rows.length;
  }

  function render() {
    const count = views.reduce((total, view) => total + renderView(view), 0);
    if (resultCount) resultCount.textContent = String(count) + " " + (count === 1 ? "Variante" : "Varianten") + " sichtbar";
    updateSortIndicators();
  }

  function csvEscape(value) {
    const text = String(value ?? "");
    return /[;"\r\n]/.test(text) ? "\"" + text.replace(/"/g, "\"\"") + "\"" : text;
  }

  function localDateStamp() {
    const now = new Date();
    const local = new Date(now.getTime() - now.getTimezoneOffset() * 60_000);
    return local.toISOString().slice(0, 10);
  }

  const exportColumns = {
    inventory: [
      ["Artikel", (row) => row.article_name],
      ["Optionen", (row) => row.option_text],
      ["Gekauft", (row) => row.purchased_quantity],
      ["Verkauft", (row) => row.sold_quantity],
      ["Aktueller Bestand", (row) => row.stock],
      ["Mindestbestand", (row) => row.minimum_stock === null || row.minimum_stock === undefined ? "" : row.minimum_stock],
      ["Mindestbestandswarnung", (row) => row.minimum_stock_warning ? "ja" : "nein"],
      ["Nachbestellen", (row) => row.no_reorder ? "nein" : "ja"],
      ["Angeboten", (row) => row.is_available_for_sale ? "ja" : "nein"],
      ["Ausgaben", (row) => money(row.purchase_cost_cents)],
      ["Umsatz", (row) => money(row.revenue_cents)],
      ["Spenden", (row) => money(row.donation_cents)],
    ],
    articles: [
      ["Artikel", (row) => row.article_name],
      ["Optionen", (row) => row.option_text],
      ["Verkaufspreis", (row) => money(row.sale_price_cents)],
      ["Standard-Einkaufspreis", (row) => money(row.default_purchase_price_cents)],
      ["Mindestbestand", (row) => row.minimum_stock === null || row.minimum_stock === undefined ? "" : row.minimum_stock],
      ["Nachbestellen", (row) => row.no_reorder ? "nein" : "ja"],
      ["Angeboten", (row) => row.is_available_for_sale ? "ja" : "nein"],
      ["Status", (row) => row.is_active ? "aktiv" : "inaktiv"],
    ],
  };

  function addExportRows(output, rows, columns) {
    if (state.grouped) {
      groupsFor(rows).forEach((group, index) => {
        if (index > 0) output.push([]);
        group.rows.forEach((row) => output.push(columns.map(([, value]) => value(row))));
      });
      return;
    }
    rows.forEach((row) => output.push(columns.map(([, value]) => value(row))));
  }

  function downloadCsv(kind) {
    const columns = exportColumns[kind];
    if (!columns) return;
    const output = [];
    let hasRows = false;
    views.forEach((view) => {
      const rows = sortedRows(view);
      if (!rows.length) return;
      if (hasRows) output.push([]);
      output.push([categoryTitles[view]]);
      output.push(columns.map(([label]) => label));
      addExportRows(output, rows, columns);
      hasRows = true;
    });
    if (!hasRows) output.push(["Keine Varianten entsprechen der aktuellen Anzeige."]);

    const csv = "\ufeff" + output.map((row) => row.map(csvEscape).join(";")).join("\r\n") + "\r\n";
    const blob = new Blob([csv], { type: "text/csv;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = (kind === "inventory" ? "bestand" : "artikel") + "-" + localDateStamp() + ".csv";
    document.body.append(link);
    link.click();
    link.remove();
    URL.revokeObjectURL(url);
  }

  filterInput.addEventListener("input", () => {
    state.filter = normalise(filterInput.value).trim();
    render();
  });
  onlyPurchasedInput.addEventListener("change", () => {
    state.onlyPurchased = onlyPurchasedInput.checked;
    render();
  });
  groupByArticleInput.addEventListener("change", () => {
    state.grouped = groupByArticleInput.checked;
    render();
  });
  document.addEventListener("click", (event) => {
    const sortButton = event.target.closest("button[data-balance-sort-key][data-balance-view]");
    if (sortButton) {
      const view = sortButton.dataset.balanceView;
      const key = sortButton.dataset.balanceSortKey;
      if (!views.includes(view) || !headers.some(([headerKey]) => headerKey === key)) return;
      const sort = state.sort[view];
      if (sort.key !== key || sort.direction === "default") {
        state.sort[view] = { key, direction: "asc" };
      } else if (sort.direction === "asc") {
        state.sort[view] = { key, direction: "desc" };
      } else {
        state.sort[view] = { key: null, direction: "default" };
      }
      render();
      return;
    }
    const exportButton = event.target.closest("button[data-balance-export]");
    if (exportButton) downloadCsv(exportButton.dataset.balanceExport);
  });

  render();
})();

(function () {
  "use strict";
  const cards = document.querySelectorAll("[data-ranking-card]");
  if (!cards.length) return;
  const collator = new Intl.Collator("de-DE", { numeric: true, sensitivity: "base" });
  const money = (cents) => window.MERCH_APP.moneyFormatter.format((Number(cents) || 0) / 100);

  function setMode(card, mode) {
    const list = card.querySelector("[data-ranking-list]");
    if (!list) return;
    const rows = Array.from(list.querySelectorAll("[data-ranking-row]"));
    rows.sort((left, right) => {
      const leftValue = Number(left.dataset[mode + "Cents"] || 0);
      const rightValue = Number(right.dataset[mode + "Cents"] || 0);
      const difference = rightValue - leftValue;
      if (difference) return difference;
      return collator.compare(left.querySelector("strong")?.textContent || "", right.querySelector("strong")?.textContent || "");
    });
    rows.forEach((row, index) => {
      row.hidden = index >= 5;
      const value = row.querySelector("[data-ranking-value]");
      if (value) value.textContent = money(row.dataset[mode + "Cents"]);
      list.append(row);
    });

    const title = card.querySelector("[data-ranking-title]");
    if (title) title.textContent = mode === "profit" ? title.dataset.profitTitle : title.dataset.incomeTitle;
    const description = card.querySelector("[data-ranking-description]");
    if (description) {
      description.textContent = mode === "profit"
        ? description.dataset.profitDescription
        : description.dataset.incomeDescription;
    }
    card.querySelectorAll("[data-ranking-mode]").forEach((button) => {
      const active = button.dataset.rankingMode === mode;
      button.classList.toggle("active", active);
      button.setAttribute("aria-pressed", String(active));
    });
  }

  cards.forEach((card) => {
    card.querySelectorAll("[data-ranking-mode]").forEach((button) => {
      button.addEventListener("click", () => setMode(card, button.dataset.rankingMode));
    });
    setMode(card, "income");
  });
})();
