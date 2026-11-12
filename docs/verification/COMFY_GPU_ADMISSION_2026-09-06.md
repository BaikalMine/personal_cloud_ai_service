# ComfyUI Executor Admission

## Scope

This is the next F04 stage after commit `f361280` (owned Windows inference
recovery). No installed ComfyUI, Ollama, miner, production service or production
database has been changed. The backend and repository-only adapter have passed
synthetic local verification; this is not real ComfyUI/GPU or rollout acceptance.

The previous F04 observer samples `/queue`, training activity and loaded Ollama
models. Such observations alone cannot stop a direct ComfyUI submission from
starting immediately after a Gateway GPU claim. The native executor needs to
participate before validating or enqueuing a prompt.

## Added Backend

- Migration 69, `gpu_executor_permits`, records a scoped ComfyUI permit with
  resource, random request/instance identity, hashed token and optional managed
  GPU work/lease binding. States: reserved, running, closed. The table admits one
  active native permit per GPU and retains terminal identities against replay.
- External reservation needs a fresh idle observation, no held Gateway work and
  no ready internal queue item. Managed reservation validates the already held
  generation work and lease. External calls have no authenticated account policy:
  they cannot automatically stop mining and must wait for it to be stopped.
- Start grants execution only once. Completion accepts only the matching local
  executor's `not_started` or `quiesced` evidence. There is no TTL-based release.
- AcquireGPUWork checks the native permit even after a stale idle observation.
  ReleaseGPUWork cannot release a managed generation while its native permit is
  active, even if ComfyUI history already reports a terminal job.
- Admin-listener callback route `/internal/gpu/comfy/permit` is LAN-gated and uses a
  separate bearer token, bounded strict JSON and explicit reserve/start/finish/
  status actions. Restore-drill mode refuses this API.
- `COMFY_GPU_GUARD_REQUIRED` requires mining command fencing and a dedicated
  32-256 printable ASCII character `COMFY_GPU_GUARD_TOKEN`. Default is disabled. Gateway probes
  `/ai-gateway/gpu/status` with a private key header when this mode is required.
- Managed generation requests transmit their GPU work/lease in private headers
  after the existing beforeSend source fence. Raw reverse-proxy requests strip
  all client-supplied `X-AI-Gateway-GPU-*` headers. Secrets must never enter prompt
  metadata, PNG metadata or ComfyUI history.

## Executor Adapter

The [repository-only adapter](../../integrations/comfyui_gpu_guard/README.md) uses
aiohttp middleware before `/prompt` or `/api/prompt` validation, plus ownership
checks on queue put/get and executor execute. One request task can enqueue once.
Credentials are removed before the original handler and remain out of history.

Completion waits beyond `task_done`: the worker must reach its next `get`, after
its output postprocessing and the request handler's return. Only then are its
executor caches reset, tracked models unloaded, all CUDA devices synchronized
and caches emptied. Remaining allocated or reserved PyTorch CUDA memory rejects
completion. Pending queue deletion/wipe has its own cleanup path; running wipe,
unexpected validator exceptions/cancellation and failed cleanup do not release.

Only locally proved terminal receipts are persisted in SQLite with full
synchronous commits. Lost callback responses retry idempotently. New processes
replay those receipts but never infer completion of unknown old execution.
Invalid configuration leaves rejecting hooks and prevents application startup.

## Verification

- Full sequential Go suite with an isolated PostgreSQL 16 and the Python protocol
  fixture passed: `go test -p 1 -count=1 -timeout=180s ./...`. Gateway took about
  156 seconds, inference 56 seconds and store 19 seconds. `go vet ./...` passed.
- New store tests cover idempotent reserve/finish, one-shot start, wrong proof
  identity, stale idle observations, waiting Gateway priority, terminal replay,
  managed release and 30 reserve-vs-enqueue/claim races per run. Every race admits
  exactly one side.
- New Go HTTP tests cover disabled/wrong-key/method/oversize/trailing JSON, restore
  drill, strict status protocol and identity, bounded replies and removal of all
  case variants of private proxy headers. Configuration tests cover required
  mining fencing, key size/characters and the disabled default.
- 22 Python tests passed with `-B -W error::ResourceWarning` in the isolated test
  image (Python 3.14.7, aiohttp 3.13.5). They cover worker postprocessing boundaries,
  one-shot/private submission, busy concurrent requests, detached submission,
  malformed JSON before reservation, failed/canceled validation, pending deletion, running wipe, duplicate cleanup,
  lost start/finish responses, journal reopening, hook replacement, malformed or
  redirected callback responses, and all-device CUDA cleanup on synthetic APIs.
- `TestComfyGPUProtocolIntegration` runs the actual Python adapter and Go callback
  against PostgreSQL: external work blocks a Gateway claim, managed headers bind
  to the right owner/lease, history cannot prematurely release it, and killing
  the synthetic Python executor leaves an unknown permit that its new process
  cannot close. The test launches no real model or GPU work.
- After the final strict key/probe changes, the new Go config/gateway/store tests
  passed three repetitions (including 90 reserve/claim races and three native
  fixture restarts). The new `scripts/test-comfy-gpu-guard.ps1` gate passed twice,
  including the final JSON parsing change. It is called by the normal non-Quick
  repository gate and uses only its own uniquely named, unpublished tmpfs test
  database. Both gate databases and the manually created integration database were
  removed. Focused `vet` for the final Go changes also passed. The complete UI/JS
  gate was not rerun in this backend/adapter stage.

## Remaining Acceptance

1. Pin and verify an installed ComfyUI/node/runtime compatibility set in an
   isolated copy. Detached custom-node work, non-PyTorch CUDA allocations and
   alternate executors cannot be proved quiescent by this adapter alone. CPU-only
   and non-CUDA execution are not supported by its real quiescer.
2. Direct Ollama/OpenWebUI admission and old unowned unknown model calls remain
   separate F04 work. This does not close all external execution races.
3. Run real mixed GPU workloads, cancellation and memory-release acceptance only
   after the required operator approval. The working ComfyUI and launcher remain
   unchanged; the adapter has not been installed there.
4. F14 must include the private receipt journal in coordinated backup/restore,
   compatibility manifests, operator reconciliation and rollback. There is no
   force-release API and no timeout-based deletion of unknown permits.
5. Perform the coordinated opt-in rollout and verify authenticated guard status
   before admitting work or mining. F04 and the overall goal remain open.

## API References

The upstream [ComfyUI server](https://github.com/Comfy-Org/ComfyUI/blob/master/server.py)
constructs an aiohttp application and its prompt queue. Its ordinary on-prompt
callback facility is not a fail-closed admission boundary because exceptions are
caught by the server. The [queue implementation](https://github.com/Comfy-Org/ComfyUI/blob/master/execution.py)
and [worker](https://github.com/Comfy-Org/ComfyUI/blob/master/main.py) need targeted
compatibility checks before the planned adapter is enabled. These are references,
not evidence that the locally installed process has been modified or verified.
