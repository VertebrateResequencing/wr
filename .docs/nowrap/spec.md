# No-shell Client API Specification

## Overview

External Go packages should not shell out to `wr add` or `wr status` to submit
jobs, get stable job keys, wait for completion, or inspect status. The
`client.Scheduler` type already owns deployment config, cwd defaults, queue
settings, default requirements, and pretend submissions, but it exposes only
`SubmitJobs` for adds.

Add a small high-level API on `client.Scheduler` that wraps the existing
`jobqueue.Client` ID, status, and subscription APIs. Keep all existing
`SubmitJobs` behavior for current consumers.

Key behaviors:

- Submit jobs and return stable job keys without parsing CLI output.
- Submit and wait with `context.Context`, returning terminal jobs with
  stdout/stderr populated.
- Wait later by previously returned keys.
- Fetch typed job status by key.
- Wait until a key is running, lost, complete, buried, unknown, missing, or
  canceled; `reserved` is still pre-start and keeps polling.
- Make queued duplicates and completed-job reruns explicit.
- Preserve useful pretend-mode behavior for downstream tests.

## Architecture

### Packages and files

- `client/client.go`: add public API types and `Scheduler` methods, extend the
  private `jobqueueClient` interface, and implement pretend-mode equivalents.
  Test in `client/client_test.go`.
- `client/client_compat_test.go`: add compile-only tests for the existing
  `Scheduler` surface used by `wrstat`, `wrstat-ui`, and `ibackup`.
- `client/example_test.go`: add godoc examples showing no-shell equivalents of
  `wr add --simple`, `wr add --sync`, `wr add --rerun`, and current
  `wr add` JSON via `NewJobFromJSON`. Test by compiling with the client
  package tests.
- `jobqueue/subscription.go`: reuse `Client.AddAndWait` and
  `Client.SubscribeToJobKeys`. Add a lower-level wait-by-keys helper here only
  if duplicating the terminal-key collector in `client` would be worse. Test in
  `jobqueue/subscription_test.go` if changed.
- `jobqueue/client.go`: reuse `AddAndReturnIDs`, `GetByEssence`, and
  `GetByEssences`. Do not change existing signatures unless a lower-level
  helper is strictly required.
- `jobqueue/serverREST.go`: reuse exported `JobViaJSON` and `JobDefaults`.
  Do not create a parallel client job-spec model.

### Public API

Add these exports to package `client`:

```go
var ErrDuplicateJobs = errors.New("some of the added jobs were duplicates")

type SubmitJobsOptions struct {
    // EnvVars is passed to wr for job execution. nil means os.Environ().
    // A non-nil empty slice means no environment variables.
    EnvVars []string

    // RerunCompleted matches `wr add --rerun`. false skips already complete
    // matching jobs; true re-adds them.
    RerunCompleted bool
}

func (s *Scheduler) SubmitJobsAndReturnIDs(
    jobs []*jobqueue.Job,
    opts SubmitJobsOptions,
) ([]string, error)

func (s *Scheduler) SubmitJobsAndWait(
    ctx context.Context,
    jobs []*jobqueue.Job,
    opts SubmitJobsOptions,
) ([]*jobqueue.Job, error)

func (s *Scheduler) WaitForJobs(
    ctx context.Context,
    keys ...string,
) ([]*jobqueue.Job, error)

func (s *Scheduler) GetJobByKey(
    key string,
    getStd bool,
    getEnv bool,
) (*jobqueue.Job, error)

func (s *Scheduler) WaitForRunning(
    ctx context.Context,
    key string,
    pollInterval time.Duration,
) (*jobqueue.Job, error)

func (s *Scheduler) JobDefaults() *jobqueue.JobDefaults

func (s *Scheduler) NewJobFromJSON(
    spec *jobqueue.JobViaJSON,
) (*jobqueue.Job, error)
```

