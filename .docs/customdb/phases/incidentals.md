# Incidental findings during customdb delivery

Branch-owned list of findings that came up during implementation and review
and are not part of the current item. Each is resolved before the PR is
called ready, or explicitly deferred with the owner.

- [ ] (from 0a.1 review) The rerun guard's member check has no `*db`-level
  test: changing the `archiveJobTx` `addStored` closure to `live != nil`
  survives every test. Add a bbolt test that archives a guarded dependent
  after `setDependents` but before the add's write commits, and asserts it
  is not kept live. Pre-existing gap at d1a4b6d7; C4 tests 9-11 cover the
  log store in Phase 1.
- [x] (from 0a.1 review) spec.md's store code block declares
  `storeKindLog`/`storeKindBolt`, which Phase 0 omits as unused; add them
  in Phase 1 (1c.1/1e.4) when the log kind is first read. Done in 0b.1:
  `forEachStore` needs both kinds, so `store.go` now declares them.
- [ ] (from 0a.1 implementation) `cleanorder -min-diff` wants to move
  untouched declarations in `reliable4_status_limit_test.go` and
  `runner_lifecycle_test.go`; pre-existing ordering drift, left alone.
- [x] (from 0b.1) `db_test.go` and `db_runstate_test.go` stay whole in
  `boltOnlyTests`, so F5 would delete their behavioural tests with their
  bbolt ones. Move the behavioural tests onto `storeInspector` (or into
  `*_bolt_test.go` files) before Phase 2.
  Done in 0b.1: the bbolt tests are in `db_bolt_test.go` and
  `db_runstate_bolt_test.go`; the rest run through the store API in
  `store_jobs_test.go`, `backup_copy_writer_test.go`, `db_encode_test.go`
  and `db_runstate_test.go`.
- [x] (from 0b.1) Unlisted test files still call helpers defined in
  bolt-only files, so F5's deletions break their build until each helper
  has a store-neutral home: `boltWrites` (`db_bench_test.go`, used by
  `readd_queued_test.go`), `rewriteStoredLiveJob`
  (`store_bolt_inspect_test.go`, used by
  `running_dependent_rerun_test.go`), and `server.boltDB()` with
  `archivedDecodes` (status and history-scan tests). The A2 guard only
  greps for direct bbolt use, so it does not catch these.
  Done in 0b.1: generic helpers moved to unlisted files, bbolt ones stay
  with the tests that need them in listed files, `rewriteStoredLiveJob`
  and `openStoreFileInspector` go through the store, and A2 test 4
  (`TestBoltOnlyTests`) type-checks the package with every listed file
  emptied. `boltDB()` and `archivedDecodes` were never in listed files.
- [ ] (from 0b.1) Unlisted tests reach the bbolt store through
  `server.boltDB()`, which panics once 1e.4 makes the log store the
  default, so they fail at 1e.4, not at F5. 0b.1 ported the plain store
  inspection ones (`crash_mid_add_guard_test.go`,
  `buried_dependent_restart_test.go`, `confirm_dead_after_stop_test.go`,
  `kick_order_test.go`, `running_dependent_rerun_test.go`,
  `shutdown_runner_wait_test.go`, `runner_lifecycle_test.go`, and the
  `recommendedReqGroup*`, `recMBRound` and `archiveJob` uses in
  `jobqueue_test.go`, `nohold_test.go`, `queues_avoid_scheduling_test.go`
  and `runner_scheduling_test.go`) and moved the bbolt-only ones to
  `release_std_bolt_test.go` and `resume_order_bolt_test.go`. Left, each
  needing something the log store brings in Phase 1:
  `configureFastTestBackups` and `s3accessor` (`jobqueue_test.go`: backup
  tuning and the S3 backup client); `archivedDecodes`
  (`reliable4_history_scan_test.go`, `reliable4_status_limit_test.go`,
  `reliable4_status_unbounded_test.go`, `status_seed_decode_test.go`: a
  decode counter); `reqStats` (`nohold_test.go` `loadsAfter`,
  `reqstat_cache_test.go`, `rac_learned_bench_test.go`: the learned
  requirement cache). Also failing at 1e.4: tests driven by `db.go` hooks
  the log store does not call (`jobChangeAheadHook`,
  `jobChangeAheadQueueingHook`, `jobExitSnapshotHook`,
  `dependentsReadHook`, `dependentsGuardHook`, `archiveGuardsSeenHook`,
  `reqStatLoadHook`; `grep` each in `jobqueue/*_test.go`), and listed tests
  that `serve()` a manager and call `boltDB()`, which must set
  `storeKind: storeKindBolt`. Owner: 1e.4, which ports each, pins it to
  bbolt, or deletes it where a log-store test covers it.
