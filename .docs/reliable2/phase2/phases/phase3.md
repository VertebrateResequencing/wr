# Phase 3: Double-reservation prevention (section C)

Ref: [spec.md](../spec.md) sections C1, C2, C3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

TDD throughout: the reserve/reclaim tests run under `-race` in the Option-R
determinism style (`os.Getpid()` alive owner, a definitely-dead pid, short
`ItemTTR = 500ms`). Build/test with `-tags netgo`; unset ALL `OS_*` env vars for
`make test` / `make race`; GoConvey `So()` assertions; copyright headers on new
files.

This phase comes AFTER Phase 2 so the reserved-not-started liveness signal (C2)
is not itself starved by the single-reader backlog. C1 is groundwork for BOTH
C2 (confirm-dead reclaim, which relies on the host+pid recorded at reserve) and
C3 (the runner sends its scheduler element id on the SAME reserve request and
the server calls `Reserved` in the same `respondWithReservedJob` C1 edits), so
C1 is sequenced first (Item 3.1). After C1, C2 and C3 touch disjoint files
(C2: `jobqueue/server.go` + `reliable2_reserve_test.go`; C3: the scheduler
package + `cmd/runner.go` + the additive `SchedulerID` field and `Reserved`
call), so they form a parallel batch (Items 3.2, 3.3).

The three `clientRequest` fields (`Host`, `Pid`, `SchedulerID`) are
binc-tolerant ADDITIVE wire-only fields: an old runner sends the zero values
(pid 0 -> old-client fallback). No new RPC, no schema change.

## Items

### Item 3.1: C1 - Report and record host+pid at reserve

spec.md section: C1

Make a reserved job carry the reserving runner's host and pid immediately
(before `Started`), so C2 can confirm death via the scheduler independently of
the backlogged RPC stream. Sequenced FIRST; C2 and C3 build on it.

- Client (`jobqueue/client.go`): `Reserve`/`ReserveScheduled`
  (client.go:865/891) set `Host = os.Hostname()` and `Pid = os.Getpid()` on the
  reserve `clientRequest` (add the `Host`/`Pid` fields, client.go:213). This is
  the runner's OWN pid (overwritten by the command's pid at `Started`,
  applyJobStart serverCLI.go:893). Do NOT piggyback on the touch stream (that
  stream is exactly what Phase 2 decouples). No new RPC.
- Server (`jobqueue/serverCLI.go`): in `respondWithReservedJob`
  (serverCLI.go:813) record `cr.Host` and `cr.Pid` onto the reserved job. In
  `resetJobForReservation` (serverCLI.go:842-844) STOP zeroing `Host`/`Pid`
  (leave `StartTime` zeroed - it is still set at `Started`). Old client (no
  host+pid) -> pid stays 0.

Tests in the new file `jobqueue/reliable2_reserve_test.go`. Covers both C1
acceptance tests: (1) after a client reserves a job, the server-side job has
`Host == the client's host` and `Pid == os.Getpid()` of the reserving process
BEFORE any `Started` call (fail-before: `resetJobForReservation` zeroed them);
(2) a reserve request carrying no host+pid (old-client shape) leaves the job's
`Pid == 0` and the reservation still succeeds (backward compatible).

- [x] implemented
- [x] reviewed

### Batch 1 (parallel, after Item 3.1 is reviewed)

#### Item 3.2: C2 - Reserved-not-started liveness reclaim [parallel with 3.3]

spec.md section: C2

A reserved-not-started job whose TTR expires must be parked in Run and requeued
only after its runner is CONFIRMED DEAD (never on a `StartTime.IsZero()` proxy),
so a live-but-backlogged runner's job is never re-reserved while a genuinely
dead runner's job is still reclaimed. Depends on Item 3.1 (uses the recorded
host+pid). Reuses the UNCHANGED `markJobLost`/`confirmOrReleaseLostJob`
machinery. Touches `jobqueue/server.go` only (disjoint from Item 3.3).

In `ttrCallback` (server.go:3349-3357) split the
`if job.StartTime.IsZero() || job.Exited` branch:

- `job.Exited` (a released/finished item awaiting delay): unchanged -> return
  `queue.SubQueueDelay`.
- reserved-not-started (`StartTime.IsZero() && !job.Exited`): treat like the
  started path - set `Lost=true`, `FailReason=FailReasonLost`, `EndTime=now`,
  return `queue.SubQueueRun` (parked, un-reservable), and defer `markJobLost`
  (server.go:3375). `markJobLost` snapshots `job.Host`/`job.Pid`
  (server.go:3387-3388); `confirmOrReleaseLostJob` -> `confirmJobDead`
  (server.go:4180) -> `ProcessNotRunningOnHost` confirms death, then `killJob`
  requeues.
- the already-`Lost` early return (server.go:3361) is unchanged (a parked job is
  not re-marked/re-confirmed).

Old-client fallback (`pid == 0`): `confirmJobDead` returns false for pid 0
(server.go:4182), so the job is NOT confirmed dead and stays PARKED in Run -
never blindly re-reserved and never reverted to the old `StartTime`-based
requeue. A stuck parked job recovers when its `Started`/`Touch` finally drains
(applyJobStart / recoverLostTouchedJob clear `Lost`).