`SubmitJobs` remains and returns `ErrDuplicateJobs` for queued duplicates. Keep
the old error string exactly for compatibility. `ErrDuplicateJobs` is a
package-level sentinel variable; `errors.Is(err, ErrDuplicateJobs)` must be true
for duplicate errors returned by `SubmitJobs`.

### Semantics

- `SubmitJobsOptions{}` uses `os.Environ()` and `ignoreComplete=true`, matching
  default `wr add` completed-job behavior.
- `SubmitJobsOptions{RerunCompleted: true}` uses `ignoreComplete=false`,
  matching `wr add --rerun`.
- `SubmitJobsAndReturnIDs` returns keys from `jobqueue.Client.AddAndReturnIDs`.
  Queued duplicates are not an error; their existing keys are returned. Already
  complete matching jobs are skipped when `RerunCompleted` is false.
- `SubmitJobsAndWait` calls `jobqueue.Client.AddAndWait`. Results are in key
  order after de-duplicating returned keys. Complete and buried jobs are
  successful results; job failure is represented by `State`, `Exitcode`, and
  `FailReason`, not a Go error.
- If `ctx` is canceled while waiting, return terminal jobs gathered so far plus
  an error that contains every unfinished key.
- `WaitForJobs` de-duplicates supplied keys in input order. It first resolves
  current status with stdout/stderr; keys already `complete` or `buried` before
  the call return immediately as typed jobs. It waits only for non-terminal
  keys to reach `complete` or `buried`. On context cancellation or deadline,
  return terminal jobs gathered so far in key order plus an error wrapping
  `ctx.Err()` whose string contains
  `unfinished job keys: ` followed by comma-separated unfinished keys in key
  order.
- `GetJobByKey` wraps `GetByEssence(&jobqueue.JobEssence{JobKey: key}, ...)`.
  Blank keys return `jobqueue.Error{Op: "GetJobByKey", Err:
  jobqueue.ErrBadRequest}`. Missing keys return `jobqueue.Error{Op:
  "GetJobByKey", Item: key, Err: jobqueue.ErrBadJob}`.
- `WaitForRunning` polls `GetJobByKey(key, false, false)` until state is
  `running`, `lost`, `complete`, `buried`, or `unknown`, then returns that job.
  `reserved` is still pre-start and must keep polling. `lost` is treated as
  "started", matching the `go-softpack-builder` wrapper. `unknown` is treated
  as an invalid status and returned without retrying.
- `WaitForRunning` does not retry keys that cannot resolve to a job. Blank keys
  return `jobqueue.Error{Op: "WaitForRunning", Err:
  jobqueue.ErrBadRequest}`. Missing keys return `jobqueue.Error{Op:
  "WaitForRunning", Item: key, Err: jobqueue.ErrBadJob}`. This matches the old
  wrapper's "not pending anymore" behavior while surfacing typed Go errors.
- If `pollInterval <= 0`, use 5 seconds. Return `ctx.Err()` on cancellation.
- `JobDefaults` maps scheduler defaults into `jobqueue.JobDefaults`: scheduler
  cwd, `CwdMatters=true`, queue and queues-avoid settings, default RAM, time,
  cores, disk, `DiskSet=true`, retries, and override `0`.
- `NewJobFromJSON` errors on nil `spec`, otherwise calls
  `spec.Convert(s.JobDefaults())`.

### Pretend submissions

When `PretendSubmissions` is set:

- `SubmitJobsAndReturnIDs` records jobs like `SubmitJobs`, sets them delayed,
  writes JSON to the configured file descriptor if present, and returns
  `job.Key()` for every input job in input order.
- `SubmitJobsAndWait` records jobs, marks them complete with exit code `0`,
  returns them immediately, and leaves them visible via `SubmittedJobs()`.
- `WaitForJobs` returns recorded jobs whose `Key()` matches each supplied key;
  incomplete matches are marked complete with exit code `0` before return.
- `GetJobByKey` returns the recorded job for that key, or the missing-key error
  defined above.