- [ ] (from 0b.1) Unlisted tests still open a bbolt `*db` directly
  (`initDB`, `depGroupSeenGets`, for example in `jobqueue_test.go`,
  `db_unreadable_test.go`, `db_encode_test.go`, `client_payload_test.go`
  and `status_summary_decode_test.go`), so they build only while `db.go`
  exists. A2 test 4 cannot see this, since `db.go` stays until F5. Owner:
  2e.1 (F5), which deletes `db.go` and must port each to the store API or
  `storeInspector`, or delete it where a log-store test covers it.
- [ ] (from 0b.1) The tests that open their crash window by holding the
  bbolt write lock are listed, so F5 deletes them: `start_durability_test.go`,
  `release_durability_test.go`, `release_durability_resend_bolt_test.go`,
  `release_std_bolt_test.go`, `reserve_ttr_test.go`,
  `reserve_durability_bolt_test.go`, `archive_stall_bolt_test.go`,
  `archive_before_start_bolt_test.go` and `warn_storm_bolt_test.go` (its
  reservation-warning half; the slow-request half needs no bbolt and is in
  the unlisted `warn_storm_test.go`); so are `resume_order_bolt_test.go`'s
  resumes racing the bbolt writer's final drain. On the log store, stalled
  reservations are C3's (1e.1, tests 2-4 and 6) and acknowledged starts and
  releases are A4's (0d.1, 1g.1). An acknowledged bury, a buried job's
  stderr seen once it is buried, a re-sent release or bury, a stalled
  archive, a late start and a resume during the writer's last drain have
  no log-store test yet. Owner: 1e.1 (C3), whose sync hook can hold the
  commit as these tests hold bbolt's lock.
  The same holds for four more listed tests that hold bbolt's writer:
  `TestReliable4AddCloseDrains` and `TestReliable4ArchiveCloseDrains`
  (adds and archives queued when `close()` is called are all answered
  and durable), `TestReliable4BestEffortCloseDrains` (best-effort changes
  queued when `close()` is called are durable with their latest state)
  and `TestReliable4ArchiveBeatsResurrectingChange` (a change or exit
  queued behind an archive's held commit does not bring the job back
  live). Their unheld forms run on every store
  (`TestStoreCloseDrainsWrites`, `TestStoreArchivedJobNotResurrected` in
  `store_jobs_test.go`), but there the race with `close()` decides how
  many writes are in flight; 1e.1 adds held-commit forms on the log store.
- [x] (from 0b.1 review) F5 broke the `reliability_repro` build:
  `envIntDefault` and `reliable4GiB` were defined only in the listed
  `reliable4_backup_repro_test.go`. Done in 0b.1: both moved to
  `reliable4_reportstorm_test.go`, and A2 test 4 now vets every tag set.
- [x] (from 0b.1 review) F5 deleted behavioural coverage with its bbolt
  step: `TestMovedOnRunnerAfterRestartSecondReserved` now composes its
  crash image through the store in `moved_on_runner_test.go`, and
  `TestReserveAndStartWriteRunState`'s crash recovery and duplicate start
  are in `TestReservationAndStartRecoveredAfterCrash`
  (`db_runstate_test.go`), leaving the live record bytes and the
  `runStates=1` log in `db_runstate_bolt_test.go`.
