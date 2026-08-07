(() => {
  const root = document.querySelector("[data-comfy-generation]");
  if (!root) return;

  const form = document.getElementById("generation-form");
  const templateID = document.getElementById("template-id");
  const inputImage = document.getElementById("input-image");
  const fileInput = document.getElementById("source-image");
  const preview = document.getElementById("upload-preview");
  const previewImage = document.getElementById("upload-preview-image");
  const uploadName = document.getElementById("upload-name");
  const uploadState = document.getElementById("upload-state");
  const uploadNext = document.getElementById("upload-next");
  const promptsNext = document.getElementById("prompts-next");
  const positive = document.getElementById("positive-prompt");
  const checkpoint = document.getElementById("checkpoint");
  const result = document.getElementById("generation-result");
  const resultTitle = document.getElementById("generation-result-title");
  const resultStatus = document.getElementById("generation-status");
  const outputGrid = document.getElementById("generation-output-grid");
  const panels = [...root.querySelectorAll("[data-step]")];
  const progress = [...root.querySelectorAll("[data-progress]")];
  let currentStep = 1;
  let requiresImage = false;
  let uploadInFlight = false;

  const showStep = (step) => {
    currentStep = step;
    panels.forEach((panel) => panel.classList.toggle("is-visible", Number(panel.dataset.step) === step));
    progress.forEach((item) => {
      const number = Number(item.dataset.progress);
      item.classList.toggle("is-active", number === step);
      item.classList.toggle("is-complete", number < step);
    });
    window.scrollTo({ top: 0, behavior: "smooth" });
  };

  const selectedChoice = () => root.querySelector(".workflow-choice.is-selected");
  const chooseWorkflow = (button) => {
    root.querySelectorAll(".workflow-choice").forEach((item) => item.classList.remove("is-selected"));
    button.classList.add("is-selected");
    templateID.value = button.dataset.workflowId;
    requiresImage = button.dataset.requiresImage === "true";
    root.querySelectorAll(".generation-next").forEach((item) => {
      if (item.id !== "upload-next" && item.id !== "prompts-next") item.disabled = false;
    });
    document.querySelectorAll(".denoise-field").forEach((item) => item.classList.toggle("is-hidden", !requiresImage));
  };

  root.querySelectorAll(".workflow-choice").forEach((button) => {
    button.addEventListener("click", () => chooseWorkflow(button));
  });

  root.querySelectorAll(".generation-back").forEach((button) => {
    button.addEventListener("click", () => showStep(Math.max(1, currentStep - 1)));
  });
  root.querySelectorAll("[data-step='1'] .generation-next").forEach((button) => {
    button.addEventListener("click", () => showStep(requiresImage ? 2 : 3));
  });

  fileInput?.addEventListener("change", () => {
    const file = fileInput.files?.[0];
    inputImage.value = "";
    uploadNext.disabled = true;
    if (!file) {
      preview.hidden = true;
      return;
    }
    preview.hidden = false;
    previewImage.src = URL.createObjectURL(file);
    uploadName.textContent = file.name;
    uploadState.textContent = "Готово к загрузке";
    uploadNext.disabled = false;
  });

  uploadNext?.addEventListener("click", async () => {
    const file = fileInput?.files?.[0];
    if (!file || uploadInFlight) return;
    uploadInFlight = true;
    uploadNext.disabled = true;
    uploadNext.classList.add("is-loading");
    uploadState.textContent = "Загружаем фото...";
    const body = new FormData();
    body.append("image", file, file.name);
    body.append("type", "input");
    body.append("overwrite", "true");
    try {
      const response = await fetch("/comfyui/upload/image", { method: "POST", body, credentials: "same-origin" });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok || !payload.name) throw new Error(payload.error || "ComfyUI не принял фото");
      inputImage.value = [payload.subfolder, payload.name].filter(Boolean).join("/");
      uploadState.textContent = "Фото загружено в вашу сессию";
      showStep(3);
    } catch (error) {
      uploadState.textContent = error.message || "Не удалось загрузить фото";
      uploadNext.disabled = false;
    } finally {
      uploadInFlight = false;
      uploadNext.classList.remove("is-loading");
    }
  });

  positive?.addEventListener("input", () => { promptsNext.disabled = !positive.value.trim(); });
  promptsNext?.addEventListener("click", () => {
    if (!positive.value.trim()) return;
    showStep(4);
    checkpoint?.focus({ preventScroll: true });
  });

  const renderOutputs = (outputs) => {
    outputGrid.replaceChildren();
    outputs.forEach((output) => {
      const media = output.media_type === "video" ? document.createElement("video") : document.createElement("img");
      media.src = output.url;
      media.controls = output.media_type === "video";
      media.loading = "lazy";
      media.alt = output.filename;
      const card = document.createElement("figure");
      card.className = "generation-output";
      const caption = document.createElement("figcaption");
      caption.textContent = output.filename;
      card.append(media, caption);
      outputGrid.append(card);
    });
  };

  const poll = async (promptID) => {
    const deadline = Date.now() + 10 * 60 * 1000;
    while (Date.now() < deadline) {
      const response = await fetch(`/generate/status?prompt_id=${encodeURIComponent(promptID)}`, { credentials: "same-origin" });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(payload.error || "Не удалось получить статус");
      resultStatus.textContent = payload.message || "Проверяем состояние...";
      if (payload.state === "completed") {
        resultTitle.textContent = "Готово";
        renderOutputs(payload.outputs || []);
        return;
      }
      if (payload.state === "error") throw new Error(payload.message || "ComfyUI завершил генерацию с ошибкой");
      await new Promise((resolve) => window.setTimeout(resolve, 2000));
    }
    throw new Error("Генерация выполняется слишком долго. Проверьте результат позже в ComfyUI.");
  };

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!selectedChoice() || !positive.value.trim() || !checkpoint.value.trim()) return;
    const submit = document.getElementById("generation-submit");
    submit.disabled = true;
    submit.classList.add("is-loading");
    result.hidden = false;
    resultTitle.textContent = "Генерация выполняется";
    resultStatus.textContent = "Ставим workflow в очередь ComfyUI...";
    outputGrid.replaceChildren();
    try {
      const response = await fetch("/generate/run", { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" }, body: new URLSearchParams(new FormData(form)), credentials: "same-origin" });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(payload.error || "Не удалось запустить генерацию");
      await poll(payload.prompt_id);
    } catch (error) {
      resultTitle.textContent = "Не удалось выполнить генерацию";
      resultStatus.textContent = error.message || "Неизвестная ошибка";
      result.classList.add("has-error");
    } finally {
      submit.disabled = false;
      submit.classList.remove("is-loading");
    }
  });

  const initialID = root.dataset.selectedWorkflow || "";
  const initial = initialID ? root.querySelector(`[data-workflow-id="${CSS.escape(initialID)}"]`) : null;
  if (initial) chooseWorkflow(initial);
})();
