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

## Live Read Lifecycle

- `live-events.js` owns the reusable EventSource reconnect and optional poll
  timers. Existing task countdown/recovery stays in its task controller.
- SSE revisions invalidate read models, never replay mutations. A reconnect
  reconciles current state, including a lower revision after a restore.
- A cursor advances only after its data is applied. Reads coalesce a burst into
  one pending refresh; an old source or departed-page read cannot resume a stream.
- Hidden pages pause view-only streams or slow optional reads. Notification SSE
  remains available for permitted browser notifications. Pagehide closes streams
  and optional timers; pageshow resumes without submitting operations again.
- Session/access failure stops reconnects and requires explicit authentication.

## Components

Generation upload orchestration lives in `generation-media.js`. Autosave and
preflight use the same controller; page bindings supply source reads and commit
presentation. Do not restore transport fallbacks or independent pending maps in
`generate.js`. A source must retain object identity while selected, and stale
responses must not update either its stored value or the replacement's status.

`generation-payload.js` is the single adapter for named model controls, saved
values, launch and draft serialization. Add ordinary model parameters to the
existing form/manifest contract, not to independent page allowlists. Disabled
controls and inactive media remain draft data; they must not leak into launch.
`generation-draft.js` owns transport and the draft session, including restore
suppression and background upload attempts. Validate saved selection before
changing presentation. Do not restore these owners inside `generate.js`.
The generation form disables native autocomplete restoration so a reload cannot
independently restore stale model fields after a saved draft has been deleted.
Deleting a draft still leaves the current open editor unchanged.
Draft visibility autosave must check that the page has not departed. A late
hidden event after pagehide must not start another POST during an accepted
unload. Cancelling beforeunload must still permit ordinary autosave.

`generation-collections.js` owns collection reads, revision-aware mutations and
late-response fencing. `collection-workspace.js` binds those operations to the
studio and Results; `collection-workspace.css` owns their compact, unframed
layout. Selecting a collection changes the destination of new results only.
It must not replace editor fields. Save/open are explicit actions; opening
confirms replacement, settles the current draft and pending uploads, and uses
the draft controller's replacement operation. Local edits made while opening
remain local and require an explicit version choice without stealing focus.
Unknown writes require an explicit state refresh, never an automatic POST.
Pagehide invalidates pending presentation; returning does not replay a write.
Collection context does not extend source retention. Missing sources retain
their names without active URLs, and the collection rail remains available
before the first result exists. No separate project or collaboration model is
introduced by this module.

`generation-submission.js` owns the ordinary single/batch launch, durable browser
pointer, recovery reads, countdown, cancellation and ComfyUI WebSocket lifecycle.
The page supplies presentation callbacks and the existing payload adapter only.
Do not add a second POST, polling loop or localStorage owner to `generate.js`.
Persist an account-scoped request pointer before the sole launch POST. An unknown
response or HTTP 404 is not evidence that execution never started; recover the
same ID. Explicit closure fences only creation, while a bound job/batch uses its
existing cancellation API. Keep cancellation intent until a terminal reply.
Separate monotonic terminal/cancelling markers prevent stale tab writes from
erasing those decisions; they contain no prompts, form parameters or media.
An unscoped legacy pointer is removed only after an owned binding is confirmed.

Authentication/storage failures and long-running checks retain a reachable
recovery action. Pagehide suspends reads and sockets without cancelling a job;
pageshow reads the saved request without dispatching it again. Hidden pages slow
polling and close their optional progress socket. Only matching prompt events
may update progress. Disconnected sockets fall back to HTTP, and a completed
status without outputs reads retained, permission-filtered media from the job.
Until acceptance, label the previous result and keep recovery actions before
that media so they remain reachable on mobile. A confirmed batch hands off to
the durable task workspace instead of continuing a second batch monitor.
Restoring a saved legacy wizard step must not switch the studio pane: a delayed
draft response cannot hide an already recovered job. Explicit editor navigation
still selects Configure normally.

The active result action bar keeps task navigation, status refresh and cancellation
in a compact row with 44px touch targets. Status refresh is icon-only with a full
accessible name and tooltip; a terminal retry retains its visible label. Pending
cancellation keeps its disabled state and full accessible description. Only empty
active result surfaces shrink to content; retained media and history inspectors
keep their own layout. Do not change submission/recovery ownership for styling.

`generation-assistant.js` owns active request context, corrections, transport,
pointer persistence and review decisions. `generation-assistant-jobs.js` remains
the only durable assistant submit/recovery controller. Page bindings adapt the
active editor and render callbacks; do not restore parallel HTTP, request serial,
correction or decision owners in `generate.js`. Apply/Keep await acknowledgement
and recheck prompt, proposal and source context before changing editor state.
Late answers remain separate from manual or approved text. Source corrections
must match the uploaded source; same-name file replacements are different inputs.
Persisted job context encoding and the owner-scoped pointer schema stay compatible
with accepted jobs. A poll with unchanged pointer data must not dirty the draft.
Pagehide suspends reads and fences late presentation, never automatically retries
a model request; pageshow resumes the existing durable controller.

After applying an assistant proposal, original-text restoration belongs to the
same assistant session. Reuse the saved original and existing decision endpoint;
do not add a second browser history store or call the model again. Keep the
proposal available for review. Confirm replacing manual edits, serialize a blur
save before restoration, and fence late acknowledgements against editor/context
changes. Launch waits for restoration to settle. Preserve draft round trips and
do not focus a text field automatically when restoration completes.

State synchronization must not replace a button's unchanged text node during
field blur/change. In WebKit this can discard the following native click, even
when pointer-down and pointer-up target the same enabled submit button.

