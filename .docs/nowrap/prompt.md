# Feature: No-shell high-level wr client API for external Go packages

## Background

Several external repos use `wr` from Go code. Some use the current high-level
`client.Scheduler` package successfully, while one important consumer still
carries its own wrapper that shells out to the `wr` binary. The goal of this
feature is to make the supported Go client pleasant enough that external Go
packages can schedule wr jobs, get stable job handles, and await completion
without writing wrappers around command-line invocations.

Current external usage found during investigation:

- `/home/ubuntu/wrstat` imports `github.com/VertebrateResequencing/wr/client`
  and `jobqueue`. It uses `client.New`, `Scheduler.NewJob`,
  `Scheduler.SubmitJobs`, `FindJobsByRepGroupPrefixAndState`,
  `FindJobsByRepGroupSuffix`, `KillJobs`, `RemoveJobs`, `DefaultRequirements`,
  `Executable`, and `UniqueString`.
- `/home/ubuntu/wrstat-ui` imports `client` and `jobqueue`. It creates
  summariser jobs with `Scheduler.NewJob` and submits them with
  `Scheduler.SubmitJobs`, including queue and queues-avoid settings.
- `/home/ubuntu/ibackup` imports `client`, `jobqueue`, `jobqueue/scheduler`,
  and `queue`. Its server and `fofn` watcher use a local `JobSubmitter`
  interface that is effectively a subset of `client.Scheduler`: `NewJob`,
  `SubmitJobs`, `FindIncompleteJobsByRepGroup`,
  `GetLastCompletionTimeByRepGroup`, `RemoveJobs`, and `Disconnect`.
  `ibackup/server/server.go` currently ignores duplicate submission errors by
  string matching for `"duplicate"`, which suggests the high-level API should
  expose duplicate handling more explicitly.
- `/home/ubuntu/go-softpack-builder` has its own package
  `/home/ubuntu/go-softpack-builder/wr`. This is the main remaining no-shell
  gap. It shells out to:
  - `wr add --deployment <deployment> --simple --time 8h --memory <memory> -o 2 --rerun`
    and pipes a JSON job spec on stdin;
  - `wr status --deployment <deployment> -o plain -i <jobID> -y`, polling until
    the job starts or exits;
  - tests also shell out to `wr limit` and `wr status -o json`.
  The wrapper defines its own `WRJobStatus` enum, parses `wr status` output,
  polls every 5 seconds by default, and has `Add`, `WaitForRunning`, `Wait`,
  and `Status` methods. Its job JSON uses fields already supported by
  `wr add` / `jobqueue.JobViaJSON`: `cmd`, `retries`, `rep_grp`,
  `limit_grps`, and S3 mount config.
- `/home/ubuntu/backup-plans` has `wr` as an indirect dependency but no direct
  scheduling API use found.
- `/home/ubuntu/wa` and `/home/ubuntu/wa-jobrun` do not appear to currently
  import `wr`; `wa-jobrun/.docs/wr_changes.md` is historical input for the
  subscription work that has now landed in `wr`.

The current `wr` repo already has major lower-level improvements:

- `jobqueue.Client.AddAndReturnIDs` returns internal job keys for jobs now in
  the queue, including existing duplicates.
- `jobqueue.Client.SubscribeToJobKeys`, `SubscribeToRepGroup`, `Subscription`,
  and `JobUpdate` provide push-style update APIs.
- `jobqueue.Client.AddAndWait(ctx, jobs, envVars, ignoreComplete)` adds jobs
  and waits for each just-added job to reach a terminal state, returning the
  final `*jobqueue.Job` values with stdout/stderr populated.
- `cmd/add.go` uses `AddAndWait` for `wr add --sync`.

The remaining problem is discoverability and ergonomics: those methods live on
the lower-level `jobqueue.Client`, while external packages already prefer
`client.Scheduler` because it handles deployment config, cwd defaults, queue
settings, default requirements, pretend submissions, and common lookup/remove
helpers. `client.Scheduler` currently exposes `SubmitJobs` only; it does not
expose IDs, `AddAndWait`, direct wait/status helpers, or typed duplicate
semantics. Its internal `jobqueueClient` interface also does not include the
new lower-level methods.

## What We Want

Add a small, idiomatic, high-level API to the `client` package so external Go
packages can replace shell wrappers around `wr add` / `wr status` with direct
Go calls.

