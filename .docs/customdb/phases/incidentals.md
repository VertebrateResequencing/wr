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
  `server.boltDB()`, which panics once 1f.11 makes the log store the
  default, so they fail at 1f.11, not at F5. 0b.1 ported the plain store
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
  requirement cache). Also failing at 1f.11: tests driven by `db.go` hooks
  the log store does not call (`jobChangeAheadHook`,
  `jobChangeAheadQueueingHook`, `jobExitSnapshotHook`,
  `dependentsReadHook`, `dependentsGuardHook`, `archiveGuardsSeenHook`,
  `reqStatLoadHook`; `grep` each in `jobqueue/*_test.go`), and listed tests
  that `serve()` a manager and call `boltDB()`, which must set
  `storeKind: storeKindBolt`. Owner: 1f.11 (moved from 1e.4), which ports
  each, pins it to bbolt, or deletes it where a log-store test covers it.
  Not done in 1e.4: which of them fail is seen only with the log store the
  default, and that switch is blocked (see the 1e.1 incidental on putting
  `storeKindLog` first); a first run with it made saw `boltDB()` panics in
  `jobqueue_test.go` (`configureFastTestBackups`) and
  `reliable4_add_tx_test.go`, before the suite stopped.
- [ ] (from 0b.1) Unlisted tests still open a bbolt `*db` directly
  (`initDB`, `depGroupSeenGets`, for example in `jobqueue_test.go`,
  `db_unreadable_test.go`, `db_encode_test.go`, `client_payload_test.go`
  and `status_summary_decode_test.go`), so they build only while `db.go`
  exists. A2 test 4 cannot see this, since `db.go` stays until F5. Owner:
  2e.1 (F5), which deletes `db.go` and must port each to the store API or
  `storeInspector`, or delete it where a log-store test covers it.
- [x] (from 0b.1) The tests that open their crash window by holding the
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
  Done in 1e.1: a held wal sync holds the log's commit, with a manager on
  the log store (`storeKind: storeKindLog`). Stalled reservations are C3's
  tests in `store_reserve_test.go`; `store_log_held_test.go` has an
  acknowledged release and bury recovered from a crash copy
  (`TestLogHeldReleaseAndBury`), re-sent ones not acknowledged while held
  (`TestLogHeldResentReport`), a buried job's stderr (`TestLogHeldBuriedStd`),
  a stalled archive (`TestLogHeldArchiveStall`, which fails without
  `ttrCallback`'s `archivePendingLocked` guard), a late start
  (`TestLogHeldLateStart`), adds, archives and best-effort changes in flight
  at close (`TestLogHeldCloseDrains`) and a change or exit queued behind a
  held archive (`TestLogHeldArchiveBeatsChange`). A resume during the
  writer's last drain cannot happen on the log store: its state record is
  appended under the queue's lock, which shutdown's `q.Destroy` takes before
  the store closes, so at the latest it is in flight at close, the case
  `TestLogHeldCloseDrains` drains.

- [ ] (from 1e.1) Under C3, a reservation made while bbolt's write lock is
  held is abandoned, so `TestArchiveStallDoesNotRerunJob`
  (`archive_stall_bolt_test.go`) no longer fails without `ttrCallback`'s
  `archivePendingLocked` guard; its log-store form,
  `TestLogHeldArchiveStall`, does. No action beyond keeping the log-store
  form; F5 deletes the bbolt one. Owner: 2e.1 (F5), to delete it.

- [ ] (from 1e.1) C3's tests need a manager on the log store before 1f.11
  makes it the default, so `openStore` opens the log store for
  `storeKindLog`, and `storeKindBolt` is put first in `store.go`, making it
  the zero `storeKind` and so still the default. The log store's `backup`
  returns `errLogStoreBackup` (E5, 1f.3), its `upgradedOnOpen` is false (E1,
  1f.2) and `openStore` does no dev wipe for it (1f.1, 1f.4). Owner: 1f.11
  (moved from 1e.4): put `storeKindLog` first again, as the spec has it,
  to make it the default.
  Blocked in 1e.4: with the switch made, `make test` fails in about 25
  tests across `jobqueue`, `cmd` and `developers/soak/dbstart`, needing
  the log store's dev wipe (1f.1), backup (1f.3, 1f.5) and conversion of a
  bbolt file at the store path (1f.2), and 9 `forEachStore` tests fail
  with `WR_TEST_STORE=all` (`TestStoreCrash` among them); the switch is
  reverted until those land or are pulled forward.
  1e.5 made `WR_TEST_STORE=all` run the log store: those 9 now pass on it,
  with the leaves that need a backup (1f.3, 1f.5) skipped there.
  1f.1 added the log store's dev wipe (`wipeDevStore`, called from
  `openStoreKind` for both store kinds).
  1f.2 added conversion of a bbolt file at the store path
  (`openConvertedLogStore`, `openLogStoreInOrder` since 1f.4), after which
  `upgradedOnOpen` is true.
  1f.5 added the log store's incremental backups (`store_backup_tick.go`).
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
- [x] (from 0b.1 review) `reliable2_dbcompat_test.go` is listed whole, as
  its fixture is a bbolt golden file, so F5 deletes
  `TestReliable2RecoveryWindowReturnsRecovering` and
  `TestReliable2RecoveryRestoresIncompleteJobs`: a recovery-window j* call
  gets `ErrRecovering`, and the fixture's incomplete jobs become reservable
  once recovery ends. These are E1 conversion coverage: a converted golden
  bbolt file must recover the same way. Owner: 1f.2 (E1, Phase 1 form),
  kept by 2d.1.
  Done in 1f.2: `TestStoreConvertedDBCompatRecoveryWindow`
  (`store_convert_test.go`) runs both on the golden fixture, uncompacted
  and unversioned, converted by a log-store manager's start: the window's
  j* call gets `ErrRecovering`, nothing is queued until recovery is
  released, then both incomplete jobs are restored and reservable and the
  complete rep group counts 2 complete. The fixture's constants and copy,
  key and reserve helpers moved to `store_convert_test.go`, which F5 keeps.
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
- [x] (from 0b.1 review) `TestReqStatCacheUncommittedStat`
  (`reqstat_cache_bolt_test.go`) holds a bbolt write transaction open,
  so F5 deletes its rule: a learned-requirements load that runs while a
  new sample is not yet committed is not current once it commits. On the
  log store the folder adds samples and bumps `reqStatChanges` (spec
  "Folder and history"), so the same race is a fold commit. Owner: 1d.1
  (D1), which adds a log-store test with the fold commit held.
  Done in 1d.1: `TestStoreFoldUncommittedSample` (`store_fold_test.go`)
  loads the req group from inside a fold's open history.bolt transaction
  (`logFoldTxHook`), which sees no sample, and requires the cache not
  current once the fold commits and the next load to give the new sample.
  The fold adds samples through `reqStatCache.putStat`, whose commit hook
  invalidates, as bbolt's archive does.
- [x] (from 0b.1 review) Listed tests cover what a manager does when it
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
  Done in 1f.2 (`store_convert_test.go`): `TestStoreConvertPreUpgradeDB`
  converts a database with a retired dep group index entry and no
  `depgroups` or `jobLookupEntries` bucket, and requires the child to
  resolve, a key-changing modify of the parent to work and the recovered
  keys to match bbolt's after `initDB`'s upgrade; `TestStoreConvertSchemas`
  back-fills `depgroups` from `depgroupToKey` as `initDB` does. The
  conversion reports through the same `dbUpgradeReporter` as the upgrade:
  `TestStoreConvertLogsAsUpgrade` (its milestones at the default log
  level, no line per progress point, none on a restart),
  `TestStoreLongConvertReportsProgress` (a long conversion's "still
  running" lines), `TestDBUpgradeProgressLogRateLimit` (moved from
  `reliable4_upgrade_log_test.go`, now on the conversion phase: one
  promoted line per interval), `TestStoreConvertProgress` (the sidecar
  during and after) and `TestServeReportsPostConversionStartupUntilTokenReady`
  (the startup sidecar after a conversion until the token is ready). The
  upgrade log match constants and `logLinesForPhase` moved to
  `store_convert_test.go`.
