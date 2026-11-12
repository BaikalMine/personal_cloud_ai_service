# DLSS Frame Generation Runtime Guard

Pinned plugin: `RH-RunningHub/ComfyUI-RH-DLSS5`, commit
`bb044f99ff6b64e6eab3ed180d39708ef5dc0889`.

`fg.py` is the reviewed installed replacement for `dlss5nr/fg.py`, including
the existing Windows memmap and runtime discovery fixes. `lifecycle.py` is the
same process guard as `../comfyui_dlss_guard/lifecycle.py`, installed alongside
it under `dlss5nr/`. The accompanying lifecycle tests belong under the plugin's
`tests/` directory. `files.json` records the exact package hashes.

The guard bounds probe (120 s), handshake (90 s), frame I/O (30 s), and close
(30 s). It observes ComfyUI cancellation and uses a Windows kill-on-job-close
job object. GPU caches are released before the D3D12 frame-generation session.
Cancellation tests must confirm worker exit, not only a UI state change.

The old signed driver DLL 310.2.1.0 reported zero generated-frame capacity and
failed the real session test. This installation uses NVIDIA's signed SDK DLL
310.9.1.0, SHA256
`FF6E90EB78B827927DFF5B4ECC6B1C870C2E9BCA29ED9F48C7D348CC9E170B82`:

https://raw.githubusercontent.com/NVIDIA/DLSS/374959484e79a640feaba44c93ac8cfb0a03f5b5/lib/Windows_x86_64/rel/nvngx_dlssg.dll

Place it in `ComfyUI/models/dlss5/dlssg/nvngx_dlssg.dll`. The existing community
worker and loader DLLs are unchanged. This is frame generation, not the separate
community DLSS5 neural-rendering enhancement stage; the package does not claim
an official DLSS5 enhancement release.

Do not overwrite a newer plugin blindly. Rebase/review the guarded changes,
verify the signed runtime and recorded hashes, then test 2x/3x/4x, cancellation,
duration/audio, and mining recovery. Install only with admission stopped and
all AI queues proven idle. Keep exact file backups and restart ComfyUI once.
The reviewed installation and rollback procedure is recorded in
`artifacts/dlss-fg-20260921/install-comfy.ps1` and `installation.json`.

Verification: `docs/verification/DLSS_FG_2026-09-21.md`.
