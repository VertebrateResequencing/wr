# Idea 1 — Surgical: make a genuinely-finished job's result authoritative

**Class:** targeted bug fix (smallest change, no transport rework).

## Problem recap

Under real load the manager falsely flips an alive-but-slow-to-be-processed
running job out of `SubQueueRun` (TTR expiry on a backlogged touch). When the
runner then reports a **successful** completion, `handleArchive →
markJobComplete → canCompleteFromQueueState` rejects it (`bad job`) because the
item is no longer in `ItemStateRun`. The successful work is discarded and the
job re-runs (M1≈0, M2 huge). Because the archive that would set
`State=Complete` was rejected, the job later leaves the queue with
`State!=Complete`, so `changeCallbackToState` broadcasts **`deleted`** to the
web UI (M4).

## The idea

Treat the **runner that legitimately held the reservation** as authoritative
for its job's outcome. A successful (`Exited && Exitcode==0`) completion from
the job's current `ReservedBy` client must be **accepted regardless of the
item's current sub-queue**, unless a *newer attempt* has superseded it.

Concretely:

1. **Attempt epoch.** Add a monotonically increasing `attempt`/reservation
   epoch to each job, bumped on every `Reserve`. The runner already carries a
   `ClientID`; carry the epoch it was reserved under in the archive/release
   request.
2. **Accept-late-success.** In `canCompleteFromQueueState`/`markJobComplete`,
   accept a completion when `cr.ClientID == job.ReservedBy` **and** the request
   epoch == the job's current epoch, even if the item is in `Run`/`Lost`/parked
   state — i.e. "you still own this job and nobody else has re-reserved it, and
   you succeeded, so you win." Only reject if a *newer* reservation exists
   (genuine double-run: the other attempt wins, this one is a no-op).
3. **Lost→Complete is legal.** A job flipped to `Lost` while its owner is still
   running must be completable by that owner (clear `Lost`, set `Complete`).
   This already half-exists for the delayed/lost path; extend it to the
   "still in Run but marked Lost" and "flipped out under saturation" cases.
4. **Deleted-broadcast fix.** In `changeCallbackToState`, a removal whose job
   `Exited && Exitcode==0` (or `State==Complete`) must map to `Complete`, never
   `Deleted`. Belt-and-braces so a completed job can never render `deleted`.
5. **Status (M5)** is *not* deeply addressed here; pair with the existing cheap
   `-o counts` path and (if needed) the tiny "Idea 4-lite" separate status
   listener from v1. This idea's scope is correctness (M1/M2/M3/M4).

## Why it solves the symptoms

- M1/M2: successful commands are recorded complete even when the manager was
  briefly wrong about liveness → no discard, no re-run churn.
- M4: completed jobs broadcast `complete`, never `deleted`.
- M3: double-run is bounded by the epoch (only one attempt wins).

**v0.36.5 alignment (strongest):** this restores exactly what v0.36.5 did — its
`jarchive` accepted an owner's successful result on an in-`Run` job with no
`job.State` gate and no strict state machine, so a late success was never
discarded. Idea 1 re-establishes that "owner's success wins" semantics on top
of the retained accuracy machinery, with the epoch as the double-run guard that
v0.36.5 didn't need (because it never re-reserved an alive job).

## Risks / tradeoffs

- Must get the epoch/ownership check exactly right or you risk a *duplicate*
  accept (two runners both complete) — the epoch is the guard.
- Doesn't remove the single-reader contention, so M5 needs the small add-on.
- Touches the delicate `getij`/`markJobComplete` invariants — heavy test
  coverage required (don't regress the false-lost guards).

## Trial checklist (prove it works)

- [ ] Land the shared in-process saturation repro in `harness/`
      (`reliable2_churn_test.go`): short `ItemTTR`, N saturating connections,
      and runners that hold a job **longer than the TTR** then archive success.
      Confirm on **current** code it reproduces: ≥1 `bad job` rejection of an
      exit-0 archive, and a `deleted` broadcast for a succeeded job.
- [ ] Temp-implement the epoch field + `Reserve` bump; thread it through the
      archive/release client calls (temp, behind the existing protocol).
- [ ] Temp-relax `canCompleteFromQueueState` to accept owner+epoch success from
      any sub-queue; add the `changeCallbackToState` complete-wins guard.
- [ ] Re-run the repro: assert **0** exit-0 archive rejections (M2), every
      succeeded job ends `complete` (M1), web-UI transition is `complete` not
      `deleted` (M4), and the v1 `TestReliableFalseLostUnderSaturation` +
      committed `TestLostDetectionRecentContactNotLost` stay green (M3).
- [ ] Add a double-run test: force two reservations of one job; assert exactly
      one `complete`, the loser is a clean no-op (not an error storm).
- [ ] `make lint`, targeted `-race` runs (OS_* unset), and M6 startup check on
      the real DB copy unchanged.
- [ ] Farm sanity: a small `portal_builder` run reaches `complete` for the
      jobs whose commands succeeded (no endless re-run).

## Trial results

_(to be filled during trialling)_
