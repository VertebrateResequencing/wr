# Suspend And Resume Non-Running Jobs Specification

## Overview

Add a first-class `suspended` state for live queued jobs. Users can pause
queued work with `wr suspend` and make it schedulable again with `wr resume`
without using the limit-group-zero workaround.

Suspension applies only to live non-running work that can otherwise become
schedulable: `delayed`, `ready`, and `dependent`. It does not apply to
`reserved`, `running`, `lost`, `buried`, or `complete`. Buried jobs remain a
failure state that must use `wr retry`; resuming a buried job must not make it
runnable.

Suspended jobs remain in the queue, live database, report-group lookups,
dependency graph, and limit-group metadata, but they are never candidates for
reservation. Resume re-evaluates current dependencies: unresolved dependencies
return the job to `dependent`; otherwise the job returns to `ready`, where the
existing scheduler and limit logic decide when it can run.

## Architecture

- `queue/`: add `ItemStateSuspended`, `SubQueueSuspended`, suspended queue
  storage, stats, transitions, and `Queue.Suspend`/`Queue.Resume`.
- `jobqueue/`: add `JobStateSuspended`, state mappings, client methods,
  server request handlers, persistence recovery, status summaries, REST
  filters, websocket state updates, and web status counts.
- `cmd/`: add `suspend.go` and `resume.go`; update `status.go`,
  `status_table.go`, `lsf.go`, and tests.
- `jobqueue/static/`: add suspended counts, progress bars, details filter, and
  details status text. Reuse delayed/dependent warning styling.

Public API:

```go
func (queue *Queue) Suspend(ctx context.Context, key string) error
func (queue *Queue) Resume(ctx context.Context, key string) error

func (c *Client) Suspend(jes []*JobEssence) (int, error)
func (c *Client) Resume(jes []*JobEssence) (int, error)
```

New constants and sentinels:

```go
const ItemStateSuspended ItemState = "suspended"
const SubQueueSuspended SubQueue = "suspended"
const JobStateSuspended JobState = "suspended"

var ErrNotSuspendable = errors.New("not suspendable")
var ErrNotSuspended = errors.New("not suspended")
```

Server request methods:

- `jsuspend`: suspend eligible jobs identified by `clientRequest.Keys`.
- `jresume`: resume suspended jobs identified by `clientRequest.Keys`.

Both return `serverResponse{Existed: count}` and ignore ineligible or missing
keys, matching `jkick`, `jdel`, and `jkill`.

## Section A: Queue State

### A1: Suspend And Resume Queue Items

As a queue user, I want a suspended subqueue, so that ready jobs can be made
non-reservable without deleting them.

**Package:** `queue/`
**File:** `queue/item.go`, `queue/queue.go`
**Test file:** `queue/item_test.go`, `queue/queue_test.go`

`Queue.Suspend` moves `delay`, `ready`, or `dependent` items to
`SubQueueSuspended`, sends the changed callback from the old subqueue to
`suspended`, and does not call `readyAdded`. It returns `ErrNotSuspendable` for
items in `run` or `bury`, removed items, and missing keys. Suspending a delayed
item removes it from delay processing; the original delay expiry must not
promote it while suspended.

`Queue.Resume` moves only suspended items. If the item has unresolved
dependencies, it moves to `dependent`; otherwise it moves to `ready`. It sends
the changed callback from `suspended` to the new subqueue and calls
`readyAdded(ctx, "resumed")` only when the item becomes ready. It returns
`ErrNotSuspended` for all non-suspended items.

**Acceptance tests:**

1. Given a ready item, when `Queue.Suspend(ctx, key)` is called, then
   `item.Stats().State == ItemStateSuspended`, queue stats are
   `Ready == 0` and `Suspended == 1`, and `Reserve("", 10*time.Millisecond)`
   returns a `queue.Error` wrapping `ErrNothingReady`.
2. Given the suspended item from test 1, when `Queue.Resume(ctx, key)` is
   called, then `item.Stats().State == ItemStateReady`, queue stats are
   `Ready == 1` and `Suspended == 0`, and `Reserve("", 0)` returns that item.