- `WaitForRunning` returns the recorded job immediately. If it is delayed,
  ready, or reserved, mark it running first. If it is already running, lost,
  complete, buried, or unknown, return it unchanged.

## A. Submit Jobs And Keys

### A1: Return job keys from Scheduler

As an external Go caller, I want to submit jobs through `client.Scheduler` and
receive job keys, so that I can store stable handles without shelling out.

**Package:** `client/`
**File:** `client/client.go`
**Test file:** `client/client_test.go`

Use:

```go
func (s *Scheduler) SubmitJobsAndReturnIDs(
    jobs []*jobqueue.Job,
    opts SubmitJobsOptions,
) ([]string, error)
```

**Acceptance tests:**

1. Given a running test manager and one `s.NewJob("echo ok", "rg-a1",
   "req-a1", "", "", nil)`, when `SubmitJobsAndReturnIDs` is called with
   `SubmitJobsOptions{}`, then it returns `[]string{job.Key()}`, nil error,
   and server stats show `Ready == 1`.
2. Given a submitted job from test 1, when the same job is submitted again with
   `SubmitJobsAndReturnIDs`, then it returns `[]string{job.Key()}`, nil error,
   and server stats still show `Ready == 1`.
3. Given the same duplicate job, when `SubmitJobs` is called, then
   `errors.Is(err, ErrDuplicateJobs)` is true and the error string is exactly
   `"some of the added jobs were duplicates"`.
4. Given test code contains `_ = &ErrDuplicateJobs`, when `go test ./client`
   compiles, then it succeeds, proving `ErrDuplicateJobs` is an addressable
   package-level `var`, and `ErrDuplicateJobs.Error() == "some of the added
   jobs were duplicates"`.

### A2: Control completed-job reruns

As an external Go caller, I want explicit completed-job policy, so that I can
choose between default CLI skipping and `wr add --rerun` behavior.

**Package:** `client/`
**File:** `client/client.go`
**Test file:** `client/client_test.go`

**Acceptance tests:**

1. Given a job has already completed and remains in wr history, when the same
   job is passed to `SubmitJobsAndReturnIDs` with `SubmitJobsOptions{}`, then
   the returned key slice is empty and no new ready job is created.
2. Given the same completed job, when it is passed to
   `SubmitJobsAndReturnIDs` with `SubmitJobsOptions{RerunCompleted: true}`,
   then the returned slice is `[]string{job.Key()}` and one new ready job is
   created.
3. Given `SubmitJobsOptions{EnvVars: []string{"A=B"}}`, when a job is submitted
   and later fetched with `GetJobByKey(key, false, true)`, then `job.Env()`
   contains `A=B`.
4. Given `SubmitJobsOptions{EnvVars: []string{}}`, when a job is submitted and
   later fetched with `GetJobByKey(key, false, true)`, then `job.Env()` returns
   an empty environment slice.

## B. Wait And Status

### B1: Submit and wait for terminal jobs

As an external Go caller, I want to submit jobs and block until they finish, so
that I can replace `wr add --sync` from Go.

**Package:** `client/`
**File:** `client/client.go`
**Test file:** `client/client_test.go`

Use:

```go
func (s *Scheduler) SubmitJobsAndWait(
    ctx context.Context,
    jobs []*jobqueue.Job,
    opts SubmitJobsOptions,
) ([]*jobqueue.Job, error)
```

**Acceptance tests:**

1. Given two submitted jobs and a test runner archives the first with stdout
   `"a1 stdout"` and stderr `"a1 stderr"` and buries the second with exit code
   `12`, fail reason `"b1 failed"`, and stderr `"b1 stderr"`, when
   `SubmitJobsAndWait` is called, then it returns two jobs in submitted-key
   order, nil error, states `complete` and `buried`, exit codes `0` and `12`,
   fail reason `"b1 failed"`, and readable stdout/stderr values above.
2. Given a context canceled before submission, when `SubmitJobsAndWait` is
   called, then it returns nil jobs and `context.Canceled`.
