# Performance regressions since v0.37.2 (2026-10-02)

- Branch: `fix-perf-regressions`
- Base: `origin/develop` at `c9d9c5fa`
- Queue owner: `fix-perf-regressions`, this checklist

Source: `.docs/perf/261001-version-comparison.md` (PR #662). Evidence:
`/nfs/hgi/wr/sb10-bigdb/speed/`. Numbers below are `-count 6 -benchtime 1s`
on this host under `nice -n 19`, compared with benchstat.

- [x] #590 (commit 0cc79218, ContainerImageUser): AddJobs, UpdateJobState,
  ArchiveJobs +15-58% B/op, about 2.7 KiB more allocated per job, no time cost;
  the allocating line isn't known. Find it (memprofile/-benchmem, pprof
  -alloc_space) and remove the extra allocation without changing behaviour or
  the stored format (jobs stored by v0.38.0 must still decode).
  - Red: `CGO_ENABLED=0 nice -n 19 go test -tags netgo -run '^$' -bench
    'Benchmark(AddJobs|UpdateJobState|ArchiveJobs)$' -benchmem -count 6
    -benchtime 1s ./jobqueue/` on v0.37.2 and on develop (`c9d9c5fa`), then
    benchstat. Exit 0; the regression is in B/op:

    ```
                       │    v0372     │                  dev                  │
                       │     B/op     │     B/op       vs base                │
    AddJobs-8            26.49Mi ± 4%    29.26Mi ± 3%   +10.48% (p=0.002 n=6)
    UpdateJobState-8     14.06Mi ± 1%    21.94Mi ± 2%   +55.97% (p=0.002 n=6)
    ArchiveJobs-8        27.85Mi ± 0%    43.46Mi ± 2%   +56.05% (p=0.002 n=6)
    ```
  - Cause: `ContainerImageUser` was Job's first `omitempty` field. That moves
    Job off ugorji codec's `kStructSimple` encode path onto `kStruct`, which
    gathers the fields into a per-Encoder scratch slice (`e.slist.get`,
    encode.go:475 in codec v1.3.1) of 64 32-byte entries. wr made a new
    Encoder with `codec.NewEncoderBytes` for every job encode, so each encode
    allocated that scratch afresh.
  - Fix: `jobqueue/db.go` keeps a `sync.Pool` of Encoders on `db`, reused via
    `ResetBytes` in a new `db.encode`, which every job encode on the add,
    state-change and archive paths now uses. The encoded bytes are unchanged.
  - Test: `jobqueue/db_encode_test.go` `TestDBEncodeJob` bounds the bytes
    allocated per `encodeJob` (fails at 4879 B with a fresh Encoder, passes at
    about 2304 B), and checks that reused-encoder output is byte-identical to a
    fresh encode and decodes back with `ContainerImageUser` and the other new
    fields.
  - After (reviewer's rerun, same command, benchstat vs develop):

    ```
                       │     dev      │                after                 │
                       │     B/op     │     B/op      vs base                │
    AddJobs-8            29.26Mi ± 3%   23.19Mi ± 5%  -20.74% (p=0.002 n=6)
    UpdateJobState-8     21.94Mi ± 2%   13.45Mi ± 2%  -38.68% (p=0.002 n=6)
    ArchiveJobs-8        43.46Mi ± 2%   35.54Mi ± 3%  -18.23% (p=0.002 n=6)
    ```

    Against v0.37.2, B/op is now -12.4% for AddJobs and -4.4% for
    UpdateJobState. ArchiveJobs stays +27.6%, the part #555 added with its
    single archive writer (bolt pages/job 2.66 to 3.23). No sec/op got worse.
- [ ] Limiter (#555, commit 4e5739fc, withResolvedGroups which keeps the DB
  read outside the lock): limiter Inc/Dec and Capacity +58-68% sec/op, ~2x
  B/op, +75% allocs (~+1.1µs/call). Remove the overhead on the common path
  (e.g. avoid allocating when all groups are already resolved/cached; fast path
  under a read lock) while keeping #555's guarantee that the DB read happens
  outside the lock.
  - Red: `CGO_ENABLED=0 nice -n 19 go test -tags netgo -run '^$' -bench
    'BenchmarkLimiter' -benchmem -count 6 -benchtime 1s ./limiter/` on
    v0.37.2 and develop, then benchstat. Exit 0. IncDec's sec/op was noisy on
    v0.37.2 this run (±90%); the comparison report has +57.95% (p=0.002):

    ```
                      │    v0372     │                dev                 │
                      │    sec/op    │   sec/op     vs base               │
    LimiterIncDec-8     2.343µ ± 90%   3.293µ ± 3%        ~ (p=0.065 n=6)
    LimiterCapacity-8   1.797µ ±  7%   3.129µ ± 3%  +74.12% (p=0.002 n=6)
                      │    B/op    │     B/op      vs base                │
    LimiterIncDec-8     544.0 ± 0%    1120.0 ± 0%  +105.88% (p=0.002 n=6)
    LimiterCapacity-8   632.0 ± 0%    1240.0 ± 0%   +96.20% (p=0.002 n=6)
                      │ allocs/op  │  allocs/op   vs base               │
    LimiterIncDec-8     9.000 ± 0%   16.000 ± 0%  +77.78% (p=0.002 n=6)
    LimiterCapacity-8   11.00 ± 0%    19.00 ± 0%  +72.73% (p=0.002 n=6)
    ```
- [ ] JobCleanup (#575, commit db108fa8): JobCleanup* +17-33% sec/op, 4x
  allocs (Depth1 540→2190). Find and cut the extra allocations while keeping
  #575's safety behaviour (it made cleanup prove the directory is wr's own
  before deleting).
  - Red: `CGO_ENABLED=0 nice -n 19 go test -tags netgo -run '^$' -bench
    'BenchmarkJobCleanup' -benchmem -count 6 -benchtime 1s ./jobqueue/` on
    v0.37.2 and develop, then benchstat. Exit 0:

    ```
                       │    v0372     │                 dev                 │
                       │    sec/op    │    sec/op     vs base               │
    JobCleanup-8         334.8µ ± 17%   377.5µ ±  6%        ~ (p=0.065 n=6)
    JobCleanupDepth1-8   3.458m ±  9%   4.542m ± 16%  +31.36% (p=0.002 n=6)
    JobCleanupDepth8-8   3.460m ±  7%   5.987m ± 33%  +73.04% (p=0.002 n=6)
                       │     B/op     │     B/op       vs base                │
    JobCleanup-8         6.470Ki ± 0%   11.153Ki ± 0%   +72.38% (p=0.002 n=6)
    JobCleanupDepth1-8   17.78Ki ± 1%   127.18Ki ± 0%  +615.44% (p=0.002 n=6)
    JobCleanupDepth8-8   18.77Ki ± 1%   139.66Ki ± 0%  +644.20% (p=0.002 n=6)
                       │  allocs/op  │  allocs/op   vs base                │
    JobCleanup-8          135.0 ± 0%    203.0 ± 0%   +50.37% (p=0.002 n=6)
    JobCleanupDepth1-8    540.0 ± 0%   2194.0 ± 0%  +306.30% (p=0.002 n=6)
    JobCleanupDepth8-8    596.0 ± 0%   2376.0 ± 0%  +298.66% (p=0.002 n=6)
    ```
