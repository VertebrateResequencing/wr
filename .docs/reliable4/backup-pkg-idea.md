# PARKED: extract a general-purpose public bolt-backup package

Decision (2026-07-27): capture this now, DEFER the work. We finish the wr reliability
work first; once wr is fully reliable we refactor the backup code out to a new public
package on a NEW branch, and wr depends on it. **Part-C-type copy-I/O relief belongs IN
this package, not in-tree** — so do NOT implement an in-tree Part C throttle/stage; fold
it into the package instead.

## Why
We have several repos that back up bolt DBs. The naive backup
(`db.View(func(tx){ tx.WriteTo(os.Create(dst)) })`) freezes writers when the DB is large
and busy on slow/NFS storage (dirty-page pileup starves a concurrent commit's fdatasync)
— exactly the report-storm freeze we fixed in wr. A shared, tested package would give all
those repos a safe busy-DB backup and remove the duplicated (and subtly buggy) in-tree code.

## What's reusable (bolt-agnostic), and better than naive tx.WriteTo
All currently in `jobqueue/db.go` (+ `db_backupsync_{linux,other}.go`):
1. **Paced hot-backup writer** (`backupCopyWriter` + `backupPaceRange`) — the novel core:
   streams `tx.WriteTo` through a writer that forces writeback every N bytes via
   `sync_file_range` (Linux; portable fallback in `_other.go`), so the copy's dirty pages
   never clog the writeback queue a concurrent commit's fdatasync waits on. This is what a
   naive backup lacks and why it freezes a busy DB on NFS.
2. **Non-blocking periodic scheduler** (the f51af04 Part-A machinery: `backupDirty atomic.Bool`
   + a single ticker goroutine + a dedicated `backupMu`): the app calls a lock-free
   `MarkDirty()` after writes; the scheduler runs at most one backup per min-interval OFF the
   write path; `Close()` does a final backup + stops. Backup triggering never serialises the
   app's writes. Also the "don't back up more often than a backup takes" spacing heuristic.
3. **(Part C) optional bandwidth throttle** — the deferred duty-cycle cap (trial git-stashed
   on reliable4 stash@{0}: WR_EXP_BACKUP_THROTTLE + the stage-tmp mode; stage-mem was REJECTED
   — 46GB RSS for a 6.9GB DB + its unpaced NFS-read stall). For storage so slow that even the
   paced full-file copy starves the committer past the client's timeout. Belongs in the pkg.
4. **Restore helper** — "copy the backup file into place if the primary is missing/corrupt"
   (wr does this in `initDB`).

## What is NOT in the package (it was wr's bug)
Any coupling of the backup to the app's own mutexes / transaction tracking. wr's original bug
was coupling backups to the db RWMutex (per-archive `backgroundBackup`) and a `wgMutex`-held
`wg.Wait(60s)` drain. A clean package relies ONLY on bolt's `tx.WriteTo` read-tx snapshot for
consistency — no app-lock coupling, no draining app transactions. (Consequence, as in wr: a
periodic backup may be up to ~min-interval stale — fine for a DR fallback.)

## Sketch API
```go
package boltbackup // e.g. github.com/VertebrateResequencing/boltbackup (new repo/branch)

type Options struct {
    PaceBytes      int64          // writeback pacing (e.g. 8<<20); 0 = off
    MinInterval    time.Duration  // min spacing between periodic backups
    ThrottleFactor float64        // optional duty-cycle bandwidth cap (Part C); 0 = off
}
func Backup(db *bolt.DB, dst string, o Options) error            // one paced, consistent hot backup
func Restore(dst, primary string) (restored bool, err error)     // copy back if primary missing/corrupt
type Scheduler struct { /* ... */ }
func NewScheduler(db *bolt.DB, dst string, o Options) *Scheduler  // starts the ticker
func (s *Scheduler) MarkDirty()                                   // lock-free; call after writes
func (s *Scheduler) Close() error                                 // final backup + stop
```

## Plan (later, new branch, after wr is fully reliable)
1. New module (own repo or a `backup/` subpkg published separately), extract items 1-4 with a
   clean API + tests (include a "busy DB on a throttled/slow FS doesn't freeze writers" test).
2. Keep the Linux/`_other` build-tag split for `sync_file_range`.
3. Make wr depend on it: delete the in-tree `backupCopyWriter`/`backupPaceRange`/`copyBackup`
   and the scheduler, wire wr's write sites to `Scheduler.MarkDirty()`, `close()` to
   `Scheduler.Close()`, and `initDB` restore to `boltbackup.Restore`.
4. Fold the stashed Part-C throttle/stage trial into the package's `ThrottleFactor`/staging,
   validated on slow storage (the report-storm-lsf regime where prod's NFS froze ~108s).

Current in-tree implementation to extract from: `jobqueue/db.go` (backupCopyWriter, copyBackup,
backupToBackupFile, backgroundBackup, runBackgroundBackup, finaliseBackup, backupTicker,
backupDirty/backupMu/backupStopped), `jobqueue/db_backupsync_{linux,other}.go`. Fix history:
[[reliable4-backup-coordination-fix-done]] (f51af04), design .docs/reliable4/backup-coordination-fix.md.
