# ffd180d prod-freeze reproduction attempt (2026-07-28)

## STATUS: could NOT reproduce the prod freeze synthetically. The deployed
## ffd180d fixes cope with every faithful stress at/above prod scale.
## Recommendation: profile the REAL prod manager during a real freeze (see end).

## Problem being chased

Deployed manager `v0.37.1-35-gffd180d` still shows, in the HGI production
deployment (mercury, DB+backup on team166 = `file01-d1:/vol/team166` = `/nfs/hgi`):

- portal jobs still going lost (low level)
- most **compress** jobs going **delayed** instead of completing
- runner logs in the bad window (2026-07-27 ~21:16-21:35) show archive
  `"failed to update server with cmd's final state" err="receive time out"`
  every ~75s for 10+ min on 8/8 sampled runners = the manager was unresponsive
  to final-state reports for >60s (the client `ClientMinRequestTimeout` floor),
  so jobs never complete -> TTR -> lost -> re-reserved -> the original runner's
  archive is rejected (ErrMustReserve, new-run-wins) -> work discarded -> retry.

Prod's real peak concurrency is **<2100** (the results_portal limit is 2000,
plus a handful of other jobs).

## Method (per the "isolated production manager, not dev hacks" directive)

Everything on **team166** (prod's exact NFS), isolated PROD-mode manager as
`sb10` on port 51782/web 51783 (never the real mercury prod), LSF jobs
namespaced `wrpiso51782_*`. No manager code was modified (three exploratory
`WR_DEV_*` hooks were written then fully reverted — client.go/db.go == HEAD).
The earlier "L1 fix validated" run had used `$HOME/wr-devtest` = home NFS
(`nfs_s`, a *different, faster/less-loaded* server) — correcting that to team166
was the whole point of this pass.

Two DBs used: the real **prod.db** (7.9GB, copied to `/nfs/hgi/wr/sb10-bigdb/prod.db`,
recovered read-only via `-s local --max_ram 1` so nothing executes) and the
synthetic **pristine10** (10.7GB, ~2.1M inflated complete records, harmless
`true`/`false` commands).

### prod.db shape (read-only bolt Stats)

| bucket | keys | ~MiB | notes |
|---|---|---|---|
| jobscomplete | 2,155,154 | 2971 | ~1.4KB/rec — records are NOT fat |
| jobslive | 118,213 | 186 | ~1.6KB/rec; fresh-recovery: ~98k ready, ~18k buried, ~1080 suspended |
| jobLookupEntries | 5,072,063 | 651 | |
| repgroupToKey | 4,167,762 | 298 | |
| stde / stdo | 322k / 27.8k | 85 / 15 | most jobs have little/no stdout |

~25,854 rep groups total, but only **~20** have INCOMPLETE jobs (compress,
dedupe, ibackup_fofn*, wrstat-ui-summarise*). The DB is huge by **count**, not
record size.

## Every faithful stress — and it COPED (no >60s freeze)

| # | stress | result |
|---|---|---|
| 1 | report-storm-lsf, ~1900 concurrent, back-to-back 10GB backups on team166 | status_rpc 300-470ms; **0 churn**. 126 pprof samples: bbolt committer `fdatasync` **never** starved (backup `WriteTo`/`copyBackup` runs concurrent with `.Commit` but does not block it). |
| 2 | as #1 + `WR_RS_PADKB=25` (prod-fat ~25KB records) | status_rpc 200-450ms; no freeze. |
| 3 | web status feed (`wsprobe` "current") on prod.db exact shape | full seed in **~20 messages < 10s**, ping steady 32-44ms. Only ~20 active rep groups => `sendCurrentStatusCounts` per-rep-group loop is cheap. NOT the freeze. |
| 4 | ~3000 concurrent (above prod's 2100) + 3 slow(50ms)/refresh-loop wsprobe browsers + backups | worst status_rpc ~1.1s; **mild self-recovering churn** (delayed spiked to 1401 during a peak-ramp+backup coincidence, then settled to 0; complete kept progressing; badjob/archive_reject=0). pprof (10k goroutines): 2050 concurrent `handleArchive`->bbolt; confirm-dead SSH cache **stable ~334 conns** (bounded per-host cache, not a leak). |
| 5 | external team166 write contention: 4x4GB `dd` fsync/direct during #4 | status_rpc 202-471ms — did **not** stall the committer. |
| 6 | **fail/succeed storm**: 100k `false` (fail immediately) + 100k `sleep 1`, limit 2000, retries 30, on the 10GB DB, backups on | LSF_RUN ~3000; **delayed churn 900-2170** (failing jobs cycling release->retry, the L2 release/bury path at high rate) yet status_rpc **44-291ms**, complete progressing. The high-failure-report-rate path also copes. |

## Ruled out

- `touchJob` does **not** write the DB — TTR touches are in-memory only, so
  prod's long touched jobs add no committer/backup pressure.
- **prod confirm-dead works fine** (user-confirmed; current prod log has only 2
  "could not confirm" total). The SSH-auth-failure claims from the reliable3/
  reliable4-restart notes are **outdated/operationally-fixed** — not the cause.
- All reliable4 fixes are ancestors of ffd180d: bounded rac scan `85db7b2`,
  TTR-miss `804e05a`, L1 backup-coord `f51af04`, L2 `3426158`, L3 `525e819`.
- Rewriting prod.db commands in place is **unsafe** (recovery decodes the stored
  value but the bucket key is `Job.Key()` from Cmd+Cwd, so rewriting desyncs the
  key from archive/lookup).
- The backup copy is NOT the current freeze: the deployed paced fix
  (`efb9303`/`209b607`/`619c09c`) copes with back-to-back 10GB backups on team166.
- Re-reading the current prod log: the manager is ALIVE during the big silent
  gaps (its DB is written during a 393s log-silence; debug is off so silence !=
  frozen). The real unresponsiveness was a **clustered high-load window**, not a
  constant freeze.

## Conclusion

Under every reproducible condition at or above prod's real peak (~2100
concurrent) the ffd180d manager stays responsive (worst status_rpc ~1.1s) with
at most mild, self-recovering churn. The prod freeze is **intermittent** and did
not reproduce. The most likely remaining explanations both require the exact
prod runtime state, which a synthetic harness can't recreate on demand:
- a rare/intermittent stall on the real workload (a specific job/rep-group/dep
  pattern, or a GC/allocation spike on the 10GB working set), or
- a peak external team166 contention event beyond the modest `dd` load tried.

Guessing further synthetic variants has diminishing returns. We should **measure
the real manager**.

## RECOMMENDATION: profile the REAL prod manager during a real freeze

A goroutine dump taken *while the manager is unresponsive* will show exactly what
the RPC-handler / committer / status-feed goroutines are blocked on — turning
this from guesswork into a definitive diagnosis. pprof over HTTP is read-only and
non-invasive (its HTTP server runs on its own goroutine, unaffected by a
committer/queue freeze, so it keeps answering during the freeze).

How (operator actions on the HGI prod manager, which runs as mercury on
172.27.71.193 — not reachable from the dev host):

1. **Enable pprof.** At the next (planned) manager start, set
   `WR_PPROF_ADDR=localhost:<port>` (e.g. `localhost:6060`) in its environment.
   This only starts a local pprof HTTP server; it changes nothing else. (A
   restart preserves runner reconnection via the existing token as long as it's
   an unclean restart that keeps the token file — do NOT `wr manager stop`, which
   invalidates reconnection.)
2. **Trigger the load.** Un-suspend a portal batch so the ~2000-concurrent
   compress/dedupe workload runs and the freeze recurs.
3. **Capture during the freeze.** When runners start logging `receive time out`
   / the status web page hangs, from the prod host run, repeatedly (every ~5s for
   a minute):
   ```
   curl -s "http://localhost:<port>/debug/pprof/goroutine?debug=2" -o goro_$(date +%s).txt
   curl -s "http://localhost:<port>/debug/pprof/block"  -o block.pprof   # if block profiling on
   curl -s "http://localhost:<port>/debug/pprof/mutex"  -o mutex.pprof   # if mutex profiling on
   ```
   The goroutine dump is the key artifact: look for the committer stuck in
   `fdatasync`, RPC handlers blocked on `queue.mutex`/a `sync.Cond`, or the
   status feed holding a lock while scanning — whichever it is *is* the cause.
4. **Also raise log visibility** for the freeze window (debug on, or at least
   ensure backup/lost/TTR events log) so the log timeline corroborates the dump.

Hand the goroutine dumps back and the fix follows directly from what they show.

## Reproducer assets (reusable)

- `developers/wrdev.sh report-storm-lsf [jobs] [limit] [runsec]` with
  `WRDEV_ROOT=/nfs/hgi/wr/sb10-bigdb/ftroot` +
  `WRDEV_PRISTINE_DB=/nfs/hgi/wr/sb10-bigdb/pristine10` + `WR_RS_PPROF=<port>`
  runs the isolated prod manager + storm on team166.
- `wsprobe` (`.docs/reliable2/phase2/wsprobe`) = the browser/status-UI simulator
  (`host:webport token secs slowReadMs`); slowReadMs>0 models a slow browser.
- fail/succeed mix: add a JSON of interleaved `false` + `sleep 1` jobs behind a
  limit group with `--retries` to exercise the release/bury path at high rate.
- Do NOT `git stash pop` `stash@{0}` (a parked, separate Part-C throttle trial).
- Part C (backup copy-I/O relief) is NOT justified: the backup is not the freeze
  under any reproducible condition here.
