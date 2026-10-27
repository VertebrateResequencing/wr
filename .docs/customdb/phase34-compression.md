# Phases 3 and 4, and compression: experiments of 2026-10-27

The owner asked whether Phases 3 (lazy specs, D3) and 4 (leases D6, or a
local-disk primary D5) are needed, and which designs to add to the spec, and
for trials of compression that keep or improve speed while cutting disk use.
All runs were on 2026-10-27 in the daytime (the earlier benchmarks were at
night), on the same host and NFS mount as [benchmarks.md](benchmarks.md),
under `nice -n 19`, one experiment at a time. Raw outputs are in
`results/261027/`.
The code is in `proto/` (`cmd/compbench`, `cmd/nfsprobe`, `internal/zc`,
batch compression in `internal/gc`, and `-stallctl`, `-timeline`, `-corpus`,
`-codec` and `-batchcodec` in `cmd/hotpath` and `cmd/recoverbench`).

## Compression

### Method

`cmd/compbench` reads the production fixture
(`runstate-gate/fixtures/prod.db`, copied to local disk) and re-encodes, with
the `flat` codec, all 118,213 live jobs (113MB) and 200,000 completed jobs as
spec plus run state (168MB). It also keeps the live jobs' Binc values as
stored today (187MB). The 591 records over 4KB form a third group, "big". Each
compressor runs single-threaded on each record alone ("per record"), on
64-record batches (a group commit), on 1000-record batches (an add), and on
64KB and 1MB blocks (a sealed history segment). Dictionaries are trained on
every tenth record and tested on the other nine. Every output is decompressed,
and one in seven is compared with its input.

The prototype's synthetic 10KB portal commands (`strings.Repeat("a")`) and
prodsim's padding (sequential sample names) both compress unrealistically
well, so every number here comes from real commands. Later runs build their
10KB commands by joining real production commands of at least 1KB
(`corpus.big`, 18,978 commands).

`klauspost/compress` v1.20.0's `dict.BuildZstdDict` panics on these records
(`zstd/enc_base.go:71`), so zstd uses the 64KB of content that
`dict.BuildS2Dict` chose as a raw-content dictionary.

### Results

The `none` row is the cost of copying bytes into the output (about 0.4us per
record), so the real cost of compressing is each row's figure minus it.

Live specs (production, mean 960 bytes):

| Codec | Per record: ratio, enc / dec | 64-record batch | 1MB block |
| --- | --- | --- | --- |
| none | 1.0, 0.46 / 0.15us | 1.0, 0.41 / 0.11us | 1.0 |
| s2 | 2.2, 0.98 / 0.37us | **12.0, 0.41 / 0.28us** | 13.3 |
| s2 + dictionary | 6.4, 1.97 / 0.46us | 12.7, 0.49 / 0.25us | |
| lz4 | 2.2, 2.1 / 0.30us | 13.3, 0.73 / 0.16us | 15.2 |
| zstd fastest | 2.6, 12.4 / 5.7us | **17.6, 1.30 / 0.37us** | 21.7 |
| zstd fastest + dictionary | 6.5, 13.8 / 1.5us | 19.1, 1.56 / 0.56us | |
| zstd better | 2.6, 15.0 / 5.4us | 18.1, 2.9 / 0.35us | 22.0 |

Completed jobs (spec plus 192-byte run state):

| Codec | Per record | 64-record batch | 64KB block | 1MB block |
| --- | --- | --- | --- | --- |
| s2 | 1.7 | 4.0 | 4.2 | 6.2 |
| s2 + dictionary | 3.5 | 5.1 | | |
| zstd fastest | 1.9 | 5.1 | 5.3 | 8.4 |
| zstd better | 1.9 | 5.4 | 5.6 | 9.1 |

Records over 4KB compress 12x (s2) to 27-31x (zstd) in every mode. Today's
Binc values compress 1.6x (s2) to 1.9x (zstd) per record, which is all a
per-value compression inside bbolt could get.

What this shows:

- **Compress batches and blocks, not records.** Consecutive records share
  cwd, rep group, requirements and most of the command, so a 64-record batch
  compresses 12-18x where a record alone compresses 2-2.6x. A dictionary
  recovers much of that for single records (6.4x), but a batch beats it
  without one.
- **s2 on a batch is free.** It costs nothing measurable over copying the
  bytes, and decodes at 3.4GB/s.
- **zstd buys 40-50% more** on top of s2, at about 1us per record to encode.
  It is the right choice where bytes are written once and kept: history
  segments, checkpoints and backups.