3. Given two jobs where only the first reaches terminal state before the
   context deadline, when `SubmitJobsAndWait` returns, then it returns one job
   for the first key and an error string containing the second key but not the
   first key.
4. Given an already complete matching job and `SubmitJobsOptions{}`, when
   `SubmitJobsAndWait` is called, then it returns an empty job slice and nil
   error.
5. Given an already complete matching job and
   `SubmitJobsOptions{RerunCompleted: true}`, when `SubmitJobsAndWait` is
   called and the rerun is archived, then it returns one complete job for that
   key.

### B2: Wait later by key

As an external Go caller, I want to wait by previously stored job keys, so that
submission and waiting can happen in different process phases.

**Package:** `client/`
**File:** `client/client.go`
**Test file:** `client/client_test.go`

Use:

```go
func (s *Scheduler) WaitForJobs(
    ctx context.Context,
    keys ...string,
) ([]*jobqueue.Job, error)
```

**Acceptance tests:**

1. Given two live jobs submitted with `SubmitJobsAndReturnIDs`, when
   `WaitForJobs(ctx, keys...)` is called and both jobs are archived, then it
   returns two complete jobs in key order with nil error.
2. Given one job is already archived as complete with stdout `"pre stdout"`
   and stderr `"pre stderr"` and another is already buried with exit code `7`,
   fail reason `"pre failed"`, and stderr `"pre buried stderr"` before
   `WaitForJobs` starts, when `WaitForJobs(ctx, completeKey, buriedKey)` is
   called and no new job updates are sent, then it returns two jobs in key
   order, nil error, states `complete` and `buried`, exit codes `0` and `7`,
   fail reason `"pre failed"`, and those stdout/stderr values.
3. Given keys `[]string{key1, key1, key2}`, when both distinct jobs complete,
   then `WaitForJobs` returns exactly two jobs in order `key1`, `key2`.
4. Given no keys, when `WaitForJobs(ctx)` is called, then it returns an empty
   slice and nil error.
5. Given keys `[]string{""}`, when `WaitForJobs` is called, then it returns
   `jobqueue.Error{Op: "WaitForJobs", Err: jobqueue.ErrBadRequest}`.
6. Given two live job keys where only `key1` reaches complete before the
   context deadline expires, when `WaitForJobs(ctx, key1, key2)` returns, then
   it returns one complete job with `Key() == key1`, `errors.Is(err,
   context.DeadlineExceeded)` is true, and `err.Error()` contains
   `"unfinished job keys: "+key2` but does not contain `key1`.

### B3: Fetch typed status by key

As an external Go caller, I want typed job status by key, so that I do not run
and parse `wr status`.

**Package:** `client/`
**File:** `client/client.go`
**Test file:** `client/client_test.go`

Use:

```go
func (s *Scheduler) GetJobByKey(
    key string,
    getStd bool,
    getEnv bool,
) (*jobqueue.Job, error)
```

**Acceptance tests:**

1. Given a submitted ready job key, when `GetJobByKey(key, false, false)` is
   called, then it returns a job with `Key() == key`, `State == ready`, and nil
   error.
2. Given a complete job with stdout `"typed stdout"` and stderr
   `"typed stderr"`, when `GetJobByKey(key, true, false)` is called, then
   `StdOut()` returns `"typed stdout"` and `StdErr()` returns
   `"typed stderr"`.
3. Given key `""`, when `GetJobByKey("", false, false)` is called, then it
   returns nil job and `jobqueue.ErrBadRequest` in a `jobqueue.Error`.
4. Given key `"missing-key"`, when `GetJobByKey("missing-key", false, false)`
   is called, then it returns nil job and `jobqueue.ErrBadJob` in a
   `jobqueue.Error`.

## C. Wait For Running

### C1: Return when a job starts or has already ended

As an external Go caller, I want to wait until a job has started running, so
that I can separate "build started" from "build finished".