- [x] (from 0b.1 review) `TestReliable2FastStartupNoHistoryScan`
  (`reliable2_startup_scale_bolt_test.go`) writes its 250,000-job history
  as raw bbolt records, so F5 deletes the check that startup time does not
  grow with completed-only history and seeds no complete counts. Owner:
  1d.1 (D1), which can seed `history.bolt` at that scale and assert the
  same of the log store's startup.
  Done in 1d.1: `TestLogStoreFastStartupNoHistoryScan`
  (`store_history_bolt_test.go`, in `boltOnlyTests` as it seeds
  history.bolt raw; Phase 2 needs its own over segments) seeds 25,000 and
  250,000 completed-only jobs whose history records no decoder accepts, so
  an open that read one would fail, and requires the large open within
  `c2AbsoluteStartupLimit`. It does not assert bbolt's no-scaling ratio:
  the log store loads the in-memory history key index (spec "In-memory
  state of logStore") from `completeSeq` at open, about 0.25µs per
  completed key (6ms at 25,000, 62ms at 250,000), which that ratio fails.
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
- [x] (from 0d.3 review) The 1g.2 replay folds before each
  learned-requirements probe, which hides a folder that stalls or never
  invalidates `reqStatCache`. Spec D1 has no acceptance test that the
  folder keeps folding and invalidates the cache unprompted (D1 test 2 and
  C6's wait on the folder's position cover it only in part). Owner: 1d.1
  (D1), which adds that test.
  Done in 1d.1: `TestStoreFoldUnprompted` archives two jobs with new
  samples, one after the other, never calling `foldNow`, and requires each
  folded and the cache invalidated within 5 fold intervals, then the new
  value learned; `TestStoreFoldFailureRetried` requires the folder to fold
  by itself after failed folds.
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

- [x] (from 1c.3) `foldHistory` trusts its caller for contiguity: it does
  not check that a fold starts at the previous folded position F, and
  `putHistoryMeta` persists `f.to` even when it is below the stored F
  (in-memory F never moves back). Both are safe with one folder reading
  the log in order. Owner: 1d.1: have the folder fold contiguously from F,
  and either assert that in `foldHistory` or never persist a lower F.
  Done in 1d.1: folds are made one at a time (`foldNow` under `foldMu`),
  each reading the log from F, taken under the store's lock, to the
  writer's durable end; `putHistoryMeta` never writes a folded position
  before the one `meta` holds (`TestStoreFoldNeverMovesBack`, which reopens
  after a fold to an earlier position).

- [x] (from 1c.4) C4 tests 9 to 11 say "run a server on a log store",
  which cannot run until 1e.4 makes the log store the server's store.
  1c.4 covers every store-visible clause of them at the store level
  (`store_log_guard_test.go`). Still to cover with a server: the
  dependent going `ItemStateDependent` then `ItemStateReady`, "reserved
  again", "runs again once m completes", and test 9's `archiveKeptLive`
  (set by the server's mark; at the store level the outcome is
  `archiveRemovedLive` with the guard keeping d live). Owner: 1e.4.
  Done in 1e.4: `TestLogStoreRerunGuardServer` (`store_log_guard_test.go`)
  runs them on a server with `storeKind: storeKindLog`, through to d
  waiting on m, being reserved again and running once more, also after a
  restart. Test 9's server outcome is a race: d's archive decides once the
  add's write finishes, and the server's mark of d then claims d's key as
  the archive does, so either may go first (13 of 30 unforced runs gave
  `archiveKeptLive`, 17 `archiveRemovedLive`, every one correct after).
  The test forces the archive first (`archiveRemovedLive`, the add putting
  d back); the mark first, giving `archiveKeptLive`, is the first case of
  `TestRunningDependentRerunArchiveRaces` (`rdaMarkedToRunAgain`), which
  now runs on both stores; its crash leaf stays bbolt-only until 1f.3
  gives the log store backups, which `rdrCrash` takes its image with.
  Spec C4 test 9 amended to allow either order.

- [x] (from 1c.4) A modify drops its old key's rglookups, as bbolt's
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
  1d.1 half done: history.bolt gains `keyRepgroups`, `repgroupToKey`'s
  reverse, and a fold of a modify record deletes its old key's folded
  rglookups through it, in log order with the fold's rglookups. Until the
  modify folds, the reducer notes the old key in `logState.unlooked` (not
  for a modify already folded), and `repGroupKeys` and the add dedup's
  `completedUnder` skip its folded rglookups. In-memory rglookups now carry
  their record's seq, so a fold of an older rglookup never drops a newer
  one of the same rep group and key (re-added after a modify the fold did
  not read). `TestStoreModifyDropsRepGroupLookups` runs each case with a fold
  before the modify and checks again once the modify folds and after
  reopen; `TestStoreFoldLookupAfterModify` makes the put-back, modify and
  delete while a fold of the original rglookup commits. The 1d.2 half
  stays open, and must also settle this: the folder drops a modify's old
  key's folded rglookups whenever it folds the modify record, while
  `applyModify` ignores a modify at or below its key's live spec seq. No
  live path writes such a modify today, but checkpoint replay (a modify
  replayed after a spec its checkpoint already holds) may reach it, and
  then history.bolt would lose rglookups the in-memory state keeps.
  Done in 1d.2: in Phase 1 there is no log between F and `walFrom`. A
  checkpoint folds to the writer's durable end, past its cut, before it
  merges (spec "Checkpoints", step 2), and F never moves back, so F is at
  or past the newest checkpoint's `walFrom`; recovery replays from
  `walFrom` and refuses an F before it as `errStoreCorrupt`
  (`TestLogStoreRecoverHistoryBeforeCheckpoint`, which puts back a
  history.bolt from before the checkpoint; spec "Recovery", step 3). Nor
  can a checkpoint make a modify meet a spec it would not meet in a full
  replay: the checkpoint is the reducer's state over the log before its
  cut, and replay applies the records after it in log order, so each
  modify after the cut meets the live spec seqs a replay of the whole log
  gives it, and no spec of its key with a higher seq is earlier in the
  log, as claims and `guardMu` make such a spec's write decide after the
  modify has applied. Phase 2's checkpoints, whose F can be before the
  cut, are 2b's (spec "Checkpoints", step 3).

- [x] (from 1c.4 review) `TestLogStoreFailedWriteNotReplayedAfterSpentFile`
  flakes about 1 in 500 under load, pre-existing (load-matched A/B at 1000
  runs: 2 fails at 4d128f96, 1 on the 1c.4 tree; files `[1 2]` instead of
  `[1 2 3]`). In `jobqueue/wal/writer.go` `run()`, if Close is seen before
  the writer's first reopen try, `reopen()` tries file 2, which fails and
  spends that number, and the loop returns without trying file 3.
  Owner: 1c.5: decide whether a close after a spent open must open file
  n+1 (a product fix) or the test must wait for the reopen before
  closing, with a red loop first.
  Done in 1c.5, as a test fix: red loop 3 fails in 1,200 runs (8 at once,
  -race), 0 in 1,200 after. The writer does what spec B2 says: retries
  while idle, and `Close` makes one last try; the residual window names
  a close whose last try fails, spent or not, as leaving the abandoned file
  unbounded. A close racing the first try is that case, not the test's
  (file 3 opened while idle), so the test now waits for `Durable()` to
  reach file 3 before closing.

- [x] (from 1c.4 review) No test pins that a delete over the block size
  limit splits only between keys (mutant `delete-not-grouped`, dropping
  `f.group()` per key in `deleteLiveJobs`, survives). Impact is small: a
  delete is one record per key. Owner: 1c.5: add a Convey under
  `logBlockMaxFrames = 1` asserting a 3-key delete gives 3 frames, like
  `TestLogStoreSplitPutBacks`.
  Done in 1c.5: `TestLogStoreSplitPutBacks` deletes 3 live jobs under a
  1 byte limit and requires 3 frames of 1 delete record each, and none live
  after reopen; `delete-not-grouped` is killed.

- [x] (from 1c.5 review) Two recovery behaviours have no test:
  `recoverIncompleteJobs` returning jobs in key order, as bbolt does
  (mutant `jobs-unsorted` survives), and `jobsOf` failing with
  `errStoreCorrupt` when a live ref's wal file is missing (mutant
  `missing-record-skipped`, unreachable until checkpoints delete old wal
  files). Owner: 1d.2: once checkpoints delete wal files, add a test that
  deletes a wal file holding a live ref after open and expects
  `errStoreCorrupt`, and pin the key order of recovered jobs (or state in
  the spec that callers must not rely on it).
  Done in 1d.2: `TestLogStoreRecoverMissingWAL` deletes the wal file
  holding the live jobs' records after open and requires
  `recoverIncompleteJobs` to fail with `errStoreCorrupt`, and requires the
  recovered jobs in key order (mutant `jobs-unsorted` killed). `liveJobs`
  now reads each file its Refs name, a checkpoint's frame by frame, so a
  missing file is `errStoreCorrupt` before `jobsOf`'s check of each live
  job's records, which stays; a mutant skipping either alone survives as
  the other still fails the recovery, and skipping both is killed.

