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
- [ ] Flake: TestDepGranularitySidecarReportsElapsedTime failed once under load (a prior fix made it wait for the next heartbeat instead of 200ms; check it's on develop and why it can still fail).
- [ ] Flake: TestArchiveStallDoesNotRerunJob (jobqueue/archive_stall_test.go:191) failed once: the test's held bolt write caught the runner's jstart (a 30s slow request) instead of the archive, so the run never reached its archive. Make the hold target the archive write deterministically.
- [ ] Test env leak: jobqueue/behaviours_env_test.go:177 TestBehaviourRunEnv ("a CwdMatters Job's run behaviour gets its environment untouched") fails whenever the caller has TMPDIR set because it inherits it. Make it hermetic.
- [ ] developers/wrdev.sh: asl_adder (~line 1625) seeds RANDOM from $(date +%N); a leading zero makes bash read it as octal (e.g. 08/09 invalid), killing adders. Fix (e.g. strip leading zeros / use 10#).
- [ ] developers/wrdev.sh add-storm-fixture bakes the generating root's absolute wr binary path and WR_CONFIG_DIR into self-adding jobs, so fixtures only exercise jobs-adding-jobs when run from the root that built them (and could add to another root's manager if it ran). Make self-adding jobs call a wrapper script in the fixture's job cwd that add-storm-lsf (re)writes at the start of each run to point at the current root's binary and config; keep existing fixtures usable.
- [ ] .docs/reliable/harness/loadrunner.go (soak harness fake runner) should set the new runner marker that `wr runner` sets (Client.SetReserveAsRunner), only if PR #657 (fix-moved-on-runner) has merged into develop.
