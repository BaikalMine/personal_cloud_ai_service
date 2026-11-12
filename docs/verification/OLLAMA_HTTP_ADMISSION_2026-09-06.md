# Guarded Ollama HTTP Admission

Date: 2026-09-06. F04 implementation after the native HTTP transport and migration
70. Locally tested, not deployed. Existing Ollama, OpenWebUI, ComfyUI, Windows
Firewall and production configuration were not changed.

## Request Lifecycle

The opt-in native worker listener shares the same run lock, durable outcome
preparation and native owner as managed assistant/caption requests. The client
key is distinct from the internal Gateway admission key. Neither incoming
authorization nor client-selected upstream headers are passed to the model.

1. Authenticate, validate the route/method and bounded JSON body. Invalid or busy
   requests do not reserve GPU or start a process.
2. Restore managed receipts and prepare the native owner. Ask the Gateway's LAN
   admin endpoint to reserve an Ollama permit while holding the shared admission
   guard. The Gateway checks mining, other executors and waiting managed work.
3. Consume the one-shot start in PostgreSQL before invoking the native runtime.
   Stream the model response while retaining the running permit through native
   process-tree cleanup. No response frame is GPU-release evidence.
4. Persist `quiesced` only after the runtime confirms its owned tree stopped.
   This transaction uses its own bounded context, independent of client cancel.
5. On an ambiguous reserve/start, failed stop, failed receipt or handler panic,
   invalidate the owner. Before another dispatch, native drain and a new durable
   generation fence late messages and reconcile older same-owner permits. A
   missing row alone is never proof that a timed-out reservation cannot commit.

External requests are not replayed by this adapter and their response bodies are
not added to the managed outcome journal. Clients may retry a rejected request;
there is no automatic external retry queue. A broken stream is aborted rather
than completed with an appended, incompatible JSON error. Closed permits remain
as identities; failed requests do not disappear by age.

## HTTP Surface

Supported read paths (GET/HEAD): `/`, `/api/version`, `/api/tags`, `/api/ps`,
`/v1/models`, `/v1/models/{model}`. Supported POST paths: `/api/chat`,
`/api/generate`, `/api/embed`, `/api/embeddings`, `/api/show`,
`/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`, `/v1/responses`.
Native NDJSON and compatibility SSE are forwarded incrementally. Actual model
and endpoint support still depends on the installed Ollama version.

Other paths, alternate encoded/query routes, model pull/push/create/copy/delete
and blob upload are rejected. Models must already exist in the configured model
directory. There is no cloud fallback, model download or public browser CORS API.
Model administration remains an explicit operator action outside this listener.

Limits: one call per worker, the existing 96 MiB request limit, 64 MiB streamed
response budget, 30-second request read and per-write bounds, and one hour per
execution. Busy returns HTTP 409 with `Retry-After: 3`. Failed admission returns
503. Authentication uses `Authorization: Bearer <client key>` and returns 401 on
failure. Native and compatibility error envelopes match their respective shape.

Metadata calls also acquire admission before starting the short-lived native
server. They can return busy while GPU work runs. `/api/ps` describes that owned
invocation, not the user's ordinary Ollama or a global process inventory. Gateway
coordination deliberately uses the resource-neutral readiness endpoint instead.

