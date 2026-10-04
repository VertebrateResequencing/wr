# Bugfixes 26-10-01: flakes and developer tooling

Quality gates: `make lint`, `make test`, `make race`, run with
`nice -n 19`, `GOFLAGS=-p=2` and `GOCACHE=/tmp/claude-11346/gocache-tooling`.

- [x] Flake: TestJobqueueModify fails under some ad-hoc combined `-race -run` patterns, on develop too ("schedgrp 200:30:1:0 not found, we have: 800:30:1:0"); passes alone and in make race. Find the cross-test leak/ordering dependence and fix.
  - Red: add a throwaway `jobqueue/aaa_ballast_test.go` whose `TestAAABallast`
    holds a live 700 MiB slice, then
    `go test -count=1 -run 'TestAAABallast|TestJobqueueModify$' ./jobqueue`
    (exit 1): `schedgrp 200:30:1:0 not found, we have: - 1500:30:1:0 (1 jobs)`,
    `*** test from line 6872 failed`, and the same at line 6950.
  - Cause: the PeakRAM an in-process `Client.Execute` records includes the
    calling process's memory twice: the child's rusage `Maxrss` carries the
    parent's RSS high-water mark (Go starts children with CLONE_VM|CLONE_VFORK;
    `/bin/true` reported 2 MB from a small parent and 703 MB from one holding
    700 MiB), and `ownMemoryMB()` then adds the caller's Pss. The test binary's
    size depends on which tests ran earlier, so the hard-coded learned group
    `200:30:1:0` was wrong after a memory-heavy predecessor.
  - Fix (test only), `jobqueue/jobqueue_test.go`: dropped the hard-coded
    learned group and the 300-600 alternative-group fallback; a `learned`
    helper reads the value the server learned
    (`server.db.recommendedReqGroupMemory`), asserts it is above 0, and derives
    the scheduler group from it. The reqgroup and override cases now assert the
    reserved RAM equals it.
  - After: the red command passes; mutations disabling the ReqGroup or Override
    modify in `jobqueue/job.go` both fail. `make lint` reports 0 issues.
  - Product finding, not fixed here (needs an owner decision): because of the
    inherited `Maxrss`, every job's recorded PeakRAM is at least the `wr runner`
    process's RSS high-water mark, and `ownMemoryMB` adds the runner's Pss again,
    so small jobs are over-learned by about twice the runner's footprint.
    - Since fixed by #660, `261001-peak-ram-includes-runner.md`.
