# Remove the global reserve hold (racPending), option D

Delivery-queue row 10 (`.docs/reserve-runstate/delivery-queue.md`): remove the
global reserve hold (`racPending`), option D in
`.docs/nohold/racpending-options.md` (copied from
`runstate-gate/analysis/racpending-options.md`; read the whole doc: P1, P2,
the adjacent released-for-RAM gap, options A-D, the recommendation and the red
probe).

Owner decision 2026-10-08: option A (document the hold as best-effort, #688)
shipped for the bolt release; option D gets a spec after the release. v0.39.0
was tagged 2026-10-12.

The spec must include red tests from the racPending probe (P1 and P2) and for
the released-for-RAM gap (a kicked RAM-failed job is reserved at the bumped
RAM, not the old one).

Branch `nohold-2b5fd495`, worktree `../wr-nohold`, base `origin/develop` at
`7b445f43`. Background: `.docs/bugfixes/261006-kick-reservable-race-8213f1ec.md`
(deferred note near line 235) and `.docs/reserve-runstate/`.

## Notes

- Ready-time preparation runs through one hook for every way an item enters
  ready: new, kicked, resumed, delayed, released with no delay, dependency
  satisfied, requeued, updated and TTR. Preparation means recomputing the
  requirements and setting the reserve group under the queue lock before the
  item can be reserved.
- Ready-time preparation applies both learned (95th-percentile)
  recommendations and failure bumps. The stat lookup is cached per ReqGroup
  and invalidated when a new stat is written, so add, kick, resume and delay
  do not pay uncached bolt prefix scans on every item.
- A failure bump takes effect when the job fails, in the release or bury
  path, so `wr status` shows the size the retry will use while the job is
  delayed or buried.
- The high-peak RAM bump for a failure other than RAM, which today applies
  only to a delayed job (`job.State == JobStateDelayed`), also applies to a
  buried job, so kicking a buried job that exceeded its memory estimate
  reruns it with the bump.
- The ready-added callback keeps refreshing the requirements of schedulable
  ready jobs (it still runs `prepareReadyJob`), so a job that waits a long
  time picks up newer learning. Ready-time preparation is a floor, not a
  replacement. The reliable4 bounded rac scan is unchanged.
- `skipReserve` on a runner's first reserve skips only when its group count
  is 0 and no item in the runner's reserve group is ready. "Count > 0 means
  proceed" is unchanged.
- The `queue` package's exported API may change, including breaking changes
  to existing signatures.
- Findings to carry into the spec: `resetJobForReservation`
  (`jobqueue/serverCLI.go`) clears `FailReason` through `resetRunLocked`, so a
  bump not applied before reservation is lost; recovered ready jobs keep
  reserve group "" on purpose so a group-less `Reserve` can pop them; the
  queue's `changed` callback is asynchronous, so `job.State` lags the queue
  item's state; the callback skips `prepareReadyJob` for limit-blocked jobs,
  which is another stale-size path that ready-time preparation covers.
- Tests that encode the hold and must be reworked, not deleted silently:
  `TestResumeRacingReserveKeepsRACPending`, the `racPending` simulation in
  `reliable4_slow_request_test.go`, and the tests that poll `racRunning` as an
  idle signal (`depgranularity_add_test.go`, `reliable4_rac_bound_test.go`,
  `reliable4_reportstorm_test.go`, `reliable4_snapshot_memo_test.go`,
  `reliable4_repro_test.go`).
- The red probe (control, P1 and P2 leaves) is copied to
  `.docs/nohold/racpending_probe_test.go.txt` (from
  `runstate-gate/probes/racpending_probe_test.go`).
  Under option D its assertion inverts: a reserve may return at once, but the
  item it returns must already be prepared.
- Verification bar is DEVELOPERS.md section 5 for a reserve-path change:
  `make speed` and the kick/resume benchmark, the real-LSF scenarios
  `add-storm-lsf`, `report-storm-lsf` and `limit-drain`, and a `prodsim` soak
  at production concurrency.
- Ready-time preparation never does bolt I/O under the queue lock. On a stat
  cache miss it applies the ReqGroup's last cached learned values (or none if
  there are none) plus the failure bumps, which need no I/O, and refreshes the
  cache off the lock; the ready-added callback's refresh then applies newer
  values. Each ReqGroup's cache is refreshed at most once every N seconds, so
  a busy group does not rescan on every completion.
- Recovered ready items are prepared fully, like any other item entering
  ready: requirements recomputed and the real reserve group set when a runner
  command is set. The recovery special case that keeps reserve group "" goes.
- An explicit user `wr mod` of a resource requirement clears that resource's
  pending failure bump, so the user's size is used when the job is next ready.