- [x] (from 1d.1) E1 step 4 writes `history.bolt` (spec "Folder and
  history"), which now also has `keyRepgroups`, `repgroupToKey`'s reverse:
  a fold of a modify record finds the old key's folded rglookups only
  through it, so a converted store whose `keyRepgroups` is empty keeps a
  modified key under its old rep groups, as the log store did before 1d.1.
  Owner: 1f.2 (E1): write one `keyRepgroups` entry per `repgroupToKey`
  entry it converts, and add a modify-after-conversion check.
  Done in 1f.2: `storeBuild.copyHistory` (`store_convert.go`) writes
  `keyRepgroups` as `repgroupToKey`'s reverse, and
  `TestStoreConvertKeyChangingModify` converts a job completed and put
  back live in rep group A, modifies it to a new key in rep group B, folds,
  and requires A no longer to find it, before and after a reopen (mutants
  dropping or not reversing the entries are killed).

- [x] (from 1d.1) The folder paired a `complete` record with its key's
  spec (`logState.specOf`): the overlay entry's if the record applied as
  it, else the latest spec of its key read before it by the same
  `foldNow`, in any of its chunks (from 1d.1 fix round 2, so chunk
  boundaries did not matter: `TestStoreFoldSpecPrecedence` folds in chunks
  of 1 record), else the live spec. What remained was pairing across
  separate `foldNow` calls: a record that applied and was replaced by a
  later `complete` of its key before the folder read it (an archive, a
  re-add and a second archive within one fold interval), whose spec record
  an earlier `foldNow` read (or one before a restart), gets its key's
  current spec, whose ReqGroup may differ if the re-add changed it, so
  that one learned sample goes to the new ReqGroup, where bbolt gives it to
  the old one. Only that sample differs: the history entry is replaced by
  the later one either way, and no in-memory state depends on it. Owner:
  2b.2 (folder writes segments): carry the spec Ref of a replaced overlay
  entry until it folds, or accept and say so in the spec.
  Resolved by 1d.1 (fix round 3), as review round 3 showed the in-memory
  state did depend on it (next entry): the reducer keeps each replaced
  overlay entry until it folds (`logState.replaced`), so a `complete` that
  applied is always paired with the spec it applied with, whichever
  `foldNow`, chunk or restart reads it; one that has not applied yet gets
  the spec the reducer will give it, as before. No pairing difference
  remains. Review round 4 showed the spec records kept across chunks were
  then dead, and could grow past 100MB at 2M jobs: a `complete` that has
  not applied finds every earlier write of its key applied (claims), so
  its key's live spec is the one, or, if the key is not live, its write is
  an archive of a key not live, one block holding its spec record, which
  no chunk splits. Fix round 4 keeps only the chunk's own spec records
  (`foldRead.specs`). The open entries below route keeping
  `logState.replaced` to 2b.2 and swapping its spec Refs to 1d.2.

- [x] (from 1d.1 review round 4) A replaced overlay entry
  (`logState.replaced`) keeps the spec Ref of the `complete` record it
  applied as until it folds, and the folder reads that spec through it
  (`specOf`, then `readRecordBodies`). Once checkpoints delete wal files, a
  replaced entry whose spec Ref points into a deleted file makes every fold
  fail on `readRecordBodies`, and the folder never moves past it. Owner:
  1d.2 (C6): swap the spec Refs of replaced entries with the overlay's
  (spec C6 step 4 now lists them), and add a test that archives a job,
  re-adds it, archives it again before a fold, checkpoints past the first
  archive's spec and deletes its wal file, then folds both archives.
  Done in 1d.2: a checkpoint swaps the spec (and complete) Refs of every
  overlay and replaced entry below its cut, replacing the entry, as
  entries are never changed (`logState.swapRefs`).
  `TestLogStoreCheckpointReplacedSpecs` archives k, re-adds and archives it
  again after the checkpoint's cut but before its swap
  (`logCheckpointFoldedHook`), so the first archive's replaced entry pairs
  with k's spec in the wal file the checkpoint deletes, then folds both
  archives and requires the second's completion, also after reopen
  (mutant `replaced-not-swapped` killed: the fold fails reading the
  deleted file).

- [ ] (from 1d.1 review round 4) The Phase 2 folder pairs each `complete`
  record with the spec it applied with through `logState.replaced`, so
  when the folder writes segments it must keep `replaced` and drop its
  entries only as they seal, and the Phase 2 checkpoint must hold one spec
  per overlay or replaced entry, not one per key, as a re-add or modify can
  change a key's spec between two of its `complete` records (spec C6 step
  3 now says so). Owner: 2b.2: keep `replaced` in the segment folder, and
  cover C6 test 2 with a key re-added under another spec between two
  `complete` records in the open segment.

- [x] (from 1d.1 review round 3) Memory diverged from replay: a job added
  under rep group rg1 and req group q1 and folded, archived (C1), added
  again under rg2 and q2 and archived again (C2), with one fold then
  reading C1, the re-add's spec and C2, paired C1 with the re-add's spec,
  since C2 had replaced C1's overlay entry and C1's spec was before F. So
  C1's EndTime went to rg2, `overlayEnds[rg1]` was never dropped, and after
  a reopen rg1 was missing from `retrieveLastCompletionTimeByRepGroup`,
  `repGroupHasHistory(rg1)` was false and q1 learned no RAM, where bbolt
  gives 300. A `wr mod` changing the rep group of a kept-live job between
  two archives did the same. Owner: 1d.1. Done in 1d.1 (fix round 3): see
  the previous entry. `TestStoreFoldReplacedElsewhere` moves the job
  through three rep and req groups, by re-adds and, kept live, by modifies
  (archived twice under the first, so two replaced entries share a spec),
  each with one fold of every archive, with two folds (the first to the
  log's durable end noted after the first archive, made once later ones
  have replaced it, which must drop that archive's replaced entry from
  memory while the current one stays, from fix round 4) and with a reopen
  before any fold of them, and
  requires bbolt's last completion times, history, learned requirements
  and sample count before and after reopen, with nothing left unfolded.

- [ ] (from 1d.1) The log store loads its whole history key index from
  `completeSeq` at open (spec "In-memory state of logStore", from 1c.3),
  so its startup and heap grow with the number of completed keys, which
  DEVELOPERS.md rule 6 forbids for bbolt's startup: measured at about
  0.25µs per key (62ms at 250,000, `TestLogStoreFastStartupNoHistoryScan`),
  so roughly 0.5s, and tens of MB of map, at prod's 2.15M completed jobs.
  It decodes no history record, so it is far from the cold scan rule 6
  was written for, but nothing measures it at prod scale yet. Owner: 1f.8
  (F3, dbstart over the new store): time `openStore` and its heap on a
  store converted from a prod-sized database, and if either is too much,
  route a lazier index (seqs read from `completeSeq` on demand) back to
  the spec.

- [x] (from 1d.1 review) Folds were unbounded: a fold read the whole log
  from F to the durable end, so after a backlog (150,000 archives from 64
  writers) one fold of 158,000 records held the store's write lock for
  86ms in `foldHistory` and its read lock for 22ms in `specRefs`, and the
  slowest archive rose from 24-36ms to 124-361ms. Owner: 1d.1. Done in
  1d.1 (fix round 1): `foldNow` folds in chunks of `foldChunkRecords`
  (4,096) records, each ending at a frame and committed with the folded
  position it moves to in a transaction of its own, until it reaches the
  durable end it started for (`TestStoreFoldChunks`, which crashes between
  two chunks and reopens). `foldHistory` changes the in-memory state in
  batches of `foldApplyBatch` (1,024), each under the lock: the folded
  position and history index first, then the modify drops, then the
  overlay's completions and rglookups, so no answer has a gap between
  them (`TestStoreFoldBeforeApply`, `TestStoreFoldTwoModifies`). One block,
  such as a 150,000-job add's, is still folded in one transaction, since
  the folded position cannot fall inside a block; only its lock holds are
  batched. Interleaved A/B against 875f784e (no folder), 150,000 archives
  from 64 writers: slowest archive 77-85ms against 6-24ms, p99 11-14ms
  against 3.1-3.3ms, throughput 16-21% lower. Lock holds are now at most
  about 3ms; the remaining slowest archive is the one transaction folding
  the probe's 150,000-job add block, whose writes delay the log's syncs
  (28-57ms with that add folded before the archives). p99 and throughput
  are the folder's own work (reading spec blocks, decoding and encoding
  jobs, bbolt writes) on the same CPUs and disk: with the folder paused
  the build matches 875f784e.

- [x] (from 1d.1 fix round 1) `retrieveRepGroups` (rep groups ever added,
  spec D1) lost a rep group on the log store when a modify dropped its only
  rglookup before the folder folded it: the reducer's `dropLookups` removed
  it from the in-memory rglookups (from 1c.4), and the folder's net of the
  rglookup and the modify put nothing into `repgroups`, where bbolt's add
  keeps it there for good. Owner: 1d.1. Done in 1d.1 (fix round 1): the
  reducer keeps the rep group of each rglookup not yet folded in
  `logState.lookedUpRGs`, which a modify does not drop and a fold of that
  rglookup does, and a fold puts every rglookup's rep group into
  `repgroups`. `TestStoreModifyDropsRepGroupLookups` moves the modified job
  to rep group B and requires A still listed, on bbolt and on the log store
  before the modify folds, after and after reopen.

- [x] (from 1d.1 review round 2) The folder takes the spec Refs of a
  chunk's `complete` records (`foldRead.specRefs`) under the store's read
  lock, then reads those records' frames with no lock
  (`readRecordBodies`), and reads wal files listed once per chunk
  (`walFileNumbers`). Nothing deletes a wal file yet, so this is safe
  today; once checkpoints swap Refs and delete wal files, a checkpoint
  between the two can delete a file a Ref names, and the fold fails with
  an error until it retries. A rotation during a fold is the other half:
  `readFiles` skips files after its durable file (`n > to.file`), which
  `TestStoreFoldStopsAtItsDurableFile` pins by reading to a durable end
  noted before a reopen started a new file, but no test races a real
  fold with a rotation or a checkpoint. Owner: 1d.2 (checkpoints): give the
  folder a reader hold that keeps the files it reads until its fold ends,
  or add a test of a fold racing a checkpoint, with a rotation during the
  fold, that shows the fold succeeds or retries to the same history.
  Done in 1d.2, both: every reader of records through Refs, the fold's
  `specRefs` included, takes a hold (`holdRefs`) while it copies them under
  the store's lock and releases it once it has read them, and a checkpoint
  deletes files only once no reader holds a generation from before its
  swap (spec "Checkpoints", step 4). The wal files a fold scans are at or
  after F, which deletion never reaches.
  `TestLogStoreCheckpointWaitsForFoldReads` holds a fold after it took k's
  spec Ref, below the cut, and requires the checkpoint to wait, k's wal
  file kept, until the fold has read it, then both to succeed and k's
  completion folded (mutant `no-reader-wait` killed).
  `TestLogStoreCheckpointRotatesDuringFold` rotates the log with a
  checkpoint while a fold of 10 archives is reading, and requires the
  checkpoint to wait for that fold, which folds them all, also after
  reopen.

