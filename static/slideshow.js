/* Shared catalogue photo gallery and shop-display slideshow.
 *
 * A slideshow cycle is a shuffled "bag": every selected picture appears once
 * before the next cycle is shuffled. This avoids the familiar repeated-picture
 * effect of choosing a random item on every timer tick.
 */
(function () {
  "use strict";

  const dataNode = document.getElementById("product-slideshow-data");
  const gallery = document.getElementById("product-photo-gallery");
  const startButton = document.getElementById("start-product-slideshow");
  const selectedCount = document.getElementById("slideshow-selected-count");
  const variantSelect = document.getElementById("slideshow-variant-select");
  const uploadInput = document.getElementById("slideshow-photo-upload");
  const uploadStatus = document.getElementById("slideshow-upload-status");
  const changeRateInput = document.getElementById("slideshow-change-rate");
  const changeRateOutput = document.getElementById("slideshow-change-rate-value");
  const animationSpeedInput = document.getElementById("slideshow-animation-speed");
  const animationSpeedOutput = document.getElementById("slideshow-animation-speed-value");
  const overlay = document.getElementById("product-slideshow-overlay");
  const stage = document.getElementById("product-slideshow-stage");
  if (
    !dataNode || !gallery || !startButton || !selectedCount || !variantSelect || !uploadInput || !uploadStatus
    || !changeRateInput || !changeRateOutput || !animationSpeedInput || !animationSpeedOutput || !overlay || !stage
  ) return;

  const source = JSON.parse(dataNode.textContent || "{}");
  const variants = Array.isArray(source.variants) ? source.variants : [];
  const strings = {
    selectedCount: "{count} von {total} Fotos für die Diashow ausgewählt",
    empty: "Noch keine Bilder für die Diashow vorhanden.",
    noSelected: "Wähle mindestens ein Bild für die Diashow aus.",
    variantRequired: "Wähle eine Variante oder Anderes für die Fotos aus.",
    defaultVariant: "Standardvariante",
    other: "Anderes",
    otherHint: "Eigenständiges Dia ohne Artikel, Variante und Preis",
    notOffered: "Nicht im Verkauf angeboten",
    uploading: "Fotos werden hochgeladen und optimiert …",
    uploadFailed: "Die Produktfotos konnten nicht hochgeladen werden.",
    updateFailed: "Die Dia-Auswahl konnte nicht gespeichert werden.",
    deleteOther: "Bild entfernen",
    deleteOtherConfirm: "Dieses eigenständige Dia wirklich entfernen?",
    deleteFailed: "Das Dia konnte nicht entfernt werden.",
    changeRateValue: "alle {seconds} s",
    animationSpeedValue: "{speed}×",
    ...(window.MERCH_APP?.slideshowStrings || {}),
  };
  const directions = ["from-left", "from-right", "from-top", "from-bottom"];
  const exitDirections = ["to-left", "to-right", "to-top", "to-bottom"];
  const photos = Array.isArray(source.photos) ? source.photos.map(normalizePhoto) : [];
  let slideDurationMs = 6500;
  let frameAnimationMs = 750;
  let copyAnimationMs = 810;
  let copyDelayMs = 140;
  let uploadBusy = false;
  let slideshowRunning = false;
  let slideshowTimer = null;
  let slideshowBag = [];
  let previousSlideKey = null;
  let viewportFitFrame = null;

  function format(template, values) {
    return Object.entries(values).reduce(
      (result, [name, value]) => result.replaceAll(`{${name}}`, String(value)),
      template
    );
  }

  function isProductPhoto(photo) {
    return photo.is_product_photo !== false && photo.kind !== "other";
  }

  function photoKey(photo) {
    return String(photo.key || `${isProductPhoto(photo) ? "variant" : "other"}:${photo.id}`);
  }

  function normalizePhoto(photo) {
    const productPhoto = photo.is_product_photo !== false && photo.kind !== "other";
    const kind = productPhoto ? "variant" : "other";
    return {
      ...photo,
      kind,
      key: String(photo.key || `${kind}:${photo.id}`),
      is_product_photo: productPhoto,
      include_in_slideshow: Boolean(photo.include_in_slideshow),
    };
  }

  function photoUrl(photo) {
    if (photo.url) return String(photo.url);
    return isProductPhoto(photo) ? `/api/variantenfotos/${photo.id}` : `/api/diashow/fotos/${photo.id}`;
  }

  function selectedPhotos() {
    return photos.filter((photo) => photo.include_in_slideshow);
  }

  function photoVariantLabel(photo) {
    return photo.option_text || strings.defaultVariant;
  }

  function photoAlt(photo) {
    return isProductPhoto(photo)
      ? `${photo.article_name} · ${photoVariantLabel(photo)}`
      : (photo.original_filename || strings.other);
  }

  function money(cents) {
    return window.MERCH_APP.moneyFormatter.format((Number(cents) || 0) / 100);
  }

  function formatNumber(value) {
    const number = Number(value) || 0;
    const decimals = Math.abs(number - Math.round(number)) > 0.001 ? 2 : 0;
    return new Intl.NumberFormat(window.MERCH_APP?.language || "de", {
      minimumFractionDigits: decimals,
      maximumFractionDigits: 2,
    }).format(number);
  }

  function rangeValue(input, fallback) {
    const requested = Number(input.value);
    const minimum = Number(input.min);
    const maximum = Number(input.max);
    const value = Number.isFinite(requested) ? requested : fallback;
    return Math.min(maximum, Math.max(minimum, value));
  }

  function updateTimingControls() {
    const seconds = rangeValue(changeRateInput, 6.5);
    const speed = rangeValue(animationSpeedInput, 1);
    const frameDuration = Math.max(0.2, 0.75 / speed);
    const copyDuration = Math.max(0.22, frameDuration * 1.08);
    const copyDelay = Math.min(0.25, frameDuration * 0.18);

    changeRateInput.value = String(seconds);
    animationSpeedInput.value = String(speed);
    changeRateOutput.value = format(strings.changeRateValue, { seconds: formatNumber(seconds) });
    changeRateOutput.textContent = changeRateOutput.value;
    animationSpeedOutput.value = format(strings.animationSpeedValue, { speed: formatNumber(speed) });
    animationSpeedOutput.textContent = animationSpeedOutput.value;
    slideDurationMs = Math.round(seconds * 1000);
    frameAnimationMs = Math.round(frameDuration * 1000);
    copyAnimationMs = Math.round(copyDuration * 1000);
    copyDelayMs = Math.round(copyDelay * 1000);
    overlay.style.setProperty("--slideshow-frame-duration", `${frameDuration.toFixed(2)}s`);
    overlay.style.setProperty("--slideshow-copy-duration", `${copyDuration.toFixed(2)}s`);
    overlay.style.setProperty("--slideshow-copy-delay", `${copyDelay.toFixed(2)}s`);
  }

  function cssPixels(value) {
    const parsed = Number.parseFloat(value);
    return Number.isFinite(parsed) ? parsed : 0;
  }

  function availableSlideshowSize() {
    const style = window.getComputedStyle(stage);
    const visualWidth = Number(window.visualViewport?.width) || stage.clientWidth;
    const visualHeight = Number(window.visualViewport?.height) || stage.clientHeight;
    const stageWidth = Math.min(stage.clientWidth || visualWidth, visualWidth);
    const stageHeight = Math.min(stage.clientHeight || visualHeight, visualHeight);
    return {
      width: Math.max(1, stageWidth - cssPixels(style.paddingLeft) - cssPixels(style.paddingRight)),
      height: Math.max(1, stageHeight - cssPixels(style.paddingTop) - cssPixels(style.paddingBottom)),
    };
  }

  function fitSlideImage(slide, image) {
    if (!image.naturalWidth || !image.naturalHeight) return;
    const available = availableSlideshowSize();
    const aspectRatio = image.naturalWidth / image.naturalHeight;
    let width = available.width;
    let height = width / aspectRatio;
    if (height > available.height) {
      height = available.height;
      width = height * aspectRatio;
    }
    // Whole pixels prevent a fractional border from extending beyond the
    // visible viewport on high-DPI/mobile displays.
    slide.style.width = `${Math.max(1, Math.floor(width))}px`;
    slide.style.height = `${Math.max(1, Math.floor(height))}px`;
  }

  function fitCurrentSlide() {
    if (!slideshowRunning) return;
    if (viewportFitFrame !== null) window.cancelAnimationFrame(viewportFitFrame);
    viewportFitFrame = window.requestAnimationFrame(() => {
      viewportFitFrame = null;
      const slide = stage.querySelector(".product-slideshow-slide");
      const image = slide?.querySelector(".product-slideshow-frame img");
      if (slide instanceof HTMLElement && image instanceof HTMLImageElement) fitSlideImage(slide, image);
    });
  }

  function showUploadStatus(message = "", isError = false) {
    uploadStatus.hidden = !message;
    uploadStatus.textContent = message;
    uploadStatus.classList.toggle("file-status-error", isError);
  }

  function updateSelectionControls() {
    const selected = selectedPhotos().length;
    selectedCount.textContent = format(strings.selectedCount, { count: selected, total: photos.length });
    selectedCount.classList.toggle("warning", selected === 0);
    selectedCount.classList.toggle("good", selected > 0);
    startButton.disabled = selected === 0;
  }

  function gallerySort(left, right) {
    const typeDifference = Number(isProductPhoto(right)) - Number(isProductPhoto(left));
    if (typeDifference) return typeDifference;
    if (!isProductPhoto(left)) {
      return String(left.original_filename).localeCompare(String(right.original_filename), window.MERCH_APP.language)
        || Number(left.position) - Number(right.position)
        || Number(left.id) - Number(right.id);
    }
    return String(left.article_name).localeCompare(String(right.article_name), window.MERCH_APP.language)
      || String(left.option_text).localeCompare(String(right.option_text), window.MERCH_APP.language)
      || Number(left.position) - Number(right.position)
      || Number(left.id) - Number(right.id);
  }

  function renderGallery() {
    gallery.replaceChildren();
    const sortedPhotos = [...photos].sort(gallerySort);
    if (!sortedPhotos.length) {
      const empty = document.createElement("p");
      empty.className = "empty-state compact";
      empty.textContent = strings.empty;
      gallery.append(empty);
      updateSelectionControls();
      return;
    }
    sortedPhotos.forEach((photo) => {
      const productPhoto = isProductPhoto(photo);
      const card = document.createElement("article");
      card.className = "product-photo-card";
      card.classList.toggle("is-other", !productPhoto);
      card.classList.toggle("is-excluded", !photo.include_in_slideshow);
      card.dataset.photoKey = photoKey(photo);

      const image = document.createElement("img");
      image.src = photoUrl(photo);
      image.alt = photoAlt(photo);
      image.loading = "lazy";
      card.append(image);

      const copy = document.createElement("div");
      copy.className = "product-photo-card-copy";
      const name = document.createElement("strong");
      name.textContent = productPhoto ? photo.article_name : strings.other;
      const variant = document.createElement("span");
      variant.textContent = productPhoto ? photoVariantLabel(photo) : (photo.original_filename || strings.otherHint);
      copy.append(name, variant);
      if (productPhoto) {
        const price = document.createElement("span");
        price.className = "product-photo-card-price";
        price.textContent = money(photo.sale_price_cents);
        copy.append(price);
        if (!photo.is_offered) {
          const unavailable = document.createElement("small");
          unavailable.className = "status warning";
          unavailable.textContent = strings.notOffered;
          copy.append(unavailable);
        }
      } else {
        const hint = document.createElement("small");
        hint.textContent = strings.otherHint;
        copy.append(hint);
        const remove = document.createElement("button");
        remove.type = "button";
        remove.className = "secondary-button product-photo-delete-button";
        remove.dataset.deleteOtherPhoto = photoKey(photo);
        remove.textContent = strings.deleteOther;
        copy.append(remove);
      }
      card.append(copy);

      const include = document.createElement("label");
      include.className = "slideshow-include-toggle toggle-label";
      const checkbox = document.createElement("input");
      checkbox.type = "checkbox";
      checkbox.checked = Boolean(photo.include_in_slideshow);
      checkbox.dataset.photoInclusion = photoKey(photo);
      const label = document.createElement("span");
      label.textContent = strings.include;
      include.append(checkbox, label);
      card.append(include);
      gallery.append(card);
    });
    updateSelectionControls();
  }

  function setUploadBusy(busy) {
    uploadBusy = busy;
    variantSelect.disabled = busy;
    uploadInput.disabled = busy;
    uploadInput.closest(".slideshow-upload-button")?.classList.toggle("is-busy", busy);
  }

  async function responseBody(response, fallbackMessage) {
    const body = await response.json().catch(() => ({}));
    if (!response.ok || !body.ok) throw new Error(body.error || fallbackMessage);
    return body;
  }

  function upsertPhoto(photo) {
    const normalised = normalizePhoto(photo);
    const existingIndex = photos.findIndex((item) => photoKey(item) === photoKey(normalised));
    if (existingIndex === -1) photos.push(normalised);
    else photos[existingIndex] = { ...photos[existingIndex], ...normalised };
  }

  function upsertVariantPhotos(variantId, returnedPhotos) {
    const variant = variants.find((item) => Number(item.id) === Number(variantId));
    if (!variant) return;
    (Array.isArray(returnedPhotos) ? returnedPhotos : []).forEach((returnedPhoto) => {
      upsertPhoto({
        ...returnedPhoto,
        kind: "variant",
        key: `variant:${returnedPhoto.id}`,
        is_product_photo: true,
        variant_id: Number(variant.id),
        include_in_slideshow: Boolean(returnedPhoto.include_in_slideshow),
        article_name: variant.article_name,
        option_text: variant.option_text,
        label: variant.label,
        sale_price_cents: variant.sale_price_cents,
        is_offered: Boolean(variant.is_offered),
      });
    });
  }

  function upsertExtraPhotos(returnedPhotos) {
    (Array.isArray(returnedPhotos) ? returnedPhotos : []).forEach(upsertPhoto);
  }

  uploadInput.addEventListener("change", async () => {
    const files = Array.from(uploadInput.files || []);
    const target = variantSelect.value;
    if (!files.length) return;
    if (!target) {
      showUploadStatus(strings.variantRequired, true);
      uploadInput.value = "";
      return;
    }
    const isOther = target === "other";
    const variantId = Number(target);
    if (!isOther && (!Number.isInteger(variantId) || variantId <= 0)) {
      showUploadStatus(strings.variantRequired, true);
      uploadInput.value = "";
      return;
    }
    if (uploadBusy) return;
    setUploadBusy(true);
    showUploadStatus(strings.uploading);
    try {
      const formData = new FormData();
      files.forEach((file) => formData.append("photos", file));
      const response = await fetch(isOther ? "/api/diashow/fotos" : `/api/varianten/${variantId}/fotos`, {
        method: "POST",
        headers: { "X-CSRF-Token": window.MERCH_APP.csrfToken },
        body: formData,
      });
      const body = await responseBody(response, strings.uploadFailed);
      if (isOther) upsertExtraPhotos(body.photos);
      else upsertVariantPhotos(variantId, body.photos);
      showUploadStatus();
      renderGallery();
    } catch (error) {
      showUploadStatus(error instanceof Error ? error.message : strings.uploadFailed, true);
    } finally {
      uploadInput.value = "";
      setUploadBusy(false);
    }
  });

  gallery.addEventListener("change", async (event) => {
    const checkbox = event.target;
    if (!(checkbox instanceof HTMLInputElement) || checkbox.dataset.photoInclusion === undefined) return;
    const photo = photos.find((item) => photoKey(item) === checkbox.dataset.photoInclusion);
    if (!photo) return;
    const previousValue = Boolean(photo.include_in_slideshow);
    const nextValue = checkbox.checked;
    photo.include_in_slideshow = nextValue;
    renderGallery();
    const endpoint = isProductPhoto(photo)
      ? `/api/variantenfotos/${photo.id}/diashow`
      : `/api/diashow/fotos/${photo.id}`;
    try {
      const response = await fetch(endpoint, {
        method: "PATCH",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": window.MERCH_APP.csrfToken },
        body: JSON.stringify({ include_in_slideshow: nextValue }),
      });
      const body = await responseBody(response, strings.updateFailed);
      photo.include_in_slideshow = Boolean(body.include_in_slideshow);
      renderGallery();
    } catch (error) {
      photo.include_in_slideshow = previousValue;
      showUploadStatus(error instanceof Error ? error.message : strings.updateFailed, true);
      renderGallery();
    }
  });

  gallery.addEventListener("click", async (event) => {
    if (!(event.target instanceof Element)) return;
    const button = event.target.closest("[data-delete-other-photo]");
    if (!(button instanceof HTMLButtonElement)) return;
    const photo = photos.find((item) => photoKey(item) === button.dataset.deleteOtherPhoto);
    if (!photo || isProductPhoto(photo) || !window.confirm(strings.deleteOtherConfirm)) return;
    button.disabled = true;
    try {
      const response = await fetch(`/api/diashow/fotos/${photo.id}`, {
        method: "DELETE",
        headers: { "X-CSRF-Token": window.MERCH_APP.csrfToken },
      });
      await responseBody(response, strings.deleteFailed);
      const index = photos.findIndex((item) => photoKey(item) === photoKey(photo));
      if (index !== -1) photos.splice(index, 1);
      showUploadStatus();
      renderGallery();
    } catch (error) {
      button.disabled = false;
      showUploadStatus(error instanceof Error ? error.message : strings.deleteFailed, true);
    }
  });

  function shuffle(items) {
    const shuffled = [...items];
    for (let index = shuffled.length - 1; index > 0; index -= 1) {
      const replacement = Math.floor(Math.random() * (index + 1));
      [shuffled[index], shuffled[replacement]] = [shuffled[replacement], shuffled[index]];
    }
    return shuffled;
  }

  function refillSlideshowBag() {
    slideshowBag = shuffle(selectedPhotos());
    // The next item is popped from the array's end. At a cycle boundary make
    // a same-picture transition impossible when more than one picture is active.
    const lastIndex = slideshowBag.length - 1;
    if (slideshowBag.length > 1 && previousSlideKey !== null && photoKey(slideshowBag[lastIndex]) === previousSlideKey) {
      const alternative = Math.floor(Math.random() * lastIndex);
      [slideshowBag[lastIndex], slideshowBag[alternative]] = [slideshowBag[alternative], slideshowBag[lastIndex]];
    }
  }

  function randomDirection() {
    return directions[Math.floor(Math.random() * directions.length)];
  }

  function oppositeDirection(direction) {
    return {
      "from-left": "from-right",
      "from-right": "from-left",
      "from-top": "from-bottom",
      "from-bottom": "from-top",
    }[direction] || "from-right";
  }

  function exitDirection(direction) {
    return direction.replace("from-", "to-");
  }

  function beginSlideExit() {
    if (!slideshowRunning) return;
    const frame = stage.querySelector(".product-slideshow-frame");
    const copy = stage.querySelector(".product-slideshow-copy");
    if (!(frame instanceof HTMLElement)) {
      showNextSlide();
      return;
    }
    const frameDirection = randomDirection();
    const copyDirection = oppositeDirection(frameDirection);
    frame.classList.remove(...exitDirections);
    frame.classList.add("is-leaving", exitDirection(frameDirection));
    if (copy instanceof HTMLElement) {
      copy.classList.remove(...exitDirections);
      copy.classList.add("is-leaving", exitDirection(copyDirection));
    }
    const exitDuration = copy instanceof HTMLElement
      ? Math.max(frameAnimationMs, copyAnimationMs)
      : frameAnimationMs;
    slideshowTimer = window.setTimeout(showNextSlide, exitDuration);
  }

  function scheduleSlideExit() {
    const hasCopy = stage.querySelector(".product-slideshow-copy") instanceof HTMLElement;
    const entranceDuration = hasCopy
      ? Math.max(frameAnimationMs, copyAnimationMs + copyDelayMs)
      : frameAnimationMs;
    const exitDuration = hasCopy ? Math.max(frameAnimationMs, copyAnimationMs) : frameAnimationMs;
    const delayBeforeExit = Math.max(entranceDuration, slideDurationMs - exitDuration);
    slideshowTimer = window.setTimeout(beginSlideExit, delayBeforeExit);
  }

  function renderSlide(photo) {
    const direction = randomDirection();
    const frame = document.createElement("figure");
    frame.className = `product-slideshow-frame ${direction}`;
    const image = document.createElement("img");
    image.src = photoUrl(photo);
    image.alt = photoAlt(photo);
    image.decoding = "async";
    frame.append(image);

    const slide = document.createElement("div");
    slide.className = "product-slideshow-slide";
    slide.append(frame);
    if (isProductPhoto(photo)) {
      const copy = document.createElement("figcaption");
      copy.className = `product-slideshow-copy ${oppositeDirection(direction)}`;
      const article = document.createElement("strong");
      article.textContent = photo.article_name;
      const variant = document.createElement("span");
      variant.textContent = photoVariantLabel(photo);
      const price = document.createElement("em");
      price.textContent = money(photo.sale_price_cents);
      copy.append(article, variant, price);
      slide.append(copy);
    }
    stage.replaceChildren(slide);
    image.addEventListener("load", fitCurrentSlide, { once: true });
    if (image.complete && image.naturalWidth) fitCurrentSlide();
  }

  function preloadNextSlide() {
    const next = slideshowBag[slideshowBag.length - 1];
    if (!next) return;
    const image = new Image();
    image.src = photoUrl(next);
  }

  function showNextSlide() {
    if (!slideshowRunning) return;
    if (!slideshowBag.length) refillSlideshowBag();
    const photo = slideshowBag.pop();
    if (!photo) {
      closeSlideshow();
      return;
    }
    previousSlideKey = photoKey(photo);
    renderSlide(photo);
    preloadNextSlide();
    scheduleSlideExit();
  }

  function onSlideshowExit(event) {
    event.preventDefault();
    closeSlideshow();
  }

  function removeExitListeners() {
    document.removeEventListener("keydown", onSlideshowExit, true);
    document.removeEventListener("pointerdown", onSlideshowExit, true);
  }

  function closeSlideshow({ leaveFullscreen = true } = {}) {
    if (!slideshowRunning) return;
    slideshowRunning = false;
    window.clearTimeout(slideshowTimer);
    slideshowTimer = null;
    if (viewportFitFrame !== null) window.cancelAnimationFrame(viewportFitFrame);
    viewportFitFrame = null;
    removeExitListeners();
    overlay.hidden = true;
    overlay.setAttribute("aria-hidden", "true");
    stage.replaceChildren();
    document.body.classList.remove("product-slideshow-running");
    if (leaveFullscreen && document.fullscreenElement === overlay) {
      document.exitFullscreen?.().catch(() => {});
    }
    startButton.focus();
  }

  function onFullscreenChange() {
    if (!slideshowRunning) return;
    if (document.fullscreenElement !== overlay) {
      closeSlideshow({ leaveFullscreen: false });
      return;
    }
    fitCurrentSlide();
  }

  function startSlideshow() {
    if (!selectedPhotos().length) {
      showUploadStatus(strings.noSelected, true);
      return;
    }
    showUploadStatus();
    slideshowRunning = true;
    slideshowBag = [];
    previousSlideKey = null;
    overlay.hidden = false;
    overlay.setAttribute("aria-hidden", "false");
    document.body.classList.add("product-slideshow-running");
    overlay.focus();
    showNextSlide();
    const fullscreen = overlay.requestFullscreen?.();
    if (fullscreen && typeof fullscreen.catch === "function") fullscreen.catch(() => {});
    // The initiating click's pointerdown already happened. Waiting one task
    // lets the exact next click/key be the explicit "exit" gesture.
    window.setTimeout(() => {
      if (!slideshowRunning) return;
      document.addEventListener("keydown", onSlideshowExit, true);
      document.addEventListener("pointerdown", onSlideshowExit, true);
    }, 0);
  }

  function onTimingInput() {
    updateTimingControls();
    if (!slideshowRunning) return;
    window.clearTimeout(slideshowTimer);
    const currentSlide = stage.querySelector(".product-slideshow-slide");
    if (currentSlide?.querySelector(".is-leaving")) {
      const hasCopy = currentSlide.querySelector(".product-slideshow-copy") instanceof HTMLElement;
      slideshowTimer = window.setTimeout(
        showNextSlide,
        hasCopy ? Math.max(frameAnimationMs, copyAnimationMs) : frameAnimationMs
      );
    } else {
      scheduleSlideExit();
    }
  }

  startButton.addEventListener("click", startSlideshow);
  changeRateInput.addEventListener("input", onTimingInput);
  animationSpeedInput.addEventListener("input", onTimingInput);
  window.addEventListener("resize", fitCurrentSlide);
  window.visualViewport?.addEventListener("resize", fitCurrentSlide);
  document.addEventListener("fullscreenchange", onFullscreenChange);
  updateTimingControls();
  renderGallery();
  setUploadBusy(false);
})();
