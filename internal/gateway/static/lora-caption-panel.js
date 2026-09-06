(() => {
  window.AIGatewayLoraCaptionPanel = {
    create({ root, view, edit, describe, save }) {
      const find = suffix => root.querySelector(`[data-caption-${suffix}]`);
      const field = find("text");
      const image = find("image");
      const thumbnails = new Map();
      let selected = "";
      let origin = null;
      let captionValue;
      let triggerValue;
      let editing = false;
      const refresh = () => {
        if (!root.open) return;
        const state = view(selected);
        if (!state?.item) { root.close(); return; }
        const { item, items, trigger } = state;
        const index = items.findIndex(value => value.id === selected);
        find("title").textContent = `Кадр ${index + 1} из ${items.length}`;
        find("name").textContent = item.name;
        find("meta").textContent = item.meta;
        if (image.getAttribute("src") !== item.src) image.src = item.src;
        image.alt = item.name;
        find("trigger").textContent = trigger || "Триггер не задан";
        find("trigger").classList.toggle("is-missing", !trigger);
        if (!editing && (captionValue !== item.caption || triggerValue !== trigger)) {
          field.value = window.AIGatewayLoraCaptions.captionBody(item.caption, trigger);
        }
        captionValue = item.caption; triggerValue = trigger;
        field.maxLength = Math.max(1, 1000 - (trigger ? trigger.length + 2 : 0));
        field.disabled = !state.editable;
        find("save").disabled = !state.canSave;
        find("save-state").textContent = state.saveState;
        find("save-state").classList.toggle("is-error", state.saveError);
        find("status").textContent = item.status;
        find("status").classList.toggle("is-error", item.statusError);
        const feedback = find("feedback");
        if (feedback) { feedback.hidden = !state.error; feedback.textContent = state.error || ""; }
        find("action-label").textContent = item.actionLabel;
        find("describe").disabled = item.actionDisabled;
        find("describe").setAttribute("aria-busy", String(item.active));
        find("previous").disabled = index === 0;
        find("next").disabled = index === items.length - 1;
        for (const [key, button] of thumbnails) if (!items.some(value => value.id === key)) { button.remove(); thumbnails.delete(key); }
        items.forEach((entry, position) => {
          let button = thumbnails.get(entry.id);
          if (!button) {
            button = document.createElement("button"); button.type = "button";
            button.className = "caption-frame-thumb";
            const picture = document.createElement("img"); picture.alt = ""; picture.loading = "lazy";
            const number = document.createElement("span");
            button.append(picture, number);
            button.addEventListener("click", () => choose(entry.id));
            thumbnails.set(entry.id, button);
          }
          const picture = button.querySelector("img");
          if (picture.getAttribute("src") !== entry.src) picture.src = entry.src;
          button.querySelector("span").textContent = position + 1;
          button.setAttribute("aria-label", `Кадр ${position + 1}: ${entry.name}`);
          button.setAttribute("aria-pressed", String(entry.id === selected));
          const strip = find("strip");
          if (strip.children[position] !== button) strip.insertBefore(button, strip.children[position] || null);
        });
      };
      const choose = key => {
        selected = key; captionValue = undefined; triggerValue = undefined;
        refresh();
        thumbnails.get(key)?.scrollIntoView({ block: "nearest", inline: "nearest" });
      };
      field.addEventListener("input", () => {
        editing = true;
        try { edit(selected, window.AIGatewayLoraCaptions.withTrigger(field.value, view(selected)?.trigger)); }
        finally { editing = false; }
      });
      find("describe").addEventListener("click", () => void describe(selected));
      find("save").addEventListener("click", () => void save());
      find("close").addEventListener("click", () => root.close());
      for (const [name, offset] of [["previous", -1], ["next", 1]]) find(name).addEventListener("click", () => {
        const items = view(selected)?.items || [];
        const next = items[items.findIndex(item => item.id === selected) + offset];
        if (next) choose(next.id);
      });
      root.addEventListener("close", () => {
        if (origin?.isConnected) origin.focus({ preventScroll: true });
        selected = ""; captionValue = undefined; triggerValue = undefined;
        image.removeAttribute("src");
        find("strip").replaceChildren(); thumbnails.clear();
      });
      return {
        refresh,
        close: () => root.open && root.close(),
        open(key, source) {
          selected = key; origin = source || document.activeElement;
          captionValue = undefined; triggerValue = undefined;
          if (!root.open) root.showModal();
          refresh();
        },
      };
    },
  };
})();
