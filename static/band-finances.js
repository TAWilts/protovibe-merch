(() => {
  "use strict";

  const category = document.getElementById("band-transaction-category");
  const dialog = document.getElementById("band-category-dialog");
  const form = document.getElementById("band-category-form");
  const name = document.getElementById("band-new-category-name");
  const error = document.getElementById("band-category-error");
  const cancel = document.getElementById("cancel-band-category");
  if (!category || !dialog || !form || !name || !error || !cancel) return;

  let lastCategory = category.value;

  function closeDialog() {
    dialog.close();
    error.hidden = true;
    error.textContent = "";
  }

  category.addEventListener("change", () => {
    if (category.value !== "__new__") {
      lastCategory = category.value;
      return;
    }
    name.value = "";
    error.hidden = true;
    dialog.showModal();
    name.focus();
  });

  cancel.addEventListener("click", () => {
    category.value = lastCategory;
    closeDialog();
  });
  dialog.addEventListener("cancel", () => {
    category.value = lastCategory;
  });
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    const value = name.value.trim().replace(/\s+/g, " ");
    if (!value) {
      error.textContent = "Bitte einen Kategorienamen eingeben.";
      error.hidden = false;
      return;
    }
    const existing = Array.from(category.options).find((option) => option.value.localeCompare(value, undefined, { sensitivity: "accent" }) === 0);
    if (existing) {
      category.value = existing.value;
    } else {
      const option = new Option(value, value, true, true);
      category.add(option, category.options.length - 1);
    }
    lastCategory = category.value;
    closeDialog();
  });

  document.querySelectorAll("[data-band-cancel-form]").forEach((cancelForm) => {
    cancelForm.addEventListener("submit", (event) => {
      if (!window.confirm("Diese Buchung stornieren? Betrag und Anhänge bleiben in der Historie erhalten.")) event.preventDefault();
    });
  });
})();
