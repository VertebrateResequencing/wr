# Status summary decoding

- [x] `wr status -i <repgroup> -z -o summary` scales with the rep group's
  archived history. Its median was 257ms at 600 runners, 9.4s at 2,100, and
  25.6s at 3,000 (p95 30.8s), approaching the 60s client floor. Also, the
  status web page's `ws_seed` median grew from 0.5s to 2.4s, and
  `ws_details_first` p95 reached 1.2-4.4s.
  - Source: prodsim rounds 2 and 3. The round-3 evidence is in
    `/nfs/hgi/wr/sb10-bigdb/prodsim3/prodsim-1790596675/` (`report.txt`,
    `calls.tsv`, `profiles/`). Round 1 (`260927-prodsim-findings.md`, "Known,
    re-observed") left it as the O(history) status path that
    `.docs/reliable4/background.md` "Reserved" names.
  - Root cause: `addCompleteJobStatus` (`jobqueue/db.go`) fully decoded every
    archived record of the rep group, 20KB `Cmd` and env included, to read six
    fields: `PeakRAM`, `PeakDisk`, `StartTime`, `EndTime`, `CPUtime` and
    `State`. In the 14:48 profile (`poststart.1790603296.cpu.pprof`) the summary
    path took 12.7s of CPU in 30s. Of the scan loop's 15.2s, the decode was
    8.8s, the complete-bucket `Get` 4.6s and the live-bucket `Get` 1.7s. The
    `Get`s total includes the web seed's count-only scan.
  - Red command:
    `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run TestStatusSummaryWithoutDecodingHistory`
    (exit 1). The test seeds 300 varied archived jobs with 4KB commands, one of
    them live again, plus a live-only key and an interleaved second rep group.
    The detailed summary must do no full decodes (`db.archivedDecodes`, which
    the old full decode was made to bump for the red run). It must also
    allocate less than half a command's bytes per record, which catches a full
    decode that bypasses the counter. Before the fix:

    ```
    Line 84:
    Expected: 0
    Actual:   299
    (Should equal)!
    ```

    With the counter bump removed, the allocation bound failed the same way on
    the unfixed code (`Expected '6928' to be less than '2048'`). With both
    cost assertions disabled, the test passed on the unfixed code. So its
    `ShouldResemble` against `fullDecodeCompleteJobStatus`, the old loop kept
    in the test, pins what the summary returned before the fix.
  - Fix: the detailed scan decodes each record into `completeJobUsage`
    (`jobqueue/status_summary.go`), which has only those six fields, named as
    on `Job`. A reused decoder matches them by name and structurally skips the
    rest, the same way `archivedJobFacets` does for the bounded history pages.
    `RepGroupStatus.addCompleteUsage` pushes the same values in the same
    lookup order as `AddCompleteJob`, which now delegates to it. `wallTime` is
    shared with `Job.WallTime`. So the summary is bit-for-bit what the full
    decode gave, and so is the printed output. The test compares whole
    `RepGroupStatus` values, floats and zoned times included.
  - Design considered and not taken: a per-rep-group aggregate kept up to date
    at archive time would make the summary O(1). But `StatusMeasure` is
    Welford's running mean and variance, which depends on the order values are
    added in. An aggregate built in completion order cannot reproduce the
    lookup-order result exactly. Removing a re-run's old values is lossy too.
    So the printed means and deviations could change in their last digit,
    such as `int(mean)` MB flipping across an integer. An exact integer-sum
    aggregate avoids that, but changes today's output in the same way. It
    would also need a schema migration to backfill existing histories, and
    handling for re-runs, keys shared between rep groups, and live-again jobs.
    A per-job compact stats index (about 90 bytes per archived job) would
    remove the complete-bucket `Get` and most of the decode, but still be
    O(history), and costs DB space on every archived job.
  - Numbers (`BenchmarkRepGroupStatusDetails`, 20KB commands, one summary):

    | archived | before | after | before alloc | after alloc |
    | --- | --- | --- | --- | --- |
    | 1,000 | 23.4ms | 4.2ms | 23.9MB | 0.30MB |
    | 10,000 | 571ms | 57ms | 239MB | 3.2MB |
    | 30,000 | 1,053ms | 157ms | 717MB | 9.6MB |

    Full decodes per summary went from N-1 to 0. In production the per-record
    `Get`s are relatively dearer than in this in-cache benchmark. From the
    profile split, expect about 2.5-3x less summary CPU there, and much less
    GC pressure (about 560MB less allocated per call at prodsim's ~28k jobs).
    The summary is still O(history) in `Get`s.
  - Web page, reported rather than fixed:
    - `ws_seed` is `writeStatusCountSeed`'s count-only scan, which does no
      decoding since #636. Its remaining cost is two bolt `Get`s per archived
      key of every live rep group (3.6-5.7s of CPU in 30s in the round-3
      profiles), run while holding the connection's write mutex. The obvious
      fix, a persisted per-rep-group archived count, would be exact (integers),
      but must track re-runs, live-again keys, lookup deletion on removal, and
      keys in more than one rep group. That is not a cheap, safe change.
    - `ws_details_first` for `State: complete` goes through
      `selectOldestArchivedJobs`, which already decodes only facets but still
      walks and `Get`s the whole history. Other states skip the history
      (`getDBJobsByRepGroup`). The 5% of searches send no limit and fully
      decode within the archived-bytes budget. The rest of its CPU is decoding
      each returned job's env for display (`jobStatuses`), which the page
      shows. Nothing cheap stood out.
  - Files: `jobqueue/db.go`, `jobqueue/status_summary.go`, `jobqueue/job.go`,
    `jobqueue/status_summary_decode_test.go`, `CHANGELOG.md`.
