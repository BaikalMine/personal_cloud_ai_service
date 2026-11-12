const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const source = fs.readFileSync(path.join(__dirname, "../../static/account-pages.js"), "utf8");

function setup({ sessions = false, disabled = false, missing = false } = {}) {
  const element = (props = {}) => Object.assign({ attrs: {}, events: {}, hidden: true, dataset: {}, textContent: "",
    setAttribute(key, value) { this.attrs[key] = value; }, getAttribute(key) { return this.attrs[key]; }, hasAttribute(key) { return key in this.attrs; }, removeAttribute(key) { delete this.attrs[key]; },
    addEventListener(key, fn) { this.events[key] = fn; }, replaceChildren(child) { this.child = child; }
  }, props);
  const checkboxLabel = element(), checkbox = element({ required: true, checked: true, closest: () => checkboxLabel });
  const button = element({ disabled, attrs: sessions ? { "data-confirm": "Confirm" } : {} });
  const otherButton = element({ disabled: true }), progress = element(), preference = element();
  const form = element({ attrs: sessions ? { "data-account-confirm": "" } : {}, querySelectorAll: () => [button], querySelector(selector) { return selector === '[name="confirm_revoke"]' ? (sessions ? checkbox : null) : progress; } });
  const otherForm = element({ querySelectorAll: () => [otherButton], querySelector: () => null });
  const password = element({ type: "password", value: "secret-for-test-only" });
  const toggle = element({ dataset: { accountPasswordToggle: "password" }, attrs: { "aria-label": "\u041f\u043e\u043a\u0430\u0437\u0430\u0442\u044c password" } });
  const page = element({ dataset: { accountPage: sessions ? "sessions" : "password", accountRefresh: "/account/sessions?q=browser&offset=50" }, querySelectorAll(selector) { return selector === "[data-account-form]" ? [form, otherForm] : selector === "[data-account-script-only]" ? [preference] : [toggle]; } });
  const listeners = [], windowEvents = {}, timers = [], navigations = [];
  vm.runInNewContext(source, { Map, setTimeout: fn => timers.push(fn),
    document: { querySelector: () => missing ? null : page, getElementById: () => password, createElement: () => element(), addEventListener(name, fn, capture) { listeners.push({ name, fn, capture }); } },
    window: { location: { replace: target => navigations.push(target) }, addEventListener(name, fn) { windowEvents[name] = fn; } }
  });
  function submit({ prevented = false, target = form, submitter = button } = {}) {
    const event = { target, submitter, defaultPrevented: prevented, stopped: false, preventDefault() { this.defaultPrevented = true; }, stopImmediatePropagation() { this.stopped = true; } };
    for (const listener of listeners.filter(l => l.name === "submit").sort((a,b) => Number(!!b.capture) - Number(!!a.capture))) { listener.fn(event); if (event.stopped) break; }
    return event;
  }
  return { form, button, otherButton, progress, password, toggle, preference, checkbox, checkboxLabel, navigations, submit, flush: () => timers.splice(0).forEach(fn => fn()), restore: persisted => windowEvents.pageshow?.({ persisted }) };
}

test("account controller scopes itself to account pages and leaves other forms alone", () => {
  const missing = setup({ missing: true }); assert.equal(missing.toggle.hidden, true);
  const f = setup(); assert.equal(f.preference.hidden, false); assert.equal(f.submit({ target: {} }).defaultPrevented, false); assert.equal(f.form.attrs["aria-busy"], undefined);
});
test("account native pending state blocks a second write across forms", () => {
  const f = setup(); f.submit({ prevented: true }); assert.equal(f.form.attrs["aria-busy"], undefined);
  assert.equal(f.submit().defaultPrevented, false); assert.equal(f.button.disabled, false); f.flush(); assert.equal(f.button.disabled, true);
  assert.equal(f.submit().defaultPrevented, true); assert.ok(f.progress.textContent); f.restore(false); assert.equal(f.button.disabled, true);
  f.restore(true); assert.equal(f.button.disabled, false); assert.equal(f.otherButton.disabled, true); assert.equal(f.progress.textContent, "");
});
test("account restore preserves server-disabled controls", () => {
  const f = setup({ disabled: true }); f.restore(true); assert.equal(f.button.disabled, true);
});
test("account password visibility remasks on history restore without storing or erasing input", () => {
  const f = setup(); assert.equal(f.toggle.hidden, false); f.toggle.events.click(); assert.equal(f.password.type, "text"); assert.equal(f.toggle.child.attrs["data-lucide"], "eye-off");
  f.restore(true); assert.equal(f.password.type, "password"); assert.equal(f.password.value, "secret-for-test-only"); assert.equal(f.toggle.attrs["aria-pressed"], "false");
  for (const forbidden of [/localStorage/, /sessionStorage/, /indexedDB/, /fetch\(/, /XMLHttpRequest/, /\.submit\(/, /requestSubmit\(/]) assert.doesNotMatch(source, forbidden);
});
test("session confirmation is set only after shared confirmation accepted the native submit", () => {
  const f = setup({ sessions: true }); assert.equal(f.checkbox.required, false); assert.equal(f.checkbox.checked, false); assert.equal(f.checkboxLabel.hidden, true);
  assert.equal(f.submit({ prevented: true }).defaultPrevented, true); assert.equal(f.checkbox.checked, false);
  assert.equal(f.submit({ submitter: null }).defaultPrevented, true); assert.equal(f.checkbox.checked, false);
  assert.equal(f.submit().defaultPrevented, false); assert.equal(f.checkbox.checked, true); assert.equal(f.submit().defaultPrevented, true);
});
test("session history restore navigates to the canonical filtered GET and never replays revoke", () => {
  const f = setup({ sessions: true }); f.submit(); f.flush(); f.restore(false); assert.deepEqual(f.navigations, []);
  f.restore(true); assert.deepEqual(f.navigations, ["/account/sessions?q=browser&offset=50"]); assert.equal(f.button.disabled, true);
});
