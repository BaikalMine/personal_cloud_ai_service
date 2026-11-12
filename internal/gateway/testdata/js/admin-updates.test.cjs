const test = require("node:test");
const assert = require("node:assert/strict");
const { selection, confirmation } = require("../../static/admin-updates.js");
test("updates selection separates checkable and installable components", () => {
  assert.deepEqual(selection([
    { name: "Gateway", selected: true, checkable: true, installable: false },
    { name: "OpenWebUI", selected: true, checkable: true, installable: true },
    { name: "ComfyUI", selected: true, checkable: false, installable: true },
  ]), { count: 2, names: ["Gateway", "OpenWebUI"], installable: 1 });
});
test("updates empty selection never enables installation", () => {
  assert.deepEqual(selection([{ name: "Gateway", selected: false, checkable: true, installable: true }]), { count: 0, names: [], installable: 0 });
});
test("updates confirmation names every selected target without inventing success", () => {
  const text = confirmation(["Gateway", "OpenWebUI"]);
  assert.match(text, /Gateway, OpenWebUI/);
  assert.match(text, /повторно проверит/);
  assert.doesNotMatch(text, /ComfyUI|успешно/);
});
