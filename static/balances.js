/* Render the balance trend without a third-party chart dependency.
 *
 * The server provides cent-accurate, already-authorised data. SVG keeps the
 * chart crisp when the phone/browser zoom level changes and remains usable in
 * an otherwise dependency-free NAS installation.
 */
(function () {
  "use strict";
  const container = document.getElementById("income-chart");
  const source = document.getElementById("balance-daily-income-data");
  const empty = document.getElementById("income-chart-empty");
  if (!container || !source) return;

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

  svg.setAttribute("viewBox", `0 0 ${width} ${height}`);
  svg.setAttribute("preserveAspectRatio", "none");
  svg.setAttribute("aria-hidden", "true");
  svg.classList.add("income-chart-svg");

  [0, 0.5, 1].forEach((fraction) => {
    const y = yFor(maxValue * fraction);
    add("line", { x1: padding.left, x2: width - padding.right, y1: y, y2: y, class: "income-grid-line" });
    add("text", { x: padding.left - 10, y: y + 4, "text-anchor": "end", class: "income-axis-label" }, format(maxValue * fraction));
  });

  const points = values.map((value, index) => `${xFor(index)},${yFor(value)}`).join(" ");
  const areaPoints = `${padding.left},${padding.top + graphHeight} ${points} ${width - padding.right},${padding.top + graphHeight}`;
  add("polygon", { points: areaPoints, class: "income-area" });
  add("polyline", { points, class: "income-line" });
  values.forEach((value, index) => {
    const dot = add("circle", { cx: xFor(index), cy: yFor(value), r: 4, class: "income-dot" });
    const title = document.createElementNS(namespace, "title");
    title.textContent = `${rows[index].date}: ${format(value)}`;
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
