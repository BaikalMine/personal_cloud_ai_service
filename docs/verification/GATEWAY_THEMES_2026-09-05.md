# D01: Gateway Theme Verification

Status: implemented in the actual Gateway; Chromium acceptance passed. WebKit
stability and rollout remain open. Production has not been updated in this step.

## Scope

- A single role-based light/dark palette in `static/theme.css`, with legacy
  aliases to keep the existing components compatible during D02-D09 migration.
- Light, dark and system controls on user/admin navigation and unauthenticated
  screens. The standalone administrative media viewer also exposes the control.
- Preference is validated server-side, applied by a blocking same-origin head
  script before CSS, stored in cookie/localStorage and synchronized across tabs.
  Manual selection takes precedence over OS changes. Storage failures do not
  prevent an in-page change; cookies and localStorage have independent fallbacks.
- Native controls inherit color-scheme. Charts use the same palette; media is
  not inverted, filtered or recolored by the theme. Proxy UIs are unchanged.
- Existing page compositions and generation payloads are preserved. This does
  not claim the new studio, navigation or other page redesigns are finished.

## Completed Checks

- Full uncached `go test -p 1 -count=1 -timeout=180s ./...`, with a separate
  tmpfs PostgreSQL 16 database. The temporary database was removed afterward.
- 73 Node tests, including palette ownership, undefined color roles, early
  preference resolution, independent storage fallbacks and cross-tab removal.
- 24 Playwright theme cases on 390x844, 768x1024, 1440x900 and 1920x1080.
  Both themes cover 28 preview routes; serious/critical WCAG axe findings are
  checked across all 28 routes at 1440 px. No such findings remain there.
- Four explicit state/reflow cases at 1440 px, also exercising 320x568,
  640x450 and 1280x900. The 640px case checks the effective layout width of a
  1280px window at 200%; it is not a native browser zoom or iOS keyboard test.
- Hover/focus on primary, secondary and destructive actions, theme-picker focus,
  notification dialog, media viewer focus/escape, and exact screenshot equality
  for unobstructed media pixels across a theme change.
- Screenshots manually inspected: light login on mobile, light users, dark
  components, mobile gallery, light navigation, dark notifications and media
  before/after. Test photos/data are fixtures, not production material.
- Full existing UI gate plus the theme/state cases: 124 passed, 84 intentional
  skips of duplicate canonical-desktop scenarios, zero failures (208 cases).
- The explicit 36-case baseline pass succeeded: 30 passed, six desktop-only
  skips. All 86 existing PNGs were refreshed, including color differences below
  the previous comparator tolerance. A capture-only rule anchors the sticky
  user header to its document position so it cannot cover long component PNGs.
- Additional engine checks use a real loopback HTTP fixture with the unchanged
  production CSP. Chromium and Firefox passed. WebKit passed five consecutive
  final runs, but an earlier loopback repetition still returned an unstyled body
  once; its root cause has not been established. Do not treat that as resolved.
  The test retains failure diagnostics. Firefox/WebKit are opt-in via
  `UI_THEME_CROSS_BROWSER=1` until the broader D10 engine matrix is integrated.

## Defects Caught During Verification

- The additional theme control overflowed the wide user header. Navigation
  items now wrap within the available width; replacing the shell is still D02.
- Light update controls and compatibility text had weak contrast. Their role
  assignments were corrected. Three existing update checkboxes gained names.
- Destructive hover states and the loading spinner needed different foreground
  roles. Prompt-diff deletions now use a readable tinted background.
- Removing the preference in another tab originally left a stale cookie.
  Synchronization now updates the cookie as well.
- A blocked cookie getter originally prevented an available localStorage
  fallback. The storage paths are now independent and covered by tests.
- Test-only corrections: the language guard accepts additional html attributes;
  media pixel comparison excludes the theme-aware selection checkbox overlay;
  desktop admin navigation does not require a nonexistent drawer button.

## Artifacts

Local reports under `artifacts/playwright/` (ignored build output):

- `theme-responsive-final/report/index.html`
- `theme-states-final/report/index.html`
- `theme-regression/report/index.html`
- `theme-baselines-final/report/index.html`
- `theme-engines-repeat/report/index.html` (includes the unresolved WebKit run)
- `theme-webkit-repeat/report/index.html` (five later successful runs)

Runtime: the pinned `ai-access-gateway-playwright-tests:1.62.1` Linux image.
Preview: `http://127.0.0.1:18085/preview/generate`, with fixture data only.

## Remaining

Production build, deployment and smoke remain open; binary rollback alone is
not sufficient for the earlier migrations in this release cycle.
WebKit stability, full cross-browser task coverage, real-device keyboard/zoom,
slow-network first-paint recording and
user usability acceptance remain part of D10. No GPU workload or production
ComfyUI, Docker Desktop or mining process was started/stopped for this UI step.
