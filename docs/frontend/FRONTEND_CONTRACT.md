# Frontend Component Contract

AI Gateway uses server-rendered Go templates and one shared CSS system. New
surfaces extend this system instead of introducing page-local button, field or
status styles.

## Tokens

Use the role-based variables declared in the single `:root` block of
`internal/gateway/static/style.css`:

- surfaces and text: `--canvas`, `--surface*`, `--text*`, `--muted`, `--faint`;
- actions and states: `--mint`, `--cyan`, `--amber`, `--coral`, `--violet`;
- type: `--font-size-body`, `--font-size-utility`, `--font-size-badge`,
  `--font-size-section`, `--font-size-title`;
- rhythm: `--space-1` through `--space-6`, `--control-height`,
  `--control-height-compact`;
- shape and interaction: `--radius-*`, `--focus-ring`, `--transition-fast`.

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

## Media Library Contract

- The personal media library is a shared source for every image-capable workflow; do not create a model-specific gallery or picker.
- Workflow capabilities control available slots and roles: Krea2 currently accepts two images, while Flux2 and MiniMax accept up to four.
- A reuse action must show the destination workflow, slot and semantic role before navigation. The selected asset then renders inside that exact source card in the generation wizard.
- Search, type and state filters remain above the results. Collections are a secondary navigation rail on desktop and a readable vertical list on narrow screens.
- Pinning changes retention and must expose the new deadline. Favorite is organizational only and must not imply extended storage.
- Source job and downstream reference lineage stay available from each result without competing with the primary reuse and download actions.

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
