/* Open the protected SMTP settings form without exposing any secret values. */
(function () {
  "use strict";

  const dialog = document.getElementById("email-settings-dialog");
  if (!(dialog instanceof HTMLDialogElement)) return;

  document.querySelectorAll("[data-email-settings-open]").forEach((button) => {
    button.addEventListener("click", () => {
      if (!dialog.open) dialog.showModal();
      dialog.querySelector('input[name="current_password"]')?.focus();
    });
  });
  dialog.querySelector("[data-email-settings-close]")?.addEventListener("click", () => dialog.close());
  dialog.addEventListener("click", (event) => {
    if (event.target === dialog) dialog.close();
  });
})();
