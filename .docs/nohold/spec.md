# Remove the Global Reserve Hold Specification

## Overview

Every `handleReserve` waits in `waitForPendingReserves` while `racPending` or
`racRunning` is set. That hold exists so that the ready-added callback (RAC) can
recompute a newly ready job's requirements and reserve group first. It is one
shared bool and is released early in two ways (P1: another operation's
`clearRACPending`; P2: a running RAC's `finishRAC` drops the hold of an
operation merged into its recall). Releases and delay expiries never take it at
all. A reserve in the gap takes the item at its old size, and
`resetJobForReservation` then blanks `FailReason`, so a failure bump is lost: a
job buried for RAM with `Retries: 0` and then kicked can rerun at the old RAM
and be buried again. A fresh runner's first reserve can also be told nothing is
ready, because `skipReserve` reads counts only the RAC sets.

This change (option D of `.docs/nohold/racpending-options.md`) makes an item
correct when it becomes ready, so reserves never wait on the RAC:

- The `queue` package calls a ready hook, under its lock, for every item
  entering the ready sub-queue. jobqueue's hook recomputes requirements (cached
  learned values plus failure bumps) and sets the real reserve group when a
  runner command is set.
- Failure bumps are applied when the job fails, atomically with the queue move,
  so `wr status` shows the size a retry will use. The high-peak RAM bump also
  applies to buried jobs. A `wr mod` of a resource clears that resource's
  pending bump.
- Learned values come from a per-ReqGroup cache, invalidated when a stat write
  creates a new key and refreshed at most once a second. No bolt I/O happens
  under the queue lock.
- The hold (`racPending`, `waitingReserves`, `waitForPendingReserves`) is
  removed. `skipReserve` skips only when the group's count is 0 and nothing in
  the runner's reserve group is ready. The RAC still refreshes schedulable ready
  jobs and schedules runners; the reliable4 bounded scan is unchanged.

## Architecture

### Packages and files

- `queue/queue.go`: `ReadyHook`, `SetReadyHook`, `ReleaseWith`, `RequeueWith`,
  `BuryWith`, `HasReady`, `ReadyAddedCallbackRunning`; every push into the ready
  sub-queue goes through one internal helper that calls the hook. Tests:
  `queue/ready_hook_test.go`, `queue/release_with_test.go`.
- `jobqueue/reqstat_cache.go` (new): the learned-requirement cache. Tests:
  `jobqueue/reqstat_cache_test.go`.
- `jobqueue/ready_prepare.go` (new): the jobqueue ready hook, failure-bump
  helpers and the bump-cleared bits. Tests: `jobqueue/nohold_test.go`.
