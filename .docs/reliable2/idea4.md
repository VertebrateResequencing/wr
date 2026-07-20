# Idea 4 — Runner-authoritative outcome + idempotent reconciliation

**Class:** higher-level design change (durable outcome, eventual consistency).

## Problem recap

Today a job's outcome is only "real" if the runner's `Archive` RPC is accepted
in the exact instant the manager still agrees the job is running. That coupling
is what loses successful work under load (M1/M2) and mislabels it `deleted`
(M4). The command *did* run and succeed — the failure is purely in *recording*
that fact through a contended, stateful RPC.

## The idea

Make the **runner the source of truth for its own outcome**, written durably
and independently of the manager's live state, and have the manager
**reconcile** from it idempotently.

1. **Durable outcome record.** When a runner finishes a command it writes an
   outcome record — exit code, peak RAM/disk, start/end time, cwd — to a
   durable location the manager can read later: e.g. a small file in the job's
   wr working dir (wr already owns `wr_cwd/...`) or an append-only per-manager
   outcome log/spool on shared storage. This write is cheap and does not depend
   on the manager being responsive.
2. **Idempotent completion.** `Archive` becomes an *optimisation/notification*:
   if it lands promptly, great. If it's rejected/lost/late, the outcome record
   still exists. Completion is keyed by (jobKey, attempt-epoch); applying the
   same outcome twice is a no-op.
3. **Reconciliation loop.** A background reconciler (and startup recovery)
   scans outstanding running/lost jobs against outcome records and finalises
   any job whose command demonstrably succeeded — so a successful command is
   **never** re-run or shown `deleted`, even if the manager was unresponsive,
   restarted, or the runner died right after writing its outcome.
4. **Web-UI/state derive from the reconciled truth**, so `complete` reflects
   the durable outcome, not a race with the archive RPC.

This is close in spirit to how robust batch systems decouple "task ran" from
"controller noticed": the controller can lag or restart without losing work.

## Why it solves the symptoms

- M1/M2: a succeeded command is finalised from its durable record regardless of
  RPC timing → zero discarded work, zero re-run churn.
- M4: `complete` is derived from the outcome record; a succeeded job can never
  be broadcast `deleted`.
- M3: false TTR loss becomes harmless (reconciliation finalises the job anyway);
  and lost-detection can be relaxed to "re-run only if no success record".
- M5: reconciliation is background and off the hot request path; pair with
  Idea 2 for the status read path if needed.

## Risks / tradeoffs

- New durable-write surface: where to write (shared FS vs manager spool), how to
  garbage-collect, how to handle partial/corrupt records, security/ownership.
- "Reconcile from disk" adds I/O and a scan cost — must be O(outstanding), not
  O(history); reuse the counter/live-bucket machinery from `#550`.
- Bigger conceptual change; needs careful crash-consistency design so a record
  written but not yet reconciled survives a manager `kill -9`.
- Must not change the user-visible contract (same states, same output files).

## Trial checklist (prove it works)

- [ ] Land `harness/reliable2_churn_test.go`; baseline the discard/re-run under
      saturation and after a mid-run manager `kill -9` + restart.
- [ ] Temp-implement an outcome-record write in the in-process runner double
      (write to `t.TempDir()`), and a reconciler that finalises jobs from it.
- [ ] Repro with archives artificially rejected (simulate the TTR flip): assert
      every succeeded job still ends `complete` via reconciliation (M1/M2), and
      is broadcast `complete` not `deleted` (M4).
- [ ] Crash test: `kill -9` the in-process server after outcome records exist
      but before archive; on restart, reconciliation finalises them (no re-run).
- [ ] Assert idempotency: applying an outcome twice / racing archive+reconcile
      yields exactly one `complete`.
- [ ] M3 guards green; M6 startup still ≤ few s (reconciler scan is
      O(outstanding)); M7 throughput not regressed; `make lint`, `-race`.
- [ ] Farm: `portal_builder` at scale — successful `zopfli`/`jq` commands all
      reach `complete`; none re-run; web UI shows complete.

## Trial results

_(to be filled during trialling)_
