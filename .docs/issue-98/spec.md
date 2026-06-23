# Live Job Introspection Specification

## Overview

Running job detail views already receive live walltime and state changes through
runner touches and job subscriptions. Extend that path so each touch can carry
the runner's latest peak RAM, CPU time, recent stdout/stderr tail, and actual
working directory. The manager stores the latest live snapshot on the running
job and pushes it to existing per-job subscribers.

The feature is a secure convenience view, not a terminal. It must only expose
live output tails and SSH commands over authenticated TLS manager surfaces. If
live data is absent, an older runner touches the job, or the secure gate is not
enabled for an authenticated runner, existing touch, status, and subscription
behaviour remains unchanged.

## Architecture

### Packages and files

- `jobqueue/client.go`: keep public `Touch` unchanged, add bounded live
  stdout/stderr tail capture inside `Execute`, snapshot live peak RAM, disk,
  CPU time, and actual cwd on the existing touch ticker. Test in
  `jobqueue/client_payload_test.go` and `jobqueue/jobqueue_test.go`.
- `jobqueue/utils.go`: add the bounded live tail writer/compressor next to
  `prefixSuffixSaver` and `stdFilter`. Test in `jobqueue/utils_test.go`.
- `jobqueue/serverCLI.go`: extend `jtouch` to apply live snapshots and enqueue
  live subscription updates after `q.Touch`. Test in
  `jobqueue/jobqueue_test.go` or `jobqueue/subscription_test.go`.
- `jobqueue/subscription.go`: add `JobUpdateLive` and live fields to
  `JobUpdate`. Existing terminal wait helpers ignore non-terminal live updates.
  Test in `jobqueue/subscription_test.go`.
- `jobqueue/server_subscription.go`: deliver live updates to key subscriptions
  and status websocket detail subscriptions. Live updates must not complete a
  RepGroup subscription. Test in `jobqueue/subscription_test.go`.
- `jobqueue/job.go` and `jobqueue/serverWebI.go`: include live fields and an
  SSH command in `JStatus`. Test in `jobqueue/serverWebI_test.go` and
  `jobqueue/rest_test.go`.
- `jobqueue/static/status.html`, `jobqueue/static/js/wr/websocket-handler.js`,
  `jobqueue/static/js/wr/utility.js`, and `jobqueue/static/css/wr-0.36.0.css`:
  render live RAM/CPU, live stdout/stderr, and a copyable SSH command for
  running jobs. Do not add an embedded terminal.

### Public API and wire shape

Keep this public method signature:

```go
func (c *Client) Touch(job *Job) (bool, error)
```

Do not change `JobEndState`. During a live touch `Exited` is false, `Exitcode`
and `EndTime` are ignored, and these existing fields are meaningful:

```go
Cwd      string
PeakRAM  int
PeakDisk int64
CPUtime  time.Duration
Stdout   []byte
Stderr   []byte
Exited   bool
```

Append the new kind after existing `JobUpdateKind` values so old numeric values
do not move:

```go
const (
    JobUpdateTerminal JobUpdateKind = iota
    JobUpdateLost
    JobUpdateRepGroupDone
    JobUpdateResync
    JobUpdateStateChange
    JobUpdateLive
)
```

Extend `JobUpdate` without removing existing fields. Add:

```go
PeakRAM    int
PeakDisk   int64
Pid        int
CPUtime    time.Duration
Host       string
HostID     string
HostIP     string
CwdBase    string
Cwd        string
StdOut     string
StdErr     string
SSHCommand string
```

Extend `JStatus` without removing existing fields. Add:

```go
SSHCommand string
```

### Semantics

- Touch frequency stays `Client.touchInterval`; no new heartbeat or polling
  loop is added.
- Live stdout/stderr tail is per stream:
  - raw in-memory tail cap: `64 * 1024` bytes;
  - compressed zlib payload cap: `4096` bytes;
  - nil when no bytes were written since the previous touch attempt;
  - data is reset after each touch attempt, successful or failed;
  - final archived stdout/stderr still uses the existing final
    `prefixSuffixSaver` path and is not truncated by live flushing.
