(() => {
  const page = document.querySelector('[data-lora-training-page]');
  if (!page) return;
  const steps = ['material', 'captions', 'training', 'result'];
  const labels = ['Материал', 'Подписи', 'Обучение', 'Результат'];
  const buttons = [...page.querySelectorAll('[data-training-step]')];
  const previous = page.querySelector('[data-training-previous]');
  const next = page.querySelector('[data-training-next]');
  let current = '';
  const show = (stage, focus = false) => {
    if (!steps.includes(stage)) stage = 'material';
    current = stage;
    page.dataset.trainingStage = stage;
    page.querySelectorAll('[data-training-panel]').forEach(panel => { panel.hidden = panel.dataset.trainingPanel !== stage; });
    buttons.forEach(button => {
      if (button.dataset.trainingStep === stage) button.setAttribute('aria-current', 'step');
      else button.removeAttribute('aria-current');
    });
    const index = steps.indexOf(stage);
    previous.hidden = index === 0;
    next.hidden = index >= 2;
    next.textContent = index === 0 ? 'К подписям' : 'К обучению';
    page.querySelector('.lora-section-heading h2').textContent = labels[index];
    const address = new URL(location.href);
    address.searchParams.set('stage', stage);
    history.replaceState(history.state, '', address);
    if (focus) buttons[index].focus({ preventScroll: true });
    page.dispatchEvent(new CustomEvent('lora-stage-changed', { detail: stage }));
  };
  buttons.forEach(button => button.addEventListener('click', () => show(button.dataset.trainingStep)));
  page.addEventListener('click', event => {
    const go = event.target.closest('[data-training-go]');
    if (go) show(go.dataset.trainingGo, true);
  });
  previous.addEventListener('click', () => show(steps[steps.indexOf(current) - 1], true));
  next.addEventListener('click', () => show(steps[steps.indexOf(current) + 1], true));
  const form = page.querySelector('form[data-lora-training-form]');
  form.noValidate = true;
  form.addEventListener('submit', event => {
    if (current !== 'training') { event.preventDefault(); event.stopImmediatePropagation(); return; }
    const invalid = [...form.elements].find(field => field.willValidate && !field.validity.valid);
    if (!invalid) return;
    event.preventDefault(); event.stopImmediatePropagation();
    const panel = invalid.closest('[data-training-panel]');
    if (panel) show(panel.dataset.trainingPanel);
    invalid.focus(); invalid.reportValidity();
  });
  page.addEventListener('lora-training-created', event => {
    const address = new URL(location.href);
    address.searchParams.set('run', event.detail.id);
    history.replaceState(history.state, '', address);
    show('result', true);
  });
  page.addEventListener('lora-stage-request', event => show(event.detail, true));
  page.addEventListener('lora-workspace-reset', () => {
    const address = new URL(location.href);
    address.searchParams.delete('run');
    history.replaceState(history.state, '', address);
    show('material', true);
  });
  const query = new URLSearchParams(location.search);
  show(query.get('stage') || (query.has('run') ? 'result' : query.has('image') ? 'captions' : 'material'));
})();
