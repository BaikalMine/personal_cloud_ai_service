# F04: mining leases for local inference

Status: implemented and verified locally. Not deployed. F04 remains open.

## Defects fixed

- Maintenance released anonymous assistant/caption mining leases after two
  minutes even while the model request was still running.
- Both request paths released their lease on any return, including a proxy 504,
  cancelled request, lost connection or incomplete response.
- The last mining lease was deleted before contacting the Windows agent.
  A Gateway exit during that call could lose the pending resume operation.

## Contract

- The assistant client reports execution evidence separately from the quality
  of the generated text. Local validation before HTTP dispatch is settled;
  network dispatch makes execution uncertain until a complete successful
  Ollama response explicitly reports `done: true`.
- Missing/false `done`, malformed/truncated/oversized responses, HTTP errors and
  request cancellation do not prove completion. This includes HTTP 4xx because
  an intermediary response is not an executor receipt.
- Both the prompt-assistant handler and durable LoRA caption worker use this
  evidence when releasing their mining lease. A valid executor completion can
  release it even when the generated content is empty or invalid.
- Migration 59 adds `resume_ready`, initially false for every existing lease.
  No legacy anonymous lease is assumed finished from its age. Completion is
  stored before attempting resume; the last row stays durable through agent I/O.
- Maintenance retries confirmed completion after agent unavailability or a
  Gateway restart. If an earlier Start succeeded but its reply was lost, the
  next state check observes the running miner and does not issue another Start.
- Existing mining policy is unchanged. This does not force mining pauses on
  users who do not have that policy, change queue priority, or modify ComfyUI.

## Verification

- Full uncached `go test -p 1 -count=1 -timeout=180s ./...` passed with a separate
  tmpfs PostgreSQL 16 database. Production data was not used.
- Client tests cover both assistant and caption: final response, final response
  with empty content, missing/false done, 504, 400, malformed JSON, oversized
  response, cancelled transport and local validation before dispatch.
- Eight gateway integration scenarios exercise actual assistant/caption code:
  success, proxy timeout and cancellation for each path; unavailable agent and
  lost Start reply across new App instances; two leases finishing in sequence.
  The complete eight-scenario test also passed five consecutive uncached runs.
- During each active model call, its lease is aged by one day and maintenance
  runs through a fresh App instance. No Start is emitted. The fake agent also
  checks that completion remains in PostgreSQL at the exact Start boundary.
- Migration test verifies existing unknown work stays unconfirmed and repeat
  migration preserves explicit completion.
- The first cancellation test run exposed a test-server cleanup problem: the
  fake server had not consumed the request body. Its body is now drained and
  the reply gate is always released on cleanup; the repeated and full runs pass.
- No templates, CSS, JS or browser behavior changed; no new visual acceptance
  claim is made. No real model, training, mining or ComfyUI process was started
  or stopped by these tests.

## Remaining F04 work and rollout

This change protects mining lifecycle; it is not the unified GPU admission
integration. Generation, training, captions and assistant still need common
dispatch, wait reasons and executor reconciliation. Caption retry and other
model requests are not yet globally fenced by these mining leases.

An uncertain inference lease intentionally remains held. Automatic resolution
needs positive executor/host evidence; a timeout, empty model inventory or
another successful model response is not sufficient. Operator-facing recovery
and cooperation with external ComfyUI/OpenWebUI work remain open. The existing
in-process mining mutex is not a distributed executor fence.

Rollout needs a new Gateway build and migration 59. No agent or ComfyUI restart
is needed for this substep. As with migration 58, older binaries reject newer
schema versions, so binary-only rollback is not a valid rollback plan.
