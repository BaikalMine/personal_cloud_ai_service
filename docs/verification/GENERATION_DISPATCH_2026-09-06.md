# Durable generation dispatch: local verification

Date: 2026-09-06. Base commit: `e6a6bc5`. Scope: the active single and batch
generation paths, not the complete F04 cross-kind GPU admission system.
Production, ComfyUI, mining and real inference were not changed or exercised.

## Runtime changes

- `/generate/run` prepares and saves the payload, reserves quota and publishes
  the job in PostgreSQL. Its HTTP 202 receipt includes a stable job/request ID;
  it no longer calls ComfyUI's prompt endpoint in the browser request.
- Single jobs and batch children use `dispatchGenerationJobs` and
  `ClaimNextGenerationDispatch`. The old independent batch claim is removed.
  The existing maintenance key `generation_batches` is retained for monitoring.
- Dispatch claims have a token and a two-minute lease. They can be reclaimed
  only before the persistent submission marker. Priority has a bounded
  ten-minute head start; older ordinary jobs can overtake it.
- Quota and payload must be durable before a single job is published. A lost
  publication reply returns the saved job receipt, not a new request identity.
- Local ComfyUI admission rejection postpones the job for five seconds. Mining
  is paused only after this admission, immediately before the submission marker.
- The marker is persisted before sending the prompt. Receipt persistence has
  its own bounded context, independent of the expired dispatch context.
- Only recognized, complete ComfyUI HTTP 400 validation responses prove that a
  prompt was rejected before enqueue. HTTP 504, a malformed receipt or an unknown
  error leaves the submission unconfirmed and does not authorize resubmission,
  quota release or mining resume. Queue/history evidence can recover its ID.
- Resource release atomically closes dispatch before touching quota or mining.
  The `dispatch_closed_at` marker prevents both stale publication and a batch
  draft claim during the gap before final job state/resource persistence.
- Cancellation before submission prevents sending. Cancellation after an
  unknown receipt remains pending. Batch cancellation reports the fresh batch
  state, not merely the absence of an HTTP error; untouched siblings are stopped.

## User experience

The existing studio is retained. A saved, unbound job displays one waiting
message with the next-check countdown and an immediately reachable Cancel
action. It has no moving progress bar, percentage or duplicate status box.
The browser retains job identity across recovery and ignores late replies after
cancellation. A terminal pre-dispatch error ends waiting without another POST.

The previous image stays visible during preparation and after a rejected HTTP
launch. Once a new launch is accepted, that image is removed from the active
result area so it cannot be mistaken for the new result; history is unchanged.

## Verification evidence

- Final `go test -p 1 -count=1 ./...`: passed with an isolated tmpfs PostgreSQL
  16 container and no test-result cache. All packages passed after the final
  `dispatch_closed_at` change. Go runtime: `golang:1.26.5-alpine`.
- `generation_dispatch_integration_test.go` uses actual handlers/store queries
  with an HTTP ComfyUI fixture. It covers receipt/idempotency, quota once,
  admission waiting, priority aging, independent worker launch, ten-minute
  waiting, HTTP 504 plus application restart and receipt recovery, validation
  rejection, pre-send cancellation, uncertain cancellation and batch siblings.
- Race scenarios cover 16 concurrent claims and 20 release-versus-send races.
  Closed jobs cannot be republished or sent; closed batch drafts cannot be claimed.
- The database integration test migrates an older schema through migration 60
  and verifies that legacy unbound preparing/uploading/waiting jobs remain
  uncertain instead of being submitted again.
- JS module checks: 96 passed after the final browser change.
- Playwright regression (generation dispatch, gateway, studio): 71 passed,
  45 expected skips of duplicate desktop-only cases, no failures. Artifacts:
  `artifacts/playwright/f04-dispatch-20260906-r3`.
- After removing the duplicate waiting message, the final dispatch/studio run
  passed all 24 cases across 390/768/1440/1920 px, without changing baselines.
  Artifacts: `artifacts/playwright/f04-dispatch-20260906-r4`.
- Final dark desktop and light mobile queue screenshots were inspected: old
  media is absent, Cancel is in the viewport, and status text fits the surface.
- Browser fixtures use the existing owned preview. JS loads from the worktree;
  HTTP queue/launch responses are intercepted by the new tests. The real handler
  and batch status changes are covered by PostgreSQL/HTTP integration tests,
  not claimed as real GPU or production browser testing.

## Defects found during verification

The first queue UI fixture missed a preflight response; that fixture was fixed.
An initial result-clearing change broke the existing failed-launch contract on
all four widths. Clearing was moved after HTTP acceptance, and the original
regression test was preserved. Test-only unused import, uninitialized telemetry
map and integer type errors were fixed before the final full Go run.

Review also found and fixed false-positive batch cancellation and the gap in
which resource release could be followed by publication/reclaim before the job
became terminal. Those have new integration coverage rather than weaker tests.

## Release and remaining F04 work

Migration 60 is new and has not been applied to production. Stop/drain only the
Gateway launch intake during coordinated rollout, never the user's ComfyUI.
Keep uncertain legacy jobs for positive executor reconciliation. This change
must not be rolled back by starting an older Gateway against the new schema:
the migration catalogue rejects unknown/newer versions, and old dispatch logic
does not understand queued-but-unsubmitted or uncertain new jobs. F14 still
requires an explicit forward-compatible rollback or a verified restore procedure
that preserves accepted jobs.

This step does NOT connect generation, training, captions and prompt assistance
to `AcquireGPUWork`, nor does it prove that an external ComfyUI/Ollama task cannot
race a new launch. Executor observation, fencing/reconciliation, common queue
integration, mixed GPU workloads, real mining restart and production acceptance
remain open. A missing queue/history item alone cannot settle an unknown send.
There is no automatic resubmit based solely on age or a network timeout.

The full F01-F15 and D01-D10 goal remains active. D06 is only partially advanced
by the waiting/cancel changes; its cross-job workspace is not implemented here.
