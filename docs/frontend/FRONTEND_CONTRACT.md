# Frontend Component Contract

AI Gateway uses server-rendered Go templates and one shared CSS system. New
surfaces extend this system instead of introducing page-local button, field or
status styles.

## Tokens

Use `internal/gateway/static/theme.css` as the single color source. Its role
tokens use `light-dark()` and the root `color-scheme` for light, dark and system
preferences. Legacy color names below are aliases, not independent palettes.
Typography, rhythm and dimensions stay in `internal/gateway/static/style.css`:

- surfaces and text: `--canvas`, `--surface*`, `--text*`, `--muted`, `--faint`;
- actions and states: `--mint`, `--cyan`, `--amber`, `--coral`, `--violet`;
- type: `--font-size-body`, `--font-size-label`, `--font-size-control`, `--font-size-utility`, `--font-size-badge`,
  `--font-size-section`, `--font-size-title`;
- rhythm: `--space-1` through `--space-6`, `--control-height`,
  `--control-height-compact`;
- shape and interaction: `--radius-*`, `--focus-ring`, `--transition-fast`.

New color consumers use `--color-*` roles. Fixed media colors and overlays also
belong in theme.css. Never invert media or override a proxied application's theme.
The blocking same-origin theme.js resolves the preference before styles paint;
the validated cookie supplies the server fallback when JavaScript is unavailable.

Normal explanatory and utility text is at least 12 px. The only smaller token
is `--font-size-badge`, reserved for short badges. Letter spacing remains zero.

## Components

| Component | Class | Required states |
| --- | --- | --- |
| Field | `.ui-field` | default, focus, disabled, loading, error, success |
| Aligned field group | `.ui-field-grid` + `.ui-field-label` | wrapped labels, validation messages, hidden fields, single column |
| Binary or radio choice | `.ui-choice` | unchecked, checked, disabled, loading, error, success |
| Segmented choice | `.ui-segmented` + `.ui-segment` | default, selected, focus, disabled |
| Optional settings disclosure | `.ui-disclosure` | closed, open, focus; native keyboard behavior |
| Toolbar | `.ui-toolbar` | default, wrapping, loading; use `--plain` when its parent already frames the task |
| Dialog surface | `.ui-dialog` | open, loading, error, success |
| Media/result card | `.ui-media-card` | default, loading, error, success |
| Status banner | `.ui-status-banner` | info, loading, success, warning, error |
| Empty state | `.ui-empty-state` or legacy `.empty-state` | useful explanation and a nearby recovery action when one exists |

Page-specific classes remain responsible for content layout. They may not
redefine the shared focus, disabled, loading or semantic state language.

`controls.css` owns native inputs, buttons, `.ui-field`, `.ui-choice`, segmented
choices and disclosures. It loads after style.css and before shell.css. Pages
own column counts and placement, not alternate field heights or focus styles.
Regular fields place the label text in `.ui-field-label`, then the native control,
then an optional `small` or `.field-hint`. Direct fields in `.ui-field-grid` share
three subgrid rows so a wrapping label or hint cannot misalign neighboring inputs.
Do not put buttons or another interactive control inside a field label.
The fallback for browsers without subgrid preserves a readable independent grid.
Control text is 14 px on desktop and 16 px on narrow screens; control height is
at least 44 px. Labels use 13 px/600, hints use 12 px/400. Binary choices use an
18 px native input inside a 44 px or larger target. `.ui-choice--row` is the
unframed option row for processing modules, not a nested card.

## Responsive Contract

- The shared user/admin shell uses a 224 px sidebar from 1100 px upward;
  primary routes never disappear into a menu at 1280 or 1440 px.
- At 760-1099 px the sidebar is a 72 px icon rail with accessible names and
  a labelled menu button. Below 760 px, user routes have bottom shortcuts
  for Create, Results, Tasks and More. Permission filtering applies to both.
- Navigation comes from `navigation.go`, renders through `_shell.html`, and
  is owned by `shell.css`/`shell.js`. Do not restore old topbar/sidebar owners
  in style.css or app.js. Shared page controls still use the classes above.
- Account pages keep the admin workspace on the admin listener. This server
  context only selects navigation; it does not grant permissions. Return links
  to the studio use the public base URL.
- The drawer traps focus, makes the background inert, closes with Escape or
  its backdrop, and clears its state when the layout breakpoint changes.
- The topbar groups the theme, notifications and account controls. Sensitive
  media and administrator priority preferences live in the account dialog;
  moving them must not change their persistence, permissions or mining policy.
- Tasks shortcuts open the existing notification panel and share live counts.
  This shell does not yet implement the cross-job workspace planned in F07/D06.
- Task surfaces become a single primary column at 760 px or earlier when their
  content needs it.
- Touch actions are at least 44 px high. Logout remains available in the account
  dialog and in the no-JavaScript fallback on every supported viewport.
- Tables use `.table-scroll`; never let an unframed minimum-width table expand
  the page. Mobile selection actions stay above bottom navigation using
  `--workspace-bottom-offset`.
- Supported visual widths are 390, 768, 1280, 1440 and 1920 px.

## Generation Studio Contract