3. Given delayed, ready, and dependent items, when each is suspended, then all
   three have `ItemStateSuspended`, `Stats().Suspended == 3`, and no suspended
   item is returned by `Reserve`.
4. Given a delayed item with callback recorders, when it is suspended before
   its delay expires and time advances past the original delay, then
   `item.Stats().State == ItemStateSuspended`,
   `Reserve("", 10*time.Millisecond)` returns a `queue.Error` wrapping
   `ErrNothingReady`, `readyAdded` is not called, and changed callbacks include
   `delay -> suspended` but never `delay -> ready`.
5. Given a delayed item is suspended before its delay expires, time advances
   past the original delay, and it has no unresolved dependencies, when
   `Queue.Resume(ctx, key)` is called, then
   `item.Stats().State == ItemStateReady` and `Reserve("", 0)` returns it.
6. Given a delayed item is suspended before its delay expires,
   `Queue.Update` assigns a dependency on existing item `p`, and time advances
   past the original delay, when `Queue.Resume(ctx, key)` is called, then
   `item.Stats().State == ItemStateDependent`,
   `item.UnresolvedDependencies() == []string{"p"}`, and `Reserve` does not
   return it.
7. Given run and bury items, when `Queue.Suspend(ctx, key)` is called for each,
   then each call returns a `queue.Error` wrapping `ErrNotSuspendable` and the
   item states remain `ItemStateRun` and `ItemStateBury`.
8. Given a non-suspended ready item, when `Queue.Resume(ctx, key)` is called,
   then it returns a `queue.Error` wrapping `ErrNotSuspended` and the item
   remains ready.
9. Given a changed callback is installed, when a ready item is suspended and
   resumed, then callbacks are exactly `ready -> suspended` and
   `suspended -> ready`, each with one item.

### A2: Preserve Dependency Accounting

As a workflow author, I want suspended jobs to remain in dependency accounting,
so that pausing a parent does not unblock its children.

**Package:** `queue/`
**File:** `queue/queue.go`
**Test file:** `queue/dependency_queue_test.go`, `queue/queue_test.go`

Suspension must not remove dependency links. Dependencies may resolve while a
child is suspended; the child remains suspended until resumed.

**Acceptance tests:**

1. Given parent `p` is ready and child `c` is dependent on `p`, when `p` is
   suspended, then `c.Stats().State == ItemStateDependent`,
   `Reserve("", 10*time.Millisecond)` returns `ErrNothingReady`, and no
   changed callback reports `c` moving to ready.
2. Given child `c` is dependent on parent `p`, when `c` is suspended and `p` is
   removed, then `c.Stats().State == ItemStateSuspended`,
   `c.UnresolvedDependencies()` is empty, and no ready callback fires.
3. Given test 2, when `c` is resumed, then `c.Stats().State == ItemStateReady`
   and `Reserve("", 0)` returns `c`.
4. Given child `c` is suspended and still has unresolved parent `p`, when `c`
   is resumed, then `c.Stats().State == ItemStateDependent` and `Reserve`
   does not return `c`.

## Section B: Jobqueue Behaviour

### B1: Persist And Recover Suspended Jobs

As an operator, I want suspended state to survive manager restarts, so that a
paused queue stays paused.

**Package:** `jobqueue/`
**File:** `jobqueue/job.go`, `jobqueue/server.go`, `jobqueue/db.go`,
`jobqueue/serverCLI.go`
**Test file:** `jobqueue/jobqueue_test.go`

Add `JobStateSuspended` to `subqueueToJobState`, `itemsStateToJobState`,
`queueItemStatusState`, `itemStateToJobState`, `subscriptionUpdateState`, and
status grouping. When suspending or resuming, set `job.State`, call the queue
transition, and persist with `db.updateJobAfterChange(ctx, job)`.

Recovery from `db.recoverIncompleteJobs()` must put `JobStateSuspended` jobs
into `queue.SubQueueSuspended`. They must not trigger ready callbacks or
scheduler work during recovery.

