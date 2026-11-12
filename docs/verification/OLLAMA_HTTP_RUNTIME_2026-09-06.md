# Owned Ollama HTTP Transport

## Scope

F04 transport foundation after `b896dc8` (ComfyUI executor admission). The existing
Windows-owned model runtime now supports incremental HTTP consumption as well as
its previous bounded, non-streaming assistant request. No new listener, service,
production configuration or OpenWebUI/Ollama setting has been created or changed.
This does not yet coordinate external Ollama calls with the Gateway queue.

## Contract

- `Runtime.ExecuteHTTP` uses the same exclusive native process group, lifetime
  cancellation, occupied-port rejection and connected-peer ownership verification
  as `Execute`. Its caller must already own the global GPU resource for the entire
  call, including the cleanup after its consumer returns.
- The internal request accepts an origin-relative path and GET/HEAD/POST/DELETE,
  retains the query and body, and requires an explicit response budget up to
  64 MiB. Existing request limits still apply. Alternate authorities, fragments,
  backslashes, unsupported methods and a missing consumer are rejected before
  starting a process. This transport is not route authorization for a public API.
- The synchronous consumer receives status, a private copy of response headers
  and a bounded body reader. It can forward NDJSON or SSE bytes incrementally,
  without parsing tokens or buffering the whole response. It must consume EOF or
  return an error; silent early return is not a completed response. Reader errors
  cannot be hidden by a consumer returning nil, and excess bytes are not delivered.
- A response status or final token is never GPU-release evidence. On EOF, client
  error, cancellation, truncated upstream response or consumer panic, the runtime
  stops only its owned tree. `Result.Stopped` stays false if ownership proof was
  lost, even if the HTTP stream ended. The existing no-redirect policy remains.
- The previous `Execute` API still validates non-streaming chat and retains its
  64 KiB bounded result. It now shares the transport and cleanup implementation;
  existing inference-worker interfaces and durable job semantics are unchanged.

Ollama's native [chat](https://docs.ollama.com/api/chat) and
[generation](https://docs.ollama.com/api/generate) APIs support streamed responses.
Its [compatibility API](https://docs.ollama.com/api/openai-compatibility) is another
HTTP entry point that must remain under the same resource lifecycle. These API
references motivated the transparent transport; they are not evidence of testing
an installed Ollama model or every OpenWebUI feature.

## Verification

- Native Windows `internal/modelruntime` test binary passed **10 complete runs**.
  Existing request, ownership, foreign-port, cancellation, generation and recovery
  tests ran along with the new HTTP tests. Helpers are hidden test executables,
  not Ollama or model runners.
- Streaming fixtures cover `/api/chat`, `/api/generate` and
  `/v1/chat/completions`. The first frame must be consumed before the fixture is
  allowed to send the last. While consuming it, both native fixture processes
  remain owned and a second request returns busy. Original request bytes and
  method/path/query are checked.
- New native cases cover consumer error, context cancellation, mid-stream socket
  loss, missing ownership proof, consumer panic, response overflow, early consumer
  return, GET/HEAD/DELETE, refusal to follow a redirect and invalid input before
  launch. The owned process group is empty after confirmed cleanup.
- The crash fixture now tests both the previous hanging buffered request and an
  incremental HTTP stream. It waits for the first streamed frame, kills only the
  synthetic owner, verifies both held child process handles have signaled, then
  opens the same runtime state and confirms no surviving owned process.
- Shared tests cover relative URL validation, exact/over-limit bodies, metadata
  copying, unchanged NDJSON/SSE data and a consumer that ignores a read error.
- `go test -p 1 -count=1 -timeout=180s ./...` passed without a test database.
  Windows `go vet` passed for modelruntime, inference and inference-worker.
  Database integration, UI and real GPU checks were not repeated in this
  transport-only stage; no database schema or UI changed.

## Remaining F04 Work

Follow-up: ownership persistence and the opt-in request adapter are now covered
by [HTTP admission verification](OLLAMA_HTTP_ADMISSION_2026-09-06.md). The items
below record the transport stage's handoff; bypass removal and real rollout
acceptance are still outstanding.

1. Add the external Ollama admission lifecycle to the persistent GPU coordinator,
   including a precise owned-instance/generation binding and crash recovery.
   Do not reinterpret ordinary direct Ollama health as process-stop evidence.
2. Add an opt-in authenticated OpenWebUI/native API entry point using this owned
   runtime and that admission. Preserve supported streaming, metadata and model
   workflows; document any unsupported administration paths before rollout.
3. Keep the executor's coordination/status probe resource-neutral. The Gateway
   observer already holds the admission guard; a probe that itself tries to
   reserve another permit would deadlock or make all work unavailable.
4. Coordinate bypass removal and required guards as one approved rollout. A proxy
   does not make the current ordinary Ollama endpoint mutually exclusive. Do not
   stop, rebind or change the existing Ollama/OpenWebUI automatically.
5. Verify the installed model/runtime compatibility, memory requirements and real
   mixed GPU workloads. F04, F14 rollout and the overall product/redesign goal
   remain open; the next roadmap item after F04 remains F06.
