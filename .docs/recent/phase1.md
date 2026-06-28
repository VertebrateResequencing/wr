# Phase 1: End-time index (jobqueue/db.go)

Ref: [spec.md](spec.md) sections A1, A2, A3

## Dependencies

None. This is the foundation phase; Phases 2 and 4 build on it.

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

The items in this phase are sequential: A2 retrieval reads the index
written by A1, and A3 exercises durability of both. Implement and
review them in order.

After A1 and A2 are in place, run
`timeout 600 make bench BENCH=BenchmarkArchiveJobs` to confirm the
D1.1/D1.2 archive bar early (no extra `bolt_writes/job`; `bolt_pages/job`
and `ns/op` within noise). This is a sanity check here; the full
benchmark sign-off is Phase 5.

## Items

### Item 1.1: A1 - Index written on archive, no extra commit

spec.md section: A1

Add bucket `bucketEndTimeToKey` (plus the `endTimeBytes = 8` const) and
create it in `initDB` with `CreateBucketIfNotExists`. Implement
`endTimeIndexKey(endNanos, jobKey)` (8-byte big-endian UnixNano +
`dbDelimiter` + jobKey) and `(db *db) updateEndTimeIndex(tx, jobKey, job)`
(latest-per-key, idempotent on unchanged end time; recovers the prior end
time from the job's existing `bucketJobsComplete` record to drop the stale
forward entry - no second index bucket), and wire `updateEndTimeIndex` into
`archiveJobTx` inside the existing `bolt.Batch` transaction, before the
complete-record Put (no extra commit). File: `jobqueue/db.go`. Covering all 4
acceptance tests from A1.

- [x] implemented
- [x] reviewed

### Item 1.2: A2 - Windowed retrieval from the index

spec.md section: A2

Implement `retrieveCompleteJobsRecent(cutoff time.Time) ([]*Job, error)`
on `*db`: a `View` that seeks `bucketEndTimeToKey` at `cutoff.UnixNano()`
(8 big-endian bytes), scans forward to the end of the bucket, parses each
trailing jobKey via `lookupEntryJobKey`, decodes via `decodeArchivedJob`
(skipping absent or currently-live keys), and returns the non-nil jobs in
ascending end-time order. An absent/empty index yields an empty slice and
no error. File: `jobqueue/db.go`. Covering all 3 acceptance tests from
A2. Depends on Item 1.1.

- [x] implemented
- [x] reviewed

### Item 1.3: A3 - Durability and consistency

spec.md section: A3

Add tests confirming the index survives a db close/reopen from the same
file and contains no entry pointing at a never-archived or superseded
key: an archived job is still retrieved after reopen (durability, no
back-fill); a key archived at T1 then re-archived at T2 returns once with
its T2 end time after reopen (no duplicate, no stale T1 entry). Test
file: `jobqueue/db_test.go`. Covering all 2 acceptance tests from A3.
Depends on Items 1.1 and 1.2.

- [x] implemented
- [x] reviewed
