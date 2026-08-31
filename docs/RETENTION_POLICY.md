# Data retention policy

The Gateway loads all active data lifetimes into `config.RetentionPolicy`.
Runtime writers pass explicit expiration timestamps to the store; database
defaults are a recovery boundary for manual or older clients, not a second
source of product behavior.

| Data | Configuration | Default | Current behavior |
| --- | --- | ---: | --- |
| Quick-generation history | `GENERATION_RETENTION` | 24 hours | Finished attempts disappear with their generated files. Active attempts remain visible. |
| Generated image/video media | `GENERATION_RETENTION` | 24 hours | Encrypted media and tracked ComfyUI outputs are removed after expiry. |
| AI-content metadata | `AI_CONTENT_RETENTION` | 7 days | Prompt, response, settings and status remain after media expiry; the UI shows an archived state instead of a broken preview. |
| ComfyUI input files | `COMFY_INPUT_RETENTION` | 72 hours | Stored, user-owned input assets are removed by maintenance. Upload reservations still have a short safety timeout. |
| Host metrics | `HOST_METRIC_RETENTION` | 7 days | Old samples are deleted by maintenance; the dashboard may show a shorter chart window. |
| Audit log | `AUDIT_LOG_RETENTION` | 90 days | The policy is typed now; bounded audit cleanup and export are implemented in G0.5. |

`AI_CONTENT_RETENTION` cannot be shorter than `GENERATION_RETENTION`. The
generation setting deliberately controls both history and binary media so a
gallery entry cannot outlive its usable result.

The AI-content administration page reports active event count, live media
count and bytes, and the nearest event/media expiry. Cleanup operations are
idempotent and may safely be retried after interruption.
