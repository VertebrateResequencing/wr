# Idea 5 — Storage overhaul: split hot (small, local) from cold (history)

**Type:** storage-engine / data-layout overhaul (largest change, biggest scale
payoff).
**Goal:** attack the substrate. Every measured cost is really "a large,
ever-growing BoltDB on NFS": `initDB` freelist load grows with size, seeding does
cold random `Get`s across a 1.9 M-entry mmap, and single-writer commits slow as
the file grows. Make the operational store **small, local, and bounded** so those
costs cannot arise.

## The problem it targets

- `testing.md` S4: `initDB` of the 6.2 GB DB = up to 12.6 s (freelist).
- `testing.md` S3: seeding = cold random reads into the **1.9 M completed-job**
  bucket (190 s / 162 s).
- `testing.md` S5: archive throughput decays as the completed bucket grows.

All three are consequences of keeping **1.9 M completed jobs + 3.5 M lookup
entries in the same DB the manager must open and traverse to operate**. The
operational working set (incomplete jobs) is tiny (46–thousands); the history is
what's huge and only the web UI wants it.

## Design

Separate hot from cold:

- **Hot store (operational):** a small DB containing only *incomplete* jobs +
  the maintained per-repgroup counters (Idea 3). Kept **on local disk** (or
  tmpfs) — not NFS — because it's small and rewritten constantly. Startup opens
  only this: `initDB` is instant, recovery decodes only incomplete jobs, no
  history to seed. This is the store the running-of-jobs depends on.
- **Cold store (history):** completed jobs move to a separate, append-mostly
  store (a second BoltDB, rotated files, or a columnar/log store) that the
  operational paths **never read**. Only the web UI / `wr status --recent` /
  audit reads it, lazily. It can live on NFS; its size never affects operations.
