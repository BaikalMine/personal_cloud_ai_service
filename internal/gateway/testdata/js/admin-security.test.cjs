const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const source = fs.readFileSync(path.join(__dirname, "../../static/admin-security.js"), "utf8");

function setup(present = true) {
  const listeners = [], timers = [], replacements = [];
  const label = { hidden: false };
  const checkbox = { required: true, checked: true, closest: () => label };
  const button = { disabled: false, hasAttribute: name => name === "data-confirm" };
  const form = { dataset: { revokeSession: "71" }, querySelector: () => checkbox, querySelectorAll: () => [button] };
  const progress = { textContent: "" }, attrs = {};
  const page = { dataset: { currentUrl: "/admin/sessions?q=a&state=all" }, querySelectorAll: () => [form], querySelector: () => progress, setAttribute: (key, value) => { attrs[key] = value; } };
  const windowListeners = {};
  vm.runInNewContext(source, {
    document: { querySelector: () => present ? page : null, addEventListener: (name, fn, capture) => listeners.push({ name, fn, capture }) },
    window: { addEventListener: (name, fn) => { windowListeners[name] = fn; }, location: { replace: value => replacements.push(value) } },
    setTimeout: fn => timers.push(fn)
  });
  const event = (prevented = false, submitter = button) => ({
    target: form, submitter, defaultPrevented: prevented, stopped: false,
    preventDefault() { this.defaultPrevented = true; }, stopImmediatePropagation() { this.stopped = true; }
  });
  const dispatch = e => {
    for (const listener of listeners.filter(l => l.capture)) listener.fn(e);
    if (!e.stopped) for (const listener of listeners.filter(l => !l.capture)) listener.fn(e);
  };
  return { checkbox, label, button, progress, attrs, listeners, timers, event, dispatch, windowListeners, replacements };
}
test("security controller leaves unrelated pages alone", () => {
  assert.equal(setup(false).listeners.length, 0);
});
test("native no-JS confirmation is replaced by shared dialog confirmation", () => {
  const s = setup();
  assert.equal(s.checkbox.required, false); assert.equal(s.checkbox.checked, false); assert.equal(s.label.hidden, true);
  s.dispatch(s.event(true));
  assert.equal(s.checkbox.checked, false); assert.equal(s.attrs["aria-busy"], undefined); assert.equal(s.timers.length, 0);
});
test("accepted submission keeps native payload and blocks further writes", () => {
  const s = setup(); const first = s.event(); s.dispatch(first);
  assert.equal(first.defaultPrevented, false); assert.equal(s.checkbox.checked, true); assert.equal(s.button.disabled, false);
  assert.equal(s.attrs["aria-busy"], "true"); assert.match(s.progress.textContent, /#71/);
  const second = s.event(); s.dispatch(second);
  assert.equal(second.defaultPrevented, true); assert.equal(second.stopped, true); assert.equal(s.timers.length, 1);
  s.timers[0](); assert.equal(s.button.disabled, true);
});
test("submission without the confirmed action is refused", () => {
  const s = setup(); const e = s.event(false, null); s.dispatch(e);
  assert.equal(e.defaultPrevented, true); assert.equal(s.checkbox.checked, false); assert.equal(s.timers.length, 0);
});
test("bfcache restoration performs a canonical GET without replaying a form", () => {
  const s = setup(); s.windowListeners.pageshow({ persisted: false }); assert.deepEqual(s.replacements, []);
  s.windowListeners.pageshow({ persisted: true }); assert.deepEqual(s.replacements, ["/admin/sessions?q=a&state=all"]);
});