`generation-results.js` owns retained output and history DOM. Extend its single
job-card renderer for both standalone and batch entries; do not reintroduce
page-local builders. `generation-history.js` owns read coalescing, timelines,
filters, batch/list actions and the shared live lifecycle. The page supplies the
editor adapter and delegates active launch cancellation to submission. Reads and
page resume must never replay a mutation. Preserve existing media nodes, expanded
details and keyboard focus across live updates, including moves between replaced
batch containers. A failed read retains known data and exposes explicit retry;
an access failure clears private result views and stops reads.

Keep `generation-lightbox.js` as the playback/download/focus owner. Preview
playback must check current view visibility before reacting to hover or focus:
closing an overlay may generate another pointer-enter after an earlier pause.
Branch preparation must compare editor values and actual selected-file identities
before applying a late response. Ordinary repeat/branch does not edit postprocess
parameters, and verification batches keep their dedicated comparison workflow.

`image-comparison.js` owns the original/result curtain shared by studio and
gallery. It reads only the primary retained input from the owned, completed
edit through `/generate/jobs/comparison`, never the current editor selection.
The existing viewport geometry drives both layers; differing aspect ratios use
contain, not stretching or an invented crop. The divider has a keyboard slider
contract and a 48px touch grip. A second pointer cancels divider dragging and
hands the gesture to shared zoom. Close, replacement and pagehide fence late
reads and release the original. Missing inputs leave ordinary viewing intact;
transient failures offer an explicit retry without submitting GPU work.

`generation-feedback.js` owns shared status presentation and optional read state.
It does not own durable generation, assistant or draft operations. Presentation
preserves unchanged text nodes, existing disabled-state ownership and domain
states such as a draft conflict when an attempted choice fails. Busy controls
retain their dimensions and accessible names.

`generation-studio.js` owns the compact workflow picker presentation. Its native
dialog reuses the existing workflow buttons and selection controller; it does not
own a second selected workflow or submission path. Escape/backdrop/close cancel
without replacing settings, selection restores focus, and unavailable options
remain labelled but disabled. The model select may present a compact label, but
its value and full original label remain intact.

Capabilities, queue facts, the image picker and preflight use the bounded JSON
boundary and coalesced resource reads. Transient errors retain known settings;
unconfirmed queue estimates disappear. Access failure clears a resource's private
list and offers sign-in without restarting reads. Pagehide aborts optional reads
and fences late replies; return may reread but never repeats preflight or a write.
An interrupted result-to-editor transfer keeps its source URL until a resumed
read completes. Preflight evidence is rejected after editor parameters change.

`generation-recipes.js` owns saved-settings reads, selection and explicit writes.
Accept a confirmed save/delete directly, without depending on a second list read.
Do not read concurrently with a write, replay an uncertain write, or enable another
write before a confirming list check. A late save must not clear a newly edited
name. Unknown responses and access loss are not empty saved-settings lists.

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
- Tasks shortcuts navigate to `/tasks` when at least one task family is allowed.
  The topbar bell retains the notification panel. Its active count combines
  generation, training and individual caption jobs without double-counting series.
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
- Image dimensions stay in the editor under the existing advanced-settings permission. The dialog groups optional controls by LoRA, processing, sampling, frame preparation, memory and export. Closing a group never disables its inputs or changes the submitted values.
- Profiles disclose their actual before/after changes; a manual change to a profile field clears the active preset indicator. A missing default model falls back only to an available compatible option.
- The launch summary distinguishes base pixels from expected output pixels for every family. Video shows requested duration and output FPS; RIFE changes FPS, RTX changes only output size. Batch ranges describe each variant without weakening the existing quota checks. Source-analysis megapixels are not output resolution.
- The collapsed summary shows result count and expected size. Expanded facts scroll in their own bounded region; both size columns align and launch actions remain outside that scroll region. Forecasts must not be presented as measured metadata from a finished file.
- Reference thumbnails open the complete image. Audio/video sources use native players and an optional fragment preview without rewriting trim fields. Removing or replacing a local source revokes its preview URL; switching away from reference mode pauses playback.
- Device upload and personal-library selection are two sources for the same reference slot. Slot count, roles and exact settings come from workflow capabilities rather than duplicated model-specific UI.
- A mode change retains inactive media and its role in the draft with a visible notice. It must exclude that media from generation, preflight and assistant payloads. Only an explicit remove action clears a source.
- Preflight, assistant and launch share coalesced media uploads. A changed selection invalidates an in-flight preparation or preflight. Repeated submit events may not create duplicate jobs.
- Internal same-tab navigation waits for the latest dirty draft, including edits made during an in-flight save. Save errors, conflicts and unsaved files keep the form open and reveal its recovery controls. Native unload warnings remain for reload/closing; new-tab, download and fragment links keep their native behavior.
- The previous result remains visible while preparing the next job and after a rejected HTTP launch. Once the new job is accepted, clear the old media from the active result area; its history is unchanged. A saved job waiting before ComfyUI shows one status/countdown and a reachable Cancel action, without animated progress or a duplicate status panel. Unknown sends remain pending until confirmed.
- Recent results show at most eight run tiles, grouping a batch as one run. Preserve an explicitly selected older run within that bound when newer jobs arrive. Arrow/Home/End keys move focus; activating a tile selects it without changing the draft. The full history belongs to Tasks, with typed links to the selected job or batch.
- Only one selected run is expanded. Batch variants share a bounded, keyboard-scrollable inspector rather than increasing the whole page height. Empty, loading, stale, missing and access-loss states are explicit. Deep links hydrate an exact older job/batch through the existing permission-checked reads.
- Strip covers use the existing static task thumbnail endpoint, not original images or video players. Sensitive and expired covers stay placeholders; sensitive originals require a permitted reveal. Server reads and the thumbnail endpoint independently enforce owner, access and retention. Submission, history and the lightbox retain separate ownership.
- The primary path uses user-facing intent and result language. Internal branch and node names belong in exact settings or diagnostics, not in the main decision flow.

## Checked Generation Repeat

