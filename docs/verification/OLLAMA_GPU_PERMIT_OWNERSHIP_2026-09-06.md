# Owned Ollama GPU Permit Ownership

Date: 2026-09-06. Scope: F04 persistence and recovery foundation only.
This does not yet admit external Ollama HTTP requests or reroute OpenWebUI.

## Changes

- Migration 70 extends durable executor permits to Ollama with a registered
  native owner ID, generation and hashed session token. Existing ComfyUI permits
  retain their original scope and cannot acquire an Ollama owner.
- Ollama permits cannot adopt a managed ComfyUI work/lease. New reservations and
  one-shot starts validate the current registered generation inside their
  transaction. Old identities remain queryable but cannot reopen closed work.
- Recovery closes only older Ollama permits of the same native owner, after the
  owned worker has confirmed process-tree drain and registered its generation.
  Current generations, foreign owners and ComfyUI are excluded. Recovery also
  invalidates the previous GPU observation; a fresh observation is still needed.
- The owned worker performs this recovery before becoming ready. A failed native
  drain cannot reach registration/recovery. Windows-owned startup now requires
  migration 70; direct-mode startup's existing schema requirement is unchanged.
- The existing ComfyUI endpoint explicitly fixes executor scope and rejects an
  Ollama request. There is no new Ollama endpoint or credential in this change.

## Verification

All database tests used a dedicated PostgreSQL 16 container with synthetic
credentials and no published ports. No model, real GPU job, production database,
ComfyUI process, OpenWebUI configuration or host listener was changed.

- Migration 69 -> 70 preserves a running ComfyUI permit and is idempotent.
  The database rejects an ownerless Ollama permit.
- Store tests verify owner registration/token/generation checks, stale start and
  recovery rejection, current/foreign/ComfyUI isolation, idempotent recovery,
  closed identity replay and invalidation of a stale idle observation.
- Five worker recovery scenarios passed: same owner, foreign owner, current
  generation, failed native drain and ComfyUI. None invoke the model executor.
- A complete sequential `go test -p 1 -count=1 -timeout=240s ./...` passed with
  the isolated PostgreSQL. After that run, the existing claim-race test was
  extended to Ollama as well as ComfyUI.
- Final focused database/store/inference/Gateway tests passed three repetitions,
  including 90 Ollama-versus-Gateway and 90 ComfyUI-versus-Gateway admission races.
  Exactly one contender obtained admission in each race.
- `go vet ./...` passed. The final changes do not modify native Windows process
  code; native crash tests and UI/JS checks were not rerun in this stage.
- The focused ComfyUI config/Gateway/store regression also passed with
  `COMFY_GUARD_TEST_PYTHON=python3`, including the real Python/Go/database protocol
  fixture. The temporary database was removed after verification.

## Remaining Work

Follow-up implementation: [guarded HTTP admission](OLLAMA_HTTP_ADMISSION_2026-09-06.md).
The ownership foundation below is now used by that opt-in adapter; it does not
by itself prove that existing clients have been migrated.

The next step is an opt-in HTTP admission adapter around the existing owned
streaming runtime. It must reserve before native startup, serialize with managed
inference, retain permits on uncertain stop/persistence outcomes, and authenticate
both the client listener and Gateway admission channel. Route/method allowlists,
stream cancellation and lost acknowledgements need adapter-level tests.

Ordinary Ollama, direct OpenWebUI calls and existing unknown unowned work are not
covered by this recovery. Integration, real mixed-GPU acceptance, coordinated
rollout and the remaining F04 criteria are still open. F06 follows F04.