**Acceptance tests:**

1. Given a ready job added through `Client.Add`, when `Client.Suspend` is
   called with that job key, then it returns `1`, `GetByEssence` returns
   `State == JobStateSuspended`, and `Reserve(50*time.Millisecond)` returns
   nil job and nil error.
2. Given the suspended job from test 1, when `Client.Resume` is called, then it
   returns `1`, `GetByEssence` returns `State == JobStateReady`, and
   `Reserve(2*time.Second)` returns that job key.
3. Given a running job, a buried job, and a complete job, when their keys are
   passed to `Client.Suspend`, then it returns `0` and their states remain
   `running`, `buried`, and `complete`.
4. Given a job suspended from the ready state, when the manager is stopped and
   restarted with the same database, then `GetByEssence` returns
   `State == JobStateSuspended`, `Reserve(50*time.Millisecond)` returns nil
   job and nil error, and `Client.Resume` makes the job reservable.
5. Given a child suspended from the dependent state and an incomplete parent,
   when the manager restarts, then the child is still `JobStateSuspended`; after
   the parent completes and the child is resumed, the child becomes
   `JobStateReady`.

### B2: Client And Server APIs

As a Go caller, I want typed suspend and resume methods, so that tools do not
shell out to the CLI.

**Package:** `jobqueue/`
**File:** `jobqueue/client.go`, `jobqueue/serverCLI.go`, `jobqueue/server.go`
**Test file:** `jobqueue/client_test.go`, `jobqueue/jobqueue_test.go`

```go
func (c *Client) Suspend(jes []*JobEssence) (int, error)
func (c *Client) Resume(jes []*JobEssence) (int, error)
```

`Suspend` sends `jsuspend`; `Resume` sends `jresume`. Both convert
`JobEssence` values with existing `jesToKeys`.

**Acceptance tests:**

1. Given one delayed, one dependent, and one ready job, when `Client.Suspend`
   receives all three keys, then it returns `3` and all three jobs become
   `JobStateSuspended`.
2. Given three ready jobs and one running job, when `Client.Suspend` receives
   all four keys, then it returns `3`; the three ready jobs become suspended
   and the running job remains running.
3. Given one reserved job and one lost job, when `Client.Suspend` receives both
   keys, then it returns `0` and their states remain `JobStateReserved` and
   `JobStateLost`.
4. Given two suspended jobs and one ready job, when `Client.Resume` receives
   all three keys, then it returns `2`; the suspended jobs become ready or
   dependent according to current dependencies and the ready job remains ready.
5. Given a delayed job is suspended before `DelayTime` expires, the original
   delay has elapsed, and it has no unresolved dependencies, when
   `Client.Resume` receives its key, then it returns `1`, `GetByEssence`
   returns `State == JobStateReady`, and `Reserve(2*time.Second)` returns that
   job key.
6. Given a delayed job is suspended before `DelayTime` expires, the original
   delay has elapsed, and an incomplete parent dependency is added before
   resume, when `Client.Resume` receives its key, then it returns `1`,
   `GetByEssence` returns `State == JobStateDependent`, and
   `Reserve(50*time.Millisecond)` returns nil job and nil error.
7. Given an empty `[]*JobEssence`, when `Suspend` or `Resume` is called, then
   each returns `0, nil`.
8. Given one missing key and one eligible key, when `Suspend` is called, then it
   returns `1`, no error, and the eligible job is suspended.

### B3: Preserve Limit-Group Scheduling Accounting

As an operator, I want suspended jobs to keep limit-group membership, so that
resumed work obeys current scheduler limits.

**Package:** `jobqueue/`
**File:** `jobqueue/server.go`, `jobqueue/serverCLI.go`
**Test file:** `jobqueue/jobqueue_test.go`, `jobqueue/mockrunner_test.go`

Suspend and resume must not edit `Job.LimitGroups`, stored limit-group rows, or
the limit-group suffix in a job scheduler group. Suspended jobs are excluded
from runner scheduling counts. Resumed jobs re-enter ready scheduling through
the existing limit check before any runner is requested. A suspended delayed
job must not enter scheduler counts when its original delay expires.