- [x] Flake: TestDepGranularitySidecarReportsElapsedTime failed once under load (a prior fix made it wait for the next heartbeat instead of 200ms; check it's on develop and why it can still fail).
  - The prior fix (`dgsWaitForSidecarRewrite`) is on develop in 95faf168.
  - Red: temporary injected delays, since removed. In the heartbeat goroutine,
    tick 1 sleeps until 200us before the second tick boundary and tick 2 sleeps
    30ms after computing elapsed; the test sleeps 110ms before
    `dgsWaitForSidecarState`. Then
    `go test -count=3 -run '^TestDepGranularitySidecarReportsElapsedTime$' ./jobqueue/`
    failed 3 of 3: `Expected '101ms' to be greater than '101ms' (but it wasn't)!`.
  - Cause (test): the detail's elapsed time is rounded to the millisecond. A
    heartbeat goroutine starved past a tick handles the late tick and then the
    next one within a millisecond, writing the same elapsed twice with
    different `UpdatedAt`s. If the test's first sample was the first of those,
    the wait accepted the second and the strictly-growing assertion failed.
  - Fix (test only), `jobqueue/depgranularity_startup_test.go`:
    `dgsWaitForSidecarRewrite` waits for a sample in the same state with a
    different detail. Every assertion is kept.
  - After: the red passes 5 of 5 with the same injections; `-count=20` and a
    `-race` run pass. A sidecar never refreshed, or one whose elapsed never
    changes, fails at `found` after the 30s bound. Ruled out: a leaked server
    reading the swapped `recoveryHeartbeatInterval` (Stop waits on `bgWG`
    without a timeout), a shared sidecar path (each init gets its own
    manager dir), and wall-clock `UpdatedAt` steps.
- [x] Flake: TestArchiveStallDoesNotRerunJob (jobqueue/archive_stall_test.go:191) failed once: the test's held bolt write caught the runner's jstart (a 30s slow request) instead of the archive, so the run never reached its archive. Make the hold target the archive write deterministically.
  - Red: a temporary `time.Sleep(300 * time.Millisecond)` in `handleStart`
    (`jobqueue/serverCLI.go`) just before `s.db.updateJobAfterChangeDurable(job)`,
    then `go test -count=1 -run '^TestArchiveStallDoesNotRerunJob$' ./jobqueue/`
    failed: `slow request method=jstart duration=30.108928823s` and
    `archive_stall_test.go Line 191: Expected: true Actual: false`.
  - Cause (test): `handleStart` sets StartTime in memory before the start's
    durable write, and the test began its bolt hold on seeing StartTime, so the
    hold could block the start's write rather than the archive's.
  - Fix (test only), `jobqueue/archive_stall_test.go`: arm the hold only after
    the existing `startPersistedHook` reports the reserved job's start
    committed. All assertions are kept; the held write is still shown to be the
    archive (`archivesPending == 1`).
  - After: with the same injection, 3 of 3 pass; without it, `-count=10` and a
    `-race` run pass. `make lint` reports 0 issues.
  - Superseded on rebase: #657 (`fc2460c2`, "Hold the archive stall test's
    transaction only once the start is on disk") made the same fix on
    develop, also waiting on `startPersistedHook`. This branch's version of
    the test change was dropped when rebasing onto develop `8ee8c00d`, so
    there is one implementation: develop's, which closes a channel through a
    `sync.Once`. Only this checklist entry remains from this item.
- [x] Test env leak: jobqueue/behaviours_env_test.go:177 TestBehaviourRunEnv ("a CwdMatters Job's run behaviour gets its environment untouched") fails whenever the caller has TMPDIR set because it inherits it. Make it hermetic.
  - Red: `TMPDIR=/tmp go test -count=1 -run '^TestBehaviourRunEnv$' ./jobqueue/`
    failed: `Line 177: Expected '/tmp' to be blank (but it wasn't)!`.
  - Cause (test): `storeTestEnv` builds the Job's stored environment from
    `os.Environ()`. For a CwdMatters Job wr makes no directories, so
    `envWithRunDirs` passes the stored environment through as intended,
    including the caller's TMPDIR.
  - Fix (test only), `jobqueue/behaviours_env_test.go`: store a sentinel
    `TMPDIR=/nonexistent/stored-tmp` and assert the behaviour sees exactly it,
    which also fails if wr injects a TMPDIR of its own (mutation-checked).
  - After: passes with TMPDIR set and unset; `make lint` reports 0 issues.
- [x] developers/wrdev.sh: asl_adder (~line 1625) seeds RANDOM from $(date +%N); a leading zero makes bash read it as octal (e.g. 08/09 invalid), killing adders. Fix (e.g. strip leading zeros / use 10#).
  - Red: `bash -c 'date() { echo 089123456; }; id=1; eval "$(grep -m1 "RANDOM=.*date +%N" developers/wrdev.sh)"; echo seeded'`
    failed: `value too great for base (error token is "089123456")`.
  - Fix, `developers/wrdev.sh`: read the nanoseconds as `10#$(date +%N)`.
    No other zero-padded date field reaches bash arithmetic in `developers/`
    or `.docs/reliable/` (`%s%3N` starts with the epoch; `%s.%N` goes to `bc`).
  - After: the red prints `seeded` for `089123456` and `000000001`;
    `bash -n developers/wrdev.sh` passes. Verified directly by the
    orchestrator given the one-token change.
- [x] developers/wrdev.sh add-storm-fixture bakes the generating root's absolute wr binary path and WR_CONFIG_DIR into self-adding jobs, so fixtures only exercise jobs-adding-jobs when run from the root that built them (and could add to another root's manager if it ran). Make self-adding jobs call a wrapper script in the fixture's job cwd that add-storm-lsf (re)writes at the start of each run to point at the current root's binary and config; keep existing fixtures usable.
  - Red: an offline harness (scratch, not committed) that sources `wrdev.sh`
    under a scratch `WRDEV_ROOT`, drives the fixture job generator, the wrapper
    writer and the incomplete-job audit with a stub `wr`. Before: 6 checks
    failed, including `no command bakes WR_CONFIG_DIR`, `no command bakes the
    generating root's binary`, `every self-adding command pipes into
    <jobcwd>/wradd.sh`, and `audit rejects tampered/foreign commands` (the old
    audit only checked the prefix, so `echo aslfix 4 plain; touch /tmp/x`
    passed).
  - Fix, `developers/wrdev.sh`:
    - Self-adding jobs pipe their child into `<fixture>.jobcwd/wradd.sh`
      (generator moved into `asl_fixture_jobs`); nothing root-specific is baked.
    - `asl_write_wradd` writes the wrapper atomically (mktemp and mv, `%q`
      quoting): `HOME=… WR_CONFIG_DIR=… exec <wr> add -f - --deployment
      production "$@"`. `add-storm-lsf` writes it for its own root before any
      manager starts.
    - `add-storm-lsf` claims the fixture with an exclusive-create
      `<jobcwd>/wradd.owner` before setting its traps, so a second concurrent
      run (from any root) refuses without touching the first run's manager or
      jobs; `asl_cleanup` removes the claim. A run killed with SIGKILL leaves
      the claim behind, and the refusal names the file to delete.
    - `asl_prefix_audit` accepts only the two exact generated forms, with the
      self-add's flags pinned to the generated ones (the queue may differ, in a
      safe charset). Its JSON regex no longer drops a command with an escaped
      quote from the count.
    - The manifest is `aslfixture 2`; a version 1 fixture is refused before
      anything starts with a message to regenerate it.
    - The fixture path is restricted to `[A-Za-z0-9._/-]`, since the generator
      embeds it unquoted.
  - After: the harness reports `fails=0`; the wrapper run against a stub `wr`
    under `env -i` passed the right binary, config dir, HOME, args, stdin and
    exit code; v1 manifests and a held claim are refused offline.
    `bash -n developers/wrdev.sh` passes. Not run: a real fixture build or
    LSF run.
