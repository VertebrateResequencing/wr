# racPending: what the shared bool costs, and options (2026-10-08)

Delivery queue item 4. Investigation only: no production code was changed.
Base: `origin/develop` at `f89d28d5`. Sources: the deferred note in
`.docs/bugfixes/261006-kick-reservable-race-8213f1ec.md` (third item) and
PR #683's "Deferred" section.

## 1. What racPending is for

`waitForPendingReserves` (`jobqueue/serverCLI.go`) runs at the top of every
`handleReserve`. While `racPending || racRunning`, the reserve parks on a
channel in `waitingReserves` until the hold is released. The hold exists so a
reserve does not act before the ready-added callback (RAC) has processed newly
ready items. The RAC does three things a reserve can see:

- `prepareReadyJob` recomputes requirements (learned RAM, disk and time from
  `recommendedReqForGroup`, and the post-failure bump for `FailReasonRAM`,
  `FailReasonDisk` and `FailReasonTime`), then moves the item to its new reserve
  group with `q.SetReserveGroup`.
- It updates `previouslyScheduledGroups` counts, which `skipReserve` reads for a
  runner's first reserve.
- It asks the scheduler for runners.

**Who sets it:** `setRACPending` runs before a mutation that will queue
`readyAdded`:

- `enqueueItems` (add)
- `updateJobDependencies`
- `updateLiveRerunItemWithReadyPending`
- `resumeQueueItem`
- `kickJobs`
- `triggerReadyAddedCallback`, used by suspend, the RAC check timer, recovery
  and completions

**Who clears it:**

- `clearRACPending` when an operation turns out not to queue a callback: an
  add of nothing, an add error, a resume to Dependent, a failed resume, a failed
  kick, or a dependency or live-rerun update error.
- `finishRAC`, at the end of every RAC, which also clears `racRunning`.
- Shutdown.

Every clear also closes all waiters. Releases, delay expiries, TTR expiries and
queue-internal dependency resolution queue `readyAdded` without taking a hold.

## 2. What goes wrong

The probe shows two ways a hold is dropped before the RAC that covers the
operation has started.

- **P1, the case the deferred note describes.** Operation A sets the hold and
  queues `readyAdded`. Before the RAC goroutine sets `racRunning`, operation B
  sets and then clears the bool, for example with a resume to Dependent. B's
  clear releases all waiters and drops A's hold.
- **P2, the larger case.** It follows from "`finishRAC` resets everything"
  in the same note. If A arrives while a RAC is already running, the queue
  merges A's `readyAdded` into a recall. The running RAC's item snapshot was
  taken before A. Its `finishRAC` drops A's hold, and the recall that covers A
  only runs after `recallBreak` (500ms). For that half second, and until the
  recall sets `racRunning`, reserves run against items no RAC has processed.

### Consequences for users

A reserve that lands in the gap behaves as if the hold did not exist. A runner
that is already waiting inside `q.Reserve` behaves that way even with the hold,
because it is past `waitForPendingReserves`. The observable effects are:

1. **Stale-size run.** The item still has its add-time or earlier scheduler
   group. A runner of that group can take it before the RAC raises its
   requirements. `resetJobForReservation` then blanks `FailReason`, so the
   bump for that attempt is lost completely. The worst concrete case is a job
   buried for RAM and then kicked. The kick resets `UntilBuried` to
   `Retries+1`, so a job with `Retries: 0` can rerun at the old RAM, fail for
   RAM again and be buried again. The user sees that kicking it did not help.
   For other jobs the cost is one wasted attempt, after which the normal retry
   path bumps the requirements.
2. **A fresh runner exits idle.** On a runner's first reserve, `skipReserve`
   reads `previouslyScheduledGroups`. If the RAC that would raise that group's
   count has not run, the runner is told nothing is ready and exits. The next
   RAC submits another runner, which costs scheduler dispatch latency and LSF
   churn.
3. **Nothing worse.** No job is lost, run twice or stuck. An early release only
   lets a reserve proceed sooner. Items stay in the queue, and a reserve goes
   through the normal reservation path. A group change made by the RAC
   afterwards wakes waiters, because `subQueue.update` calls `triggerNotify`.
   The opposite failure, a hold that is never released, would stall every
   reserve. Any fix has to guard against that.

### Likelihood at production scale

