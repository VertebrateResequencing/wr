# Phase 2: cache

Ref: [spec.md](spec.md) sections B1, B2, B3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

This phase is independent of phase 1 and may run in parallel with it;
both must be reviewed before phase 3 starts. The items run in sequence:
B2 adds the rate limit and single flight to B1's cache, and B3 switches
the RAC to it. `jobqueueTestInit` gets `ReqStatRefreshInterval = -1`
(B2 test 8) in this phase, and all existing tests pass at its end.

## Items

### Item 2.1: B1 - Cache, invalidation on a new stat key

spec.md section: B1

In new `jobqueue/reqstat_cache.go`, add `reqStatCache`, `reqStatEntry`,
`cachedReqGroupRecs`, `refreshReqGroupRecs`, `invalidateReqGroupRecs`,
`warmReqGroupRecs`, `putStat` (replacing `putJobStat`, invalidating through
`tx.OnCommit` only when the Put creates a key) and the `reqStatLoadHook`
seam, as given under "Learned-requirement cache" in spec.md's
Architecture. Make `putJobStats` and `jobExitData.updateFailStat` take the
db's cache and call `putStat`, and switch the test callers named there
(`queues_avoid_scheduling_test.go`, `db_test.go`). Call
`scanReqGroupStat` with the prefix `reqGroup + dbDelimiter`. Add the test
helper `seedReqGroupStats`.
Covering all 10 acceptance tests from B1, in new
`jobqueue/reqstat_cache_test.go`. Test 9 is red: show it failing on the
base (seeding with `putJobStat`) before the fix. Test 4 polls
`!racPending && !racRunning` under `rpmutex`, so this phase stays
independent of phase 1; phase 3 switches it to `racIdle()`.

- [x] implemented
- [x] reviewed

### Item 2.2: B2 - Refresh rate limit and single flight

spec.md section: B2

Add `ServerTimings.ReqStatRefreshInterval` (`withDefaults` maps 0 to 1s
and a negative value to 0), the cache's `interval`, single-flight loading
that never holds `mu` across a load, `refreshReqGroupRecsLater` tracked by
the cache's `wg`, and the `closed` flag that `db.close` sets before
waiting for `wg`. Set `Timings.ReqStatRefreshInterval = -1` in
`jobqueueTestInit`, and list in the phase's checklist each test that
builds its own `ServerConfig` and writes or depends on learned stats, with
the result of checking it. Covering all 8 acceptance tests from B2, in
`jobqueue/reqstat_cache_test.go`. Depends on item 2.1.

Tests that build their own `ServerConfig`, not through `jobqueueTestInit`,
checked for learned stats (B2 test 8):

- `TestREST` (`jobqueue/rest_test.go`): its `PeakRAM` 321 is a live touch
  shown by the REST status, not a learned value. Not affected.
- `TestJobqueueWithOpenStack` (`jobqueue/jobqueue_test.go`): its jobs use
  `Override: 2` or are the first of their ReqGroup, and it asserts no
  learned value. Not affected; it runs only with OpenStack credentials.
- `TestJobqueueWithMounts` (`jobqueue/jobqueue_test.go`): archives jobs of
  ReqGroup `"cat"`, but asserts no requirement. Not affected.
- `TestJobqueueSpeed` (`jobqueue/jobqueue_test.go`): always skipped.
- `cmd/status_test.go` (`startStatusTestServer`):
  `TestStatusDetailsPreservesHighMemoryFailureNote` writes a high-peak RAM
  stat on release, but asserts only the status text. Not affected.
- `cmd/runner_clean_restart_test.go`, `cmd/manager_stop_shutdown_test.go`
  and `developers/soak/dbstart/main_test.go`: no assertion on learned
  values. Not affected.
- `client/testing.PrepareWrConfig` (used by `client/client_test.go`,
  `client/submit_resend_test.go`, `client/testing/server_test.go` and
  `developers/prodsim/sim_test.go`): their requirement assertions are on
  jobs built client-side, before any server learning. Not affected.
- Tests that open a db with `initDB` and no `Serve` (such as `db_test.go`
  and `queues_avoid_scheduling_test.go`) get a cache interval of 0, so no
  limit. Not affected.

- [x] implemented
- [x] reviewed

### Item 2.3: B3 - The RAC reads the cache

spec.md section: B3

Make `recommendedReqForGroup` (`jobqueue/server.go`) keep its per-cycle
map but fill it from `db.refreshReqGroupRecs`. Covering all 3 acceptance
tests from B3, in `jobqueue/reqstat_cache_test.go` (test 3 is the existing
`TestReliable4*` rac-bound tests, with `racScanWork` unchanged). Depends
on items 2.1 and 2.2.

- [x] implemented
- [x] reviewed