- [x] .docs/reliable/harness/loadrunner.go (soak harness fake runner) should set the new runner marker that `wr runner` sets (Client.SetReserveAsRunner), only if PR #657 (fix-moved-on-runner) has merged into develop.
  - Skipped: PR #657 (fix-moved-on-runner) was still open, not merged into
    develop, on 26-10-01, so `Client.SetReserveAsRunner` is not on develop yet.
    Do this once #657 merges.
  - Resumed on 26-10-02: #657 merged into develop (`e8751718`), and this
    branch is rebased onto develop `8ee8c00d`, so `Client.SetReserveAsRunner`
    exists (`jobqueue/client.go`; `cmd/runner.go` calls it after
    `SetReserveSchedulerID`). The harness is not in a Go package the gates
    build (it lives under `.docs/`), so there is no test seam.
  - Red (check): `grep -q SetReserveAsRunner .docs/reliable/harness/loadrunner.go`
    exits 1. Note `-mode hold` reserves `-hold-per` jobs on one client, which
    a runner-marked client must not do (the manager would release each earlier
    job as soon as it reserves the next), so the marker belongs only on
    clients that hold one job at a time.
  - Fix, `.docs/reliable/harness/loadrunner.go`: each worker client calls
    `c.SetReserveAsRunner(*mode != "hold" || *holdPer == 1)`, with a comment
    like `cmd/runner.go`'s. drive (and an unknown mode, which falls through to
    it) and hold with the default `-holdper 1` get the marker; hold with more
    than one held job does not; churn and ping reserve nothing.
  - After: the grep check passes; the README build of the harness and `go vet`
    on it are clean; `make lint` reports 0 issues. A drive run (4 workers)
    against an isolated local manager under /tmp reserved, started and
    archived 199 of 200 jobs (the manager's own local runner ran the other).
  - Reviewer: PASS. In drive mode a worker reserves again before settling its
    job only after `Started` or `Archive` failed, when the job is already
    abandoned, so releasing it then is intended.
- [ ] Deferred (found while doing the loadrunner item, independent, low
  impact): loadrunner's `-group ""` fallback builds the group from
  `-ram/-time/-cores/-disk` as `100:1:1:0`, which does not match the group
  the manager gives jobs added with those requirements (for example
  `200:30:1:0:<hash>`), so drive mode without `-group` silently reserves
  nothing. The README and `exp_drive_ab.sh` pass the group from the manager's
  log, so only ad-hoc use is affected. Also, the README's mode list
  (line ~40) leaves out churn.