The default Repeat action opens `/generate?edit_job=...` for editing and never
submits the studio form. Restore the source prompt, exact string seed, model,
workflow settings, LoRA strengths, reference roles, and retained owner-scoped
input assets through the existing draft renderer. Missing assets stay visible
and block submission until replaced or removed; a failed parameter read keeps
the source identity and permits retry. Import does not activate a batch or an
assistant job. Pending submissions retain Check Status rather than Repeat.

Repeat Immediately is a distinct action (a labelled play icon in the compact
result action row). It checks the saved source recipe, not the current draft,
then submits once only when the checked parameters are unchanged. Missing
optional file hashes alone do not require a second click; changed parameters
still require explicit confirmation. Closing/backgrounding
inspection clears immediate-launch intent. Unknown outcomes use the same
durable request ID and recovery path, never an automatic second submission.

Checked generation repeat is a separate object action, not a studio form submit.
History and gallery viewer share the checked API for immediate repeats and
confirmation of changed conditions. Opening or confirming that dialog must
not replace the current draft. It shows current conditions,
human changes before technical hashes, exact string seed, relevant quota and
honest unknown/missing states. Source media must not fetch sensitive or expired
bytes before a permitted reveal; close/hidden clears media and pauses checks.
Confirmation persists an owner-scoped recovery pointer before POST. Unknown
responses cannot be cleared by a 404 or automatically resent under another ID;
explicit abandonment needs the server fence. Keyboard focus stays inside and
returns to the live source action even when the history rerenders.

## Independent Postprocessing

- Improve Result is an exact retained-media action in gallery cards/viewer. It
  opens a native dialog and never writes a studio draft or repeats a sampler.
- The server supplies compatible exact sizes/qualities, video interpolation and
  color choices. Required resizing follows the selected operation. Combined
  admission is decided by the quote, not inferred from browser geometry.
- Source measurements and requested output are separate. Cost and expiry precede
  explicit confirmation. Acceptance with delayed publication links to the same
  durable task; the browser does not publish/retry work on its own.
- Persist/read back the owner-keyed request pointer before quote and confirm.
  Unknown responses and status 404 retain it. A new calculation or edited plan
  requires a confirmed discard fence; a racing acceptance wins without a new ID.
- Source/reference IDs remain canonical strings. Reference lists fetch metadata
  with pagination, not every original image. Only the chosen reference has a full
  preview, with sensitivity and expiry handling. Closing/backgrounding clears
  media and disposable reads without forgetting a mutation.
- The dialog header/footer remain outside its scroll region. Desktop keeps the
  complete source near the choices; mobile omits image-inapplicable motion fields.
  All viewer actions remain inside 320px, including the new processing action.

## Results Workspace Contract

- `gallery.css` owns the Results layout. Do not restore gallery selectors to
  `style.css`. Keep the compact media tile separate from its inspector; tags,
  prompt and technical evidence must not grow into a repeated wall on every tile.
- `gallery-state.js` owns bounded persisted selection and selected-output read
  state; `gallery-viewport.js` owns linked viewing geometry; `gallery-viewer.js`
  presents evidence and media; `gallery-use.js` owns destination/recovery dialogs.
- Collections are a side rail only above 1100 px. Smaller screens start with a
  closed collection disclosure above the grid. Viewer media/facts become separate
  views at 760 px. All dialogs retain reachable actions while their body scrolls.
- Retained media IDs, not visible array offsets or a latest-100 query, identify
  transfers. Model/recipe permissions and frame capabilities come from the server.
  A late read cannot replace another edited source or silently clamp its slot.
- Stored launch facts and measured output are distinct. Missing historical
  evidence remains missing; raw JSON downloads preserve exact large seeds.
  Scrollable technical content is keyboard-focusable. Measurements load only for
  the selected source, and stale/hidden/expired state cannot expose prior facts.
- A/B shares scale and normalized center. Video playback uses a common timeline;
  a transient event alone is not proof of buffering, and an old play promise must
  not resume a cancelled/replaced comparison. Playback errors appear in its dialog.
- Dataset import persists an owner-scoped request before POST. Unknown replies
  recover by GET or explicit same-ID retry/discard; no implicit second creation.
  Dataset revision, ownership and same-asset deduplication remain server-enforced.
- Hidden sensitive media has no image/video `src` until reveal. Closing viewing
  surfaces clears media resources. Hide copy distinguishes removing an item from
  Results/reuse from physically deleting its underlying bytes.

## Task Workspace Contract

- `/tasks` combines owner-scoped generation, training and caption rows without
  replacing their source state machines. A separate owner-scoped detail endpoint
  supplies actions; administrator status does not expose another owner's task.
- Type, state, literal search and local-date boundaries filter the list. Loaded
  pages, selection, expanded packages and scroll survive navigation in the same
  tab. Changed revisions or rejected cursors trigger reconciliation, not append-only
  duplication. A revoked selected family returns to accessible filters.
- A generation package groups only matching children in the filtered list. Its
  detail shows the complete package and source-validated cancel-remainder action.
  Do not group captions into a fabricated historical series by dataset ID.
- The caption family offers Frames and Series views. Only explicitly recorded
  series appear in history; never reconstruct attempts from a current dataset.
  The default/all list counts individual source jobs, not series aggregates.
  Series use owner-scoped creation-order pagination, recorded names, terminal
  counts and the existing task revision. They do not fetch each member's detail
  or claim one shared GPU position. Opening a member retains an explicit return
  to the browsed series across reload, not just the member's latest series link.
- Desktop uses a sticky inspector whose available height leaves actions in view;
  content scrolls inside it. Below 1000 px it is a native modal with a close/return
  control, focus containment and restoration. Full media is contained, not cropped.
- Connection loss leaves confirmed source states unchanged, disables mutation
  buttons and exposes reconnect/countdown recovery. Waiting states have no fake
  percentage or ETA. A late detail response cannot replace the current selection.