- [x] (from 1d.1 review round 3) Close waited for a whole backlog's fold:
  `stopFolder` waits for the folder goroutine, whose `foldNow` folded every
  chunk to the durable end it started for. Owner: 1d.1. Done in 1d.1 (fix
  round 3): `foldNow` returns `errDBClosed` after the chunk it is folding
  once close has asked the folder to stop, which `foldRound` does not count
  or log as a failed fold; each chunk commits with its folded position, so
  this is the crash-between-chunks case and the next open folds the rest.
  `TestStoreFoldStopsAtClose` closes while the folder reads its first chunk
  of several (`logFoldReadHook`, from fix round 4, so that a close
  discarding the chunk it read fails), and requires that chunk committed and
  no other, no failed-fold line, and a reopen that holds that chunk and folds
  the rest.

- [x] (from 1d.1 review round 3) `TestReqStatCache` never asserted that a
  reload learning the same value leaves `reqStatChanges` alone, so a
  `reqStatCache` that bumped `changes` on every load passed (mutant
  `changes-every-load`). The code moved to `reqstat_cache.go` in 1d.1 and
  both stores share it, so the log-versus-bbolt comparisons cannot catch
  it. Owner: 2b. Done in 1d.1 (fix round 3): `TestReqStatCache` reloads an
  unchanged req group and requires the stamp unchanged, then learns a new
  value and requires it raised.

- [x] (from 1d.1 review round 6) A rep group's latest overlay EndTime
  (`logState.overlayEnds`) can differ between memory and a replay of the
  log, though every answer agrees. Memory drops a rep group's entry once a
  fold commits a completion as late, as spec "In-memory state of
  `logStore`" allows, while a replay rebuilds it from the completions after
  F, which may include an earlier-ending one still unfolded; history.bolt's
  later EndTime then gives the same answer. Owner: 1d.2 (C6): checkpoint
  and recovery comparisons must compare `overlayEnds` by its answers
  (`retrieveLastCompletionTimeByRepGroup`, `repGroupHasHistory`), not
  field by field, or the checkpoint must canonicalise it, as memory may
  legitimately differ from replay.
  Done in 1d.2: `comparableLogState` leaves `overlayEnds` out, and every
  comparison of memory with a replay (`soStateLikeReplay`, C5 test 1) also
  compares `overlayEndAnswers`, each rep group's last completion time and
  `repGroupHasHistory`. `TestLogStoreReplayDiffersOnlyInAnswers` folds a
  completion ending at 200 while one of its rep group ending at 100 applies
  after the fold read the log, and requires memory without the entry, a
  replay with 100, and the same answers (comparing field by field fails).

- [x] (from 1d.1 review round 7) The order of a key's rep groups in
  `logState.keyRepGroups` follows the order its rglookups applied in, so
  under concurrent writes it can differ from a replay's, which applies them
  in log order, when a key has two or more rep groups. Every answer
  agrees, as the slice is used as a set. This predates 1d.1 (from 1c).
  Owner: 1d.2 (C6): checkpoint and recovery comparisons must compare
  `keyRepGroups` as a set, or the checkpoint must normalise its order.
  Done in 1d.2: `comparableLogState` sorts each key's rep groups.
  `TestLogStoreReplayDiffersOnlyInAnswers` looks a key up under a, then b
  and a again after a fold read its first lookup, and requires [a b] in
  memory, [b a] in a replay and the states equal as sets (comparing in
  order fails).

- [ ] (from 1d.2) Recovery step 4 makes "no valid checkpoint"
  `errStoreCorrupt`, but stores are still created with no checkpoint, so a
  store with no checkpoint file replays every wal file, as before 1d.2;
  only checkpoint files of which none has a valid trailer are
  `errStoreCorrupt`. Owner: 1f (E2), which creates a new store with an
  empty checkpoint: make a store with none `errStoreCorrupt` then.