- Archiving a job = move from hot to cold + bump the hot counter (both cheap).
- **Durability:** the hot store still fsyncs job-start/complete as today; the
  cold store is written append-only and can tolerate a looser sync (it's history,
  re-derivable from the hot store's transitions if needed).

Alternatives within this idea (evaluate during the trial):
- Replace BoltDB with **SQLite (WAL)** for the hot store (better concurrent
  read/write, indexed count queries, mature on NFS-avoidance guidance).
- Keep BoltDB but add **online compaction** + `FreelistType: map` to bound the
  freelist/size of a single DB (a smaller, simpler step that addresses S4/S5 but
  not the fundamental "history in the hot path").

## Priority alignment

The store the manager needs to run jobs is always small and local, so
startup/add/archive costs are bounded and independent of how much history has
accumulated. History (what the web UI wants) is physically separate and its size
can never affect operations — the brief's guarantee, achieved at the storage
layer.

## Trade-offs / risks

- Largest change: two stores, a migration for the existing 6.2 GB DB, and every
  code path that currently reads completed jobs from the one DB must be routed to
  the cold store.
- Local hot store means the manager is tied to a node's local disk for its
  working state (need a story for manager relocation / backup — the cold store +
  transition log can reconstruct).
- If SQLite: a new dependency and a rewrite of the persistence layer.
- Migration must be a one-time offline split of the current DB.

## Trial checklist (prove it works)

- [ ] **Baseline.** `testing.md` S3/S4/S5 on the real DB (190 s add, 162 s
  restart, 12.6 s local initDB, archive decay).
- [ ] **Measure the split's promise cheaply first.** Build a **hot-only** DB
  containing just the incomplete jobs + counters (drop the complete bucket into a
  separate file). Start the manager on it (`WR_HOTDB=/local/path`): assert
  `initDB` < 100 ms and restart < 1 s even though the *history* (separate file)
  is 1.9 M jobs. This validates the core hypothesis before building the full
  two-store machinery.
- [ ] **Spike archive routing.** Behind `WR_COLDSTORE=1`, on archive write the
  job to the cold store + bump the hot counter (Idea 3), and remove it from hot.
  Ensure no operational path reads the cold store.
- [ ] **Kill the costs.** Re-run S3: add < 50 ms, restart ~1 s. Re-run S4:
  `initDB` bounded regardless of history. Re-run S5: no archive decay as history
  grows (history goes to the append-only cold store).
- [ ] **`wr status` still works.** `wr status`/`--recent`/repgroup summaries read
  the cold store lazily; assert correct results and that a slow cold-store read
  never blocks the manager (run a job storm during a big `wr status` and assert
  throughput unaffected).
- [ ] **Migration.** Offline tool splits the existing 6.2 GB DB into hot+cold;
  assert resulting counts == a full recompute == original.
- [ ] **(If SQLite variant)** re-implement the hot store on SQLite(WAL); re-run
  S1/S3/S4/S5; compare against the BoltDB hot store on the same hardware.
- [ ] **No-regression + durability.** `make test`, `make race`; crash-recovery
  test passes (kill -9 mid-churn → hot store consistent; cold store re-derivable);
  document the backup/relocation story for the local hot store.

## Coverage of the full test set (see testing.md acceptance criteria)

| id | criterion | how Idea 5 covers it |
|---|---|---|
| B | startup responsive | **core** — hot store is small/local; no history to open or scan |
| C | false-lost consequence stays fixed | preserved |
| D | removed-on-refresh stays fixed | preserved (seed logic unchanged; counts from the hot store) |
| E | false-lost CAUSE | **DONE in baseline (F0, a19d390); idea must not regress it** |
| F | status responsive under load | faster DB helps; needs F0/1f for the bound |
| G | throughput not regressed | improved (small hot store, less commit cost) |

**Honest scope:** F0 (a19d390) already fixes E; Idea 5 attacks the storage
substrate (**B**) and lightens F/G. Status-vs-runner isolation (F) still needs
Idea 1f/4. Every trial re-runs the `harness/` F0 test to confirm no regression.

- [ ] **Full acceptance set (testing.md).** With Idea 5 + F0: the 3 Go tests all
  PASS; `exp_startup_ab.sh`/`exp_realdb_seed.sh` `initDB` + restart bounded
  regardless of history; `exp_reconnect.sh` bounded; `exp1.sh`/`exp_drive_ab.sh`
  ≥ v0.36.5 (expect better). Record numbers.

## Trial results (spike on F0 baseline)

Offline `split` tool + measurement on a synthetic **150,000-complete + 5,001-incomplete**
DB (331 MB). Hot-only DB (dropped `jobscomplete`+`endTimeToKey`, pruned lookups to
the incomplete set) = **12 MB (28× smaller)**, built in <1 s.

| metric (warm, local) | full DB (331 MB) | hot-only (12 MB) |
|---|---|---|
| `initDB` (freelist) | 0–1 ms | 0 ms |
| seedStatusStateForItemDefs | 180–192 ms | 0–1 ms |
| time-to-responsive | 0.32 s | 0.27 s |

**The full-vs-hot startup delta is almost entirely seeding** — which **Idea 3
already removes**. Idea 5's *unique* lever is `initDB` (freelist load), which was
**0 ms at this scale**: freelist time scales with **free-page count
(fragmentation)**, not file size — the synthetic single-pass build had only 1847
free pages, so it can't reproduce S4's 12.6 s (that comes from 1.9 M jobs churned
over months). initDB cost must be extrapolated from S4 (**~1.0 s NFS / 12.6 s
cold-local** on the real 6.2 GB DB). S5 archive decay reproduced (7047→6533/s as
complete grew; PING p99 ~5 ms throughout — never blocked responsiveness); it's the
history sitting in the operational b-tree, inherent BoltDB growth (HEAD ≥ v0.36.5).

**Marginal value over Idea 2+3:** after 2 (respond first) + 3 (no seeding scan),
time-to-responsive ≈ `initDB`. Idea 5's only unique startup contribution is
bounding that `initDB` floor (~1 s NFS at real scale) plus stopping archive decay
and bounding operational DB size. After 2+3+F0 the **reliability** failures
(162–190 s seeding, false-lost) are gone; what remains is **efficiency**, not the
reported symptoms.

**Complexity:** full BoltDB two-store is **large/multi-week, high blast radius** —
the hard parts are the archive hot path (`dropStaleEndTimeIndex` reads
`jobscomplete`) and dependency/duplicate-check reads of completed history; ~8
reporting read-sites reroute easily; local hot store needs a new backup story;
SQLite variant = rewrite of the ~2700-line bbolt-coupled `db.go`.

**F0 regression:** 4/4 deterministic tests PASS.

**Verdict:** **future / extreme-scale option, NOT warranted now.** Revisit only if,
after 2+3+F0 land, the real DB still shows a biting `initDB` or archive decay on
NFS. **Smallest useful subset = Fix 1d** (`FreelistType: FreelistMapType` on the 5
`bolt.Open` calls + periodic `bolt.Compact`) — captures most of the S4/S5 benefit
at ~a day's work / near-zero risk, without touching archive/dependency/status read
paths. (Validate map-freelist against `.tmp/db`, not synthetic scale — the benefit
only shows at the real DB's fragmentation.)
