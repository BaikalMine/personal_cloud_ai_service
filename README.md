# AI Access Gateway

Dockerized Go + PostgreSQL gateway for controlled access to ComfyUI and OpenWebUI.

The Gateway user interface and administration area are localized in Russian;
the proxied ComfyUI and OpenWebUI interfaces remain unchanged.

## Layout

- `cmd/gateway`: process entrypoint only.
- `internal/config`: validated environment and network policy configuration.
- `internal/database`: PostgreSQL connection pool and transactional, checksummed schema migrations.
- `internal/domain`: business entities and service-access rules.
- `internal/security`: password hashing, session/invite tokens, CSRF signing, and login throttling.
- `internal/store`: PostgreSQL repositories for identity, sessions, invites, audit, telemetry, and analytics.
- `internal/gateway`: HTTP handlers, reverse proxy, request policy, Prometheus adapter, and UI composition.
- `internal/gateway/templates`: server-rendered UI templates.
- `internal/gateway/static`: local CSS assets.
- `cmd/mining-agent`: restricted Windows host agent used to start and stop allow-listed mining scripts.
- `docker-compose.yml`: app and PostgreSQL services.

The gateway keeps the public and admin listeners separate. The admin listener is
restricted by both the Windows firewall and the application allow-list. The
database is reachable only from the Compose network; it has no host port.

Database migrations are serialized with a PostgreSQL advisory lock and recorded
in `schema_migrations`. A checksum mismatch or a database schema newer than the
running binary stops startup instead of silently changing an unknown schema.

Important environment settings:

- `PUBLIC_BASE_URL` is the canonical HTTPS URL used in invite links.
- `COOKIE_SECURE=true` is required when the public URL is HTTPS.
- `TRUSTED_PROXIES` controls which peers may supply forwarded client headers.
- `ADMIN_ALLOWED_CIDRS` controls the application-level admin network boundary.
- `SESSION_TTL` controls the absolute lifetime of a browser session.
- `SESSION_IDLE_TIMEOUT` expires sessions that have not been used recently.
- `ACCOUNT_LOCK_THRESHOLD` and `ACCOUNT_LOCK_DURATION` control persistent login lockout.

Passwords use a versioned `bcrypt-sha256` format. Pre-hashing removes bcrypt's
72-byte input limitation while bcrypt still provides the adaptive work factor.
Legacy bcrypt hashes remain valid and are transparently upgraded after the next
successful login; no password reset or plaintext migration is required.

Docker base images and PostgreSQL are pinned by both a readable version tag and
an immutable registry digest. Dependency updates should change the tag and
digest together, run `.\scripts\test.ps1`, create a database backup, and only
then rebuild production.

## Run

```powershell
docker compose up -d --build
```

Public UI listens on `8090`; admin UI listens on `8091`.

The optional mining controls use a separate Windows host agent on port `8092`.
Build the agent for Windows, generate a unique token, and install it from an
elevated PowerShell session:

```powershell
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
go build -trimpath -ldflags '-H=windowsgui -s -w' -o .\dist\mining-agent.exe .\cmd\mining-agent
$tokenBytes = [byte[]]::new(48)
$random = [Security.Cryptography.RandomNumberGenerator]::Create()
$random.GetBytes($tokenBytes)
$random.Dispose()
$token = [Convert]::ToBase64String($tokenBytes)
.\scripts\install-mining-agent.ps1 -Executable .\dist\mining-agent.exe -Token $token -InteractiveConsole
```

Set the same token as `MINING_AGENT_TOKEN` in `.env` and keep
`MINING_AGENT_URL=http://host.docker.internal:8092`. With
`-InteractiveConsole`, the installer creates an elevated at-logon task for the
current Windows user, allowing each miner to open a visible console. Console
output is also appended to the agent log; use its generated log-view command
for a live tail. The inbound firewall rule remains restricted to Docker/WSL
networks. The agent accepts only authenticated start, stop, and state requests,
and scripts must resolve below the configured mining directory; arbitrary
commands are not accepted.

The Keenetic HTTPS proxy must preserve the public `Host` value and response
security headers. Verify the final Internet-facing response after router
changes:

```powershell
curl.exe -I https://gateway.example.com/login
```

The response should include `X-Request-ID`, `X-Frame-Options: DENY`,
`X-Content-Type-Options: nosniff`, and `Strict-Transport-Security`. The Gateway
sets HSTS for the canonical HTTPS origin; if it is absent externally but present
when requesting port `8090` with the canonical Host, the edge proxy is removing
it and must be adjusted there.

## Backup

Create and validate a consistent PostgreSQL dump and OpenWebUI data-volume
archive:

```powershell
.\scripts\backup.ps1
```

