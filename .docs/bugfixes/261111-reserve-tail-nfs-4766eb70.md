# Bugfix: reservation wait tail on NFS under daytime soak load

- Branch: customdb-f397b547
- Base: customdb-f397b547 at f1bcc1d7
- Queue owner: customdb-f397b547, .docs/delivery-queue.md row 20
  (phase 1 gate item 1g.5, .docs/customdb/phases/phase1-gate.md)

## Items

- [ ] 1g.5 (daytime NFS targets) fails the stall-free soak criterion: every
  `"reservation wait"` line must have `p99` at most 50ms and `abandoned=0`,
  with no averaging. In the 1h NFS soak (10:00 to 11:02 GMT on 2026-11-11,
  HEAD f7006373, peak LSF RUN 3,121, 280,956 reservations) `abandoned=0`
  held but 31 of 61 lines exceed 50ms. The worst, 427.8ms, is in the first
  minute (one 1.94s wal fsync at start-up). From about 10:19 most lines are
  51-128ms, growing over the hour; portal-burst minutes are 172-277ms (each
  burst writes 35-50MB of wal in a minute). p50 0.9-3.3ms.
  - Source: phase 1 gate item 1g.5
    (.docs/customdb/results/phase1-gate-1g5.md)
  - Root cause: two writers on the store's NFS client hold up the wal's
    syncs, which a reservation waits for (a reservation's wait is about
    two wal syncs, its batch's and the one in flight). Measured with
    load-matched soaks on NFS, back to back on 2026-11-11, each the gate
    soak's shape for 45 minutes, from a diagnostic build that traces every
    wal batch sync, fold transaction and history copy (details, tables and
    raw outputs in the results doc's "Fix" section):
    1. **Growth over the hour: the incremental backup's history.bolt
       copy.** Every backup tick (30s) whose folded position had moved,
       which is every tick, copied `history.bolt` whole (spec E4, Phase 1
       form), with writeback forced only every 8MiB. `history.bolt` grows
       about 17-35MB a minute in the soak (1.3GB at 33 minutes, 2.3GB at
       45), so the copy ran almost without a pause by the second half hour
       and its 8MiB writeback slices sat ahead of the wal's syncs. A/B: arm
       B1 (normal) against arm A1 (identical, but `history.bolt` copied
       only on the first tick): at 4,600 to 6,600 reservations a minute,
       the lines without a portal burst had p99 34-49ms in B1 and 9-25ms
       in A1. The lines with a burst failed in both.
    2. **Bursts: the folder's large, back-to-back commits.** A portal
       burst adds 13,000 to 33,000 jobs twice over in 1,000-job adds,
       each one block, and the folder then folds their rglookup records
       in 4,096-record chunks back to back for 30-40s. Each chunk's
       `history.bolt` transaction wrote 10-15MB in about 2,600 runs of
       pages and held 0.6-1.6s, and the wal syncs in flight during its
       commit took 100-150ms: in A1 every wal sync over 50ms in a burst
       minute fell inside a fold transaction (18 of 18, 24 of 24; over
       whole runs 247 of 267 in B1 and 136 of 143 in A1, against a fold
       duty of 0.30). The steady folds (one a second, about 750 runs)
       gave no slow syncs. Further arms (F2, F3, F4, F6 in the results
       doc) narrowed it: pacing the chunks alone (F2), or with smaller
       chunks (F3, F4), still failed 2-3 burst lines, because a chunk
       never splits a block, so its commit stays a whole 1,000-job add's
       rglookups (7-9MB, 60-115ms per sync); putting a fold's rglookups in
       transactions of at most 256 (about 2MB, 40ms each), unpaced (F6),
       left 1 line failing (53.5ms), in a minute of 231 such transactions
       nearly back to back. The first full gate soak of a fixed build
       (G1, 2026-11-12) showed the same for completions: in its last three
       minutes, at 6,600 reservations a minute and no burst, the fold's
       chunks of several hundred completions each held their transaction
       1.6-3.7s (a completion writes its job, end-time index, rep group
       end time, seq and samples at random places in a 1-2GB
       `history.bolt`, about 3-4ms each), with wal syncs up to 341ms, and
       three lines failed (51.4-57.7ms); the last of them (51.4ms, partial)
       also held the soak's last checkpoint (59.6MB), so it is not the
       fold's alone.
    3. **The first minute's 1.94s fsync** is not wr's: arm A1 had the same
       thing mid-run (13:00:11-13:00:37), when every wal sync and every
       fold commit took 0.5-2.2s together for 26s, with nothing of wr's
       out of the ordinary. That is the NFS service stalling, and it fails
       the line it falls in (A1's 13:00 line, p99 103.6ms). See "Open" in
       the results doc.
    The checkpoint (1d.2) was not a cause of the 1g.5 run's failure: its
    write minutes coincide with bursts in the gate soak, and minutes with
    only a checkpoint passed in 1g.5's checkpoint soak and in A1. Once the
    other causes were fixed it is the likely cause of G6's one failing
    line; see the checkpoint item below.
  - Red command (local disk, HEAD code with the new tagged test; it takes
    about 90s, most of it building a history of 300,000 completed jobs):
    `(unset $(compgen -v | grep '^OS_'); HOME=<scratch> CGO_ENABLED=1 go test -tags 'netgo reliability_repro' --count 1 ./jobqueue -run '^TestReserveWaitDuringBackup$' -v -timeout 30m)`, exit 1:

    ```text
    reserve_backup_tail_repro_test.go:111: reservations 4400: p50 1.644292ms, p99 73.878409ms, max 123.06215ms
    Line 114:
    Expected '73.878409ms' to be less than or equal to '50ms' (but it wasn't)!
    --- FAIL: TestReserveWaitDuringBackup (89.32s)
    ```

    The test opens a production log store whose `history.bolt` holds
    300,000 completed jobs (about 840MB), with backup ticks every 10s, and
    makes 110 archives and 110 reservation records a second for 40s,
    timing each reservation record's durability wait. Without backups the
    same run gave p99 4.3-5.4ms (4 interleaved runs, local disk), so the
    copy alone adds the tail even on local disk. It reproduces cause 1,
    not cause 2: on local disk the folder made no difference (fold on and
    off gave the same p99), so the burst cause only shows on NFS, in the
    soak.
  - Approach (TDD; each new test was red against the code before it):
    - Backup (cause 1): a tick copies `history.bolt` again only when the
      backup's copy is at most `historyRecopyFloor` (64MiB) or the wal
      bytes shipped since that copy are at least its size (`historyDue`,
      `incBackup.histBytes`/`walSinceHist`). Meanwhile the backup keeps its
      checkpoint and the wal from its copy's folded position, which restore
      folds again; a newer checkpoint kept back for that reason is not
      warned about (`keepCheckpoint`). A store's first tick, and a backup
      started again, still copy it. So the history bytes a backup ships are
      at most the wal bytes it ships, not `history.bolt`'s size every 30s.
    - Backup pacing: `backupCopySyncInterval` 8MiB to 1MiB, so a copy that
      does run (the first after a restart, a due re-copy, a checkpoint, a
      large wal part) holds a concurrent sync behind at most about 20ms of
      writeback on NFS. An 842MB copy took 1.86s against 1.82s at 8MiB
      (local disk); the soak's 571MB re-copy in F2 took 1.7s and its minute
      line was 8.2ms.
    - Folder (cause 2, and the completions G1 found): `historyBolt.fold`
      puts a fold's net rglookups 64 at a time (`foldLookupBatch`,
      `putHistoryLookups`), then records its completions
      one at a time, at least 16 a transaction (`recordCompletions`,
      `foldTxMinCompletions`), each in key order, and once its
      transaction's estimated size reaches its bound commits it and makes
      the rest in further transactions (`foldSomeTx`, `putSome`); the first
      also makes the chunk's rglookup drops, and the last commits the
      samples, rep groups ever added and folded position, with the highest
      seq of all the chunk's completions (`foldTx`).
      - The bound (`foldTxLimit`, passed by `foldNow`/`foldChunk` through
        `foldHistoryUpTo`) is `foldTxBytes` (2MB) while the fold keeps up,
        and that times lag/`foldLagStep` (60s) past that, lag being how long
        the oldest record being folded may have waited since it was durable
        (since the start of the last fold that reached its target,
        `logStore.foldLagFrom`); so a fold that falls behind makes bigger,
        cheaper-per-record transactions, as `f1bcc1d7`'s unsplit chunks
        were (review round 3's B1). It is never below the bytes each commit
        writes for history.bolt's freelist (`freelistWrite`), so the
        freelist cannot outweigh a transaction's own records (B2).
      - The estimate (`foldTxSize`) is a page per node the transaction
        changed (bbolt's node count) plus the bytes of keys and values it
        put, scaled by `historyBolt.writeScale`: the bytes of pages the
        last fold transaction wrote (bbolt's `PageAlloc`, read in
        `tx.OnCommit`), less its freelist write, per byte estimated
        (`noteWrite`), 1 until the first commits after an open; unclamped
        and unsmoothed (round 3's half-way smoothing was dropped in round
        4, when no test could tell it from its absence).
      - The fold writer's `"db writer"` line logs `maxLag`, the longest lag
        of the interval (`writerStats.observeLag`). It is an upper bound: it
        counts from the previous fold's start, so it can read up to about
        twice the oldest record's real age when the folder trails. After an
        open the lag starts at 0 (the first fold sets `foldLagFrom`), which
        errs towards the small bound.
      - Those puts, drops and completions are idempotent (`completeSeq`
        makes a completion recorded again a no-op) and only add to or take
        from what the overlay holds, so a crash between them leaves every
        history query's answer as it was (but for learned-requirement
        samples, which go with their completions) and the refold makes the
        rest. No wait between transactions; `foldChunkRecords` stays
        4,096.
      - History: the first final fix (256 rglookups a transaction plus a
        capped wait) regressed the benchmarks (review round 1); the bound
        covered rglookups alone in G1, which failed 3 lines on chunks of
        completions; G2 and G3 measured by node count alone, which review
        round 2 (F2) showed under-measures values larger than a page; G4's
        scaled estimate held the bound but its fixed 2MB split capped the
        folder's capacity near G4's load, and a large freelist collapsed it
        (review round 3); this round's lag growth and freelist handling
        answer those.
    - Recovery (review round 2's F1): replay's `applyComplete` returned
      early, without clearing a live job's std, for a complete record at
      or below its key's seq in the history index, assuming a later
      complete record had applied in memory. A split fold breaks that:
      recovery loads the index from `completeSeq`, which an earlier fold
      transaction can have committed past the folded position, so a
      kept-live (rerun) job's old std came back after a crash or restore
      between the transactions, and every checkpoint failed
      (`errCheckpointRefsUnswapped`) until the next restart. Now that case
      clears std as the folded-position case does (`completeOutranked`);
      `clearStd` is the same whichever order complete records apply in.
      The other recovery paths that read the index or history.bolt were
      checked: `applyDelete`'s unindex, `dropFoldedLive` (relies on that
      clearStd), `checkIfComplete`, rglookups put early and drops made
      early (queries skip by key; `unlooked` still drops nothing extra),
      rep groups (last transaction only) and checkpoint Ref swapping
      (the regression test checkpoints each image); samples alone differ,
      see Incidentals.
    - Spec: E4 tick steps 2 and 4, the paced-copy paragraph, the Folder
      section, and the crash soak's caveat.
  - Files: jobqueue/store_backup_tick.go, jobqueue/store_history_bolt.go,
    jobqueue/store_records.go (`applyComplete`, `completeOutranked`),
    jobqueue/store_fold.go (`foldNow`, `foldChunk`, `foldTxLimit`),
    jobqueue/store_log.go (`foldLagFrom`), jobqueue/store_stats.go
    (`maxLag`), jobqueue/store_stats_test.go,
    jobqueue/db.go (`backupCopySyncInterval`),
    jobqueue/store_backup_history_copy_test.go (new),
    jobqueue/store_fold_lookups_test.go (new),
    jobqueue/store_inspect_test.go (`boltOnlyTests`),
    jobqueue/reserve_backup_tail_repro_test.go (new, tag
    `reliability_repro`), .docs/customdb/spec.md,
    .docs/customdb/results/phase1-gate-1g5.md and its `fix-*` files.
  - Tests:
    - `TestLogStoreIncrementalBackupDefersHistory`: with the floor at 1
      byte, ticks after folds copy `history.bolt` again only once the wal
      shipped since the last copy reaches that copy's size, twice over;
      meanwhile no tick warns, the backup holds no newer checkpoint, and it
      restores with every job held and every archived one complete; with
      the floor at the copy's size, the next tick after a fold copies it.
    - `TestLogStoreIncrementalBackupPacesHistoryEveryMiB`: with the default
      pace, a tick's copy of a `history.bolt` over 3MiB forces writeback at
      least once a MiB.
    - `TestStoreFoldLookupsInBoundedTransactions`: an unfolded 1,000-job
      add folds in 1 transaction with the default bound; with the bound at
      1 byte, in 16 (64 rglookups each), the "db writer" fold figures
      counting its 1,000 records once, and history.bolt holds its 1,000
      rglookups once; a second 1,000-job add folded with the bound at 32
      pages takes more than 1 and fewer than 16 transactions (split by
      pages dirtied, not per batch); with the bound at 1 byte and the 4th
      transaction failing, 192 rglookups are held, the folded position
      and history.bolt's rep groups are unchanged, and the next fold holds
      1,000 and rep group a; a modify of the 1,000 into rep group b, keys
      unchanged, folded with the bound at 1 byte, leaves 1,000 rglookups
      under b and none under a.
    - `TestStoreFoldCompletionsInBoundedTransactions`: 100 archived jobs,
      unfolded, fold in 1 transaction with the default bound; with the
      bound at 1 byte, in 7 (16 completions each but the last), the "db
      writer" figures counting the 100 records once, history.bolt holding
      each and the highest seq; with the 3rd transaction failing, 32 are
      held, the
      folded position and highest seq are unchanged, all 100 are returned
      by `retrieveCompleteJobsByRepGroup`, and the next fold holds each
      once with the highest seq. Red against the rglookup-only bound (G1's
      code): 1 transaction.
    - `TestStoreFoldTransactionsWriteAboutTheBound`: for commands of 100
      bytes, 8KiB and 40KiB, 1,000 archived jobs folded, then 1 more, then
      1,000 more folded with the bound at 2MiB: more than 1 transaction,
      each writing (history.bolt's `PageAlloc` between transactions, less
      its freelist write) at most 1.5 times the bound, the first (on the
      last fold's scale) at most twice, and all but the last at least a
      third of it. Red with either half of G3's measure missing from the
      estimate: without the key and value bytes, or without the scale,
      transactions wrote over 1.5 times the bound (mutants below).
    - `TestStoreFoldSplitCrashImages`: 40 jobs, the first buried with std
      then archived and kept live to rerun, the rest archived, folded with
      the bound at 1 byte (3 transactions: 16, 16 and 8 completions, by the
      16-completion floor; under round 3's code it was 40, one each, not
      the 3 its description said), the store imaged before each of the
      first 2; and 1,000 jobs added then 100 modified into another rep
      group, imaged before each transaction. Each image, opened, answers
      (rep groups, complete jobs, live, complete, std, complete records) as
      the first, checkpoints, folds, answers as the store and holds its
      samples, and checkpoints again. Red without the F1 fix: the kept-live
      job's std comes back (`stdo="out" stde="err"` against `""`).
    - `TestStoreFoldTxLimit`: the bound is `foldTxBytes` to a lag of
      `foldLagStep`, then that times lag/`foldLagStep`; it does not grow
      at a portal burst's lag of 55s, and has doubled by 2 minutes.
    - `TestStoreFoldBacklogCapacity`: 2,000 completions of 8KiB-command
      jobs unfolded; with the bound off, 1 transaction; with records that
      have waited 20 `foldLagStep`s (`foldLagFrom` 20 minutes back), at
      most 2 transactions writing at most 1.25 times as much, and `maxLag`
      at least that; with
      records just written, more than 4; and once the lagged fold reached
      its target, the next fold's `maxLag` is under `foldLagStep`. Red
      against the fixed bound (G4's code, as mutant `nolaggrow`). Its
      second Convey (round 5): 200 jobs folded with the bound at 1 byte and
      each transaction taking 100ms (about 0.4s in all), then 10 more
      folded: the second fold's `maxLag` is at least the first fold's
      duration, as its records may have been written as the first began.
      Red on review round 4's mutant MA (lag counted from a fold's end):
      `Expected '1399393' to be greater than or equal to '409946911'`
      (nanoseconds: 1.4ms against 409.9ms).
    - `TestStoreFoldLargeFreelist`: history.bolt with a freelist of about
      320KiB (over 4 times a 64KiB bound), 2,000 completions unfolded: with
      the bound, freelists included, at most 3.5 times the bytes of a fold
      with it off, every transaction but the last writing at least half the
      freelist of its own. Red against G4's code (freelist in the scale,
      no freelist floor; as mutants `flinscale`, `noflfloor`).
    - `TestStoreStatsReport` (extended): a writer's line logs the longest
      lag it was told of as `maxLag`, and a writer told of none logs none;
      and (round 5) a lag told of in an interval with no transaction is
      logged with the next transaction's line (red on review round 4's
      mutant MC, which dropped `take`'s restore of `maxLag`).
    - `TestReserveWaitDuringBackup` (tagged): the red above.
  - Mutants of the final code (each in its own plain rsync copy under the
    scratchpad, each compiles; run on the targeted tests above):

    | Mutant | Killed by |
    | --- | --- |
    | `historyDue` always true (f1bcc1d7's every-tick copy) | DefersHistory, Line 102 (copy at tick 0) |
    | no floor (`histBytes <= floor` dropped) | DefersHistory, Line 164 (no copy under the floor) |
    | wal bytes not counted in `shipPart` | DefersHistory, Line 111 (never copied) |
    | count not reset on a copy | DefersHistory, Line 102 (second copy too soon) |
    | warns about a kept checkpoint while deferred | DefersHistory, Line 258 (warn line) |
    | ships a newer checkpoint while deferred | DefersHistory, Line 259 (checkpoint 2 shipped) |
    | copy pace back to 8MiB | PacesHistoryEveryMiB, Line 229 (1 pace, want 7) |
    | rglookups never split | FoldLookupsInBoundedTransactions, Lines 75, 92, 114 |
    | completions never split (G1's code) | FoldCompletionsInBoundedTransactions, Lines 188, 214; FoldTransactionsWriteAboutTheBound, Line 282 |
    | an earlier transaction's records counted again in the last's "db writer" figures (review mutant E) | FoldLookupsInBoundedTransactions, Line 76; FoldCompletionsInBoundedTransactions, Line 189 |
    | highest seq not taken from the chunk's completions | FoldCompletionsInBoundedTransactions, Lines 191, 230 |
    | drops made in every transaction | FoldLookupsInBoundedTransactions, Line 148; FoldBatches (existing), Line 1347 |
    | rep groups put in the first transaction | FoldLookupsInBoundedTransactions, Line 120 |
    | `logFoldTxHook` not called in earlier transactions | FoldLookupsInBoundedTransactions, Line 114; FoldCompletionsInBoundedTransactions, Line 214 |
    | the estimate's scale ignored | FoldTransactionsWriteAboutTheBound, Line 288 |
    | the estimate's key and value bytes ignored | FoldTransactionsWriteAboutTheBound, Lines 286, 292; FoldBacklogCapacity, Line 408 |
    | the scale never noted (`OnCommit` dropped) | FoldTransactionsWriteAboutTheBound, Line 288 |
    | recovery's outranked complete record not clearing std (F1) | FoldSplitCrashImages, Line 665 |
    | it clearing std for seq 0 | FoldSplitCrashImages, Line 665 |
    | the bound not growing with lag (G4's code) | FoldTxLimit, Line 354; FoldBacklogCapacity, Line 406 |
    | `foldLagStep` 2 minutes, or 5s (G5's) | FoldTxLimit, Lines 361, 360 |
    | lag not reset when a fold reaches its target | FoldBacklogCapacity, Line 416 |
    | lag not observed | FoldBacklogCapacity, Line 400 |
    | the freelist write counted in the scale (G4's code) | FoldLargeFreelist, Line 466 |
    | no freelist floor on the bound | FoldLargeFreelist, Line 466 |
    | no 16-completion floor | FoldCompletionsInBoundedTransactions, Lines 188, 215 |
    | `maxLag` not logged | StoreStatsReport, Line 372 |
    | lag counted from a fold's end, not its start (review round 4's MA) | FoldBacklogCapacity, Line 437 (round 5) |
    | `take` not restoring `maxLag` in an interval with no transaction (review round 4's MC) | StoreStatsReport, Line 390 (round 5) |
    | a recorded completion's samples not put | FoldLearned (existing), Line 436 (round 2's run) |

    The backup rows were run on the first final fix, whose backup files and
    tests are unchanged in the final code; the fold, lag, freelist and recovery
    rows on the final code, 2026-11-13 11:59-16:40
    (`g5fix-log/mutants-round4.txt`, `mutants-round4-regress.txt` and
    `mutants-round4-final.txt` in the scratchpad; line numbers as of each run:
    all but the `foldLagStep` rows' predate `TestStoreFoldTxLimit`'s second
    Convey, which moved later lines down by 7). Review round 1's mutants A and B
    (always waiting the cap; also waiting after a fold's last transaction)
    mutated the wait, which the final code no longer has. Three candidate
    refinements were tried and dropped as no test could tell them from their
    absence: a clamp on the scale (0.25 to 64), skipping transactions estimated
    at under a quarter of the bound, and moving the scale half way to each new
    figure (the last survived its mutant in round 4's regression run). Mutants
    that did not compile were rewritten until they did. One survives, as an
    equivalent mutant: not resetting `histDeferred` at the start of
    `shipHistory`. A tick holds its checkpoint before the history step, so after
    a tick that copies, the held checkpoint's `histFrom` is never past the
    copy's folded position, and the warn the stale flag could hide is reached
    only by a store's first tick, when nothing has been deferred. A test of a
    fold and a checkpoint during a copy after a deferral (written, then removed)
    logged no warn either way. The first `historyDue` mutant written did not
    compile (an unused variable) and was rewritten.
  - Soak evidence (results doc's "Gate soaks, 2026-11-12"; each the gate
    soak's settings, 1h, NFS, no diagnostic build):
    - G1 (10:00-11:01; backup fix + rglookups bounded; LSF RUN 3,000 from
      10:47, peak 3,103; 273,559 reservations): 3 of 61 lines over 50ms
      (57.7, 53.5 and 51.4ms, the last three minutes, no burst), every
      burst line under 30ms, 0 abandoned. The fold's transactions held
      1.6s on average, up to 3.7s, folding chunks of completions, with
      wal syncs up to 341ms: so completions were bounded too. The last,
      partial line (51.4ms) also held the last checkpoint (59.6MB in 6.5s).
    - K1 (11:02-11:48, 45 minutes; backup fix alone): void as an A/B, the
      farm gave it at most 1,686 runners; 1 of 46 lines over (64.0ms,
      its first burst line).
    - G2 (11:49-12:50; the round 2 code, node-count bound; 3,000 runners
      only from 12:42): 0 of 61 over, worst 27.8ms, 0 abandoned;
      under-loaded.
    - G3 (12:54-13:53; as G2; 3,000 runners from 13:29, peak
      3,228; 280,553 reservations): 0 of 61 over, worst 36.7ms (a burst
      line), 0 abandoned, 0 not durable; fold transactions at most
      0.87s. The criterion held on that code; G4 below is the final
      code's soak.
    - `TestAddLatency1000` 3 times after each of G1, G2 and G3: p99
      53.5-66.1ms, PASS each time.
  - Gates on the final code (round 4, 60s `foldLagStep`; scratch `HOME`,
    every `OS_` variable unset, 2026-11-13): `make lint` 0 issues
    (14:59-15:00); `TestReserveWaitDuringBackup` p99 8.0ms; `make test`
    15:02-15:08, `WR_TEST_STORE=all make test` 15:08-15:13 and `make race`
    15:13-15:21 passed (1,295 tests each). The 5s-step draft passed the
    same at 12:24-12:48, round 3's code on 2026-11-12 20:16-20:39.
  - Speed (results doc's "Speed" and "Round 3"), `speed.sh quick`, the
    four `*UnderReserveLoad` benchmarks against f1bcc1d7: the G3 code, 12
    interleaved rounds on a quiet host by review round 2 (17:07-19:21),
    PASS with nothing significant (Suspend reserve-p50 -10.8%, p=0.16), so
    the 14:30 run's Suspend FAIL (+40.7%, p=0.007; a 10-round rerun at
    16:12 passed) was noise; the final code, 8 rounds (20:44-22:40), PASS,
    nothing significant. Fold time, final code against f1bcc1d7 or the
    bound off (local disk): review's probe 0.84-0.90s against 1.08-1.13s
    (+27%); a large chunk into a large history.bolt up to about 2.2 times
    (4,000 completions into 1,000,000 held: 0.49-0.55s against
    1.07-1.17s). Bytes written per transaction at the 2MB bound: 2.1-2.5MB
    with 100-byte commands, and after a fold's first transaction 1.8-2.6MB
    with 8KB and 1.7-2.8MB with 40KB (first: 4.3MB and 5.3MB).
  - Speed and fold time of the final code (round 4; results doc's "Round
    4"): the four `*UnderReserveLoad` benchmarks against f1bcc1d7, 8
    interleaved rounds (2026-11-13 17:39-19:12), PASS with nothing
    significant. Fold time, keeping up (local disk): review's probe
    0.88-0.90s against 1.07-1.09s; 4,000 completions into 1,000,000 held
    0.54-0.58s against 0.85-0.88s. Review round 3's freelist probe, 4,000
    completions: 2.1MB freelist 0.31s in 13 transactions (round 3: 2.26s,
    about 226; f1bcc1d7 0.20s, 2), 4.1MB 0.31s in 7 (round 3: 57s, 3,616;
    f1bcc1d7 0.21s, 2).
  - Review round 1 (FAIL) found: the fold half of the first final fix
    regressed the benchmarks (head against the fold changes switched off:
    sec/op +26% and +30%, reserve-p50 +65% and +57%) and slowed a backlog's
    fold up to ten times (7.3-7.9s against 0.8-1.0s); the wait after a
    chunk counted the chunk's read and earlier transactions and waits, so
    nearly always waited the full 100ms; mutants A, B and E survived; and
    the results doc wrongly said the fold changes did not account for the
    speed failure, and gave a cap rationale and a 7.5ms figure that did
    not hold. The final code drops the wait (the timing bug and mutants A
    and B with it), bounds the split by pages dirtied, and has a test
    killing E; the docs are corrected.

  - Review round 2 (FAIL) found: F1, a crash or restore between a split
    fold's transactions brought back a kept-live job's old std and failed
    every checkpoint until a restart; F2, the node-count bound
    under-measured values larger than a page. Both fixed in round 3 (see
    the approach above). It also found the backup half has no clean A/B (A1
    is a diagnostic proxy, K1 was void), and that G1 against G3 is
    load-unmatched evidence for the completion bound only; the results doc
    says so. Drops in a chunk's first transaction and samples in its last,
    and samples counting early after a crash, are incidentals below.
  - Gate soak of the final code: G3 measured the node-count code, whose
    transactions differ (prodsim's wrote about 2.8MB under its 2MB bound),
    so it no longer evidences the final code. **G4 (2026-11-13; `src-gate`
    at 353a4db, a fresh build; lines 10:01:40-11:01:18; 3,000 runners from
    10:36, peak 3,056; 279,706 reservations): 0 of 61 lines over 50ms,
    worst 25.7ms, 0 abandoned, 0 not durable, largest single wait
    177ms.** `TestAddLatency1000` 3 times after it: p99 59.8, 64.0 and
    57.7ms, PASS. Its folder was busy about 58s of each minute from 10:48
    (about 310 transactions a minute of about 35 completions); fold lag is
    not logged (see the results doc's "Open for the owner").
  - Review round 3 (FAIL) found B1 (the fixed 2MB split capped the
    folder near G4's load: busy 96% against `f7006373`'s 63% at matched
    load, fold lag up to 147s against 51s) and B2 (a large freelist
    collapsed the fold to about a completion a transaction). Round 4's
    soaks (2026-11-13, results doc's "Round 4"):
    - G5 (12:55-13:54; bound growing from a 5s lag): 3 of 61 lines over,
      all burst lines (69.2-98.6ms), the bound grown to 15-20MB at 46-51s
      of lag; lag bounded (checkpoints at most 51.5s).
    - H1 (13:57-14:57; the same at prodsim scale 2): lag bounded (`maxLag`
      at most 52s, checkpoints 48.3s), 6 burst lines over (59.8-123.7ms).
    - **G6 (15:23-16:22; round 4's code, 60s step; `src-gate` 4c3e7d5):
      60 of 61 lines passed, the five burst lines 23.6-36.7ms; the one
      failure, 61.9ms, is the last, partial line: 39s, from 16:22:02 to
      `wr manager stop` at 16:22:40. It coincides with the last checkpoint
      (0000000d, 58.5MB over 5.7s, 16:22:18-16:22:23). Round 4's lag
      growth was inactive in it: lag at most 6.2s (the fold writer's
      16:23:02 line; its 16:22:02 line's 19.1s is the minute before), so
      the bound was 2MB. The wal's slowest sync (87.7ms) and the fold's
      longest hold (910ms) come from that 16:23:02 line, whose interval
      includes the shutdown. The lines before were 40.9ms (16:21:02, no
      burst) and 36.7ms (16:22:02). Final checkpoints of the same size
      passed in G5 (58.8MB, 10.2ms) and H1 (58.2MB, 24.6ms), and G1's
      failing last line (51.4ms) also held its 0000000d (59.6MB). Without
      a trace of that window the cause cannot be separated between the
      checkpoint and the 2MB fold commits or NFS load; the checkpoint item
      below shows an unpaced checkpoint of that size holding reservations
      past 50ms, and paces it. So the gate criterion fails on round 4's
      code by that line.** Fold lag (`maxLag`, an upper bound) peaked at
      55s in the first burst cycle at 3,000 runners (15:58) and
      2m0s-2m16s in the next four, and returned to 3-7s between
      (checkpoints at most 106.6s). `TestAddLatency1000` 3 times after it:
      p99 59.8, 66.1, 61.9ms, PASS. The final code's soaks (G7, then H2 at
      prodsim scale 2) are prepared for 2026-11-16; see the checkpoint
      item below.
  - Review round 4 (FAIL) found the code sound but: mutant MA (lag
    counted from a fold's end) survived every test, as
    `TestStoreFoldBacklogCapacity` set `foldLagFrom` by hand; and this
    checklist and the results doc overclaimed G6's failing line as "the
    1d.2 checkpoint, not the fold", with several figures taken from the
    wrong interval. Round 5 adds the Convey that kills MA (and one that
    kills MC), and rewords G6 above and in the results doc ("Round 4" and
    "Round 5"). Its probe on simulated NFS at 400 completions a second
    gave a fold lag of 108s and rising slowly, against 3-7s for
    `f1bcc1d7`: the 60s step lets the fold settle minutes behind where
    `f1bcc1d7` settles seconds behind (results doc, "Open for the
    owner").

- [ ] Checkpoint writeback (1g.5 with the 1d.2 incidental,
  .docs/customdb/phases/incidentals.md): the gate soak G6 of round 4's
  code failed one line, its last (61.9ms), which coincided with its last
  checkpoint (58.5MB written over 5.7s); production-sized checkpoints are
  55-76MB, written over 5-7.6s. The checkpoint's `.ckpt.tmp` write was
  unpaced: pace it as the backup copy is.
  - Source: phase 1 gate item 1g.5, review round 4 of this fix
    (2026-11-13), and the 1d.2 incidental.
  - Root cause: `checkpointMerge.write` (jobqueue/store_checkpoint.go)
    wrote the whole file through a 4KiB `bufio.Writer` straight to the
    `.ckpt.tmp`, then made it durable with one `f.Sync`. No hot-path lock
    is held meanwhile (the checkpointer holds only `ckptMu`; the store's
    lock is taken later, in batches, for the Ref swap), so the cost is
    I/O alone: the file sits as dirty pages until that fsync, which then
    sends it all at once, and the wal's syncs, which reservations wait
    for, queue behind it, on local disk as on NFS.
  - Red command (local disk, about 30s; 120,000 live jobs of 1KiB random
    commands, so a 72.9MB checkpoint):
    `(unset $(compgen -v | grep '^OS_'); HOME=<scratch> CGO_ENABLED=1 go test -tags 'netgo reliability_repro' --count 1 ./jobqueue -run '^TestReserveWaitDuringCheckpoint$' -v -timeout 30m)`,
    exit 1 on round 4's working tree (2026-11-13 20:08):

    ```text
    reserve_checkpoint_tail_repro_test.go:102: checkpoint 72896485B in 2.392579134s; reservations 476: p50 965.69µs, p99 128.311322ms, max 146.011401ms
    Line 106:
    Expected '128.311322ms' to be less than or equal to '50ms' (but it wasn't)!
    --- FAIL: TestReserveWaitDuringCheckpoint (11.41s)
    ```

    The test checkpoints the store once, then reserves every 5ms, each
    reservation waiting for its record to be durable, from a second before
    the next checkpoint until it ends, and requires the p99 of those made
    while it ran to be at most 50ms. `WR_REPRO_DIR` puts the store on NFS.
  - Approach: `checkpointMerge.write` writes through `backupCopyWriter`
    (jobqueue/db.go), which starts writeback of each `backupCopySyncBytes`
    (1MiB) and waits on the one before, so at most about 2MiB is ever
    outstanding; its new `keepCached` skips the backup copy's
    `FADV_DONTNEED` (`backupPaceRange` takes a `drop` flag), as folds and
    queries read the checkpoint's records through their Refs. The
    temp-file, fsync, rename and directory-fsync steps are unchanged, so
    the checkpoint is as crash-safe as before. Spec "Checkpoints" step 3
    says so.
  - Files: jobqueue/store_checkpoint.go (`write`), jobqueue/db.go
    (`backupCopyWriter.keepCached`, `pace`), jobqueue/db_backupsync_linux.go
    and jobqueue/db_backupsync_other.go (`backupPaceRange`'s `drop`),
    jobqueue/reserve_checkpoint_tail_repro_test.go (new, tag
    `reliability_repro`), jobqueue/store_checkpoint_pace_test.go (new),
    .docs/customdb/spec.md, .docs/customdb/phases/incidentals.md,
    .docs/customdb/results/phase1-gate-1g5.md ("Round 5") and its
    `fix-r5-*` files.
  - Tests:
    - `TestReserveWaitDuringCheckpoint` (tagged): the red above. Interleaved
      on 2026-11-13 (results doc's "Round 5", `fix-r5-ckpt-ab.txt`):
      local disk, p99 122-142ms at `f1bcc1d7` (4 runs) and 128-138ms at
      round 4 (4) against 5.5-16.1ms at round 5 (8); NFS (evening),
      97-111ms at `f1bcc1d7` (5) and 99-143ms at round 4 (3) against
      3.4-43.7ms (8; all but one under 7ms). The checkpoint took
      2.19-2.36s locally at round 5 against 2.33-2.74s at `f1bcc1d7`, and
      2.12-2.66s on NFS against 2.70-3.03s: pacing costs it nothing.
    - `TestLogStoreCheckpointPacesItsWrite`: a checkpoint of 3,000 live
      jobs with 4KiB random commands (about 6MB) forces writeback at least
      once a MiB and leaves more than half its file in the page cache
      (100%; 22% with the pages dropped; 90% until round 6).
    - Round 6: `TestLogStoreIncrementalBackupDropsHistoryCopyFromCache`
      (a tick's copy of a history.bolt over 3MiB, through
      `writePacedFile`) and a Convey in `TestDBBackupCopy`
      (`db.copyBackup`) require at most 50% of the backup copy resident
      (2.1% and 2.7%), so `keepCached` stays the checkpoint's alone.
    - Round 7 (review round 6's R6-1): the three residency checks go
      through `soResidency`, which probes the temporary directory first (a
      64KiB file written, synced, renamed and reopened must show its pages
      resident and lose them on `FADV_DONTNEED`) and skips the assertion,
      logging why, where it cannot (tmpfs keeps the pages; NFS's mincore
      shows none) or off Linux. Shown on ext4 `/tmp` (asserting; M1, M2
      and CK2 killed), `/dev/shm` and NFS (skipping, passing):
      `fix-r7-mutants.txt`, `fix-r7-tmpdirs.txt`.
  - Mutants (plain copies under the scratchpad, `r5g5-mut-*`, each
    compiling; `fix-r5-mutants.txt`):

    | Mutant | Killed by |
    | --- | --- |
    | CK1: the pre-fix unpaced write | PacesItsWrite, Line 79 (0 paces, want at least 5); `TestReserveWaitDuringCheckpoint` (round 4's arm) |
    | CK2: `keepCached` false | PacesItsWrite, Line 80 (23% resident) |
    | CK3: pace every 8MiB | PacesItsWrite, Line 79 (0 paces) |
    | M1 (round 6): `keepCached: true` in `writePacedFile` | DropsHistoryCopyFromCache, Line 280 (100% resident) |
    | M2 (round 6): `keepCached: true` in `copyBackup` | DBBackupCopy, Line 1343 (100% resident) |

    M1 and M2 survived round 5's tests (review round 5's B1);
    `fix-r6-mutants.txt` has round 6's runs, CK2 again among them.
  - Round 6 gates (2026-11-14 00:22-00:34; `fix-r6-gates.txt`): `make
    lint` 0 issues; `go vet` with and without `reliability_repro` clean;
    `TestStoreFold*`, `TestStoreStats*`, `TestLogStoreCheckpoint*`,
    `TestLogStoreIncrementalBackup*` and `TestDBBackupCopy` passed, plain
    and under `-race`; `make test` passed, 1,297 tests. Only tests and
    docs changed against round 5.
  - Round 7 gates (2026-11-14 00:57-01:09; `fix-r7-gates.txt`): `make
    lint` 0 issues; `go vet` with and without `reliability_repro` clean;
    the targeted set passed, plain and under `-race`; `make test` passed,
    1,297 tests. Still only tests and docs changed against round 5.

  - Gates on the final code (2026-11-13, scratch `HOME`, every `OS_`
    variable unset): `make lint` 0 issues; `TestReserveWaitDuringCheckpoint`
    p99 11.6ms; `TestReserveWaitDuringBackup` p99 7.4ms; `make test`,
    `WR_TEST_STORE=all make test` and `make race` passed, 1,296 tests each
    (20:30-20:55).
  - Speed: the four `*UnderReserveLoad` benchmarks against `f1bcc1d7`, 8
    interleaved rounds (21:52-23:25), PASS, no metric significant (sec/op
    geomean +1.5%); a first run was void, the base's benchmark server
    failing to bind its port. Checkpoint time: no slower (above).
  - Found on the way, not fixed here: on NFS a checkpoint's fold of
    120,000 freshly added jobs took 27.5-27.7s against 6.6-6.8s at
    `f1bcc1d7`, the round 1-4 fold bound's 2MB commits each paying an
    NFS fsync (results doc, "Round 5" and "The fold bound's cost").
  - Gate soaks: not yet run. `src-gate`
    (/nfs/hgi/wr/sb10-bigdb/customdb/1g5fix/src-gate) holds the final code
    at commit 8dab632aa844fad73528e4172ba257ac90e7ab3f, equal file for
    file to round 5's working tree outside `.docs` (round 6 changed only
    tests and docs, so its wr binary is the final code);
    `tools/chain17.sh` runs G7 (gate settings) from 10:00 on 2026-11-16,
    then `TestAddLatency1000` 3 times (runs 19-21), then H2 (prodsim scale 2) if it can start by 15:52. It waits for 09:40,
    checks `src-gate` is clean at that commit, rebuilds the test binary
    from it, and logs each soak's wr buildinfo. **It was started at
    23:33:24 UTC on Friday 2026-11-13, as PID 308228; do not start it
    again**, as a second copy would collide on the soaks' `SOAK_ROOT` and
    ports. Note for future chain scripts: take a `flock` at start. Its
    command:

    ```text
    setsid nohup /nfs/hgi/wr/sb10-bigdb/customdb/1g5fix/tools/chain17.sh > /nfs/hgi/wr/sb10-bigdb/customdb/1g5fix/chain17.out 2>&1 < /dev/null &
    ```

## Incidentals

- [ ] On NFS a checkpoint's fold of 120,000 freshly added jobs (24 adds of
  5,000) took 27.5-27.7s against 6.6-6.8s at `f1bcc1d7`: the fold bound's
  2MB commits each pay an NFS fsync, and records that fresh never let the
  bound grow (`TestReserveWaitDuringCheckpoint`'s setup, round 5,
  `fix-r5-ckpt-ab.txt`). Real but bounded; a later fix could grow the
  bound with the backlog's size as well as its age. Found in round 5
  (2026-11-13). Owner: customdb delivery queue row 20.
- [ ] The checkpoint's pacing shares the backup copy's test seams
  (`backupCopySyncBytes`, `backupPaceHook`), so a test shrinking the
  backup's pace also changes the checkpoint's. The fix is a pace interval
  and hook of the checkpoint's own: a code change, so not made in round 6.
  Found by review round 5 (2026-11-14). Owner: customdb delivery queue
  row 20.
- [ ] `backupCopyWriter`'s doc comment (jobqueue/db.go) describes only
  the backup copy, though the checkpoint now writes through it too. A
  code comment, so recorded and left in round 6. Found by review round 5
  (2026-11-14). Owner: customdb delivery queue row 20.

- [ ] `TestWriterSpendsAnExistingNumber` (jobqueue/wal/writer_test.go) failed
  once in `make test` at 17:18 on 2026-11-11 (Line 1462: Expected
  `[]uint32{1, 2, 3}`, Actual `[]uint32{1, 2}`), with this fix in the
  tree. The jobqueue/wal package is unchanged from f1bcc1d7 (`git diff
  f1bcc1d7 -- jobqueue/wal` is empty), and the test passed 200 times in a
  row (`-count 200`) beside the next `make test`. Its second Convey
  appears to depend on whether the writer's idle reopen of the spent file
  2 has run before `Close`: if not, `Close`'s one pass reopens file 2,
  which fails, and returns without making file 3. Not on the known-flake
  list; routed to the queue owner (.docs/delivery-queue.md row 20) for a
  deterministic rewrite, not fixed here.
- [ ] A fold chunk's rglookup drops all go in its first transaction and its
  exitstat samples all in its last, outside the size bound, so a large `wr
  mod` (one drop scan per modified key) or a burst of failures still makes
  one large commit. Found by review round 2 of this fix (2026-11-12).
  Owner: customdb delivery queue row 20 (.docs/delivery-queue.md). Not
  bounded here: drops would need their own count in the fold's
  transaction accounting.
- [ ] A completion's learned-requirement samples go in the transaction that
  records it, so after a crash or restore between a split fold's
  transactions they count before the folded position does (the store
  inspected between transactions holds more samples than before the
  fold; the refold adds none twice, by `completeSeq`). Moving them to the
  last transaction would lose them on a refold, as `completeSeq` then
  skips their completion. Found by review round 2 (2026-11-12). Owner:
  customdb delivery queue row 20.
- [ ] Every commit of history.bolt writes its freelist (bbolt's `NoFreelistSync`
  is off), outside the size bound, so a split fold in a history.bolt with a
  large freelist writes it once per transaction: with 2,000,000 rglookups put
  then deleted in a test store, transactions bound at 1MiB wrote up to 1.9MB
  (measured with a draft of the scale that counted only transactions the bound
  ended). The soaks' history.bolt held about 2,000 free pages (2026-11-11
  traces), a 16KB freelist. Round 4 leaves the freelist out of the scale and
  bounds no transaction below the freelist (review round 3's B2), so a split
  fold writes its freelist at most once per freelist's worth of its own
  records; the per-commit write itself remains. Found while testing the bound
  (2026-11-12). Owner: customdb delivery queue row 20.
- [ ] `wrdev.sh prodsim`'s restarter outlived the gate soak G4 (PID
  3978040, parent init, `sleep 180` loop, at 11:04 on 2026-11-13, two
  minutes after the soak ended; it exited later): `prodsim_cleanup`'s
  `our_child` check fails when prodsim runs in a subshell, as
  `developers/soak/run.sh` runs it, so the restarter is orphaned rather
  than stopped. Found by review round 3 (2026-11-13). Owner: customdb
  delivery queue row 20 / 1g.6. Not fixed here.

## Review round 7: PASS (2026-11-14)

Review rounds 1 to 6 failed; each round's findings are recorded above.
Round 7 passed the code, tests and docs at the working tree that src-gate
8dab632 builds (non-test Go code byte-identical). Non-blocking notes:

- `residencyUnobservable`'s comment says the probe's pages "must" be
  resident and "must" be dropped; the code tests "any resident" and "not
  all resident" afterwards.
- Residency failures report at the helper's Line 100, not the caller's.
- The src-gate note could say "rounds 6 and 7 changed only tests and docs".

The two fix items stay unticked until the gate soak G7 (chain17.sh, PID
308228, from 10:00 GMT on 2026-11-16) shows 1g.5's criteria on this code.
