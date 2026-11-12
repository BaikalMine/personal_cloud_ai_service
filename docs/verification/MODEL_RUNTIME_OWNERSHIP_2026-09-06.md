# Owned Local Model Runtime

## Purpose And Boundary

F04 still needs recovery of a model call whose inference worker died after
dispatch. Inspection confirmed that the installed Windows Ollama server is a
child of the Ollama desktop application, not of the current inference worker.
Restarting that worker cannot prove that its model request has stopped.

This change implements the native Windows execution boundary for that recovery.
It does not yet connect it to inference-worker claims or mark old requests settled.
The current Ollama process, its application, production config and GPU workloads
were not started, stopped, updated or replaced during this work.

## Implemented

- Extract the already tested Job Object implementation into `internal/processowner`.
  Mining uses the same lock path and `Mining` namespace through a thin adapter.
- Add child-specific UTF-16 environment support without changing the parent
  environment. Unsupported custom stdin/stdout/stderr are rejected explicitly.
- Add `internal/modelruntime` for a dedicated `ollama serve` child on a configured
  IPv4 loopback port. Executable, model directory and state paths are explicit.
  Existing model files are reused; this change does not download or copy models.
- Configure only the child: host/model path, local-only model operation, one
  parallel request and one loaded model. Requests preserve their image and text
  payload; only non-streaming local chat requests are accepted by this boundary.
- Serialize calls. Start model processes only inside Execute, not while constructing
  an idle runtime. The future caller must retain its global GPU permit throughout.
- Before any HTTP bytes are sent, bind the accepted TCP connection to its owning
  PID using Windows TCP accounting, then confirm membership in the owned Job Object.
  Checking only a listening port or a successful health response is insufficient.
- Reject an occupied port without contacting its owner. Also reject another server
  that takes the port between the initial check and connecting to the model.
- On response, model error, malformed/oversized body, cancellation or connection
  failure, terminate and drain the owned model tree before reporting `Stopped`.
  `Stopped` is process evidence, not a claim that generation succeeded.
- Close cancels the active call and drains the group. If the ownership handle or
  stop proof is unavailable, `Stopped` remains false and response bytes are cleared.
- Abrupt parent death closes the job handle, terminating server and runner
  descendants. A new runtime must drain the previous named group before readiness.

## Native Verification

Tests execute a hidden HTTP fixture and its child instead of the installed Ollama.
They do not load a model, consume GPU or modify a running user service.

- Normal response and repeat execution; error, malformed JSON, disconnect and
  oversized response with physical tree termination.
- Exact image/text request preservation and per-child environment overrides,
  including spaces/non-ASCII model paths and an existing conflicting host variable.
- Busy rejection, cancellation, orderly Close, startup timeout and absent stop proof.
- A foreign HTTP server wins the port race: it receives no HTTP request from the
  runtime and remains reachable after the owned tree is stopped.
- An actual supervisor-process kill after the fixture observed the chat request
  terminates both model server and child; a restarted owner observes an empty group.

The model-runtime native suite passed ten repetitions after initial checks.
TCP parser/environment tests passed ten repetitions; the entire mining-agent
native package passed three repetitions after extraction. Windows vet and the
mining-agent cross-build passed. The full Go suite passed without
`TEST_DATABASE_URL`; database-dependent tests were skipped in this turn because
the database layer was not changed. This is not a new PostgreSQL acceptance run.

## Next Required Integration

The native-to-database integration below was subsequently implemented and locally
verified in [Owned Inference Recovery](OWNED_INFERENCE_RECOVERY_2026-09-06.md).
That follow-up is still not a production rollout or full F04 acceptance.

Use the existing Windows inference-worker as the process owner, rather than add
another network service. Before dispatch, persist the executor identity and its
run generation with the inference request. On restart, replay known receipts first;
then use the exclusive owned runtime's confirmed drain to settle only requests
bound to older generations of that same executor. A different host/state directory,
an unowned direct Ollama call, or a missing identity must not be accepted as proof.

The integration also needs a distinct stopped/failed outcome in the store, client,
GPU evidence and journal; readiness must remain unavailable until reconciliation
finishes. Existing unknown direct calls stay quarantined. Timeout or generic health
must not be used as a substitute. Add a real Windows worker/DB crash-recovery test
before enabling this path in a coordinated release.

External ComfyUI/OpenWebUI admission, mixed GPU acceptance, operator recovery and
rollout remain open. This document does not close F04 or the overall goal.

## Primary References

[Ollama configuration](https://docs.ollama.com/faq) documents child environment
settings for the host, model directory and local-only execution.
[Chat API](https://docs.ollama.com/api/chat) defines the existing request format.
[Windows TCP ownership API](https://learn.microsoft.com/en-us/windows/win32/api/iphlpapi/nf-iphlpapi-getextendedtcptable)
and [row layout](https://learn.microsoft.com/en-us/windows/win32/api/tcpmib/ns-tcpmib-mib_tcprow_owner_pid)
define the connection-to-PID evidence used before HTTP dispatch.
