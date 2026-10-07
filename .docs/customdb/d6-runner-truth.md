# D6: runners as the source of truth for running state, with lease batches

## Sketch

Every other design makes the manager's disk the record of which runner holds
which job, and pays a durable write before each hand-out and each start. D6
takes that off the hot path. The runner already holds everything a
reservation and a start would record (job key, its client id, its runner
reservation number, host, pid, start time); after a manager crash every live
runner reconnects and reports. So:

- Durable at add: the spec (as D2).
- Durable at completion: archive, release, bury (as D2).
- Not durable per job: reserve and start.
- Instead, before handing out jobs from a scheduler group, the manager writes
  one durable lease record naming the next batch of keys it may hand out
  (say the next 1000, or every key it hands out in the next second), and
  hands out from memory while the lease covers them. Leases are amortised:
  one 16KB record per 1000 hand-outs instead of 2000 records.
- Recovery: any live job named in a lease written since its last completion
  record is "possibly running". It is quarantined, offered to no runner,
  until either its runner reports (start, touch, archive, release with the
  runner's client id and reservation, which the manager authenticates as
  today with the token it already issues) or the confirmed-dead path
  (TTR lapse, `LostRunnerBackstop`, pid check) says the runner is gone.
  Jobs not named in any open lease are ready immediately.

## What it guarantees

- No double run: a job is only offered again when it is not covered by an
  open lease (never handed out since its last durable outcome) or its holder
  is confirmed dead. That is the same rule today's recovery applies to jobs
  recovered into Run; D6 applies it to the leased set instead of to the jobs
  whose reservation write happened to commit.
- No lost completion: archive, release and bury stay durable before reply.
- Cost: after a crash, leased-but-not-started jobs (at most one lease batch
  per group) wait for the TTR before they run. Started jobs resume as soon as
  their runners reconnect.

## Why consider it

- It removes two thirds of the per-run durable writes (reserve and start) and
  makes the hand-out a memory operation, so the hand-out can never be blocked
  by a slow or stalled filesystem. Today's 10s ReserveWriteWait and its
  "handed out before recorded" escape hatch disappear: the lease is written
  ahead, and if the lease write is slow the manager simply has nothing leased
  to hand out yet (runners wait, no job is at risk).
- It is a protocol change that composes with any storage design, including
  bbolt: with leases, bbolt would only carry adds, archives and exits.

## Risks

- Protocol complexity in recovery: quarantine, runner-report matching against
  leases, and the timing of releasing unclaimed leased jobs. This is close to
  what `recoversIntoRun`, `recoverRunnerHold` and the confirmed-dead
  coordinator do today, but it would be the primary path, not the fallback.
- Old runners that do not report their run state after reconnect must be
  handled (wr upgrades runners with the manager, so this is a release-time
  concern).
- Status during the quarantine window shows jobs as "possibly running"
  without a host until their runner reports.

## Files and inodes

As the storage design it sits on.

## Measured

`proto/cmd/hotpath -lease` on the D2 log: reserve and start make no durable
write; one lease write per 1000 reservations. See `benchmarks.md`.
