# ComfyUI DLSS lifecycle guard

Local patch for Blueforcer/ComfyUI-DLSS5-Enhancer at commit
`796ed5927a202ba50b5c929cd08e16b365041162`, with the separately verified
community runtime v3.0. This is not an official NVIDIA distribution.

`guard.patch` adds bounded worker I/O, cancellation while blocked in a pipe,
and a Windows job object with KILL_ON_JOB_CLOSE for the native worker tree.
Handshake timeout: 90 seconds; each frame: 30 seconds; drain/close: 65 seconds.
Both image and video nodes unload ComfyUI model caches before native D3D12 work.
Feature-18 verification is still mandatory in Gateway graphs.

The runtime binaries, RH-DLSS5 package, and antivirus settings are not modified.
Do not copy this runtime into the old RH package: their worker protocols differ.

Tests: `python -m unittest discover -s integrations/comfyui_dlss_guard`.
The activation artifacts also contain a real GPU cancellation test and a
parent-process containment test. CPU unit tests alone do not prove GPU support.

To reinstall after a clean checkout of the pinned upstream commit, first run
`git apply --check /absolute/path/to/guard.patch` inside that checkout, then
`git apply /absolute/path/to/guard.patch`. Validate the normalized source hashes
against `guard-manifest.json`. Drain Gateway/ComfyUI before restarting ComfyUI.
An upstream node update can remove this patch; revalidate instead of blindly
applying it to a different version. `export_patch.py` records the reviewed local
changes and is not an updater or downloader.
