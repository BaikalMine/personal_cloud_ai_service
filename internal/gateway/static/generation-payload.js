(function bootstrapGenerationPayload(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.payload = api;
})(typeof window !== "undefined" ? window : null, function generationPayloadFactory() {
  const imageFields = ["input_image", "input_image_2", "input_image_3", "input_image_4", "input_image_5", "input_image_6", "input_image_7", "input_image_8", "input_image_9", "input_image_10"];
  const isLoraSelection = name => /^lora_\d+$/.test(name);
  const isLoraField = name => name === "loras_configured" || /^lora_(?:model_strength_|clip_strength_)?\d+$/.test(name);
  const isImageField = name => imageFields.includes(name);
  const isDraftMediaField = name => isImageField(name) || /^image_role_(?:10|[1-9])$/.test(name) || ["input_audio", "input_video"].includes(name);
  const bool = value => String(Boolean(value));

  /**
   * @typedef {Object} PayloadState
   * @property {{scenarioID: string, workflowID: string}} selection
   * @property {Object} assistant Reducer state plus enabled/template/think/jobID/draft/corrections.
   * @property {Object} media Images contain slot/value/active/role/name/reference;
   * audio/video contain value/active/name, video also referenceAudio. Assets is
   * an iterable of [field, asset]; pending contains field and source objects.
   * @property {{quality: string, step: number, advanced: boolean, parentJobID: string}} editor
   */
  /**
   * One adapter for successful form controls, draft controls and saved values.
   * Model-specific fields are discovered by name, not maintained in allowlists.
   * Disabled controls are excluded from launch, but retained in a draft. LoRA
   * names duplicated across families resolve to the selected family first.
   * @param {Object} options
   * @param {HTMLFormElement} options.form
   * @param {HTMLElement} options.root
   * @param {() => string} options.loraKind
   * @param {() => FormData} [options.formData] Testable native serialization boundary.
   * @param {(node: *) => boolean} [options.isControl]
   */
  const createAdapter = ({ form, root, loraKind = () => "", formData = () => new FormData(form), isControl = node => node?.nodeType === 1 }) => {
    const controls = name => {
      const named = form.elements.namedItem(name);
      if (!named) return [];
      const candidates = isControl(named) ? [named] : [...named].filter(isControl);
      if (isLoraField(name)) {
        const kind = loraKind();
        const scoped = candidates.filter(control => control.closest?.("[data-lora-slots]")?.dataset.loraSlots === kind);
        if (scoped.length) return scoped;
      }
      const enabled = candidates.filter(control => !control.disabled);
      return enabled.length ? enabled : candidates;
    };
    const set = (name, value) => {
      const candidates = controls(name);
      if (!candidates.length) return;
      const text = String(value);
      if (candidates.every(control => control.type === "radio")) {
        const option = candidates.find(control => control.value === text);
        if (option) option.checked = true;
        return;
      }
      const control = candidates[0];
      if (["file", "submit", "button"].includes(control.type)) return;
      if (control.type === "checkbox") control.checked = ["true", "on", "1"].includes(text);
      else control.value = text;
    };
    /** Apply ordinary model parameters after the caller selected its workflow. */
    const apply = (values, { afterLora = () => {} } = {}) => {
      const entries = Object.entries(values).filter(([name]) => !["template_id", "generation_workflow"].includes(name) && !isImageField(name));
      entries.filter(([name]) => isLoraSelection(name))
        .sort(([left], [right]) => Number(left.slice(5)) - Number(right.slice(5)))
        .forEach(([name, value]) => { set(name, value); afterLora(); });
      entries.forEach(([name, value]) => { if (!isLoraSelection(name)) set(name, value); });
    };
    /** Validate references to available choices before any form mutation. */
    const resolveSelection = values => {
      if (!values || typeof values !== "object" || Array.isArray(values)) throw new Error("Некорректные сохранённые параметры.");
      const scenario = values.template_id ? [...root.querySelectorAll(".scenario-choice")].find(choice => choice.dataset.workflowId === values.template_id) : null;
      const workflow = values.generation_workflow ? [...root.querySelectorAll(".generation-workflow-choice")].find(choice => choice.dataset.presetId === values.generation_workflow) : null;
      if ((values.template_id && (!scenario || scenario.disabled)) || (values.generation_workflow && (!workflow || workflow.disabled))) {
        throw new Error("Модель черновика сейчас недоступна. Сохранённая версия не изменена.");
      }
      if (workflow && values.template_id && workflow.dataset.templateId !== values.template_id) {
        throw new Error("Сохранённый workflow не соответствует выбранному типу генерации. Параметры не изменены.");
      }
      const model = controls("model")[0];
      if (values.model && ![...(model?.options || [])].some(option => option.value === values.model && option.dataset.available === "true" && (!workflow || option.dataset.family === workflow.dataset.family) && (workflow?.dataset.presetId !== "aihub-minimax-h3" || option.value === workflow.dataset.defaultModelId))) {
        throw new Error("Модель черновика сейчас недоступна. Сохранённая версия не изменена.");
      }
      const kind = { minimax_h3: "minimax", flux2: "flux", krea2: "krea", moss: "moss" }[workflow?.dataset.family];
      if (kind) {
        const choices = [...root.querySelectorAll(`[data-lora-slots="${kind}"] select option`)];
        if (Object.entries(values).some(([name, value]) => isLoraSelection(name) && value && !choices.some(option => option.value === value))) {
          throw new Error("Одна из LoRA черновика сейчас недоступна. Сохранённая версия не изменена.");
        }
      }
      return { scenario, workflow };
    };
    const successfulControls = () => {
      const body = new URLSearchParams([...formData()].filter(([, value]) => typeof value === "string"));
      const numbers = new Set([...form.querySelectorAll('input[type="number"], input[data-localized-decimal]')].map(field => field.name));
      for (const [name, value] of [...body]) if (numbers.has(name)) body.set(name, value.replaceAll(",", "."));
      form.querySelectorAll('input[type="checkbox"][name]:not(:disabled)').forEach(control => body.set(control.name, bool(control.checked)));
      return body;
    };
    /** @param {PayloadState} state @returns {URLSearchParams} */
    const launch = ({ selection = {}, assistant = {}, media = {}, editor = {} } = {}) => {
      const body = successfulControls();
      body.set("template_id", selection.scenarioID || "");
      body.set("generation_workflow", selection.workflowID || "");
      body.set("assistant_requested", bool(assistant.original || assistant.correlationID));
      body.set("assistant_applied", bool(["applied", "edited_after_apply"].includes(assistant.action)));
      body.set("assistant_action", assistant.action || "");
      body.set("assistant_template_used", assistant.original ? assistant.template || "" : "");
      body.set("assistant_think_used", bool(assistant.original && assistant.think));
      body.set("assistant_original_prompt", assistant.original || "");
      body.set("assistant_suggestion", assistant.suggestion || "");
      body.set("assistant_original_negative", assistant.originalNegative || "");
      body.set("assistant_original_audio", assistant.originalAudio || "");
      body.set("assistant_negative_suggestion", assistant.negativeSuggestion || "");
      body.set("assistant_audio_suggestion", assistant.audioSuggestion || "");
      if (assistant.correlationID) body.set("correlation_id", assistant.correlationID);
      imageFields.forEach((field, index) => {
        const image = media.images?.find(item => item.slot === index + 1);
        body.set(field, image?.active ? image.value || "" : "");
        for (const name of ["role", "source", "source_id", "source_name"]) body.delete(`image_${name}_${index + 1}`);
      });
      (media.images || []).forEach(image => {
        const reference = image.active && image.reference;
        if (!reference || !imageFields[image.slot - 1]) return;
        body.set(`image_role_${image.slot}`, reference.role || "details");
        body.set(`image_source_${image.slot}`, reference.source || "unknown");
        if (reference.sourceID) body.set(`image_source_id_${image.slot}`, reference.sourceID);
        if (reference.sourceName) body.set(`image_source_name_${image.slot}`, reference.sourceName);
      });
      body.set("input_audio", media.audio?.active ? media.audio.value || "" : "");
      body.set("input_video", media.video?.active ? media.video.value || "" : "");
      body.set("video_reference_audio", bool(media.video?.active && media.video.value && media.video.referenceAudio));
      if (editor.parentJobID) body.set("parent_job_id", editor.parentJobID);
      if (selection.workflowID !== "aihub-minimax-h3") imageFields.slice(4).forEach(field => body.delete(field));
      if (!body.get("collection_id")) body.delete("collection_id");
      return body;
    };
    /** @param {PayloadState} state @returns {URLSearchParams} */
    const draft = (state = {}) => {
      const { assistant = {}, media = {}, editor = {} } = state;
      const body = launch(state);
      (media.images || []).forEach(image => {
        const field = imageFields[image.slot - 1];
        if (!field) return;
        body.set(field, image.value || "");
        body.set(`image_role_${image.slot}`, image.role ?? "details");
      });
      body.set("input_audio", media.audio?.value || "");
      body.set("input_video", media.video?.value || "");
      const names = new Set([...form.elements].map(control => control.name).filter(Boolean));
      names.forEach(name => {
        if (isDraftMediaField(name)) return;
        const control = controls(name).find(item => item.type !== "radio" || item.checked);
        if (!control || ["file", "submit", "button"].includes(control.type)) return;
        body.set(name, control.type === "checkbox" ? bool(control.checked) : control.value);
      });
      body.set("assistant_enabled", bool(assistant.enabled));
      body.set("assistant_job_id", assistant.jobID || "");
      if (assistant.modelID) body.set("assistant_model", assistant.modelID);
      body.set("assistant_template_used", assistant.template || "");
      body.set("assistant_think_used", bool(assistant.think));
      body.set("assistant_draft", assistant.draft || "");
      body.set("assistant_negative_draft", assistant.negativeDraft || "");
      body.set("assistant_references", JSON.stringify(assistant.references || []));
      body.set("assistant_stale", bool(assistant.stale));
      body.set("assistant_draft_edited", bool(assistant.draftEdited));
      body.set("assistant_corrections", JSON.stringify(assistant.corrections || {}));
      body.set("quality_preset", editor.quality || "");
      body.set("draft_step", String(editor.step || 1));
      body.set("draft_advanced", bool(editor.advanced));
      (media.images || []).forEach(image => {
        const field = imageFields[image.slot - 1];
        if (field && image.name) body.set(`draft_name_${field}`, image.name);
      });
      body.set("draft_name_input_audio", media.audio?.name || "");
      body.set("draft_name_input_video", media.video?.name || "");
      for (const [field, asset] of media.assets || []) {
        if (asset.id) body.set(`draft_asset_${field}`, asset.id);
        else if (!asset.available) body.set(`draft_pending_${field}`, asset.name || "Материал");
        body.set(`draft_name_${field}`, asset.name || "");
      }
      (media.pending || []).forEach(item => {
        body.delete(`draft_asset_${item.field}`);
        body.set(`draft_pending_${item.field}`, item.source.name || item.source.filename || "Материал");
      });
      if (!body.get("collection_id")) body.delete("collection_id");
      return body;
    };
    return { controls, set, apply, resolveSelection, launch, draft };
  };
  return { createAdapter };
});