- Generation simplification is project-wide. The one-workspace studio covers Krea2 text generation, Krea2 and Flux2 image editing, and MiniMax video; never describe the whole workflow as a MiniMax-only feature.
- The first allowed scenario and compatible recipe are selected without an extra step. Drafts, repeats and library links still override that initial selection.
- Desktop keeps a 340-420 px editor beside the result. The editor scrolls independently; the launch summary and actions occupy a separate row and never overlay its controls. Below 900 px, Configure and Result are two views of the same draft, not separate pages.
- Family-specific controls appear only for a compatible workflow. Video modes, audio/video references, RIFE, RTX and other video postprocessing remain MiniMax-specific. Exact settings use a native dialog while retaining the same form ownership, rights and field names.
- Device upload and personal-library selection are two sources for the same reference slot. Slot count, roles and exact settings come from workflow capabilities rather than duplicated model-specific UI.
- A mode change retains inactive media and its role in the draft with a visible notice. It must exclude that media from generation, preflight and assistant payloads. Only an explicit remove action clears a source.
- Preflight, assistant and launch share coalesced media uploads. A changed selection invalidates an in-flight preparation or preflight. Repeated submit events may not create duplicate jobs.
- The previous result remains visible while preparing the next job. The complete cross-job task workspace and the compact recent-results strip remain separate roadmap work, not implied by the existing generation history.
- The primary path uses user-facing intent and result language. Internal branch and node names belong in exact settings or diagnostics, not in the main decision flow.

## Media Library Contract

- The personal media library is a shared source for every image-capable workflow; do not create a model-specific gallery or picker.
- Workflow capabilities control available slots and roles: Krea2 currently accepts two images, while Flux2 and MiniMax accept up to four.
- A reuse action must show the destination workflow, slot and semantic role before navigation. The selected asset then renders inside that exact source card in the generation wizard.
- Search, type and state filters remain above the results. Collections are a secondary navigation rail on desktop and a readable vertical list on narrow screens.
- Pinning changes retention and must expose the new deadline. Favorite is organizational only and must not imply extended storage.
- Source job and downstream reference lineage stay available from each result without competing with the primary reuse and download actions.

## AI Content Task Contract

- One administrative card represents one user task. Prompt-assistant output, the applied prompt, durable job stages, errors and generated media may not split into parallel cards.
- Media is the visual focus when available. Text-only, failed, cancelled and retention-expired tasks use explicit semantic states instead of empty frames.
- Deleted accounts retain the username snapshot with an explicit deleted-author label; no broken user link is rendered.
- SSE reconciliation is keyed by stable task ID and version. Reuse unchanged DOM nodes and preserve scroll, revealed sensitive content and the currently open task dialog.
- The detail dialog exposes the full prompt flow and diagnostics, traps keyboard focus, closes with Escape and restores focus to the originating card.

## Admin Operations Contract

- `/admin` is an operations workbench, not a traffic dashboard. The first viewport prioritizes actionable problems, durable jobs, the ComfyUI queue and dependency freshness; aggregate traffic remains in `/admin/metrics`.
- Every attention row names the condition, affected count and next diagnostic action. Derived queue or workflow warnings must not duplicate the primary dependency outage that caused them.
- Desktop uses two independent content columns so a long dependency list cannot stretch unrelated cards or create empty grid rows. At 1180 px and below, the columns become one ordered flow: jobs, dependencies, workflow, maintenance and storage.
- Healthy workers stay collapsed; failures, stale data and retries remain visible. Links lead to the specific job, service, workflow matrix, content queue or storage policy rather than a generic dashboard.
- Resource history uses real timestamps and states its period, sample count, min, max and current values. Generation starts appear as markers; gauges are reserved for current CPU, RAM, GPU and VRAM values.
- The canonical operations preview must include running and overdue jobs, a mixed dependency state, a worker retry, storage growth and generation failures so visual regression cannot pass on an unrealistically empty page.

## Suggestion Review Contract

- `FEATURE_SUGGESTIONS_ENABLED` controls only public intake and its user navigation entry. The administrative review queue remains available when intake is hidden, and toggling the flag never migrates or deletes stored proposals.
- A proposal follows one visible lifecycle: draft, submitted, scanning, review, accepted or rejected. Drafts are owner-only and editable; submitted proposals cannot be silently rewritten.
- Text is sufficient for submission. Up to three source links and one valid JSON file up to 5 MB are optional. The selected filename is shown inside the upload control, and an already saved draft attachment remains explicit until replaced or removed.
- Users see the lifecycle status and administrator comment without VirusTotal identifiers, counters or raw errors. Administrators see per-source diagnostics, but an unchecked link is not clickable and JSON is downloadable only after the saved file itself has a clean result.
- Accepting a proposal records a review decision; it never installs nodes, loads a workflow or copies a model or LoRA into ComfyUI. Rejection requires a useful user-facing comment.
- Desktop intake keeps composition and personal history in two readable columns. They stack before either column becomes cramped; narrow-screen review actions become full-width without changing decision order.
- Canonical previews cover draft, scanning, review, accepted, text-only and deleted-author states. Interaction tests select a JSON file, open diagnostics and verify the only available safe review actions.

## Verification

`/preview/components` in `cmd/ui-preview` renders the canonical states. The
frontend contract test guards the token and selector inventory. The isolated
`scripts/test-ui.ps1` gate starts the preview in Docker and runs Playwright at
390x844, 768x1024, 1440x900 and 1920x1080. It verifies screenshots, horizontal
overflow, text containment, keyboard navigation through the wizard, media
picker and lightbox, canonical loading/empty/error/sensitive/offline/queued/
completed states, and serious or critical axe violations on the main product
surfaces. Use `scripts/test-ui.ps1 -UpdateSnapshots` only after visually
reviewing an intentional interface change; the ordinary command must pass
without rewriting baselines.