The script briefly pauses only the OpenWebUI container while archiving its
volume, always unpauses it in `finally`, validates both archives, and writes a
JSON manifest with sizes and SHA-256 hashes under `backups`. It does not include
`.env`, Basic Auth files, or plaintext credentials. It keeps the three newest
manifest-backed backup sets by default; use `-Keep` to choose another retention
count. Older manual dumps without a manifest are never removed.

## Main routes

- `/app`: user dashboard and upstream health.
- `/account/password`: self-service password change.
- `/account/sessions`: view and revoke personal sessions.
- `/comfyui/` and `/openwebui/`: authenticated HTTP/WebSocket proxies.
- `/admin/users`, `/admin/invites`, `/admin/sessions`: identity and access administration.
- `/admin/services/comfyui` and `/admin/services/openwebui`: 30-day service analytics.
- `/admin/metrics` and `/admin/audit`: operational metrics and security audit history.
- `/admin/mining`: miner profiles, icons, scripts, status, and start/stop controls.
- `/metrics`: Prometheus text format on the LAN-only admin listener.

Administrators can independently grant ComfyUI and OpenWebUI access from each
user detail page. Administrators themselves always retain access to both
services.

## Per-user upstream state

Gateway accounts remain isolated even though the existing ComfyUI process is
started without `--multi-user` and its launch command is not modified:

- ComfyUI queue, history, WebSocket client ID, control actions, generated
  outputs, settings, and userdata/workflows are scoped to the authenticated
  Gateway user.
- Multipart image/mask uploads are rewritten into a stable per-user
  `gateway/<HMAC-client-id>` input subfolder. Cross-user `/view`, mask
  references, and prompt references to another Gateway namespace are rejected.
  Upload bodies are limited to 128 MiB and at most two are processed at once.
- Unowned legacy input/output files created directly through port `8188` are
  visible through the Gateway only to an administrator. Ordinary users fail
  closed unless the input is in their namespace or the output is tied to one
  of their recorded prompt IDs.
- Settings and userdata are stored in PostgreSQL (`comfy_settings` and
  `comfy_userdata`), so they are included in the normal Gateway backup. A
  single userdata file is limited to 32 MiB and each user has a 256 MiB quota.
- OpenWebUI receives a stable HMAC-derived email for each Gateway user and
  always assigns the upstream role `user`, including when the Gateway account
  is an administrator. A signed identity cookie prevents a bearer token from
  surviving a switch to another Gateway account in the same browser.

To import the current local ComfyUI `default` profile into the Gateway
administrator's isolated profile without changing the source files or ComfyUI
startup command:

```powershell
.\scripts\import-comfy-default.ps1
```

The importer creates a short-lived database session, uploads settings and
userdata through the authenticated Gateway API, and removes the temporary
session in a `finally` block.

Run the production upload-isolation smoke after proxy changes:

```powershell
.\scripts\smoke-comfy-upload.ps1
```

It creates two temporary unprivileged users, uploads the same PNG name for
both, verifies owner access and cross-user denial against the real ComfyUI,
then removes the temporary users, sessions, and exact smoke files.

Verify the complete OpenWebUI/Ollama audit path, including encrypted storage
and decrypted rendering in the administration page:

```powershell
.\scripts\smoke-openweb-content.ps1
```

Verify the ComfyUI workflow and generated-media audit path without loading a
model or using the GPU:

```powershell
.\scripts\smoke-comfy-content.ps1
```

It creates a temporary unprivileged Gateway user, submits a tiny
`LoadImage -> SaveImage` workflow through the real proxy, checks the encrypted
workflow event and media record, and removes only its temporary user, records,
and exact smoke files.

With SRBMiner stopped, verify both user and administrator mining controls, PID
visibility, audit records, and the final stopped state:

```powershell
.\scripts\smoke-mining.ps1
```

The script uses a short-lived administrator session, sends one minimal request
through the real OpenWebUI proxy to the smallest installed Ollama model,
verifies the captured prompt and response in `/admin/content`, and removes the
exact smoke event and session afterward.

Invite links also carry initial service grants. The administrator chooses
ComfyUI, OpenWebUI, or both when creating an invite; registration applies those
permissions atomically with the invite use counter.

## Isolated integration environment

`docker-compose.e2e.yml` starts a separate PostgreSQL database in `tmpfs` and
publishes the test Gateway only on `18090/18091`. It never uses the production
database or its volume.

```powershell
.\scripts\test.ps1
```

The test script runs formatting verification, `go vet`, unit tests, and the
PostgreSQL lifecycle suite. The integration suite verifies concurrent
single-use invites, service grants, session revocation, encrypted-content
storage accounting, per-user ComfyUI settings/workflow isolation and quotas,
and the configured seven-day/text and three-day/media retention windows. Its
temporary database and network are removed in a `finally` block even when a
check fails.

Use `.\scripts\test.ps1 -Deep` before production releases to additionally run
the pinned Staticcheck `v0.7.0` analyzer and Go race detector. Their compiler
tooling is installed only inside disposable containers.