- [ ] (from 1d.2 review) A checkpoint writes its whole `.ckpt` (about
  40-180MB at production's live set) through one buffered writer with no
  pacing, then makes it durable with a single fsync, on NFS. That is the
  burst pattern that starved the bbolt committer's fdatasync before, which
  `backupCopyWriter` fixed by forcing writeback of the backup copy every
  `syncEvery` bytes. Here it could delay the wal writer's syncs, and so
  reservations, while a checkpoint runs. Owner: 1g.5 (daytime NFS
  targets): make sure checkpoints run during the stall-free soak and check
  the `"reservation wait"` p99 of the lines covering them; if any is above
  50ms, pace the checkpoint's write as `backupCopyWriter` does. A soak at
  test scale writes `.ckpt` files far smaller than production's, so also
  measure that p99 while a checkpoint of a production-sized live set (a
  `.ckpt` of 40MB or more) is written on the NFS work directory, such as
  with checkpoints forced while the soak runs against a store seeded with
  that many live jobs.

- [x] (from 1d.2 review round 6) Recovery step 1 deletes every `*.tmp`
  (`removeTmpFiles`, `jobqueue/store_checkpoint.go`), but only a
  `.ckpt.tmp` is tested, so deleting only checkpoint temp files would pass.
  No effect in Phase 1, where the only other `.tmp` is `VERSION.tmp`,
  which `createStoreDir` rewrites. Owner: 1f.4 (E2), before conversion and
  compaction write other `.tmp` files: a reopen test that plants a
  non-checkpoint `x.tmp` and requires it gone.
  Done in 1f.4: `TestLogStoreRecoverRemovesTmpFiles` (`store_recover_test.go`)
  plants `x.tmp` in a closed store and requires a reopen to remove it.

- [x] (from 1d.2 review round 6) `dropBefore` (`jobqueue/store_records.go`)
  zeroes `stateSeq` but keeps the old state Ref. The swap and the
  checkpoint's copy both skip a state whose `stateSeq` is 0, so that Ref
  would stay below the cut and dangle once its file is deleted. It cannot
  happen today, as every spec write carries a state record. Owner: 1f.4
  (E2): zero the Ref there, as `clearStd` does for std, with a test.
  Done in 1f.4: `dropBefore` zeroes the state Ref with its seq;
  `TestLogLiveDropBefore` (`store_log_test.go`) fails without it.

- [ ] (from 1e.1 review round 1 fix) Fixing 1e.1's limit-group leak on an
  abandoned reservation also fixed one that predates 1e.1: a released job
  reserved again before `finalizeReleasedJob` gave back its limit groups
  lost a slot of each group until restart. `runEndCallback` now gives them
  back inside the `ReleaseWith` callback. Owner: 2e.2 (F6): add a
  CHANGELOG line under Fixed.

- [x] (from 1e.1 review round 1 fix) `jobqueue/wal`
  `TestWriterReopensAfterAbandon` failed once in `make race` under heavy
  load, seeing segment files {1,2} instead of {1}
  (`writer_test.go:1300`); 0 failures in 100 runs alone and in a
  load-matched A/B of 120 runs a side. Each background reopen try creates
  segment 2 (`writer.go:775`) and removes it only after its header sync
  fails (`writer.go:785`), so the test can list the files while a try is
  in flight. Owner: 1e.4, which runs the suite on the log store: make the
  test wait for the tries to settle (or the writer not leave a visible
  segment), and show it with a hook that holds a try in flight.
  Done in 1e.4: the test's sync hook holds each reopen try in flight for
  20ms, so the listing at the 4th try always sees it (5 of 5 runs failed
  {1,2} without the wait), and the test waits for segment 2 to go before
  listing (20 of 20 pass).

- [x] (from 1e.1 review round 2) `TestStoreReserve*` assert the reserve
  bound as `reserveTestRequestTimeout / reserveDurableWaitFraction`, so
  changing the fraction passes (`store_reserve_test.go`); the spec says
  "800ms (bound 200ms)". Owner: 1e.4: assert a literal 200ms.
  Done in 1e.4: `reserveTestBound` is a literal 200ms, which
  `TestStoreReserveStalled` asserts `reserveDurableWait()` equals.

- [x] (from 1e.1 review round 2) An abandoned reservation records the job
  ready with the previous attempt's Exitcode, FailReason, StdErrC, PeakRAM
  and similar fields cleared (`resetJobForReservation` runs before the
  persist wait), so `wr status` loses why a retrying job last failed until
  it runs again. Before 1e.1 such a reservation was handed out anyway, so
  this only shows in the stall regime, but user-facing behaviour must not
  change. Owner: 1e.4: keep the earlier attempt's details on an abandon
  (restore them in the abandon, or clear them only after the reservation
  is durable), with a test, and make spec C3's "does not undo the rest"
  say so.
  Done in 1e.4: `resetJobForReservation` returns the run's `runDetails`
  from before its reset and the abandon's change gives them back
  (`unreserveChange`), so they are in its state record too
  (`TestStoreReserveAbandonKeepsLastAttempt`, on both stores, through
  `GetByEssence` and after a restart, with every restored field set in the
  failed attempt); spec C3 step 4 and "does not undo the rest" amended.
  The std tails it gives back are the ones a touch left on the manager's
  job, which its state records carry; `wr status` reads a ready job's std
  from the store, so the test asserts those on the manager's job.

- [ ] (from 1e.1 review round 2) Two abandon details are untested: the
  `discardJobChangeAhead` after a failed `ReleaseWith`
  (`serverCLI.go`, only reachable when the queue is destroyed during a
  stalled reservation at shutdown; on bbolt a missing discard makes close
  wait its 1-minute timeout), and that the abandon clears the job's note
  of its limit groups (it uses the shared helper, which does). Owner: 2e.1
  (F5), which deletes the bbolt path: add tests for whichever still apply.

- [x] (from 1e.2 review) `TestConfigRemovedKnobsAbsentFromSource`
  (`internal/config_test.go`, `skipNonModuleDir`) walks `.tmp/` and other
  dot-directories, which are gitignored, hold agents' scratch files and
  are not part of the module, so a stray `.go` file there fails the test.
  Owner: 1e.4: skip directories whose names start with "." or "_", as the
  go tool does.
  Done in 1e.4: `skipNonModuleDir` skips them; a stray `.tmp/agent/*.go`
  naming a knob failed the test before and passes after.

- [ ] (from 1e.2 review) `developers/soak/anyway.py` (and
  `developers/soak/README.md` where it describes it) still describe the
  pre-C3 behaviour: `ReserveWriteWait`, `serverReserveWriteWait()` and
  reservations "handed out anyway". It still works on soak trees from
  before customdb. Owner: 1f.9 (F4): say it applies only to trees from
  before C3.

- [ ] (from 1e.2 review) F6 lists only the two removed config keys for
  the CHANGELOG, but 1e.1 and 1e.2 also removed exported Go API that
  library users may set: `jobqueue.ServerTimings.DBBatchDelay`,
  `DBBatchSize` and `ReserveWriteWait`, and `jobqueue.ServerDBBatchDelay`
  and `ServerDBBatchSize`. Owner: 2e.2 (F6): list them in the CHANGELOG
  under Changed (removed Go API).

- [x] (from 1e.3 review round 3) `TestStoreModifyRace`
  (`jobqueue/store_modify_race_test.go`) never asserts that each round's
  modifies changed any keys, so a modify that silently did nothing would
  pass with zero lost persists; and `installModifyRaceEntry` sets
  `modifyAppliedHook` before registering its `Reset`. Owner: 1e.4: assert
  each round's keys differ from the keys before it, and register `Reset`
  first.
  Done in 1e.4: both; with both modify paths made no-ops the new
  assertion fails on kept keys (scratch-copy mutant).

- [x] (from 1e.4 review round 2) `TestConfigRemovedKnobsAbsentFromSource`
  (`internal/config_test.go:123`) fails when another package's test
  removes a directory in the module tree during the walk (e.g.
  `jobqueue/scheduler/scheduler_test.go:1377` makes and removes
  `./wr_schedulers_test_output_dir_*`): the `WalkDir` callback returns
  every error. Seen once in `make race`; reproduced 41 and 54 times in
  `-count=100` with such directories churning, on the 1e.4 tree and on
  1e.2's. Owner: 1e.5: ignore `fs.ErrNotExist` in the callback, with a
  red test first.
  Done in 1e.5: the callback, and its read of each `.go` file, skip a
  path that is gone. Red: 44 of 100 runs failed with a script making and
  removing directories in the module tree; green: 0 of 100 under the same
  churn.

- [x] (from 1e.4 review round 2) `jobqueue/store.go:42` still says "until
  item 1e.4 makes the log store the default" (now 1f.11); and in the bolt
  branch of `startAbandonTestManager` (`jobqueue/store_reserve_test.go`)
  `Reset(drains.uninstall)` is registered after the manager starts, so a
  failed start leaves `bestEffortSwappedHook` installed. Owner: 1e.5.
  Done in 1e.5: the comment names 1f.11; `startReserveTestManagerThen`
  registers one `Reset`, before the manager starts, that stops the
  manager if it started and then calls `afterStop`, which the bolt branch
  passes `drains.uninstall`, so the hook goes on a failed start too and
  still only after every store is closed.

- [x] (from 1e.5) The log store makes no backups yet, so with
  `WR_TEST_STORE=all` or `log` these skip on it (`conveyNeedingBackup`,
  `jobqueue/store_inspect_test.go`): `TestStoreInspector`'s "a forced
  backup holds what the store held" leaf and all of
  `TestStoreCorruptRestoredFromBackup`. `storeLockSeeded`
  (`jobqueue/store_lock_test.go`) backs up only bbolt, whose backup
  `TestStoreCorruptRestoredFromBackup` restores and `TestStoreOpenLocked`
  checks a locked-out open leaves alone. `recordWorkload`
  (`jobqueue/store_record_test.go`) requires the log store's `backup` to
  fail with `errLogStoreBackup`. Owner: 1f.3 and 1f.5 (E4, E5): make
  `conveyNeedingBackup` return `Convey` for every kind, make
  `storeLockSeeded` back up every kind, and require `backup` to succeed in
  `recordWorkload`.
  Done in 1f.3: `recordWorkload` requires `backup` to succeed, and
  `storeLockSeeded` backs up every kind, the log store's backup being its
  self-contained tar (`storeWipeSeeded` swaps that for a copy of the store
  directory, the backup directory E3 test 1 names). `conveyNeedingBackup`
  still skips both tests on the log store: restoring the tar
  `TestStoreCorruptRestoredFromBackup` needs is E2's (owner 1f.4), and the
  log store's `forceBackup`, an incremental tick, returns
  `errLogStoreNoForcedBackup` until E4 (owner 1f.5). The last of 1f.4 and
  1f.5 makes `conveyNeedingBackup` return `Convey` for every kind.
  Done in 1f.4: `TestStoreCorruptRestoredFromBackup` runs on every store kind,
  restoring the log store's self-contained backup; `conveyNeedingBackup` now
  guards only `TestStoreInspector`'s forced-backup leaf, which 1f.5 un-skips.
  Done in 1f.5: the log store's `forceBackup` makes an incremental backup
  tick (spec E4), so `errLogStoreNoForcedBackup` and `conveyNeedingBackup`
  are gone, and `TestStoreInspector`'s forced-backup leaf runs on every store
  kind, opening the backup directory the tick writes through
  `openLogBackupFileInspector`, which now accepts a backup directory as well
  as a self-contained file.

- [x] (from 1e.5) A replay against the log store must fold before each
  probe of its learned requirements (spec A5), or every replay leaf of
  `TestStoreReplayLearned` (`jobqueue/store_record_test.go`) mismatches on
  it. The test wrappers there embed `store`, so a replayer cannot find the
  log store's `foldNow` through them by a type assertion. Owner: 1g.2.
  Done in 1e.5: the replayer's `fold`, if set, runs before each probe
  (once per write for `probeAllLearned`), and its error fails the replay;
  `replayRecordingFolding` sets it to the inner store's `flush`. The
  leaves run on both stores; with no fold, 8 of the 9 fail on the log
  store (the alternating-refresh leaf still finds its mismatch).

- [x] (from 1e.5) After damage that truncates the wal before the folder's
  position, a job already folded into `history.bolt` can recover both
  complete (from history) and live at an earlier state (from older wal
  records). A SIGKILL cannot produce this, and A4 test 2 does not check
  for it. Owner: 1f.4 (E2): decide what recovery does when history holds
  a key the replayed wal also holds live, and test it with damage that
  cuts the wal before the folded position.
  Done in 1f.4: decided that history outranks the damaged log, rather than
  refusing the store as corrupt, which spec A4 test 2 forbids (its random cut of
  the last wal file must recover; `TestStoreCrash` failed with
  `WR_TEST_STORE=all` when this was tried). Recovery drops a live entry whose
  spec, state and std records all precede its key's completion
  (`dropFoldedLive`, `store_records.go`, on every open, so the drop needs no
  record of its own): an archive follows its complete record with a delete or,
  keeping the job to run again, a state record. When the log's valid end is
  before F, `rewindFoldedTo` (`store_log.go`) also durably moves F back to the
  log's end (`historyBolt.rewind`), so writes after the reopen, which can land
  in a file of F's number again once a torn last file is removed, are folded
  rather than taken for folded; the log's end is never before the checkpoint's
  `walFrom` file, as the scan starts there, and a removed torn `walFrom` file
  ends the log at its start (review round 1, B2: it had read as offset zero
  of file zero, `errStoreCorrupt`). Spec Recovery step 4 says so.
  `TestLogStoreRecoverTornWalFromHeader` cuts a checkpoint's header-only
  `walFrom` file inside its header.
  `TestLogStoreRecoverWALCutBeforeFolded` cuts the file holding the completion
  after its header (the job complete, not live, F rewound and logged) and inside
  its header (the same, kept across reopens, F not rewound again, and a job
  completed after the reopen complete after another).

- [x] (from 1e.5 review) A last wal file whose header is full length but
  fails its CRC, with records after it, is taken as torn and removed with
  only a warning (`settleWALEnd`, `case limit < 0 && end == 0`, and
  `walHeader` in `jobqueue/store_recover.go`). Flipping one byte at
  offset 12 of an 8972-byte wal holding 50 acked adds, the reopen
  succeeded and recovered 0 of the 50. A crash cannot do this, since no
  record is written before its header is durable, so spec Recovery step
  4's "holds nothing durable" is wrong when bytes follow the header.
  Owner: 1f.4 (E2): with a red test first, make a whole-length header that
  fails its CRC with bytes after it `errStoreCorrupt` (restore from
  backup), count only a short or zero-filled header as torn, and amend
  Recovery step 4.
  Done in 1f.4: red first in `TestLogStoreRecoveryFileRules`
  (`store_log_test.go`). `checkTornHeader` (`store_recover.go`) takes a last
  file with no whole header as torn only if nothing but zeros follows a header
  frame's length; otherwise it is `errStoreCorrupt` (review round 1, N5: it had
  counted the header's own bytes too, unlike the spec). Spec Recovery step 4
  amended, and spec A4 test 2 and `damageCrashFile` (`store_crash_test.go`) now
  flip a byte only after the header frame, as a flip inside it is now
  corruption, as a flip of its length already was.

- [ ] (from 1e.5 review round 3) Log-store methods other than
  `checkIfComplete` and `checkIfLive` that take keys through
  `parseLogKeys` (for example `retrieveCompleteJobsByKeys` in
  `jobqueue/store_log.go`) return an error for a malformed key, where
  bbolt returns no match. Also `checkIfComplete` lacks the
  `//nolint:nilerr` that `checkIfLive` carries on the same
  `return false, nil`; make the two match. Owner: 1f.11 (the switch):
  trace whether a client can pass these methods unchecked keys, and if so
  match bbolt with a test on both stores.

- [x] (from 1f.1 review) `initDB`'s bbolt corrupt-restore path still
  removes with plain `os.Remove`: the temporary S3 download at `bkPath`
  (`jobqueue/db.go` line 1525) and `dbFile` before copying the backup over
  it (line 1543). Once `dbFile` or the backup can be a store directory,
  these fail on a non-empty directory. Owner: 1f.4 (E2): its rewrite of
  the restore path makes both removals recursive, covered by E3 tests 4
  and 5.
  Done in 1f.4: both are `os.RemoveAll`. The log store does not use `initDB`;
  its restore (`store_open.go`) removes `.restoring` and `.corrupt` recursively,
  covered by E3 tests 4 and 5 (`TestStoreRestoreRemoves`). For bbolt neither
  path can hold a directory (a directory at `dbFile` is `ErrDBUnreadable`), so
  no bbolt test can tell the two apart.

- [x] (from 1f.1 review round 3) The fake S3's `DownloadFile`
  (`jobqueue/store_wipe_test.go` line 176) differs from the real one,
  which is minio v7.3.0's `FGetObject` (muxfys v5.1.1 `s3.go` line 286).
  For a missing key the fake returns an error wrapping `os.ErrNotExist`;
  minio returns an `ErrorResponse` with code `NoSuchKey` from its
  `StatObject` (`api-get-object-file.go` line 66,
  `api-error-response.go` lines 154 to 160), which `errors.Is` does not
  match to `os.ErrNotExist`. The fake writes `dest` directly; minio first
  creates any missing parent directories of `dest` (lines 57 to 63, before
  the key is checked, so even a missing key leaves them), then stages the
  object in `dest`'s directory as
  `sha256hex(base(dest) + etag) + ".part.minio"` (line 72) and renames it
  to `dest` (line 143). On an error return it removes the part file (lines
  83 to 89), but a manager killed mid-download leaves it, and the next
  download of the same object appends to it (lines 75 and 99 to 100). E3
  test 5 ("no downloaded file remains anywhere") passes on the fake
  without covering these. Owner: 1f.4 (E2, E3 test 5): make the fake
  create parent directories and stage to a `.part.minio` file as minio
  does, return a `NoSuchKey`-coded `minio.ErrorResponse` for a missing
  key, and have restore handle both, removing any directories it caused
  to be created and any stale `.part.minio` beside the download.
  Done in 1f.4: the fake creates `dest`'s parents first, stages to the
  minio-named `.part.minio` file (appending to a stale one), renames it, removes
  it on a failed download, and returns a `NoSuchKey` `minio.ErrorResponse` for a
  missing key. Restore downloads only inside `.restoring`, which it removes on
  any error and open order step 2 removes before restoring, so parents and part
  files go with it; `TestStoreRestoreRemoves` and `TestStoreOpenRestoreS3` cover
  a stale part file, a failed download and a `NoSuchKey`.

- [x] (from 1f.1 review round 6) No test covers which S3 key `initDB`
  downloads from for a development manager with `forceBackups`: changing
  its `s3BackupLocation(dbBkFile, deployment)` to `internal.Production`
  survives, so such a manager could restore the production backup. The
  gap predates 1f.1, which only moved the code. Also, the dev-wipe tests'
  fake S3 holds keys only one level below the backup prefix
  (`key/parts/x`), so a recursion that stops below one nesting level
  survives. Owner: 1f.4 (E2): when restore moves onto `backupTarget`,
  assert a development restore reads the `.development` key, and add a
  deeper key such as `key/a/b/c` to the wipe test's objects.
  Done in 1f.4: `TestStoreOpenRestoreS3` requires a development manager with
  `forceBackups` to download only the `.development` key's objects (and one
  without to not reach S3), and the wipe test's objects include `key/a/b/c`.
  `initDB`'s own S3 download stays covered only for production, as the log
  store's restore is the path that replaces it.