**Acceptance tests:**

1. Given two ready jobs in limit group `lg-suspend:1`, when one job is
   suspended, then `GetLimitGroups()["lg-suspend"] == 1`, the suspended job
   still has `LimitGroups == []string{"lg-suspend"}`, its scheduler group still
   ends in `~lg-suspend`, and only the non-suspended job can be reserved.
2. Given one running job holds limit group `lg-resume:1` and a second job in
   `lg-resume` is suspended, when the second job is resumed, then scheduler
   accounting records the resumed job as limit-skipped, `ReserveScheduled` for
   `110:30:1:0~lg-resume` returns nil while the first job runs, and the resumed
   job is reserved only after the first job completes or releases.
3. Given a delayed job in scheduler group `110:30:1:0` is suspended before
   `DelayTime` expires, when the original delay elapses, then runner scheduling
   does not request it, `ReserveScheduled("110:30:1:0")` returns nil, and
   `GetByEssence` returns `State == JobStateSuspended`.

## Section C: CLI Commands

### C1: Suspend Selected Jobs

As a CLI user, I want `wr suspend`, so that I can pause queued work by report
group, job id, command, file, or all jobs.

**Package:** `cmd/`
**File:** `cmd/suspend.go`
**Test file:** `cmd/suspend_test.go`

Selection flags match `retry`/`remove`: exactly one of `-f`, `-l`, `-i`, or
`-a`; `-z` and `-y` require `-i`; `-c`, `--mount_json`, and `--mounts` apply
to `-f`/`-l`; `--timeout` matches existing commands. `-a` means all live
jobs, but only `delayed`, `ready`, and `dependent` jobs are suspended.

Output on success:

```text
Suspended <changed> queued commands (out of <matching> matching)
```

**Acceptance tests:**

1. Given one delayed, one dependent, and one ready job in report group
   `rg-suspend`, when `wr suspend -i rg-suspend` runs, then stdout contains
   exactly `Suspended 3 queued commands (out of 3 matching)` and
   `wr status -i rg-suspend -o plain` prints three lines ending
   `\tsuspended`.
2. Given ready jobs in report groups `team-a-1` and `team-a-2`, when
   `wr suspend -i team-a -z` runs, then stdout contains exactly
   `Suspended 2 queued commands (out of 2 matching)` and status for both report
   groups prints `suspended`.
3. Given a ready job whose internal key is `<job-key>`, when
   `wr suspend -i <job-key> -y` runs, then stdout contains exactly
   `Suspended 1 queued commands (out of 1 matching)` and
   `wr status -i <job-key> -y -o plain` prints `<job-key>\tsuspended`.
4. Given a command file `commands.txt` contains two queued commands, when
   `wr suspend -f commands.txt` runs, then stdout contains exactly
   `Suspended 2 queued commands (out of 2 matching)` and both commands are
   `suspended`.
5. Given one queued command `echo by-line` in cwd `/tmp/wr207`, when
   `wr suspend -l "echo by-line" -c /tmp/wr207` runs, then stdout contains
   exactly `Suspended 1 queued commands (out of 1 matching)` and the command is
   `suspended`.
6. Given two ready jobs are the only live jobs, when `wr suspend -a` runs, then
   stdout contains exactly `Suspended 2 queued commands (out of 2 matching)`
   and both jobs are `suspended`.
7. Given one ready job and one running job in report group `rg-suspend-mixed`,
   when `wr suspend -i rg-suspend-mixed` runs, then stdout contains
   `Suspended 1 queued commands (out of 2 matching)`, the ready job is
   suspended, and the running job remains running.
8. Given one buried job in report group `<buried-rg>`, when
   `wr suspend -i <buried-rg>` runs, then stdout contains
   `Suspended 0 queued commands (out of 1 matching)` and the job remains
   buried.
9. Given no selector, when `wr suspend` runs, then it exits non-zero with
   `1 of -f, -i, -l or -a is required`.