- The selected inspector shows GPU reasons and a timestamped queue position from
  an independent owner-scoped read, not a task-list index or a lease. It polls
  once per five seconds while active, coalesces requests and backs off on errors.
  Disconnect/pause/read failure clears position claims immediately; evidence
  expires after ten seconds measured from monotonic request start, even if the
  server clock is ahead or a response is still pending. Source state stays intact.
- GPU-only updates retain media nodes, scroll and focus. A background refresh may
  not disable the focused control and lose Escape handling in Firefox. Terminal
  source state is not GPU completion evidence; keep checking an existing intent
  until absent, and never invent a single position for batch/series aggregates.
- Visible source rows share one bounded `/api/tasks/gpu-snapshots` read (at most
  32 identities), with the same five-second poll and ten-second monotonic age
  bound as the inspector. No per-row detail fan-out or task-revision cache may
  substitute for this evidence. Hidden pages and the mobile modal pause list
  polling; viewport changes abort obsolete batches and drop departed samples.
  Missing sources revalidate the list, and manual refresh can recheck them.
  GPU-only failure removes rank claims without failing the source or disabling
  its otherwise valid actions. Row names include only currently shown evidence;
  background reads do not add a live region to every row.
- Mutations use existing source handlers and CSRF. Cancellation requires explicit
  confirmation. An ambiguous response warns that the operation may have happened;
  it never implies that remote execution stopped. Caption retry is unavailable
  until its prior inference is settled.
- SSE currently checks the owner's revision and access per connection. This is
  not the F12 shared hub or proof of session-expiry/load acceptance. Training and
  durable caption-series notifications link to owner-scoped task details. Series
  preserve exact attempt membership after retry; unavailable historical attempts
  never silently open a newer result as their original result.
- Caption editor links bind a dataset, stable image ID and asset ID. Check current
  membership on the server and after loading the editor. A stale or removed frame
  must not select another frame. A series opens its recorded working set without
  choosing an arbitrary member.
- Training links open the immutable source version. Opening never restores or
  starts anything: Restore Copy is a separate explicit action and creates another
  working set. This is not Resume or a promise of exact training reproduction.
- Editor links select a version or a dataset, never both. A failed explicit load
  retains its exact target for retry and does not load the newest dataset instead.
  Dataset selection and the address remain aligned through create, switch, copy,
  fork and delete; stale load/dialog replies cannot change a newer selection.

- List still previews come from the bounded owner-scoped list snapshot, not from
  per-row detail requests. Missing/hidden/expired previews use a stable kind icon;
  video-only results use a static first frame. Training covers come only from the
  recorded immutable dataset snapshot, never the current editable dataset. Their
  visible provenance labels distinguish dataset frames from generated results.
  Sensitive previews must not fetch bytes before reveal. Keep unchanged image
  nodes; late errors cannot replace newer content. Retry failed images only on
  a fresh list. Known expiry removes the image locally, while a connected active
  page refreshes media metadata every 30 seconds independently of task revision.
  Page departure pauses these checks. Rows request bounded static PNG derivatives
  (160px maximum edge, 128 KiB maximum), not original images or playable video.
  The byte endpoint revalidates ownership, family access, sensitivity and source
  retention even on cache hits. Source identity changes produce a different URL.
  Preserve the 36/44px contain-fit slot, selection and independent row actions.
  Many-tab load and full task-workspace acceptance remain separate work.

## Contextual Assistant Contract

Dataset quality review is separate from caption job state. Its CPU measurements
are advisory, persist per immutable image, and never exclude an image or block
training. Show checked/remaining counts, stop and retry; do not present missing
or obsolete measurements as a pass. Similarity and background hints must describe
their visual limits rather than claim semantic recognition. Keep frame numbers
current after reorder and omit excluded frames from advice.

- Generation assistance opens from the prompt in a native dialog. The active workflow selects the video profile. The original text is changed only after an explicit Apply action; closing the dialog preserves the editable proposal.
- The panel lists active source images and their roles. Audio/video entries describe available workflow materials, not a promise that the assistant inspected their contents.
- A changed source, mode, model or original prompt invalidates the old proposal without deleting it. A late response must not overwrite new input or steal focus from a closed panel.
- Per-photo corrections belong to the source asset as well as its picture number. Neither the submitted correction nor the visible correction field may inherit a previous image's observation after replacement.
- `generation-assistant.css` owns the generation panel. Header and actions remain outside the scrolling body; diff and technical metadata are secondary disclosures.
- The LoRA caption panel uses the existing dataset controller and durable caption jobs. It never creates a second dataset or uploads an already saved image again. Navigation selects a stable image ID, not a captured array position.
- The complete photo remains visible with `object-fit: contain`. Trigger and editable caption body are separate in the panel but stored as one trigger-first caption. Clearing the body leaves an empty caption eligible for the empty-only series.
- Manual caption edits update the caption revision. A response for an older revision, asset or trigger cannot replace the manual caption. Retry/cancel targets the selected job; next/previous and the filmstrip do not start requests.
- Caption errors and save/conflict states remain visible inside the panel. Closing or switching frames retains edits. The series summary names the running frame and distinguishes errors, cancelled, skipped and unapplied responses.
- `lora-caption-panel.js` owns presentation only, with dataset state and requests remaining in the editor/controller. `lora-caption-panel.css` owns its layout; shared controls and theme tokens stay authoritative.

## LoRA Library Contract

- `/train-lora` opens the owner-scoped LoRA library, with a separate Datasets
  view. Explicit `dataset`, `version`, `run` and `new=1` links open the
  training editor. POST validation errors still render the submitted form.
- New training starts an empty unsaved dataset, never the latest saved set.
  Opening the page, selecting a result and reading its passport issue no training
  or generation mutation. An empty editor keeps `new=1` in its address so reload
  does not unexpectedly return to the library.
