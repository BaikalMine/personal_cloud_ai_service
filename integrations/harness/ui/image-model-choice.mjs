export function imageModelChoices(models) {
  if (!Array.isArray(models) || !models.length || models.some(model => typeof model.name !== 'string' || !model.name)) throw new Error('No installed image models are available');
  const choices = models.map(model => {
    const filename = model.name.replaceAll('\\', '/').split('/').at(-1);
    const title = filename.replace(/\.(safetensors|gguf)$/i, '').replace(/_\d{6,}$/, '')
      .replace(/_/g, ' ').replace(/([a-z0-9])([A-Z])/g, '$1 $2');
    return { name: model.name, label: title, description: filename };
  });
  return choices.map((choice, index) => ({ ...choice, label: choices.filter(other => other.label === choice.label).length > 1 ? `${choice.label} (${index + 1})` : choice.label }));
}

// A selection belongs to one human task. It is never inferred from LLM arguments.
export function createImageModelChoice({ catalog, ask, onWaiting = () => {}, questionID = 'krea_ultra_model', family = 'Krea2' }) {
  const selected = new WeakMap(), pending = new WeakMap(), epochs = new WeakMap();
  async function resolve(exec, preferred, force = false) {
    if (!exec.agent?.session) throw new Error('Image model selection requires an agent Session');
    const session = exec.agent.session;
    const epoch = epochs.get(session) || 0;
    if (pending.has(session)) {
      if (await pending.get(session) === undefined) return undefined;
      return resolve(exec, preferred, force);
    }
    const models = imageModelChoices((await catalog(exec.signal)).models);
    exec.signal?.throwIfAborted();
    if (pending.has(session)) {
      if (await pending.get(session) === undefined) return undefined;
      return resolve(exec, preferred, force);
    }
    const current = selected.get(session);
    if (!force && current && (!preferred || preferred === current) && models.some(model => model.name === current)) return current;
    selected.delete(session);
    const choices = [...models].sort((a, b) => Number(b.name === preferred) - Number(a.name === preferred));
    const decision = (async () => {
      onWaiting(exec, true, exec.callId);
      let result;
      try { result = await ask({ agent: exec.agent, signal: exec.signal, ...(exec.callId ? { wait: { callId: exec.callId, timed: false } } : {}), questions: [{
        id: questionID, header: 'Модель изображения', question: `Какую модель ${family} использовать для этого задания?`,
        options: choices.map(({ label, description }) => ({ label, description })), multiSelect: false,
      }] }); }
      finally { onWaiting(exec, false, exec.callId); }
      exec.signal?.throwIfAborted();
      if ((epochs.get(session) || 0) !== epoch) throw new Error('The image-model choice belongs to a finished task');
      const answer = result.answers?.find(item => item.id === questionID);
      if (!answer || (!answer.selected?.length && !answer.custom?.trim())) return undefined;
      if (answer.selected?.length > 1 || answer.selected?.length && answer.custom?.trim()) throw new Error('Choose one installed image model');
      const value = answer.selected?.[0] ?? answer.custom.trim();
      const chosen = models.find(model => value === model.label || value === model.name || value === model.description);
      if (!chosen) throw new Error('The selected image model is not in the installed catalog');
      selected.set(session, chosen.name);
      return chosen.name;
    })();
    pending.set(session, decision);
    try { return await decision; }
    finally { if (pending.get(session) === decision) pending.delete(session); }
  }
  return { resolve, clear: session => { selected.delete(session); epochs.set(session, (epochs.get(session) || 0) + 1); } };
}