- Findings to carry into the spec (round 2):
  - `releaseRun` moves the queue item (`q.Release` / `Requeue`) before
    `finalizeReleasedJob` sets `FailReason` and `PeakRAM`; with zero delay
    `q.Release` goes straight to ready. The end state and failure bump must be
    applied before the queue move.
  - New adds enter with reserve group "" today (`itemDefsForNewJobs` uses an
    unset `getSchedulerGroup()`); under D the hook sets the real group at
    enqueue when a runner command is set, and leaves it untouched when the
    runner command is "", mirroring `prepareReadyJob`, because
    `Queue.Reserve("")` pops only group-"" items.
  - The RAM, disk and time bumps are idempotent and raise-only, so applying
    them at fail time, at ready time and in the callback is safe.
  - The high-peak test is `job.State == JobStateDelayed`, but `kickChange`
    sets `State = Ready` inside the `KickWith` callback, so the buried
    extension must key on the failure, not the live state.
  - Every readyAdded source the hook covers, in `queue/queue.go`: resumed,
    dependent, requeued, kicked, new, updated, released, delayed and ttr;
    "triggered" and "recall" move no items.
  - Bumps persist across a restart (release write and the reserve run-state
    record both store requirements); a buried record from an older manager
    still gets its bump from ready-time preparation on kick.
  - Lock order is queue then job; the hook must keep it and must not call
    back into the queue.
  - `queue` is a public Go package; the breaking API change needs a CHANGELOG
    entry.
- Findings to carry into the spec (round 3):
  - Default N = 1s for the per-ReqGroup refresh limit, held in a server
    timing field tests can set to 0. Today every ReqGroup with schedulable
    jobs is rescanned every RAC cycle (about every 500ms under churn), so 1s
    adds no scan load and no extra staleness over today.
  - Stat keys are per distinct value (`putJobStat` key is reqGroup plus the
    zero-padded value), so the 95th percentile is over distinct values; keep
    that unchanged. A recommendation can only change when a write creates a
    new key, so invalidation can key on "this Put created a new key".
  - Stats are written on every completion (`putJobStats` on archive) as well
    as on failures.
  - Measured on a prod.db copy: 130 ReqGroups; worst prefix scan about 18k
    keys (jobSecs), a few milliseconds. A plain map cache needs no eviction.
  - Pause and drain need no new rule: `handleReserve` returns nothing while
    draining before the hold check. Under D, ready-time preparation still runs
    while paused, so items are already prepared at Resume.
  - Limit-blocked groups cannot park a fresh runner under the new
    `skipReserve` rule: `incrementReserveLimit` returns `ErrNothingReady` at
    once when the limit is reached.
  - A cold-cache miss keeps the stored requirements
    (`applyRecommendedJobRequirements` returns early on nil). Close the
    first-add window by warming the cache off the lock once per ReqGroup per
    call, before `AddMany`, `KickWith` and `ResumeWith`; queue-internal
    entries (delay expiry, dependency satisfied, TTR) rely on the RAC to catch
    up on learned values (their failure bumps are still applied).
  - Comments describing the hold must be rewritten or deleted with it
    (`racPending` block comment, `setRACPending`/`clearRACPending`/`finishRAC`,
    the `resumeQueueItem` doc, shutdown's clears, `waitForPendingReserves`).
    `racRunning` can stay as a RAC-idle signal for tests;
    `jobqueue/resume_order_test.go` also references the hold.

- Owner decision 2026-10-16 (item 6.3b): a learned requirement change must
  reach ready jobs that are already queued, including jobs blocked behind a
  full limit group, without the hold. Each job records the learned values it
  was last prepared with; the RAC's cheap pre-pass compares them with the
  cycle's values for its ReqGroup (O(1) per job) and recomputes and regroups
  a job whose values differ, in bulk, in chunks of 1,024 jobs with one
  queue-lock hold each. This amends
  D3.2 and B3.3 (the RAC scan-work bound) and the reliable4 rac-bound tests
  to match. The LSF gates (6.3) are rerun after the fix.
- Owner decision 2026-10-19 (item 6.3d): the extra `wr add` latency on NFS
  is not accepted as-is. Without the hold, archives complete faster, so the
  single bolt writer saturates more often and adds wait behind archive
  transactions. Develop a fix first: bounded alternation between the bolt
  writers so a pending add is not starved by archive and best-effort writes,
  with a cap so archives are not starved during an add storm. It is proven
  with report-storm, add-storm and add-storm-lsf evidence showing archive
  latency and throughput are not worse by more than 10%, then the gates are
  rerun.
- Owner decision 2026-10-19 (item 6.3e): the write-turns fix (a waiting add
  commits in the next bolt write transaction; archive and best-effort writes
  wait behind at most 3) is accepted with its remaining add p50 gap (67ms
  against the base's 16ms in-process on NFS; mean -15% against the base).
  The gap is writer occupancy from the extra archive throughput, which no
  write ordering can remove.
- Owner decision 2026-10-19 (item 6.3 record): two further costs of removing
  the hold are accepted. (1) Kid ordering: in add-storm-lsf the branch
  completes far fewer self-added kid jobs than the base (112 against 519),
  because ready jobs now reach their learned size and, within a priority, the
  queue serves older jobs first; the base finished more kids only because its
  fixture jobs were stuck at a stale size. (2) In-process archive latency on
  NFS: the archive median rises (299ms against the base's 20ms in the 6.3e
  probe) from writer occupancy; at LSF scale the archive writer's mean wait
  is +8% with +31% throughput.