- The training list reuses task pagination and status translations. Revision
  changes replace stale pages. Only filters and a selected ID are stored in the
  owner-keyed session preference; names, images and passports are not cached there.
- Dataset-frame thumbnails say what they are, not verification results. Sensitive,
  expired and nonlocal sources are not requested. Expiry removes the image while
  preserving row keyboard focus. Backgrounding the page drops preview sources;
  returning refreshes the list and selected passport.
- The inspector separates requested parameters from recorded execution, preserves
  the requested seed as a string and explicitly identifies missing legacy evidence.
  Full file hashes live in the passport disclosure. The recorded source-version
  link requires passport-confirmed availability; never substitute the latest set.
- Desktop has an unframed inspector with a height calculated from its actual top
  edge. Its body scrolls while actions remain in the viewport. Below 1000px it is
  a native modal with Back, Escape and focus restoration. Late responses cannot
  replace the current choice, and access loss clears both list and detail.
- Refresh is explicit on this library surface. Model availability and Use require
  a current installed-file/architecture inspection; the passport alone is not
  proof of compatibility.

## LoRA Training Workspace Contract

- Material, Captions, Training and Result share one dataset controller. Stage
  navigation never submits training, resets edits or disables hidden form values.
  Validation reveals the responsible stage before focusing an invalid field;
  Enter outside Training cannot launch a run.
- Desktop pairs thumbnails with one selected photo and caption. Mobile shows
  that photo, a frame strip and its editing actions. Inline and full-photo views
  share stable selection, trigger/body normalization and manual revisions.
  Selecting, saving or closing a photo does not start description requests.
- Autosave and assigning an ID to a new set preserve the selected stage.
  Explicit new/switch/restore/delete actions clear the previous run and select
  Material. An empty new set does not silently reload a different saved set.
- Result reads only the selected `run` ID, never the newest run. Reload restores
  that selection without another POST. Late responses cannot replace a newer
  choice; hidden pages pause polling and terminal states stop it. A connection
  failure preserves the last confirmed state, not a fabricated job failure.
- Source links use the recorded immutable version. Passport, use/check and
  download actions lead to the existing owner-scoped surfaces. Unsupported
  checkpoint continuation is not offered; failed-run retention remains visible.
- Cancellation returns to the focused run only for exact
  `return_view=workspace`; default library behavior, ownership and CSRF checks
  remain unchanged. Arbitrary return URLs are never accepted.
- `lora-training-workspace.js/css` own stages and responsive composition.
  `lora-caption-panel.js/css` own shared photo presentation; the existing
  dataset editor/controller owns persistence, jobs, uploads and conflicts.

## LoRA Verification Contract

- `lora-verification.js` owns test setup, confirmation, submission recovery and
  persisted comparison/history. `generate.js` provides ordinary form capture,
  input preparation, source transfer, draft notification and media rendering.
- A test varies only the selected adapter across paired prompt/seed cases.
  Baseline clears its name and MODEL/CLIP strengths; mandatory workflow adapters
  remain disclosed. Ordinary batches cannot impersonate this special mode.
- The exact count and quota precede explicit enqueue. Confirmation is bound to
  current parameters and source evidence. A duplicate unchanged input/change
  event is not an edit; do not recreate the submit DOM during a pointer gesture.
- Persist the owner-keyed pending request/source IDs before sending. Failure to
  persist prevents submission; lost responses recover by that ID without an
  automatic new POST. Prompts and test plans remain in encrypted server storage.
- Saved comparison retains missing/failed members, uses existing sensitive-media
  handling, and links to the actual source/jobs. Hidden pages pause polling;
  access failure clears images. Cancellation uses the ordinary batch endpoint.
- Full-image fit/1:1, Escape and focus behavior must work in all three engines.
  Check text within action buttons at 320px, not only document overflow.

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

## Invitation Composer Contract

- `admin-access.js` exclusively owns template lookup/application, revision-aware
  template writes and the invitation summary. `app.js` must not add a second
  owner for these fields. `admin-access.css` owns the unframed composition and
  native template dialog; shared row controls may use `ui-choice--compact` for
  a 44px target without changing other row variants.
- Saved templates are explicit copies into the editor, not live links to issued
  invitations. Preserve exact string IDs/revisions, input on errors/conflicts,
  and reject responses after the picker closes or a newer lookup begins.
- Photo and video daily/total limits are independent. Unlimited is a checkbox,
  not an ambiguous blank/zero. Keep inactive values submitted and invalid fields
  reachable; generation availability must not erase training-only priority.
- Link lifetime and account lifetime are separate. The displayed server review
  must match submitted settings before creating the invitation; an edit revokes
  the reviewed state. Native form review remains functional without JavaScript.
- Verify both themes, desktop/mobile/320px, focus, long names, pagination, lost
  mutations and no-JavaScript review. List/profile redesign is a separate scope.

## Access Lists Contract

- User and invitation lists share native GET filters and 50-row pagination.
  Preserve filter URLs through reload and invitation mutations; filter changes
  reset the offset. Counts and rows come from the same database snapshot.
- User capabilities are effective access, including account state and role.
  Invitation capabilities are the settings granted on activation. Do not show
  inactive photo/video families as unlimited, or conflate account and link
  expiry. Keep daily and total remainders separate for photo and video.
- `admin-access.css` owns list layouts; `_access_list.html` owns filters,
  pagination and invitation rows. The existing access-summary partial owns
  the detailed rights vocabulary. Complex editing remains outside list rows.
- Narrow lists wrap into labelled rows. Keep controls reachable at 320px,
  exact IDs as decimal strings, and native fallbacks for icon-only navigation.
- Align an initial invitation-list hash after conditional fields settle.
  Verify native anchor visibility before screenshots. Capture long lists as
  components using the existing chrome-exclusion stylesheet; do not let a
  previous full-page capture determine another page's visual framing.

