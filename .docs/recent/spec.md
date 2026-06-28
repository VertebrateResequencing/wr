# `wr status --recent` Specification

## Overview

Add a new job-selection mode to `wr status`: `--recent <duration>` returns every
job belonging to the requesting user that finished running (was archived to the
complete store) within the last `<duration>`, across all report groups. This
gives operators a single view of everything that ended recently without
enumerating report groups.

`--recent` is mutually exclusive with `-f`, `-i` and `-l`. It is a retrieval
mode only: it changes which jobs the manager returns, never how the CLI displays
them (same default `-o details`, same `--limit` grouping, same `-o` formats).
State filters are rejected under `--recent` (only exit-0 jobs are ever archived,
so the other states can never match). `--limit`, `--env` and the client-side
`--host` post-filter are honoured (STDOUT/STDERR follow the chosen `-o` output
format, as in the other modes; there is no `--std` flag).

Retrieval is backed by a new forward-only, time-ordered per-job end-time index
in BoltDB. It is written inside the existing archive transaction (no extra
commit), reflects the latest archive per job key (re-running a command replaces
its earlier index entry), is durable across restarts, and is not back-filled for
jobs archived before the feature shipped.

## Architecture

### Packages / files touched

- `jobqueue/db.go` - new end-time index bucket, written in `archiveJobTx`;
  helper `retrieveCompleteJobsRecent`.
- `jobqueue/server.go` - server helper `getJobsRecent`; reuse `limitJobs`.
- `jobqueue/serverCLI.go` - request-method constant + handler `handleGetRecent`;
  dispatch case.
- `jobqueue/client.go` - `clientRequest.Period` field; `Client.GetRecent`.
- `cmd/status.go` - `--recent` flag, mutual-exclusion count, duration parsing,
  state-filter rejection, call into `GetRecent`, long-help update.
- `cmd/duration.go` (new) - `parseRecentDuration` supporting `d`/`w` units.

### Data model: the end-time index

Job keys are 32-char lowercase hex (FarmHash128, see `byteKey`); they never
contain the `dbDelimiter` (`_::_`) or any byte that breaks prefix/range scans,
so they compose cleanly into index keys.

Add one sibling bucket (do NOT extend `bucketRGEndTime`; see Key Decisions):

```go
bucketEndTimeToKey = []byte("endTimeToKey") // forward: time-ordered index

const endTimeBytes = 8 // big-endian uint64 nanoseconds, like rgEndTimeBytes
```

- `bucketEndTimeToKey` entry: key = `endTimeIndexKey(endNanos, jobKey)`,
  value = `nil`. The key is an 8-byte big-endian `uint64` of the job's end time
  `UnixNano`, then `dbDelimiter`, then the `jobKey`. Fixed-width big-endian
  nanoseconds sort chronologically as bytes (UnixNano is positive for all
  realistic times), so a byte range scan `[cutoffNanos, +inf)` yields exactly
  the in-window keys in ascending end-time order, and nanosecond resolution
  keeps near-simultaneous completions distinct.
- Latest-per-key cleanup needs the key's *prior* end time, to delete its prior
  forward entry on re-archive. That prior end time is recovered from the job's
  existing `bucketJobsComplete` record (read before `archiveJobTx` overwrites
  it), so NO second per-job bucket is needed. A dedicated per-key time bucket
  would instead add a random-jobKey-ordered write that scatters BoltDB pages on
  every archive and measurably regresses the archive write/page benchmark (see
  Key Decisions).

Use big-endian nanoseconds (not `time.RFC3339Nano`) for the time prefix: it is
fixed-width and orders correctly as raw bytes. `time.RFC3339Nano` is NOT safe
here because it trims trailing fractional zeros, so e.g. `:05Z` would sort after
`:05.5Z` lexically though it is earlier in time. This mirrors the existing
`bucketRGEndTime` encoding (`rgEndTimeBytes`, big-endian Unix seconds), extended
to nanosecond precision.

`bucketEndTimeToKey` is created with `CreateBucketIfNotExists` in `initDB`
(alongside the existing bucket creation block). No rebuild/back-fill step. The
manager must not error if the bucket is empty or newly created.

`endTimeIndexKey`:

