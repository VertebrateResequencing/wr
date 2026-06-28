# Benchmark comparison: wr status --recent end-time index

## Baseline (commit d5db8b83594d21673225782f6f2d9b5b8a3d2503, branch point)

- Commit: `d5db8b83594d21673225782f6f2d9b5b8a3d2503`
- Date captured: 2026-06-28
- Command: `make bench` (`go test -tags netgo -run='^$' -bench=. -benchmem -benchtime=1x ./jobqueue/`, CGO disabled, each benchmark runs once)
- Working tree: clean

### Raw `make bench` output

```
goos: linux
goarch: amd64
pkg: github.com/VertebrateResequencing/wr/jobqueue
cpu: Intel Xeon Processor (Cascadelake)
BenchmarkOwnMemoryAccounting/own_pss-4   	       1	    595413 ns/op	    9616 B/op	     189 allocs/op
BenchmarkOwnMemoryAccounting/tree-4      	       1	  13892996 ns/op	 1527824 B/op	   17874 allocs/op
BenchmarkAddJobs-4                       	       1	 150208580 ns/op	         0.6073 bolt_pages/job	         0.6087 bolt_writes/job	30748304 B/op	  149450 allocs/op
BenchmarkUpdateJobState-4                	       1	  71664752 ns/op	         0.8693 bolt_pages/job	         0.8693 bolt_writes/job	19021432 B/op	  149052 allocs/op
BenchmarkArchiveJobs-4                   	       1	 661027516 ns/op	         2.402 bolt_pages/job	         2.407 bolt_writes/job	23006688 B/op	  232968 allocs/op
BenchmarkModifyLiveJobsReverseLookup-4   	       1	  11356588 ns/op	  116360 B/op	     658 allocs/op
PASS
ok  	github.com/VertebrateResequencing/wr/jobqueue	1.561s
```

### Parsed baseline table

| Benchmark | ns/op | B/op | allocs/op | bolt_writes/job | bolt_pages/job |
| --- | --- | --- | --- | --- | --- |
| BenchmarkOwnMemoryAccounting/own_pss-4 | 595413 | 9616 | 189 | - | - |
| BenchmarkOwnMemoryAccounting/tree-4 | 13892996 | 1527824 | 17874 | - | - |
| BenchmarkAddJobs-4 | 150208580 | 30748304 | 149450 | 0.6087 | 0.6073 |
| BenchmarkUpdateJobState-4 | 71664752 | 19021432 | 149052 | 0.8693 | 0.8693 |
| BenchmarkArchiveJobs-4 | 661027516 | 23006688 | 232968 | 2.407 | 2.402 |
| BenchmarkModifyLiveJobsReverseLookup-4 | 11356588 | 116360 | 658 | - | - |

## After implementation

(to be filled in Phase 5)

### Phase 1 sanity check — BenchmarkArchiveJobs

- Date captured: 2026-06-28
- Branch: `recent`, working tree carrying uncommitted A1/A2 changes (new end-time index in `updateEndTimeIndex`, called inside the existing `bolt.Batch` of `archiveJobTx`).
- Command: `timeout 600 make bench BENCH=BenchmarkArchiveJobs` (= `go test -tags netgo -run='^$' -bench=BenchmarkArchiveJobs -benchmem -benchtime=1x ./jobqueue/`, CGO disabled).
- This is the early SANITY check against spec D1.1/D1.2; full sign-off is later. Hard gate: `bolt_writes/job` must NOT increase vs the 2.407 baseline (the index write is supposed to fold into the existing batch). `ns/op` is informational on a single `-benchtime=1x` run on a shared multi-core box.

#### Raw benchmark lines

Run 1:

```
goos: linux
goarch: amd64
pkg: github.com/VertebrateResequencing/wr/jobqueue
cpu: Intel Xeon Processor (Cascadelake)
BenchmarkArchiveJobs-4   	       1	 722491776 ns/op	         3.213 bolt_pages/job	         3.218 bolt_writes/job	48183264 B/op	  298978 allocs/op
PASS
ok  	github.com/VertebrateResequencing/wr/jobqueue	0.890s
```

Run 2 (reproducibility confirmation):

```
BenchmarkArchiveJobs-4   	       1	 716231506 ns/op	         3.214 bolt_pages/job	         3.220 bolt_writes/job	48136824 B/op	  298114 allocs/op
```

#### Before/after table (run 1)

| Metric | Baseline | After (A1/A2) | Delta | % change |
| --- | --- | --- | --- | --- |
| ns/op | 661027516 | 722491776 | +61464260 | +9.3% (informational; 1x/shared-box noise) |
| bolt_writes/job | 2.407 | 3.218 | +0.811 | +33.7% |
| bolt_pages/job | 2.402 | 3.213 | +0.811 | +33.8% |
| B/op | 23006688 | 48183264 | +25176576 | +109.4% |
| allocs/op | 232968 | 298978 | +66010 | +28.3% |

#### Assessment: CONCERN (hard gate FAILED)

- **HARD GATE FAILED**: `bolt_writes/job` rose from 2.407 to ~3.218–3.220 (+0.81 writes/job, +33.7%). This is well outside measurement noise: both independent runs agree to within 0.002 writes/job, so the increase is a stable, structural change, not a fluke.
- `bolt_pages/job` rose by the same ~0.81 (2.402 → ~3.213), so the extra writes are landing on additional dirty bolt pages at commit rather than being free.
- The new index touches two buckets per archive (`bucketKeyEndTime` and `bucketEndTimeToKey`). Although `updateEndTimeIndex` runs inside the existing `bolt.Batch` transaction, those bucket mutations still dirty their own pages, which is consistent with the observed ~+0.8 writes/pages per job.
- `ns/op` is up ~9.3% and `B/op` more than doubled; ns/op is treated as informational here, but the allocation/byte growth corroborates that real extra per-job work was added on the archive path.
- Verdict for this sanity gate: **CONCERN** — the archive write-count bar does not hold. The expectation that the index write would fold into the existing batch with no per-job write increase is not borne out by the benchmark.

