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
- type: `--font-size-body`, `--font-size-utility`, `--font-size-badge`,
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
| Binary or radio choice | `.ui-choice` | unchecked, checked, disabled, loading, error, success |
| Segmented choice | `.ui-segmented` + `.ui-segment` | default, selected, focus, disabled |
| Toolbar | `.ui-toolbar` | default, wrapping, loading; use `--plain` when its parent already frames the task |
| Dialog surface | `.ui-dialog` | open, loading, error, success |
| Media/result card | `.ui-media-card` | default, loading, error, success |
| Status banner | `.ui-status-banner` | info, loading, success, warning, error |
| Empty state | `.ui-empty-state` or legacy `.empty-state` | useful explanation and a nearby recovery action when one exists |

Page-specific classes remain responsible for content layout. They may not
redefine the shared focus, disabled, loading or semantic state language.

## Responsive Contract

- Full user navigation is used above 1540 px. At and below 1540 px it moves
  behind the labelled menu button before it can force horizontal overflow.
- The admin sidebar becomes a drawer at 980 px.
- Task surfaces become a single primary column at 760 px or earlier when their
  content needs it.
- Touch actions are at least 44 px high. Logout remains visible in the open
  menu on every supported viewport.
- Supported visual widths are 390, 768, 1280, 1440 and 1920 px.

## Generation Wizard Contract

- Generation simplification is project-wide. The shared intent-first wizard covers Krea2 text generation, Krea2 and Flux2 image editing, and MiniMax video; never describe the whole workflow as a MiniMax-only feature.
- Family-specific controls appear only after the user selects a compatible workflow. Video modes, audio/video references, RIFE, RTX and other video postprocessing remain MiniMax-specific without changing the shared three-step structure.
- Device upload and personal-library selection are two sources for the same reference slot. Slot count, roles and exact settings come from workflow capabilities rather than duplicated model-specific UI.
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
