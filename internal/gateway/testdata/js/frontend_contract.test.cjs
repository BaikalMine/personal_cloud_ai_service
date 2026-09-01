const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

const projectRoot = path.resolve(__dirname, "../../../..");
const stylePath = path.join(projectRoot, "internal/gateway/static/style.css");
const generatePath = path.join(projectRoot, "internal/gateway/static/generate.js");
const galleryPath = path.join(projectRoot, "internal/gateway/templates/gallery.html");
const templateRoot = path.join(projectRoot, "internal/gateway/templates");
const previewPath = path.join(projectRoot, "cmd/ui-preview/main.go");
const css = fs.readFileSync(stylePath, "utf8");
const generateScript = fs.readFileSync(generatePath, "utf8");
const galleryTemplate = fs.readFileSync(galleryPath, "utf8");
const templates = fs.readdirSync(templateRoot)
  .filter((name) => name.endsWith(".html"))
  .map((name) => fs.readFileSync(path.join(templateRoot, name), "utf8"))
  .join("\n");

function selectorInventory(source) {
  const clean = source.replace(/\/\*[\s\S]*?\*\//g, "");
  const counts = new Map();
  const matchingBrace = (open, end) => {
    let depth = 1;
    for (let index = open + 1; index < end; index += 1) {
      if (clean[index] === "{") depth += 1;
      if (clean[index] === "}") depth -= 1;
      if (depth === 0) return index;
    }
    throw new Error(`unclosed CSS block at ${open}`);
  };
  const walk = (start, end, context) => {
    let cursor = start;
    while (cursor < end) {
      while (cursor < end && /\s/.test(clean[cursor])) cursor += 1;
      if (cursor >= end) break;
      const open = clean.indexOf("{", cursor);
      const semicolon = clean.indexOf(";", cursor);
      if (semicolon !== -1 && (open === -1 || semicolon < open)) {
        cursor = semicolon + 1;
        continue;
      }
      if (open === -1 || open >= end) break;
      const close = matchingBrace(open, end);
      const head = clean.slice(cursor, open).trim().replace(/\s+/g, " ");
      if (/^@(media|supports|container|layer)\b/.test(head)) {
        walk(open + 1, close, `${context}|${head}`);
      } else if (head && !head.startsWith("@")) {
        for (const raw of head.split(",")) {
          const selector = raw.trim().replace(/\s+/g, " ");
          if (!selector || /^(?:from|to|\d+%)$/.test(selector)) continue;
          const key = `${context}|${selector}`;
          counts.set(key, (counts.get(key) || 0) + 1);
        }
      }
      cursor = close + 1;
    }
  };
  walk(0, clean.length, "root");
  const repeated = [...counts.values()].filter((count) => count > 1);
  return {
    declarations: [...counts.values()].reduce((sum, count) => sum + count, 0),
    unique: counts.size,
    repeatedNames: repeated.length,
    excessDeclarations: repeated.reduce((sum, count) => sum + count - 1, 0),
  };
}

test("frontend tokens have one source of truth", () => {
  assert.equal((css.match(/:root\s*\{/g) || []).length, 1);
  for (const token of [
    "--font-size-utility", "--font-size-badge", "--control-height",
    "--radius-control", "--radius-panel", "--focus-ring", "--transition-fast",
  ]) {
    assert.match(css, new RegExp(`${token.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*:`));
  }
});

test("service copy never falls below 12px outside the short badge token", () => {
  const literalSmallSizes = [...css.matchAll(/(?:font-size\s*:\s*|font\s*:[^;]*?\s)(9|10|11)px/g)]
    .filter((match) => !css.slice(Math.max(0, match.index - 40), match.index + 40).includes("--font-size-badge"));
  assert.deepEqual(literalSmallSizes.map((match) => match[0]), []);
  for (const match of css.matchAll(/letter-spacing\s*:\s*([^;]+);/g)) {
    assert.equal(match[1].trim(), "0", `non-zero letter spacing: ${match[0]}`);
  }
  assert.match(css, /\.badge\s*\{[^}]*font-size:\s*var\(--font-size-badge\)/s);
});

test("shared components expose semantic states", () => {
  for (const selector of [
    ".ui-field", ".ui-choice", ".ui-segmented", ".ui-toolbar", ".ui-dialog",
    ".ui-media-card", ".ui-status-banner", ".ui-empty-state",
  ]) {
    assert.ok(css.includes(selector), `missing ${selector}`);
    assert.ok(templates.includes(selector.slice(1)), `templates do not use ${selector}`);
  }
  for (const state of ["is-loading", "is-error", "is-success", "aria-disabled", "aria-busy"]) {
    assert.ok(css.includes(state), `missing component state ${state}`);
  }
});

test("selector duplication stays below the G2.3 baseline", () => {
  const inventory = selectorInventory(css);
  console.info(`frontend selector inventory: ${JSON.stringify(inventory)}`);
  assert.ok(inventory.repeatedNames <= 110, JSON.stringify(inventory));
  assert.ok(inventory.excessDeclarations <= 120, JSON.stringify(inventory));
});

test("ui preview renders the component contract", () => {
  const preview = fs.readFileSync(previewPath, "utf8");
  assert.match(preview, /\/preview\/components/);
  assert.match(templates, /define "ui_components"/);
});

test("ui preview exposes every supported reference slot", () => {
  const preview = fs.readFileSync(previewPath, "utf8");
  assert.match(preview, /"ID": "photoflow-krea2-edit"[^\n]+"MaxInputImages": 2/);
  assert.match(preview, /"ID": "photoflow-flux2-edit"[^\n]+"AllowsImages": true, "MaxInputImages": 4/);
  assert.match(preview, /"ID": "minimax-h3-video"[^\n]+"AllowsImages": true, "MaxInputImages": 4/);
});

test("a sole compatible workflow is selected automatically", () => {
  assert.match(generateScript, /compatibleWorkflows\.length === 1\) chooseGenerationWorkflow\(compatibleWorkflows\[0\]\)/);
});

test("the media library reuses an image across every compatible workflow", () => {
  for (const workflow of ["photoflow-krea2-edit", "photoflow-flux2-edit", "minimax-h3-video"]) {
    assert.match(galleryTemplate, new RegExp(`name="workflow" value="${workflow}"`));
    assert.ok(generateScript.includes(`"${workflow}"`));
  }
  for (const parameter of ["media", "template", "workflow", "slot", "role"]) {
    assert.ok(generateScript.includes(`requestedQuery.get("${parameter}")`), `missing library query ${parameter}`);
  }
  assert.match(generateScript, /selectGalleryImage\(item, entry\)/);
});