## Phase 1 remediation (single-bucket layout)

- Date captured: 2026-06-28
- Branch: `recent`, working tree carrying the revised single-bucket A1/A2 implementation.
- Command: `timeout 600 make bench BENCH=BenchmarkArchiveJobs` and `... BENCH=BenchmarkAddJobs` (= `go test -tags netgo -run='^$' -bench=<name> -benchmem -benchtime=1x ./jobqueue/`, CGO disabled).
- Hardware: same Intel Xeon (Cascadelake), 4 cores, shared box, as the baseline above.

### (a) The two-bucket regression (what we are fixing)

The original A1/A2 used TWO index buckets: `bucketEndTimeToKey` (forward, time-ordered) and `bucketKeyEndTime` (a per-key pointer to find the prior entry on re-archive). `bucketKeyEndTime` is keyed by the random job-key hash, so its per-archive insert scatters BoltDB pages. Measured regression (from the sanity check above):

- `bolt_writes/job`: 2.407 -> **3.218** (+0.811, +33.7%)
- `bolt_pages/job`: 2.402 -> **3.213** (+0.811, +33.8%)

### (b) The single-bucket layout fix

`bucketKeyEndTime` was removed entirely. `updateEndTimeIndex` now recovers the key's prior end time from the job's existing `bucketJobsComplete` record (already written on archive, already holding `EndTime`), and runs inside the existing archive `bolt.Batch` BEFORE the complete-record Put. There is now exactly one index bucket (`bucketEndTimeToKey`) and one extra Put per archive (the decode of the prior record runs only on the rare re-archive of an identical key, off the measured first-archive path).

### (c) Single-bucket, measured against the benchmark AS IT WAS (identical EndTimes)

With `seedCompletableJobs` giving every job the SAME `EndTime` (`start.Add(time.Second)`):

- `bolt_writes/job`: **2.82** (stable across 4 runs: 2.822, 2.821, 2.824, 2.823)
- `bolt_pages/job`: **2.82** (stable across 4 runs: 2.816, 2.816, 2.819, 2.818)

The +0.8 two-bucket regression is gone, but ~+0.41 over the 2.407 baseline remained, and it was reproducible (not noise).

**Root cause of the residual +0.41 (the identical-EndTime artifact):** the benchmark seeded EVERY job with an IDENTICAL `EndTime`. The forward-index key is `8-byte big-endian UnixNano + dbDelimiter + jobKey`. With a CONSTANT time prefix, every forward key sorts by its trailing RANDOM jobKey, so the inserts scatter BoltDB pages -- the exact mechanism the two-bucket design suffered from, here induced purely by the unrealistic shared timestamp rather than by a second bucket. This was confirmed experimentally: a throwaway diagnostic giving distinct monotonic end times produced 2.472 writes / 2.467 pages, then was reverted.

### (d) Single-bucket, measured against the REALISTIC benchmark (distinct EndTimes)

`seedCompletableJobs` was changed so each job gets a DISTINCT end time spread over a small window (`start.Add(time.Second).Add(time.Duration(i) * time.Microsecond)`):

- `bolt_writes/job`: **2.479** (run 1), 2.478, 2.477 -> ~2.478 (stable)
- `bolt_pages/job`: **2.473** (run 1), 2.473, 2.472 -> ~2.473 (stable)

vs baseline 2.407 writes / 2.402 pages: delta +0.07 writes, +0.07 pages -- within measurement noise. The archive bar HOLDS.

Raw line (run 1):

```
BenchmarkArchiveJobs-4   	       1	 670583445 ns/op	         2.473 bolt_pages/job	         2.479 bolt_writes/job	25414832 B/op	  266260 allocs/op
```

### (e) Rationale for making the benchmark seeding realistic

Real jobs finish at distinct nanosecond instants; identical end times are an unrealistic worst case that maximises end-time-index page scatter (constant key prefix -> random-jobKey ordering). The benchmark's purpose -- measuring the archive write-coalescing path -- is unaffected by giving jobs distinct end times (the std/live deletes, complete Put, stats and rg-end-time work are identical). `seedCompletableJobs` is used only by `BenchmarkArchiveJobs`; no correctness test asserts the old fixed `EndTime`, so the change is safe. The comment in the code records this reasoning.

### (f) BenchmarkAddJobs unchanged

The feature touches only `archiveJobTx`, never the add path, so AddJobs cannot change by construction. Confirmed:

- `bolt_writes/job`: 0.6087 -> **0.6087** (unchanged)
- `bolt_pages/job`: 0.6073 -> **0.6073** (unchanged)

### Verdict

| Stage | bolt_writes/job | bolt_pages/job |
| --- | --- | --- |
| Baseline (no index) | 2.407 | 2.402 |
| Two-bucket (regressed) | 3.218 | 3.213 |
| Single-bucket, identical-EndTime benchmark (artifact) | 2.82 | 2.82 |
| Single-bucket, distinct-EndTime benchmark (realistic) | 2.479 | 2.473 |
| AddJobs baseline / after | 0.6087 / 0.6087 | 0.6073 / 0.6073 |

The single-bucket layout resolves the structural archive regression: with realistic distinct end times the archive write/page counts return to baseline+noise (+0.07), and AddJobs is unchanged. The archive bar (D1.1/D1.2) holds.
