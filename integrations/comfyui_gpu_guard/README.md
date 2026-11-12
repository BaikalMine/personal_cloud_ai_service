# ComfyUI GPU Admission Adapter

Repository-only, opt-in F04 implementation. It is **not installed or enabled** in
the user's ComfyUI. Synthetic Python/HTTP/PostgreSQL tests are not real GPU or
custom-node compatibility acceptance. Do not deploy it independently of Gateway.

## Contract

- `POST /prompt` and `/api/prompt` reserve and start one persistent Gateway permit
  before entering ComfyUI validation. Busy callers get a retryable HTTP 409; the
  adapter does not maintain another waiting queue or automatically retry prompts.
- Managed Gateway calls bind to their existing GPU work and lease through private
  headers. Those headers are removed before the ComfyUI request handler. The
  adapter never writes a permit, token or lease into prompt data or history.
- One permit admits one `PromptQueue.put` from its original request task. The
  queue's `get` and `PromptExecutor.execute` also check local ownership. Unknown
  direct queue/execute paths are unsupported and fail closed.
- `task_done` records a local boundary but does not release anything. At the
  worker's next `get`, after output postprocessing and after the HTTP handler has
  returned, the adapter synchronizes every CUDA device, resets that executor,
  unloads ComfyUI-managed models and empties caches. Nonzero live **or reserved**
  PyTorch CUDA memory prevents completion. CPU-only and non-CUDA runtimes are
  intentionally not accepted by the real quiescer.
- A normal validation rejection or pending deletion/wipe performs cleanup before
  completion. A running task is not completed by a queue wipe. Unexpected handler
  exceptions, cancellation, lost execution or failed cleanup retain the permit.
- Only proved `not_started` or `quiesced` outcomes enter the local SQLite journal.
  Their idempotent callbacks survive lost responses and process restarts. An
  empty queue, a new process, a timeout or ComfyUI history is never crash evidence.
- `/ai-gateway/gpu/status` requires the dedicated key and checks that the hooks
  remain installed. Invalid configuration leaves rejecting hooks and fails app
  startup. Removing/disabling the whole custom-node package is outside this
  mechanism: coordinated deployment must verify its authenticated status.

## Compatibility Limits

