/* Shared article/variant selector for the Verkauf and Einkäufe screens.
 *
 * The server sends generic option groups.  This script deliberately does not
 * know anything about colours or sizes: it only selects one value per group and
 * finds the matching variant by its stable option-value ID combination.
 */
(function () {
  "use strict";

  function centsToEuro(cents) {
    return window.MERCH_APP.moneyFormatter.format((Number(cents) || 0) / 100);
  }

  function moneyInputToCents(input) {
    const raw = String(input || "").trim();
    if (!raw) return null;
    const cleaned = raw.replace(/€/g, "").replace(/\s/g, "");
    if (!cleaned) return null;
    const normalised = cleaned.includes(",")
      ? cleaned.replace(/\./g, "").replace(",", ".")
      : cleaned;
    const value = Number(normalised);
    return Number.isFinite(value) && value >= 0 ? Math.round(value * 100) : null;
  }

  function inputToCents(input) {
    return moneyInputToCents(input) ?? 0;
  }

  function centsToInput(cents) {
    return ((Number(cents) || 0) / 100).toFixed(2).replace(".", ",");
  }

  function optionKey(ids) {
    return [...ids].map(Number).sort((a, b) => a - b).join("|");
  }

  /**
   * Wire a generic article selector to concrete DOM nodes.
   *
   * @param {Object} config
   * @param {Array} config.articles Article payload from the Flask view
   * @param {HTMLElement} config.buttonContainer Left-side article button list
   * @param {HTMLElement} config.optionContainer Middle option area
   * @param {(variant: Object|null, article: Object|null) => void} config.onVariantChanged
   */
  function setupVariantSelector(config) {
    const articlesById = new Map(config.articles.map((article) => [Number(article.id), article]));
    let selectedArticle = null;
    let selectedValues = new Map();

    function emitVariant() {
      if (!selectedArticle) {
        config.onVariantChanged(null, null);
        return;
      }
      const expectedGroups = selectedArticle.groups || [];
      if (expectedGroups.length !== selectedValues.size) {
        config.onVariantChanged(null, selectedArticle);
        return;
      }
      const key = optionKey([...selectedValues.values()]);
      const variant = (selectedArticle.variants || []).find(
        (candidate) => optionKey(candidate.option_value_ids || []) === key
      );
      config.onVariantChanged(variant || null, selectedArticle);
    }

    function selectableValues(group) {
      if (!selectedArticle) return [];
      const groupId = Number(group.id);
      return (group.values || []).filter((value) => {
        const valueId = Number(value.id);
        return (selectedArticle.variants || []).some((variant) => {
          const optionIds = new Set((variant.option_value_ids || []).map(Number));
          if (!optionIds.has(valueId)) return false;
          return [...selectedValues.entries()].every(([selectedGroupId, selectedValueId]) => {
            // The current group is intentionally ignored: this is the value
            // the person can still choose or change right now.
            if (Number(selectedGroupId) === groupId) return true;
            return optionIds.has(Number(selectedValueId));
          });
        });
      });
    }

    function selectValue(groupId, valueId) {
      selectedValues.set(Number(groupId), Number(valueId));
      // When an earlier choice changes, clear only the now-impossible choices
      // in the other groups. This prevents a withdrawn variant from being
      // reconstructed through a stale option selection.
      (selectedArticle?.groups || []).forEach((group) => {
        if (Number(group.id) === Number(groupId)) return;
        const selectedValueId = selectedValues.get(Number(group.id));
        if (
          selectedValueId !== undefined &&
          !selectableValues(group).some((value) => Number(value.id) === Number(selectedValueId))
        ) {
          selectedValues.delete(Number(group.id));
        }
      });
      renderOptions();
      emitVariant();
    }

    function renderOptions() {
      config.optionContainer.replaceChildren();
      if (!selectedArticle) {
        const message = document.createElement("div");
        message.className = "empty-selection";
        message.textContent = "Bitte links einen Artikel auswählen.";
        config.optionContainer.append(message);
        return;
      }
      const groups = selectedArticle.groups || [];
      if (!groups.length) {
        const message = document.createElement("div");
        message.className = "empty-selection";
        message.textContent = "Dieser Artikel hat keine auswählbaren Optionen.";
        config.optionContainer.append(message);
        return;
      }
      groups.forEach((group) => {
        const wrapper = document.createElement("section");
        wrapper.className = "option-group";
        const heading = document.createElement("h3");
        heading.textContent = group.name;
        const choices = document.createElement("div");
        choices.className = "option-choice-list";
        selectableValues(group).forEach((value) => {
          const choice = document.createElement("button");
          choice.type = "button";
          choice.className = "option-choice";
          choice.textContent = value.value;
          if (selectedValues.get(Number(group.id)) === Number(value.id)) {
            choice.classList.add("selected");
          }
          choice.addEventListener("click", () => selectValue(group.id, value.id));
          choices.append(choice);
        });
        wrapper.append(heading, choices);
        config.optionContainer.append(wrapper);
      });
    }

    function selectArticle(articleId) {
      selectedArticle = articlesById.get(Number(articleId)) || null;
      selectedValues = new Map();
      config.buttonContainer.querySelectorAll("[data-article-id]").forEach((button) => {
        button.classList.toggle("selected", Number(button.dataset.articleId) === Number(articleId));
      });
      // A group with exactly one valid value is unambiguous; selecting it saves
      // a tap without imposing an arbitrary default for larger lists.
      if (selectedArticle) {
        (selectedArticle.groups || []).forEach((group) => {
          const values = selectableValues(group);
          if (values.length === 1) selectedValues.set(Number(group.id), Number(values[0].id));
        });
      }
      renderOptions();
      if (selectedArticle && !(selectedArticle.groups || []).length) {
        config.onVariantChanged((selectedArticle.variants || [])[0] || null, selectedArticle);
      } else {
        emitVariant();
      }
    }

    config.buttonContainer.querySelectorAll("[data-article-id]").forEach((button) => {
      button.addEventListener("click", () => selectArticle(button.dataset.articleId));
    });

    renderOptions();
    return {
      clear() {
        selectedArticle = null;
        selectedValues = new Map();
        config.buttonContainer.querySelectorAll("[data-article-id]").forEach((button) => button.classList.remove("selected"));
        renderOptions();
        config.onVariantChanged(null, null);
      },
      selectedVariant() {
        if (!selectedArticle) return null;
        const key = optionKey([...selectedValues.values()]);
        return (selectedArticle.variants || []).find((variant) => optionKey(variant.option_value_ids || []) === key) || null;
      },
    };
  }

  window.MerchTransaction = { setupVariantSelector, centsToEuro, moneyInputToCents, inputToCents, centsToInput };
})();
