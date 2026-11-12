# OpenWebUI Ollama Compatibility

## Scope

F04 follow-up to `49c5353`. The installed OpenWebUI image identifies itself as
version `0.11.1`, revision `d3e8bf3405e848cfba377814d0aa7ba7290e414d`, digest
`ghcr.io/open-webui/open-webui@sha256:6bb1fbe8ab0a3e0456067f493044ffb66a30a65a34be47f6a5862176a370dd16`.
Its provider router source was inspected read-only. No account, connection,
database, container configuration or model in the working installation changed.

The compatibility test uses a fresh instance of that exact image, disposable
OpenWebUI data and PostgreSQL on tmpfs, no published ports, no GPU, and a network
namespace with no external interface. It registers only a synthetic test account
and connects to the real Gateway Ollama HTTP adapter with a synthetic owned model
runtime. The admission callback and persistent permit store remain in the path.
This verifies OpenWebUI backend integration, not actual model quality, GPU memory
requirements or browser rendering.

## Catalog Regression

Before the fix, the installed OpenWebUI's short-lived model-list cache expired
during an active response. Its next `/ollama/api/tags` refresh received `gpu_busy`
from the adapter and returned an empty model list, losing the known model.

The adapter now keeps at most two last-confirmed catalog responses, each limited
to 1 MiB: `/api/tags` and `/v1/models`. A snapshot is published only after a full
successful JSON response, confirmed native stop and committed permit receipt.
Errors, oversized bodies, partial reads and unconfirmed cleanup cannot publish it.

- A normal idle request still obtains admission and refreshes the catalog.
- Only local contention or a positively confirmed busy admission can use a
  snapshot younger than one hour. An unknown admission result remains an error.
- Cached replies require the same authorization, valid route/method and empty
  GET/HEAD request body. HEAD does not return a body.
- Cached responses carry `X-AI-Gateway-Catalog: cached` and `Cache-Control: no-store`.
  They describe the last observed model files, not current GPU availability.
- `/api/ps`, version, inference, embeddings and model administration are not
  cached. Cached reads never start a runtime or create/release a GPU permit.
- Snapshots are process-local and disappear on restart. Cold or expired catalogs
  still return 409 when busy. Catalog warming before rollout and the behavior of
  model pickers during work longer than the one-hour fallback window remain
  explicit operational limitations, not evidence of comprehensive F04 acceptance.

## Verification

`scripts/test-openwebui-compat.ps1` builds the test binary, creates uniquely named
isolated fixtures and removes them in `finally`. Default OpenWebUI and compiler
references are immutable. The compiler container needs dependencies/network only
during compilation; the compatibility execution has no external network or GPU.
The fixture name and exact version are checked before account/config writes.

Executed with the existing local Go test image:

```powershell
./scripts/test-openwebui-compat.ps1 -GoImage ai-gateway-comfy-guard-test:20260906 -Count 3
```

Three complete repetitions passed with the actual OpenWebUI image:

- Provider URL and bearer-key verification and persisted provider configuration.
- Native/OpenAI model lists, model details and version/running-model endpoints.
- Native and compatibility embeddings, buffered native/common chat, Responses.
- NDJSON generation, common `/api/chat/completions` streaming and OpenAI SSE.
- First streamed frame before completion, native and common `/api/models` catalog
  refresh after the OpenWebUI model cache expires, and preservation of 409 on a
  competing generation. The common list may also contain OpenWebUI's own
  `arena-model`; the test checks that our known model remains present.
- Cancellation through common `/api/chat/completions`, upstream cancellation,
  and eventual disappearance of the active GPU permit after confirmed stop.

Catalog tests also cover auth, route/body validation, HEAD, bounds/expiry, missing
snapshots, externally busy admission, unknown admission, closed worker, upstream
failure, lost stop proof and a rejected database stop receipt.

The complete sequential Go suite passed with a separate temporary PostgreSQL.
The full native Windows inference package also passed on a separate loopback-only
database, including the native HTTP cancellation/stop-receipt test. No native
worker was installed and no model was launched. Windows `go vet` passed for
inference, modelruntime and inference-worker. The shared cached-response write
uses the same 30-second write deadline as the streaming path.
The optional OpenWebUI test is skipped by ordinary Go runs unless explicitly
configured. It is intentionally a separate image-dependent gate, not an implicit
dependency on the user's running OpenWebUI.

## Remaining Acceptance

This does not migrate the working OpenWebUI connection. The API remains opt-in.
Real Ollama/model execution, model features such as tools/vision, uploaded-document
RAG, browser interaction and long-busy/cold-start model discovery still require
acceptance. Administrative model mutation remains denied by the adapter.
Guard rollout, old bypass removal, mixed GPU workloads, memory measurements and
the rest of F04/F06/product/redesign remain open. Do not change the user's ordinary
Ollama or ComfyUI as an automatic consequence of this test.
