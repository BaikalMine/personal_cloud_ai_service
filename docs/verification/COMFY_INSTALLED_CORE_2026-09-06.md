# Installed ComfyUI Core Compatibility

## Verified Boundary

The F04 adapter was exercised against an archived copy of the installed core,
not only the synthetic queue/server classes from its original unit tests.

| Component | Observed version |
| --- | --- |
| ComfyUI | `0.34.0` |
| Core commit | `8a33128f2f8c5585c57486c07de481241e70a39c` |
| Python | `3.12.10` |
| PyTorch | `2.12.1+cu130` |
| aiohttp | `3.14.1` |

The core source had no tracked Python/dependency changes relative to that commit.
An unrelated missing output-directory placeholder and untracked local directories
were not reverted, archived or interpreted as tested custom-node packages.

## Isolation

`scripts/test-comfy-installed-core.ps1` archives the recorded Git revision to a
uniquely named workspace `bin` directory. It refuses dirty tracked Python or
dependency files and checks source stability again after the test. The installed
Python is used without installing or updating packages. Bytecode writes are off.

The test imports the archived `main`, `server`, `execution` and model-management
modules. It constructs the real `PromptServer` and runs the real `prompt_worker`.
Only a test output node is registered; no installed custom nodes, model files,
user data or workflows are copied or loaded. Base/user/output/cache/temp locations
are confined to the disposable fixture directories. The default frontend loader
and asset scanner are not used. Test HTTP listeners bind ephemeral loopback ports.

`--cpu`, `--disable-dynamic-vram`, hidden CUDA/HIP devices and disabled API/custom
nodes prevent GPU workloads. The test checks that PyTorch never initialized CUDA.
The actual production `cuda_quiescer` must reject this CPU environment. A separate
explicitly synthetic quiescer resets the real executor and lets the test inspect
completion ordering; its success is **not** evidence of CUDA memory release.

The active ComfyUI process, launcher, Python environment, installed node packages,
models, user settings, Gateway and production database are unchanged. Temporary
archives, synthetic settings and receipt databases are removed in `finally` after
an absolute workspace-containment check.

## Scenarios

- Authenticated resource-neutral guard readiness; no permit for status reads.
- Malformed JSON and forged managed headers rejected before the callback.
- Callback contention returns 409 through the real `/api/prompt` route, with no
  queue insertion or node execution.
- Normal domain validation failure obtains cleanup before its terminal receipt.
- Pending deletion and whole-queue clearing through `/queue`, plus individual
  `/api/jobs/{id}/cancel` and batch `/api/jobs/cancel`, release only unstarted work.
- Native six-field queue items, including the sensitive-data field, pass through
  the real worker. One output node executes only after confirmed start.
- A competing prompt is rejected while that node is blocked. An unadmitted direct
  `PromptQueue.put` is also rejected.
- The output is present in the real history endpoint. Cleanup occurs only after
  the worker has removed its running item and published history, at its next
  queue read after the installed worker's postprocessing segment.
- Running cancellation via `/interrupt`, individual job cancellation and batch
  cancellation produces a non-completed history record and then cleanup.
- A synthetic cleanup failure retains the fake callback's permit, marks guard
  readiness unenforced and returns 503 on subsequent `/api/prompt`; the history
  record alone never permits another execution.

The callback is a loopback fixture for this core-contract test, not PostgreSQL.
The existing `TestComfyGPUProtocolIntegration` separately covers the actual
Gateway callback/store and synthetic Python executor. Neither test is presented
as a full real-GPU mixed-workload run.

## Commands

```powershell
./scripts/test-comfy-installed-core.ps1
./scripts/test-comfy-installed-core.ps1 -CacheMode none
```

The default run uses the installed worker's normal RAM-pressure cache selection;
the second uses its supported no-cache mode. Each full matrix performs five
CPU-only fixture executions and nine synthetic successful cleanup confirmations,
then deliberately leaves one unconfirmed fake permit to test failure closure.
The intentional final guard error is expected; a successful run ends with the
structured `result: passed`, exact versions and `real_gpu_stop_verified: false`.
Both complete matrices passed. The existing 22 Python guard tests also passed
with `ResourceWarning` treated as an error in the network-disabled Docker fixture.
No production Go/JS/adapter code changed in this verification stage.

## Remaining F04 Acceptance

This confirms the installed **core contract**, not the whole installed workflow
environment. Custom-node versions, async/detached work, native CUDA allocations,
actual cached tensors and real memory release still require a supported-runtime
inventory and GPU acceptance. The plugin import/install path has not been enabled
in working ComfyUI. No full JS/browser or real image/video/LoRA test was run here.

Real mixed load and coordinated rollout still require operator approval. Keep the
existing no-force-release behavior and do not infer completion from these CPU
fixtures. F04, F14 and the remaining product/redesign plan are not closed.