- [x] (from 1f.2) E1 says a corrupt source is restored as E2 order step 8,
  with today's "corrupt (?) db file" message. 1f.2 stops short of the
  restore, which 1f.4 builds: `convertBoltDB` (`store_convert.go`) renames
  a corrupt bbolt file back to `dbFile` if step 3 moved it, removes
  `.converting`, and returns an error wrapping `errStoreCorrupt` with the
  cause. Only an error or panic of bbolt's reading the source counts: step
  0's `openBoltSource` and `boltSourceSchema`, and step 4's reads through
  `sourceBucket`, `eachSource` (with `sourceMove`) and `sourceGet`. Step 4
  reads `jobslive` and `jobrunstate` again through `eachLiveJobTx`, which is
  unguarded, so `countJobs` walks both through `eachSource` first, in the
  same transaction. `TestStoreConvert` and `TestStoreConvertPanickingSource`
  cover the rename-back, a corrupt meta pair from step 0, a zeroed branch
  page, stdout bucket root and run-state bucket root in step 4, and a
  claimed error (`ErrDBLocked`) before and after step 3, neither of which
  renames the bbolt file back.
  These failures do not wrap `errStoreCorrupt`, and 1f.4 must not restore
  on them:
  - A record bbolt reads that the converter cannot (an undecodable live or
    complete job, a non-job key in `jobslive` or `jobscomplete`, a limit
    under 8 bytes) wraps `errBoltUnconvertible`. The bbolt file is renamed
    back and `.converting` removed, as for a corrupt source.
  - A `SOURCE` naming no pre name of `dbFile` wraps `errBadConvertSource`,
    and nothing is changed.
  - Any other failure (a write error on the new store, a cancelled
    context, a panic in the converter's own code, wrapping
    `errConvertPanicked`) leaves `.converting` and `SOURCE` for the next
    start to resume.
  Done in 1f.4: `openLogStoreAt` (`store_open.go`) restores only on
  `errStoreCorrupt`. `TestStoreOpenRestoreBoltDBFile` covers E2 tests 11 to 13
  and, with a backup present, an unconvertible file, a bad `SOURCE` and a
  resumable failure (which leaves no `dbFile`) restoring nothing.

  `TestStoreConvert` covers each, including a crash between the rename-back
  and the removal and a resumed conversion failing the same way again.
  Owner: 1f.4 (E2 tests 11 to 13): route only an `errStoreCorrupt` from the
  conversion to restore with today's message, and with no backup return
  the error, as E2 order step 8 says. Return the other three errors with
  `dbFile` untouched, and add a test that a backup present beside an
  unconvertible bbolt file, a bad `SOURCE` and a resumable failure is not
  restored from.

  Decision (orchestrator ruling for the owner, 1f.2 review round 1): an
  undecodable live record or a non-job key is not a corrupt source. bbolt
  reads such a file without error, and today's bbolt start fails on an
  undecodable live job and leaves the file alone, so the conversion does
  the same rather than restoring over it and deleting the only copy. An
  undecodable complete record fails the same way rather than being copied
  as it is, as compaction copies it: E1 drops every complete record's std,
  which needs it decoded. This is stricter than bbolt's start, which never
  decodes complete records; the owner may prefer copying it as it is.

- [x] (from 1f.2 review round 4) No Phase 1 code reads
  `checkpointHeader.histFrom`: it is only written, by the checkpoint
  writer (`store_checkpoint.go`) and by E1 step 4's converted checkpoint
  (`store_convert.go`), so a mutant writing the converted checkpoint's
  `histFrom` as zero survives. Owner: 1f.5 (E4), its first reader: assert
  that a converted store's checkpoint has `histFrom` at
  `(convertWalFrom, 0)`, or that a backup tick ships the converted store's
  checkpoint.
  Done in 1f.5: a backup tick reads a checkpoint's `histFrom` to decide
  whether to ship it (`shipNewCheckpoint`, `store_backup_tick.go`), and
  `TestLogStoreIncrementalBackupOfConverted` asserts that a converted store's
  checkpoint has `histFrom` at `(convertWalFrom, 0)` and that its first tick
  ships that checkpoint.

