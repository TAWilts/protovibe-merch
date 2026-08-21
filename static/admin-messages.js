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

  const emailDialog = document.getElementById("email-settings-dialog");
  const emailOpenButton = document.querySelector("[data-email-settings-open]");
  const emailCloseButton = document.querySelector("[data-email-settings-close]");
  if (emailDialog instanceof HTMLDialogElement && emailOpenButton instanceof HTMLButtonElement) {
    emailOpenButton.addEventListener("click", () => {
      emailDialog.showModal();
      emailDialog.querySelector('input[name="host"]')?.focus();
    });
    emailCloseButton?.addEventListener("click", () => emailDialog.close());
  }
})();