## Admin User Profile Contract

- The profile uses native GET routes for access, tasks, activity and sessions;
  browser history and no-JavaScript operation must remain functional. Load the
  model catalog only for access editing, never for unrelated profile tabs.
- The access form reuses the invitation quota partial and shared access summary.
  Keep configured rights distinct from effective access and current remaining
  quotas. Invalid submissions retain values; a server review never saves them.
- `admin-user-access.js` owns profile conditional fields and dirty state;
  `admin-access.js` owns the shared normalization/summary API. Do not create a
  second rights or quota model in `app.js` or a template-specific parser.
- Missing saved policy selections remain visible and checked when the model
  catalog is unavailable. Ordinary save must not remove an unavailable allowlist.
- Profile tasks are owner-scoped administrative views. Do not reuse creator-only
  links or previews for a different user. Sessions/activity use bounded pages,
  literal filters and snapshot-consistent counts; never render session hashes or
  request query strings. A single revoke preserves filters and targets one owner.
- Keep dangerous account actions confirmed, administrator rights immutable in
  this form, and the current actor session unavailable for single-row revoke.
  Verify themes, all four widths, 320px, empty/error/account states and native
  keyboard save. In tests, wait for confirmed mutation navigation before running
  page-settling scripts, especially after the custom confirmation dialog.

## Admin Operations Contract

- `/admin` is an operations workbench, not a traffic dashboard. The first viewport prioritizes actionable problems, durable jobs, the ComfyUI queue and dependency freshness; aggregate traffic remains in `/admin/metrics`.
- Every attention row names the condition, affected count and next diagnostic action. Derived queue or workflow warnings must not duplicate the primary dependency outage that caused them.
- Desktop uses two independent content columns so a long dependency list cannot stretch unrelated cards or create empty grid rows. At 1180 px and below, the columns become one ordered flow: jobs, dependencies, workflow, maintenance and storage.
- Healthy workers stay collapsed; failures, stale data and retries remain visible. Links lead to the specific job, service, workflow matrix, content queue or storage policy rather than a generic dashboard.
- Resource history uses real timestamps and states its period, sample count, min, max and current values. Generation starts appear as markers; gauges are reserved for current CPU, RAM, GPU and VRAM values.
- The canonical operations preview must include running and overdue jobs, a mixed dependency state, a worker retry, storage growth and generation failures so visual regression cannot pass on an unrealistically empty page.

## Storage Workspace Contract

- `storage.css` owns the unframed category workspace, object rows and dialogs.
  Tables opt out of the old mobile card transformation through `compact-table`;
  storage owns their labelled narrow layout. Icon actions retain 44px targets
  inside their table bounds. Shared controls, tokens and focus behavior remain.
- ComfyUI registry, read-only host observations, training objects and database
  facts are distinct scopes. Never combine expected registry bytes, observed
  logical bytes, database bytes and confirmed physical deletion in one total.
  Training size summaries describe the current page. An unreadable root is
  unknown, not an empty directory. Partial scans retain their reason and scope.
- Registry selection can inspect a fingerprint without authorizing cleanup.
  Preparing deletion excludes retained, active, unconfirmed and unversioned
  files. Source models, trained artifacts and unmanaged host observations stay
  protected; only server-approved rebuildable training objects are selectable.
- `storage-state.js` owns labels, eligibility and bounded owner-keyed recovery
  pointers. `storage-operations.js` owns the native operation dialog and durable
  lifecycle. `storage.js` owns list reads, filters, selection and read-only file
  inspection. No raw JSON is the main interaction surface.
- Persist and read back the exact operation ID/selection before preparation or
  confirmation. Lost replies, closing and 404 never authorize another deletion.
  Only an unconfirmed preparation can be retried under the same ID or forgotten;
  a saved server preview is cancelled on the server. Active operations reconcile
  the same ID explicitly. Confirmed terminal results clear a matching pointer.
- Confirm/cancel/reconcile capabilities come from exact server-side actor
  comparisons and lifecycle state. Confirmation also requires current inspected
  file identity and a nonexpired preview. Cross-tab changes revoke actions even
  when an older read arrives later; another explicit read is required.
- Lists refresh explicitly and discard late pages. Dialog header/footer remain
  reachable while the body scrolls. Closing restores a live trigger or recovery
  action; access loss clears private facts and locks further operation requests.
  Database facts remain server-rendered when JavaScript is unavailable.
- Verify synthetic list/cleanup/recovery flows in Chromium, Firefox and WebKit,
  both themes, supported widths, 320px, enlarged layout, keyboard and axe. Preview
  success is not proof of production asset registration: test the real static
  handlers and version inventory separately.

## Suggestion Review Contract

- `FEATURE_SUGGESTIONS_ENABLED` controls only public intake and its user navigation entry. The administrative review queue remains available when intake is hidden, and toggling the flag never migrates or deletes stored proposals.
- A proposal follows one visible lifecycle: draft, submitted, scanning, review, accepted or rejected. Drafts are owner-only and editable; submitted proposals cannot be silently rewritten.
- Text is sufficient for submission. Up to three source links and one valid JSON file up to 5 MB are optional. The selected filename is shown inside the upload control, and an already saved draft attachment remains explicit until replaced or removed.
- Users see the lifecycle status and administrator comment without VirusTotal identifiers, counters or raw errors. Administrators see per-source diagnostics, but an unchecked link is not clickable and JSON is downloadable only after the saved file itself has a clean result.
- Accepting a proposal records a review decision; it never installs nodes, loads a workflow or copies a model or LoRA into ComfyUI. Rejection requires a useful user-facing comment.
- Desktop intake keeps composition and personal history in two readable columns. They stack before either column becomes cramped; narrow-screen review actions become full-width without changing decision order.
- Canonical previews cover draft, scanning, review, accepted, text-only and deleted-author states. Interaction tests select a JSON file, open diagnostics and verify the only available safe review actions.