- [x] (from 1f.2 review round 4) No test puts a regular file that is not
  bbolt at `dbFile`, so a `hasBoltMagic` (`store_convert.go`) that never
  compares the magic survives: it takes any file of 20 bytes or more for
  bbolt, and the conversion then fails wrapping `errStoreCorrupt` instead
  of the open failing on `VERSION: not a directory`. Neither loses data.
  Also, `hasBoltMagic` treats any read error, an `EIO` included, as "not
  bbolt" rather than returning `ErrDBUnreadable`. Owner: 1f.4 (E2, which
  owns how anything else at `dbFile` is classified): test that a ustar tar
  and a garbage regular file at `dbFile` are not taken for bbolt, and
  decide whether a read error there is `ErrDBUnreadable`.
  Done in 1f.4: a tar at `dbFile` is restored from itself (E2 test 3, with a
  different backup present), and 4KiB of random bytes is restored as
  unrecognised, not as a corrupt bbolt source (the message says so). Decided: a
  read error there that `openErrorThatIsNotCorruption` claims, such as `EIO`, is
  `ErrDBUnreadable` (`hasBoltMagic` and `hasTarMagic` read through
  `readFileAt`); a test reads `/proc/self/mem` through a symlink at `dbFile` and
  requires `ErrDBUnreadable` with nothing restored.

- [x] (from 1f.2 review round 5) No test makes `abandon`
  (`store_convert.go`) fail to remove `.converting`, so a mutant that
  ignores the `os.RemoveAll` error survives. In that double fault (a
  corrupt source whose `.converting` cannot be removed) the mutant returns
  the error wrapping `errStoreCorrupt` while `SOURCE` remains, where the
  code returns the cause joined with the removal error. Once 1f.4 restores
  on `errStoreCorrupt`, the mutant's next start would resume on a `SOURCE`
  whose pre file the restore had replaced, and stay stuck there, though no
  data is lost. Owner: 1f.4 (E2, which routes `errStoreCorrupt` to
  restore): decide what this double fault returns, then test it by making
  `.converting` unremovable (for example mode 0500) before a corrupt-source
  abandon, asserting the error does not wrap `errStoreCorrupt` and
  `SOURCE` is kept, or whatever 1f.4 rules.
  Done in 1f.4: decided to keep what it returns: the cause joined with the
  removal error, not wrapping `errStoreCorrupt`, so nothing is restored and
  `SOURCE` stays (spec E2). A leaf of `TestStoreOpenRestoreBoltDBFile` makes
  `.converting` mode 0500 at `convertAtRestored` and asserts that, then that the
  next open, once it can be removed, restores.