The feature should preserve all existing `client.Scheduler` behavior for
current consumers, while adding explicit methods for:

- submitting jobs and getting their stable internal job keys;
- submitting jobs and waiting for completion with `context.Context`;
- waiting for previously returned job keys to complete;
- fetching status by job key without parsing CLI output;
- optionally waiting until a job has started running, for callers like
  `go-softpack-builder` that track "build started" separately from "build
  finished";
- making duplicate and "previously complete job" handling explicit, avoiding
  string matching and avoiding surprise errors for expected duplicates.

The API should be easy to use from a package that already has a
`*client.Scheduler` and has created jobs with either `Scheduler.NewJob` or the
existing `jobqueue.JobViaJSON` conversion path.

## Suggested API Shape

The exact names are for the spec to settle, but the resulting API should cover
these capabilities from the `client` package:

- `Scheduler.SubmitJobsAndReturnIDs(...)` or equivalent, backed by
  `jobqueue.Client.AddAndReturnIDs`, returning job keys for jobs now in the
  queue. This should support the same semantics as `wr add --simple`.
- `Scheduler.SubmitJobsAndWait(ctx, jobs, options...)` or equivalent, backed by
  `jobqueue.Client.AddAndWait`, returning terminal `*jobqueue.Job` values. This
  should support the same core semantics as `wr add --sync` but work for one or
  many jobs.
- `Scheduler.WaitForJobs(ctx, keys...)` or equivalent, for code that submitted
  earlier, stored the returned keys, and wants to wait later.
- `Scheduler.GetJobByKey(key, options...)` / `Scheduler.JobStatus(key)` or
  equivalent, returning a typed `*jobqueue.Job` or small status struct rather
  than requiring a caller to run and parse `wr status`.
- `Scheduler.WaitForRunning(ctx, key)` or equivalent if the existing
  subscription machinery can support it cleanly. If not, the spec should
  explicitly decide whether a status-polling helper in `client` is acceptable
  as an interim convenience. The important consumer need is to stop external
  packages writing their own polling loops and status parsers.
- A submission options/result type, if useful, to make these policies explicit:
  - whether previously complete matching jobs should be rerun or ignored
    (`wr add --rerun` maps to `ignoreComplete=false`; the default CLI behavior
    maps to `ignoreComplete=true`);
  - whether existing queued duplicates are an error, returned as existing IDs,
    or reported in a result counter;
  - whether environment variables should default to `os.Environ()` as
    `Scheduler.SubmitJobs` does today.

Do not remove or break `Scheduler.SubmitJobs`. Existing packages use it.

## Job Construction Ergonomics

The spec should consider whether `client` also needs a small helper around
`jobqueue.JobViaJSON.Convert` so external packages that currently build
`wr add` JSON can stop thinking in terms of stdin to a CLI.

The helper does not need to clone all cobra flag parsing from `cmd/add.go`, but
there should be an obvious documented path for creating a job with the fields
that `go-softpack-builder` currently sends to `wr add`:

- command string;
- `rep_grp`;
- `retries`;
- `limit_grps`;
- memory/time/override;
- mount configs, including S3 writable cached mounts;
- queue and queues-avoid settings from `SchedulerSettings`;
- cwd / cwd_matters defaults compatible with `client.Scheduler`.

Reusing or aliasing the existing exported `jobqueue.JobViaJSON` and
`jobqueue.JobDefaults` is preferable to inventing a parallel data model, unless
the spec finds a clear reason to add a smaller `client.JobSpec`.

## Pretend Submissions and Testing

`client.PretendSubmissions` is important to current external tests
(`ibackup`, `wrstat`, and `wrstat-ui` use it directly or indirectly). Any new
high-level methods must have sensible pretend-mode behavior:

- returning deterministic job keys from submitted pretend jobs;
- recording jobs so `SubmittedJobs()` keeps working;
- making wait/status helpers either return immediately with the recorded
  pretend jobs, or return a documented typed error if a behavior cannot be
  meaningfully simulated.

Prefer the first option where possible so downstream packages can test
submission-and-wait code without a real wr manager.

## Acceptance Criteria

- A package using `client.Scheduler` can submit one job, receive its internal
  job key, and later fetch typed status for that key without shelling out.
- A package using `client.Scheduler` can submit one or more jobs and block with
  a `context.Context` until all just-submitted jobs reach terminal state,
  receiving `*jobqueue.Job` results that expose `State`, `Exitcode`,
  `FailReason`, stdout, and stderr.