- [ ] (from 0b.1 review) `reliable2_dbcompat_test.go` is listed whole, as
  its fixture is a bbolt golden file, so F5 deletes
  `TestReliable2RecoveryWindowReturnsRecovering` and
  `TestReliable2RecoveryRestoresIncompleteJobs`: a recovery-window j* call
  gets `ErrRecovering`, and the fixture's incomplete jobs become reservable
  once recovery ends. These are E1 conversion coverage: a converted golden
  bbolt file must recover the same way. Owner: 1f.2 (E1, Phase 1 form),
  kept by 2d.1.
- [x] (from 0b.1 review 5) More store-neutral behaviour that F5 would
  have deleted with its listed file now runs on every store: an env
  retrieved before it is stored is still stored (`TestStoreEnv`, from
  `TestReliable4EnvMissNotCached`); a second open of a held store gets
  `ErrDBLocked` and changes no file, the holder keeps working, an open
  waits for a briefly held lock, and a corrupt store is restored from its
  backup (`store_lock_test.go`, from `depgranularity_dblock_test.go`,
  which keeps only its bbolt `ErrTimeout` check); the recovery dependency
  pass resolves as per-job resolution does and stops on a cancelled
  context (`TestStoreRecoveryDependencyPass`, from
  `TestReliable4RecoveryDependencyPass`); a reopened store gives back its
  live and archived jobs (`TestStoreReopenRoundTrip`, from
  `TestDBMapFreelistRoundTrip`); and a key archived twice at one end time
  is recent once (`TestStoreRetrieveCompleteJobsRecent`, from
  `TestDBEndTimeIndex`).
- [x] (from 0b.1 review) The ported lock tests (`TestStoreOpenLocked` in
  `store_lock_test.go`) hold a store open and require a second open to
  fail with `ErrDBLocked` leaving every file in and beside `dbFile`
  unchanged, `.restoring` and `.corrupt` included. bbolt meets this
  because its lock is on the file it opens, but its dev wipe removes
  `dbFile` before taking that lock. The log store must take its lock
  before E2's open-order steps 1-3 (dev wipe, removing `.restoring` and
  `.corrupt`, resuming conversion), and so cannot keep it inside the
  store directory those steps remove or rename. spec.md now says so: the
  lock is `dbFile + ".lock"`, taken first (C7, E2, E3), and C7 test 1
  checks the files. Owner: 1c.2 (C7), kept by 1f.4 (E2). Done in 1c.2:
  `lockStore` (`store_open.go`) flocks `dbFile + ".lock"`, retried until
  `managerDBOpenTimeout`, then `ErrDBLocked`. `openStore` takes it before
  `initDB` for every kind, so bbolt's dev wipe no longer runs while
  another manager holds the store, and `openLogStoreLocked` takes it
  before the store directory or `VERSION` is touched; each store releases
  it on close. `TestStoreOpenLock` (`store_open_test.go`) proves a refused
  open changes no file, with a dev wipe asked for (bbolt) and with only the
  lock held and no store directory yet (log).
- [ ] (from 0b.1 review) `TestReqStatCacheUncommittedStat`
  (`reqstat_cache_bolt_test.go`) holds a bbolt write transaction open,
  so F5 deletes its rule: a learned-requirements load that runs while a
  new sample is not yet committed is not current once it commits. On the
  log store the folder adds samples and bumps `reqStatChanges` (spec
  "Folder and history"), so the same race is a fold commit. Owner: 1d.1
  (D1), which adds a log-store test with the fold commit held.
