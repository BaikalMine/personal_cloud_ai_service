# DeepSeek Harness

Admin-only web integration at `/harness/`, using the upstream interface from
`@deepseek-ai/dsh@0.2.1-alpha.2`. This preview is not an audited security boundary;
Docker isolation is mandatory. No host directories, Docker socket, GPU or native
executor credentials are exposed to Harness.

## Features

- Persistent sessions and workspace files in `harness-data` and `harness-workspace`.
- Native attachment picker, image input, streaming, cancellation, token statistics,
  workspace tools and model selection.
- Existing local Gemma/Qwen models through the owned Windows inference worker.
- Gateway login and admin authorization on HTTP and WebSocket requests.
- No URL credential in the browser; private bootstrap helper exchanges the native
  launch credential and scopes its cookie to `/harness/`.

The container's network is internal: Internet search, remote providers and remote
plugin/package installation are not available in this deployment. Local workspace
commands are supported. Large documents should be read in parts with file tools;
attaching a file does not guarantee every byte fits the model context. The declared
context budget defaults to 131,072 tokens and output budget to 32,768 tokens per
model call, including reasoning. There is no integration-imposed total chat or
tool-step budget. A model can still end an answer early; a larger cap is not a
promise of a longer answer. The native agent reports `max-tokens` separately from
normal completion. It does not silently auto-submit repeated continuation turns.

