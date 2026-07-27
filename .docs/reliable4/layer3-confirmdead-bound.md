# reliable4 — Layer 3: bound the confirm-dead SSH storm

Third layer of the holistic report-storm fix. Concrete, storage-independent, testable win.

## Problem (observed in the report-storm-lsf profiling)
`markJobLost` (server.go ~4092) spawns `go s.confirmOrReleaseLostJob(...)` per lost job with NO
concurrency bound. Under mass false-lost (a freeze/overload firing TTR on thousands of jobs at
once), thousands of concurrent `confirmOrReleaseLostJob -> confirmJobDeadAndKill -> confirmJobDead
-> s.scheduler.ProcessNotRunningOnHost(...)` fire — each an SSH session to a runner host. The pin
dump caught ~852 concurrent `golang.org/x/crypto/ssh` goroutines. That is a resource storm: file
descriptors, network, CPU, and load on the single mercury SSH auth path — and it can itself slow
the manager and the confirmations.

NOTE: Layer 1 removed the DOMINANT freeze that caused mass false-lost, so this storm should be rare
now; Layer 3 is DEFENSE for any residual mass-lost event (a genuine mass runner death, a residual
overload, or a future regression). It is cheap and removes a real failure amplifier.

## What is already handled (do NOT regress)
- **Don't false-lose an actively-reporting runner:** `confirmJobDead`'s both-pid check
  (server.go:4880) declares a job dead only if BOTH its command pid AND its runner pid are gone.
  A live/actively-reporting runner's pid is alive -> `ProcessNotRunningOnHost` returns false -> NOT
  confirmed dead -> NOT killed. Plus Layer 2 now accepts that runner's late report (no discard),
  and a late touch clears the Lost flag (ttrCallback). Keep all of this intact.

## The fix — bound concurrent confirm-dead checks
Limit concurrent confirm-dead SSH checks to a small configurable N (default ~10-16) via a
semaphore (a buffered `chan struct{}` on the Server, sized N) acquired around the
`confirmOrReleaseLostJob` SSH work (i.e. before `confirmJobDeadAndKill`/`confirmJobDead`'s
`ProcessNotRunningOnHost` calls) and released after. Excess lost-job confirmations wait for a slot.
- Implementation choice (implementor's call, follow repo conventions): a semaphore acquired inside
  the spawned goroutine (simplest; 2000 goroutines may still spawn but only N do SSH at once), OR a
  bounded worker pool fed by a channel (also caps goroutine count). The SSH concurrency is the
  resource that must be bounded; goroutine count is secondary. Prefer the minimal clean option; if
  a package-level/Server field is added, respect gochecknoglobals (Server field, not a global).
- Make N a Server field seeded from timings/config with a sane default so tests can set it small.
- The `confirmJobDeadAndKillAfterRetryTime` retry path (server.go:4904) must also go through the
  same bound (it re-checks via `ProcessNotRunningOnHost`).

## Why bounding is safe (no correctness loss)
A delayed confirmation never kills an alive runner (both-pid check) and never loses work (Layer 2
accepts the owner's late report; a touch recovers a spuriously-lost job). The only cost is that
GENUINELY-dead jobs under a mass-death event reclaim their slots slightly slower (serialised at N
at a time) — acceptable, and far better than the SSH storm. Under the common case (mass FALSE-lost
from a transient stall) the jobs are alive and recover regardless, so the bound is pure upside.

## Regression test (TDD)
Use the mock scheduler: make its `ProcessNotRunningOnHost` record the number of IN-FLIGHT calls
(atomic inc on entry / dec on exit, track the peak) and block briefly (so overlap is observable).
Mark M >> N jobs lost (drive markJobLost / the TTR path for M jobs). Assert the observed PEAK
concurrent `ProcessNotRunningOnHost` <= N. RED without the semaphore (peak == M, unbounded); GREEN
with it (peak <= N). Keep it fast + deterministic (untagged, in make test).

## Must-not-regress
TestLostDetection*, TestReliable4LostRunnerBackstop, TestReliable4RunnerPidLiveness,
TestReliable2Lost*, TestReliable3Recovery*, Layer-1 + Layer-2 tests. The both-pid liveness verdict
and the kill/release-after-confirm behaviour are unchanged — only their CONCURRENCY is bounded.

## Gates
`unset OS_*` then `make test`; `make lint` 0 issues (errcheck rejects `_ =`; gochecknoglobals for
any new state); `go vet ./...`; `go build -tags reliability_repro ./jobqueue/`.

## After Layer 3 = releasable state
L1 (backup coordination, f51af04) + L2 (report reconciliation, 3426158) + L3 = a coherent
releasable reliable4. The user deploys to prod; Part C (copy-I/O relief, task #26) is REVISITED
after that prod run WITH a real reproducer (prod NFS == the test NFS, so Part C must not be guessed
— see memory prod-nfs-same-as-test).