- [ ] (from 0b.1 review) Listed tests cover what a manager does when it
  opens an older bbolt database, and F5 deletes them: its upgrade is
  logged with progress, rate-limited, and not at all when there is none
  (`reliable4_upgrade_log_test.go`, `TestDBUpgradeProgress`); the
  startup-progress sidecar stays live after it until the token is ready
  (`TestServeReportsPostUpgradeStartupUntilTokenReady`); and pre-upgrade
  records (retired dep group index, missing reverse lookups and seen dep
  groups) still resolve dependencies and allow a key-changing modify
  (`TestDepGroupRetiredIndex`, `TestDBReverseLookupIndex`,
  `TestDBDepGroups`). On the log store the old database is converted
  instead. Owner: 1f.2 (E1), which shows a converted pre-upgrade database
  resolves and modifies the same way, and that the conversion's sidecar
  and log lines behave as these tests require of the upgrade's.
- [ ] (from 0b.1 review) `TestReliable2FastStartupNoHistoryScan`
  (`reliable2_startup_scale_bolt_test.go`) writes its 250,000-job history
  as raw bbolt records, so F5 deletes the check that startup time does not
  grow with completed-only history and seeds no complete counts. Owner:
  1d.1 (D1), which can seed `history.bolt` at that scale and assert the
  same of the log store's startup.
- [x] (from 0c.1) Four `db.bolt.Update` calls bypass write turns and
  Batch, so no "db writer" line counts them: `storeRunningRerunMarks`
  and `storeLiveForRerun` (while serving), `dropRunStates` (recovery)
  and `syncFreelist` (close). A3 names five writers only; the 0c.1
  review decides whether the two serving-time ones count under `batch`.
  Done in 0c.1: the two serving-time writes count under `batch` via
  `updateUncoalesced`; the recovery and close ones stay uncounted.