The route set follows Ollama's [native API](https://docs.ollama.com/api/introduction)
and [compatibility API](https://docs.ollama.com/api/openai-compatibility). These
references define protocol scope, not proof of testing all installed models.

## Configuration And Rollout

No listener is enabled by default. The native `inference-worker` supports:

| Setting | Requirement |
| --- | --- |
| `INFERENCE_RUNTIME` | `windows-owned`; direct mode rejects the API settings |
| `INFERENCE_OLLAMA_ADDR` | Explicit loopback/private IP and port 1024-65535; no wildcard, separate from the model port |
| `INFERENCE_OLLAMA_CLIENT_KEY` | Independent random 32-256 printable ASCII characters, no spaces |
| `INFERENCE_OLLAMA_ADMISSION_URL` | Gateway LAN admin URL ending exactly in `/internal/gpu/ollama/permit`, without URL credentials, query or fragment |
| `INFERENCE_OLLAMA_ADMISSION_KEY` | Matches Gateway `OLLAMA_GPU_GUARD_TOKEN`; different from the client key |

Gateway requires `OLLAMA_GPU_GUARD_REQUIRED=true`, a distinct
`OLLAMA_GPU_GUARD_TOKEN`, `MINING_FENCED_COMMANDS=true`,
`LORA_CAPTION_DURABLE_INFERENCE=true` and `PROMPT_ASSISTANT_DURABLE_JOBS=true`.
`INFERENCE_WORKER_URL` must reach this native worker's readiness listener. Its
response must confirm both native ownership and the enabled Ollama adapter.
The coordinator does not call back into `/api/ps` and recursively acquire GPU.

During an approved idle-window rollout, migrate to schema 70, stop the old direct
inference worker, preserve the native owner/outcome directories, scope native
API/readiness/database/admin access, and verify the guarded listener. Use the
client key only in model clients; keep the internal admission key private to the
worker and Gateway. Firewall and listener changes require their own validation.

OpenWebUI supports provider URL/key configuration under Admin > Connections
([provider connection guide](https://docs.openwebui.com/getting-started/quick-start/connect-a-provider/)).
Configure its server-side Ollama connection to the reachable native listener and
the client key, not the admission key. Disable old local-GPU connections and
verify saved connection settings, metadata refresh, chat, embeddings and cancel
on the installed OpenWebUI build. Its connection configuration may be persisted
([environment reference](https://docs.openwebui.com/reference/env-configuration/));
changing an environment value alone is not proof of a completed migration.

All clients and old ordinary Ollama listeners must be included in that rollout.
The adapter cannot prevent an unrelated process or the old unguarded endpoint
from using GPU. Do not enable the required guard while leaving bypasses active.
Do not stop or reconfigure the user's ComfyUI as a side effect. Returning to a
direct-worker build also requires an idle, reconciled transition; never erase
owner state or active permits to force rollback.

## Verification

- Full sequential Go suite with isolated PostgreSQL passed, including existing
  managed inference and ComfyUI admission tests.
- New tests cover configuration/credential isolation, route/method/body limits,
  one-shot confirmation, duplicate start, missing/late reservation responses,
  cancellation, postprocessing hold, receipt failure, unknown stop and panic.
- The real Gateway admin callback, worker readiness, HTTP adapter and PostgreSQL
  roundtrip passed with a synthetic model runtime. Training waits while the
  external stream owns GPU, then acquires it after confirmed stop.
- Native Windows cancellation passed three repetitions. A real owned test HTTP
  process emits a frame; held server/child process handles are confirmed signaled
  before the adapter's `quiesced` receipt is observed.
- The complete Windows inference test package passed with a separate loopback-only
  PostgreSQL, including the existing native owner-crash recovery. The Windows
  worker binary cross-build passed; that worker binary was not installed or run.
- `scripts/test-ollama-gpu-guard.ps1` is connected to the ordinary non-Quick gate.
  It owns a uniquely named unpublished temporary database and removes it in
  `finally`. This standalone gate passed. Native Windows tests remain a separate
  platform check. Repository-wide Linux `go vet ./...` also passed.
- Windows `vet` for inference, the native runtime and worker passed. Focused
  configuration/API/Gateway tests passed three repetitions. After the final
  model-path escaping fix, the focused Linux tests and Windows HTTP matrix passed
  again, including native cancellation; the Windows worker was rebuilt. Both
  manually created test databases were removed, and the gate's own database was
  confirmed absent. The complete UI and full Go suite were not rerun for this
  final localized escaping change.

No real GPU/model execution, installed OpenWebUI compatibility check or UI/JS
regression was run in this backend stage. Actual bypass removal, mixed GPU load,
memory measurements, coordinated deployment and complete F04 acceptance remain
open. F06 and the remaining product/redesign plan are not completed by this work.
