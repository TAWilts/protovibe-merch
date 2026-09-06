(function () {
  "use strict";

  document.querySelectorAll("[data-csv-example-open]").forEach((button) => {
    button.addEventListener("click", () => {
      const dialog = document.getElementById(button.dataset.csvExampleOpen);
      if (dialog instanceof HTMLDialogElement) dialog.showModal();
    });
  });

  document.querySelectorAll("[data-csv-example-close]").forEach((button) => {
    button.addEventListener("click", () => button.closest("dialog")?.close());
  });

  document.querySelectorAll("[data-csv-copy]").forEach((button) => {
    button.addEventListener("click", async () => {
      const source = document.getElementById(button.dataset.csvCopy);
      if (!(source instanceof HTMLTextAreaElement)) return;
      try {
        await navigator.clipboard.writeText(source.value);
      } catch (_) {
        source.focus();
        source.select();
        document.execCommand("copy");
      }
      const previousLabel = button.textContent;
      button.textContent = "Kopiert";
      window.setTimeout(() => { button.textContent = previousLabel; }, 1400);
    });
  });
})();
