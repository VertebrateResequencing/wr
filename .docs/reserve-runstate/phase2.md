# Phase 2: Record and cleanup

Ref: [spec.md](spec.md) sections A1, A4, A5

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

Reserve and start still write full records in this phase, so behaviour is
unchanged. The items run in sequence: A4 and A5 both change
`jobqueue/db.go`'s live-record writes, and both build run-state records
with item 2.1's functions.

## Items

### Item 2.1: A1 - Record encoding and overlay

spec.md section: A1

In new `jobqueue/db_runstate.go`, add `jobRunState`, `newJobRunState`,
`(*jobRunState).applyTo`, `runStateRecord` and `runStateOver`, as given
under "Run-state record" in spec.md's Architecture (CRC-32C Castagnoli
prefix, binc body, deep copies of `Requirements` and `RequirementsOrig`
including their `Other` maps). Covering all 6 acceptance tests from A1, in
`jobqueue/db_runstate_test.go`. Depends on phase 1's `bucketJobRunState`.

Review note: the spec's "older record" lists a differing value for every
`jobRunState` field except `RerunAfterRun`. Set the older record's
`RerunAfterRun` to true (the example job's is false), so test 2 really
covers every field.

- [ ] implemented
- [ ] reviewed

### Item 2.2: A4 - Full writes and deletes supersede the run-state record

spec.md section: A4

Add `putLiveRecord` and `deleteLiveRecord` (spec.md "Live-record helpers")
to `jobqueue/db_runstate.go`, both treating a missing `bucketJobRunState`
as empty, and route every non-test `Put` or `Delete` of a `bucketJobsLive`
key through them: `beBatch` full changes and exit ops, `archiveJobTx`
(`recordCompleteTx`, `keepLiveForRerunTx`), `putRunningRerunMark`,
`storeLiveForRerun`, `putBackArchivedDependentsTx`, `putNewLiveJobs`,
`modifyLiveJobsTx` (`deleteOldLiveJobs` and the new keys' put) and
`deleteLiveJobs`, including indirect writes through
`putEncodedJobs(tx, bucketJobsLive, ...)`. Use
`grep -n 'bucketJobsLive' jobqueue/*.go | grep -v _test.go` to find every
site; it shows where the bucket is fetched, not the `Put` and `Delete`
lines. Tests on bare databases (`reliable4ACAddOpenBareDB`, used by
`TestReliable4AddFinalDrainLoops` and `reliable4_add_foldcap_test.go`) must
not panic. Covering all 7 acceptance tests from A4, in
`jobqueue/db_runstate_test.go`. Depends on item 2.1.

- [ ] implemented
- [ ] reviewed

### Item 2.3: A5 - The add path keeps a handed-out job's records

spec.md section: A5

Change `putNewLiveJobs` (`jobqueue/db.go`) to treat an existing live record
as handed out if a matching run-state record exists (checked with
`runStateOver`, a missing bucket counting as empty) or
`liveRecordHandedOut` says so; otherwise write with `putLiveRecord`.
Covering all 3 acceptance tests from A5, in `jobqueue/db_runstate_test.go`.
Depends on items 2.1 and 2.2.

Review note, a narrow race the spec leaves open: two concurrent adds of the
same new job, where the second add's fresh copy is put after an undrained
reservation's run state was queued. The run-state record then lands over
the second add's live record, so the overlay sits on that copy's non-run
fields until the job's next full write. This is no wider than today's
stale full change. Do not change the A5 behaviour for it. The run-state
drain (A3) and recovery overlay (B1) arrive in phase 3, so a test here
stands in for the drain by putting `runStateRecord` over the second add's
live record, and reads it back with `runStateOver` and `applyTo`. Cover the
race with such a deterministic test pinning the outcome (run state is the
reservation's, non-run fields the second add's) if one can be built without
sleeps; otherwise document the window in a comment on `putNewLiveJobs`.
Either way, describe it in the PR body.

- [ ] implemented
- [ ] reviewed