- Live `CPUtime` is cumulative process-tree user+system CPU time observed while
  the job is running. Docker-monitored jobs add the existing container CPU
  seconds. Zero means unavailable.
- Live `PeakRAM` is the highest MB value observed so far by the existing
  resource checker. `PeakDisk` is optional but should be updated when already
  available.
- A live snapshot is present only when `JobEndState != nil` and at least one
  live field is set: `Cwd != ""`, `PeakRAM != 0`, `PeakDisk != 0`,
  `CPUtime != 0`, `len(Stdout) != 0`, or `len(Stderr) != 0`. Nil
  `JobEndState`, `&JobEndState{}`, and all-zero live fields from older runners
  count as absent live data.
- The manager applies live snapshots only for authenticated `jtouch` requests
  and only when live introspection is enabled. In current `Serve` this means the
  manager has a valid token and HTTPS web port. If `ServerInfo.WebPort == ""`
  for an authenticated runner, `jtouch` only extends TTR and returns
  `KillCalled`.
- Absent live data must keep existing `jtouch` TTR and `KillCalled` behaviour.
  It must not apply a live snapshot or emit live subscription or websocket
  updates.
- If HTTPS is present but the `jtouch` token is absent or invalid, the request
  is denied. It must not touch TTR, update live fields, emit live updates, or
  return a normal `KillCalled` response.
- Applying a live snapshot must not set `Exited`, `Exitcode`, `EndTime`, or a
  terminal state. Lost-to-running recovery performed by existing `jtouch`
  remains unchanged.
- `JobUpdateLive` is delivered to key subscriptions and status websocket detail
  subscriptions. `AddAndWait` and terminal collection ignore it. RepGroup-only
  subscriptions do not emit per-job live events and live events do not trigger
  `JobUpdateRepGroupDone`.
- SSH command is only for `JobStateRunning`. Return an empty command for
  complete, buried, and lost jobs, even when `Host`, `HostIP`, and `ActualCwd`
  are still set from the run. UI must hide the SSH command control whenever the
  command is empty.
- For running jobs, SSH command target is `cloud_user@HostIP` when
  `Requirements.Other` contains `cloud_user` and `HostIP` is set; otherwise use
  `HostIP`, then `Host`. The remote command is
  `cd <actual-cwd> && exec ${SHELL:-/bin/sh} -l`, with local shell arguments and
  the remote cwd shell-quoted. Return an empty command when host or actual cwd
  is missing.

## Section A: Runner Touch Payload

### A1: Bound live output tail payloads

As a runner, I want to send only a bounded recent output tail, so that touch
payloads stay small even when jobs write large logs.

**Package:** `jobqueue/`
**File:** `jobqueue/utils.go`
**Test file:** `jobqueue/utils_test.go`

Add unexported helpers:

```go
const (
    liveStdRawTailLimit        = 64 * 1024
    liveStdCompressedLimit     = 4096
)

type liveTailSaver struct { /* unexported fields */ }

func (w *liveTailSaver) Write(p []byte) (int, error)
func (w *liveTailSaver) FlushCompressed() []byte
```

**Acceptance tests:**

1. Given a new `liveTailSaver`, when `Write([]byte("one\n"))` then
   `FlushCompressed()` is called, then the compressed result is non-nil,
   `len(result) <= 4096`, and decompressing it returns exactly `"one\n"`.
2. Given test 1 has flushed, when `FlushCompressed()` is called again without
   more writes, then it returns nil.
3. Given a new `liveTailSaver`, when it writes 64 KiB of deterministic
   pseudo-random bytes from `rand.New(rand.NewSource(1))`, then
   `FlushCompressed()` returns non-nil bytes with `len(result) <= 4096`, and
   decompressed bytes equal a suffix of the written bytes.