- **P1 is rare.** It needs a hold-taking operation that queues nothing to
  coincide with another operation's window. That window runs from
  `setRACPending`, through the `AddMany`, `KickWith` or `ResumeWith`, to the
  RAC goroutine's start. The triggers are an add of nothing (rare, because
  `jobsNotAlreadyQueued` filters duplicates first), a resume to Dependent, a
  concurrent kick that loses, or an error path.
- **P2 is the common case.** Under sustained churn, RACs run nearly back to
  back. An add, kick or resume that arrives during a RAC loses its hold at that
  RAC's end.
- **Even P2 matters only if something reserves in the gap.** The effect shows
  only when that reserve's outcome would differ: a pending requirement change,
  or a first reserve for a group whose count is zero. Runners already waiting
  inside `q.Reserve` bypass the hold anyway. The same stale-size effect also
  happens without any hold for releases and delay expiries, which never take
  one.

In short, the hold is a best-effort ordering hint, not an invariant. This is
consistent with "ordering only" in the checklist. The note understates the
effect slightly: the requirement bump can be lost, which can cost an attempt or
re-bury a kicked job with no retries left.

### Adjacent pre-existing gap (unverified, separate item if wanted)

A job released for RAM goes through delay and then becomes ready with
`readyAdded("delayed")`, which takes no hold. A runner of the old group that is
waiting in `q.Reserve` can take it before the RAC applies the RAM bump, and
`resetJobForReservation` blanks `FailReason`. If that happens, the retry runs at
the old size, and the bump arrives only after another failure. This was not
reproduced. It is outside racPending's scope, since no hold is involved, and is
probably more frequent than P1. If the owner wants it pursued, it needs its own
red test.

## 3. Red probe

`/nfs/hgi/wr/sb10-bigdb/runstate-gate/probes/racpending_probe_test.go` belongs
in package `jobqueue` and is a probe only, not a proposed test. The setup
reserves a parent and leaves it incomplete. It suspends a child that depends on
the parent, waits until RAC activity is idle, and puts a gate in front of the
real `readyAddedCallback`. Each leaf then has a second client call
`Reserve(50ms)` and asserts that the call does not return within 1s while the
covering RAC is gated.

- **Control:** add a job with the RAC gated. The reserve waits. Passes.
- **P1:** add a job with the RAC gated, then `jq.Resume` the child, which goes
  back to Dependent and calls `clearRACPending`. The reserve returns the new
  job at once. Fails.
- **P2:** trigger a RAC and hold it at the gate, add a job (merged into a
  recall), let the first RAC finish, and gate the recall. The reserve returns
  the new job at once. Fails.

Command, run from a worktree with the probe in `jobqueue/`:

```bash
nice -n 19 go test ./jobqueue -count=1 -run 'TestRACPendingProbe$' -v
```

Result: exit 1 in 3 of 3 runs, plus once under `-race` with
`CGO_ENABLED=1` and no race report. Output:

```text
    control: a reserve waits for the callback a new add queued ✔✔✔
    P1: a resume to dependent does not release the add's hold ✔✔✔✔✘
    P2: an add during a running callback keeps its hold until the recall runs ✔✔✘
  Line 238: Expected 'eccbfd53cc70f648f94dc13c1f9288cd' to be blank (but it wasn't)!
  Line 262: Expected 'eccbfd53cc70f648f94dc13c1f9288cd' to be blank (but it wasn't)!
--- FAIL: TestRACPendingProbe (2.09s)
```

The returned key is the newly added job's. The probe asserts ordering, which is
the documented contract of `waitForPendingReserves`. It does not drive the
stale-size or idle-runner effects end to end, because those need a runner
command and learned stats.

## 4. Options

### A. Accept and document (no behaviour change)

Rewrite the comment on `waitForPendingReserves` to say the hold is best-effort,
and record P1, P2 and their effects. Optionally, file the adjacent RAM-bump gap
as its own item.

- Complexity: none. Risk: none. Hot-path cost: none.
- Test: none owed, since behaviour does not change.

### B. Counted holds with an epoch, keeping finishRAC as the release valve (fixes P1 only)

Replace the bool with `racHolds int` and `racEpoch uint64`:

- `setRACPending` increments `racHolds` and returns the current epoch.
- `clearRACPending(epoch)` decrements only if the epoch has not changed, and
  closes waiters only when `racHolds == 0 && !racRunning`.
- `finishRAC` zeroes `racHolds`, increments the epoch and releases waiters, as
  it does today.