```go
// endTimeIndexKey returns the bucketEndTimeToKey key for an archived job:
// 8-byte big-endian end-time UnixNano, then dbDelimiter, then its job key.
// Sorts chronologically as raw bytes.
func endTimeIndexKey(endNanos []byte, jobKey []byte) []byte
```

`endNanos` is `make([]byte, endTimeBytes)` filled via
`binary.BigEndian.PutUint64(endNanos, uint64(job.EndTime.UnixNano()))`.

Write the index inside `archiveJobTx` (which already runs in the single archive
`bolt.Batch` transaction, so no extra commit and no extra fsync). Call it BEFORE
the `bucketJobsComplete` Put, so it can still read the job's prior complete
record to recover the previous end time:

```go
func (db *db) archiveJobTx(tx *bolt.Tx, key, encoded []byte, job *Job) error {
    // ... existing std/live deletes ...
    // updateEndTimeIndex BEFORE the complete Put (it reads the prior record):
    //     if err := db.updateEndTimeIndex(tx, key, job); err != nil { return err }
    // ... existing complete Put, putJobStats, updateRGEndTime ...
}

// updateEndTimeIndex records job's end time in the time-ordered per-job index,
// replacing any previous entry for the same key so only the latest completion
// per key is indexed. Uses job.EndTime.UnixNano as 8 big-endian bytes. The prior
// end time is recovered from the job's existing bucketJobsComplete record, so
// this must run before that record is overwritten. No-op if the stored end time
// is unchanged.
func (db *db) updateEndTimeIndex(tx *bolt.Tx, jobKey []byte, job *Job) error
```

`updateEndTimeIndex` behaviour:
1. Compute `newTimeBytes` = 8 big-endian bytes of `job.EndTime.UnixNano()`.
2. `oldEncoded := tx.Bucket(bucketJobsComplete).Get(jobKey)`. If
   `len(oldEncoded) == 0` (first archive of this key), skip to step 4.
3. Decode `oldEncoded` (via the db codec handle) to a `*Job` and take its
   `EndTime.UnixNano()` as `oldNanos`. If `oldNanos == job.EndTime.UnixNano()`,
   return nil (idempotent, unchanged). Otherwise delete the stale forward entry
   `tx.Bucket(bucketEndTimeToKey).Delete(endTimeIndexKey(oldTimeBytes, jobKey))`,
   where `oldTimeBytes` is the 8 big-endian bytes of `oldNanos`.
4. Put forward entry `endTimeIndexKey(newTimeBytes, jobKey) -> nil`.

### Retrieval

```go
// retrieveCompleteJobsRecent returns archived jobs whose end time is at or past
// cutoff, decoded from bucketJobsComplete, by seeking bucketEndTimeToKey at
// cutoff's UnixNano and scanning forward. A job currently live again (being
// re-run) is skipped. Returns jobs in ascending end-time order. An absent/empty
// index yields an empty slice, no error.
func (db *db) retrieveCompleteJobsRecent(cutoff time.Time) ([]*Job, error)
```

Implementation: `View`; `seek := make([]byte, endTimeBytes)` with
`binary.BigEndian.PutUint64(seek, uint64(cutoff.UnixNano()))`; cursor
`Seek(seek)`; for each key, parse the trailing jobKey via `lookupEntryJobKey`
(LastIndex of `dbDelimiter`); decode via `decodeArchivedJob` (skips absent or
currently-live keys); collect non-nil jobs. Scan runs to the end of the bucket
(all entries from cutoff onward are in-window). A forward key equals
`cutoffNanos` only as a strict prefix followed by `dbDelimiter`+jobKey, so it
sorts after `seek` and an exactly-at-cutoff completion is included.

### Server helper + handler

```go
// getJobsRecent returns archived jobs that finished within period of now,
// across all rep groups, after applying the shared limit/std/env filtering.
func (s *Server) getJobsRecent(ctx context.Context, period time.Duration,
    limit int, getStd, getEnv bool) (jobs []*Job, srerr string, qerr string)
```

- `cutoff := time.Now().Add(-period)`.
- `jobs, err := s.db.retrieveCompleteJobsRecent(cutoff)`; on err return
  `ErrDBError`, `err.Error()`.
- `jobs = s.limitJobs(ctx, jobs, limitJobsOptions{Limit: limit, GetStd: getStd,
  GetEnv: getEnv})` (no State: state filtering is rejected client-side; all
  results are JobStateComplete).