4. Given a new `liveTailSaver`, when it writes `"UNIQUE-PREFIX\n"`, 128 KiB
   of deterministic pseudo-random bytes, and `"UNIQUE-SUFFIX\n"`, then
   decompressed flush output contains `"UNIQUE-SUFFIX\n"` and does not contain
   `"UNIQUE-PREFIX\n"`.
5. Given stdout writes `"old\n"` then flushes and later writes `"new\n"`, when
   the second flush is decompressed, then it returns exactly `"new\n"`.

### A2: Send live metrics on existing touches

As a runner, I want each normal touch to include current live resource and tail
data, so that clients see the latest run information without a new polling loop.

**Package:** `jobqueue/`
**File:** `jobqueue/client.go`
**Test file:** `jobqueue/client_payload_test.go`

Add an unexported helper and keep `Touch` as a wrapper:

```go
func (c *Client) touch(job *Job, endState *JobEndState) (bool, error)
```

**Acceptance tests:**

1. Given a job with key `k`, `PeakRAM=123`, `PeakDisk=456`,
   `CPUtime=7*time.Second`, `StdOutC=zlib("out\n")`, and
   `StdErrC=zlib("err\n")`, when `Touch(job)` is called on a capture client,
   then the request method is `"jtouch"`, `Keys == []string{k}`, `Job == nil`,
   and `JobEndState` contains exactly those five live fields.
2. Given `Execute` runs a command in actual cwd `/tmp/wr-live/job1`, and the
   command writes stdout `"alpha\n"` before the first touch and `"beta\n"`
   before the second touch, when test hooks capture the two live touch states,
   then their decompressed stdout values are exactly `"alpha\n"` and
   `"beta\n"`, and both have `Cwd == "/tmp/wr-live/job1"`.
3. Given `Execute` runs a command that writes stderr `"err-alpha\n"` before
   the first touch and `"err-beta\n"` before the second touch, when test hooks
   capture the two live touch states, then decompressed
   `JobEndState.Stderr` values are exactly `"err-alpha\n"` and
   `"err-beta\n"`.
4. Given a running process tree has used at least `1*time.Millisecond` CPU and
   peak RAM has been observed as `>= 1`, when a touch state is captured, then
   `JobEndState.CPUtime >= 1*time.Millisecond` and `PeakRAM >= 1`.
5. Given `Execute` writes `OUT-OLD\n`, 128 KiB of deterministic pseudo-random
   stdout, `OUT-NEW\n`, `ERR-OLD\n`, 128 KiB of deterministic pseudo-random
   stderr, and `ERR-NEW\n` before the first touch, when a test hook captures
   that live touch and the job archives, then captured `JobEndState.Stdout` and
   `JobEndState.Stderr` are non-nil, each `len <= 4096`, each decompresses to
   a suffix containing its `*-NEW\n` marker and not its `*-OLD\n` marker, and
   final archived `StdOut()`/`StdErr()` equal the existing final
   `prefixSuffixSaver` output for each complete stream.

## Section B: Manager Live State

### B1: Apply live snapshots on jtouch

As a manager, I want to store the latest live snapshot on the running job, so
that all existing status paths read the same current data.

**Package:** `jobqueue/`
**File:** `jobqueue/serverCLI.go`
**Test file:** `jobqueue/jobqueue_test.go`

**Acceptance tests:**

1. Given an authenticated server with live introspection enabled and a running
   job with key `k`, when it handles `jtouch` for `k` with
   `JobEndState{Cwd:"/tmp/wr/job1", PeakRAM:321, PeakDisk:9,
   CPUtime:4*time.Second, Stdout:zlib("out\n"), Stderr:zlib("err\n")}`, then
   the response has `KillCalled == false`, the job remains `running`,
   `Exited == false`, `ActualCwd == "/tmp/wr/job1"`, `PeakRAM == 321`,
   `PeakDisk == 9`, `CPUtime == 4*time.Second`, `StdOut() == "out\n"`, and
   `StdErr() == "err\n"`.