**Package:** `client/`
**File:** `client/client.go`
**Test file:** `client/client_test.go`

Use:

```go
func (s *Scheduler) WaitForRunning(
    ctx context.Context,
    key string,
    pollInterval time.Duration,
) (*jobqueue.Job, error)
```

The helper may poll `GetJobByKey`; do not depend on key subscriptions emitting
non-terminal running updates.

**Acceptance tests:**

1. Given a ready job key, when `WaitForRunning(ctx, key, 10*time.Millisecond)`
   is called and a test runner reserves and starts the job, then it returns a
   job with `State == running`, `Key() == key`, and nil error.
2. Given `GetJobByKey` returns `State == reserved` before each final state,
   when `WaitForRunning` is exercised with final states `running`, `lost`,
   `complete`, `buried`, and `unknown`, then no call returns `reserved`, each
   call returns the final state with nil error, and a sequence that stays
   `reserved` until context cancellation returns nil job and `context.Canceled`.
3. Given a job reaches lost before polling observes running, when
   `WaitForRunning` is called, then it returns a job with `State == lost`,
   `Key() == key`, and nil error.
4. Given a job reaches complete before polling observes running, when
   `WaitForRunning` is called, then it returns a job with `State == complete`
   and nil error.
5. Given a job reaches buried before polling observes running, when
   `WaitForRunning` is called, then it returns a job with `State == buried`
   and nil error.
6. Given `GetJobByKey` observes `State == unknown`, when `WaitForRunning` is
   called, then it returns that job with `State == unknown` and nil error.
7. Given key `""`, when `WaitForRunning` is called, then it returns nil job and
   `jobqueue.ErrBadRequest` in a `jobqueue.Error` with `Op ==
   "WaitForRunning"`.
8. Given key `"missing-key"`, when `WaitForRunning` is called, then it returns
   nil job and `jobqueue.ErrBadJob` in a `jobqueue.Error` with `Op ==
   "WaitForRunning"` and `Item == "missing-key"`.
9. Given a ready job and a context deadline expires before it starts, when
   `WaitForRunning` is called, then it returns nil job and
   `context.DeadlineExceeded`.
10. Given `pollInterval <= 0`, when `WaitForRunning` is called, then it polls
   without panic and uses a 5 second interval; use a canceled context in the
   test so it returns immediately with `context.Canceled`.

## D. JSON Job Construction And Docs

### D1: Convert JobViaJSON with Scheduler defaults

As an external Go caller, I want an obvious path from `wr add` JSON to
`*jobqueue.Job`, so that existing stdin-to-CLI wrappers can move to Go calls.

**Package:** `client/`
**File:** `client/client.go`
**Test file:** `client/client_test.go`

Use:

```go
func (s *Scheduler) JobDefaults() *jobqueue.JobDefaults

func (s *Scheduler) NewJobFromJSON(
    spec *jobqueue.JobViaJSON,
) (*jobqueue.Job, error)
```

**Acceptance tests:**

1. Given scheduler settings with cwd `tmp`, queue `"short"`, and queues-avoid
   `"slow,big"`, when `JobDefaults()` is called, then returned defaults have
   `Cwd == tmp`, `CwdMatters == true`, `SchedulerQueue == "short"`,
   `SchedulerQueuesAvoid == "slow,big"`, `Memory == 100`, `Time == 10s`,
   `CPUs == 1`, `Disk == 1`, `DiskSet == true`, `Retries == 30`, and
   `Override == 0`.
2. Given a `JobViaJSON` with `Cmd: "echo json"`, `RepGrp: "rg-json"`,
   `Retries` pointing to `3`, `LimitGrps: []string{"lg1"}`,
   `Memory: "8G"`, `Time: "8h"`, `Override` pointing to `2`, and
   `MountConfigs: jobqueue.MountConfigs{{Mount: "mnt", CacheBase:
   "cache-base", Targets: []jobqueue.MountTarget{{Profile: "prof", Path:
   "bucket/path", Cache: true, CacheDir: "cache-dir", Write: true}}}}`, when
   `NewJobFromJSON` is called, then the returned job has those command, rep
   group, retries, limit groups, RAM, duration, scheduler cwd and queue
   defaults, `Override == 2`, and the same mount config, including
   `CacheBase == "cache-base"`, `CacheDir == "cache-dir"`, `Cache == true`,
   and `Write == true`.
