# Benchmarks

All prototype code is in `proto/` (its own Go module, ignored by the root
module's `go list`, `make lint` and `make test`). Raw outputs were kept in
the session scratchpad and are summarised here; the raw outputs are in `results/`.

## Environment and method

- Host: 8-core Skylake VM, 91GB RAM, Go 1.27.1. Every run under `nice -n 19`
  with `GOFLAGS=-p=2`.
- Local disk: `/tmp`, ext4 on a VM block device (`/dev/vda3`).
- NFS: `/nfs/hgi/wr/sb10-bigdb/customdb-proto/` (NFSv3, `rsize=wsize=32768`,
  `hard`, `proto=tcp`, the same filesystem as the soak databases).
- Runs were 2026-10-07 22:00 to 2026-10-08 01:00. The farm was quiet: the
  NFS numbers are what this mount gives when it is not loaded, so absolute
  latencies under the daytime load the soaks saw will be higher for every
  design. The comparisons are between designs run back to back in the same
  window, one at a time.
- Designs (all in `proto/internal/store/`):
  - `wal`: D2, one log file, group commit.
  - `keyfiles`: D1 (owner's design, as corrected: 64 key-bucket files).
  - `slots`: D3, spec log + 192-byte slot table + history log.
  - `sqlite`: D4, `modernc.org/sqlite`, WAL, exclusive locking,
    `synchronous=FULL`, one group-commit writer.
  - `boltsmall`, `boltfull`: bbolt with wr's bucket layout, Binc-encoded jobs,
    develop's options (`NoFreelistSync`, map freelist, big initial mmap), and
    ONE group-commit writer for all transitions and archives plus adds in
    their own `Update`. This is kinder to bbolt than develop, whose archive,
    best-effort, add and delete writers queue separately for the write lock.
    `boltsmall` writes a 192-byte state for reserve/start (develop since
    #684), `boltfull` re-encodes the whole job (before #684).
  - D5 is `wal` on local disk with `-ship` copying to NFS; D6 is `wal` with
    `-lease` (no reserve/start writes, one lease write per 1000 reserves).

## 1. Storage primitives (`proto/cmd/fsyncbench`)

Append then fdatasync, one file, 200 iterations:

| Size | Local p50 / p99 | NFS p50 / p99 | NFS MB/s |
| --- | --- | --- | --- |
| 128B | 0.77 / 1.94ms | 0.71 / 1.41ms | |
| 4KB | 0.64 / 4.85ms | 0.71 / 1.55ms | 5 |
| 64KB | 0.92 / 1.78ms | 2.05 / 2.91ms | 31 |
| 1MB | 4.7 / 5.4ms | 6.6 / 7.7ms | 156 |
| 8MB | 28 / 36ms | 30 / 35ms | 269 |

4KB appends from 4/16/64 files at once: local 4,194/10,623/19,346 ops/s;
NFS 4,134/4,731/4,619 ops/s (the NFS server serialises commits around
4,600/s for this client).

Create + write 4KB + fdatasync + rename + fsync directory (the per-key-file
commit of the rejected file-per-job form):

| Parallel | Local p50 / p99, ops/s | NFS p50 / p99, ops/s |
| --- | --- | --- |
| 1 | 1.3 / 2.0ms, 734 | 4.3 / 5.5ms, 155 |
| 16 | 2.6 / 3.4ms, 5,548 | 6.2 / 9.4ms, 1,427 |
| 64 | 5.2 / 11ms, 9,644 | 22 / 79ms, 1,497 |

So an idle NFS fdatasync of a small append costs about 0.7ms, the same as
this VM's local disk, and a log can move 150-270MB/s in large appends. The
per-file commit tops out near 1,500/s on NFS.

## 2. Encode and decode (`proto/internal/flat`, `go test -bench`)

Realistic job (`proto/internal/jobgen`), 1.3KB and 10KB commands:

| Operation | ns/op | allocs/op | B/op |
| --- | --- | --- | --- |
| Binc encode (today), 1.3KB / 10KB | 2,494 / 2,705 | 1 / 1 | 16 |
| Binc decode (today), 1.3KB / 10KB | 6,639 / 11,439 | 13 / 13 | 3,312 / 12,144 |
| flat spec encode, 1.3KB / 10KB | 119 / 205 | 0 | 0 |
| flat spec decode into a reused Job | 163 / 159 | 0 | 0 |
| flat spec decode into a new Job, private copy of the record (recovery) | 5,371 (10KB) | 4 | 10,320 |
| flat state put / get (192 bytes) | 32 / 32 | 0 | 0 |
| Binc run state (develop's `jobRunState`, 381 bytes) | 1,181 | 2 | 32 |

Encoded sizes: Binc 2,154 / 10,854 bytes; flat 1,512 / 10,212 bytes.

## 3. Hot path at production shape (`proto/cmd/hotpath`)

6,000 closed-loop runners started over 30s; each reserves (waits for its
write, counts waits of 10s or more), starts (waits), runs 20-60s, archives
(waits). 120,000 live 10KB jobs preloaded; a 1000-job add every 5s (200
jobs/s, 2MB/s). 180s measured. About 122 runs/s, which is battery10's peak.

NFS:

| Design | reserve p50 / p99 / max | start p50 / p99 | archive p50 / p99 | add (1000) p50 / p99 | preload 120k |
| --- | --- | --- | --- | --- | --- |
| D2 `wal` | 0.80ms / 4.0ms / 57ms | 0.74 / 2.5ms | 0.83 / 12ms | 58 / 74ms | 10s |
| D1 `keyfiles` | 0.81ms / 5.3ms / 76ms | 0.75 / 4.0ms | 0.88 / 8.3ms | 52 / 63ms | 9s |
| D3 `slots` | 0.77ms / 5.1ms / 75ms | 0.73 / 2.7ms | 0.91 / 24ms | 64 / 98ms | 10s |
| D4 `sqlite` | 22ms / 2.1s / 3.0s | 13ms / 176ms | 57ms / 2.8s | 2.5 / 2.9s | 2m17s |
| bbolt small state (develop-like, single writer) | 1.6s / 4.8s / 4.9s | 1.4 / 4.6s | 2.0 / 4.3s | 2.2 / 3.6s | 2m50s |
| bbolt full rewrite (pre-#684, single writer) | 4.4s / 7.8s / 7.8s | 3.5 / 7.8s | 3.4 / 6.9s | 2.7 / 4.0s | 2m48s |
| D6 `wal -lease` | 0.74ms / 2.4ms (lease writes only; hand-outs 0) | none | 0.84 / 16ms | 59 / 110ms | 11s |
| D2 `wal -nosync` | 8us / 1.3s / 2.7s | 4us / 0.29ms | 19us / 6.5ms | 16 / 27ms | 6s |
| bbolt small `-nosync` | 0.25ms / 1.9s / 2.5s | 0.13 / 22ms | 2.4ms / 2.7s | 1.6 / 3.0s | 1m56s |

Throughput was set by the runners (122 runs/s) for every design except
bbolt full rewrite (94 runs/s) and bbolt small (106 runs/s), which could not
keep up. No design had a reservation wait of 10s or more in this window.

Local disk (`/tmp`), same load:

| Design | reserve p50 / p99 / max | archive p50 / p99 | add p50 / p99 |
| --- | --- | --- | --- |
| D2 `wal` | 0.96ms / 3.9ms / 66ms | 0.99 / 8.1ms | 42 / 54ms |
| D3 `slots` | 0.50ms / 5.9ms / 49ms | 0.93 / 10ms | 40 / 52ms |
| bbolt small | 0.81ms / 13ms / 310ms | 1.5 / 220ms | 241 / 306ms |
| bbolt full | 0.91ms / 14ms / 325ms | 1.4 / 218ms | 240 / 304ms |
| D4 `sqlite` | 1.6ms / 276ms / 587ms | 1.9 / 465ms | 509 / 562ms |
| D5 `wal` + ship to NFS | 0.97ms / 3.3ms / 41ms | 1.1 / 4.9ms | 47 / 56ms |

D5's shipper (copy new bytes every 200ms, fdatasync the NFS replica each
second) kept the replica at most 10MB (one add) and 217ms behind, plus up to
1s until its next fdatasync.

### What the bbolt rows say

- On local disk bbolt is fine: the write lock and page rewrites are cheap
  there. On NFS the same single-writer bbolt is 2,000 times slower than a
  log at p50 (1.6s against 0.8ms), and that is with one writer, not
  develop's four.
- It is not fdatasync. With `NoSync`, bbolt on NFS still has reserve p99 1.9s
  and 1.6s adds: a 1000-job add's transaction (10MB of overflow pages plus
  index pages scattered over the file) holds the only write lock while its
  pages are written to NFS, and every transition waits behind it. Preloading
  120k jobs took 1m56s without sync and 2m50s with it, against 6-10s for a
  log writing the same bytes. The likely mechanism is bbolt's write pattern
  on NFS (thousands of scattered page writes per commit, mmap reads of
  pages the client has written) rather than sync; it was not traced further.
- The log is not faster because it skips durability: every acknowledged
  transition was fdatasynced. Its batches are a few KB of sequential bytes,
  and adds do not block transitions (they share batches).
- `wal -nosync` is not smoother than `wal`: without syncs the NFS client
  builds up dirty pages and then throttles a `write()` for up to 2.7s.
  Per-batch fdatasync is what keeps the tail flat on NFS.

## 4. Saturation (`hotpath`, NFS, runners do no work)

6,000 runners with 0-1s jobs (60k preloaded, adds every 5s): every log design
ran out of jobs (1,183 runs/s); bbolt small managed 405 runs/s with reserve
p50 3.8s, SQLite 399 runs/s with p50 4.5s.

| Design | runs/s | reserve p50 / p99 | syncs per batch | mean batch flush |
| --- | --- | --- | --- | --- |
| D2 `wal` | 1,183 (out of jobs) | 1.6 / 8.3ms | 1 | 0.95ms |
| D1 `keyfiles` | 1,183 (out of jobs) | 17 / 32ms | 8.4 | 2.1ms |
| D3 `slots` | 1,183 (out of jobs) | 57 / 93ms | 1.9 | 3.1ms |
| bbolt small | 405 | 3.8 / 7.6s | | 831ms per tx |
| D4 `sqlite` | 399 | 4.5 / 7.2s | | 781ms per tx |

Ceiling run for the log designs (250k preloaded, 0s jobs, 1000-job adds every
second): all three again ran out of jobs, at 5,150 runs/s (15,450 durable
transitions/s plus 1,000 adds/s): `wal` reserve p50 10ms / p99 57ms (one sync
per batch, 11.5ms flushes of ~2MB), `keyfiles` 21 / 89ms (59 syncs per
batch), `slots` 38 / 95ms. That is 40 times battery10's peak.

D1's key layout costs it: each batch fsyncs the bucket files it touched (8 per
batch at 1,183 runs/s, 59 at 5,150), so its latency is twice to ten times
the single log's. D3's in-place slot writes are scattered 192-byte writes,
which on NFS are partial-page writes; they cost more than appends.

## 5. Recovery (`proto/cmd/recoverbench`, NFS)

N live jobs of 10KB (5% of them with a running state), files evicted from the
client cache with `FADV_DONTNEED` for the cold run. Recovery reads every
record and decodes every live job into a new `jobqueue.Job` with the flat
codec, except `slots lazy`, which reads only the slot table.

| Design | 120k cold / warm | 800k cold / warm | bytes read (800k) |
| --- | --- | --- | --- |
| D2 `wal` (one file, one stream) | 5.8s / 1.05s | 37.6s / 7.1s | 8.2GB |
| D1 `keyfiles` (64 files, 8 streams) | 1.9s / 0.53s | 12.4s / 3.0s | 8.2GB |
| D3 `slots` (slot table + pread of specs, 8 streams) | 2.0s / 0.50s | 12.4s / 2.7s | 8.3GB |
| D3 `slots lazy` (slot table only) | 84ms / 21ms | 567ms / 133ms | 154MB |
| D4 `sqlite` | 43.9s / 42.8s | | 1.2GB (120k) |
| bbolt small (prototype; post-crash open with freelist walk, then prefetch) | 55s / 60s (prefetch 6s) | not measured: building it with `NoSync` had written 7.4GB of about 9.8GB after 38 minutes and was stopped | |

The prototype's bbolt store is opened with `NoFreelistSync` and never writes
a freelist, so every open walks the whole B+tree to rebuild it, as develop's
manager does after a crash (develop syncs the freelist only at a clean stop).
That walk is what the bbolt row measures: on local disk the same 120k
recovery took 33.4s cold (2.5s of it the prefetch) and 1.9s warm, so the
decode itself is about 15us per job and the rest is page-at-a-time reads.
Develop's own measurements on NFS: 120k live jobs open and decode in
9.7-14.7s with the prefetch, 13.7s after a crash
(`.docs/bugfixes/260928-db-cold-start-and-freelist.md`); in the runstate
soak, 566k live jobs decoded in 9.9s after a whole-file prefetch of 28s
(warm) to 7m10s (cold, battery10 A2).

Building the 800k stores took 37s (D2), 46s (D1) and 27s (D3) on NFS with
syncs off; bbolt took 2m58s for 120k and had not finished 800k after 38
minutes.

Recovery that reads every live job's spec is bounded by NFS read bandwidth
for any design: 8GB is 12-38s depending on parallelism. Only D3 with lazy
specs (or a local-disk primary, D5) avoids that.

## 6. Crash tests (`proto/cmd/crashtest`)

Each round: a child process runs 300 writers taking 3,000 jobs through
reserve, start and archive in a loop, appending (key, sequence) to an ack file
only after each write is reported durable; the parent sends SIGKILL to that
child's exact pid after 1.5-4.5s, recovers the store, and requires every
acknowledged sequence to be present. It then makes 20 copies of the main log,
truncating 10 at random offsets and flipping a byte at a random offset in the
other 10, and requires recovery to succeed with no more records than the
intact log.

| Design | Local rounds | NFS rounds | Lost acknowledged writes | Torn tails seen and cut | Truncation/flip failures |
| --- | --- | --- | --- | --- | --- |
| D2 `wal` | 10 | 6 | 0 | 1 (190 bytes) | 0 / 320 |
| D1 `keyfiles` | 10 | 6 | 0 | 1 (208 bytes) | 0 / 320 |
| D3 `slots` | 10 + 10 after a fix | 6 + 6 | 0 | 1 (136 bytes) | 0 / 640 |
| D4 `sqlite` | 10 | 6 | 0 | | (not applicable) |

These test process crashes (what the soaks inject). A host crash, where the
page cache is lost, was not simulated beyond the truncations and flips; the
design docs say what each design needs for it (CRC framing for the logs, A/B
slots for D3).

## 7. Files and inodes

| Design | 1M jobs | 5M jobs | 10M jobs |
| --- | --- | --- | --- |
| File per job (rejected) | 1-2M + 65,536 dirs | 5-10M | 10-20M |
| D1 key buckets (64, sealed 64MB segments) | 70-90 | 170-850 | 270-1,650 |
| D2 log + snapshots + 256MB history segments | 15-60 | 30-200 | 60-400 |
| D3 slots + spec/history segments | 10-50 | 30-200 | 60-400 |
| D4 SQLite | 2-3 | 2-3 | 2-3 |
| bbolt | 1 (+ backup, + temp) | same | same |
| D5 | D2 x 2 | | |
| D6 | as its storage | | |

Ranges are 1.3KB jobs (production) to 10KB jobs (portal).
