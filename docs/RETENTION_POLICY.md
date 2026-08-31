# Data retention policy

The Gateway loads all active data lifetimes into `config.RetentionPolicy`.
Runtime writers pass explicit expiration timestamps to the store; database
defaults are a recovery boundary for manual or older clients, not a second
source of product behavior.

## Managed data

| Data / table | Configuration | Default | Owner and lifecycle |
| --- | --- | ---: | --- |
| Quick-generation history (`quick_generation_variants`) | `GENERATION_RETENTION` | 24 hours | Quick generation. Only terminal attempts expire; queued and running attempts remain. |
| Generated media (`content_media`, `comfy_output_ownership`) | `GENERATION_RETENTION` | 24 hours | Content and file isolation. Encrypted media and orphaned ComfyUI ownership rows expire together. |
| AI-content metadata (`content_events`) | `AI_CONTENT_RETENTION` | 7 days | Content review. The event remains after media expiry and renders an archived state instead of a broken preview. |
| ComfyUI inputs (`comfy_input_assets`) | `COMFY_INPUT_RETENTION` | 72 hours | File isolation. User-owned references expire after their last permitted use. |
| Host samples (`host_metrics`) | `HOST_METRIC_RETENTION` | 7 days | Monitoring. The dashboard can display a shorter chart window. |
| Dependency latency (`service_observations`) | `HOST_METRIC_RETENTION` | 7 days | Observability. Stores component/operation outcome, latency and optional generation correlation. |
| Gateway snapshots (`gateway_observations`) | `HOST_METRIC_RETENTION` | 7 days | Observability. Stores queue, backlog, leases, database size and cleanup freshness. |
| Audit (`audit_log`) | `AUDIT_LOG_RETENTION` | 90 days | Operations/security. Export is available before deletion. |
| HTTP telemetry (`proxy_requests`) | `PROXY_REQUEST_RETENTION` | 90 days | Gateway telemetry, including accepted quick-generation requests. |
| Closed WebSockets (`websocket_sessions`) | `WEBSOCKET_SESSION_RETENTION` | 30 days | Gateway telemetry. Open sessions are never removed. |
| Request recovery (`generation_requests`) | `GENERATION_REQUEST_RETENTION` | 7 days | Quick generation. The value cannot be shorter than `GENERATION_RETENTION`. |
| Daily quota counters (`quick_generation_daily_usage`) | `DAILY_USAGE_RETENTION` | 90 days | Generation quotas. Historical dates expire; the current counter remains. |
| Finished invitations (`invites`, `invite_uses`) | `INVITE_HISTORY_RETENTION` | 90 days | Access management. Only expired invitations and their dependent activations are removed. |

Sessions expire by their own absolute/idle deadlines. Temporary users are
deleted at account expiry, while preserved AI-content and media keep an
explicit deleted-user presentation. Configuration, recipes, proposals,
migration history and other lifecycle-bound tables remain until their owner or
parent object is removed.

`AI_CONTENT_RETENTION` cannot be shorter than `GENERATION_RETENTION`.
`GENERATION_REQUEST_RETENTION` also cannot be shorter than it. The generation
setting deliberately controls both history and binary media so a gallery entry
cannot outlive its usable result.

## Cleanup execution

Maintenance runs the retention pass at startup and then on the regular
15-minute maintenance interval. Each managed table is deleted independently in
small transactions using `FOR UPDATE SKIP LOCKED`:

- `DATABASE_CLEANUP_BATCH_SIZE` controls rows per batch (default `1000`, range `100..10000`);
- `DATABASE_CLEANUP_MAX_BATCHES` limits batches per table and pass (default `20`, range `1..100`);
- an error in one table is recorded as `partial` and does not skip the others;
- a repeated pass is idempotent and never changes current or active rows.

The administrator page `/admin/storage` reports database/table size, estimated
row count, oldest row, policy owner and the persisted result of the latest
cleanup. Any table unknown to the retention matrix is highlighted instead of
silently growing without an owner.

The AI-content page separately reports live event/media totals and nearest
expiry. `/admin/audit/export` streams the complete audit history up to an
optional RFC3339 `before` boundary as UTF-8 CSV, allowing an administrator to
archive the long-lived log before its retention window closes.