- Changed: `jobqueue/server.go`, `jobqueue/serverCLI.go`, `jobqueue/db.go`,
  `jobqueue/job.go`, `jobqueue/serverREST.go` and `CHANGELOG.md`
  (`jobqueue/running_dependent.go` is unchanged; see "Run end applied with the
  queue move").

### queue API

```go
// ReadyHook is called by the queue, with the queue locked, for every item
// about to enter the ready sub-queue, after the operation's own changes to the
// item (including any KickWith, ResumeWith, ReleaseWith or RequeueWith
// callback, and Update's new reserve group) and before the item is pushed. It
// gets the item's data and current reserve group and returns the reserve group
// the item is pushed under. It must not call the queue.
type ReadyHook func(data any, reserveGroup string) string

// SetReadyHook sets the queue's ReadyHook, under the queue's lock. nil (the
// default) leaves every item's reserve group as it is.
func (queue *Queue) SetReadyHook(hook ReadyHook)

// ReleaseWith is Release, except that releasing, if not nil, is called with
// the item's data and the queue locked, once the item is known to be in the
// run sub-queue and before it leaves it. It is not called if the release
// fails. releasing must not call the queue.
func (queue *Queue) ReleaseWith(ctx context.Context, key string,
	releasing func(data any)) error

// RequeueWith is Requeue with a callback, as ReleaseWith.
func (queue *Queue) RequeueWith(ctx context.Context, key string, deps []string,
	requeueing func(data any)) error

// BuryWith is Bury with a callback, as ReleaseWith.
func (queue *Queue) BuryWith(key string, burying func(data any)) error

// HasReady reports whether at least one item with the given reserve group is
// in the ready sub-queue. A closed queue has none.
func (queue *Queue) HasReady(reserveGroup string) bool

// ReadyAddedCallbackRunning reports whether the ready-added callback is
// running or queued to run (readyAddedCbRunning), recall break included.
func (queue *Queue) ReadyAddedCallbackRunning() bool
```

For that flag to be a reliable idle signal, `rescheduleReadyAddedIfRecall` hands
off to the recall without clearing it: under `readyAddedCbMutex` it clears
`readyAddedCbRecall`, leaving `readyAddedCbRunning` set; it releases the mutex,
calls the test seam `recallHandOffHook` (an unexported package var, nil in
production) with the mutex not held, so the hook may call
`ReadyAddedCallbackRunning()`, and then starts the recall's `runReadyAddedCb`
goroutine. Today it clears the flag, unlocks, then calls `readyAdded("recall")`,
so the flag is briefly false. `SetReadyAddedCallback` writes `readyAddedCb`
under `readyAddedCbMutex`, and `readyAdded`/`runReadyAddedCb` read it under the
same lock, so a test replacing the callback on a live queue is race-free.

`Release`, `Requeue` and `Bury` become the `With` variants with a nil callback.
The ready-entry sources the hook covers are the `readyAdded` sources: new
(`Add`, `AddWithSize`, `AddMany`), delayed, dependent (`SatisfyDependency`,
`Remove*` promoting dependants), requeued, kicked, resumed, released (zero
delay), updated and ttr. "triggered" and "recall" move no items. The item is
pushed under the hook's group, so only reservers of that group are notified. In
`Update` and `updateUnlessRunning` the caller's reserve group and priority are
applied before the item is pushed into ready (today `applyItemPropertyChanges`
runs after the push), so they cannot overwrite the hook's group.
`applyItemPropertyChanges` keeps its one-category precedence: delay first, so
when the delay differs the caller's group and priority are not applied, as
today.

An item already in ready that gets new data (`Update`, or `replaceLiveRerunItem`
of a ready item) does not enter ready again, so the hook is not called.
`replaceLiveRerunItem` gives such an item the new, unprepared Job and the item
def's reserve group "". With a runner command set, no runner reserves group "",
so the item waits for the RAC to prepare and regroup it. With runner command "",
a group-less reserve can take it at once at its unlearned size. Both are
accepted.

### Learned-requirement cache (`jobqueue/reqstat_cache.go`)

```go
// reqStatCache holds each ReqGroup's learned requirements, as
// recommendedReqForGroup computed them from the jobRAM, jobDisk and jobSecs
// buckets. Entries are never evicted (production has about 130 ReqGroups).
type reqStatCache struct {
	mu       sync.Mutex
	entries  map[string]*reqStatEntry
	interval time.Duration // least time between load starts for one ReqGroup
	wg       sync.WaitGroup // loads started by refreshReqGroupRecsLater
	closed   bool
}

type reqStatEntry struct {
	rec        *scheduler.Requirements // last loaded; nil if never loaded
	gen        uint64                  // incremented by invalidate
	loadedGen  uint64                  // gen when rec's load started
	loaded     bool
	loadStart  time.Time
	loading    bool
}

// cachedReqGroupRecs returns reqGroup's last loaded learned requirements (nil
// if never loaded) and whether they are current: loaded, and not invalidated
// since the load started. It does no database I/O and is safe under the
// queue lock.
func (db *db) cachedReqGroupRecs(reqGroup string) (*scheduler.Requirements, bool)

// refreshReqGroupRecs loads reqGroup's learned requirements if they are not
// current, the last load started at least interval ago, and no load is in
// flight; otherwise it loads nothing. It returns the cached value afterwards.
func (db *db) refreshReqGroupRecs(reqGroup string) *scheduler.Requirements

// refreshReqGroupRecsLater is refreshReqGroupRecs in a goroutine tracked by
// the cache's wg, started only when a load is due. It does nothing once the
// database has started closing.
func (db *db) refreshReqGroupRecsLater(reqGroup string)

// warmReqGroupRecs calls refreshReqGroupRecs once for each distinct ReqGroup
// of jobs.
func (db *db) warmReqGroupRecs(jobs []*Job)

// invalidateReqGroupRecs marks reqGroup's entry not current.
func (db *db) invalidateReqGroupRecs(reqGroup string)
```

- A load computes exactly what `recommendedReqForGroup` computes today: RAM from
  `recommendedReqGroupMemory`, disk in GB rounded up with `DiskSet`, time in
  seconds. A ReqGroup with no stats loads `&Requirements{DiskSet: true}` with
  zero RAM, disk and time. The 95th percentile over distinct values is
  unchanged.
- `mu` is held only to read and update entries, never across a load: a load sets
  `loading`, releases `mu`, reads bolt, then retakes `mu` to store the result
  and clear `loading`. Callers that find `loading` set do not wait; they use the
  cached value.
- On a load error the entry keeps its previous `rec`, stays not current, and is
  retried once due. The error is logged at debug. So a RAC whose load fails
  keeps using that ReqGroup's last cached learned values, where today a failed
  load means no learned values for that cycle.
- Every stat write goes through one helper, which replaces `putJobStat`:

  ```go
  // putStat puts val for reqGroup in tx's bucket, keyed as putJobStat keys
  // it. If the Put created a new key, it registers tx.OnCommit to invalidate
  // reqGroup, so the invalidation happens only once the write is committed.
  // An overwrite of an existing key invalidates nothing. Repeated
  // invalidations of one ReqGroup in a transaction are harmless.
  func (c *reqStatCache) putStat(tx *bolt.Tx, bucket []byte, reqGroup string,
  	val int) error
  ```

  `putJobStats` (archive) and `jobExitData.updateFailStat` (exit ops) take the
  db's `*reqStatCache` and call `putStat`, as does the test helper
  `seedReqGroupStats`, so tests exercise the production invalidation path. Tests
  that call `putJobStat` directly, such as `queues_avoid_scheduling_test.go`
  (about line 105), call `putStat` instead, and `db_test.go`'s calls of
  `putJobStats` (about lines 325, 350 and 375) pass the cache.
- `scanReqGroupStat` is called with the prefix `reqGroup + dbDelimiter`, not
  `reqGroup`, so a ReqGroup's recommendation no longer includes the stats of any
  ReqGroup whose name starts with it (`"rg"` and `"rgz"`). This fixes a
  pre-existing bug, and is what makes per-ReqGroup invalidation sufficient.
- `db.close` sets `closed` and waits for `wg` before closing bolt.
- `reqStatLoadHook func(reqGroup string) error` (package var in
  `reqstat_cache.go`, nil in production) is called at the start of each load; a
  non-nil error fails the load. Tests count loads and inject errors with it.

`ServerTimings` gains:

```go
// ReqStatRefreshInterval is the least time between two loads of one
// ReqGroup's learned requirements from the database (default
// serverReqStatRefreshInterval, 1s). Negative means no limit, for tests.
ReqStatRefreshInterval time.Duration
```

`withDefaults` maps 0 to 1s and a negative value to 0; the db's cache uses the
result.

### Failure bumps (`jobqueue/ready_prepare.go`)

```go
// FailBump* are the bits of Job.FailBumpsCleared.
const (
	FailBumpRAM uint8 = 1 << iota
	FailBumpDisk
	FailBumpTime
)

// applyFailureBumpsLocked raises job's requirements for the failure its
// FailReason, PeakRAM, PeakDisk, StartTime and EndTime record, and does
// nothing for jobOverrideAlwaysUseJobReqs. A resource whose bit is set in
// FailBumpsCleared is skipped only while its value is still the user's
// value in ModifiedReqs; if learning replaced it, its increase applies as
// today. RAM is raised when FailReason is FailReasonRAM, or when
// shouldIncreaseJobRAMAfterHighPeak says so. Raise-only. Only
// updateJobRequirementsForRetry calls it, after step 2's reset, which is what
// makes the whole recompute idempotent. The caller holds job's lock.
func applyFailureBumpsLocked(job *Job)
```

- `updateJobRequirementsForRetry(job, jobOverride, rec)` is the one recompute,
  used at fail time (the release callback, and the lost-release report paths
  below), in the ready hook and in the RAC's `prepareReadyJob`, with `rec` from
  `cachedReqGroupRecs` outside the RAC:
  1. Set `RequirementsOrig` if nil.
  2. For a job with a pending failure (`FailReason != ""`, other than
     `FailReasonResource`), reset each of RAM, disk (`Disk` only; `DiskSet` is
     left as it is, as today) and time, whatever the override: one whose
     `FailBump*` bit is set to its `ModifiedReqs` value (the user's), and one
     whose bit is clear to its `FailedRunReqs` value. A record from an older
     manager (nil `FailedRunReqs`) keeps the current values of unmodified
     resources. This is a no-op when nothing was bumped or learned since. A
     `FailReasonResource` job, which `buryImpossibleItem` buries without
     `applyRunEndLocked`, is not reset: its `FailedRunReqs` may be from an
     earlier failure.
  3. `applyRecommendedJobRequirements` under `jobOverride`. As today, this runs
     for Override 2 too: it then fills in only resources the user did not
     specify (`RequirementsOrig` zero; see cmd/add.go's "If you choose to
     override eg. only disk...").
  4. If `jobOverride` is `jobOverrideAlwaysUseJobReqs`, stop here, as today.
  5. `applyFailureBumpsLocked(job)`: a resource with its bit set skips its
     increase only if step 3 left it at its `ModifiedReqs` value. Under Override
     0, a learned value replaces the user's (as today), and then the increase
     applies as today, so a modify never leaves the job at a learned size that
     its last failure showed too small.

  Every recompute therefore starts from the failed run's RAM, not from a RAM an
  earlier recompute already bumped, so the result is the same however often it
  runs, and for Override 0 and 1 it is what today's ready-time `prepareReadyJob`
  gives: the high-peak bump fires only when the peak exceeds the post-learned
  RAM, so learning that covers the peak means no bump
  (`memory_attribution_test.go`'s "Recommended RAM is applied before non-RAM
  high-peak retry growth is reconsidered"). Disk and time are reset too, so that
  a later `wr mod --override 2` (which sets no `FailBump*` bit) returns the job
  to its failed run's sizes plus learned values for unspecified resources. For a
  buried or delayed job that is today's result. For a ready job a RAC had
  already raised, today keeps the raised size (`preferredIntRequirement` keeps
  the current value under Override 2), so this is an accepted difference (see
  Key Decisions).
- `shouldIncreaseJobRAMAfterHighPeak(job)` keeps its signature and today's
  comparison of `PeakRAM` with `Requirements.RAM`, is false for
  `FailReasonResource` (an impossible job's kick then drops to the learned
  value, as today, instead of being bumped back to an impossible size), and no
  longer reads `job.State` (the changed callback is asynchronous and
  `kickChange` sets `State = Ready` inside `KickWith`).
- `newJobExitData` sets `requiredRAM` from `FailedRunReqs.RAM` when `FailReason
  != ""` and `FailedRunReqs` is not nil, and from `Requirements.RAM` otherwise,
  so the exit op's `shouldRecordHighPeakRAMStat` still sees the run's own RAM
  and writes the high-peak `jobRAM` stat although the bump has already raised
  `Requirements.RAM`.

`Job` gains three persisted fields:

```go
// FailBumpsCleared holds a FailBump* bit for each resource whose requirement
// a user modified since the job last failed: the increase that failure called
// for is no longer applied to it. A new failure clears it.
FailBumpsCleared uint8

// ModifiedReqs holds the RAM, Disk, DiskSet and Time a user's wr mod set,
// for the resources whose bit is set in FailBumpsCleared. The requirement
// recompute resets those resources to it and compares with it. It is never
// nil while any FailBump* bit is set; a new failure sets it to nil along
// with clearing the bits.
ModifiedReqs *scheduler.Requirements

// FailedRunReqs holds the RAM, Disk, DiskSet and Time the job's last failed
// run reserved, copied from Requirements when the failure is recorded and
// before any increase for it; that is what the run reserved, since a modify
// skips running jobs. Every requirement recompute starts from it, and the
// failure's jobRAM stat is judged against its RAM. Only read while
// FailReason is set; nil in records from older managers.
FailedRunReqs *scheduler.Requirements
```

As exported `Job` fields they also appear in JSON output (`wr status -o json`,
the REST API). That is intended: they explain why a retry has the size it has.

`copyJobForClient` (`jobqueue/serverCLI.go`) copies all three, since tests and
users read them through `GetByEssence`.

`JobModifier.applyRequirements` sets `FailBumpRAM` when it sets RAM,
`FailBumpTime` when it sets Time, and `FailBumpDisk` when `DiskSet`, and records
each value it sets in `ModifiedReqs` (allocating it if nil).

### Run end applied with the queue move

```go
// applyRunEndLocked applies a release's end of run to job: the end state
// (recordEndStateLocked, only if rep.endState exited and job.Exited is false),
// FailReason, FailedRunReqs (a copy of Requirements' RAM, Disk, DiskSet and
// Time), the retry budget and state (what applyRelease does today), clears
// FailBumpsCleared and sets ModifiedReqs to nil, then
// updateJobRequirementsForRetry with cachedReqGroupRecs, calling
// refreshReqGroupRecsLater if those are not current, as the hook does. It
// returns the scheduler group and log message applyRelease returns. The caller
// holds job's lock.
func (j *Job) applyRunEndLocked(rep releaseReport) (string, string)
```

For a release that takes the item out of Run (`releaseMoved`),
`applyReleaseQueueChange` and `buryItemWhereItIs` call `ReleaseWith`,
`RequeueWith` or `BuryWith` with a callback that locks the job and calls
`applyRunEndLocked`, keeping the scheduler group it returns. This covers every
path that takes an item out of Run on a failure: `buryReleasedItem` calls
`buryItemWhereItIs`, which uses `BuryWith` when the item is in Run
(`BuryWaiting` is unchanged); and `applyReleaseQueueChangeForRerun`
(`running_dependent.go`) passes its rerun dependencies to
`applyReleaseQueueChange`, which then uses `RequeueWith` with the same callback.
`running_dependent.go` itself does not change: `requeueRerun` (after a
successful archive) keeps plain `Requeue`, since no failure is recorded, and the
hook prepares the item if it goes to ready. `finalizeReleasedJob` then only
calls `job.decrementLimitGroups`, `decrementGroupCount` with that kept group (a
zero-delay release's hook may already have changed `job.schedulerGroup`) and
`writeReleasedJob` for that outcome. `releaseAlreadyDone` is unchanged. A
pre-existing race is also unchanged: after a zero-delay `ReleaseWith`, the item
can be reserved again before `finalizeReleasedJob` runs `decrementLimitGroups`
and `writeReleasedJob`.

### Owner's report after a lost release

No path takes a `FailReasonLost` item to ready without `applyRunEndLocked`:
`ttrCallback` parks a lost job in Run (`SubQueueRun`), and it leaves Run only
through `releaseRun` (`killLostRun`, the owner's report or a moved-on release).
Its `job.Exited` branch, which returns `SubQueueDelay`, is for an item whose end
state a release has already recorded, and that item reaches ready through the
delay sub-queue, where the hook runs the full recompute. Under this spec that
branch cannot carry a pending failure that skipped `applyRunEndLocked`: a
failure's end state is recorded on an item in Run only inside the
`ReleaseWith`/`RequeueWith`/`BuryWith` callback, which moves it out of Run in
the same lock hold.

A run the manager released as lost has `FailReasonLost` and the end state
`lostJobReleaseReport` gives it, exited with code -1 and PeakRAM 0 (which is
what lets `replaceLostEndState` see the owner's later exited report as the real
one), so its fail-time bump was nothing, and if its item is already ready the
hook prepared it without one. When the owner's real report arrives:

- `replaceLostEndState` is unchanged; it has no access to the cache. Its callers
  recompute: a new `func (s *Server) recomputeJobRequirements(job *Job)` locks
  the job and calls `updateJobRequirementsForRetry(job, job.Override,
  s.db.cachedReqGroupRecs(job.ReqGroup))` (lock order job, then the cache's
  `mu`), calling `refreshReqGroupRecsLater` when those values are not current,
  as the hook and `applyRunEndLocked` do. `FailedRunReqs` keeps the value the
  lost release recorded, which is the run's own.
- `ackAlreadyReleased` calls `recomputeJobRequirements` once when its
  `replaceLostEndState` returns true, before its `writeReleasedJob`, so the
  persisted record and `wr status` carry the increase at once.
- `releaseBuriedWaiting` keeps today's finalize steps (`updateAfterExit`,
  `replaceLostEndState`, `applyRelease`), then calls `recomputeJobRequirements`
  once, after `applyRelease`; that is the only recompute on this path.
- Neither path clears `FailBumpsCleared`. The report is about the same run the
  lost release ended, which already cleared it; any bit set since was set by the
  user's `wr mod` after that run ended, and is kept.
- After `ackAlreadyReleased`'s `replaceLostEndState` returns true, if the job's
  item is ready, the job is prepared again as after a modify (C3): recompute
  under the job lock, release it, then `q.SetReserveGroup`, so the lock order
  stays queue then job. A delayed or dependent item needs nothing more: the hook
  prepares it when it becomes ready.

### jobqueue ready hook

```go
// prepareForReady is the queue's ReadyHook. For a *Job it takes the job's lock
// and calls updateJobRequirementsForRetry with cachedReqGroupRecs(
// job.ReqGroup) unconditionally (not under prepareReadyJob's "rec != nil ||
// failureMayUpdateJobRequirements" guard), and, when the runner command is not
// "", sets job.schedulerGroup to the group of
// job.snapshotWithDerived(job.derivedLocked()) and returns that group. It must
// not call schedulerGroupSnapshot, which takes the job lock it holds. When the
// runner command is "" it returns reserveGroup unchanged. If the cached values
// are not current it calls refreshReqGroupRecsLater. It does no bolt I/O, takes
// no Server mutex, does not count racScanWork and never calls the queue. Other
// data is returned unchanged.
func (s *Server) prepareForReady(data any, reserveGroup string) string
```

- `createQueue` sets it with `q.SetReadyHook` before any item is added.
- Because the hook always runs `updateJobRequirementsForRetry`, a job's
  `RequirementsOrig` is now captured when it first becomes ready (normally at
  enqueue), not at the first RAC that prepares it. The two differ only for a `wr
  mod` made in between.
- Lock order stays queue, then job, then the cache's `mu`. The hook must not
  take `racmutex`: `waitThenRecheckRAC` holds `racmutex` and calls `q.Stats()`.
  The `rc` field is replaced by `rcValue atomic.Pointer[string]`, stored by
  `setRC` and by `Serve` (where `rc: config.RunnerCmd` is today);
  `runnerCommand()`, `readyAddedCallback`, `createJobs` (its `rcSet`) and the
  hook all read it, and an unset `rcValue` reads as "". Tests that build
  `Server{rc: ...}` literals (`schedule_retry_backoff_test.go`,
  `reliable2_scheduledeadlock_test.go`, `reliable4_schedule_async_test.go`) call
  `setRC` instead, so the hook and the RAC always agree.
- No caller may hold any job's lock while calling any queue method that takes
  the queue lock (read or write): the hook runs under the queue's write lock and
  locks the job of whichever item enters ready, which may not be the caller's,
  so even a `q.Get` or `Stats` under a job lock could deadlock. Jobs are already
  locked under the queue lock today (the `KickWith` and `ResumeWith` callbacks
  through `markItemJobChanged` -> `queueJobChangeAhead`, and `ttrCallback`);
  phase 3 adds the `ReleaseWith` callbacks and audits every `s.q.`/`q.` call in
  `jobqueue` for this and lists the result in its checklist. One known case must
  be fixed: `JobModifier.modifyJob` holds the job's write lock and, when the
  job's key changes, calls `skipForDuplicateKey` -> `checkJobByKey` ->
  `s.q.Get`. `handleModify` only pauses the server, so a delay expiry whose hook
  locks that job would deadlock with it. `modifyJob` releases the job's lock
  before that queue call (or does the duplicate-key check before taking the
  lock); C3 test 10 covers it.
- `recommendedReqForGroup` keeps its per-cycle map but fills it from
  `db.refreshReqGroupRecs`, so the RAC loads a ReqGroup only when it is not
  current and due.
- `enqueueItems` calls `warmReqGroupRecs` before `AddMany` on the jobs whose
  item defs start ready (no `StartQueue`, no dependencies), which covers
  recovered ready jobs. On the add path, `queueNewJobItems` calls it on every
  item def's job before `replaceLiveRerunItems`, which removes the replaced item
  defs before `enqueueItems` sees them, so live-rerun replacements are warmed
  too. `kickJobs` calls it on its jobs before its loop, and `resumeJobs` first
  collects the queued job of each key (`q.Get`), calls it on those, then
  resumes. Queue-internal entries (delay expiry, dependency satisfied, TTR) do
  not warm, and nor do existing dependents that become ready through `Update`
  ("updated") after `updateJobDependencies`; they rely on the RAC to catch up on
  learned values, and no warm is added there.

### Removed

`racPending`, `waitingReserves`, `setRACPending`, `clearRACPending`,
`clearRACWaiters`, `waitForPendingReserves`, the `readyCallbackExpected` logic
in `enqueueItems`, `updateJobDependencies`, `gatherDependencyUpdates`,
`updateLiveRerunItemWithReadyPending` and `kickJobs`, and
`itemDefTriggersReadyAdded` and `itemWillBecomeReadyAfterDependencyUpdate` if
unused. `triggerReadyAddedCallback` only calls `q.TriggerReadyAddedCallback`.
`racRunning` stays, set at the RAC's start and cleared at its end under
`rpmutex` (now documented as protecting `racRunning` only).

`func (s *Server) racIdle() bool` is added in phase 3. It returns
`!s.q.ReadyAddedCallbackRunning()` and, until phase 5, also `!racPending` under
`rpmutex`; phase 5 drops the `racPending` part. The queue's flag (A3) is set by
`readyAdded` before the operation that queued the callback returns, and stays
set through the callback, any recall and its break, so `racIdle()` true means no
RAC is running or queued; `!racRunning` alone would be true before a queued RAC
starts. Tests poll until `racIdle()` is true (`pollUntil`), never check it once.
A test with a runner command set may still see the RAC check timer
(`CheckRunnerTime`) start a RAC after the gate is installed; such a RAC is held
at the gate like any other, and only E1, whose runner command is "", relies on
gate indices.

## Section A: Queue ready hook

### A1: The hook runs for every entry into ready

As a queue user, I want one hook called for every item entering ready, so that
items are correct before any Reserve can see them.

**Package:** `queue/`
**File:** `queue/queue.go`
**Test file:** `queue/ready_hook_test.go`

Every acceptance test uses a queue whose hook records `(data, reserveGroup)` per
call and returns `"g2"` unless stated.

**Acceptance tests:**

1. Given the hook, when `Add(ctx, "k", "g1", "d", 0, 0, ttr, "")`, then the hook
   was called once with `("d", "g1")`, the item's `ReserveGroup` is `"g2"`,
   `Reserve("g1", 0)` errors with `ErrNothingReady` and `Reserve("g2", 0)`
   returns `"k"`.
2. Given the hook, when an item with group `"g1"` enters ready by each of:
   `AddMany` with no delay; `AddWithSize` with no delay; `Add` with a 20ms delay
   (after 200ms); `Remove` of its only parent; `SatisfyDependency` of its only
   dependency; `KickWith` of it buried; `ResumeWith` of it suspended with no
   dependencies; `Release` of it reserved with zero delay; `Requeue` of it
   reserved with nil deps; `Update` and `UpdateHolderUnlessRunning` each
   removing its last dependency with reserveGroup `"g1"`; TTR expiry with a TTR
   callback returning `SubQueueReady`, then for each source the hook was called
   exactly once for that item and its `ReserveGroup` is `"g2"`.
3. Given the hook, with its call count reset to 0 after each item's setup
   (adding it, reserving it and any `SetDelay`), when items go to delay (before
   expiry), dependent, run, bury or suspended through `Add`/`AddMany` start
   queues, `Suspend`, `Bury`, `Release` of a reserved item given a 1h delay with
   `SetDelay` after the reserve, `Requeue` with deps, `ResumeWith` to dependent
   and `KickWith` to dependent, then the hook was called 0 times.
4. Given `KickWith` and `ResumeWith` callbacks that append `"cb"` to a shared
   slice and a hook that appends `"hook"`, when each moves an item to ready,
   then the slice is `["cb", "hook"]`.
5. Given a goroutine blocked in `Reserve("g1", 2*time.Second)` and another in
   `Reserve("g2", 2*time.Second)`, when an item with group `"g1"` is added and
   the hook returns `"g2"`, then the `"g2"` reserve returns that item within
   200ms, and the `"g1"` reserve has not returned 500ms after the add and errors
   with `ErrNothingReady` after its 2s.
6. Given a hook that returns its `reserveGroup` argument, when an item with
   group `"g1"` is added, then its group is `"g1"`.
7. Given no hook set, when the queue package's existing tests run, then they
   pass unchanged.

### A2: Callbacks on release, requeue and bury

As jobqueue, I want to change a job under the queue lock as its run ends, so
that a zero-delay release can never be reserved before the change.

**Package:** `queue/`
**File:** `queue/queue.go`
**Test file:** `queue/release_with_test.go`

**Acceptance tests:**

1. Given a reserved item with delay 0, a callback appending `"cb"` and a hook
   appending `"hook"`, when `ReleaseWith`, then the slice is `["cb", "hook"]`
   and the item is ready.
2. Given an item added with delay 0, reserved, then given a 1h delay with
   `SetDelay`, and the hook's call count reset to 0 after that setup, when
   `ReleaseWith`, then the callback was called once, the item is delayed and the
   hook was not called.
3. Given a reserved item, when `RequeueWith` with nil deps, then the slice is
   `["cb", "hook"]` and the item is ready; with deps `["p"]`, the callback was
   called once, the item is dependent and the hook was not called.
4. Given a reserved item, when `BuryWith`, then the callback was called once,
   the item is buried and the hook was not called.
5. Given a ready item, when `ReleaseWith`, `RequeueWith` or `BuryWith`, then
   each returns an `Error` wrapping `ErrNotRunning` and the callback was not
   called; for a missing key, `ErrNotFound`; for a destroyed queue,
   `ErrQueueClosed`.
6. Given `Release`, `Requeue` and `Bury`, then each behaves as its `With`
   variant with a nil callback (the existing tests pass unchanged).

### A3: HasReady and the callback-running flag

As jobqueue, I want to ask whether a reserve group has ready items, so that
`skipReserve` can decide without the RAC's counts, and whether the ready-added
callback is running or queued, so that tests can wait for it.

**Package:** `queue/`
**File:** `queue/queue.go`
**Test file:** `queue/ready_hook_test.go`

**Acceptance tests:**

1. Given an empty queue, then `HasReady("g")` is false.
2. Given a ready item with group `"g"`, then `HasReady("g")` is true and
   `HasReady("h")` is false; after `Reserve("g", 0)`, `HasReady("g")` is false.
3. Given a ready item with group `""`, then `HasReady("")` is true.
4. Given a destroyed queue, then `HasReady("g")` is false.
5. Given a ready-added callback that blocks until released, when an item is
   added, then `ReadyAddedCallbackRunning()` is true when `Add` returns and
   while the callback blocks; when a second item is added during the callback
   and the callback is released, then the flag is true when `recallHandOffHook`
   runs and the hook reads it with `ReadyAddedCallbackRunning()` (the seam sits
   where today's code has just cleared the flag before `readyAdded("recall")`),
   it is true while the recall runs, and it is false within 2s of the recall
   returning.
6. Given a callback replaced with `SetReadyAddedCallback` while items are being
   added from another goroutine, then `go test -race ./queue` reports no race.

## Section B: Learned-requirement cache

### B1: Cache, invalidation on a new stat key

As the manager, I want learned requirements cached per ReqGroup and invalidated
only when they can change, so that ready-time preparation needs no I/O.

**Package:** `jobqueue/`
**File:** `jobqueue/reqstat_cache.go`, `jobqueue/db.go`
**Test file:** `jobqueue/reqstat_cache_test.go`

Tests use a server from `startDurabilityConfig` with `ReqStatRefreshInterval:
-1` unless stated, count loads with `reqStatLoadHook`, and seed stats with a
test helper `seedReqGroupStats(server, bucket, reqGroup, vals...)` that puts
each value with `server.db.reqStats.putStat` in one `db.bolt.Update`.

**Acceptance tests:**

1. Given ReqGroup `"rg"` with no stats, then `cachedReqGroupRecs("rg")` returns
   `(nil, false)`; after `refreshReqGroupRecs("rg")` (1 load) it returns a rec
   with RAM 0, Disk 0, Time 0 and true.
2. Given `"rg"` loaded with no stats, when RAM 300 is seeded, then
   `cachedReqGroupRecs` returns the old rec and false, and `refreshReqGroupRecs`
   loads once and returns RAM 300.
3. Given `"rg"` loaded after RAM 300 was seeded, when RAM 300 is seeded again
   (an existing key), then `cachedReqGroupRecs` returns RAM 300 and true and
   `refreshReqGroupRecs` does not load.
4. Given `"rg"` loaded, when a job of ReqGroup `"rg"` (added, reserved, then
   polling until `!racPending && !racRunning` under `rpmutex`, which keeps phase
   2 independent of phase 1, and from phase 3 until `racIdle()`, so no RAC can
   load `"rg"` after the archive) is archived with PeakRAM 700 (a new key)
   through the archive path, then after the archive returns
   `cachedReqGroupRecs("rg")` is not current.
5. Given `"rg"` loaded and `ReleaseDelayMin` 1h, when a job of `"rg"` is
   released by its runner with `FailReasonRAM` and PeakRAM 900, then once the
   durable `Release` has returned, `cachedReqGroupRecs("rg")` is not current
   (the item stays delayed, so no hook or RAC reloads `"rg"` first).
6. Given RAM values 100 to 2000 in steps of 100 seeded for `"rg"`, then the
   loaded rec's RAM equals `db.recommendedReqGroupMemory("rg")`, and its Disk
   and Time equal what `recommendedReqForGroup` computed before this change.
7. Given `reqStatLoadHook` returning an error for `"rg"` loaded at RAM 300 and
   then invalidated, when `refreshReqGroupRecs("rg")`, then it returns RAM 300
   and `cachedReqGroupRecs` reports false; when the hook stops erroring, the
   next refresh loads and reports true.
8. Given a server restarted on the same database, which holds no ready job of
   `"rg"` (recovery's warm would load it), then `cachedReqGroupRecs("rg")`
   returns `(nil, false)`.
9. (Red: prefix collision.) Given RAM 300 seeded for `"rg"` and RAM 5000 for
   `"rgz"`, then `refreshReqGroupRecs("rg")` returns RAM 300 and
   `db.recommendedReqGroupMemory("rg")` returns 300 (5000 on the base, whose
   scan of `"rg"` also reads `"rgz"`'s keys); `"rgz"` gives 5000.
10. Given `"rg"` and `"rgz"` both loaded, when RAM 800 is seeded for `"rgz"`,
    then `cachedReqGroupRecs("rg")` is still current.

### B2: Refresh rate limit and single flight

As the manager, I want each ReqGroup reloaded at most once a second, so that a
busy ReqGroup is not rescanned on every completion.

**Package:** `jobqueue/`
**File:** `jobqueue/reqstat_cache.go`, `jobqueue/server.go`
**Test file:** `jobqueue/reqstat_cache_test.go`

**Acceptance tests:**

1. Given `ServerTimings{}.withDefaults()`, then `ReqStatRefreshInterval` is 1s;
   with -1, 0; with 2s, 2s.
2. Given `ReqStatRefreshInterval` 2s and `"rg"` just loaded at RAM 300, when RAM
   values 400, 500, 600 and 700 are seeded 40ms apart with
   `refreshReqGroupRecs("rg")` after each, then there are 0 loads and each call
   returns RAM 300; when 2s have passed since the first load and it is called
   again, there is 1 load and it returns RAM 700 (five values, so the
   recommendation is the maximum); a further call makes 0 loads.
3. Given interval -1 and `"rg"` loaded, when the same 4 seeds and calls are
   made, then there are 4 loads, and the last returns RAM 700.
4. Given interval -1, `"rg"` not current, and `reqStatLoadHook` blocking until
   released, when `refreshReqGroupRecsLater("rg")` is called 20 times from 20
   goroutines and all 20 calls have returned before the hook is released, then
   there is exactly 1 load.
5. Given `"rg"` current, when `refreshReqGroupRecsLater("rg")` is called and the
   cache's `wg` is then waited on, then the wait returns at once and the load
   count is 0.
6. Given a load started by `refreshReqGroupRecsLater` and blocked in
   `reqStatLoadHook`, when `Stop` is called in a goroutine, then `Stop` returns
   only after the hook is released: it has not returned 200ms after the call,
   and returns within 1s of the release. A `refreshReqGroupRecsLater` after
   close starts no load.
7. Given a load of `"rg"` blocked in `reqStatLoadHook` (which blocks only for
   `"rg"`), then `cachedReqGroupRecs("rg")`, `refreshReqGroupRecs("rg")` and
   `refreshReqGroupRecs("other")` (a different ReqGroup) each return within
   100ms, the first two with `"rg"`'s previous value.
8. Existing tests that write stats and expect the next add or RAC to use them
   could be held back by the refresh limit once the RAC reads through the cache
   (B3, this phase). `jobqueueTestInit` therefore sets
   `Timings.ReqStatRefreshInterval = -1`, so every test built on it
   (`startDurabilityConfig` included) keeps today's behaviour, such as the
   learned-requirements check near `jobqueue_test.go` line 2494. Tests of the
   limit itself set a positive interval explicitly, as B2 test 2 sets 2s. This
   phase's checklist lists the tests that build their own `ServerConfig` and
   write or depend on learned stats, and each is checked; all existing tests
   pass at the end of phase 2.

### B3: The RAC reads the cache

As the manager, I want the RAC to load each ReqGroup only when its values may
have changed, so that the RAC does no more I/O than today.

**Package:** `jobqueue/`
**File:** `jobqueue/server.go`
**Test file:** `jobqueue/reqstat_cache_test.go`

**Acceptance tests:**

1. Given a paused server (as `reliable4_rac_bound_test.go` does) with the
   B-section default interval -1 and 100 ready jobs of ReqGroup `"rg"` and no
   new stats, when `buildSchedulerGroups(ctx, q, data, "true")` is called twice,
   then there is at most 1 load of `"rg"` in total.
2. Given the same, with the calls made with rc "" (so every ready job is
   prepared) and RAM 300 seeded between them, then the second call loads `"rg"`
   once and all 100 jobs have `Requirements.RAM` 300.
3. Given the existing `TestReliable4*` rac-bound tests, then they pass with
   `racScanWork` unchanged.

## Section C: Failure bumps at fail time

All new Section C, D and E tests are in `jobqueue/nohold_test.go`. The runner
command is "" unless a test says `serverRC` (C1.10, C3.5, D1.3, D1.5, D1.10,
D2.1, D3.1, D4.2, E2.1 to E2.3, E2.5); C1.1 depends on it being "". Unless
stated, the server is from `startDurabilityConfig` (ReleaseDelayMin 100ms) with
`Timings.ItemTTR = time.Minute` and `Timings.ReqStatRefreshInterval = -1` set
by the test, so a seed made just after a load is picked up by the next warm;
jobs use `standardReqs` (RAM 10), Override 0 and a ReqGroup unique to the test,
and the RAC is replaced by a gate (the probe's `racGates` and
`reserveReturnsWhileGated`, both moved into `nohold_test.go`) that is never
opened during the test. As the probe does, each test makes its setup adds and
reserves first, polls until `racIdle()` is true, then installs the gate, and
opens it at teardown (`defer gates.openAll()`). Until phase 5 an add, kick,
resume or `Modify` (whose `Resume` triggers the RAC), a suspend (`suspendJobs`),
a bury of a ready item (`buryItemWhereItIs`), an archive of a ready item
(`giveBackArchivedItem`), `decrementGroupCount` on a group with skipped jobs
(runner command `serverRC` with limit groups, as in D1.5) and the
`CheckRunnerTime` recheck, as well as `updateJobDependencies`, live-rerun
updates, `SetLimitGroup`, the impossible-schedule bury, the server's `Resume`
and `rescheduleReadyAfterRecovery`, all still set `racPending`, and a gated RAC
never clears it, so no test reserves after one of those while the gate is
closed. "Fails for RAM" is done after the gate is installed; its reserve is not
held, because the setup's RAC has run to its `finishRAC` (which `racIdle()`
waited for) and nothing since has set `racPending`. With runner command
`serverRC`, every reserve in these tests, including the one in "fails for RAM",
is `ReserveScheduled` on the job's current group, since a group-less `Reserve`
gets nothing. "Fails for RAM" means: reserve, `Started(job, os.Getpid())`, then
`Release(job, &JobEndState{Exited: true, Exitcode: 1, PeakRAM: 150, EndTime:
time.Now()}, FailReasonRAM)`. With PeakRAM 150 the RAM bump is to 1200
(max(150*2, 150+1000), rounded up to 100).

### C1: A release or bury applies the bump with the queue move

As a user, I want a failed job's retry size set when it fails, so that no
reserve can take it at the old size and `wr status` shows the retry size.

**Package:** `jobqueue/`
**File:** `jobqueue/server.go`, `jobqueue/ready_prepare.go`
**Test file:** `jobqueue/nohold_test.go`

**Acceptance tests:**

1. (Red: released-for-RAM gap.) Given `ReleaseDelayMin` 2s and a job with
   Retries 3, when it fails for RAM, then `GetByEssence` at once returns State
   delayed and `Requirements.RAM` 1200, and a second client's
   `Reserve(8*time.Second)` (the first retry delay is 2-4s) returns the job with
   `Requirements.RAM` 1200 while the RAC is gated.
2. Given a job with Retries 0, when it fails for RAM, then `GetByEssence`
   returns State buried and `Requirements.RAM` 1200.
3. Given a job with Retries 3, when it is released with `FailReasonDisk` and
   PeakDisk 2048, then `GetByEssence` returns `Requirements.Disk` 100.
4. Given a job with Retries 3, when it is released with `FailReasonTime` and an
   `EndTime` 10s after the `StartTime` that `GetByEssence` returned after
   `Started`, then `Requirements.Time` is 1h0m10s.
5. Given a job with Override 2 (`jobOverrideAlwaysUseJobReqs`), when it fails
   for RAM, then `Requirements.RAM` stays 10.
6. Given C1.1's delayed job, then `storedLiveJob` returns `Requirements.RAM`
   1200 once the release has returned.
7. Given C1.1's runner re-sending the same release after it returned, then the
   job's `Requirements.RAM` is still 1200 and its `UntilBuried` is 3.
8. Given the existing release, bury, lost-release and owner-report tests
   (`TestReliable4BusyExitStates`, `release_after_lost_test.go`,
   `runner_report_followups_test.go`), then they pass unchanged.
9. (Red: owner's report after a lost release.) Given runner command "", the RAC
   not gated, `Timings.ItemTTR` 1s and `ReleaseDelayMin` 100ms (unlike
   `release_after_lost_test.go`'s 1m delay), a started job with Retries 3 whose
   run the manager releases as lost after its TTR (as
   `release_after_lost_test.go` does), a `Modify` setting only Time 2h once the
   item is delayed or ready (either is fine), and its item then ready, when the
   owner's `Release(job, &JobEndState{Exited: true, Exitcode: 1, PeakRAM: 150},
   FailReasonRAM)` returns, then `GetByEssence` returns `Requirements.RAM` 1200,
   `Requirements.Time` 2h and `FailBumpsCleared` `FailBumpTime` (the user's
   modify is kept), and `Reserve(5*time.Second)` returns the job with
   `Requirements.RAM` 1200 and `Requirements.Time` 2h.
10. Given C1.9 with runner command `serverRC`, then when the owner's `Release`
    returns, the item's `ReserveGroup` and the job's scheduler group are the RAM
    1200 group.
11. Given `ReleaseDelayMin` 2s and C1.9's job delayed after the lost release and
    the `Modify`, when the owner sends `Bury(job, &JobEndState{Exited: true,
    Exitcode: 1, PeakRAM: 150}, FailReasonRAM)` instead (the
    `releaseBuriedWaiting` path), then the buried job has `Requirements.RAM`
    1200, `Requirements.Time` 2h and `FailBumpsCleared` `FailBumpTime`.
12. `TestJobqueueSignal` (`jobqueue_test.go`, about line 1829) asserts
    `job2.Requirements.Time.Seconds()` is 1 on the manager's copy of a job
    buried for `FailReasonTime`. It now asserts at least 3601 (the time bump is
    applied at bury), and the comment after it ("requirements only change on
    becoming ready") is rewritten to match. Every other existing test is swept
    for assertions on the manager's copy (`GetByEssence`, the queue item's job,
    `storedLiveJob`) of a failed job's requirements, and each one found is
    updated to the bumped value, with the list in the phase's checklist.
13. Given C1.9's setup with RAM 200 seeded for the job's ReqGroup before the add
    (so the cache holds a learned 200 and the run reserved 200), and a `Modify`
    setting only RAM 500 instead of Time, when the owner's RAM report returns,
    then `GetByEssence` returns `Requirements.RAM` 1200 and `FailBumpsCleared`
    `FailBumpRAM`: under Override 0 the learned 200 replaces the user's 500, so
    the increase applies, as today.

### C2: The high-peak bump applies to buried jobs

As a user, I want kicking a buried job that exceeded its memory estimate to
rerun it with more memory, as a retry already does.

**Package:** `jobqueue/`
**File:** `jobqueue/server.go`
**Test file:** `jobqueue/nohold_test.go`

"Low learning" means RAM stats 1 to 40 seeded for the test's ReqGroup before the
job is added. With the failure's own stat of 150 added, the learned RAM is then
100 (the sixth-highest of 41 values, 36, rounded up to 100), so learning does
not cover a peak of 150.

**Acceptance tests:**

1. (Red.) Given low learning, a job with Retries 0 and the RAC not gated, when
   it is released with `FailReasonExit` and PeakRAM 150, then `GetByEssence`
   returns State buried and `Requirements.RAM` 1200; after `Kick`, a
   `Reserve(5*time.Second)` returns it with `Requirements.RAM` 1200 (the base
   gives 100: its kicked job is no longer delayed, so no bump).
2. Given low learning, the RAC not gated and the same with PeakRAM 5, then once
   kicked the job has `Requirements.RAM` 100 (learned, no bump).
3. Given low learning, `ReleaseDelayMin` 2s and a job with Retries 3 released
   with `FailReasonExit` and PeakRAM 150, then at once it is delayed with
   `Requirements.RAM` 1200 and `FailedRunReqs.RAM` 100 (the run had the learned
   100), and once its delay expires its item's data has `Requirements.RAM` 1200.
4. Given a started job with Retries 3, when it is released with `Release(job,
   nil, FailReasonLost)`, then `Requirements.RAM` stays 10.
5. In `jobqueue/memory_attribution_test.go`, the leaf "A kicked non-RAM failure
   does not grow RAM from stale peak usage" (from #526) becomes "A kicked or
   buried non-RAM failure grows RAM from its peak usage": the same job (RAM 100,
   PeakRAM 200, `FailReasonExit`, State ready) gives
   `shouldIncreaseJobRAMAfterHighPeak` true and, after
   `increaseJobRAMAfterHighPeak`, RAM 1200; the same with State buried gives the
   same. New leaves, each a job with RAM 1200 (already bumped), PeakRAM 150 and
   `FailReasonExit`, run through `updateJobRequirementsForRetry`:
   - Override 0, `FailedRunReqs.RAM` 10: learned 100 gives 1200; learned 200
     gives 200 (learning covers the peak).
   - Override 1, `FailedRunReqs.RAM` 100: learned 200 gives 200 (a recompute
     from the bumped 1200 would keep 1200, so this pins the base as
     `FailedRunReqs`); learned 100 gives 1200.
   - Override 1, `FailedRunReqs.RAM` 100, `FailBumpsCleared` `FailBumpRAM`,
     `ModifiedReqs.RAM` 500 and RAM 500 (a user's `wr mod`): learned 200
     gives 500.
   - Override 0, `FailReasonRAM`, PeakRAM 150, `FailedRunReqs.RAM` 10,
     `FailBumpsCleared` `FailBumpRAM`, `ModifiedReqs.RAM` 500 and RAM 500:
     learned 200 gives 1200 (learning replaced the user's 500, so the increase
     applies); no learned value gives 500.
   - Override 0, `FailedRunReqs.RAM` 200, `FailBumpsCleared` `FailBumpRAM`,
     `ModifiedReqs.RAM` 500, PeakRAM 150, learned 200: run once with
     `FailReasonLost` (gives 200, no bump), then change FailReason to
     `FailReasonRAM` and run again: gives 1200 (the second run resets RAM to the
     stored 500 rather than taking the current 200 as the user's).
   - Override 0, `FailReasonResource`, RAM 64000 (impossible), PeakRAM 70000,
     `FailedRunReqs.RAM` 10, learned 200: gives 200, as today's kick does (no
     reset, no high-peak bump).
   - Running the first leaf's recompute twice gives the same RAM.

   The leaf "Recommended RAM is applied before non-RAM high-peak retry growth is
   reconsidered" (about line 237) passes unchanged on its own premise: its
   learned 300 covers its peak of 200, so its RAM is 300. A further leaf covers
   older records: a job with nil `FailedRunReqs`, RAM 100, PeakRAM 150 and
   `FailReasonExit`, run through `updateJobRequirementsForRetry` with no learned
   values, keeps its current RAM as the base and gets 1200. Another covers
   Override 2 set after a bump: a job with `FailReasonTime`, RAM 1200, Time
   1h10s, `FailedRunReqs` RAM 10 and Time 10s, `FailBumpsCleared` 0 and Override
   2 gets RAM 10 and Time 10s; the same holds for a ready job that a RAC already
   raised to RAM 1200, with `FailedRunReqs.RAM` 10 and Override 2: it gets 10
   (today it would keep 1200; accepted). Another covers Override 2 learning
   unspecified resources: a job with Override 2, `RequirementsOrig` RAM 10 and
   Disk 0 with `DiskSet` false, a pending `FailReasonExit` with PeakRAM 150, run
   through `updateJobRequirementsForRetry` with learned RAM 300 and disk 50,
   gets Disk 50 while RAM stays at the user's 10 (no learned RAM, no bump). The
   `TestHighPeakMemoryRetryGrowth` leaves that set `State: JobStateDelayed` keep
   their assertions with the State line removed, since the rule no longer reads
   it.
6. The comment "requirements only change on becoming ready" in
   `jobqueue_test.go` (before its `jq.Kick`, about line 7363) says instead that
   the manager applies a failure's increase when the job is buried; the
   assertion before it (`Requirements.RAM` 10) reads the runner's own copy of
   the job, which the manager does not change, and stays.
7. Given a job with Retries 3, when it is released with `FailReasonExit` and
   PeakRAM 150 and the durable `Release` has returned, then the `jobRAM` bucket
   holds the stat 150 for its ReqGroup (`putStat`'s key for 150 exists), and
   `db.recommendedReqGroupMemory` for its ReqGroup is 200, although its
   `Requirements.RAM` was already 1200 when the exit data was taken.
8. Given no prior stats, `ReleaseDelayMin` 2s and a job with Retries 3 released
   with `FailReasonExit` and PeakRAM 150, then at once it is delayed with
   `Requirements.RAM` 1200 (nothing learned yet); once its delay expires and the
   durable `Release` has returned, a direct `buildSchedulerGroups(ctx, q, data,
   "")`, made once a poll that calls `server.db.refreshReqGroupRecs(rg)` until
   `cachedReqGroupRecs(rg)` reports current has succeeded (in phase 3 nothing
   else reloads the ReqGroup; from phase 4 the call is a no-op while the hook's
   background load is in flight), leaves its `Requirements.RAM` at 200: its own
   stat of 150 is learned as 200, which covers the peak, so there is no bump, as
   today.
9. Given C2.8 with Override 1, then the same holds: 1200 while delayed, and 200
   after the direct `buildSchedulerGroups`, made after the same poll (from the
   bumped 1200 it would stay 1200).

### C3: `wr mod` clears a pending bump

As a user, I want a size I set with `wr mod` used when the job next runs,
instead of being raised again for its last failure.

**Package:** `jobqueue/`
**File:** `jobqueue/job.go`, `jobqueue/serverCLI.go`, `jobqueue/serverREST.go`
**Test file:** `jobqueue/nohold_test.go`

After a modify, every modified job whose item is ready is prepared again (as
`prepareReadyJob` does, `q.SetReserveGroup` included) before the server resumes,
in both `handleModify` and `modifyJobsByKeys`. The RAC is not gated in C3 tests
1 to 4 and 6 to 10. Override semantics are today's: under Override 0 a learned
value still replaces a modified size, so users who want their size kept use
Override 1 or 2.

**Acceptance tests:**

1. (Red.) Given a job with Override 1 and Retries 0 buried after failing for
   RAM, when `Modify` sets RAM 500, then `GetByEssence` returns
   `Requirements.RAM` 500, `FailBumpsCleared` `FailBumpRAM` and
   `ModifiedReqs.RAM` 500; after `Kick`, `Reserve(5*time.Second)` returns it
   with `Requirements.RAM` 500.
2. Given the same job, when `Modify` sets only Time 2h, then after `Kick` the
   reserved job has `Requirements.RAM` 1200 and `Requirements.Time` 2h.
3. Given `ReleaseDelayMin` 2s and a job with Override 1 and Retries 3 delayed
   after failing for RAM, when `Modify` sets RAM 500, then
   `Reserve(8*time.Second)` (the first retry delay is 2-4s) returns it with
   `Requirements.RAM` 500 once its delay expires.
4. Given C3.1's job reserved at RAM 500 and `Started`, when it fails for RAM
   with PeakRAM 600, then `GetByEssence` returns `Requirements.RAM` 1600,
   `FailBumpsCleared` 0 and `ModifiedReqs` nil.
5. (Red.) Given a server with runner command `serverRC`, the RAC gated, and a
   ready job (RAM 10, no stats), when `Modify` sets RAM 2000, then when `Modify`
   returns the item's `ReserveGroup` and the job's scheduler group both equal
   `schedulerGroupString(reqForScheduler(<the job's requirements>), nil)` with
   RAM 2000.
6. Given a manager restarted after C3.1's `Modify` and before the kick, then
   after `Kick` the reserved job has `Requirements.RAM` 500.
7. Given C3.1's buried job, when `server.modifyJobsByKeys(ctx, []string{key},
   <modifier setting RAM 500>)` (the REST path) returns, then `GetByEssence`
   returns `Requirements.RAM` 500 and `FailBumpsCleared` `FailBumpRAM`, and
   after `Kick` the reserved job has `Requirements.RAM` 500.
8. Given Override 0 and Retries 0, a job buried after failing for RAM
   (`Requirements.RAM` 1200), when `Modify` sets only Override 2, then after
   `Kick` the reserved job has `Requirements.RAM` 10. The same with a job buried
   after a `FailReasonTime` failure whose run took 10s (`Requirements.Time`
   1h0m10s) gives `Requirements.Time` 10s, the job's original time.
9. Given Override 0, Retries 0, a job buried after failing for RAM (RAM 10,
   PeakRAM 150, so its own stat gives a learned 200), when `Modify` sets RAM 500
   and the job is kicked, then the reserved job has `Requirements.RAM` 1200, as
   today: the learned 200 replaces the user's 500 under Override 0, so the
   increase applies.
10. Given `ReleaseDelayMin` 500ms, a job delayed after a release, and a test
    seam `modifyKeyCheckHook` (an unexported package var, nil in production)
    called by `modifyJob` just before its duplicate-key check that waits until
    the item's ready time plus 200ms has passed (the test reads `item.ReadyAt()`
    before calling `Modify`; the seam itself makes no queue call, which could
    deadlock), so the delay reliably expires and the hook tries to lock the job
    while `modifyJob` holds it, when `Modify` changes the job's Cmd (and so its
    key), then `Modify` returns within 5s, and the job is ready under its new
    key with its item prepared (the test discriminates from phase 4, when the
    hook exists: with the job lock held across the check, `Modify` deadlocks).

## Section D: Ready-time preparation

"Learned 300" means RAM 300 seeded for the test's ReqGroup before the job is
added, so the default-rounded recommendation is 300.

Section D tests check the queue item (`server.q.Get(key)`: its data and
`ReserveGroup`) as soon as it is ready, without reserving: until phase 5 a
reserve still waits on the gated RAC. Reserving at once is tested in E1 and E2.

### D1: Every item is prepared as it becomes ready

As a runner, I want every ready job already at its learned and bumped size and
in its real reserve group, so that what I reserve is what the scheduler sized me
for.

**Package:** `jobqueue/`
**File:** `jobqueue/ready_prepare.go`, `jobqueue/server.go`
**Test file:** `jobqueue/nohold_test.go`

**Acceptance tests:**

1. Given a job with Retries 0 buried after failing for RAM, with its in-memory
   `Requirements.RAM` set back to 10 and `FailedRunReqs` set to nil under its
   lock (as a record from an older manager has it), when it is kicked, then its
   item's data has `Requirements.RAM` 1200 when `Kick` returns.
2. Given learned 300 and runner command "", when a job is added, then when `Add`
   returns its item's data has `Requirements.RAM` 300 and its `ReserveGroup` is
   `""`.
3. Given learned 300 and runner command `serverRC`, when a job is added, then
   when `Add` returns its item's `ReserveGroup` is `schedulerGroupString(
   reqForScheduler(<reqs with RAM 300>), nil)` and equals the job's scheduler
   group.
4. Given learned 300, each of these is its own leaf, and in each, once the item
   is ready its data has `Requirements.RAM` 300:
   - a suspended job is resumed, with learned 300 seeded after the setup's RAC
     has finished and before the resume;
   - its dependency is satisfied: its parent, which shares its ReqGroup, is
     archived with PeakRAM 300 (the same stat key as the seeded 300, so no
     reload can move the learned value off 300); this relies on the parent's
     add having warmed the cache, since a dependent's add is not warmed;
   - its delay expires after a release with `FailReasonExit` and PeakRAM 5, its
     in-memory `Requirements.RAM` set back to 10 and `FailedRunReqs` set to nil
     under its lock while delayed (as a record from an older manager has it);
   - it is replaced by a live rerun (`replaceLiveRerunItem`) while it is
     dependent, and becomes ready when its dependency is satisfied.
5. Given runner command `serverRC`, a limit group `nohold:1` with one job
   running, and learned 300, when a second job of that limit group is added,
   then its item's data has `Requirements.RAM` 300 and its `ReserveGroup`
   matches RAM 300 with limit group `nohold`.
6. Given `racScanWork` stored as 0 after the gate is installed, when 10 jobs are
   added, then `racScanWork` is still 0.
7. Given a new ReqGroup with learned 300 and a cache entry that has never been
   loaded, when a job is added, then there was exactly 1 load of that ReqGroup,
   made before the item was queued, and the item's data has
   `Requirements.RAM` 300.
8. Given a buried job (RAM 10, Retries 0, buried after a `FailReasonExit`
   release with PeakRAM 5), learned 300 seeded for its ReqGroup after the bury,
   and then a manager restarted on the database (so the cache starts empty,
   B1.8), when it is kicked, then there was exactly 1 load of that ReqGroup, and
   once kicked the item's data has `Requirements.RAM` 300. The load hook checks
   the item's state as D4.1 checks the lock: it reads `q.Get(key)` in a
   goroutine, waits up to 1s for it, and records the state, which is buried.
9. Given the same with a suspended job (RAM 10) instead, when it is resumed,
   then there was exactly 1 load, during which the item was recorded as
   suspended in the same way, and the resumed item's data has
   `Requirements.RAM` 300.
10. (Red.) Given runner command `serverRC`, `ReleaseDelayMin` 2s, no stats, and
    a job with Retries 3 reserved with `ReserveScheduled` on its group (the RAM
    10 group), when it fails for RAM, then while it is delayed its item's
    `ReserveGroup` is still the RAM 10 group and its data has `Requirements.RAM`
    1200 (the fail-time callback does not regroup); once the delay expires with
    the gate closed, its `ReserveGroup` and the job's scheduler group are the
    RAM 1200 group. On the base it stays in the RAM 10 group with RAM 10.
11. Existing tests that assume add-time state, such as a reserve group of ""
    straight after an add with a runner command set, or unlearned RAM straight
    after an add, kick or resume, are swept. Each one found is updated to the
    prepared state, and the list is in phase 4's checklist.

### D2: Recovered ready items are prepared

As the manager, I want a recovered ready job prepared like any other, so that
reconnecting and new runners can take it at once.

**Package:** `jobqueue/`
**File:** `jobqueue/server.go`
**Test file:** `jobqueue/nohold_test.go`

`recoveredItemDef` no longer special-cases ready jobs (its comment about keeping
group "" goes); recovered running jobs still get their group there.

**Acceptance tests:**

1. (Red.) Given a stopped manager whose database holds a ready job (RAM 10) with
   learned 300, when a manager is started on it with `recoveryPauseHookForTest`
   blocking recovery, the test then calls `setRC(serverRC)` and gates the RAC,
   and releases recovery, then once the job is queued its item's `ReserveGroup`
   is the RAM 300 group and its `Requirements.RAM` is 300.
2. Given the same with runner command "", then the recovered item's
   `ReserveGroup` is `""` and its data has `Requirements.RAM` 300.
3. Given the existing recovery tests (running jobs recovered into Run with their
   group, buried and suspended jobs), then they pass unchanged.

### D3: The RAC remains a floor

As the manager, I want long-waiting ready jobs to keep picking up newer
learning, so that ready-time preparation does not freeze their size.

**Package:** `jobqueue/`
**File:** `jobqueue/server.go`
**Test file:** `jobqueue/nohold_test.go`

**Acceptance tests:**

1. Given runner command `serverRC`, interval -1, the RAC not gated, and a ready
   job at learned 300, when RAM 800 is seeded and the RAC is triggered, then
   within 5s (polling the outcome, not `racIdle()`) the job's `Requirements.RAM`
   is 800 and its item's `ReserveGroup` is the RAM 800 group.
2. Given the existing `TestReliable4Rac*` tests, then they pass and the
   limit-blocked remainder is still not prepared by the RAC.

### D4: No bolt I/O under the queue lock

As the manager, I want the queue lock never held across a database read, so that
preparation cannot stall every queue operation.

**Package:** `jobqueue/`
**File:** `jobqueue/ready_prepare.go`, `jobqueue/server.go`
**Test file:** `jobqueue/nohold_test.go`

**Acceptance tests:**

1. Given interval -1 and a `reqStatLoadHook` that, on each load, calls
   `server.q.Stats()` in a goroutine and blocks until that call returns or 1s
   passes, counting a timeout as under-lock (a load under the queue lock holds
   the lock while the hook waits, so `Stats` cannot return; the load does not
   depend on `Stats`, so this cannot deadlock), when, for a ReqGroup invalidated
   just before each step, jobs enter ready by add, kick, resume, delay expiry
   and dependency satisfied, then at least 1 load ran and 0 were under-lock.
2. Given D1.10's setup with its job delayed after failing for RAM (item still in
   the RAM 10 group), when the test holds `server.racmutex` for writing across
   the job's delay expiry, then `server.q.Stats()` returns within 1s while it is
   held, and after it is released the item's `ReserveGroup` is the RAM 1200
   group.
3. Given `setRC("")` after `setRC(serverRC)`, when a job is added, then its
   item's `ReserveGroup` is `""`.
4. `CGO_ENABLED=1 make race` passes with no race or deadlock in `queue` or
   `jobqueue`.
5. Given learned 300 loaded for a ReqGroup, then RAM 800 seeded, and a load of
   it started by `refreshReqGroupRecsLater` and blocked in `reqStatLoadHook`,
   when a job of that ReqGroup is added, then `Add` returns within 1s (its warm
   finds the load in flight and does not wait, and the hook does not wait on the
   cache) and the item's data has `Requirements.RAM` 300; once the load has
   completed (`cachedReqGroupRecs` reports current), a direct
   `buildSchedulerGroups(ctx, q, data, serverRC)` over the ready items brings it
   to 800.

## Section E: Remove the hold

### E1: Reserves never wait for the RAC

As a runner, I want a reserve to return as soon as a prepared item is ready, so
that new work starts without waiting for scheduling.

**Package:** `jobqueue/`
**File:** `jobqueue/serverCLI.go`, `jobqueue/server.go`
**Test file:** `jobqueue/nohold_test.go`

`TestNoHoldReadyPrepared` replaces the probe
(`.docs/nohold/racpending_probe_test.go.txt`): same setup (incomplete reserved
parent, suspended child, polling until `racIdle()` before gating), with
`ReqStatRefreshInterval: -1` and learned 300 for `"racprobe"` seeded before the
gate. Each leaf asserts the inverted contract: the reserve may return at once,
and the job it returns is prepared. Leaves 1 to 3 are red on the base: the
control leaf's reserve waits, and P1's and P2's return RAM 10. The base has no
`racIdle()`, so the red run on the base waits for `!racPending && !racRunning`
under `rpmutex`, as the probe does.

**Acceptance tests:**

1. (Control.) When a job is added with the RAC gated, then
   `reserveReturnsWhileGated` reports it returned, with the new job's key, and
   the reserved job's `Requirements.RAM` is 300.
2. (P1.) When a job is added with the RAC gated and the child is resumed back to
   dependent, then the reserve returns the new job with RAM 300.
3. (P2.) When a RAC is triggered and held at gate 0, a job is added (merged into
   a recall), gate 0 is opened and the recall is held at gate 1, then the
   reserve returns the new job with RAM 300.
4. Given a paused server, when a job is added, then `GetByEssence` returns RAM
   300 and `Reserve(10*time.Millisecond)` returns nil; after `Resume`, a reserve
   returns it with RAM 300.
5. (Red: kicked RAM-failed job.) Given a job with Retries 0 buried after failing
   for RAM and the RAC gated, when it is kicked, then `reserveReturnsWhileGated`
   reports it returned the job, with `Requirements.RAM` 1200. (On the base this
   fails because the reserve is held.)
6. (Red: kicked RAM-failed job, P1-style.) Given the same, when it is kicked and
   the probe's child is then resumed back to dependent (which on the base clears
   the hold, as in P1), then the reserve returns the kicked job with
   `Requirements.RAM` 1200; the base returns it with RAM 10.
7. Given the change, then this prints nothing:

   ```bash
   grep -nE 'racPending|waitingReserves|setRACPending|clearRACPending|waitForPendingReserves' jobqueue/*.go
   ```

### E2: skipReserve needs a zero count and nothing ready

As a fresh runner, I want my first reserve to get work that is ready in my
group, so that I do not exit idle while the RAC catches up.

**Package:** `jobqueue/`
**File:** `jobqueue/serverCLI.go`
**Test file:** `jobqueue/nohold_test.go`

`skipReserve` returns true only for a first reserve with a scheduler group and a
runner command, when the group's `previouslyScheduledGroups` count is 0 (or it
has no entry) and `q.HasReady(cr.SchedulerGroup)` is false. It reads the count
under `psgmutex`, releases it, then calls `q.HasReady`, so it adds no
`psgmutex`-then-queue lock order. A count above 0 proceeds, as today.

A fresh runner for a count-0 group whose ready items are all limit-blocked now
proceeds: `incrementReserveLimit` calls `s.limiter.Increment` with the reserve's
wait, which waits up to that timeout for capacity, and then the reserve returns
nothing, as any reserve with nothing available does.

**Acceptance tests:**

1. (Red.) Given runner command `serverRC` and the RAC gated, when a job is
   added, then a fresh client's first `ReserveScheduled(5*time.Second, <its
   group>)` returns it within 1s.
2. Given runner command `serverRC`, when a fresh client's first
   `ReserveScheduled(5*time.Second, "nohold-nothing")` is made for a group with
   no count and no ready item, then it returns nil within 1s.
3. Given runner command `serverRC`, the RAC not gated and a group whose only job
   a RAC counted and a client then reserved (count 1, nothing ready), when a
   fresh client's first `ReserveScheduled(300*time.Millisecond, group)` is made,
   then it returns nil after at least 300ms.
4. Given runner command "", then `skipReserve` returns false for any request.
5. Given runner command `serverRC` and the RAC gated, and then, after the gate
   is installed (so the group's count stays 0), a limit group `nohold-lb:1` with
   one job added and reserved by a client's `ReserveScheduled` on the
   limit-suffixed group (so the limiter counts 1) and a second job of it added
   and ready, when a fresh client's first
   `ReserveScheduled(500*time.Millisecond, <the second job's group>)` is made,
   then it returns nil after at least 500ms and within 1.5s, and the second job
   is still ready.

### E3: Tests and comments that encode the hold

As a maintainer, I want every test and comment that relied on the hold reworked,
so that none silently tests a contract that no longer exists.

**Package:** `jobqueue/`
**File:** `jobqueue/server.go`, `jobqueue/serverCLI.go`
**Test file:** as listed

Comments rewritten or deleted with the hold: the `racPending` block comment,
`setRACPending`, `clearRACPending`, `finishRAC`, `waitForPendingReserves`,
`resumeQueueItem`'s doc, `kickJobs`' "prepared before RAC-pending" comment,
shutdown's "free any waiting reserves", `prepareReadyJob`'s and
`ensureReserveGroup`'s notes about add-time groups, `recoveredItemDef`'s, and
`resumeItemReadyHook`'s (the seam stays, still called once `ResumeWith`
returns). `finishRAC` becomes the clear of `racRunning` only.

**Acceptance tests:**

1. `TestResumeRacingReserveKeepsRACPending` becomes
   `TestResumeRacingReserveGetsPreparedJob`: with `ReqStatRefreshInterval: -1`
   (its add warms the cache under 1s before the seed) and RAM 300 seeded for its
   ReqGroup after the target is suspended and before it is resumed, the job
   `resumeItemReadyHook` reserves has `Requirements.RAM` 300, and the other
   reserve returns nil without waiting for the gate (within 1s).
2. `TestRerunReplacementReadyCallbackBlocksReserve` becomes
   `TestRerunReplacementReservableAtOnce`: with the RAC held, the reserve
   returns the child within 1s with RepGroup `"rerun-replacement-new"`.
3. `slowRequestStallServer` (`reliable4_slow_request_test.go`) holds the queue's
   lock instead: it adds a ready item, calls `SuspendWith` in a goroutine whose
   callback signals and then sleeps for `hold`, and returns once signalled. The
   four slow-request leaves pass with their current assertions.
4. `depgranularity_add_test.go`, `reliable4_rac_bound_test.go`,
   `reliable4_reportstorm_test.go`, `reliable4_snapshot_memo_test.go` and
   `reliable4_repro_test.go` read `server.racIdle()` instead of `racRunning`
   under `rpmutex`, and pass.
5. `resume_order_test.go`'s other tests and comments no longer mention the hold,
   and pass.

## Section F: CHANGELOG

### F1: CHANGELOG entries

As a user, I want the release notes to say what changed for me and for the Go
API.

**File:** `CHANGELOG.md`

A new `## [Unreleased]` section above `## [0.39.0] - 2026-10-12` with exactly
these entries (each under its heading, in this order):

```markdown
### Added
- Go API: `queue.Queue.SetReadyHook` (with `queue.ReadyHook`),
  `ReleaseWith`, `RequeueWith`, `BuryWith`, `HasReady` and
  `ReadyAddedCallbackRunning`, and
  `jobqueue.Job.FailBumpsCleared` with its `jobqueue.FailBump*` bits, and
  `jobqueue.Job.ModifiedReqs` and `jobqueue.Job.FailedRunReqs`, which also
  appear in JSON output (such as `wr status -o json` and the REST API), and
  `jobqueue.ServerTimings.ReqStatRefreshInterval`.

### Changed
- The resources of a command waiting to run again after a failure are now
  worked out afresh from the failed run each time, rather than only ever
  raised. With `--override 1`, a later, lower learned value can now lower
  an earlier increase. `wr mod --override 2` on such a command now always
  returns it to the failed run's size, even if the manager had already
  raised it.

### Fixed
Grouped by when each bug could affect you, most likely first.

#### Everyone
- `wr kick` of a command buried for using too much RAM now reruns it with
  more memory, instead of sometimes at the old size, which could bury it
  again. Kicking a command buried for any other failure after it used more
  memory than it reserved, or resuming such a command after suspending it
  while it waited to retry, now also reruns it with more memory, as a retry
  already did.
- A retry after a memory, disk or time failure no longer sometimes runs at the
  old size. `wr status` now shows the increased size while the command is
  delayed or buried.
- A memory, disk or time set with `wr mod` on a command that failed is now
  used when it next runs, instead of being raised again for that failure,
  wherever wr uses your size: with `--override` 1 or 2 for a resource you
  gave when adding the command, or when nothing has been learned for it. As
  before, a learned value still replaces your size under `--override` 0,
  and for a resource you did not give when adding the command.
- The memory, disk and time learned for a requirements group (`--req_grp`)
  no longer include the usage of other groups whose names start with its
  name, such as `rg` and `rgz`. Learned reservations for such groups may
  change.
- A newly started runner no longer sometimes exits at once without running
  anything when commands were ready for it.

#### With large queues or many runners
Performance only.
- Runners no longer wait for the manager's scheduling pass before they can
  take newly added, kicked or resumed commands.
```

**Acceptance tests:**

1. `CHANGELOG.md` has one `## [Unreleased]` heading, directly above `##
   [0.39.0] - 2026-10-12`, containing the text above.

## Section G: Verification bar

This change meets DEVELOPERS.md section 5 for a reserve-path and scheduler
change. Each run is compared with the same run on the PR's `origin/develop`
merge-base.

### G1: Local gates

1. `make lint`, `make test` and `CGO_ENABLED=1 make race` pass (all `OS_*`
   unset).
2. `make speed` reports PASS, or every row it marks worse is re-run alone
   (`SPEED_BENCH='^Name$' SPEED_COUNT=10 make speed`) and either passes or is
   explained in the PR body.
3. This reports no row more than 10% worse at p < 0.05:

   ```bash
   SPEED_BENCH='^Benchmark(Kick|Resume|Suspend|AddMany)UnderReserveLoad$' SPEED_COUNT=10 make speed
   ```

   `BenchmarkAddManyUnderReserveLoad` is new (phase 4, in
   `jobqueue/kick_resume_bench_test.go`, modelled on
   `BenchmarkKickUnderReserveLoad`): it adds 10,000 jobs in one `Add` while
   reservers run, so it measures the hook's cost per item under the queue's
   write lock, as recovery of about 118k live jobs pays it. The base has no such
   benchmark, so its file is copied into a worktree of the base for the
   comparison. As in G1.2, a row more than 10% worse is re-run alone and, if it
   stays worse, explained in the PR body. If the hook's own cost is what exceeds
   10% (shown by a CPU profile of the benchmark), the PR body gives the per-item
   cost and the recovery-time cost it implies for about 118k live jobs, and the
   owner decides whether to accept it.

### G2: Real-LSF scenarios

Each of `wrdev.sh add-storm-lsf`, `wrdev.sh report-storm-lsf` and `wrdev.sh
limit-drain` (defaults) is run on the base and on this tree, one at a time. Each
passes on this tree by its own criteria (`add-storm-lsf` prints its `PASS: all
<n> acknowledged adds` line; `limit-drain` drains every job), and the PR body
gives both trees' figures: jobs completed, wall time, lost and confirmed-dead
counts. A figure more than 10% worse on this tree is explained in the PR body.

### G3: prodsim soak

Two `developers/soak` soaks at production concurrency, base then this tree,
never concurrently, each with its own binary for the whole run, using the staged
ramp of the reserve-runstate F3 soak (`RAMP0=600 RAMP="0:600 8:1200 16:2100
25:3000 40:3600 60:4000"`, `HOURS=3`, its crash and stall injectors). This tree
passes when:

1. `soakgate.py` shows no double run and no lost job attributable to the change
   (each one it lists is traced and its cause recorded).
2. Steady ends per minute from `latency.py` are at least 90% of the base's.
3. `latency.py`'s steady end-to-acknowledgement p50 and p99 are no more than 10%
   worse than the base's.
4. Peak RUN is reported for both, against the 5,500 target.

The PR body records both run directories, the figures and the verdict.

## Implementation Order

1. **Phase 1: queue API (A1, A2, A3).** No jobqueue behaviour changes: the hook
   is unset and the `With` callbacks are nil.
2. **Phase 2: cache (B1, B2, B3).** Independent of phase 1; may run in parallel
   with it, but both precede phase 3. The RAC switches to the cache, and
   `jobqueueTestInit` gets `ReqStatRefreshInterval = -1` (B2 test 8) in the same
   phase.
3. **Phase 3: fail-time bumps (C1, C2, C3).** Needs phases 1 (A2) and 2 (C tests
   set `ReqStatRefreshInterval`; `applyRunEndLocked` uses `cachedReqGroupRecs`).
   Adds `racIdle()`, and switches B1.4 to it. C1.1 is green here even with the
   hold present.
4. **Phase 4: ready hook (D1, D2, D3, D4).** Needs phases 1 to 3. Introduces
   `rcValue` (replacing the `rc` field, with the `Server{rc: ...}` test literals
   switched to `setRC`), which D4.2 relies on. Its tests check queue items, not
   reserves, since the hold is still present.
5. **Phase 5: remove the hold (E1, E2, E3).** Needs phase 4: without ready-time
   preparation, removing the hold would let every reserve take unprepared items,
   and without E2 a fresh runner's first reserve would be skipped.
6. **Phase 6: CHANGELOG and gates (F1, G1, G2, G3).** Sequential: F1, G1, G2,
   then G3, the last step before the PR is ready.

## Appendix: Key Decisions

- **Option D, not B or C.** B fixes only the rare P1. C holds reserves through
  back-to-back RACs under churn and risks starvation. D removes the RAC as a
  precondition for a correct reservation and also closes the released-for-RAM
  gap, which never took the hold.
- **The hook runs before the push.** Pushing first and regrouping after would
  wake reservers of the old group, which then find nothing and return
  `ErrNothingReady` before their wait ends (A1 test 5).
- **The `queue` change is additive.** One hook, three `With` variants,
  `HasReady` and `ReadyAddedCallbackRunning` cover every requirement; no
  existing signature changes, so the CHANGELOG lists them under Added.
- **Run end applied inside the queue move.** Applying the end state before an
  unlocked `q.Release` would race another release of the same run: a lost
  release could set `FailReasonLost` after the owner's reason, and the owner's
  `replaceLostEndState` would then not fire. The callback runs only for the
  release that actually moves the item, under the same lock as the move and
  before the hook.
- **Bumps are re-applied, not consumed.** The RAM, disk and time bumps are
  raise-only and idempotent, so applying them at fail time, in the hook and in
  the RAC is safe, and a buried record from an older manager still gets its bump
  on kick (D1 test 1). A user's lower size would be overridden by that
  re-application, so `wr mod` records `FailBumpsCleared` and the values it set
  in `ModifiedReqs`, both persisted with the job and cleared by the next
  failure. Storing the values, not reading them back from `Requirements`, keeps
  the recompute idempotent: a learned value written by one recompute is never
  mistaken for the user's by the next. A set bit only protects the user's value
  while it is still in use: with Override 0 a learned value still wins over the
  user's size, as today, and then the failure's increase applies as today too.
- **Each recompute starts from the failed run's RAM.** As in today's
  `prepareReadyJob`, learned values come before the bumps, the bumps only raise,
  and the non-RAM high-peak bump fires only when the peak exceeds the RAM
  learning gives the retry. Applying learning to an already-bumped RAM would
  change that: with Override 1, `preferredIntRequirement` would keep a 1200 bump
  where today the retry gets a learned 200. So each recompute resets each
  unmodified resource to `FailedRunReqs` first, which makes it idempotent and
  gives today's ready-time result for every override, including a later switch
  to Override 2 on a buried or delayed job (which, as today, still applies
  learned values for resources the user did not specify, and no bumps). A
  delayed job can show 1200 at fail time, before its own stat is learned, and
  then retry at the learned 200, as today. The exit op judges the high-peak
  `jobRAM` stat against `FailedRunReqs.RAM`, so the stat is still written after
  the fail-time bump. The buried-job extension only removes the `State` check.
  Two differences from today are accepted as part of re-deriving idempotently.
  Under Override 1, today's RAC ratchets a ready failed job's RAM upward across
  cycles (the max of its previous and the learned value), whereas the reset
  means a later learned 200 can drop an earlier 1200. And `wr mod --override 2`
  on a ready failed job that a RAC already raised (say to 1200) keeps 1200 today
  but returns it to the failed run's size (say 10) under this spec; this is
  deterministic and matches the buried and delayed cases, whereas today's result
  depends on whether a RAC happened to run first.
- **Modified ready jobs are prepared again before resume.** Today the hold
  covered the gap between `Resume` and its RAC; without it a reserve could take
  a modified ready job in its old group.
- **Prefix scan fixed (pre-existing bug).** `recommendedReqGroupStat` scanned
  with the bare ReqGroup as prefix, so `"rg"` also read `"rgz"`'s stats. With
  the delimiter in the prefix, a ReqGroup's recommendation comes from its own
  stats only, which users see as changed learned reservations for such groups;
  per-ReqGroup invalidation would otherwise leave `"rg"` stale after a write to
  `"rgz"`.
- **An owner's report after a lost release is a failure.** The lost release
  recorded no real end state, so it bumped nothing; it did clear the bump bits,
  as any failure does. The owner's report is that same run's real failure, so it
  applies the bumps and re-prepares a ready item as a modify does, but keeps
  `FailBumpsCleared`: a bit set since the lost release came from the user's `wr
  mod` after the run ended. The window between the requirement change and
  `SetReserveGroup` is the same as after a modify.
- **Cache invalidation keys on a new stat key.** Stats are stored per distinct
  value, so a recommendation can change only when a Put creates a key.
  Invalidating after commit (`tx.OnCommit`) means a load can never record
  pre-commit values as current; `gen`/`loadedGen` covers an invalidation that
  lands during a load.
- **Rate limit 1s, negative for tests.** Today every ReqGroup with schedulable
  jobs is rescanned every RAC cycle (about every 500ms under churn), so 1s adds
  no load. Staleness is bounded, not zero: a RAC that skips a rate-limited load
  uses the cached values until the next RAC that loads, which is up to
  `CheckRunnerTime` later if nothing else triggers one. Likewise, a RAC that
  runs while the hook's background load of a never-loaded ReqGroup is in flight
  gets nil from `refreshReqGroupRecs`, so `prepareReadyJob` skips the learned
  step for that ReqGroup that cycle (a pending failure's recompute still runs,
  without learned values) and schedules at the unlearned size; the next RAC
  applies the loaded values. `withDefaults` turns 0 into the default for every
  timing, so tests ask for no limit with a negative value.
- **Cold misses.** The hook uses the last cached values (none on a cold miss)
  plus the bumps, and refreshes off the lock. Adds, kicks and resumes warm the
  cache first; delay expiry, dependency satisfaction and TTR rely on the RAC to
  catch up on learned values, while their bumps always apply.
- **No racmutex in the hook.** `waitThenRecheckRAC` holds `racmutex` while
  calling `q.Stats()`, so the hook reading `runnerCommand()` under the queue
  lock would deadlock; it reads an atomic copy instead (D4 test 2).
- **Limit-blocked first reserves wait, bounded.** Under the new skip rule a
  fresh runner for a count-0 group whose ready items are all limit-blocked is
  not skipped. It waits in `limiter.Increment` for up to its reserve timeout,
  then gets nothing and exits as an idle runner does today. The wait is bounded
  by the runner's own timeout, so no runner is parked beyond it (E2 test 5).
- **Kick's and resume's pre-encoded writes.** `kickJobs` and `resumeJob`
  (through `markResumed`) encode the job's write (`ahead`) before `KickWith` or
  `ResumeWith`, so that record holds the requirements from before the hook. That
  is harmless: the bump is already in the record written at fail time, and the
  hook re-applies learned values and bumps whenever the item becomes ready
  again, including after recovery.
- **Pause and drain need no new rule.** `handleReserve` returns nothing while
  draining before it reaches `skipReserve`, and the hook still runs while
  paused, so items are prepared by `Resume`.
- **Testing.** GoConvey, following go-implementor and go-reviewer. Red tests
  (B1.9, C1.1, C1.9, C2.1, C3.1, C3.5, D1.10, D2.1, E1.1 to E1.3, E1.5, E1.6,
  E2.1) must be shown failing on the base before the fix. The red runs on the
  base use only what the base has: they seed stats with `putJobStat` (the base
  has no `putStat`), wait with the base's hold check instead of `racIdle()`, and
  drop assertions on fields and timings that do not exist there
  (`FailBumpsCleared`, `FailedRunReqs`, `ReqStatRefreshInterval`), as E1's,
  C1.9's and C3.1's do. "Returns at once" means within a 1s window, as the probe
  uses for its 50ms reserves; tests that need the reserve to wait for an item
  use 5s timeouts.