This adapter targets the synchronous `PromptExecutor.execute` / `PromptQueue`
worker contract reviewed in the upstream
[server](https://github.com/Comfy-Org/ComfyUI/blob/master/server.py),
[queue](https://github.com/Comfy-Org/ComfyUI/blob/master/execution.py) and
[worker](https://github.com/Comfy-Org/ComfyUI/blob/master/main.py).
These moving references describe the API research, not a pinned supported release.
The installed core `0.34.0` at `8a33128f2f8c5585c57486c07de481241e70a39c` has a
[CPU-only contract verification](../../docs/verification/COMFY_INSTALLED_CORE_2026-09-06.md)
using its real HTTP server, queue and worker. This is not GPU cleanup or node-pack
acceptance; those remain rollout prerequisites.

Arbitrary custom nodes can spawn detached processes/threads, retain untracked GPU
allocations, submit CUDA work later or replace core hooks. CUDA synchronization
cannot prove the end of that work. Such nodes and alternate executors must not be
declared supported by this adapter without their own completion contract. Native
CUDA allocations outside PyTorch are also not measured by `memory_allocated`.
An audited node/runtime compatibility set and real mixed-load tests remain rollout
prerequisites. The separate owned Ollama/OpenWebUI adapter is implemented locally;
its coordinated rollout and removal of old bypasses remain unfinished F04 work.

## Coordinated Opt-In Procedure

This is an operator procedure for a later approved rollout, not an instruction to
change the current installation during development.

1. Confirm no active ComfyUI work, inventory its exact commit, installed node packs
   and CUDA runtime, and run the compatibility/cleanup smoke in an isolated copy.
   Preserve the user's existing launcher, Python environment and service layout.
2. Include migration 69 and the adapter revision in the same release manifest.
   Back up PostgreSQL and the adapter journal coherently. Do not restore either
   independently over active GPU work. The journal contains bearer-like proof
   tokens and must be private to the ComfyUI operator, never web-served or committed.
3. After explicit approval, place this package in the selected ComfyUI
   `custom_nodes` directory. Create an existing private directory for its journal.
   Use ComfyUI's existing `aiohttp`, `torch` and standard-library SQLite; do not
   install the test Docker image or change its Python dependencies.
4. Put a private `guard.json` beside `__init__.py` with these three keys:

   ```json
   {
     "endpoint": "http://127.0.0.1:8091/internal/gpu/comfy/permit",
     "token": "REPLACE_WITH_A_DEDICATED_RANDOM_64_CHARACTER_KEY",
     "receipt_db": "C:/private-gateway-state/comfy-receipts.sqlite"
   }
   ```

   The endpoint is the Gateway **admin** listener, reachable from ComfyUI and
   allowed by the existing admin network policy. Use protected local networking
   or HTTPS; never expose this callback as a public proxy route. The token must be
   a new shared guard key, not a user password, session secret or mining-agent key.
5. Configure the same `COMFY_GPU_GUARD_TOKEN` in Gateway, with
   `COMFY_GPU_GUARD_REQUIRED=true` and `MINING_FENCED_COMMANDS=true`. The default
   remains disabled. Coordinate restarting the opted-in services only after
   authorization; verify guard status before admitting any GPU work or mining.
6. Verify direct ComfyUI, embedded ComfyUI and managed generation, busy rejection,
   queue deletion, interruption, memory cleanup, callback outage and restart
   behavior. External unattributed prompts cannot invoke a user's automatic
   mining-stop policy; stop mining explicitly or use a managed account workflow.

If startup, cleanup or completion becomes uncertain, keep the permit and all
journals. Do not delete permit rows, clear journals or disable the guard to make
the queue move. Investigate the exact executor instance and GPU work first.
Operator reconciliation and the approved rollback/recovery procedure remain F14
acceptance work; this adapter does not add a force-release button.

## Local Verification

Run `scripts/test-comfy-gpu-guard.ps1` for the complete focused gate. The normal
non-Quick `scripts/test.ps1` (and therefore CI) calls it. It creates and removes a
uniquely named, unpublished tmpfs PostgreSQL container and mounts source read-only.

Build `Dockerfile.test` as `ai-gateway-comfy-guard-test:20260906`. It contains only
test tooling and is never a production service. Mount the source read-only and
run `python3 -B -W error::ResourceWarning -m unittest test_guard test_storage`
from `integrations/comfyui_gpu_guard`.
Synthetic tests need neither ComfyUI nor a GPU.

For the optional installed-core contract test, run
`scripts/test-comfy-installed-core.ps1` and repeat with `-CacheMode none`.
It archives the installed revision into the workspace, hides GPU devices, disables
custom/API nodes and uses only a CPU fixture node. It does not install this adapter
in the working ComfyUI. It is a separate local acceptance gate because CI does not
have the user's installed Python/ComfyUI stack.

With an isolated PostgreSQL, run Go tests sequentially (`-p 1`) and set
`TEST_DATABASE_URL` plus `COMFY_GUARD_TEST_PYTHON=python3`. The
`TestComfyGPUProtocolIntegration` test starts its own loopback-only Python fixture,
exercises the real Gateway callback/store, kills only that fixture process and
verifies that a new instance cannot close its unknown permit. Do not point these
tests at production: existing integration fixtures reset their synthetic schema.

## Storage Window Participant (Not Rolled Out)

The repository adapter accepts an optional boolean `storage_enabled`, default
`false`. Do not enable it on an installed service as part of ordinary GPU guard
installation. Repository tests cover the Gateway/update-agent cleanup protocol,
input-reference admission and paired journal backup. Installed-core acceptance,
writer-boundary audit and the remaining rollout gates still apply before exposing
manual deletion.

When explicitly enabled on an isolated test instance, status adds
`storage_protocol: 1`, `storage_journal_id` and `storage_held`. The private
`POST /ai-gateway/storage/window` endpoint uses the existing guard key header,
a bounded JSON body, protocol 1 and actions `read`, `hold`, `close`. Requests bind
`id` (16-128 ASCII letters/digits/underscore/hyphen), `request_hash` and
`executor_id` (64 lowercase hex characters each), and `journal_id` (32 lowercase
hex characters). Results echo that identity with `unknown`, `held` or `closed`.

`hold` requires healthy hooks, no admitted GPU ticket, no undelivered receipt,
an empty core queue and no in-flight mutating HTTP handler. A held window rejects
new prompts, queue/executor bypasses and all non-GET/HEAD/OPTIONS HTTP operations
except this authenticated control endpoint. Reads stay available. Windows are
persisted in the same SQLite database as GPU receipts, with FULL synchronous
transactions. A restart retains a hold; neither a timeout nor the optional flag
can clear it. Closed IDs are retained without expiry. Closing an unknown ID also
prevents its delayed hold from opening. Different hashes/executors are conflicts.

`close` additionally requires `executor_fenced: true`. This is a **trusted
orchestrator assertion**, not proof manufactured by the guard: the
orchestrator must first obtain durable evidence that the original file executor
cannot unlink again, then close the window. Never assert it based on elapsed
time, a missing HTTP reply or an empty queue. This endpoint alone is not an
end-to-end file-deletion safety guarantee and does not delete any files.

The mutation boundary assumes one owning ComfyUI process and audited handlers
whose writes finish before returning. It does not fence shell/local file writes,
another ComfyUI process, detached custom-node tasks/subprocesses or custom GET
routes with side effects. Such writers must be excluded before rollout. A
canceled/failed handler keeps admission unhealthy because it may still own a
threaded write; normal 4xx route/validation exceptions do not poison admission.

Preserve the complete SQLite journal, including its identity and all closed IDs,
through coordinated upgrades/backup. A new journal cannot satisfy requests bound
to the old identity. Restoring an old copy of the same journal is **not** proof of
continuity and must never reopen live admission. The training-agent archive does
not include this separate ComfyUI journal. No force-release or automatic journal
reset is provided. See the [local verification report](../../docs/verification/COMFY_STORAGE_GUARD_2026-09-09.md).

## Paired Backup

Install `storage_backup.py` alongside `guard.py` when performing an explicitly
approved adapter upgrade. The authenticated, no-store, read-only
`GET /ai-gateway/storage/backup` captures storage identity, all held/closed windows
and all GPU completion receipts in one SQLite read transaction. It never acquires
a hold or pauses ComfyUI. The host backup CLI captures it twice under the executor
journal lock and rejects changes or inconsistent evidence. An older adapter
without this endpoint cannot provide a paired backup.

Restore reconstructs a separate SQLite database and records
`isolated_restore_blocked`. Every `Receipts.connect` rejects that marker; startup
also rejects it with storage disabled. No completion receipt can be delivered,
no hold closed and no new work admitted from the restored copy. This is deliberate:
an old snapshot cannot prove that newer operations did not run. There is no
automatic activation or marker-removal option.

Archive contents include internal completion tokens. Protect them as private
backup data; reports contain only identifiers, counts and hashes. See the
[backup verification](../../docs/verification/COMFY_STORAGE_BACKUP_2026-09-09.md)
and [restore runbook](../../docs/RESTORE_RUNBOOK.md).