```go
// handleGetRecent gets archived jobs finished within cr.Period.
func (s *Server) handleGetRecent(ctx context.Context, cr *clientRequest) (
    *serverResponse, string, string)
```

- If `cr.Period <= 0` return `ErrBadRequest, ""`.
- Call `getJobsRecent`; return `jobsResponse(jobs), srerr, qerr`.

Request method constant `requestMethodGetRecent = "getrec"` (with the other
constants in `serverCLI.go`); dispatch case in `dispatchMethod`:
`case requestMethodGetRecent: return s.handleGetRecent(ctx, cr)`.

Add `Period time.Duration` to `clientRequest` (reuse existing `Limit`, `GetStd`,
`GetEnv`; reuse `serverResponse.Jobs`).

### Client method

```go
// GetRecent gets archived Jobs across all rep groups that finished running
// (were Archive()d) within the last period. Only exit-0 jobs are ever archived,
// so all returned jobs are complete; state must be "" (a non-"" state is a
// programming error - the CLI rejects state filters before calling).
// 'limit', 'getStd' and 'getEnv' behave as in GetByRepGroup.
func (c *Client) GetRecent(period time.Duration, limit int, state JobState,
    getStd, getEnv bool) ([]*Job, error)
```

Signature matches the prompt. `state` is accepted for symmetry with the other
getters but the CLI guarantees it is `""`; the client sends only
`Method: requestMethodGetRecent, Period: period, Limit: limit, GetStd, GetEnv`.

### Duration parsing (cmd)

`time.ParseDuration` lacks `d`/`w`. Add:

```go
// parseRecentDuration parses a duration like time.ParseDuration but also takes
// a single trailing convenience unit d (days = 24h) or w (weeks = 7*24h), e.g.
// "1d", "2w", "36h", "90m". A bare number, an empty string, a zero/negative
// duration, or any unparseable value returns an error.
func parseRecentDuration(s string) (time.Duration, error)
```

Rules:
- Empty string -> error `errEmptyRecentDuration`.
- If `s` ends in `d` or `w` AND the prefix parses as a non-negative
  `strconv.ParseFloat`: multiply by `24h` (d) or `7*24h` (w). Reject negative or
  non-finite. (Single trailing unit only; `1d12h` is NOT supported and errors.)
- Otherwise fall back to `time.ParseDuration(s)`.
- Reject results `<= 0` with a clear error (`--recent` needs a positive window).
- Error messages mention `--recent` and the accepted units.

### cmd/status.go wiring

- New flag: `statusCmd.Flags().StringVar(&cmdRecent, "recent", "", "...")` (long
  flag only; no short letter). Package var `cmdRecent string`.
- `countGetJobArgs()`: add `if cmdRecent != "" { set++ }` so `--recent` counts
  toward the existing `set > 1` mutual-exclusion `die`. Update the message to
  `"-f, -i, -l and --recent are mutually exclusive; only specify one of them"`.
