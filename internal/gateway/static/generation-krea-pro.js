(function (root) {
  const bind = ({ form, onChange = () => {} }) => {
    const target = form.querySelector('[data-krea-pro-controls]');
    const hidden = form.elements.krea_pro_settings;
    if (!target || !hidden) return { sync() {} };
    let options = [], previous = null, ready = false;
    const upscaler = form.elements.krea_pro_upscaler;
    const vosrScale = form.elements.krea_pro_upscale;
    const relayScale = form.elements.krea_pro_relay_scale;
    const read = () => { try { return JSON.parse(hidden.value || '{}'); } catch (_) { return {}; } };
    const controls = new Map();
    const write = (id, name, value) => {
      const state = read();
      if (name === 'enabled') { state.enabled ||= {}; state.enabled[id] = value; }
      else { state.nodes ||= {}; state.nodes[id] ||= {}; state.nodes[id][name] = value; }
      hidden.value = JSON.stringify(state);
      hidden.dispatchEvent(new Event('input', { bubbles: true }));
      onChange();
    };
    const sync = () => {
      const state = read();
      let kind = upscaler?.value || 'vosr';
      if (kind === 'vosr') {
        if (vosrScale?.value === '1') kind = 'none';
        else if (state.enabled?.['4150'] === true) kind = 'relay';
        else if (state.enabled?.['4046'] === false) kind = 'none';
      }
      if (upscaler) { upscaler.value = kind; upscaler.disabled = hidden.disabled; }
      for (const [control, active] of [[vosrScale, kind === 'vosr'], [relayScale, kind === 'relay']]) {
        if (!control) continue;
        control.disabled = hidden.disabled || !active;
        control.closest('label').hidden = !active;
      }
      const current = JSON.stringify([hidden.value, hidden.disabled, kind]);
      if (!ready || current === previous) return;
      previous = current;
      for (const option of options) {
        const entry = controls.get(option.id);
        const enabled = option.id === '4046' ? kind === 'vosr' : option.id === '4150' ? kind === 'relay' : state.enabled?.[option.id] ?? option.enabled;
        entry.section.hidden = ['4046', '4150'].includes(option.id) && !enabled;
        entry.grid.hidden = option.toggle && !enabled;
        if (entry.toggle) entry.toggle.checked = enabled;
        for (const field of option.fields) {
          const input = entry.fields.get(field.name);
          if (!input) continue;
          const value = state.nodes?.[option.id]?.[field.name] ?? field.default;
          if (input.type === 'checkbox') input.checked = Boolean(value);
          else if (document.activeElement !== input) input.value = String(value ?? '');
          input.disabled = hidden.disabled || (option.toggle && !enabled);
        }
      }
    };
    const build = () => {
      target.replaceChildren();
      const groups = new Map();
      for (const option of options) {
        if (!groups.has(option.section)) {
          const details = document.createElement('details'); details.className = 'ui-disclosure';
          const summary = document.createElement('summary'); summary.textContent = option.section;
          details.append(summary); target.append(details); groups.set(option.section, details);
        }
        const section = document.createElement('section'); section.className = 'workflow-settings-section';
        const title = document.createElement('div'); title.className = 'workflow-settings-heading';
        const strong = document.createElement('strong'); strong.textContent = option.label; title.append(strong);
        const entry = { fields: new Map(), toggle: null, grid: null, section };
        if (option.toggle && !['4046', '4150'].includes(option.id)) {
          const label = document.createElement('label'); label.className = 'ui-choice';
          const toggle = document.createElement('input'); toggle.type = 'checkbox';
          toggle.setAttribute('aria-label', option.label); toggle.dataset.kreaProToggle = option.id;
          const text = document.createElement('span'); text.textContent = 'Включить'; label.append(toggle, text); title.append(label);
          entry.toggle = toggle;
          toggle.addEventListener('change', () => { write(option.id, 'enabled', toggle.checked); previous = null; sync(); });
        }
        section.append(title);
        const grid = document.createElement('div'); grid.className = 'ui-field-grid krea-pro-field-grid'; entry.grid = grid;
        for (const field of option.fields) {
          if (option.id === '4150' && ['source', 'upscale'].includes(field.name)) continue;
          const label = document.createElement('label'); label.className = field.type === 'BOOLEAN' ? 'ui-choice' : 'ui-field';
          const text = document.createElement('span'); text.className = 'ui-field-label'; text.textContent = field.label;
          let input;
          if (field.type === 'COMBO') {
            input = document.createElement('select');
            for (const value of field.choices) { const item = document.createElement('option'); item.value = value; item.textContent = value; input.append(item); }
          } else {
            input = document.createElement(field.type === 'STRING' ? 'textarea' : 'input');
            if (field.type === 'STRING') { input.rows = 3; input.maxLength = 8000; }
            else if (field.type === 'BOOLEAN') input.type = 'checkbox';
            else { input.type = 'number';
              // ComfyUI's widget step is an editor increment, not a value constraint.
              input.step = field.type === 'INT' ? '1' : 'any';
              input.inputMode = field.type === 'INT' ? 'numeric' : 'decimal'; input.required = true;
              if (field.min !== undefined) input.min = String(field.min); if (field.max !== undefined) input.max = String(field.max); }
          }
          input.dataset.kreaProNode = option.id; input.dataset.kreaProField = field.name;
          label.append(text, input); grid.append(label); entry.fields.set(field.name, input);
          input.addEventListener('change', () => {
            if (!input.checkValidity()) { input.reportValidity(); return; }
            const value = field.type === 'BOOLEAN' ? input.checked : ['INT','FLOAT'].includes(field.type) ? Number(input.value) : input.value;
            write(option.id, field.name, value);
          });
        }
        section.append(grid); groups.get(option.section).append(section); controls.set(option.id, entry);
      }
      ready = true; previous = null; sync();
    };
    const selectUpscaler = () => {
      const state = read(), kind = upscaler?.value || 'vosr';
      if (kind === 'vosr' && vosrScale?.value === '1') vosrScale.value = '2';
      state.enabled ||= {};
      state.enabled['4046'] = kind === 'vosr';
      // Older option catalogs do not know Relay, so do not serialize its flag.
      if (options.some(option => option.id === '4150')) state.enabled['4150'] = kind === 'relay';
      hidden.value = JSON.stringify(state);
      previous = null; sync();
      hidden.dispatchEvent(new Event('input', { bubbles: true }));
      onChange();
    };
    upscaler?.addEventListener('change', selectUpscaler);
    relayScale?.addEventListener('change', () => { previous = null; sync(); onChange(); });
    vosrScale?.addEventListener('change', () => {
      if (upscaler) upscaler.value = vosrScale.value === '1' ? 'none' : 'vosr';
      selectUpscaler();
    });
    sync();
    fetch('/static/krea-pro-options.json', { credentials: 'same-origin' }).then(response => {
      if (!response.ok) throw new Error('catalog'); return response.json();
    }).then(data => { options = data; build(); }).catch(() => {
      const message = document.createElement('p'); message.className = 'error'; message.textContent = 'Не удалось загрузить дополнительные настройки Ultra.'; target.replaceChildren(message);
    });
    return { sync() { previous = null; sync(); } };
  };
  root.AIGatewayGeneration ||= {}; root.AIGatewayGeneration.kreaPro = { bind };
})(window);
