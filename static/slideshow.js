(function () {
  "use strict";

  const photos = JSON.parse(document.getElementById("product-photos-data").textContent);
  const articles = JSON.parse(document.getElementById("product-articles-data").textContent);
  const $ = (id) => document.getElementById(id);
  const grid = $("product-photo-grid");
  const count = $("photo-count");
  const overlay = $("slideshow-overlay");
  const stage = $("slideshow-stage");
  const image = $("slideshow-image");
  const copy = $("slideshow-copy");
  const articleText = $("slideshow-article");
  const variantText = $("slideshow-variant");
  const priceText = $("slideshow-price");
  const photoPreferenceKey = `merch-slideshow-selection-${window.MERCH_APP.currentUser?.id || "guest"}`;
  let sequence = [];
  let sequenceIndex = 0;
  let timer = null;

  function selection() {
    try {
      const saved = JSON.parse(localStorage.getItem(photoPreferenceKey) || "null");
      return saved && typeof saved === "object" ? saved : {};
    } catch (_error) {
      return {};
    }
  }

  function updateCount() {
    const selected = grid.querySelectorAll("[data-photo-toggle]:checked").length;
    count.textContent = `(${selected} von ${photos.length} ausgewählt)`;
  }

  function restoreSelection() {
    const saved = selection();
    grid.querySelectorAll("[data-photo-card]").forEach((card) => {
      const toggle = card.querySelector("[data-photo-toggle]");
      if (Object.prototype.hasOwnProperty.call(saved, card.dataset.photoId)) {
        toggle.checked = saved[card.dataset.photoId] !== false;
      }
      toggle.addEventListener("change", () => {
        const next = selection();
        next[card.dataset.photoId] = toggle.checked;
        localStorage.setItem(photoPreferenceKey, JSON.stringify(next));
        updateCount();
      });
    });
    updateCount();
  }

  function shuffle(values) {
    for (let index = values.length - 1; index > 0; index -= 1) {
      const randomIndex = Math.floor(Math.random() * (index + 1));
      [values[index], values[randomIndex]] = [values[randomIndex], values[index]];
    }
    return values;
  }

  function stopSlideshow() {
    if (!overlay.hidden) overlay.hidden = true;
    document.body.classList.remove("slideshow-open");
    if (timer) window.clearTimeout(timer);
    timer = null;
    if (document.fullscreenElement && document.exitFullscreen) document.exitFullscreen().catch(() => {});
  }

  function displayNext() {
    if (sequenceIndex >= sequence.length) {
      stopSlideshow();
      return;
    }
    const photo = sequence[sequenceIndex];
    sequenceIndex += 1;
    image.className = "slideshow-image";
    stage.dataset.slideDirection = ["left", "right", "top", "bottom"][Math.floor(Math.random() * 4)];
    void image.offsetWidth;
    image.src = photo.url;
    image.alt = photo.original_filename || "Produktfoto";
    if (photo.variant_id || photo.article_name) {
      copy.hidden = false;
      articleText.textContent = photo.article_name || "";
      variantText.textContent = photo.variant_id ? (photo.variant_label || "") : "Produktfoto";
      priceText.textContent = photo.price_cents === null || photo.price_cents === undefined
        ? ""
        : window.MERCH_APP.moneyFormatter.format((Number(photo.price_cents) || 0) / 100);
    } else {
      copy.hidden = true;
    }
    timer = window.setTimeout(displayNext, 5200);
  }

  function startSlideshow() {
    const selectedIds = new Set([...grid.querySelectorAll("[data-photo-card]")]
      .filter((card) => card.querySelector("[data-photo-toggle]").checked)
      .map((card) => Number(card.dataset.photoId)));
    sequence = shuffle(photos.filter((photo) => selectedIds.has(Number(photo.id))).slice());
    if (!sequence.length) {
      window.alert("Bitte mindestens ein Foto auswählen.");
      return;
    }
    sequenceIndex = 0;
    overlay.hidden = false;
    document.body.classList.add("slideshow-open");
    if (document.documentElement.requestFullscreen) document.documentElement.requestFullscreen().catch(() => {});
    displayNext();
  }

  function populateVariants() {
    const articleId = Number($("photo-article").value);
    const field = $("photo-variant-field");
    const select = $("photo-variant");
    select.replaceChildren(new Option("Keine konkrete Variante", ""));
    const article = articles.find((candidate) => Number(candidate.id) === articleId);
    (article?.variants || []).forEach((variant) => select.append(new Option(variant.label, variant.id)));
    field.hidden = !article;
    select.disabled = !article;
  }

  $("start-product-slideshow").addEventListener("click", startSlideshow);
  overlay.addEventListener("click", stopSlideshow);
  window.addEventListener("keydown", (event) => {
    if (!overlay.hidden) {
      event.preventDefault();
      stopSlideshow();
    }
  });
  $("photo-article").addEventListener("change", populateVariants);
  $("product-photo-upload").addEventListener("submit", async (event) => {
    event.preventDefault();
    const status = $("product-photo-upload-status");
    status.textContent = "Fotos werden gespeichert …";
    try {
      const response = await fetch("/api/product-photos", {
        method: "POST",
        headers: { "X-CSRF-Token": window.MERCH_APP.csrfToken },
        body: new FormData(event.currentTarget),
      });
      const payload = await response.json();
      if (!response.ok || !payload.ok) throw new Error(payload.error || "Upload fehlgeschlagen.");
      window.location.reload();
    } catch (error) {
      status.textContent = error.message;
      status.classList.add("file-status-error");
    }
  });
  grid.addEventListener("click", async (event) => {
    const button = event.target.closest("[data-delete-photo]");
    if (!button) return;
    if (!window.confirm("Dieses gemeinsame Produktfoto wirklich löschen?")) return;
    const response = await fetch(`/api/product-photos/${button.dataset.deletePhoto}`, {
      method: "DELETE",
      headers: { "X-CSRF-Token": window.MERCH_APP.csrfToken },
    });
    if (response.ok) window.location.reload();
  });

  restoreSelection();
  populateVariants();
})();