- **zstd on single small records is slow** in klauspost's implementation
  (12us to encode 1KB, against 1us for s2), so per-record zstd should be
  avoided.

## Phase 4: what a filesystem stall does to each design

### Method

`proto/scripts/stall.sh` puts each store behind the soaks' `fusestall` mount
over NFS (`/nfs/hgi/wr/sb10-bigdb/customdb/stall`), runs the production-shape
load (6,000 runners, jobs of 20-60s, 120k preloaded jobs, a 1000-job add every
5s, 10KB commands), and holds every fsync for 60s starting at T+90s.

- D2: the log behind the stall.
- D6: the same log with strict leases. A hand-out is in memory, but only once
  a durable lease record covers it; the manager writes a lease for the next
  1000 hand-outs ahead of need. (The earlier prototype let hand-outs past an
  unwritten lease go ahead, which overstated D6.) Archives stay durable before
  the reply, as D6 specifies.
- D5: the log on local disk, shipped every 200ms to a replica behind the
  stall.

### Results

Reservations, archives and added jobs in 15s windows (the stall covers
90-150s):

| Window | D2 res / arc / add | D6 res / arc / add | D5 res / arc / add |
| --- | --- | --- | --- |
| 60-75s | 2,384 / 2,384 / 3,000 | 2,434 / 2,434 / 3,000 | 2,488 / 2,488 / 3,000 |
| 75-90s | 2,188 / 2,188 / 3,000 | 2,148 / 2,148 / 3,000 | 2,176 / 2,176 / 3,000 |
| 90-105s | **0 / 0 / 0** | **0 / 0 / 0** | 2,199 / 2,199 / 3,000 |
| 105-150s | **0 / 0 / 0** | **0 / 0 / 0** | 6,787 / 6,787 / 9,000 |
| 150-165s | 6,000 / 6,000 / 4,000 | 6,000 / 6,000 / 4,000 | 2,298 / 2,298 / 3,000 |

| | D2 | D6 | D5 |
| --- | --- | --- | --- |
| Runs in 240s | 25,282 | 25,195 | 31,093 |
| Archive p99 / max | 58.4s / 60.1s | 58.2s / 60.1s | 2.4ms / 39ms |
| Add max | 60.1s | 60.1s | 54ms |
| Replica lag | | | 673ms steady; 128MB backlog at the end of the stall, shipped in seconds once it ended |

- **D6 does not help with a stall.** Every runner ends each job with a
  durable archive, so within one job length (20-60s) every runner is waiting
  on the stalled archive and nobody asks for a hand-out. Leases make the
  hand-out free but leave the runner's loop gated by the archive. D6 would
  only help if completions were also acknowledged before they are durable,
  with the runner keeping and re-reporting them until the manager confirms.
  That moves completion durability into the runner, which no design here
  proposes and which would need its own correctness work (a runner that exits
  or loses its host holds the only record of a completion).
- **D5 rides through the stall with no effect** on hand-outs, archives or
  adds. The cost is that during a stall the NFS replica falls behind by the
  stall's worth of writes (128MB here). A host failure in that window loses
  those writes for a restart on another host; a process crash loses nothing,
  because the local log is synced.
- D6's lease records cut the batch count by 71% (18,912 against 65,528)
  without changing throughput or latency in normal running, since reserve and
  start records are small and batch with archives anyway.

### How often does NFS stall?

`cmd/nfsprobe` appends 4KB and fdatasyncs it every 100ms, and 1MB every 10s,
to the same NFS mount (and, for comparison, to local disk), for 12 hours from
07:12 to 19:13.

| Target | Small appends | p50 / p99 / p99.9 | Max | Over 100ms | Over 1s | 1MB p99 / max |
| --- | --- | --- | --- | --- | --- | --- |
| NFS | 429,311 | 1.0 / 2.1 / 31.8ms | 2.6s | 20 | 6 | 12.9 / 93.6ms |
| Local disk | 429,001 | 1.1 / 1.8 / 16.4ms | 0.47s | 65 | 0 | 6.0 / 330ms |

The NFS outliers come in clusters in the first minute of an hour (09:00,
10:00, 12:00, 13:00, 16:00), which looks like a scheduled job on the filer
such as a snapshot. The worst, at 09:00 and 12:00, held small appends for
1.2 to 2.6s several times within 40s. Nothing came close to the 60s stall the
soaks inject. Under D2 a 2.6s hold delays every hand-out and archive by up to
2.6s and loses nothing, well inside the 60s client timeout. The soaks' long
stalls came from a full filesystem (ENOSPC) and the injected FUSE hold, not
from NFS latency, so how often production would stall for a minute is a
question about the filer's capacity and failures, not its speed. That keeps
D5 an option for later rather than a need.