`HARNESS_MODELS` entries accept integer `contextWindow` and `maxTokens` overrides
(up to 262,144 context tokens, subject to the model's actual supported capacity).
The pinned native Models settings editor also allows explicit provider-model
budgets. Context declarations must agree with the native runtime configuration;
OpenAI-compatible Ollama requests cannot set `num_ctx` themselves. The owned worker
supports `INFERENCE_MODEL_CONTEXT_LENGTH`, `INFERENCE_MODEL_FLASH_ATTENTION`, and
`INFERENCE_MODEL_KV_CACHE_TYPE`. This deployment uses `131072`, `true`, and `q8_0`.
The context override applies only to `/v1/` calls, not native prompt-assistant
requests. KV-cache quantization is separate from model weight quantization. No
ordinary Ollama process or system-wide environment variable is changed.

On this RTX 5090, a real 128K Gemma 31B Q4 request stayed entirely on GPU (about
28.2 GiB total GPU memory used). Qwen 27B Q8 at the same context offloaded about
6.9 GiB to system memory; it works, but is not the preferred fast heavy-work route.
Use Gemma 31B as the default heavy agent, and E4B for faster lightweight work.
256K is an explicit, separately measured profile, not a universal safe default;
E4B's installed model metadata supports at most 128K.

Automatic compaction keeps the latest 16,384 tokens verbatim, reserves 8,192 tokens
of headroom, and uses at most 8,192 summary tokens. At the default output budget,
pressure starts near 90,112 tokens (token measurement is approximate). Old history
becomes a durable summary, not an exact replacement for source documents. Files
and original session logs remain available. `/compact` also works manually.

Startup upgrades only the old integration-owned 16,384/4,096 model defaults in
the existing YAML/JSON profile patch. Explicit tuning, unrelated providers and
plugins are preserved. Migration is atomic and saves the original with mode 0600.
An existing compaction override is respected. Rollback must restore the saved
profile patch as well as the previous image and worker configuration.

For this shared admin workspace, `HARNESS_PAUSE_MINING=true` and an explicit
`HARNESS_MINING_USER_ID` (an enabled admin) opt in to automatic mining management.
Generation priority settings are not changed. Each local model request retains a
durable mining reservation until its native process owner confirms shutdown;
Gateway/worker recovery uses the same evidence before restarting the selected BAT.

## GPU Admission

The private model proxy permits only `/v1/models` and `/v1/chat/completions`.
It authenticates to the native worker using a credential not available to Harness.
The worker obtains a durable GPU permit from `/internal/gpu/harness/permit` before
starting its own Ollama process. It never proxies to ordinary Ollama directly.

Mining and other jobs block new inference. Harness cannot directly control mining;
the Gateway's explicitly configured mining reservation policy owns pause/resume.
The model adapter's general retry policy remains disabled. The Gateway waits up
to four minutes only for the native worker's exact, pre-dispatch `gpu_busy`
refusal. Network failures, executed requests and 5xx responses are never replayed.
Cancellation holds the permit until the owned process tree has stopped. Windows
stop proof includes descendant process handles, not only job accounting.
For successful OpenAI SSE calls, the native worker withholds `finish_reason`,
usage and `[DONE]` until physical stop and durable permit closure. Text deltas
remain live; the next agent step cannot outrun the previous stop receipt.

## REA And Ghidra

`HARNESS_REA_ENABLED=true` adds an image-owned stdio MCP bridge, pinned to
`rea-agents@6.3.0`, and its `reverse-engineer-anything` skill. The MCP client uses
the existing pinned Harness package, not a second host or Docker connection.
REA sees the container workspace, including files uploaded to Harness, but no
Windows host directories. The REA npm dependency tree lives separately in
`/opt/rea`; the existing Harness dependency versions are preserved. Its pinned
Koffi 3.1.1 loader receives an exact-match build patch to select the musl binary
on Alpine; with gcompat, the upstream glibc-first fallback aborts instead of
throwing a recoverable import error. A changed version or source fails the build.

`mcp__rea__catalog`, `mcp__rea__tool_schema` and `mcp__rea__invoke` expose the
complete 139-tool backend through paged discovery. The agent loads a tool's full
schema on demand and passes its raw name plus a JSON object to `invoke`. Loading
all schemas eagerly made pi-ai estimate 176K tokens and clamp output to one token
even at a 128K context; compact discovery reduces the initial estimate to 11K.
No model context size or output budget is falsified to work around that clamp.

Ghidra 12.1.4 and a full OpenJDK 21 are bundled for headless native analysis.
Build the REA overlay from the repository root with
`docker build --build-context ghidra=<archive-directory> -f integrations/harness/rea.Dockerfile .`.
The named context must contain `ghidra_12.1.4_PUBLIC_20260921.zip`; it is not committed.
The official release archive is verified with SHA-256
`ddac49f903da9d5bac833e5cc79395098b9c33cfd3279be5f31bd00387d2d4db`.
Ghidra uses a 2 GiB heap and the Harness container receives 4 GiB total memory.
Its PID/thread cap is 256: the previous 128 cap was insufficient for concurrent
Harness and Java native analysis. Root/capability/filesystem restrictions remain.
The Alpine deployment is outside REA's general supported host baseline; the
Ghidra path is verified separately with a real ELF import and decompilation.
Browser-runtime, IDA, Hopper, JADX and remote Internet access are not bundled.
Do not assume all 139 discoverable tools have their optional external runtimes.

`tools/rea-check.mjs` checks discovery, pagination, schema lookup and malformed
invocation rejection, then actually imports/decompiles/closes
the image's Java launcher through Ghidra. `tools/rea-profile-check.mjs` checks
the active MCP plugin and skill through the authenticated Harness RPC. Its
opt-in `QA_AGENT=true` mode is only for a dedicated synthetic test profile.
`tools/gpu-stream-check.mjs` performs two real, short model calls, cancelling
each response immediately after the terminal frame like the model adapter.

## Configuration

Use `compose.yml` as an overlay to the site's base Compose configuration. Generate
four distinct random credentials, each at least 32 ASCII characters:
`HARNESS_BOOTSTRAP_KEY`, `HARNESS_MODEL_KEY`, `HARNESS_CLIENT_KEY`, and
`HARNESS_ADMISSION_KEY`. Never reuse the Gateway session secret.

The native worker needs `INFERENCE_OLLAMA_ADDR`, `INFERENCE_OLLAMA_CLIENT_KEY`,
`INFERENCE_OLLAMA_ADMISSION_URL`, and `INFERENCE_OLLAMA_ADMISSION_KEY`. Bind only to
the trusted server LAN address. Deploy while no inference/generation is active.
Do not enable the unrelated global Ollama or ComfyUI guards merely for Harness.

Back up both Docker volumes and the production/native configuration. Preserve the
Harness image and native worker build together during rollback. A normal container
recreation preserves sessions, workspace, settings and the browser-auth secret.

## Russian And Mobile UI

The image includes `@ai-gateway/harness-ui`, an external plugin using the native
locale and slot APIs. Russian is the default; the selected language persists in
browser localStorage. The 54 dictionaries cover all 2,520 strings extracted from
the pinned upstream release. Model IDs, commands, protocols and app names remain
literal. No model prompt or user content is translated by this UI plugin.

Below 720px the sidebar becomes a drawer, chat uses the full width, settings use
scrollable horizontal navigation, and the composer follows the visual viewport
above the keyboard. The upstream right panel keeps its native mobile fullscreen
behavior. Page zoom is not disabled. CSS-module selectors are explicitly tied to
0.2.1-alpha.2 and must be reverified when upgrading Harness.

Build with `docker build --build-arg HARNESS_MANAGED_SETTINGS_ORIGINS=https://ai.neurodock.ru -t ai-gateway-harness:0.2.1-alpha.2-gateway-ui-r2 integrations/harness`.
`ui/build.mjs` assembles human-maintained dictionaries and styles without a new
production dependency. `tools/extract-locales.cjs` reads the public installed
client bundles through Acorn; `tools/verify-locales.cjs` verifies exact key and
placeholder coverage against that extraction. Development npm dependencies are
installed with `--ignore-scripts` and are excluded from the image.

Upstream 0.2.1-alpha.2 disables the settings mirror on all non-loopback pages.
For this admin-only deployment, a narrowly scoped build-time patch allows that
mirror on the exact configured HTTPS origin, and only under `/harness/`. The
default origin list is empty; HTTP, wildcard hosts, paths and credentials are
rejected. This changes neither Connection's loopback facts nor Host authentication,
origin checks, Gateway admin authorization or settings revision validation. It
preserves the native settings owner, schemas, write queue and invalidation events.
The patch must match exactly one pinned upstream expression or the build fails.
The separate General settings-document export/reset actions remain loopback-only.

`ui-test.https.compose.yml` uses a self-signed TLS test carrier and an internal
Docker DNS alias, without connecting to production. `settings-check.cjs` reproduces
the old error, then checks provider catalog reads, durable theme writes, reload and
welcome acknowledgement. `tools/managed-settings.test.mjs` covers the exact-origin
policy and deny-by-default cases. Mobile QA now asserts the catalog contents, not
just the visibility of the Models tab.

`mobile-check.cjs` uses the dedicated `ui-test.compose.yml` fixture: Russian and
English persistence, mobile settings/models, attachments, streaming, cancellation,
history reload, drawer behavior, dark/light screenshots and reduced viewport
height. It never submits work to a real GPU. Browser viewport resizing approximates
keyboard geometry; a physical Android/iOS keyboard is still a separate device test.

Only recreate the `harness` service for this UI release; do not run the original
full integration installer (which also changes the native executor). Back up the
profile manifest and Compose override first. Startup adds a single image-owned
profile dependency and symlink; existing profile bundles/settings/history stay
intact. On rollback restore the prior profile manifest and remove only the exact
image-owned addon symlink, then recreate Harness with the prior image.

## Verification

`ui-check.cjs` runs against the opt-in `TestHarnessUIIntegration` fixture and a
synthetic OpenAI streaming server in a dedicated internal Docker network. It checks
file attachment, session reload, streaming cancellation, workspace shell execution
and desktop/mobile screenshots. It does not use a real model or GPU.

Go tests cover authentication, host/origin restrictions, cookie filtering, model
route allowlists, busy errors, durable GPU permits and native cancellation receipts.
Database tests require a dedicated `TEST_DATABASE_URL`, never the production DB.
