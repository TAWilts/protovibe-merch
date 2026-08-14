/* Dynamic article-option grid.
 *
 * The grid represents independent value lists per option column.  It does not
 * encode a matrix of variants: the backend builds the Cartesian combinations.
 * Keeping the state here explicit makes value deletion and its warning easy to
 * understand when reading the code later.
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

  let groups = (source.option_groups || []).map((group) => ({
    id: group.id,
    name: group.name,
    values: (group.values || []).map((value) => ({ id: value.id, value: value.value })),
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
    while (values.length <= valueIndex) values.push({ id: null, value: "" });
    return values[valueIndex];
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
      input.addEventListener("input", () => { groups[groupIndex].name = input.value; });
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
        const value = group.values[rowIndex] || { id: null, value: "" };
        const input = document.createElement("input");
        input.value = value.value;
        input.placeholder = "Wert";
        input.addEventListener("input", () => { ensureValue(groupIndex, rowIndex).value = input.value; });
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
  }

  addColumnButton.addEventListener("click", () => {
    const rows = maximumRows();
    groups.push({ id: null, name: "Neue Option", values: Array.from({ length: rows }, () => ({ id: null, value: "" })) });
    render();
  });
  addRowButton.addEventListener("click", () => {
    groups.forEach((group) => group.values.push({ id: null, value: "" }));
    render();
  });

  form.addEventListener("submit", (event) => {
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
