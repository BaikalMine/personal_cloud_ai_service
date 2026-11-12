# Owned Inference Recovery

## Scope

F04 now connects the native Windows model runtime to permanent inference claims.
The existing direct executor remains the default. No installed worker, Ollama,
ComfyUI, miner, production environment, or production database was changed.

## Contracts

- Migration 68 adds executor identity/generation and the distinct
  `executor_stopped` outcome. A stopped request is a failed execution, not a
  successful model response and not permission to silently resubmit the request.
- The native runtime derives its identity from the Windows installation and
  canonical state directory. An exclusive process-owner lock precedes identity
  issuance. It drains its previous Job Object before writing a new generation
  and random token to the persistent state file.
- Database registration is monotonic and idempotent for the same token. A late
  older registration or a different token for the same generation is rejected.
  Claim locks and validates this registration in the dispatch transaction.
- Startup replays encrypted outcome receipts first, preserving successful results
  and their original retention. Only then does it reconcile older running/uncertain
  requests bound to this exact executor identity after native drain confirmation.
- Direct/unbound calls, other executor identities, and current generations are
  never settled by this recovery. Missing/corrupt/rolled-back owner state cannot
  bypass database generation fencing. Do not delete it as an operational reset.
- Timeout, malformed model output and connection failure become stopped only
  after native process-tree termination. Missing stop proof leaves the request
  uncertain, marks the owned worker unready and requires a new confirmed drain.
- Store, client, GPU release evidence, caption worker, prompt assistant and
  encrypted outcome journal understand the stopped outcome. GPU release remains
  a separate coordinator action using the stored evidence.
- Readiness includes `owned` and remains false until receipts and owner recovery
  finish. This is not a health-based proof of GPU idleness.
- Concurrent RunOne on the same worker returns without blocking or dispatching;
  it cannot race a native generation advance with an active request.

## Native Configuration

The native `cmd/inference-worker` supports explicit `INFERENCE_RUNTIME=windows-owned`.
It now requires schema 70 (including owned external Ollama permit recovery),
DATABASE_URL, the existing SESSION_SECRET, a persistent
INFERENCE_OUTCOME_DIR, and four explicit absolute/local settings documented in
`.env.example`: model executable, existing model directory, dedicated state
directory, and unused IPv4 loopback address. It does not require OLLAMA_UPSTREAM
in this mode. Invalid modes fail startup. Non-Windows owned startup is rejected.

Only the child's Ollama environment is changed. Models are not downloaded or
copied. The model process starts inside Execute after the durable GPU permit was
already obtained, and its tree stops before a terminal outcome is returned.

This is configuration support, not an installed service. A coordinated rollout
must stop/disable the old direct inference worker, establish an idle GPU window,
set the Gateway readiness URL to the native worker, scope listener/DB access,
and preserve the owner-state and outcome directories with the matching database.
Restoring or moving state to a different host/path requires explicit reconciliation;
it must not manufacture proof for old direct calls. F14 backup integration and
release/rollback automation remain open.

## Verification

Local tests use a dedicated temporary PostgreSQL and synthetic HTTP executables.
They do not load a model or use GPU. Verified so far:

- Successful owned dispatch stores identity and returns the original encrypted
  result once; same-worker retries do not dispatch again.
- Current, other-owner and direct uncertain calls stay unsettled; only older
  same-owner requests acquire stopped evidence, which can release their permit.
- Stale registration/claim, token mismatch, failed native drain, malformed output
  and missing stop proof fail closed.
- Windows generation persists over reopen, changes its token, rejects another
  live owner, and rejects corrupt local state.
- An actual Windows worker subprocess was killed after its fixture model accepted
  the request. Server and child process handles both signaled termination. A new
  real native runtime recovered the original database request without resubmission.

The first restricted native crash test failed before dispatch with Access denied.
The authorized native run passed. This was a host API permission limitation, not
a successful process-recovery result in the restricted environment.

- Receipt replay was verified for both successful and stopped outcomes, including
  a second pass without any model invocation.
- Gateway adapters finish stopped assistant/caption requests as failed and release
  their GPU permit. An initial test found that the caption retry query omitted
  `stopped`; it now uses the shared domain Settled contract. The explicit retry
  regression passes.
- The final full sequential `go test -p 1 -count=1 -timeout=180s ./...` passed
  against the separate PostgreSQL. The initial full run failed only at the caption
  retry assertion above; the full run was repeated after fixing it.
- All new owned-inference Windows tests, including the real process crash and
  database recovery, passed ten repetitions. The entire native modelruntime test
  package passed three repetitions. Helper entry points are intentionally skipped
  by their parent processes.
- Windows `go vet` for the affected runtime/store/Gateway packages and the native
  worker cross-build passed. The built worker was not installed or started.

Real model quality, mixed GPU load,
external ComfyUI/OpenWebUI admission, existing unknown direct calls, and rollout
are not covered by these synthetic checks. F04 and the overall plan remain open.