2. Given the same job already has `ActualCwd="/tmp/old"`, `PeakRAM=111`,
   `PeakDisk=7`, `CPUtime=2*time.Second`, `StdOutC=zlib("old\n")`, and
   `StdErrC=zlib("olderr\n")`, when an older runner sends authenticated
   `jtouch` with `&JobEndState{}` (all live fields zero/nil/empty), then TTR is
   extended, the response has `KillCalled == false`, the job remains running,
   and all six live fields are unchanged.
3. Given live introspection is disabled because `ServerInfo.WebPort == ""`,
   when an authenticated `jtouch` supplies live stdout and metrics, then TTR is
   extended, the response has `KillCalled == false`, and the job's `PeakRAM`,
   `CPUtime`, `StdOutC`, `StdErrC`, and `ActualCwd` remain unchanged.
4. Given HTTPS is present but auth/token is absent or invalid, when `jtouch`
   supplies live stdout and metrics for a running job, then the response error
   is `ErrPermissionDenied`, no normal `KillCalled` response is returned, TTR is
   not extended, and `PeakRAM`, `CPUtime`, `StdOutC`, `StdErrC`, and
   `ActualCwd` remain unchanged.
5. Given HTTPS is present but auth/token is absent or invalid and a running job
   has `KillCalled == true`, when `jtouch` supplies live stdout and metrics,
   then the response error is `ErrPermissionDenied`, no normal
   `KillCalled == true` response is returned, TTR is not extended, and
   `PeakRAM`, `CPUtime`, `StdOutC`, `StdErrC`, and `ActualCwd` remain
   unchanged.

### B2: Push live updates to job subscribers

As a subscribed client, I want live updates for the jobs I am already watching,
so that I do not need a separate polling API.

**Package:** `jobqueue/`
**File:** `jobqueue/subscription.go`
**Test file:** `jobqueue/subscription_test.go`

**Acceptance tests:**

1. Given a key subscription for running job `k`, when `jtouch` applies
   `PeakRAM=321`, `CPUtime=4*time.Second`, `Stdout=zlib("out\n")`,
   `Stderr=zlib("err\n")`, `Host="worker1"`, `HostIP="10.0.0.8"`,
   `Pid=44`, and `Cwd="/tmp/wr/job1"`, then `Updates()` receives one
   `JobUpdate` with `Kind == JobUpdateLive`, `Key == k`,
   `State == JobStateRunning`, `PeakRAM == 321`,
   `CPUtime == 4*time.Second`, `StdOut == "out\n"`, `StdErr == "err\n"`,
   `Host == "worker1"`, `HostIP == "10.0.0.8"`, `Pid == 44`, and a
   non-empty `SSHCommand`.
2. Given `AddAndWait` is waiting on job `k`, when a `JobUpdateLive` for `k` is
   delivered before the job completes, then `AddAndWait` does not return; when
   `k` is later archived complete, `AddAndWait` returns exactly one complete
   job and nil error.
3. Given a RepGroup subscription has one running job, when that job receives a
   `JobUpdateLive`, then no `JobUpdateRepGroupDone` is delivered within
   `100*time.Millisecond`.
4. Given key and status websocket detail subscriptions watch running job `k`,
   when an older runner sends authenticated `jtouch` with `&JobEndState{}`
   (all live fields zero/nil/empty), then no `JobUpdateLive` and no pushed live
   `JStatus` are delivered within `100*time.Millisecond`.
5. Given live introspection is disabled because `ServerInfo.WebPort == ""`,
   and key and status websocket detail subscriptions watch running job `k`,
   when an authenticated `jtouch` supplies `PeakRAM=321` and
   `Stdout=zlib("out\n")`, then no `JobUpdateLive` and no pushed live
   `JStatus` are delivered within `100*time.Millisecond`.
6. Given HTTPS is present but auth/token is absent or invalid, and key and
   status websocket detail subscriptions watch running job `k`, when `jtouch`
   supplies `PeakRAM=321` and `Stdout=zlib("out\n")`, then the response error
   is `ErrPermissionDenied` and no `JobUpdateLive` and no pushed live `JStatus`
   are delivered within `100*time.Millisecond`.

## Section C: Status JSON and Web UI