- A package can wait by previously returned key(s), not only immediately after
  submission.
- Duplicate queued jobs and previously completed jobs have explicit,
  documented behavior. Callers can implement both "return the existing queued
  job ID" and "rerun a previously completed job" without shelling out and
  without string matching errors.
- The `go-softpack-builder/wr.Runner` wrapper can be replaced by direct use of
  the new `client` API without losing its current behaviors: add with rerun
  semantics, get an ID, wait until running or already terminal, wait until
  complete/buried, and inspect typed status.
- Existing `wrstat`, `wrstat-ui`, and `ibackup` usages of `client.Scheduler`
  continue to compile unchanged.
- Pretend-submission mode covers the new methods well enough for downstream
  tests to avoid a real manager when they only need to assert what would have
  been submitted.
- New tests are added in `client/client_test.go` and, if lower-level changes
  are needed, the appropriate `jobqueue` tests. Use the existing GoConvey
  style.
- Documentation or examples make the no-shell path discoverable from the
  `client` package. A reader should not have to inspect `cmd/add.go` to learn
  how to do the equivalent of `wr add --simple` or `wr add --sync` from Go.

## Out of Scope

- Changing how wr schedules or runs jobs.
- Replacing all downstream wrappers in sibling repos as part of this feature.
  The spec can mention `go-softpack-builder` as the migration target, but the
  implementation should stay in this repo.
- Reimplementing cobra/CLI parsing in `client`.
- Removing the lower-level `jobqueue.Client` APIs.
- Breaking the existing `client.Scheduler.SubmitJobs` duplicate-error behavior.

## Reference Points

- `client/client.go`:
  - `SchedulerSettings`, `Scheduler`, `New`, `NewJob`,
    `DefaultRequirements`, `SubmitJobs`, `SubmittedJobs`,
    `FindJobsByRepGroupSuffix`, `FindJobsByRepGroupPrefixAndState`,
    `FindIncompleteJobsByRepGroup`, `GetLastCompletionTimeByRepGroup`,
    `KillJobs`, `RemoveJobs`, `Disconnect`, `UniqueString`.
  - The private `jobqueueClient` interface currently lacks
    `AddAndReturnIDs`, `AddAndWait`, `GetByEssence`, and subscription methods.
  - `pretendJobqueue` currently records submitted jobs but does not expose the
    newer wait/ID methods.
- `jobqueue/client.go`:
  - `Client.Add`, `Client.AddAndReturnIDs`, `Client.GetByEssence`,
    `Client.GetByEssences`, `GetByRepGroup*`, `GetIncomplete*`.
- `jobqueue/subscription.go`:
  - `Client.SubscribeToJobKeys`, `SubscribeToRepGroup`, `Subscription`,
    `JobUpdate`, and `Client.AddAndWait`.
- `cmd/add.go`:
  - `--simple` uses `AddAndReturnIDs`.
  - `--sync` uses `AddAndWait`.
  - `--rerun` maps to `ignoreComplete=false`.
  - `parseCmdFile` and `jobqueue.JobViaJSON.Convert` show the CLI-to-job
    conversion behavior but should not be copied wholesale into `client`.
- `jobqueue/serverREST.go`:
  - `JobViaJSON`, `JobDefaults`, and `JobViaJSON.Convert`.
- `jobqueue/mount.go`:
  - `MountConfig`, `MountTarget`, and `MountConfigs`.
- `/home/ubuntu/go-softpack-builder/wr/wr.go`:
  - External wrapper to obsolete. It shells out to `wr add` and `wr status`,
    defines its own statuses, and polls for running/completion.
- `/home/ubuntu/go-softpack-builder/build/builder.go`:
  - Calls the wrapper through a `Runner` interface and needs `Add`,
    `WaitForRunning`, `Wait`, and `Status`.
- `/home/ubuntu/ibackup/fofn/jobs.go`:
  - Example of an external package abstracting the current `client.Scheduler`
    surface behind a local interface.
- `/home/ubuntu/ibackup/server/server.go`:
  - Example of duplicate errors being ignored by string matching.
- `/home/ubuntu/wrstat/cmd/root.go` and `/home/ubuntu/wrstat/cmd/multi.go`:
  - Examples of successful current `client.Scheduler` usage that must keep
    working.
