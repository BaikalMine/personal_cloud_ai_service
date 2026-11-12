const assert = require("node:assert/strict");
const test = require("node:test");
const { createFocusTrap } = require("../../static/dialog-focus.js");

function fixture() {
  const listeners = new Set();
  const documentRef = {
    activeElement: null,
    addEventListener: (_, listener) => listeners.add(listener),
    removeEventListener: (_, listener) => listeners.delete(listener),
  };
  const element = () => ({ tabIndex: 0, isConnected: true, getClientRects: () => [{}], focus() { documentRef.activeElement = this; } });
  const dialog = () => {
    const controls = [element(), element()];
    const root = { tagName: "DIALOG", open: true, hidden: false, isConnected: true, querySelectorAll: () => controls };
    return { root, controls, trap: createFocusTrap({ root, documentRef }) };
  };
  const press = key => {
    const event = { key, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {} };
    for (const listener of [...listeners]) if (listeners.has(listener)) listener(event);
  };
  return { documentRef, listeners, element, dialog, press };
}

test("only the top trap receives Tab and Escape, then the lower trap resumes", () => {
  const f = fixture(), lower = f.dialog(), upper = f.dialog();
  const closed = [];
  lower.trap.activate({ onEscape: () => { closed.push("lower"); lower.trap.deactivate(); } });
  upper.trap.activate({ onEscape: () => { closed.push("upper"); upper.root.open = false; upper.trap.deactivate(); } });
  f.press("Tab");
  assert.equal(f.documentRef.activeElement, upper.controls[1]);
  f.press("Escape");
  assert.deepEqual(closed, ["upper"]);
  assert.equal(f.documentRef.activeElement, lower.controls[0]);
  f.press("Escape");
  assert.deepEqual(closed, ["upper", "lower"]);
  assert.equal(f.listeners.size, 0);
});

test("deactivating a background trap cannot steal foreground focus", () => {
  const f = fixture(), lower = f.dialog(), upper = f.dialog();
  lower.trap.activate({ trigger: f.element() });
  upper.trap.activate();
  lower.trap.deactivate();
  assert.equal(f.documentRef.activeElement, upper.controls[0]);
  upper.trap.deactivate({ restore: false });
  assert.equal(f.listeners.size, 0);
});

test("reactivation moves one trap to the top without duplicating listeners", () => {
  const f = fixture(), first = f.dialog(), second = f.dialog();
  first.trap.activate(); second.trap.activate(); first.trap.activate();
  assert.equal(f.listeners.size, 2);
  f.press("Tab");
  assert.equal(f.documentRef.activeElement, first.controls[1]);
  first.trap.deactivate({ restore: false }); second.trap.deactivate({ restore: false });
});

for (const state of ["hidden", "closed", "disconnected"]) test("a " + state + " upper surface does not block the lower trap", () => {
  const f = fixture(), lower = f.dialog(), upper = f.dialog();
  lower.trap.activate(); upper.trap.activate();
  if (state === "hidden") upper.root.hidden = true;
  if (state === "closed") upper.root.open = false;
  if (state === "disconnected") upper.root.isConnected = false;
  f.press("Tab");
  assert.equal(f.documentRef.activeElement, lower.controls[0]);
  upper.trap.deactivate({ restore: false }); lower.trap.deactivate({ restore: false });
});

test("trap ownership is isolated between documents", () => {
  const a = fixture(), b = fixture(), left = a.dialog(), right = b.dialog();
  left.trap.activate(); right.trap.activate();
  a.press("Tab"); b.press("Tab");
  assert.equal(a.documentRef.activeElement, left.controls[1]);
  assert.equal(b.documentRef.activeElement, right.controls[1]);
  left.trap.deactivate({ restore: false }); right.trap.deactivate({ restore: false });
});
