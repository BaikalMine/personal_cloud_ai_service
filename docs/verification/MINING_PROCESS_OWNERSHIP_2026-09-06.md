# Mining Process Ownership

## F04 Scope

Command fencing rejects stale requests, but an agent crash could previously leave
an already spawned launcher alive. This change owns coordinated mining launchers
and descendants with a Windows Job Object. It is enabled only when the agent's
existing `require_fenced_commands` setting is true. The production setting and
installed agent have not been changed.

## Implementation

- Acquire an exclusive, non-inherited file handle before reading managed process
  metadata or touching the named job. A second agent using the same state directory
  cannot modify the first agent's state or terminate its processes.
- Canonicalize the parent directory before naming the job. Windows short and long
  directory aliases must address the same ownership object.
- On startup, drain any prior named job before constructing a ready controller.
  A timeout or OS error is not an idle result.
- Use `PROC_THREAD_ATTRIBUTE_JOB_LIST` in `CreateProcess`, associating the launcher
  with its job before its initial thread executes. There is no separate
  create-then-assign window and no fallback to an unowned launch.
- Set `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`; do not allow handle inheritance or job
  breakaway. OS teardown of the owner's handle kills the associated process tree.
- Stop waits for job accounting to report zero active processes. The terminated
  kernel object is then replaced before another launch. New admission to a reused
  terminated object was rejected on the tested Windows host.
- A pending launcher without the expected miner process is unavailable, not idle.
  Stop for another profile is rejected. Launcher exit does not discard ownership
  while descendants remain alive.
- A known precondition rejection does not invent an unknown operation for the
  wrong profile and lock out the correct Stop. It also cannot clear an already
  unknown operation. Ordinary execution errors still require reconciliation.
- Compare submitted and canonical script paths. Keep the submitted alias usable
  for stopping the owned tree even if its script file has since been deleted.
- Orderly controller close drains the owned tree and releases the instance lock.
  A closed group cannot reopen itself or start another process.

The launcher remains the existing PowerShell transcript wrapper. Ordinary mining
console behavior is unchanged; native tests explicitly request hidden windows.
The new direct dependency is `golang.org/x/sys/windows` (module v0.47.0).

## Native Windows Verification

Only temporary test executables and scripts were launched. No real mining,
ComfyUI, Ollama, training, GPU workload, or installed agent was started or stopped.

- Whole-tree stop followed by another start on the same group.
- Second owner rejected without touching the first live group.
- Closed group rejects Stop/reopen and Start.
- Actual controller Start/Stop/Start through the PowerShell launcher.
- Submitted short path and canonical long path both match; Stop still works after
  deleting the fixture launcher script on the second iteration.
- Second controller cannot even modify the first controller's state file.
- Pending tree does not report idle, rejects another Start and wrong-profile Stop,
  and reports idle only after confirmed termination.
- Abrupt owner-process kill terminates launcher and child. A separate sentinel
  process remains alive. Restart reacquires ownership and observes zero processes.
- Abrupt kill before delayed child creation prevents that child from appearing.

The final entire Windows mining-agent test package passed ten consecutive runs.
The subprocess helper is intentionally skipped in the parent test process.
Windows `go vet` and the final mining-agent cross-build passed. The build was not
installed or launched.

An early integration run exposed short-path/long-path mismatching and was fixed.
An earlier primitive test exposed terminated-job reuse and was fixed. These are
retained as regression cases rather than omitted from the verification history.
Repeated execution also exposed a test assumption that a live group must contain
exactly one process. The test now checks the specific launcher's live OS handle
and a nonempty group; failure cleanup terminates the fixture before waiting.

## General Regression

The first full Go run incorrectly allowed packages that reset the same PostgreSQL
schema to run concurrently. It failed with migration deadlocks and missing tables.
This test database contained synthetic data only. The prescribed sequential
`go test -p 1 -count=1 -timeout=180s ./...` passed on a new test database, including
database, gateway, inference and store integration scenarios. After the final
precondition-error distinction, affected Linux packages passed another ten runs;
the Windows package was rebuilt for its final native repetition.

## Release Boundary And Remaining F04

- This is local verification, not a production release or full F04 acceptance.
  The coordinated release still needs matching Gateway and agent versions, an
  idle maintenance window, backup, and rollback checks.
- Windows 10 or later with JOB_LIST support is required. Unsupported API access
  fails the operation; the implementation does not silently launch outside a job.
- Direct manual launch, service/WMI-mediated processes outside this process tree,
  and external ComfyUI/OpenWebUI work still need common executor admission. Job
  ownership is not a general GPU sandbox and does not control unrelated processes.
- Already dispatched model calls with unknown outcomes still need executor proof;
  neither HTTP timeout nor health status proves completion.
- Mixed real GPU workloads, full batch cancellation with automatic miner return,
  the operator recovery journal, and coordinated rollout remain open.

## Primary API References

[Microsoft Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects)
documents descendant membership and kill-on-close behavior.
[Microsoft's atomic job assignment example](https://devblogs.microsoft.com/oldnewthing/20230209-00/?p=107812)
explains JOB_LIST assignment during process creation on Windows 10.
[Job accounting layout](https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-jobobject_basic_accounting_information)
defines the active-process count used for draining.
[Go Windows APIs](https://pkg.go.dev/golang.org/x/sys/windows)
provide the syscall bindings and command-line composition.
