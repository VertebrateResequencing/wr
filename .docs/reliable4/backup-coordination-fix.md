# reliable4 — DB-backup coordination fix (archive hot-path de-serialization)

Design settled with the user 2026-07-27. This is the PRIMARY Layer-1 fix for the
report-storm churn. Copy-I/O relief (throttle/stage/D) is DEFERRED (Q3) — fix this
first, then re-measure. These fixes go in regardless of residual issues; validation
guides next steps.

## Confirmed problem (dumps + block/mutex pprof on the real manager under report-storm-lsf)
The periodic DB backup coordinates with the archive/exit hot path through two shared
locks, both contended during a backup, serialising 1000+ concurrent archivers:

1. **`db.Lock` (the db's exclusive `sync.RWMutex`)** is taken **per job-completion** in
   `updateJobAfterExit`, `launchJobExitUpdate`, and **per-archive in `backgroundBackup`**
   (called unconditionally at the end of `archiveJob` (db.go:1854), `storeNewJobs`,
   `deleteLiveJobs`, `launchJobChangeUpdate`) — just to check "should I back up?". The
   report-storm-lsf burst goroutine dump caught **1828 archivers blocked here**, all in
   `sync.(*RWMutex).Lock -> db.backgroundBackup`.
2. **`db.wgMutex` + `db.wg.Wait(dbRunningTransactionsWaitTime=60s)`**: `backupToBackupFile`
   holds `db.wgMutex` while waiting up to 60s for in-flight async writes
   (`launchBatchStore`/`launchJobExitUpdate`/`launchJobChangeUpdate`, which `wg.Add`) to
   land before snapshotting, blocking new registrations for the drain duration.

These are **filesystem-independent** — which is why moving the backup to a separate
filesystem (Lustre, direction E) did NOT reduce churn in the controlled A/B (Run A NFS
8854 vs Run B Lustre 4740-and-climbing; E only relieved the secondary copy-write
starvation). The backup is a **disaster-recovery fallback** (`initDB` restores from it
only if the primary DB is missing/corrupt), so it need not be perfectly up-to-date.

## The fix (user-confirmed design)

### Part A — decouple backup-triggering from the archive hot path
- Add a lock-free atomic dirty flag to `db` (e.g. `backupDirty atomic.Bool`). Replace every
  per-completion `db.backgroundBackup(ctx)` call with `db.backupDirty.Store(true)` (no lock).
- Add a single long-lived **backup-ticker goroutine** (started when the db is set up iff
  backups are enabled; stopped on `close()`): every ~`minimumTimeBetweenBackups` (30s) it
  does `if db.backupDirty.Swap(false) { <run one backup> }`, honouring the existing spacing
  (`backupWait`). The backup lock is now taken **once per tick, not per archive**.
- **Bonus (confirmed):** give the backup-state fields (`backingUp`, `backupQueued`,
  `backupLast`, `backupWait`, `backupFinal`, and the trigger machinery) their OWN dedicated
  `backupMu sync.Mutex`, separate from the db `RWMutex` that also guards `updatingAfterJobExit`.
  So backup coordination and exit-updates never contend. (Partition fields carefully; `closed`
  may need to stay readable by both — implementor to resolve cleanly.)
- `close()` still triggers a **final** backup directly (not via the ticker) and waits for it
  (existing `backupFinal`/`backupNotification`).

### Part B — drop the `wgMutex`-held `wg.Wait` on the PERIODIC backup
- The periodic backup does just `db.bolt.View(copyBackup)` — bbolt's read-tx already yields a
  fully **consistent** snapshot of committed state. Remove the pre-copy
  `db.wgMutex.Lock(); db.wg.Wait(60s); db.wg.Add(1); db.wgMutex.Unlock()` drain from the
  periodic path. Accepted consequence: a periodic backup may miss the last few seconds of
  in-flight writes (they land in the next backup) — fine for a DR fallback (user-confirmed:
  "stale is acceptable").
- **Keep the wait on `close()`** only (not hot): `close()`'s final backup still
  `waitForOngoingTransactions()` so a clean shutdown's backup captures everything.
- The `wg` machinery (Add/Done in the launch* funcs + `waitForOngoingTransactions`) stays for
  `close()`; only the periodic backup stops waiting.

### Part C — copy-I/O write-starvation relief: DEFERRED (Q3)
Not in this fix. After Part A+B land, re-measure with report-storm-lsf; if committer
`fdatasync` starvation from the copy's writes remains material, add the lightest fix then
(throttle/stage) or lean on D (small DB). The deferred throttle/stage trial code is git-stashed
("DEFERRED (Q3) ...").

## Regression test (TDD, fast, in-process)
White-box behavioural test (build on `jobqueue/reliable4_backup_stall_test.go` +
`db.slowBackups`/`slowBackupTestDelay`): with a backup in progress, fire many concurrent
`archiveJob` calls and assert they complete promptly (are NOT serialised for the backup's
duration). WITHOUT the fix they block on the per-archive `db.Lock` and/or the `wgMutex`-held
`wg.Wait`; WITH the fix they don't. Must be RED before, GREEN after.

## Quality gates (all must pass)
`unset $(compgen -v | grep '^OS_')` then `make test` (~362 passed); `make lint` (0 issues —
errcheck rejects `_ =` drops; watch nestif/unparam/gochecknoglobals for any package-level
ticker state); `go vet ./...`; `go build -tags reliability_repro ./jobqueue/`.
Must NOT regress crash-recovery: `TestReliable3Recovery*`, the wrdev `crash-recovery` flow,
`close()`'s final backup, and DR restore in `initDB`. Deliberate, documented behaviour change:
a periodic backup may be up to ~`minimumTimeBetweenBackups` stale (DR fallback; acceptable).

## Validation (after commit — guides next steps, not gating the commit)
Run `developers/wrdev.sh report-storm-lsf` on a quiet host: (1) a backups-OFF control (expect
~0 churn = proves backups are the cause, isolating the earlier host-load confound), (2) the
fixed build (expect churn to fall toward that floor). Any residual after the fix guides the
Part-C copy-I/O-relief and D decisions.
