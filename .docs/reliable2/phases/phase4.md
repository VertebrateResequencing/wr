# Phase 4: DB compatibility, recovery checks, and putJobStats guard (F, H2, G)

Ref: [spec.md](../spec.md) sections F1, H2, G1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

TDD throughout; the compat/recovery tests run under `-race`. This phase proves
the reworked build opens a current-code-upgraded DB without error or data loss
(F1), confirms the retained recovery window still returns `ErrRecovering` and
recovers incomplete jobs (H2), and lands the one orthogonal fix - the
`putJobStats` corrupt-duration guard (G1). Depends on Phases 1-3 being in place
(the fixture is opened with the fully reworked build).

F1 (DB-compat fixture + generator + README + open tests) and G1 (the
`putJobStats` guard) are independent - F1 is `testdata` + a new test file with a
`//go:build ignore` generator, G1 edits `putJobStats` in `db.go` and adds to
`db_test.go` - so they form a parallel batch. H2 is sequential after F1 because
it adds tests to the same `reliable2_dbcompat_test.go` file and uses F1's
fixture.

## Items

### Batch 1 (parallel)

#### Item 4.1: F1 - Open a current-upgraded DB [parallel with 4.2]

spec.md section: F1

Realise with a SMALL committed binary fixture DB produced once by current
(`reliable2`) code:

- Fixture `jobqueue/testdata/dbcompat/db.golden`; generator
  `jobqueue/testdata/dbcompat/gen.go` (a `//go:build ignore` program); procedure
  documented verbatim in `jobqueue/testdata/README.md`.
- The fixture must contain the now-dead buckets (`repGroupCompleteCount`,
  `repGroupCompleteBackfilled`), the retained buckets, ~4 jobs across two rep
  groups with at least two carrying non-empty `WaitingForDepGroups` and
  `LimitGroupsForDisplay`, two reserved+started+archived (so `jobscomplete`,
  `endTimeToKey`, `repgroupEndTime`, and `repGroupCompleteCount` populate and
  the backfill sentinel is written), and the rest incomplete in `jobslive`.
  Close cleanly.
- Regeneration (README steps 1-3): check out a pre-removal `reliable2` commit
  that STILL maintains the counters (so the fixture contains the dead buckets),
  build/run `gen.go` via the `jobqueue` DB open path, copy the bolt file to
  `db.golden`, and `git add` it (binary; keep it small).

Tests in the new file `jobqueue/reliable2_dbcompat_test.go`. The test copies the
fixture into `t.TempDir()` (BoltDB needs exclusive read-write open), then opens
it with the reworked `serve`. Covers all 4 F1 acceptance tests: (1) opens with
no error and no crash (no panic on the dead buckets, no decode error on the two
new `Job` fields `WaitingForDepGroups`/`LimitGroupsForDisplay`); (2) the
complete rep group queried with `includeComplete=true` returns the known
complete jobs as `JobStateComplete` with the expected count; (3) the known
incomplete jobs are recovered and become reservable/runnable; (4) `bucketRTK`
and `bucketJobLookupEntries` key counts are unchanged before vs after open (the
one-time index rebuilds do NOT re-run).

- [ ] implemented
- [ ] reviewed

#### Item 4.2: G1 - Do not store a corrupt duration stat [parallel with 4.1]

spec.md section: G1

Change `putJobStats` (db.go:2017): guard the runtime stat - do NOT store a
`bucketJobSecs` value when `job.EndTime.IsZero()` OR when the computed duration
(`job.EndTime.Sub(job.StartTime)`) is `<= 0`. Still store RAM/disk stats as
today. No one-time repair of existing entries (guard only).

Tests in `jobqueue/db_test.go`. Covers all 3 G1 acceptance tests (zero `EndTime`
with a valid `ReqGroup` -> no `bucketJobSecs` entry for that `ReqGroup`,
RAM/disk entries still stored; `EndTime` before `StartTime` (non-positive
duration) -> no `bucketJobSecs` entry; valid positive duration -> a
`bucketJobSecs` entry equal to `ceil(seconds)` is stored, happy path
unaffected).

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill (review all items in the
batch together in a single review pass).

### Item 4.3: H2 - Recovery window retained (after Item 4.1)

spec.md section: H2

KEEP `recoverInBackground` (server.go:1197), `isRecovering`/`finishRecovering`/
`setRecovering` (server.go:874, 929, 894), the `ErrRecovering` returned by
`getij` (serverCLI.go:1702), `rescheduleReadyAfterRecovery` (server.go:1253),
and the `!s.isRecovering()` scheduling gate. Only the `seedStatusState`/
counter-backfill work was removed from startup (Phase 2), not this recovery
window.

Add to `jobqueue/reliable2_dbcompat_test.go` (depends on Item 4.1's fixture).
Covers both H2 acceptance tests: (1) a job whose key is not yet restored during
the recovery window -> a reconnecting runner's `j*` method gets `ErrRecovering`
(retryable), not `ErrBadJob`; (2) prior incomplete jobs in a DB are recovered
and become reservable after recovery finishes (serves acceptance #5's
"incomplete jobs recover and run").

- [ ] implemented
- [ ] reviewed

## Regression guards (KEEP surfaces, section H)

Re-run after this phase; all must stay green (spec.md section H1, plus
`TestLostDetectionSilentRunner` per section B):

- `jobqueue/lost_detection_test.go`: `TestLostDetectionSilentRunner` (KEEP).
- `jobqueue/subscription_test.go` (`#503`), `jobqueue/live_jtouch_test.go`
  (`#530`/`#534`, incl. ssh-to-host), the `JobUpdateResync` reconnect/resync
  tests, `jobqueue/suspend_resume_test.go` + `wr status --suspended`,
  `jobqueue/modify_validation_test.go`, `jobqueue/serverWebI_test.go`, and the
  `wr add --sync` client test.
- `make test`, `make race`, `make lint` all clean.
