const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../../static/theme.js'), 'utf8');
const key = 'ai_gateway_theme';

function run({ cookie = '', stored = null, server = 'system', dark = false, cookieBlocked = false, storageBlocked = false } = {}) {
  const events = {};
  const root = { dataset: { themePreference: server } };
  const picker = { hidden: true };
  const control = { value: '', addEventListener(name, callback) { events.control = callback; } };
  const system = { matches: dark, addEventListener(name, callback) { events.system = callback; } };
  const storage = { getItem: () => stored, setItem: (name, value) => { stored = value; } };
  const document = {
    documentElement: root,
    querySelectorAll: selector => selector === '[data-theme-picker]' ? [picker] : [control],
    addEventListener(name, callback) { events[name] = callback; },
  };
  Object.defineProperty(document, 'cookie', {
    get() { if (cookieBlocked) throw new Error('cookies blocked'); return cookie; },
    set(value) { if (cookieBlocked) throw new Error('cookies blocked'); cookie = value; },
  });
  const window = { matchMedia: () => system, addEventListener(name, callback) { events[name] = callback; } };
  Object.defineProperty(window, 'localStorage', { get() { if (storageBlocked) throw new Error('storage blocked'); return storage; } });
  const context = { window, document, location: { protocol: 'https:' } };
  Object.defineProperty(context, 'localStorage', { get() { return window.localStorage; } });
  vm.runInNewContext(source, context);
  return { root, picker, control, system, storage, events, cookie: () => cookie, stored: () => stored };
}

test('theme resolves before DOM ready and the cookie wins over local storage', () => {
  const state = run({ cookie: `${key}=dark`, stored: 'light' });
  assert.equal(state.root.dataset.theme, 'dark');
  assert.equal(state.picker.hidden, true);
  state.events.DOMContentLoaded();
  assert.equal(state.picker.hidden, false);
  assert.equal(state.control.value, 'dark');
});

test('theme uses local storage when cookies are blocked or malformed', () => {
  for (const options of [{ cookieBlocked: true }, { cookie: `${key}=%xx` }, { cookie: `${key}=invalid` }]) {
    assert.equal(run({ ...options, stored: 'dark' }).root.dataset.theme, 'dark');
  }
});

test('theme retains the server preference when all client storage is blocked', () => {
  const state = run({ server: 'light', dark: true, cookieBlocked: true, storageBlocked: true });
  assert.equal(state.root.dataset.theme, 'light');
  state.events.DOMContentLoaded();
  state.control.value = 'dark';
  assert.doesNotThrow(() => state.events.control());
  assert.equal(state.root.dataset.theme, 'dark');
  assert.doesNotThrow(() => state.events.storage({ key, newValue: 'light' }));
});

test('theme follows OS changes only in system mode and persists explicit choices', () => {
  const state = run();
  assert.equal(state.root.dataset.theme, 'light');
  state.system.matches = true;
  state.events.system();
  assert.equal(state.root.dataset.theme, 'dark');
  state.events.DOMContentLoaded();
  state.control.value = 'light';
  state.events.control();
  assert.equal(state.stored(), 'light');
  assert.match(state.cookie(), /ai_gateway_theme=light; Path=\/; Max-Age=31536000; SameSite=Lax; Secure/);
  state.events.system();
  assert.equal(state.root.dataset.theme, 'light');
});

test('theme ignores unrelated storage and synchronizes removal into the cookie', () => {
  const state = run({ cookie: `${key}=light`, dark: true });
  state.events.storage({ key: 'different', newValue: 'dark', storageArea: state.storage });
  state.events.storage({ key, newValue: 'dark', storageArea: {} });
  assert.equal(state.root.dataset.theme, 'light');
  state.events.storage({ key, newValue: null, storageArea: state.storage });
  assert.equal(state.root.dataset.theme, 'dark');
  assert.equal(state.root.dataset.themePreference, 'system');
  assert.match(state.cookie(), /ai_gateway_theme=system;/);
});