- State-filter rejection: add a sentinel
  `errStatusStateFiltersRecent` ("state filters (...) are only supported in
  default or report group (-i) mode; remove them when using --recent") and a
  `case cmdRecent != "":` in `validateStatusStateFilters` (checked before the
  existing file/cmdline cases).
- In `Run`, when `cmdRecent != ""`: parse with `parseRecentDuration`, `die` on
  error, then fetch via `jq.GetRecent(period, statusLimit, "",
  statusOutputGetsStd(outputFormat), showEnv)`. The existing `--host`
  post-filter block, `--limit`/grouping, `showEnv` gating and all `-o` switch
  arms apply unchanged. Recent mode must take the non-fast path (it has no
  fast-status summary): make `statusRequiresFullJobFetch()` return true when
  `cmdRecent != ""`.
- Route the fetch through the existing job-selection path. Simplest: in
  `getJobs` add a `case cmdRecent != "":` returning `jq.GetRecent(...)`. Recent
  is a no-state mode, so it is only reached with `cmdState == ""` (state filters
  are rejected earlier); `set == 0` is false when `--recent` is set, so the
  `all` branch is not taken.

### Long-help update

Extend `statusCmd.Long`:
- The opening selection sentence to mention `--recent`, e.g. "Specify one of the
  flags -f, -l, -i or --recent ...".
- A paragraph documenting `--recent <duration>`: returns jobs that finished
  (were archived) within the last duration across all report groups; mutually
  exclusive with -f/-i/-l; accepts Go duration units plus `d` (days) and `w`
  (weeks); state filters are not supported with it; `--limit`, `--env` and
  `--host` are honoured (STDOUT/STDERR follow the chosen `-o` output format, as
  in the other modes; there is no `--std` flag). Include the example: "`--recent
  1w` reports jobs that finished in the last week".
- The flag usage string for `--recent` mentions the `d`/`w` units.

### Error handling

- Sentinel errors are package-level `var ... = errors.New(...)` per package.
- `die`/`warn` used in `cmd` exactly as the existing modes do.
- DB errors surface as `ErrDBError` with the underlying error string, as the
  other getters do.

## A. End-time index (jobqueue/db.go)

### A1: Index written on archive, no extra commit

As a maintainer, I want each archived job's end time recorded in a time-ordered
per-job index inside the existing archive transaction, so recent-window queries
are a bounded range scan and archive throughput is unaffected.

`bucketEndTimeToKey` created in `initDB`. `archiveJobTx` calls
`updateEndTimeIndex` within the same `bolt.Batch` transaction, before
overwriting the job's complete record.

**Package:** `jobqueue/`
**File:** `jobqueue/db.go`
**Test file:** `jobqueue/db_test.go`

**Acceptance tests:**

1. Given a fresh db, when a job with `EndTime` T is archived via `archiveJob`,
   then `bucketEndTimeToKey` contains exactly one entry whose key ends with
   `dbDelimiter` + the job's key and whose 8-byte prefix decodes (big-endian) to
   `T.UnixNano()`.
2. Given an archived job with key K at end time T1, when the same key K is
   archived again at a later end time T2, then `bucketEndTimeToKey` contains
   exactly one entry for K (the T2 entry) and no entry at T1 (latest-per-key, no
   stale entry; the prior T1 entry is recovered from K's complete record and
   deleted).
3. Given an archived job, when it is archived again with an unchanged `EndTime`,
   then `updateEndTimeIndex` makes no change (idempotent) and the index still
   has exactly one entry for the key.
4. Given a newly created db whose `bucketEndTimeToKey` has never been written,
   when `retrieveCompleteJobsRecent(time.Now().Add(-time.Hour))` is called, then
   it returns an empty slice and no error.

### A2: Windowed retrieval from the index

As a maintainer, I want `retrieveCompleteJobsRecent(cutoff)` to return archived
jobs with end time at or past cutoff, in ascending end-time order, skipping
keys that are currently live again.

**Package:** `jobqueue/`
**File:** `jobqueue/db.go`
**Test file:** `jobqueue/db_test.go`

**Acceptance tests:**

1. Given three archived jobs with distinct keys ending at now-3h, now-30m and
   now-1m, when `retrieveCompleteJobsRecent(now-1h)` is called, then it returns
   exactly the now-30m and now-1m jobs, in that order, and not the now-3h job.
2. Given an archived job at end time T whose key is also currently present in
   the live bucket (re-running), when `retrieveCompleteJobsRecent(T-1m)` runs,
   then that job is not returned (decodeArchivedJob skips live keys).
3. Given an empty complete store, when `retrieveCompleteJobsRecent(now-1h)` is
   called, then it returns an empty slice and no error.

### A3: Durability and consistency

As a maintainer, I want the index to survive a db close/reopen and to contain no
entry pointing at a never-archived or superseded key.

**Package:** `jobqueue/`
**File:** `jobqueue/db.go`
**Test file:** `jobqueue/db_test.go`

**Acceptance tests:**

1. Given a job archived at end time T (within 1h of now), when the db is closed
   and reopened from the same file, then `retrieveCompleteJobsRecent(now-1h)`
   returns that job (the index is durable; no back-fill needed because the entry
   was written at archive time).
2. Given a key archived at T1 then re-archived at T2, when the db is reopened,
   then `retrieveCompleteJobsRecent(T1-1m)` returns the job exactly once with
   its T2 end time (no duplicate, no stale T1 entry).

## B. Server retrieval path (jobqueue)

### B1: GetRecent client/server round-trip

As an operator, I want `Client.GetRecent` to return, via the manager, the
archived jobs that finished within the period across all rep groups, so I see
recent completions without naming rep groups.

**Package:** `jobqueue/`
**Files:** `jobqueue/client.go`, `jobqueue/server.go`, `jobqueue/serverCLI.go`
**Test file:** `jobqueue/jobqueue_test.go`

Tests use the existing server/client harness (`Serve`, `Connect`) and complete
jobs the real way: `Add` -> `Reserve` -> `Started` -> `Archive(job,
&JobEndState{Exited: true, Exitcode: 0, EndTime: ...})`.

**Acceptance tests:**

1. Given two jobs in different rep groups, both run and archived just now, when
   `GetRecent(1*time.Hour, 0, "", false, false)` is called, then both jobs are
   returned (one query, spanning rep groups).
2. Given a job archived with `EndTime` set to 2h ago, when `GetRecent(1*Hour, 0,
   "", false, false)` is called, it is not returned; when `GetRecent(3*Hour, 0,
   "", false, false)` is called, it is returned (window boundary).
3. Given a job that has been added and reserved but not archived (still
   incomplete), when `GetRecent(1*Hour, 0, "", false, false)` is called, then it
   is not returned (only archived jobs).
4. Given a command archived once (T1) and then re-run and re-archived (T2, just
   now), when `GetRecent(1*Hour, 0, "", false, false)` is called, then the job
   appears once with end time T2 (latest completion only; no duplicates).
5. Given five archived jobs that share state/exit-code/fail-reason, when
   `GetRecent(1*Hour, 2, "", false, false)` is called, then 2 jobs are returned
   and the last carries `Similar == 3` (the shared limit/grouping path is
   applied, exactly as `GetByRepGroup`).
6. Given an archived job, when `GetRecent(1*Hour, 0, "", true, true)` is called,
   then the returned job has its Env populated (and Std handled like other
   getters; archived jobs carry no live std).
7. Given a request with `Period <= 0` reaching `handleGetRecent`, it returns
   `ErrBadRequest` and no jobs.

### B2: GetRecent reflects window movement after time passes

As an operator, I want a job to drop out of the recent results once it is older
than the window.

**Package:** `jobqueue/`
**Test file:** `jobqueue/jobqueue_test.go`

**Acceptance tests:**

1. Given a job archived with `EndTime` set to now-90s, when `GetRecent(2*Minute,
   0, "", false, false)` is called, it is returned; when `GetRecent(1*Minute, 0,
   "", false, false)` is called, it is not returned (same data, narrower window
   excludes it).

## C. CLI wiring (cmd/status.go)

### C1: --recent flag selects recent archived jobs end-to-end

As an operator, I want `wr status --recent <duration>` to list jobs that ended
within the window across all rep groups, rendered like the other modes.

**Package:** `cmd/`
**File:** `cmd/status.go`
**Test file:** `cmd/status_test.go`

Use `startStatusTestServer` + `runStatusForTest` (which calls `statusCmd.Run`).
Extend `resetStatusForTest` to reset `cmdRecent` and the `recent` flag to `""`.

**Acceptance tests:**

1. Given a job run and archived just now in rep group "rg-recent", when
   `runStatusForTest(t, "--recent", "1h", "--output", "details")` is run, then
   the output contains the job's command and "Status: complete".
2. Given one job archived just now and another archived with `EndTime` 2h ago,
   when `runStatusForTest(t, "--recent", "1h", "--output", "plain")` is run, the
   recent job's key appears and the old job's key does not.
3. Given archived jobs, when `runStatusForTest(t, "--recent", "1h", "--output",
   "json")` is run, then stdout is a JSON array of the in-window jobs (renders
   under all `-o` formats like the other modes; assert counts/table/json each
   produce output without error in at least one test).

### C2: --recent is mutually exclusive with -f/-i/-l

As an operator, I want combining `--recent` with another selector to fail with
the existing-style message extended to mention `--recent`.

**Package:** `cmd/`
**File:** `cmd/status.go`
**Test file:** `cmd/status_test.go`

`countGetJobArgs` is unit-testable without a server (set the package flag vars).

**Acceptance tests:**

1. Given `cmdRecent = "1h"` and `cmdIDStatus = "x"`, when `countGetJobArgs()` is
   called, then it returns 2 (so `Run` would `die`).
2. Given `cmdRecent = "1h"` and `cmdLine = "echo"`, then `countGetJobArgs()`
   returns 2.
3. Given `cmdRecent = "1h"` and `cmdFileStatus = "f"`, then `countGetJobArgs()`
   returns 2.
4. Given only `cmdRecent = "1h"` set, then `countGetJobArgs()` returns 1.
5. The `die` message constant contains "--recent" and "mutually exclusive".

### C3: --recent rejects state filters

As an operator, I want a state filter combined with `--recent` to be rejected,
because only exit-0 jobs are archived so those states can never match.

**Package:** `cmd/`
**File:** `cmd/status.go`
**Test file:** `cmd/status_test.go`

**Acceptance tests:**

1. Given `cmdRecent = "1h"` and `showBuried = true`, when
   `validateStatusStateFilters(statusStateFilters())` is called, it returns a
   non-nil error whose message contains "state filters" and "--recent".
2. Given `cmdRecent = "1h"` and every state-filter flag false (and
   `showMissingDeps` false), then `validateStatusStateFilters(nil)` returns nil
   (no filter, no rejection).
3. Given `cmdRecent = "1h"` and `showMissingDeps = true`, then
   `validateStatusStateFilters` returns a non-nil error mentioning "--recent"
   (missing-deps is a state-style filter unsupported in recent mode).

### C4: --limit, --env and --host honoured

As an operator, I want the recent results to obey `--limit`, `--env` and the
`--host` post-filter exactly as the other modes (STDOUT/STDERR follow the chosen
`-o` output format; there is no `--std` flag).

**Package:** `cmd/`
**File:** `cmd/status.go`
**Test file:** `cmd/status_test.go`

**Acceptance tests:**

1. Given several archived jobs sharing state/exit/fail-reason, when
   `runStatusForTest(t, "--recent", "1h", "-o", "details", "--limit", "1")` is
   run, then the output reports the others via "+ N other commands with the same
   status".
2. Given archived jobs that ran on different hosts, when `runStatusForTest(t,
   "--recent", "1h", "--host", "<hostA>", "-o", "plain")` is run, then only
   jobs whose Host/HostID/HostIP equals hostA appear (client-side post-filter,
   same code path as other modes).

### C5: d/w duration parsing

As an operator, I want `--recent` to accept `d` (days) and `w` (weeks) in
addition to Go's standard units, with clear errors for bad input.

**Package:** `cmd/`
**File:** `cmd/duration.go`
**Test file:** `cmd/duration_test.go`

**Acceptance tests:**

1. `parseRecentDuration("1d")` returns `24*time.Hour`, nil error.
2. `parseRecentDuration("2w")` returns `14*24*time.Hour`, nil error.
3. `parseRecentDuration("90m")` returns `90*time.Minute`, nil error.
4. `parseRecentDuration("36h")` returns `36*time.Hour`, nil error.
5. `parseRecentDuration("0.5d")` returns `12*time.Hour`, nil error.
6. `parseRecentDuration("")` returns an error mentioning `--recent`.
7. `parseRecentDuration("banana")` returns a non-nil error.
8. `parseRecentDuration("1d12h")` returns a non-nil error (single trailing unit
   only).
9. `parseRecentDuration("0s")` and `parseRecentDuration("-1h")` return non-nil
   errors (window must be positive).

### C6: long help documents --recent

As an operator, I want `wr status -h` to document `--recent`, its mutual
exclusion, the `d`/`w` units and an example.

**Package:** `cmd/`
**File:** `cmd/status.go`
**Test file:** `cmd/status_test.go`

Use `compactWhitespace(commandHelpForTest(t, statusCmd))`.

**Acceptance tests:**

1. The help contains "--recent".
2. The help contains "--recent 1w" and "finished in the last week".
3. The help mentions days and weeks units (e.g. contains "d (days)" and
   "w (weeks)").
4. The help states `--recent` is mutually exclusive with the other selectors
   (contains "--recent" near "mutually exclusive" or the updated selection
   sentence listing -f, -l, -i and --recent).

## D. Performance (benchmarks)

### D1: No archive/add/modify regression

As a maintainer, I want `make bench` before vs after to show no extra per-job
bolt writes/pages on the add and archive paths, and modify ns/op within
tolerance.

**Package:** `jobqueue/`
**Test file:** `jobqueue/db_bench_test.go` (existing benchmarks; no new logic).

Procedure (record numbers in the PR / `.docs/recent`):
- Run on the branch point: `make bench` (runs `BenchmarkAddJobs`,
  `BenchmarkUpdateJobState`, `BenchmarkArchiveJobs`,
  `BenchmarkModifyLiveJobsReverseLookup`, etc.). Capture `bolt_writes/job` and
  `bolt_pages/job` from the benchmarks that report them (`BenchmarkAddJobs` and
  `BenchmarkArchiveJobs`), and `ns/op` per benchmark
  (`BenchmarkModifyLiveJobsReverseLookup` reports ns/op and allocs only).
- Run again after implementation; compare.

**Acceptance criteria (the bar):**

1. `BenchmarkArchiveJobs` `bolt_writes/job` does not increase vs baseline (the
   index write is inside the existing archive `bolt.Batch`, so no extra commit /
   meta write).
2. `BenchmarkArchiveJobs` `bolt_pages/job` does not increase vs baseline beyond
   measurement noise; `ns/op` within ~5-10%.
3. `BenchmarkAddJobs` `bolt_writes/job` and `bolt_pages/job` are unchanged (the
   feature touches neither the add nor the modify persistence path: the end-time
   index is written solely inside `archiveJobTx`, so add and modify write/page
   counts cannot change by construction). `BenchmarkModifyLiveJobsReverseLookup`
   reports ns/op and allocs only (not write/page counts), so the modify
   guarantee is its `ns/op` within ~5-10% of baseline.
4. If any of the above regresses, it must be resolved before completion (e.g.
   shrink the index value, or reconsider the key/value layout) - not waved
   through.

## Implementation Order

Each phase builds on tested foundations from the prior phase.

1. **Phase 1 - End-time index (A1, A2, A3).** Add buckets + `initDB` creation;
   `endTimeIndexKey`, `updateEndTimeIndex`, wire into `archiveJobTx`;
   `retrieveCompleteJobsRecent`. Tests in `db_test.go`. No server/CLI yet. Run
   `make bench BENCH=BenchmarkArchiveJobs` here to confirm D1.1/D1.2 early.

2. **Phase 2 - Server + client path (B1, B2).** `clientRequest.Period`;
   `requestMethodGetRecent` + dispatch; `handleGetRecent`; `getJobsRecent`
   (reusing `limitJobs`); `Client.GetRecent`. Tests in `jobqueue_test.go`.
   Depends on Phase 1.

3. **Phase 3 - Duration parsing (C5).** `cmd/duration.go` +
   `cmd/duration_test.go`. Independent of Phases 1-2; can run in parallel with
   Phase 2.

4. **Phase 4 - CLI wiring + help (C1-C4, C6).** `--recent` flag, mutual-exclude
   count + message, state-filter rejection sentinel/case,
   `statusRequiresFullJobFetch`, `getJobs` recent case, call `GetRecent`, help
   text; extend `resetStatusForTest`. Depends on Phases 2 and 3.

5. **Phase 5 - Benchmark sign-off (D1).** Run full `make bench` before/after,
   record comparison, resolve any regression. Final gate.

## Appendix: Key Decisions

- **Sibling per-job index, not extending `bucketRGEndTime`.** `bucketRGEndTime`
  stores one 8-byte latest end time per *rep group*, overwriting earlier values;
  it is keyed by rep group, so it cannot answer "which jobs finished in window"
  and cannot be range-scanned by time. The feature needs a per-job, time-ordered
  index, a fundamentally different shape, so a single sibling forward index
  (`bucketEndTimeToKey`) is added. `bucketRGEndTime` is left untouched (still
  used by `GetLastCompletionTimeByRepGroup`).

- **Key layout: big-endian UnixNano, not RFC3339.** `endTimeIndexKey` is
  `8-byte big-endian UnixNano + dbDelimiter + jobKey`. Fixed-width big-endian
  bytes are lexically chronological, so a byte range scan over `[cutoff, +inf)`
  is exact and ordered. The prompt suggested an RFC3339 timestamp prefix; it is
  NOT used because `time.RFC3339Nano` trims trailing fractional zeros (so `:05Z`
  sorts after `:05.5Z` though earlier in time) and plain second-precision
  `time.RFC3339` would collide sub-second completions. Big-endian UnixNano fixes
  both: fixed width, correct order, and nanosecond distinctness so two
  completions in the same second stay distinct and ordered. It mirrors the
  existing `bucketRGEndTime` (`rgEndTimeBytes`) encoding at finer precision. Job
  keys are hex (`byteKey`) and never contain `dbDelimiter`, so parsing the
  trailing jobKey via `lookupEntryJobKey` (existing helper, LastIndex of
  delimiter) is unambiguous.

- **Latest-per-key via the complete record, not a cleanup-pointer bucket.**
  Because the forward key embeds the end time, re-archiving a key at a new time
  would leave a stale entry. `updateEndTimeIndex` deletes the previous forward
  entry in the same transaction by recovering the key's prior end time from its
  existing `bucketJobsComplete` record (read before that record is overwritten),
  guaranteeing one entry per key (the latest) - satisfying "no duplicate jobs in
  results" and "earlier completion drops out once superseded" without storing
  historical completion events or an execution-unique index (per resolved
  decisions). An earlier design used a second `bucketKeyEndTime` pointer bucket,
  but its per-archive write was keyed by the random jobKey hash, scattering
  BoltDB pages and increasing `bolt_writes/job` / `bolt_pages/job` on the archive
  benchmark by ~34%; recovering the prior time from the already-written complete
  record adds zero extra per-archive writes (the decode runs only on the rare
  re-archive of an identical key, off the measured first-archive path), which is
  what keeps the D1 archive bar.

- **No back-fill.** Per resolved decision, only jobs archived from the upgrade
  onward are indexed; `initDB` creates the bucket but runs no rebuild scan. The
  index is durable for entries it has written. `--recent` may omit pre-upgrade
  completions until those commands are re-run.

- **Folded into the archive transaction for the benchmark bar.** `archiveJobTx`
  already runs in the one archive `bolt.Batch`; the single index Put there adds
  zero extra commits/fsyncs, so `bolt_writes/job` (the write-coalescing signal)
  cannot rise from extra commits. The one extra entry is tiny (a ~44-byte key +
  nil value in `bucketEndTimeToKey`) and is keyed time-first, so concurrent
  archives append near the right edge of the tree and dirty very few pages,
  keeping the per-job write/page delta within measurement noise. (An earlier
  two-bucket design added a second write keyed by the random jobKey, which
  scattered pages and broke this bar by ~34%; it was removed - see the
  latest-per-key decision above.) D1 makes this a measured gate, not an
  assumption. Add and modify persistence is not touched (the index is written
  only in `archiveJobTx`), so `BenchmarkAddJobs` write/page counts are unchanged
  and `BenchmarkModifyLiveJobsReverseLookup` ns/op is unaffected.

- **State filters rejected, not applied (resolved decision).** Only exit-0 jobs
  are archived, so `--buried/--running/--pending/--dependent/--missing_deps/`
  `--suspended` can never match under `--recent`; supplying one errors via the
  extended "state filters are only supported in..." message, mirroring how
  `-f`/`-l` reject state filters. `--limit`, `--env` and `--host` are still
  honoured (STDOUT/STDERR follow the chosen `-o` output format; there is no
  `--std` flag).

- **Display unchanged (resolved decision).** `--recent` is retrieval-only: same
  default `-o details`, same `--limit 1` grouping, same `--limit 0` per-job
  listing, same `-o` formats. No special default format or limit; no
  window/result cap; large windows share the existing large-result
  timeout/warning behaviour.

- **Cross-user scope.** `--recent` is scoped to the requesting user's manager
  exactly like the other modes (one manager per user); no cross-user query is
  introduced.

- **Testing strategy.** Behaviour-focused: index tests assert
  persisted/retrieved state via the db API (A); client/server tests drive the
  real round-trip and assert returned jobs (B); CLI tests run `statusCmd.Run`
  against a live test server and assert rendered output and exit behaviour, plus
  pure-function tests
  for `countGetJobArgs`, `validateStatusStateFilters` and `parseRecentDuration`
  (C). No test asserts private layout or mere absence of artefacts. Implementors
  follow **go-implementor**; reviewers follow **go-reviewer**; tests follow
  **testing-principles** and the GoConvey mechanics in **go-conventions**.