10. Given both `-i rg` and `-a`, when `wr suspend -i rg -a` runs, then it exits
   non-zero with `-f, -i, -l and -a are mutually exclusive`.
11. Given `-z` without `-i`, when `wr suspend -z` runs, then it exits non-zero
   with `-z and -y require -i`.

### C2: Resume Selected Jobs

As a CLI user, I want `wr resume`, so that I can restart work I suspended.

**Package:** `cmd/`
**File:** `cmd/resume.go`
**Test file:** `cmd/resume_test.go`

Flags and validation match `wr suspend`. `-a` resumes all suspended jobs.

Output on success:

```text
Resumed <changed> suspended commands (out of <matching> matching)
```

**Acceptance tests:**

1. Given two jobs suspended from the ready state in report group `rg-resume`,
   when `wr resume -i rg-resume` runs, then stdout contains exactly
   `Resumed 2 suspended commands (out of 2 matching)` and
   `wr status -i rg-resume -o plain` prints two `ready` states.
2. Given suspended jobs in report groups `team-b-1` and `team-b-2`, when
   `wr resume -i team-b -z` runs, then stdout contains exactly
   `Resumed 2 suspended commands (out of 2 matching)` and status for both
   report groups prints `ready`.
3. Given a suspended job whose internal key is `<job-key>`, when
   `wr resume -i <job-key> -y` runs, then stdout contains exactly
   `Resumed 1 suspended commands (out of 1 matching)` and
   `wr status -i <job-key> -y -o plain` prints `<job-key>\tready`.
4. Given a command file `commands.txt` contains two suspended commands, when
   `wr resume -f commands.txt` runs, then stdout contains exactly
   `Resumed 2 suspended commands (out of 2 matching)` and both commands are
   `ready`.
5. Given one suspended command `echo by-line` in cwd `/tmp/wr207`, when
   `wr resume -l "echo by-line" -c /tmp/wr207` runs, then stdout contains
   exactly `Resumed 1 suspended commands (out of 1 matching)` and the command
   is `ready`.
6. Given two suspended jobs are the only suspended jobs, when `wr resume -a`
   runs, then stdout contains exactly
   `Resumed 2 suspended commands (out of 2 matching)` and both jobs are
   `ready`.
7. Given a suspended child in report group `<child-rg>` whose parent is
   incomplete, when `wr resume -i <child-rg>` runs, then stdout contains
   `Resumed 1 suspended commands (out of 1 matching)` and status reports the
   child as `dependent`.
8. Given a job suspended from the delayed state in report group
   `rg-resume-delay` has no unresolved dependencies and its original delay has
   elapsed, when
   `wr resume -i rg-resume-delay` runs, then stdout contains exactly
   `Resumed 1 suspended commands (out of 1 matching)`, status reports `ready`,
   and client `Reserve(2*time.Second)` returns that job key.
9. Given a child suspended from the delayed state in report group
   `rg-resume-delay-dep` has an unresolved parent and its original delay has
   elapsed, when
   `wr resume -i rg-resume-delay-dep` runs, then stdout contains exactly
   `Resumed 1 suspended commands (out of 1 matching)`, status reports
   `dependent`, and client `Reserve(50*time.Millisecond)` returns nil job and
   nil error.
10. Given one suspended job and one ready job in report group `rg-resume-mixed`,
   when `wr resume -i rg-resume-mixed` runs, then stdout contains
   `Resumed 1 suspended commands (out of 2 matching)` and the ready job remains
   ready.
11. Given no selector, when `wr resume` runs, then it exits non-zero with
   `1 of -f, -i, -l or -a is required`.
12. Given both `-i rg` and `-a`, when `wr resume -i rg -a` runs, then it exits
   non-zero with `-f, -i, -l and -a are mutually exclusive`.
13. Given `-z` without `-i`, when `wr resume -z` runs, then it exits non-zero
   with `-z and -y require -i`.
14. Given `-y` without `-i`, when `wr resume -y` runs, then it exits non-zero
   with `-z and -y require -i`.

## Section D: Status And APIs

### D1: Show And Filter Suspended In `wr status`