3. Given `NewJobFromJSON(nil)`, when called, then it returns nil job and
   `jobqueue.ErrBadRequest` in a `jobqueue.Error`.
4. Given `JobViaJSON{RepGrp: "missing-cmd"}`, when `NewJobFromJSON` is called,
   then it returns an error string containing `"cmd was not specified"`.

### D2: Document the no-shell path

As an external Go caller, I want examples in package docs, so that I can find
the direct Go replacement for `wr add` without reading `cmd/add.go`.

**Package:** `client/`
**File:** `client/example_test.go`
**Test file:** `client/example_test.go`

**Acceptance tests:**

1. Given the client package examples, when `go test ./client` runs, then
   examples named `ExampleScheduler_SubmitJobsAndReturnIDs`,
   `ExampleScheduler_SubmitJobsAndWait`,
   `ExampleScheduler_SubmitJobsAndReturnIDs_rerunCompleted`, and
   `ExampleScheduler_NewJobFromJSON` compile.
2. Given `ExampleScheduler_SubmitJobsAndReturnIDs`, then its body shows
   creating a scheduler, creating a job with `NewJob`, calling
   `SubmitJobsAndReturnIDs`, and storing the returned key.
3. Given `ExampleScheduler_SubmitJobsAndWait`, then its body shows passing a
   `context.Context` and reading `State`, `Exitcode`, `FailReason`, `StdOut`,
   and `StdErr` from returned jobs.
4. Given `ExampleScheduler_SubmitJobsAndReturnIDs_rerunCompleted`, then its
   body shows `SubmitJobsOptions{RerunCompleted: true}`.
5. Given `ExampleScheduler_NewJobFromJSON`, then its body shows a
   `jobqueue.JobViaJSON` with `Cmd`, `RepGrp`, `Retries`, `LimitGrps`,
   `Memory: "8G"`, `Time: "8h"`, `Override`, and `MountConfigs` containing
   a writable cached S3 target with `Path`, `Cache: true`, `CacheDir`, and
   `Write: true`; it calls `NewJobFromJSON`, then submits the converted job
   with `SubmitJobsAndReturnIDs` and
   `SubmitJobsOptions{RerunCompleted: true}`.

## E. Pretend Mode

### E1: New methods work with PretendSubmissions

As a downstream test author, I want pretend submissions to cover the new API,
so that tests do not need a real wr manager.

**Package:** `client/`
**File:** `client/client.go`
**Test file:** `client/client_test.go`

**Acceptance tests:**

1. Given `PretendSubmissions = " "` and two jobs, when
   `SubmitJobsAndReturnIDs` is called, then it returns
   `[]string{job1.Key(), job2.Key()}`, `SubmittedJobs()` returns the same job
   pointers in order, and both jobs have state `delayed`.
2. Given pretend mode and two jobs, when `SubmitJobsAndWait` is called, then it
   returns those two jobs immediately with state `complete`, exit code `0`, nil
   error, and `SubmittedJobs()` returns the same job pointers.
3. Given pretend mode with a recorded job, when `GetJobByKey(job.Key(), false,
   false)` is called, then it returns that exact job pointer.
4. Given pretend mode with a recorded delayed job, when `WaitForRunning` is
   called for that key, then it returns that job pointer with state `running`.
5. Given pretend mode with a recorded delayed job, when `WaitForJobs` is called
   for that key, then it returns that job pointer with state `complete`.
6. Given pretend mode and an output file descriptor in `PretendSubmissions`,
   when `SubmitJobsAndReturnIDs` or `SubmitJobsAndWait` records jobs, then JSON
   for the submitted jobs is written exactly once per method call.