- [x] (from 0d.2) Replaying the prodsim recording may report false
  mismatches: `cachedReqGroupRecs` and `reqStatChanges` depend on when the
  async `refreshReqGroupRecsLater` loads finish, and `RepGroupStatus`
  float sums may differ in summation order. They are not writes, so the
  overlap rule does not exclude them. Check at the first replay of the
  0d.3 recording (A5 gate) and, if they mismatch, exclude or normalise
  them in `storeDigest` with a spec note. At the same replay, revisit two
  Phase 0 digest choices (spec A5): error results digest by their text,
  which the log store's errors may word differently; and handle results
  digest only whether they are nil, so a log store returning a nil handle
  where bbolt returns one mismatches.
  Done in 0d.3: the first replay had 30,701 mismatches, all in
  `cachedReqGroupRecs`, `refreshReqGroupRecs` and `reqStatChanges`; status
  float sums had none. The cause was the cache's 1s refresh interval, which
  held back loads a replay at full speed reached in under a second (so
  `reqStatChanges` ended 3 below the recording's every time), and background
  loads. Replay now runs with no interval and probes the learned values
  before each call naming a ReqGroup and after each write, and accepts a
  recorded answer that is one of the probed values or a stamp no higher
  than the replayed one (spec A5, `store_record_learned.go`,
  `TestStoreReplayLearned`); the recording now replays with 0 mismatches.
  The error-text and handle digests give bbolt's own answers on a bbolt
  replay, so only a log-store replay can test them: 1g.2 owns that.
- [ ] (from 0d.3 review) The 1g.2 replay folds before each
  learned-requirements probe, which hides a folder that stalls or never
  invalidates `reqStatCache`. Spec D1 has no acceptance test that the
  folder keeps folding and invalidates the cache unprompted (D1 test 2 and
  C6's wait on the folder's position cover it only in part). Owner: 1d.1
  (D1), which adds that test.
- [x] (from 1a.2) `wal.WriterStats` is cumulative and has no `MaxSync`,
  but A3's `log` line reports `maxSync` per interval. Owner: 1c.1 (C2),
  which adds the `log` writer's "db writer" stats and decides how the
  per-interval maximum is kept (in the writer or in the log store).
  Done in 1c.1: the writer keeps it, since only its goroutine times each
  sync. `Writer.TakeMaxSync` returns the longest batch sync since the last
  call and resets it (`TestWriterTakeMaxSync`); `WriterStats` stays totals.
  The log store's reporter, its one caller, takes it each interval and
  turns the totals into per-interval figures (`logWriterReport` in
  `store_stats.go`, `TestLogStoreWriterStats`).
- [x] (from 1c.1) After a failed sync the wal writer opened the next file
  only when the next batch arrived, so a manager stopping first left the
  abandoned file unbounded and recovery replayed the failed batch. Done in
  1c.1: the writer opens the next file at once, retries it while idle and
  makes a last try at `Close`, and never skips a file number
  (`TestWriterReopensAfterAbandon`, `TestLogStoreFailedWriteNotReplayed`;
  spec B2 and Recovery step 4 amended, with the residual window stated).
- [x] (from 1b.1 review) A decoded Behaviours or Dependencies holding a
  nil element panics in `dropImpossibleCleanups` (`behaviours.go`
  `withoutCleanups` reads `b.Do`). `FuzzFlatSpec` found it in the flat
  spec's blob, and bbolt's `decodeJob` calls the same function, so a
  corrupt bbolt record panics the same way. wr never writes nil elements.
  Owner: 1b.1 (C1), which rejects such a blob as malformed in both
  decoders through one shared check. Done in 1b.1: `checkStoredElements`
  (`job.go`) returns `errNilStoredElement`, which `decodeJob` returns and
  the flat blob decode wraps in `errFlatMalformed`.
- [x] (from 1c.1) The log store's `applySpec` adds reverse dep-group index
  entries for a new spec but never removes the replaced spec's entries, so
  the index depends on apply order once a key's spec changes its
  dependencies. Today's re-adds keep the spec, so it is not reachable yet.
  Owner: 1c.4 (C4), whose modify records change a key's spec; remove the
  replaced spec's entries and test the index after a modify in either
  apply order. The 1c.1 review also found two more order dependences C4
  must settle: a delete applied before an older spec lets that spec make
  the key live again (the reducer keeps no tombstone), and
  `applyComplete` copies whichever spec is live at apply time. Extend
  `TestLogStoreReducerOrder` to delete, modify and complete records.
  Done in 1c.4: `applySpec` unindexes the replaced spec's dep groups, and a
  modify's old key leaves the index even if complete, as bbolt's modify
  drops its lookups (`TestLogStoreReverseDepsAfterSpecChange`, both apply
  orders). No tombstone is needed because no removal applies before a
  lower-seq spec or complete record of its key: every add, delete, modify,
  archive, put-back and rep group lookup claims its keys (`logClaim`) from
  before it decides until it has applied, and a guarded add's put-backs,
  which claim nothing, decide and append under `guardMu` as removals do
  (`TestLogStoreArchiveRacesRemoval`, `TestLogStoreClaimedWrites`; tested
  with a hook holding a write between its append and its Refs,
  `logAppendedHook`); an archive
  decides and takes its seqs under the job's read lock (`decideArchive`), so
  a rerun mark's put-back after the decision is later than the archive's
  delete (`TestLogStoreArchiveRacesRemoval`). A later spec a complete record
  may pair with is a put-back of the same job, with the same spec. A
  transition or exit of a key a put-back or modify is making live waits
  for it to apply (`logStore.reviving`, spec Write path: reviving), so it
  is not dropped in memory and kept by replay
  (`TestLogStoreRevivedKeyTransitions`). The extended
  `TestLogStoreReducerOrder` applies random histories of adds,
  transitions, exits, archives (kept live or not, and of a key not live),
  put-backs, rep group lookups, deletes and modifies (keeping the key or
  not, of a live key or not, then the new key's own writes), each spec
  with its own rep and dep group, in every order those rules allow and
  checks each key ends as in seq order, rglookups included; it found that
  `clearStd` cleared a std record later than the complete record, now fixed
  (spec Records amended), and without the reviving rule it finds the
  transitions that would be dropped.

- [ ] (from 1c.2) The store lock now creates `dbFile + ".lock"` in the
  store's parent directory, so that directory must be writable. This is
  new for deployments whose parent directory is read-only. Owner: 2e.2
  (F6): add a CHANGELOG line saying so.

- [x] (from 1c.3) The reducer drops a complete record for a key that is
  not live and whose only history entry is already folded: it would need
  the key's spec from history.bolt to make an overlay entry (see the
  comment in `applyComplete`). Only an archive racing a delete (C4) can
  produce one, once 1d.1 folds. Owner: 1c.4 (C4), which owns that race.
  Done in 1c.4: an archive claims its key, so decides once any delete or
  modify of it in flight has applied, and if the key is then not live (a
  lost job removed before its run ended) its block starts with the job's
  spec record, so its complete record always finds a spec and the
  completion is recorded, as bbolt records it
  (`TestLogStoreArchiveRacesRemoval`; spec Records and Archive amended).
  The reducer's drop stays only for records no archive writes.

- [ ] (from 1c.3) `foldHistory` trusts its caller for contiguity: it does
  not check that a fold starts at the previous folded position F, and
  `putHistoryMeta` persists `f.to` even when it is below the stored F
  (in-memory F never moves back). Both are safe with one folder reading
  the log in order. Owner: 1d.1: have the folder fold contiguously from F,
  and either assert that in `foldHistory` or never persist a lower F.

- [ ] (from 1c.4) C4 tests 9 to 11 say "run a server on a log store",
  which cannot run until 1e.4 makes the log store the server's store.
  1c.4 covers every store-visible clause of them at the store level
  (`store_log_guard_test.go`). Still to cover with a server: the
  dependent going `ItemStateDependent` then `ItemStateReady`, "reserved
  again", "runs again once m completes", and test 9's `archiveKeptLive`
  (set by the server's mark; at the store level the outcome is
  `archiveRemovedLive` with the guard keeping d live). Owner: 1e.4.

- [ ] (from 1c.4) A modify drops its old key's rglookups, as bbolt's
  `deleteOldLiveJobs` drops its `repgroupToKey` entries, but the log store
  drops only those not yet folded (`applyModify` over the in-memory
  rglookups). Once 1d.1 folds rglookups into `history.bolt`, a modify after
  the fold leaves the old key under its folded rep groups, so
  `retrieveCompleteJobsByRepGroup` and the add dedup's
  `checkIfCompleteUnderRepGroup` still find it there, unlike bbolt.
  Owner: 1d.1: have the folder delete a modify's old key from
  `repgroupToKey` (history.bolt has no reverse lookup, so it needs one or a
  scan), and have reads hide folded lookups of keys modified away and not
  yet folded; extend `TestStoreModifyDropsRepGroupLookups` with a fold
  before the modify. Likewise for 1d.2 (C6): replay applies rglookup
  records from F but modify records only from a checkpoint's `walFrom`, so
  a modify between F and `walFrom` must not let its old key's earlier
  rglookups come back on recovery.

- [ ] (from 1c.4 review) `TestLogStoreFailedWriteNotReplayedAfterSpentFile`
  flakes about 1 in 500 under load, pre-existing (load-matched A/B at 1000
  runs: 2 fails at 4d128f96, 1 on the 1c.4 tree; files `[1 2]` instead of
  `[1 2 3]`). In `jobqueue/wal/writer.go` `run()`, if Close is seen before
  the writer's first reopen try, `reopen()` tries file 2, which fails and
  spends that number, and the loop returns without trying file 3.
  Owner: 1c.5: decide whether a close after a spent open must open file
  n+1 (a product fix) or the test must wait for the reopen before
  closing, with a red loop first.

- [ ] (from 1c.4 review) No test pins that a delete over the block size
  limit splits only between keys (mutant `delete-not-grouped`, dropping
  `f.group()` per key in `deleteLiveJobs`, survives). Impact is small: a
  delete is one record per key. Owner: 1c.5: add a Convey under
  `logBlockMaxFrames = 1` asserting a 3-key delete gives 3 frames, like
  `TestLogStoreSplitPutBacks`.