As a CLI user, I want suspended jobs shown by status, so that paused work is
visible and countable.

**Package:** `cmd/`
**File:** `cmd/status.go`, `cmd/status_table.go`
**Test file:** `cmd/status_test.go`, `cmd/status_table_test.go`

Add `--suspended` to state filters. It can combine with `--pending`,
`--dependent`, `--running`, and `--buried` in default and report-group modes.
It is rejected in `-f`, `-l`, and `-i --internal` modes with error strings that
list `--suspended`.

`-o counts` output order becomes:

```text
complete: <n>
running: <n>
ready: <n>
dependent: <n>
suspended: <n>
lost contact: <n>
delayed: <n>
buried: <n>
```

Summary rows become:

```text
<rg> : complete=<n> running=<n> ready=<n> dependent=<n> suspended=<n> lost=<n> delayed=<n> buried=<n>...
```

Details output for a suspended job prints:

```text
Status: suspended - use `wr resume` to make it schedulable again
```

**Acceptance tests:**

1. Given one ready, one dependent, and one suspended job, when
   `wr status -o counts` runs without state filters, then output contains
   `ready: 1`, `dependent: 1`, and `suspended: 1`.
2. Given report group `rg-status-summary` has one ready and one suspended job,
   when `wr status -o summary` runs without state filters, then its row
   contains `ready=1` and `suspended=1`.
3. Given one ready, one dependent, and one suspended job, when
   `wr status --suspended -o counts` runs, then output contains
   `ready: 0`, `dependent: 0`, and `suspended: 1`.
4. Given the same jobs, when
   `wr status --pending --dependent --suspended -o counts` runs, then output
   contains `ready: 1`, `dependent: 1`, and `suspended: 1`.
5. Given a suspended job in report group `rg-status-suspended`, when
   `wr status -i rg-status-suspended -o plain` runs, then stdout has one line
   `<job-key>\tsuspended`.
6. Given a suspended job in report group `<rg>`, when
   `wr status -i <rg> -o details` runs, then output contains the exact suspended
   status text above.
7. Given `WR_STATUS_FORMAT=status:9 count:5` and one suspended job in report
   group `<rg>`, when `wr status -i <rg> -o table` runs, then the table
   contains a `suspended` status cell and does not truncate it at width 9.
8. Given `cmdFileStatus = "commands.txt"` and `showSuspended = true`, when
   `validateStatusStateFilters` runs, then it returns an error containing
   `--suspended` and `-f`.
9. Given `cmdLine = "echo by-line"` and `showSuspended = true`, when
   `validateStatusStateFilters` runs, then it returns an error containing
   `--suspended` and `-l`.
10. Given `cmdIDStatus = "<job-key>"`, `cmdIDIsInternal = true`, and
    `showSuspended = true`, when `validateStatusStateFilters` runs, then it
    returns an error containing `--suspended` and `--internal`.

### D2: Include Suspended In Client, REST, And Subscriptions

As an API caller, I want suspended jobs in status APIs, so that tools see the
same state as the CLI.

**Package:** `jobqueue/`
**File:** `jobqueue/client.go`, `jobqueue/server.go`,
`jobqueue/serverREST.go`, `jobqueue/server_subscription.go`
**Test file:** `jobqueue/status_count_test.go`, `jobqueue/rest_test.go`,
`jobqueue/subscription_test.go`

**Acceptance tests:**

1. Given report group `rg-api` has one ready and one suspended job, when
   `GetStatusByRepGroupMatch("rg-api", RepGroupMatchExact, nil, true, false)`
   is called, then `Counts[JobStateReady] == 1` and
   `Counts[JobStateSuspended] == 1`.
2. Given the same jobs, when `GetStatusByRepGroupMatch` is called with states
   `[]JobState{JobStateSuspended}`, then suspended count is `1`, ready count is
   `0`, and complete jobs are excluded unless `includeComplete` is true.
3. Given one suspended job, when `GET /rest/v1/jobs?state=suspended` is called,
   then the JSON array has length `1` and item `State == "suspended"`.