## Operations Refresh Ownership

`operations.js` is the only read owner on `[data-operations-page]`. It coalesces
manual, timer and visibility reads of the admin-only Operations endpoint.
The server-rendered snapshot owns jobs, attention, maintenance grouping and
summary facts; `app.js` renders chart data received through `operations:snapshot`
and must not also poll `/admin/system/overview` on this page. Disclosure/focus
and chart choices survive refresh. Transient failures explicitly mark retained
data; access loss removes private content and stops reads. GPU counts describe
recorded Gateway work, never proof that an external executor or physical GPU
is idle. Refer to `../verification/OPERATIONS_WORKSPACE_2026-09-13.md` for coverage.

## AI Content Ownership

`admin-content.js` owns the AI Content list, inspector and optional reads.
Do not restore a second content SSE/polling owner in `app.js`. Revisions trigger
one coalesced full read using the existing filters; they do not replay writes.
The common live-events module still owns stream reconnect and poll primitives.
Rows and inspector templates share task keys; counts and retention come from
the same applied response. Preserve the active tab, focus and unchanged media
nodes. A reveal belongs to the media identity/classification, never just the
task key. New sensitive results require a fresh reveal unless the user's
global preference explicitly allows them. Leaving the result/page pauses video.
Transient failure retains marked data and exposes retry; access loss removes
all private page content and stops reads. Recent-task search must not be
presented as an exhaustive history search. See
`../verification/AI_CONTENT_WORKSPACE_2026-09-13.md` for coverage and limitations.

## Workflow Compatibility Ownership

`workflow_compatibility_admin.go` remains the owner of representative graph
construction and schema validation. `workflow_compatibility_details.go` projects
versions, graph-required classes, manifest options and native GET filters from
that same result. Never infer a manifest version from another workflow sharing
the template ID. Optional catalog availability is not a preflight result; an
inactive missing option does not invalidate the base scenario. File names come
from the existing execution-component parser, not a second dependency catalog,
and do not prove hashes or loaded bytes. Stale schema evidence stays marked.

The page has native admin/CSRF-protected refresh and no additional read loop.
Filters, disclosures and retry remain usable without JavaScript. Keep its CSS
in admin-workflows.css and in the real static routing/version inventory.
See `../verification/WORKFLOW_WORKSPACE_2026-09-13.md` for evidence boundaries.

## Administrative Training Ownership

The admin training page uses server-owned full-history filters, aggregates and
25-row pagination. Keep public task access owner-scoped; the explicit admin GPU
adapter accepts training identities only and reuses the existing projection.
Domain jobs must not be serialized into the overview: render the escaped page
fragment and retain the existing action handlers, CSRF and fixed-path redirects.

admin-training.js is the sole list refresh owner. task-gpu.js independently owns
GPU positions and their monotonic freshness; a list read must not renew an old
queue position. Focused rows and native confirmations defer replacement, even
for an already pending response. Preserve the original form/submitter, open
details and independent GPU nodes. Do not replay writes on resume. Access loss
clears private rows and confirmation text and restores a useful focus target.

Keep layout in admin-training.css, not a second style.css owner. Action icons
have stable dimensions and their labels must remain single-line at tablet widths.
See `../verification/ADMIN_TRAINING_WORKSPACE_2026-09-13.md` for evidence boundaries.

## Services And Mining Ownership

`admin-resources.go` owns the read-only projection for ComfyUI, OpenWebUI and
mining. Render escaped fragments rather than serializing domain jobs, lease
identities, private paths or tokens. Dependency checks, executor observations,
recorded GPU work and mining profile checks have separate timestamps and scopes.
No absence of recorded work proves that the physical GPU is free.

`admin-resources.js` is the sole refresh owner. Preserve native forms and their
submitters; defer reads during confirmation and never replay a POST on resume.
Configuration changes freeze commands until a full page reload. Any stale
profile blocks start/update eligibility; the server still validates every action.
Transient errors retain marked data, while access loss clears private content,
alerts and pending confirmation. The script display is captured at page open.

Keep page layout in `admin-resources.css` and register both assets in real
handlers and the version inventory. Preserve no-JavaScript forms and service
links. See `../verification/SERVICES_MINING_WORKSPACE_2026-09-14.md` for evidence.

## Updates Ownership

`admin_updates_view.go` separates the running Gateway build from host source
revisions and projects per-component check evidence. Missing/stale/error data
must not imply current versions or installation readiness. Agent errors during
installation leave an unconfirmed outcome; a returned command is not proof that
every selected component changed. Keep raw diagnostics secondary and escaped.

`admin-updates.js` owns selection and the in-page submission lock, not polling or
the installer lifecycle. Retain native forms, exact direct/batch targets and the
shared confirmation. The server requires admin, CSRF and `confirm_install=1`;
without JavaScript the explicit confirmation checkbox remains usable. Restore
local controls on pageshow without replaying a command. Preserve valid selections
when validation fails, and use the GET refresh link to obtain new state.

Keep layout in `admin-updates.css`. The UI guard does not supply a durable
installation receipt, exactly-once execution or rollback; those remain F14
coordinator responsibilities. See `../verification/UPDATES_WORKSPACE_2026-09-14.md`.

## Metrics Ownership

`admin_metrics.go` owns the read-only metrics page. Native GET requests select
1 hour, 24 hours or 7 days; the displayed UTC window is lower-inclusive and
upper-exclusive. Generation outcomes and dependency latency use that same
window. Queue timing uses starts in the window, execution timing uses finishes
with a recorded start. Unknown timing and a success rate without completed or
failed jobs remain unavailable, not measured zero.

`store/admin_metrics.go` reads bounded buckets of retained Gateway snapshots.
The chart shows observed peaks, sample counts and missing buckets, never an
interpolated continuous history. Keep the native details table available without
JavaScript and make horizontal table regions keyboard focusable. Current query
counts and cached host/Gateway observations have separate timestamps and labels;
opening the page must not capture observations or probe dependencies.

