# Generation job contract

`generation_jobs` is the canonical lifecycle record for every quick-generation
attempt. Existing request, variant, content and mining records remain as
specialized projections, but none of them may independently decide the current
job state after G1.1 is connected.

## State machine

The normal path is:

`draft -> preparing -> uploading -> waiting_for_resources -> queued -> running -> postprocessing -> archiving -> completed`

`uploading` is optional when the request has no input assets. `queued` may move
directly to `postprocessing` when ComfyUI completes before the next observation.
Terminal states are `completed`, `failed`, `cancelled` and `expired`.

| State | Meaning | Cancellable |
| --- | --- | --- |
| `draft` | The idempotency key has been accepted and the attempt exists durably. | yes |
| `preparing` | Access, safety, model, workflow and exact inputs are being validated. | yes |
| `uploading` | Input assets are being verified or transferred into the owned ComfyUI namespace. | yes |
| `waiting_for_resources` | Quota and mining/VRAM resources are being acquired. | yes |
| `queued` | ComfyUI accepted the prompt and owns it in its queue. | yes |
| `running` | ComfyUI is executing the workflow. | yes |
| `postprocessing` | Outputs exist and workflow postprocessing/final decoding is finishing. | no |
| `archiving` | Gateway is copying outputs into encrypted, user-owned storage. | no |
| `completed` | Outputs are archived and temporary resources are released. | no |
| `failed` | A stable technical code and user-safe explanation describe the failure. | no |
| `cancelled` | Cancellation was confirmed for every resource still owned by the job. | no |
| `expired` | An abandoned nonterminal attempt was reconciled after its recovery window. | no |

Every real state change is appended to `generation_job_transitions` with its
timestamp, attempt number, message and optional technical error code. Repeated
observations of the same state do not create duplicate transitions.

## Identity and links

- `public_id` is the opaque identifier exposed to the browser.
- `(user_id, request_id)` preserves browser idempotency.
- `prompt_id` links the accepted ComfyUI prompt.
- `parent_job_id` links a retry to its source attempt.
- `generation_requests.job_id` preserves request recovery.
- `quick_generation_variants.job_id` preserves reusable parameters and history.
- `content_events.generation_job_id` keeps prompt-assistant details, parameters
  and generated media in the same logical card.
- `quick_generation_mining_leases.generation_job_id` proves whether temporary
  mining resources have been released.

Temporary-account deletion nulls the job owner while retaining the username
snapshot for the same period as AI-content. User endpoints always require the
current owner id, so a deleted account cannot recover a retained job.

## Consistency rules

1. A handler creates or recovers the job before acquiring quota or resources.
2. State changes and transition rows are committed in one database transaction.
3. Terminal states set `finished_at` once and cannot transition again.
4. `completed` is allowed only after output archival and resource release.
5. Cancellation is accepted only in states marked cancellable and is confirmed
   against ComfyUI before the terminal transition.
6. Background reconciliation uses the same transition API as HTTP requests.
7. A global revision increments in the same transaction as every visible job
   change; Job Center SSE uses it only as an invalidation signal.

The rollout is additive: migration and backfill first, then launch/recovery,
status/cancellation, and finally the Job Center UI. Compatibility fields are
kept in sync until every existing consumer has moved to the canonical record.
