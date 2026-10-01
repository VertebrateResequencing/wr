# Peak RAM includes the runner

- [x] A job's recorded peak RAM includes the `wr runner` process's own
  memory. Go starts children with CLONE_VM|CLONE_VFORK, and on exec Linux
  records the old mm's hiwater RSS into the child's signal->maxrss, so the
  child's rusage Maxrss starts at the PARENT's peak (a /bin/true run from a
  parent holding 700MiB reported 703MB). jobqueue/client.go uses that
  (`peakRSS := rusage.Maxrss`), and ownMemoryMB() then adds the caller's
  memory again. So every job's peak RAM is over-recorded by roughly twice the
  runner's footprint, and memory learning/LSF reservations for small jobs are
  inflated.
  - Source: PR #659 item 1 (.docs/bugfixes/261001-flakes-and-tooling.md on
    that branch).
  - Red: `nice -n 19 env GOFLAGS=-p=2 GOCACHE=/tmp/claude-11346/gocache-peakram
    CGO_ENABLED=0 timeout 1200 go test ./jobqueue -run
    'TestExecutePeakRAM' -count=1 -v` exited 1 on develop:
    `Expected '555' to be less than '256' (but it wasn't)!` (a `sleep` run by
    a runner whose peak was 512MiB; at a 768MiB peak it recorded 810MB, while
    wr's live samples saw 0MB and the runner's own Pss was 24MB).
  - Fix (jobqueue/client.go, jobqueue/utils.go): right after `cmd.Start()`
    (Go's vfork means the exec has happened) Execute reads the runner's own
    `/proc/self/status` VmHWM via `ownPeakRSS()`; on Linux
    `commandPeakRSSMB()` ignores a child Maxrss no higher than that, leaving
    the periodic Pss samples. getrusage(RUSAGE_SELF) was rejected as the
    threshold because the runner's own Maxrss inherits its parent's (e.g. the
    manager's) peak. Fallback threshold 0 keeps the old behaviour. The single
    `ownMemoryMB()` add is kept (schedulers limit runner + command). Darwin is
    unchanged. Rejected: reading the child's VmHWM before reaping (impossible
    for a zombie), avoiding CLONE_VM (Go only does so with CLONE_NEWUSER, and
    a fork copies resident pages anyway).
  - Tests (jobqueue/peak_ram_test.go): small command after a 512MiB runner
    peak records 23MB (was 555MB); a command peaking above the runner's peak
    keeps it; a re-exec child runner under a 512MiB parent keeps a 256MB
    command peak (the getrusage threshold recorded 14MB there).

- [ ] `GOOS=darwin go test -c ./jobqueue` (and `go vet`) fails:
  `jobqueue/depgranularity_startup_test.go:1135:42: undefined:
  pscBindRetryBudget` (also line 1164); the constant is defined in the
  `//go:build linux` file jobqueue/port_selfconnect_test.go (both last touched
  in bbeda258, #652).
  - Source: found by the item 1 implementor while vetting for darwin.
