/* Deliberate Band-Admin assignment workflow.
 *
 * The warning is a usability safeguard. Authorization, the confirmation flag
 * and the current password are still validated by the server.
 */
(function () {
  "use strict";

  const dialog = document.getElementById("band-admin-role-dialog");
  if (!(dialog instanceof HTMLDialogElement)) return;

  const passwordInput = dialog.querySelector("[data-band-admin-password]");
  const cancelButton = dialog.querySelector("[data-band-admin-cancel]");
  const confirmButton = dialog.querySelector("[data-band-admin-confirm]");
  const countdownStatus = document.getElementById("band-admin-role-countdown");
  const roleSelects = document.querySelectorAll("[data-role-select]");
  if (!(passwordInput instanceof HTMLInputElement)
      || !(cancelButton instanceof HTMLButtonElement)
      || !(confirmButton instanceof HTMLButtonElement)
      || !(countdownStatus instanceof HTMLElement)) return;

  const CONFIRMATION_SECONDS = 3;
  let countdownTimer = null;
  let remainingSeconds = CONFIRMATION_SECONDS;
  let pendingAction = null;

  function confirmationInput(form) {
    return form.querySelector("[data-band-admin-confirmation]");
  }

  function copiedPasswordInput(form) {
    return form.querySelector("[data-band-admin-current-password]");
  }

  function clearConfirmedAction(form) {
    const confirmation = confirmationInput(form);
    if (confirmation instanceof HTMLInputElement) confirmation.value = "";
    copiedPasswordInput(form)?.remove();
  }

  function storeConfirmedAction(form, password) {
    clearConfirmedAction(form);
    const confirmation = confirmationInput(form);
    if (confirmation instanceof HTMLInputElement) confirmation.value = "confirmed";
    const passwordCopy = document.createElement("input");
    passwordCopy.type = "hidden";
    passwordCopy.name = "current_password";
    passwordCopy.value = password;
    passwordCopy.dataset.bandAdminCurrentPassword = "";
    form.append(passwordCopy);
  }

  function stopCountdown() {
    if (countdownTimer !== null) window.clearInterval(countdownTimer);
    countdownTimer = null;
  }

  function updateConfirmationState() {
    if (remainingSeconds > 0) {
      confirmButton.textContent = `Band-Admin zuweisen (${remainingSeconds})`;
      countdownStatus.textContent = `Bestätigung in ${remainingSeconds} ${remainingSeconds === 1 ? "Sekunde" : "Sekunden"} möglich.`;
    } else {
      confirmButton.textContent = "Band-Admin zuweisen";
      countdownStatus.textContent = "Die Bestätigung ist jetzt möglich.";
    }
    confirmButton.disabled = remainingSeconds > 0 || passwordInput.value.length === 0;
  }

  function startCountdown() {
    stopCountdown();
    remainingSeconds = CONFIRMATION_SECONDS;
    updateConfirmationState();
    countdownTimer = window.setInterval(() => {
      remainingSeconds -= 1;
      updateConfirmationState();
      if (remainingSeconds <= 0) stopCountdown();
    }, 1000);
  }

  function openConfirmation(select, submitAfterConfirmation) {
    const form = select.closest("form[data-role-form]");
    if (!(form instanceof HTMLFormElement)) return;
    clearConfirmedAction(form);
    pendingAction = {
      form,
      select,
      previousRole: select.dataset.currentRole || "seller",
      submitAfterConfirmation,
    };
    dialog.returnValue = "";
    passwordInput.value = "";
    startCountdown();
    dialog.showModal();
    passwordInput.focus();
  }

  function cancelPendingAction() {
    if (!pendingAction) return;
    const { form, select, previousRole } = pendingAction;
    clearConfirmedAction(form);
    select.value = previousRole;
    select.dataset.currentRole = previousRole;
    window.setTimeout(() => select.focus(), 0);
  }

  roleSelects.forEach((select) => {
    if (!(select instanceof HTMLSelectElement)) return;
    const form = select.closest("form[data-role-form]");
    if (!(form instanceof HTMLFormElement)) return;

    select.addEventListener("change", () => {
      clearConfirmedAction(form);
      if (select.value === "band_admin") {
        openConfirmation(select, form.hasAttribute("data-role-auto-submit"));
        return;
      }
      select.dataset.currentRole = select.value;
      if (form.hasAttribute("data-role-auto-submit")) form.requestSubmit();
    });

    form.addEventListener("submit", (event) => {
      if (select.value !== "band_admin") return;
      const confirmation = confirmationInput(form);
      const passwordCopy = copiedPasswordInput(form);
      if (confirmation instanceof HTMLInputElement
          && confirmation.value === "confirmed"
          && passwordCopy instanceof HTMLInputElement
          && passwordCopy.value) return;
      event.preventDefault();
      openConfirmation(select, true);
    });
  });

  passwordInput.addEventListener("input", updateConfirmationState);
  cancelButton.addEventListener("click", () => dialog.close("cancelled"));
  dialog.addEventListener("cancel", (event) => {
    event.preventDefault();
    dialog.close("cancelled");
  });
  dialog.addEventListener("close", () => {
    stopCountdown();
    passwordInput.value = "";
    if (dialog.returnValue !== "confirmed") cancelPendingAction();
    pendingAction = null;
    remainingSeconds = CONFIRMATION_SECONDS;
    updateConfirmationState();
  });

  confirmButton.addEventListener("click", () => {
    if (!pendingAction || confirmButton.disabled || !passwordInput.reportValidity()) return;
    const { form, select, submitAfterConfirmation } = pendingAction;
    storeConfirmedAction(form, passwordInput.value);
    select.dataset.currentRole = "band_admin";
    dialog.close("confirmed");
    if (submitAfterConfirmation) form.requestSubmit();
    else window.setTimeout(() => form.querySelector('button[type="submit"]')?.focus(), 0);
  });
})();
