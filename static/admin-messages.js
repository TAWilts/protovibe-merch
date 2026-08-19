(function () {
  "use strict";

  const dialog = document.getElementById("admin-message-dialog");
  const openButton = document.querySelector("[data-admin-message-open]");
  const closeButton = document.querySelector("[data-admin-message-close]");
  if (!(dialog instanceof HTMLDialogElement) || !(openButton instanceof HTMLButtonElement)) return;

  openButton.addEventListener("click", () => {
    dialog.showModal();
    dialog.querySelector('input[name="subject"]')?.focus();
  });
  closeButton?.addEventListener("click", () => dialog.close());
})();