## Compression on the hot path

`hotpath` with D2 on NFS, production shape (6,000 runners, jobs of 20-60s,
120k preloaded, a 1000-job add every 5s), 10KB commands built from real
commands of at least 1KB, each job with its own run of them. The whole of
each group-commit batch is compressed (`-batchcodec`); specs are not
compressed on their own.

| Batch codec | Runs/s | Reserve p50 / p99 | Archive p99 | Add p50 / p99 | Bytes written |
| --- | --- | --- | --- | --- | --- |
| none | 122.1 | 0.77 / 3.3ms | 15.6ms | 59.7 / 76.2ms | 1,614MB |
| s2 | 122.2 | 0.80 / 1.8ms | **2.4ms** | **28.5 / 34.9ms** | **85MB** |
| zstd fastest | 121.8 | 0.83 / 1.9ms | 2.6ms | 33.2 / 39.9ms | **58MB** |

Compression makes the hot path faster as well as smaller. An add's 10MB batch
becomes about 0.5MB, so its write and fdatasync take half the time, and the
transitions that share its batch, or queue behind it, stop waiting for it
(archive p99 15.6ms to 2.4ms). The compression itself runs in the writer
goroutine before the write; for s2 it costs less than the bytes it saves at
every batch size measured. zstd writes a third less than s2 for about 5ms more
per add.

A first run of this matrix built each job's command from corpus commands i,
i+1, ..., so neighbouring jobs shared almost all their text and the ratios
were inflated (1,614MB to 37MB). Those numbers are discarded; the table is
from the corrected generator (`jobgen.corpusStride`).

### At saturation

Same jobs, 300k preloaded, jobs of 0-1s, 60s: every run used all 311,000
jobs (5,183 runs/s), so throughput did not limit.

| Batch codec | Reserve p50 / p99 | Archive p99 | Add p50 / p99 | Bytes written |
| --- | --- | --- | --- | --- |
| none | 1.52 / 6.6ms | 13.8ms | 55 / 63ms | 3,397MB |
| s2 | 1.45 / 6.7ms | 27.1ms | 30 / 89ms | 226MB |
| zstd fastest | 1.65 / 6.8ms | 29.6ms | 38 / 90ms | 158MB |

At saturation compression costs tail latency: archive p99 doubles and add p99
rises by half, while medians improve. The compression runs on the one writer
goroutine, between taking a batch and writing it, and at 5,000 runs/s that
goroutine competes for 8 cores with 6,000 runner goroutines. The fix is where
compression runs, not whether: an add should compress its own records as one
block in the add handler, before queueing them (they are most of the bytes,
and an add already waits ~30ms), and the writer should not compress
transitions at all (a 192-byte state record gains little). Then the commit
path does no compression work. The prototype did not test that split.

### Crash safety of compressed batches

A compressed batch is one outer frame (`[len | 1<<31][crc32c][compressed
inner frames]`), so a torn or corrupt batch ends the good data at the batch
boundary, as a torn record does uncompressed. `internal/gc/gc_test.go` cuts a
compressed log at every byte offset and requires a prefix of whole batches.
`crashtest -batchcodec` repeats the kill -9 test: 10 local and 6 NFS rounds
each for s2 and zstd, with 20 truncated or byte-flipped copies per round.
All 32 rounds passed with no acknowledged write lost, and all 640 damaged
copies recovered to a prefix. A short run confirmed that every frame of such
a log is compressed (3,017 of 3,017), so the test exercised the new path.

## Phase 3: is recovery or memory a problem once the log is compressed?

`recoverbench` builds a D2 log on NFS (5% of jobs also have a running state),
evicts it from the client cache, and recovers it cold and then warm, decoding
every live job into a `jobqueue.Job`. `-heap` keeps the recovered jobs and
reports the heap they hold.

Production shape: the 118,213 live jobs, each with one real production
command (mean 960-byte spec):

| Batch codec | Log size | On disk (`du`) | Cold | Warm | Heap |
| --- | --- | --- | --- | --- | --- |
| none | 109MB | 20MB | 0.75s | 0.33s | 269MB (2.3KB/job) |
| s2 | 12MB | 15MB | 0.55s | 0.33s | 269MB |
| zstd | 8MB | 11MB | 0.38s | 0.38s | 269MB |

