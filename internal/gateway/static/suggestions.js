(() => {
  "use strict";

  const emptyLabel = "\u0424\u0430\u0439\u043b \u043d\u0435 \u0432\u044b\u0431\u0440\u0430\u043d";
  document.querySelectorAll("[data-suggestion-file-control]").forEach((control) => {
    const input = control.querySelector('input[type="file"]');
    const label = control.querySelector("[data-suggestion-file-name]");
    if (!(input instanceof HTMLInputElement) || !(label instanceof HTMLElement)) return;

    const update = () => {
      const file = input.files?.[0];
      label.textContent = file?.name || emptyLabel;
      control.classList.toggle("has-file", Boolean(file));
    };
    input.addEventListener("change", update);
    update();
  });
})();
