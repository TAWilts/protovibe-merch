/* One-time handover of the legacy Admin account.
 *
 * The timer and dialog make the privilege change deliberate. The server still
 * verifies authorization, credentials, the selected account and this explicit
 * confirmation marker inside the migration transaction.
 */
(function () {
  "use strict";

  const dialog = document.getElementById("legacy-role-handover-dialog");
  if (!(dialog instanceof HTMLDialogElement)) return;

  const forms = document.querySelectorAll("[data-legacy-role-handover-form]");
  const cancelButton = dialog.querySelector("[data-legacy-role-handover-cancel]");
  const confirmButton = dialog.querySelector("[data-legacy-role-handover-confirm]");
  const countdownStatus = document.getElementById("legacy-role-handover-countdown");
  if (!(cancelButton instanceof HTMLButtonElement)
      || !(confirmButton instanceof HTMLButtonElement)
      || !(countdownStatus instanceof HTMLElement)) return;

  const CONFIRMATION_SECONDS = 3;
  let countdownTimer = null;
  let remainingSeconds = CONFIRMATION_SECONDS;
  let pendingSubmission = null;

  function confirmationInput(form) {
    return form.querySelector("[data-legacy-role-handover-confirmation]");
  }

  function stopCountdown() {
    if (countdownTimer !== null) window.clearInterval(countdownTimer);
    countdownTimer = null;
  }

  function updateConfirmationState() {
    if (remainingSeconds > 0) {
      confirmButton.textContent = `Band-Admin übergeben (${remainingSeconds})`;
      countdownStatus.textContent = `Bestätigung in ${remainingSeconds} ${remainingSeconds === 1 ? "Sekunde" : "Sekunden"} möglich.`;
    } else {
      confirmButton.textContent = "Band-Admin übergeben";
      countdownStatus.textContent = "Die Bestätigung ist jetzt möglich.";
    }
    confirmButton.disabled = remainingSeconds > 0;
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

  function clearConfirmation(form) {
    const confirmation = confirmationInput(form);
    if (confirmation instanceof HTMLInputElement) confirmation.value = "";
  }

  forms.forEach((form) => {
    if (!(form instanceof HTMLFormElement)) return;
    form.addEventListener("submit", (event) => {
      const confirmation = confirmationInput(form);
      if (confirmation instanceof HTMLInputElement && confirmation.value === "confirmed") return;

      event.preventDefault();
      clearConfirmation(form);
      if (!form.reportValidity()) return;

      pendingSubmission = {
        form,
        submitter: event.submitter instanceof HTMLButtonElement ? event.submitter : null,
      };
      dialog.returnValue = "";
      startCountdown();
      dialog.showModal();
      cancelButton.focus();
    });
  });

  cancelButton.addEventListener("click", () => dialog.close("cancelled"));
  dialog.addEventListener("cancel", (event) => {
    event.preventDefault();
    dialog.close("cancelled");
  });
  dialog.addEventListener("close", () => {
    stopCountdown();
    const submission = pendingSubmission;
    pendingSubmission = null;

    if (submission && dialog.returnValue === "confirmed") {
      const confirmation = confirmationInput(submission.form);
      if (confirmation instanceof HTMLInputElement) confirmation.value = "confirmed";
      submission.form.requestSubmit(submission.submitter || undefined);
    } else if (submission) {
      clearConfirmation(submission.form);
      window.setTimeout(() => submission.submitter?.focus(), 0);
    }

    remainingSeconds = CONFIRMATION_SECONDS;
    updateConfirmationState();
  });

  confirmButton.addEventListener("click", () => {
    if (!pendingSubmission || confirmButton.disabled) return;
    dialog.close("confirmed");
  });
})();