### C1: Include live fields and SSH command in status

As a status client, I want running job JSON to include live metrics, output
tail, and a ready-to-copy SSH command, so that I can inspect the job host and
working directory quickly.

**Package:** `jobqueue/`
**File:** `jobqueue/job.go`
**Test file:** `jobqueue/job_test.go`, `jobqueue/serverWebI_test.go`,
`jobqueue/rest_test.go`

Add `SSHCommand` to `JStatus`. Reuse existing `PeakRAM`, `PeakDisk`, `CPUtime`,
`StdOut`, `StdErr`, `Host`, `HostIP`, `Pid`, `CwdBase`, and `Cwd` fields.

**Acceptance tests:**

1. Given a running job with `Host="worker1"`, `HostIP="10.0.0.8"`,
   `Pid=44`, `Cwd="/tmp/wr"`, `ActualCwd="/tmp/wr/job1"`,
   `Requirements.Other["cloud_user"] == "ubuntu"`, `PeakRAM=321`,
   `CPUtime=4*time.Second`, `StdOutC=zlib("out\n")`, and
   `StdErrC=zlib("err\n")`, when `ToStatus()` is called, then
   `CwdBase == "/tmp/wr"`, `Cwd == "/job1"`, `PeakRAM == 321`,
   `CPUtime == 4`, `StdOut == "out\n"`, `StdErr == "err\n"`, and
   `SSHCommand` equals `"ssh ubuntu@10.0.0.8 'cd /tmp/wr/job1 && exec "`
   plus `"${SHELL:-/bin/sh} -l'"`.
2. Given a running job with `Host="worker1"`, empty `HostIP`, and
   `ActualCwd="/tmp/wr/job1"`, when `ToStatus()` is called, then
   `SSHCommand` equals `"ssh worker1 'cd /tmp/wr/job1 && exec "` plus
   `"${SHELL:-/bin/sh} -l'"`.
3. Given a running job has no host or no actual cwd, when `ToStatus()` is
   called, then `SSHCommand == ""`.
