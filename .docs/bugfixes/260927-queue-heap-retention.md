# 260927: archived jobs stay in the manager's heap

Branch `fix-queue-heap-retention`, based on `origin/develop` at `cb9bb03`
(#626). The finding is number 1 of `.docs/bugfixes/260927-prodsim-findings.md`
on the `prodsim` branch (commits `299e48dc` and `44ade97e`).

- [x] Archived jobs stay reachable in the manager's heap for its whole
  lifetime. `subQueue.Pop` (queue/subqueue.go:441) shrinks its slice without
  clearing the popped slot, and `buryQueue.pop` (queue/bury_queue.go:52) does
  the same. Per-reserve-group ready slices are never deleted either, so the
  heap grows with every new group, and wrstat makes a new group on every run.
  Evidence: after a 40k-job burst finished, 524.7MB of 21KB Cmd strings were
  still in a forced-GC heap, and the heap grew by about 24MB per 1,000 jobs
  when each round had its own limit group.
  - Red, queue level:
    `CGO_ENABLED=1 go test -tags netgo --count 1 ./queue/ -run 'TestRemovedItemsAreCollectable|TestEmptiedReadyGroupsAreDropped|TestSliceQueuePopsLeaveItemsCollectable'`
    exited 1. Every path that goes through a heap pop kept all 200 items'
    data reachable (`Expected: 200 / Actual: 0`), which covers ready remove,
    reserve then remove, a group that keeps a live item, delay remove, release
    to delay, bury, kick, suspend, and a reserve group change. The bury,
    dependency and suspended slice pops did the same, and 50 used-up reserve
    groups were all still held (`Expected: 0 / Actual: 50`). A dependent item
    that is removed was already collectable, and stays as a guard.
  - Red, jobqueue level:
    `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue/ -run TestServerArchivedJobsLeaveTheHeap -v`
    exited 1. It runs an in-process server through 6 rounds, each with its
    own req group, rep group and datetime limit group, so each round has a
    new reserve group. Each round adds 100 jobs with 40KB commands, then
    reserves, starts and archives them all. Before the fix, HeapInuse after
    each round was
    `[9248768 14475264 19783680 24961024 30171136 35250176]` (+24MB, bound
    8MB). With only the queue fixed, the heap stayed flat but the server
    still kept a key list for all 6 rep groups (`Expected: 0 / Actual: 6`).
  - Fix:
    - `subQueue.Pop`, `buryQueue.pop` and `depQueue.pop` now nil the vacated
      slot.
    - `subQueue.Pop` deletes a ready group from `groupedItems` when its last
      item leaves. That is the last step of every heap pop and remove, under
      the sub-queue's lock, and the queue's own map readers already hold the
      queue lock that every mutation takes. `popItemList` and `Push` already
      treated a missing group as empty, so a later push re-creates the group,
      and a `Reserve` that waits on a dropped group still gets the next item
      pushed to it. Both are tested.
    - `rgToKeys.Delete` (jobqueue/server.go) forgets a RepGroup once its last
      key goes. Every caller holds the lock, `Add` re-creates a missing set,
      and `Values` returns nil for a missing RepGroup, as it did for one never
      seen.
  - After: all the queue tests pass, and the jobqueue test reads
    `[4153344 4218880 4218880 4308992 4300800 4358144]` (growth 0MB) with no
    rep groups left.
  - Audit of other removal paths:
    - queue/: `buryQueue.remove` and `depQueue.remove` already cleared the
      slot. `dependants` and `remainingDeps` delete emptied entries. The
      `pushNotificationChannels` map deletes an emptied group. Nothing else
      removes by reslicing.
    - jobqueue/: `previouslyScheduledGroups` is pruned on every
      reserve-and-schedule pass. The limiter forgets a count group when it
      reaches 0.
    - Not fixed, recorded for follow-up:
      - The limiter never forgets a time or datetime group (limiter/group.go
        `decrement` returns false for them), about 150B per group. Each
        wrstat run's datetime group therefore stays. Forgetting one would
        cost a database lookup on its next use.
      - The limiter keeps a group's `toNotify` channels until a decrement, so
        a group held at its limit gains one per timed-out reserve.
      - LSF `reservedElements` is pruned only at shutdown (`pruneReserved`,
        reached only from `busy()`). `doomedElements` keeps a finished group's
        doomed ids because its prefix is never scanned again. Both are about
        100B per runner, and pruning them needs care so that
        `killExcessCmds` does not kill a reserved runner.
      - OpenStack `spawnCanceller` can keep an empty inner map per cmd.
      - `db.applyFolded`'s swap-remove leaves the vacated slot set, but that
        slice is local and short-lived.
  - Gates: `make lint` 0 issues; `make test` 723 passed, 20 skipped;
    `CGO_ENABLED=1 make race` 722 passed, 20 skipped. No known flakes fired.
  - End to end: the prodsim branch's `developers/wrdev.sh retention-check 6
    1000 20`, run against a local-scheduler manager built from this branch,
    printed `firstMB=5 lastMB=5 growthMB=0 bound=32` and `PASS`. Before the
    fix, the findings record `retention-check 4 1000 20` growing from 29MB to
    102MB and FAILing.
- [x] Review finding, pre-existing on develop: `subQueue.update`
  (queue/subqueue.go) called `heap.Fix` on a ready item whose ReserveGroup had
  not changed without first setting `q.reserveGroup`, so the heap methods
  worked on whichever group was last pushed to or popped from. A priority
  change to a ready job (`wr mod -p`, or any `Queue.Update` that keeps the
  group) then left the job misplaced, could reorder and re-index items of the
  other group, and panicked with an index out of range when the other group
  was smaller. Nothing in the server recovers that panic.
  - Red:
    `CGO_ENABLED=1 go test -tags netgo --count 1 -timeout 30s ./queue/ -run TestQueuePriorityUpdateInAnotherReserveGroup`
    panicked in `subQueue.Less` (index out of range) and timed out.
  - Fix: `update` sets `q.reserveGroup` to the item's group before
    `heap.Fix` in the ready sub-queue.
  - After: the test passes, as do the whole queue package and it under
    `-race`.
  - Gates after both fixes: `make lint` 0 issues; `make test` 724 passed, 20
    skipped; `CGO_ENABLED=1 make race` 724 passed, 19 skipped. The jobqueue
    heap test passed 5 times under `-race` with a growth under 0.2MB each
    time.
