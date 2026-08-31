# Dependency health contract

Gateway tracks the current availability of these dependencies with one state
model: ComfyUI, OpenWebUI, Ollama prompt assistant, content moderator, mining
agent, and Windows system monitor.

## States

| State | Meaning |
| --- | --- |
| `online` | The last successful heartbeat and, where required, the last data sample are still fresh. |
| `stale` | A recent success exists, but its freshness window has expired or the agent heartbeat has no fresh data sample. |
| `offline` | No successful check exists or the latest usable success is older than the offline threshold. |
| `misconfigured` | The dependency is not configured or a check proves that its connection settings are invalid. |

`last_success_at` is the latest successful current check. `last_data_at` is
tracked separately for the Windows monitor, so an old graph point can remain
visible without making the agent look online. The latest error is retained even
while the previous success is still inside its freshness window.

## Timing

The defaults are:

- `DEPENDENCY_CHECK_INTERVAL=10s`
- `DEPENDENCY_STALE_AFTER=45s`
- `DEPENDENCY_OFFLINE_AFTER=3m`

`DEPENDENCY_STALE_AFTER` must be at least twice the check interval.
`DEPENDENCY_OFFLINE_AFTER` must be greater than the stale threshold. The admin
dashboard shows the next scheduled check as a live countdown.

## Failure behavior

Historical host metrics stay available when the Windows agent is stale or
offline, but the gauges are visually marked as non-live. A priority generation
that cannot confirm the mining stop continues in normal mode with an explicit
warning and retry countdown. A confirmed generation HTTP error immediately
restores Retry and Cancel controls; only an ambiguous transport failure enters
the browser recovery loop.

The maintenance loop performs background probes. Admin dashboard requests may
claim only checks that are already due, so loading the page does not create a
second probe storm.