4. Given a running job with `Host="worker1"` and
   `ActualCwd="/tmp/wr/live jobs/it's-ok"`, when `ToStatus()` is called, then
   `SSHCommand` equals this Go expression:

   ```go
   `ssh worker1 'cd '"'"'/tmp/wr/live jobs/it'"'"'"'"'"'"'"'"'s-ok'"'"' ` +
       `&& exec ${SHELL:-/bin/sh} -l'`
   ```

5. Given `JobStateComplete`, `JobStateBuried`, and `JobStateLost` jobs each
   have `Host="worker1"`, `HostIP="10.0.0.8"`, and
   `ActualCwd="/tmp/wr/job1"`, when status details call `ToStatus()`, then
   every returned `SSHCommand == ""`.
6. Given an authenticated REST GET for a running job with live data, when the
   JSON is decoded, then the returned `JStatus` has `PeakRAM == 321`,
   `CPUtime == 4`, `StdOut == "out\n"`, `StdErr == "err\n"`, and the SSH
   command from test 1.
7. Given the same REST URL without a token or bearer header, when it is
   requested, then the response status is `401 Unauthorized` and no job JSON is
   returned.

### C2: Render live introspection in running job details

As a web UI user, I want running job details to update in place with live
resources, output tail, and an SSH command, so that I can inspect a running job
without refreshing or opening an embedded terminal.

**Package:** `jobqueue/`
**File:** `jobqueue/static/status.html`,
`jobqueue/static/js/wr/websocket-handler.js`
**Test file:** `jobqueue/serverWebI_test.go`

**Acceptance tests:**

1. Given the details websocket is subscribed to RepGroup `rg1` and job `k` is
   running, when a live touch update for `k` is applied, then the websocket
   sends a `JStatus` with `IsPushUpdate == true`, `State == JobStateRunning`,
   `PeakRAM == 321`, `CPUtime == 4`, `StdOut == "out\n"`,
   `StdErr == "err\n"`, and the SSH command from C1 test 1.
2. Given the browser DOM has a running detail row for `k` with no live values,
   when `handleJobDetailsMessage` receives a push update with `PeakRAM=321`,
   `CPUtime=4`, `StdOut="alpha-out\n"`, and `StdErr="alpha-err\n"`, then the
   DOM panel for `k` visibly contains `321 MB`, `CPU: 4s`, `STDOUT`,
   `alpha-out`, `STDERR`, and `alpha-err`.
3. Given the DOM from test 2, when another push update for `k` has
   `PeakRAM=654`, `CPUtime=8`, `StdOut="beta-out\n"`, and
   `StdErr="beta-err\n"`, then the same DOM panel visibly contains `654 MB`,
   `CPU: 8s`, `beta-out`, and `beta-err`, and no longer contains
   `alpha-out` or `alpha-err`.
4. Given a running job has `SSHCommand == ""`, when details render, then no SSH
   command control is shown for that job.
5. Given a running job has `SSHCommand` equal to the C1 test 1 command, when
   details render, then a copyable command control is shown, its rendered text
   or copy payload equals exactly `JStatus.SSHCommand`, and no embedded web
   terminal element is created.
6. Given the browser DOM has complete, buried, and lost detail rows with
   historical host/cwd values and `SSHCommand == ""`, when details render, then
   no SSH command control is shown for those rows.

## Section D: Compatibility

### D1: Preserve existing status behaviour without live data

As an operator with mixed runner versions, I want old runners and absent live
data to keep working, so that rolling upgrades do not break live status.

**Package:** `jobqueue/`
**File:** `jobqueue/serverCLI.go`
**Test file:** `jobqueue/jobqueue_test.go`, `jobqueue/serverWebI_test.go`

**Acceptance tests:**

1. Given a running job with no live snapshot, when status details are requested,
   then the returned `JStatus` has `PeakRAM == 0`, `CPUtime == 0`,
   `StdOut == ""`, `StdErr == ""`, `SSHCommand == ""`, and the existing
   `LiveWalltime` UI behaviour is unchanged.
2. Given a live snapshot was applied and the job later archives complete with
   final stdout `"final\n"` and stderr `"done\n"`, when status details are
   requested after completion, then `StdOut == "final\n"`,
   `StdErr == "done\n"`, `Exited == true`, and the final `PeakRAM` and
   `CPUtime` values are the archive values, not stale live values.
3. Given a running job has `KillCalled == true` and existing live fields, when
   an authenticated live `jtouch` or older-runner `jtouch` with
   `&JobEndState{}` arrives, then the response still has
   `KillCalled == true`, TTR is not extended, and no live fields are
   overwritten.

## Implementation Order

1. Add bounded live tail capture and live touch state assembly in the runner.
   Stories: A1, A2. Sequential foundation for all later phases.
2. Apply live snapshots in `jtouch` and store them on running jobs behind the
   secure gate. Stories: B1, D1. Depends on phase 1.
3. Add `JobUpdateLive` delivery for key subscriptions and preserve terminal
   waiting semantics. Stories: B2. Depends on phase 2.
4. Add `SSHCommand` and live fields to status JSON, REST, websocket detail
   updates, and static UI rendering. Stories: C1, C2. Depends on phases 2-3.

## Appendix: Key Decisions

- Reuse the existing touch interval and subscription paths; no separate live
  polling endpoint and no embedded terminal are part of v1.
- Use existing status names (`PeakRAM`, `CPUtime`, `StdOut`, `StdErr`) so REST,
  websocket, and UI code share one payload shape.
- Bound stdout and stderr independently to 4096 compressed bytes per stream.
  This keeps each touch payload bounded while final job output remains on the
  current archive path.
- Deliver live updates as a new `JobUpdateLive` kind appended to the enum, so
  terminal update numeric values remain stable and `AddAndWait` can ignore
  live events.
- Follow `go-conventions` and `testing-principles`: GoConvey tests should
  assert visible behaviour through client requests, subscriptions, REST JSON,
  websocket JSON, and job status values.