## F. Existing Scheduler Compatibility

### F1: Compile old Scheduler callers unchanged

As a maintainer of an existing downstream package, I want the old
`client.Scheduler` surface to compile unchanged, so that adding no-shell
helpers does not break current users.

**Package:** `client/`
**File:** `client/client.go`
**Test file:** `client/client_compat_test.go`

**Acceptance tests:**

1. Given `client/client_compat_test.go` uses `package client_test` and defines
   the current `ibackup` `JobSubmitter` subset, when
   `go test ./client -run TestSchedulerCompatibility` compiles, then
   `var _ ibackupJobSubmitter = (*client.Scheduler)(nil)` succeeds for
   `NewJob`, `SubmitJobs`, `FindIncompleteJobsByRepGroup`,
   `GetLastCompletionTimeByRepGroup`, `RemoveJobs`, and `Disconnect`.
2. Given compile-only helpers mirror representative unchanged `wrstat` and
   `wrstat-ui` calls, when the same test compiles, then calls to
   `client.New`, `client.DefaultRequirements`, `client.UniqueString`,
   `Executable`, `NewJob`, `SubmitJobs`, `SubmittedJobs`,
   `FindJobsByRepGroupSuffix`, `FindJobsByRepGroupPrefixAndState`,
   `KillJobs`, `RemoveJobs`, and `Disconnect` compile without using any new
   API.
3. Given the compatibility test imports only `client`, `jobqueue`, and
   `jobqueue/scheduler`, when it runs, then it does not require sibling repos,
   a wr manager, or changed downstream source files.

## Implementation Order

1. Add `ErrDuplicateJobs`, `SubmitJobsOptions`, env/completed-policy helpers,
   and `SubmitJobsAndReturnIDs`. Update `SubmitJobs` to return the exported
   sentinel. Cover A1-A2 except wait cases.
2. Add `GetJobByKey`, `SubmitJobsAndWait`, and `WaitForJobs`. Reuse
   `jobqueue.Client.AddAndWait` and subscriptions; add a lower-level helper
   only if necessary. Cover B1-B3.
3. Add `WaitForRunning` using status polling with context cancellation. Cover
   C1.
4. Add `JobDefaults`, `NewJobFromJSON`, and examples. Cover D1-D2.
5. Extend `pretendJobqueue` for all new interface methods and cover E1.
6. Add compile-only compatibility tests for the old `Scheduler` surface. Cover
   F1.
7. Run targeted tests:
   `CGO_ENABLED=1 go test -tags netgo --count 1 ./client -v -run Test`.
   If `jobqueue` changed, also run:
   `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -v -run TestClient`.

Phases 1-3 are sequential. Phases 4-6 can proceed after public signatures
exist.

## Appendix: Key Decisions

- Keep `SubmitJobs` unchanged except for exported duplicate error. Existing
  `wrstat`, `wrstat-ui`, and `ibackup` callers must compile unchanged.
- New ID-returning methods use CLI-like completed-job defaults because their
  purpose is to replace `wr add --simple` and `wr add --sync`.
- Queued duplicates are useful handles, not errors, for
  `SubmitJobsAndReturnIDs`. Callers that want legacy duplicate errors keep
  using `SubmitJobs`.
- `WaitForRunning` is polling-based in `client` because current key
  subscriptions are terminal-oriented. This avoids external status parsers
  while keeping lower-level subscription semantics stable. It treats `lost` as
  started, keeps polling through `reserved`, and stops on missing or unknown
  status to match the migration target's no-longer-pending behavior.
- Reuse `jobqueue.JobViaJSON` and `JobDefaults` to support command,
  `rep_grp`, retries, `limit_grps`, requirements, queues, and mount configs.
- Tests use GoConvey as required by `go-conventions`. Every acceptance test in
  this spec needs a corresponding GoConvey assertion.
