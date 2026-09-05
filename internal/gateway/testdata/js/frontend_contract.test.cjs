const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

const projectRoot = path.resolve(__dirname, "../../../..");
const stylePath = path.join(projectRoot, "internal/gateway/static/style.css");
const generatePath = path.join(projectRoot, "internal/gateway/static/generate.js");
const batchPath = path.join(projectRoot, "internal/gateway/static/generation-batch.js");
const generateTemplatePath = path.join(projectRoot, "internal/gateway/templates/generate.html");
const gatewayRoutesPath = path.join(projectRoot, "internal/gateway/generation.go");
const galleryPath = path.join(projectRoot, "internal/gateway/templates/gallery.html");
const suggestionsPath = path.join(projectRoot, "internal/gateway/templates/suggestions.html");
const adminSuggestionsPath = path.join(projectRoot, "internal/gateway/templates/admin_suggestions.html");
const layoutPath = path.join(projectRoot, "internal/gateway/templates/_layout.html");
const appPath = path.join(projectRoot, "internal/gateway/app.go");
const suggestionStorePath = path.join(projectRoot, "internal/store/feature_suggestions.go");
const templateRoot = path.join(projectRoot, "internal/gateway/templates");
const previewPath = path.join(projectRoot, "cmd/ui-preview/main.go");
const css = fs.readFileSync(stylePath, "utf8");
const themeCSS = fs.readFileSync(path.join(projectRoot, "internal/gateway/static/theme.css"), "utf8");
const notificationCSS = fs.readFileSync(path.join(projectRoot, "internal/gateway/static/notifications.css"), "utf8");
const generateScript = fs.readFileSync(generatePath, "utf8");
const batchScript = fs.readFileSync(batchPath, "utf8");
const generateTemplate = fs.readFileSync(generateTemplatePath, "utf8");
const gatewayRoutes = fs.readFileSync(gatewayRoutesPath, "utf8");
const galleryTemplate = fs.readFileSync(galleryPath, "utf8");
const suggestionsTemplate = fs.readFileSync(suggestionsPath, "utf8");
const adminSuggestionsTemplate = fs.readFileSync(adminSuggestionsPath, "utf8");
const layoutTemplate = fs.readFileSync(layoutPath, "utf8");
const appSource = fs.readFileSync(appPath, "utf8");
const suggestionStore = fs.readFileSync(suggestionStorePath, "utf8");
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
  assert.equal((themeCSS.match(/:root\s*\{/g) || []).length, 1);
  for (const token of [
    "--font-size-utility", "--font-size-badge", "--control-height",
    "--radius-control", "--radius-panel", "--transition-fast",
  ]) {
    assert.match(css, new RegExp(`${token.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*:`));
  }
  const combined = `${themeCSS}\n${css}\n${notificationCSS}`;
  const definitions = [...combined.matchAll(/(--[\w-]+)\s*:/g)].map(match => match[1]);
  for (const token of [...themeCSS.matchAll(/(--[\w-]+)\s*:/g)].map(match => match[1])) {
    assert.equal(definitions.filter(name => name === token).length, 1, `duplicate palette token ${token}`);
  }
  for (const token of ["--focus-ring", "--color-bg", "--color-text", "--color-action", "--color-danger"]) {
    assert.ok(themeCSS.includes(`${token}:`), `missing palette token ${token}`);
  }
  for (const match of combined.matchAll(/var\((--color-[\w-]+)/g)) {
    assert.ok(definitions.includes(match[1]), `undefined color ${match[1]}`);
  }
  assert.doesNotMatch(`${css}\n${notificationCSS}`, /#[\da-f]{3,8}\b/i, "literal palette colors belong in theme.css");
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

test("controlled generation batches are wired through the form, job center, and API", () => {
  for (const id of [
    "generation-batch-builder", "generation-batch-enabled", "generation-batch-count",
    "generation-batch-parameter", "generation-batch-compare",
  ]) {
    assert.ok(generateTemplate.includes(`id="${id}"`), `missing batch control ${id}`);
  }
  assert.match(generateTemplate, /generation-batch\.js/);
  assert.match(generateScript, /fetch\("\/generate\/batches"/);
  assert.match(generateScript, /renderGenerationBatch/);
  assert.match(generateScript, /openBatchComparison/);
  assert.match(batchScript, /MIN_COUNT = 2/);
  assert.match(batchScript, /MAX_COUNT = 20/);
  for (const route of ["/generate/batches", "/generate/batches/cancel", "/generate/batches/winner"]) {
    assert.ok(gatewayRoutes.includes(route), `missing batch route ${route}`);
  }
});

test("queued generations show server status without fake animated progress", () => {
  assert.match(generateScript, /const setQueueProgress = \(detail\) =>/);
  assert.match(generateScript, /setGenerationProgress\("В очереди ComfyUI", detail, null, "queue"\)/);
  assert.match(generateScript, /generationProgressbar\.hidden = isQueueWaiting/);
  assert.equal((generateScript.match(/setGenerationProgress\("В очереди ComfyUI"/g) || []).length, 1);
  assert.match(css, /\.generation-run-progress\.is-queue-waiting \.generation-run-progress-head \{ margin-bottom: 0; \}/);
  assert.match(css, /animation-iteration-count: 1 !important/);
});

test("video quality limits the base render without constraining RTX upscale", () => {
  assert.match(generateScript, /item\.disabled = numericValue\(item\.value\) > maxVideoGenerationQuality/);
  assert.doesNotMatch(generateScript, /maxVideoGenerationQuality \/ Math\.max/);
  assert.match(generateTemplate, /База доступна до \{\{\.MaxVideoGenerationQuality\}\}p\. RTX-апскейл не входит в лимит\./);
  assert.match(generateTemplate, /name="video_rtx_scale" type="number" min="1" max="2"/);
  assert.match(templates, /Финальный RTX-апскейл этим лимитом не ограничивается\./);
});

test("suggestions keep public intake hidden without hiding the admin review queue", () => {
  assert.match(suggestionsTemplate, /name="action" value="save"/);
  assert.match(suggestionsTemplate, /name="action" value="submit"/);
  assert.match(suggestionsTemplate, /Без вложений администратор получит только ваш текст/);
  assert.match(suggestionsTemplate, /Мои предложения/);
  assert.match(adminSuggestionsTemplate, /Диагностика VirusTotal/);
  assert.match(adminSuggestionsTemplate, /CanDownloadJSON/);
  assert.match(adminSuggestionsTemplate, /name="decision" value="accepted"/);
  assert.match(adminSuggestionsTemplate, /name="decision" value="rejected"/);
  assert.doesNotMatch(adminSuggestionsTemplate, /установить|install|копировать модель/i);
  assert.match(appSource, /mux\.Handle\("\/suggestions\/"/);
  assert.match(suggestionStore, /status IN \('review','accepted'\)/);
  assert.match(suggestionStore, /scan\.status='completed' AND scan\.malicious=0 AND scan\.suspicious=0/);
  const adminNavLine = layoutTemplate.split(/\r?\n/).find((line) => line.includes('href="/admin/suggestions"'));
  assert.ok(adminNavLine && !adminNavLine.includes("FeatureSuggestionsEnabled"), adminNavLine);
});