- [x] (from 1f.2 review round 6) `hasBoltMagic` (`store_convert.go`)
  checks only meta page 0's magic, but bbolt falls back to meta page 1: a
  probe zeroed page 0 of a bbolt file, which bbolt still opens (read-only
  and through the bbolt store), while `isBoltFile` says false and the log
  store's start fails on `VERSION: not a directory`, leaving the file
  untouched. No data is lost in 1f.2, but under E2 step 8 ("unrecognised"
  restores from backup) 1f.4 would replace a bbolt file bbolt can open.
  Owner: 1f.4, with the round 4 `hasBoltMagic` entry above: when meta 0's
  magic is missing, also check meta 1 at the OS page size, reword E2 step
  6's "bbolt (meta magic)" to cover either meta page, and test that a bbolt
  file with page 0 zeroed and a backup present converts, or at least is not
  restored over.
  Done in 1f.4: `hasBoltMagic` also accepts a second meta page holding the magic
  and its own page size at a page size bbolt tries (1KiB to 16MiB below the
  file's end); spec E2 step 6 and "Location, lock, ..." say either meta page. A
  leaf of `TestStoreOpenRestoreBoltDBFile` zeroes page 0 and requires the file
  converted, not restored over.

- [x] (from 1f.3) `rdrCrash` (`jobqueue/running_dependent_rerun_test.go`)
  restarts a manager from a `BackupDB` image written to `dbFile`, which on
  the log store is now a self-contained tar that only E2 restores, so the
  crash leaf of `TestRunningDependentRerunArchiveRaces`
  (`jobqueue/running_dependent_archive_test.go`, "a mark left in its stored
  record once it is not running is not acted on after a crash") still runs
  on bbolt only. Owner: 1f.4 (E2 test 3): run that leaf on every store
  kind, making `rdrSetStoredMark` work on the log store if it does not.
  Done in 1f.4: `rdrCrash` removes the db file (a directory on the log store)
  before writing its crash image, which the restart restores (E2 step 5); the
  crash leaf runs on both kinds. `rdrSetStoredMark` needed no change.

- [x] (from 1f.3 review round 1) `historyBolt.backup`
  (`store_history_bolt.go`) holds one `history.bolt` read transaction
  while `tx.WriteTo` copies the whole file, multi-GB at prod scale, as
  spec E5 requires. A fold commit that needs a bbolt remap meanwhile waits
  for that transaction, and since bbolt's mmap lock is an `RWMutex`, every
  new `history.bolt` reader queues behind the commit until the copy ends:
  the scheduling cycle's synchronous `refreshReqGroupRecs`
  (`server.go` ~6817) and status and REST history reads. A remap happens
  only at mmap growth steps (1GB once large), so the stall is rare and
  smaller than bbolt's whole-DB version of it. E4's 30s ticks copy
  `history.bolt` whole under the same kind of transaction. Owner: 1f.5
  (E4): measure the stall, and bound it if it matters; the phase 1 gate's
  NFS targets will observe it too.
  Done in 1f.5: measured and bounded. On `/nfs/hgi` (the prod NFS), a paced
  copy of a 4.1GB `history.bolt` held its read transaction for 23-25s; with
  bbolt's default mapping a commit growing the file past it waited 23s and
  history reads 21s, the rest of the copy. `openHistoryBolt` now maps
  `history.bolt` with `managerInitialMmapSize`'s headroom, as
  `openManagerBolt` maps bbolt, falling back to the default under a low
  `RLIMIT_AS`; there the same reads took at most 11ms, so a remap waits for
  a copy only once `history.bolt` has doubled in size since the store
  opened. (The growing commit wrote 300MB and still took 22s, its own write
  and sync contending with the copy's on NFS, not a lock wait.)
  `TestHistoryBoltBackupRemap` holds a tick's copy of `history.bolt` and
  requires a commit growing it past the default mapping, and a read after
  it, to finish during the copy. Spec "Location, lock, ..." says so.

- [ ] (from 1f.3 review round 1) `Server.handleBackup`
  (`serverCLI.go`) buffers the whole backup in a `bytes.Buffer` and sends
  it as one response: multi-GB of manager memory at prod scale, held while
  the log store's `backup` holds `ckptMu`. bbolt's backup did the same, so
  this is not a regression. Owner: 1f.4: stream the backup to the client
  instead if `wr manager backup -p` must work on prod-sized stores.
  Awaiting the owner's ruling (delivery-queue row 25): stream, or accept
  bbolt-equivalent buffering.

- [x] (from 1f.3 review round 3) No test races a backup's start against
  `close`, so a `startBackup` (`store_backup.go`) that joins
  `s.backups` without holding `closeMu` survives: a backup starting as
  close reaches `backups.Wait` could then read files close is removing.
  Also, the close-during-backup leaf in `store_backup_test.go` waits for
  `ckptExited` in its hook, so it depends on close stopping the
  checkpointer before it waits for backups; a close that waits first is
  not a defect but fails that leaf after a one-minute timeout. Owner: 1f.5
  (E4, whose final tick changes the same close path): add a leaf that
  races backup starts against close under `-race`, and loosen the leaf's
  coupling if 1f.5 reorders close.
  Done in 1f.5: a leaf of `TestLogStoreSelfContainedBackup` has 4 goroutines
  start backups until one fails while the store closes, requiring each to
  succeed or fail with `errDBClosed` and close to succeed; under `-race` a
  `startBackup` that joins `s.backups` without `closeMu` fails it (the
  `WaitGroup` reuse check). Close still stops the checkpointer before it
  waits for backups: E4's final tick (`finishBackups`) runs between the two,
  so the close-during-backup leaf's coupling is unchanged.

- [x] (from 1f.4) Restore (`restoreLogStore`, `jobqueue/store_open.go`) copies a
  store backup's files as they are, and lists S3 objects under the backup's
  prefix recursively, keeping their relative paths. It does not yet rebuild a
  wal file from E4's parts (`NNNNNNNN.wal.OOOOOOOOOOOOOOOO`), concatenating them
  in offset order and stopping at the first gap, as spec "Location, lock, ..."
  says restore does, since no backup writes parts until E4. Owner: 1f.5 (E4,
  whose test 7 restores a backup of parts): add that step to restore's fill of
  `.restoring`, before recovery.
  Done in 1f.5: `copyStoreBackup` ends with `assembleWALParts`, which
  concatenates each wal file's parts in offset order, after any whole wal
  file of that number, stopping at the first gap, then removes the parts.
  E4 test 7 (`TestLogStoreIncrementalBackup`) restores a backup of 3 parts,
  and of 2 with the last deleted.

- [x] (from 1f.4 review round 1) Spec E4's backup invariant assumes the
  folded position F only advances, but since 1f.4 recovery can move it back
  (`rewindFoldedTo`, `jobqueue/store_log.go`) when damage leaves the log's
  end before it, and a last wal file removed as torn has its number reused
  by the next writer (pre-existing). E4's per-file shipped offsets ("no
  byte shipped twice") and its deletion of parts could then splice the old
  and new content of one file number into one backup. Owner: 1f.5 (E4):
  reset or verify the shipped offsets after a recovery that moved F back or
  removed a torn file, and test it: ship parts of file n, damage the store
  and reopen so n is reused, tick, restore, and compare.
  Done in 1f.5: before it truncates a wal file, or removes one as torn,
  recovery notes the end it leaves it at (0 for torn) in the store
  directory's `backup-cuts` file, durably (`noteRecoveryCut`), so a crash
  before the next tick loses no cut. A store's first tick deletes the
  backup's parts of a truncated file after that end, shipping again, under
  its own name, the local bytes of a part that spans it; for a torn file it
  deletes every part but the first, then replaces that with the reused
  file's durable header in one put (`matchRecoveryCuts`), so a reused file
  number starts afresh in the backup and a crash never leaves the backup
  without it; the tick ships the file's records only after `history.bolt`,
  so a backup whose old `history.bolt` folded into the old file never takes
  the new file's records as folded. It fsyncs the backup directory, then
  deletes `backup-cuts`.
  A cut of a file the store has since deleted (below its first local wal
  file) is skipped, as the backup's parts of it are its own consistent
  copy. A moved-back F needs nothing more: the first tick of each
  store ships `history.bolt` whatever its folded position. The leaves of
  `TestLogStoreIncrementalBackupAfterRecoveryCut` ship parts of the last wal
  file n, damage it (header torn and the rest zeroed, so n is removed and
  reused; or a record flipped, so n is truncated), reopen, tick, restore,
  and compare the live jobs with the store's; one more has the run that
  reuses n end before its first tick, and another has an open die once it
  has removed a torn `walFrom` file, each followed by a run whose tick must
  succeed and leave a backup restoring to the store. Review round 2 added
  leaves for a crash at every op of the first tick after a torn `walFrom`,
  a cut on a file a run without backups deleted, a torn cut followed by a
  truncation of the reused file, `backup-cuts` consumed once, the backup
  sync before it is deleted, and an unreadable `backup-cuts`. Review round
  3 added leaves for a crash at every op of the first tick after a torn
  file the backup's `history.bolt` folded into (every job the backup held
  stays live or complete), after a torn `walFrom` sealed by a run that
  crashed before its first tick, and after an open that died once it had
  truncated a wal file (`logWALTruncatedHook`; `backup-cuts` holds the cut),
  and pinned that the deletes come before the put of the first part.

- [ ] (from 1f.4 review round 1) The live entries recovery drops
  (`dropFoldedLive`, `jobqueue/store_records.go`) are not persisted: the
  checkpoint merge does not drop them, so they ride along in every later
  checkpoint and backup and are dropped again at each open. That is
  harmless, but they never go. Owner: 2d (E6 compact, item 2d.2): have
  compact, or the checkpoint merge, drop them durably, and test that a
  dropped entry is in no checkpoint after it.

- [x] (from 1f.4 review round 2) If the checkpoint's `walFrom` file is
  removed as a last file with a torn header (`settleWALEnd`,
  `jobqueue/store_recover.go`) and the manager dies before the writer
  creates a new file of that number, the next open fails in
  `walFilesFrom` with "the checkpoint's walFrom, is missing", which is
  `errStoreCorrupt`, so a good store is restored over from backup. Only
  damage, not a crash, can tear `walFrom`'s header, since Rotate makes the
  new file durable before a checkpoint names it, so this is unlikely; it
  predates 1f.4 and was found by reading the code, not probed. Owner: 1f.5
  (recovery owner for E4): make the open after such a removal recreate
  the `walFrom` file, or accept a missing `walFrom` when F is at its start,
  and test it by cutting `walFrom` inside its header, reopening, closing
  before any write, and reopening again.
  Done in 1f.5: the second, made safe by ordering. Recovery removes a torn
  last file only after it has moved F back (`replayFromCheckpoint`), and an
  open that finds the checkpoint's `walFrom` missing with no wal file from
  it on and F at its start takes the log as ending there, logging
  `walFromAbsentLogMsg`, so the writer makes the file again (spec
  "Recovery", step 4). `TestLogStoreTornWALFromReopen` cuts `walFrom` inside
  its header, fails the reopen as soon as recovery has removed it
  (`logTornRemovedHook`), and reopens.

- [ ] (from 1f.4 review round 4) `extractTar` (`store_open.go`) lets a
  member named `.` past its name check, which is then refused as "holds
  "." more than once", the wrong reason; a member named `..` is refused
  with a bare `path escapes from parent` not wrapped in
  `errNotSelfContainedBackup`. Both are refused before `dbFile` is
  touched, so no data is at risk. Owner: 1f.7 (E7, which next reads
  self-contained files): reject `.` and `..` in the name check with
  `errNotSelfContainedBackup`, and test both.

- [ ] (from 1f.5) An incremental backup cannot tell that a backup at
  `dbBkFile` was made from another store's files when their wal file
  numbers overlap. The first tick refuses (`errBackupAhead`) a backup
  holding a checkpoint after the store's newest or parts of a wal file the
  store has not reached, but a store restored from an older self-contained
  file while `dbBkFile` holds a newer incremental backup of the same store
  can pass that check, and the tick then ships its own files over the
  backup's, splicing the parts of a wal file number both have. No test
  reaches this; it was found by reading the code. Owner: 2c.1 (E4 final
  form): give each store an identity, written in its `VERSION` when it is
  created and shipped with it, and refuse a backup whose identity differs,
  then test a restore from an older self-contained file beside a newer
  incremental backup.

- [ ] (from 1f.5 review round 5) No real-process crash test covers
  incremental backup. `TestStoreCrash` opens its stores as a restarting
  development manager does (`openCrashStore`, `store_crash_test.go`), with
  no periodic ticks and no `forceBackups`, so its rounds make no backup
  tick and `WR_CRASHTEST_ROUNDS` does not exercise E4. E4's crash coverage
  comes only from the simulated crashes of the failing `recordingTarget`
  in `store_backup_tick_test.go`. Owner: 1f.10 (C5 recovery clauses with
  backup): give `TestStoreCrash` a mode that opens production-like stores
  with periodic ticks to a backup directory, kills the child mid-tick, and
  requires the backup to either refuse to restore, saying to move it
  aside, or restore every job the child acknowledged before its last
  completed tick.

- [ ] (from 1f.5 review round 7) Mutant E12, `wholeFrom`
  (`store_backup_tick.go`) without `!b.holdsPartsBefore(first)`, is
  equivalent only in Phase 1. `shipVersion` runs only after
  `dropUnneededParts` succeeded in the same tick, and that deletes every
  part below min(C.walFrom, F_b.file); in Phase 1, F_b.file >=
  C.histFrom.file >= C.walFrom >= first, so no part before `first` is left
  for the guard to see. Phase 2 breaks that chain, since `histFrom` may
  precede the cut. Separately, a store whose ticks never run (no backup, or
  a development store without `forceBackups`) never consumes its
  `backup-cuts` file, so recovery's cuts only accumulate there, one line
  per wal file cut. That is harmless: a later backup's first tick honours
  the old cuts, which is conservative, and the `.restoring` restore path
  needs the notes. Owner: 2c.1 (E4 final form, which replaces the
  `history.bolt` step): re-check mutant E12 against the final store, and
  keep the guard, with a test that kills the mutant, if Phase 2 lets a
  part before `first` survive `dropUnneededParts`. Round 8's mutant N03,
  `neededFrom` (`store_backup_tick.go`) taking only the backup's newest
  checkpoint and not min(it, F_b.file), is likewise equivalent only in
  Phase 1. `cutAndFold` folds every record before the cut, so a shipped
  checkpoint C has `histFrom` at or past the end of wal file `walFrom`-1,
  and F_b >= `histFrom`; any file below both F_b.file and `walFrom` is
  then wholly folded and unneeded. Phase 2's `histFrom` may precede the
  cut, so 2c.1 must re-check N03 too, and add a test that kills it if it
  is no longer equivalent.

- [ ] (from 1f.5 review round 11) An S3 backup stages each object it ships
  in the store directory before uploading it whole (`s3Target.put`,
  `store_backup.go`), `history.bolt` included, so a tick shipping a
  multi-GB `history.bolt` to S3 briefly doubles the store's local disk
  use. That is accepted Phase 1 design, as spec E4 says and as bbolt's S3
  backup went through a local cache. Owner: 2c.1 (E4 final form, where
  sealed `.hist` segments, each shipped once, replace whole `history.bolt`
  shipping): check that a stage is then at most one segment, and say so in
  spec E4.

- [ ] (from 1f.5 review round 12) Three small backup-tick gaps, each
  non-blocking. First, `backupContinued` (`store_backup_tick.go`) follows
  spec E4's literal wording, so when the wal file just before the first
  local one is spent, the first file's header names the file before that
  and a backup that does continue is judged not continued: the next run's
  first tick logs "started again", removes `VERSION` and re-ships
  everything (probe in the review's `zz_rev12_spentcontinue_test.go`:
  tick, spend file 2, add, tick, checkpoint, crash-close, reopen, tick).
  The backup still restores, but if that tick keeps failing after shipping
  `history.bolt`, restore refuses it until a tick succeeds. Second, mutant
  W51 (`shipSealed` calling `markDone` only when `end > 0`) survives: a
  spent file would then be kept locally forever. Third,
  `forgetRecoveryCuts` clears the in-memory cuts before `syncDir`, so if
  that directory fsync fails, every retried tick in that run fails with
  `errBackupAhead`, until a restart. Owner: 1f.10 (C5 recovery clauses
  with backup): count a backup as continued when it holds the named file
  ending at the header's `prevEnd` and holds every file between as an
  empty part, and say so in spec E4; make the spent-file leaf require the
  spent file deleted locally after a checkpoint past it and another tick;
  clear the cuts only after `syncDir` succeeds.