Portal worst case: 800,000 live jobs of 10KB real commands:

| Batch codec | Log size | On disk (`du`) | Cold | Warm | Heap |
| --- | --- | --- | --- | --- | --- |
| none | 7,820MB | 871MB | 35.2s | 7.5s | 9.2GB (11.5KB/job) |
| s2 | 375MB | 470MB | 13.2s | 7.7s | 9.2GB |
| zstd | 239MB | 343MB | 13.1s | 8.2s | 9.2GB |
| D3 lazy specs (no compression) | 8,343MB | | 0.51s | 0.09s | (state table only) |

- **At production shape recovery is not a problem.** The whole live set
  recovers in under a second with or without compression, and holds 269MB.
  Lazy specs would save nothing that matters.
- **At the portal worst case, compression cuts cold recovery from 35s to
  13s.** What remains is CPU: one goroutine scans, decompresses and decodes
  840,000 records (warm takes 7.5-8.2s). Read in parallel, the same work is
  much faster: D3's 8-stream reader recovered these 800,000 jobs, specs
  included, in 1.8s warm and 9.5s cold uncompressed. A log written as several
  segments and read in parallel belongs in Phase 1's checkpoint design, not a
  separate phase.
- **Lazy specs (D3) still win on memory at the portal worst case:** 800,000
  jobs of 10KB hold 9.2GB of heap, which lazy specs would replace with about
  150MB of state plus whatever specs are being run. That is the only case
  found for Phase 3, and it depends on how many 10KB jobs are queued at once.

### The NFS filer already compresses

`du` reports far less than a file's size on this mount: the uncompressed
7,820MB log occupies 871MB, so the filer compresses or deduplicates about
9x itself. wr's own compression therefore saves less disk than its ratios
suggest (871MB to 343MB with zstd, not 7,820MB to 239MB), and a compressed
file occupies a little more than its size, at block granularity. It still
cuts what crosses the network 19-28x on the hot path and 21-33x at recovery,
which is where the speed comes from, and any copy to a filesystem that does
not compress (a local-disk primary, D5, or a backup elsewhere) gets the full
saving.

## Conclusions

- **Phase 4, D6 (leases): drop it.** Under a stall it gives exactly what D2
  gives, because runners block on the durable archive, not on the hand-out.
  Its only measured benefit is 71% fewer write batches, which does not change
  latency. It would only help with stalls if completions were also
  acknowledged before they were durable, which moves completion durability
  into the runner and is a different, riskier design.
- **Phase 4, D5 (local-disk primary): keep as an option, not in this spec.**
  It is the only design that rides through a stall unaffected, and costs
  about 300 lines once D2 exists. Whether to accept a replica that lags by a
  stall's worth of writes, and a local disk to provision, is a deployment
  decision for the owner. The spec should keep it cheap to add later: the log
  must be shippable by copying whole new bytes of append-only files, which D2
  already is.
- **Phase 3, D3 (lazy specs): not needed for production; needed only for
  large numbers of large queued jobs.** At production shape (118k live jobs)
  recovery takes 0.4-0.75s and the jobs hold 269MB. At 800k queued 10KB jobs
  they hold 9.2GB and recover in 13s compressed; lazy specs would cut that to
  ~150MB and 0.5s. The spec should keep the log's record format able to
  address a spec (a checkpoint can record each live spec's segment and
  offset) so Phase 3 can come later without a format change, and leave it
  out until the manager's memory at portal scale is a real problem.
- **Compression: add it in Phases 1 and 2.**
  - Phase 1 (the live log): compress each add's records as one s2 block in
    the add handler. On the production-shape hot path this halved add latency
    (60ms to 28ms), cut archive p99 from 15.6ms to 2.4ms, and wrote 19x fewer
    bytes. Leave state records uncompressed so the commit path does no
    compression work (the saturation run shows why).
  - Phase 2 (history segments, checkpoints and backups, written once and
    kept): zstd per 64KB block, so a status query decompresses one block. On
    real completed jobs that is 5.3x (8.4x at 1MB blocks); for live specs
    18-22x.
  - Per-record compression is not worth it: 2-2.6x without a dictionary, and
    a dictionary (6.4x) adds a trained artefact to version and ship.
  - On this mount the filer already compresses about 9x, so the disk saving
    is smaller than the ratios (871MB to 343MB for the 800k log); the speed
    comes from the bytes not sent over NFS.