4. Given one suspended job and one running job, when
   `GET /rest/v1/jobs?state=deletable` is called, then the suspended job is
   included and the running job is excluded.
5. Given a client subscription for a job key, when the job is suspended and then
   resumed, then the subscription receives two `JobUpdateStateChange` updates
   with `State == JobStateSuspended` and then `State == JobStateReady`.
6. Given a suspended job, when `Job.ToStatus()` is called, then the returned
   status has `JStatus.State == JobStateSuspended`, and `Started` and `Ended`
   are nil unless the job had actually run before suspension.

### D3: Show Suspended In Web And LSF Views

As a web or LSF-compatibility user, I want suspended jobs visible in existing
inspection views, so that paused work is not hidden.

**Package:** `jobqueue/`, `cmd/`
**File:** `jobqueue/serverWebI.go`, `jobqueue/static/status.html`,
`jobqueue/static/js/wr/*.js`, `jobqueue/static/css/wr-0.36.0.css`,
`cmd/lsf.go`
**Test file:** `jobqueue/serverWebI_test.go`, `cmd/lsf_test.go`

Web UI requirements:

- Current and report-group progress bars include suspended counts.
- Suspended has a selectable state filter and details request state.
- Suspended detail panels use warning styling, may reuse delayed colour, and
  show status text `suspended - use wr resume to make it schedulable again`.
- No suspend/resume action buttons are required in the web UI for v1.

LSF compatibility maps `JobStateSuspended` to `PEND`.

**Acceptance tests:**

1. Given one suspended job, when `/status_ws` receives `{Request:"current"}`,
   then it emits a `jstateCount` for `+all+` with
   `ToState == JobStateSuspended` and `Count == 1`.
2. Given report group `rg-web` has one ready and one suspended job, when
   `/status_ws` receives `{Request:"current"}`, then it emits report-group
   progress data for `rg-web` with `ToState == JobStateReady`, `Count == 1`,
   `ToState == JobStateSuspended`, and `Count == 1`.
3. Given one suspended job in report group `rg-web`, when `/status_ws`
   receives `{Request:"details", RepGroup:"rg-web", State:"suspended"}`,
   then it emits one `JStatus` with `State == JobStateSuspended`.
4. Given the static web page is requested with `GET /status.html`, then the
   response body contains the visible label `suspended` in the state filter area
   and the details status area.
5. Given a suspended bsub-mode job, when `wr lsf bjobs -o "JOBID STAT"` runs,
   then its `STAT` column is `PEND`.

## Implementation Order

1. A1, A2: implement suspended item/subqueue state, transition methods, stats,
   callbacks, dependency accounting, and queue tests.
2. B1, B2, B3: add job state mappings, client methods, server handlers,
   suspend/resume helpers, db updates, recovery, dependency and limit-group
   scheduler behaviour, and jobqueue tests.
3. C1, C2: add `wr suspend` and `wr resume` with selection validation and
   command tests.
4. D1, D2, D3: update `wr status`, status summaries, REST, subscriptions, web
   websocket/static UI, LSF mapping, and related tests.

Phases are sequential. Visibility work depends on the persisted state and API
surface from phases 1 and 2; CLI commands depend on phase 2.

## Appendix: Key Decisions

- Suspendable states are `delayed`, `ready`, and `dependent` only. `buried`
  remains controlled by `retry`; `running` and `lost` remain controlled by
  `kill`/lost-job recovery; `complete` is immutable history.
- Resume does not restore an old delay countdown. A resumed job with unresolved
  dependencies becomes `dependent`; otherwise it becomes `ready`. Existing
  scheduler and limit-group logic still decide when ready work is reserved.
- Suspended jobs stay in live storage and report-group/dependency lookups. They
  do not decrement scheduler-group or limit-group metadata merely because they
  are paused.
- Tests use GoConvey and behaviour-level assertions per `go-conventions` and
  `testing-principles`. New Go source files use the project copyright header.
- Implementation/review should use `go-implementor` and `go-reviewer` with
  `go-conventions`.