Tests in `jobqueue/reliable2_reserve_test.go` (run under `-race`; ItemTTR =
500ms). Covers all 3 C2 acceptance tests (map to Issue B): (1) alive owner not
re-reserved - a reserved-but-never-started job (host + `os.Getpid()` recorded)
whose TTR expires stays in `SubQueueRun` (`server.q.Get(key)` state ==
`queue.ItemStateRun`) with `job.Lost == true`, and a second client's 20
`Reserve(200ms)` calls all return `nil`; (2) confirmed-dead reclaimed, no hole -
a reserved-not-started job whose recorded pid is a definitely-dead pid on a
reachable host is requeued and becomes reservable again after death is
confirmed; (3) old-client fallback parks - a reserved-not-started job with
`Pid == 0` is parked in `SubQueueRun` and a second client's repeated `Reserve`
returns `nil`.

- [x] implemented
- [x] reviewed

#### Item 3.3: C3 - Never bkill a reserved LSF array element [parallel with 3.2]

spec.md section: C3

`killExcessCmds` must never `bkill` an LSF array element wr has already handed a
job reservation to, so a PEND->RUN element that just reserved+started a job is
not killed mid-job (repro: 38,302 of ~40k elements bkilled). Protection must be
robust to `bjobs` status lag: it does NOT depend on `bjobs` having caught up to
RUN. Depends on Item 3.1 (the runner sends `SchedulerID` on the same reserve
request; the server calls `Reserved` in the same `respondWithReservedJob`).
Isolated in the scheduler package + `cmd/runner.go` (disjoint from Item 3.2).

Correlation: an LSF runner knows its element id from `LSB_JOBID` +
`LSB_JOBINDEX` (cmd/runner.go:134-136); the killable id `killCollector.consider`
builds is exactly `jobid[index]` (lsf.go:1208-1218). So:

- Runner (`cmd/runner.go`): compute `SchedulerID` = `LSB_JOBID` (with
  `[LSB_JOBINDEX]` appended when set), empty for non-LSF; send it in the reserve
  `clientRequest` (add the `SchedulerID` field, client.go:213).
- Server (`jobqueue/serverCLI.go`): in `respondWithReservedJob`, when
  `cr.SchedulerID != ""`, call `s.scheduler.Reserved(cr.SchedulerID)`.
- Scheduler (`jobqueue/scheduler/scheduler.go`): add the public
  `Reserved(schedulerID string)` on `*Scheduler` plus a `reserved` method on the
  `scheduleri` interface; the LSF impl records the id in a concurrency-safe set;
  non-LSF impls no-op.
- LSF (`jobqueue/scheduler/lsf.go`): `killCollector`/`killExcessCmds`
  (lsf.go:1162-1204) MUST skip any element whose `killableID` is in the reserved
  set (never append it to `toKill`), even when its `bjobs` `STAT != RUN`.
- Bound the set: prune reserved ids no longer present in the LSF (e.g. drop ids
  absent from a full `bjobs` snapshot, since `parseBjobs` excludes exited
  elements) so it does not grow unboundedly over a long-lived manager.

BOUNDARY (N3): this phase owns ONLY the never-bkill-a-reserved-element
protection. Reducing the VOLUME of over-submission (array cap / uncapped `bsub`)
belongs entirely to bugfix 260722-1; coordinate, do not duplicate. Do NOT
implement the rejected "re-check RUN before bkill" approach.

Tests in `jobqueue/scheduler/scheduler_lsf_test.go` (edit; pure-function test,
no real LSF). Covers all 3 C3 acceptance tests (map to Issue B): (1) a
`killCollector` with `maxAllowed` exceeded and a reserved element id `12345[7]`
recorded, considering elements including `12345[7]` with `STAT == "PEND"`
(non-RUN, normally killable), leaves `12345[7]` NOT in `toKill` while an
unreserved non-RUN excess element IS in `toKill`; (2) a reserved id whose
element no longer appears in a subsequent `bjobs` snapshot is removed from the
set when the prune runs (bounded memory); (3) a non-LSF scheduler's
`Reserved(id)` is a no-op and does not error.

- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill (review all items in the
batch together in a single review pass).

## Regression guards (KEEP surfaces, section E1)

Re-run after this phase; all must stay green under `-race` (spec.md section E1):

- Background recovery window tests (`recoverInBackground`/`isRecovering`/
  `ErrRecovering`/`rescheduleReadyAfterRecovery`).
- `jobqueue/subscription_test.go` (#503), `jobqueue/live_jtouch_test.go`
  (live RAM/CPU/STDOUT incl. ssh-to-host), the `JobUpdateResync` reconnect/
  resync tests, `jobqueue/suspend_resume_test.go` + `wr status --suspended`,
  `jobqueue/modify_validation_test.go`, `jobqueue/serverWebI_test.go`, the
  `wr add --sync` client test.
- `jobqueue/reliable2_keep_test.go`, `jobqueue/reliable2_completion_test.go`,
  `jobqueue/reliable2_lost_test.go`, `jobqueue/reliable2_dbcompat_test.go`.
- `make test`, `make race`, `make lint` all clean (with all `OS_*` env vars
  unset).
