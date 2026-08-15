/* Lightweight, local full-text filters for ledger-style tables.
 *
 * The server remains the accounting source of truth. Filtering only changes
 * the visible rows and deliberately never sends a query or any booking data
 * to a third party. Receipt detail rows carry a group key, so they disappear
 * together with their parent cart without being forced open by a search.
 */
(function () {
  "use strict";

  function normalise(value) {
    return String(value || "")
      .toLocaleLowerCase("de-DE")
      .normalize("NFD")
      .replace(/[\u0300-\u036f]/g, "");
  }

  function updateScope(scope, term) {
    let matches = 0;
    scope.querySelectorAll("[data-filter-row]").forEach((row) => {
      const text = normalise(row.dataset.filterText || row.textContent);
      const visible = !term || text.includes(term);
      row.style.display = visible ? "" : "none";
      if (visible) matches += 1;
      const group = row.dataset.filterGroup;
      if (group) {
        scope.querySelectorAll("[data-filter-linked]").forEach((linked) => {
          if (linked.dataset.filterLinked === group) linked.style.display = visible ? "" : "none";
        });
      }
    });
    return matches;
  }

  function init(input) {
    const selector = input.dataset.filterTargets;
    if (!selector) return;
    const scopes = Array.from(document.querySelectorAll(selector));
    const result = input.closest(".table-filter")?.querySelector("[data-filter-result-count]");
    const refresh = () => {
      const term = normalise(input.value).trim();
      const matches = scopes.reduce((total, scope) => total + updateScope(scope, term), 0);
      if (result) {
        result.textContent = term ? `${matches} Treffer` : "";
      }
    };
    input.addEventListener("input", refresh);
    refresh();
  }

  document.querySelectorAll("[data-table-filter]").forEach(init);
})();