The epoch is what makes a counter safe despite the merging. Without it, A's
increment is zeroed by an intervening `finishRAC`, and a later decrement from A
would then remove B's hold. With the epoch, a decrement can only cancel a hold
taken in the same RAC epoch, which is the hold the operation took.

- Complexity: about 30 lines in `jobqueue/server.go`, with six call sites
  passing the token back. No change to the queue package.
- Risk: low. A leaked increment is cleared by the next `finishRAC`, as a leaked
  bool is today, so this adds no new way to stall. P2 is unchanged.
- Hot-path cost: none. `waitForPendingReserves` reads an int instead of a
  bool under the same mutex.
- Test: the probe's P1 leaf becomes the regression test, alongside the control.
  The owed speed check is `make speed` plus the kick and resume benchmark from
  #683.
- Value: small, because P1 is the rare case and P2 dominates.

### C. Hold until the covering RAC has run (fixes P1 and P2)

B's counter alone cannot fix P2, because the queue's merge hides which RAC
covers which operation. C replaces the server-side bool with two parts:

1. A queue-owned "readyAdded requested and not yet finished" state.
   `readyAddedCbRunning` already stays true continuously through the recall
   break. The queue would expose it, plus an idle notification, so waiters can
   be woken after the queue clears the flag rather than at `finishRAC`. If the
   wake happened at `finishRAC`, the flag would still be set and woken waiters
   would block again with nothing left to wake them.
2. A server in-flight counter around each mutation, incremented before and
   decremented with `defer` after, so nothing ever has to clear another
   operation's hold.

Alternatively, add a pre-snapshot hook in `runReadyAddedCb` so the server can
record a watermark of the holds each RAC covers.

- Complexity: medium. It needs a new queue API, a new idle notification and
  changes in every holder.
- Risk: **high.** Under sustained churn, RACs plus 500ms recall breaks run back
  to back. Today reserves get through during the breaks. Under C they would be
  held almost continuously, which risks reserve starvation and a throughput
  regression at production concurrency. It would need a fairness valve, such as
  a bounded wait, which brings the P2 gap back in a weaker form. A missed idle
  notification would stall all reserves.
- Hot-path cost: one more lock read per reserve, plus much longer waits under
  load.
- Test: P1 and P2 leaves, a starvation test (reserves still complete under
  continuous adds), a production-scale soak, and `make speed`.

### D. Remove the global hold and make a ready item correct when it becomes ready

Recompute requirements and the reserve group for an item as it becomes ready,
under the queue lock:

- kick and resume, using the `KickWith` and `ResumeWith` callbacks they
  already have;
- add, using learned recommendations;
- the delay and release path.

Make `skipReserve` consult the ready count for the group instead of the counts
the RAC sets. The RAC then only schedules runners, and reserves never wait on
it. This also closes the adjacent RAM-bump gap.

- Complexity: high, at spec level. Requirement learning moves out of the RAC,
  which affects the rac-scan-bound work from reliable4, and skip semantics
  change.
- Risk: medium to high. It changes scheduling and resource behaviour and needs
  a spec.
- Hot-path cost: reserves lose the global serialisation point, which is
  probably a gain. Add, kick and resume pay the recompute, cached per
  `ReqGroup`.
- Test: behaviour tests asserting that a kicked RAM-failed job is reserved at
  the bumped size, that a fresh runner's first reserve after an add gets work,
  and that the probe's reserves no longer need to wait. Also a soak and
  `make speed`.

## 5. Recommendation

**Keep this out of the final bolt release. For the release, take option A, or
nothing at all.** It is not release-critical:

- It never loses work, runs a job twice or stalls the queue.
- The common gap (P2) has existed since 2020 and is effectively the throughput
  valve that lets reserves through between RACs.
- Fixing it properly (C or D) means changes to the reserve hot path and to
  scheduling, which would then need a production-scale soak.
- B is safe and cheap, but only removes the rare case.

After the release, if the owner wants it addressed, D is the option worth
specifying. It removes the cause, the RAC as a precondition for correct
reservations, rather than tightening a lock around it, and it also covers the
adjacent RAM-bump gap. If a small step is wanted first, B can go in as a
standalone bugfix with the probe's P1 leaf as its red test. C is not
recommended, because of the starvation risk.

The probe is kept in `probes/`. The scratch worktree was removed.