Keep page layout in `admin-metrics.css`. HTTP aggregates retain their existing
day/7-day/30-day contracts and must be labelled separately from the selected
generation window. Limit disclosures must remain explicit for outcome groups
and recent failures. Use the shared `renderStatus` for no-store errors and CSP.
See `../verification/METRICS_WORKSPACE_2026-09-14.md` for verification boundaries.

## Audit And Session Lists

`admin_security_lists.go` owns native GET filters and canonical page links;
`store/admin_security_lists.go` reads 50 rows with a coherent count and timestamp
inside a read-only repeatable-read transaction. Audit pages retain an exclusive
UTC cutoff, inclusive start date and exclusive day-after-end date. Exact action
and object filters coexist with literal substring search; wildcard characters
are not SQL patterns. A refresh takes a new cutoff.

Keep CSV as the whole retained journal before its cutoff, not the visible page
or filter result. Preserve the existing nine-column/BOM contract. Long raw JSON,
device strings and request/correlation IDs belong in escaped native details;
format JSON without floating-point conversion. Session token hashes never
belong in the view model.

Session validity includes idle/session deadlines and account disable/expiry.
The admin revoke action requires CSRF and an explicit confirmation, deletes
once with the returned token hash, and closes matching local WebSockets. Own
session revocation clears the cookie and redirects to login. The shared dialog
owns confirmation; `admin-security.js` only coordinates native submission and
the in-page lock. Unknown outcomes require a GET before another action, and
bfcache restoration never repeats a POST. Native checkboxes remain available
without JavaScript. Layout and no-JavaScript scroll offsets belong in
`admin-security.css`; keep additional audit filters in a native disclosure.

## Suggestions

Keep public intake behind `FeatureSuggestionsEnabled`; redesigning either page
must not enable it. The administrative queue remains available with intake off.
The existing last-50 user and last-200 admin list contracts are unchanged and
labelled when full. Draft attachment counts use saved links and JSON, not scans
that do not yet exist. Long text and diagnostics use escaped native disclosures.

`suggestions.css` owns both page layouts; no legacy suggestion block remains in
the global stylesheet. `suggestions.js` enhances native forms without replacing
their POST/redirect flow, locks duplicate submissions, and restores disabled
states on bfcache return without replaying a write. Shared `app.js` owns the
delete dialog. Without JavaScript, deletion requires a native checkbox; the
server requires exactly one `confirm_delete=1` plus CSRF and draft ownership.

Keep rejection comments after validation errors. Unknown decision/retry outcomes
require a fresh queue load before another action; database load failure is not
an empty queue. Decision/delete/retry bodies are bounded to 64 KiB. Model or
workflow installation is never a side effect of accepting a suggestion.
Unsafe links remain text, unsafe JSON has no download action, and safe JSON
links use native `download` as well as the existing attachment response headers.

## Own Account Settings

Own account pages share one navigation: profile, notifications, effective
access/quotas, password and sessions. Retain the current creator/admin listener
and shell; keep the old profile hash destinations reachable. Theme selection
uses theme.js and its cookie, with a CSRF-protected native POST for no-JS.
Sensitive viewing remains browser-local under the existing app.js owner.
Notification permission belongs to notifications.js, not a second controller.

Use effective domain access and real remaining image/video quotas, with Moscow
daily boundaries. Retained proxy requests are not generation counts. Do not
invent individual training quotas or show missing quota/preferences data as
zero, unlimited or default settings. Only administrators can rename their own
username or change their own priority/mining policy; these policies stay
independent. Preserve the notification JSON and legacy priority response paths.

Account forms remain native POST/redirect forms. Preserve non-secret values and
field-associated errors. Never echo or persist passwords. Password change and
revocation of other stored sessions commit together, guarded by the exact hash
that was verified. Keep the current session and close related local WebSockets
after successful commit. A competing change must not be silently overwritten.
An uncertain mutation disables another write until a fresh GET or login check.

Own session pages use owner-scoped 50-row paging and literal device/IP search,
with current/others and deterministic activity order. Show the minimum of
absolute, idle and account deadlines. Keep raw device values escaped in native
details; never expose token hashes. Revoke requires CSRF and exactly one explicit
confirmation, excludes the current session, and never reaches another owner.
Revoke-others includes hidden-by-filter sessions and keeps the current browser.

app.js owns the confirmation dialog; account-pages.js coordinates native submit,
duplicate-write prevention, independent password visibility and history restore.
No-JS confirmation remains a required native checkbox. Restored session history
performs a canonical GET, never a replayed POST. Account layout uses available
container width, not only viewport width, including 200% zoom and 320px screens.

## AIHUB Frame Generation

Keep DLSS FG independent from DLSS image enhancement and RTX upscaling. In the
processing dialog, a native opt-in checkbox enables a native 48/72/96 FPS
selector (2x/3x/4x from the 24 FPS AIHUB timeline). Default is off. The controls
are available only for AIHUB photo/video modes; carousel and ordinary workflows
hide and disable them. Existing draft and replay ownership must preserve the
selection without introducing a second persistence controller.

The launch summary and result passport use the selected output FPS. Backend
preflight validates expanded frame memory after reference duration resolution;
unresolved duration cannot reach graph dispatch. Interpolation changes neither
playback speed nor audio selection. Frame count includes the final source
frame's full display interval. Test independent toggles, restoration, mode
switches, text containment, and real decoded output at mobile and desktop sizes.

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

Direct Playwright runs can select `PLAYWRIGHT_BROWSER=firefox` or `webkit`;
the default remains Chromium. Non-Chromium screenshot baselines have separate
browser directories. Use separate output directories for each engine, and
never treat missing baselines as permission to regenerate them automatically.
