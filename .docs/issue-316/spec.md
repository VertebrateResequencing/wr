# Future Dep-Group Dependency Specification

## Overview

`wr add --deps <group>` must wait when `<group>` has never appeared as a
`dep_grp`. Today this resolves to no live job keys, so the dependent job runs
immediately. Change only dep-group dependency semantics: command and essence
dependencies still resolve to "no dependency" when their target is absent.

A dep-group dependency is eligible only after at least one job has carried that
group and all live carriers are complete. The manager must persist which
dep-groups have ever been seen so restarts keep distinguishing "never existed"
from "existed and has no live jobs".

Never-seen waits must be diagnosable. `wr add` warns but accepts the job, and
`wr status` can display and filter jobs waiting on dep-groups not yet seen.

## Architecture

### Packages and files

- `jobqueue/db.go`: add persistent dep-group names bucket, rebuild it from
  `bucketDTK` for old databases, store group names with new jobs, and expose
  dep-group seen checks.
- `jobqueue/dependency.go`: return synthetic dependency keys for never-seen
  dep-groups and the group names causing those keys.
- `jobqueue/job.go`: add `WaitingForDepGroups []string` to `Job`; include it
  in `ToStatus`.
- `jobqueue/serverWebI.go`: add `WaitingForDepGroups []string` to `JStatus`
  without JSON tags, preserving exported field names.
- `jobqueue/server.go`: set `WaitingForDepGroups` while building and updating
  queue items; filter jobs by this field.
- `jobqueue/client.go`, `jobqueue/server.go`, `jobqueue/serverCLI.go`,
  `jobqueue/subscription.go`: add add-warning response plumbing and status
  filter request plumbing.
- `jobqueue/serverREST.go`: expose `waiting_deps=true` filter and return the
  `WaitingForDepGroups` status JSON field.
- `cmd/add.go`: print add-time warnings from new add-with-warnings APIs.
- `cmd/status.go`, `cmd/status_table.go`: add `--missing_deps` and display
  never-seen waits distinctly.
- `jobqueue/static/status.html`, `jobqueue/static/js/wr/*.js`: show the
  never-seen dep-groups in job details when present.
- `cmd/add_test.go`, `cmd/status_test.go`, `jobqueue/db_test.go`,
  `jobqueue/job_test.go`, `jobqueue/jobqueue_test.go`,
  `jobqueue/rest_test.go`, `jobqueue/serverWebI_test.go`: GoConvey coverage.
- `CHANGELOG.md`: release-note the behavior change.

### Persistence

Add:

```go
bucketDepGroups = []byte("depgroups")
```

`initDB` must create the bucket. If the bucket did not already exist, rebuild it
from every prefix in `bucketDTK`, because old databases may already have
historical dep-group lookups for completed jobs.

Internal helpers:

```go
func rebuildDepGroups(tx *bolt.Tx) error
func (db *db) depGroupEverSeen(depGroup string) (bool, error)
func (db *db) depGroupsEverSeen(depGroups []string) (map[string]bool, error)
```

Store each non-empty `Job.DepGroups` value in `bucketDepGroups` in the same
`storeNewJobs` persistence path that stores `bucketDTK`.

### Dependency Resolution

Use synthetic queue dependency keys only for dep-groups that have never been
seen:

```go
const neverSeenDepGroupDependencyPrefix = "depgroup-not-seen:"

func neverSeenDepGroupDependencyKey(depGroup string) string
func neverSeenDepGroupFromDependencyKey(key string) (string, bool)
```

Change internal dependency helpers to return both queue keys and never-seen
groups:

```go
func (d Dependencies) incompleteJobKeys(db *db) ([]string, []string, error)
func (d *Dependency) incompleteJobKeys(db *db) ([]string, []string, error)
```

Dep-group resolution:

- If live carrier keys exist, return those keys and no waiting groups.
- If no live keys exist and the group has been seen, return no keys and no
  waiting groups.
- If no live keys exist and the group has never been seen, return one synthetic
  key and that group in `WaitingForDepGroups`.

Essence dependency resolution is unchanged: missing or complete essence targets
return no keys and no waiting groups.

When a real carrier for a never-seen group is added later, existing reverse
dep-group lookup logic must return affected dependent jobs. Recomputing their
dependencies removes the synthetic key and replaces it with live carrier keys.

### Public API

Keep existing `Client.Add`, `Client.AddAndReturnIDs`, and `Client.AddAndWait`
signatures and behavior. Add:

```go
type AddWarnings struct {
    NeverSeenDepGroups []string
}

func (c *Client) AddWithWarnings(
    jobs []*Job,
    envVars []string,
    ignoreComplete bool,
) (added int, existed int, warnings AddWarnings, err error)

func (c *Client) AddAndReturnIDsWithWarnings(
    jobs []*Job,
    envVars []string,
    ignoreComplete bool,
) (ids []string, warnings AddWarnings, err error)

func (c *Client) AddAndWaitWithWarnings(
    ctx context.Context,
    jobs []*Job,
    envVars []string,
    ignoreComplete bool,
) (jobsDone []*Job, warnings AddWarnings, err error)

func (c *Client) GetIncompleteWaitingForDepGroups(
    repgroup string,
    match RepGroupMatch,
    limit int,
    getStd bool,
    getEnv bool,
) ([]*Job, error)
```

Extend the existing unexported `serverResponse` and `clientRequest` with:

```go
AddWarnings         AddWarnings
WaitingForDepGroups bool
```

`AddWarnings.NeverSeenDepGroups` is de-duplicated and sorted.

`wr add --sync` must obtain warnings from the add/ID step, print them to
stderr immediately after that step succeeds, then wait for the returned job ID.
It must use `AddAndReturnIDsWithWarnings` plus existing subscription APIs, not
an API that exposes warnings only after terminal-state waiting completes.

### Status And Diagnostics

Add `Job.WaitingForDepGroups []string` and
`JStatus.WaitingForDepGroups []string`. Do not add JSON tags to `JStatus`.
Status JSON keeps existing exported field names such as `State` and
`DepGroups`; the new field serializes as `WaitingForDepGroups`.

`wr status --missing_deps` selects current jobs whose
`WaitingForDepGroups` is non-empty. It is valid in default, `--all`, exact
`--identifier`, and `--identifier --search` modes. It has the same validation
errors as state filters for `--file`, `--cmdline`, and
`--identifier --internal`.

Display rules:

- Details output uses:
  `Status: waiting on dep group(s) not yet seen: <groups>`
- Table and plain output show status text `waiting-deps` for these jobs.
- Counts and summary still count these jobs as `dependent`.
- JSON output keeps `"State":"dependent"` and includes
  `"WaitingForDepGroups"`.
- Web status details show `Waiting for dep groups not yet seen` plus the group
  list when the field is non-empty.

`wr add` warning text is one line made from these fragments with one space
after the semicolon:

```text
dependency group "<group>" has not been seen;
dependent job(s) will wait until it appears
```

Emit one warning per unique group returned by `AddWarnings`.

## A. Dependency Semantics

### A1: Wait for never-seen dep-groups

As a workflow author, I want `--deps future` to wait when no job has carried
`future`, so that out-of-order pipeline submission is safe.

**Package:** `jobqueue/`
**File:** `jobqueue/dependency.go`, `jobqueue/server.go`
**Test file:** `jobqueue/jobqueue_test.go`

**Acceptance tests:**

1. Given an empty manager, when a job with
   `Dependencies{NewDepGroupDependency("future")}` is added, then `Add`
   returns `inserts == 1`, `already == 0`, `GetByRepGroup` returns one job
   with `State == JobStateDependent`, `WaitingForDepGroups == []string{
   "future"}`, and `Reserve(50*time.Millisecond)` returns nil.
2. Given the dependent from test 1, when a carrier job with
   `DepGroups: []string{"future"}` is added, then the dependent remains
   `JobStateDependent`, `WaitingForDepGroups` becomes nil, and only the carrier
   can be reserved.
3. Given the carrier from test 2 is executed successfully, when the dependent is
   fetched, then its state is `JobStateReady` and a subsequent reserve returns
   that dependent job.

### A2: Seen completed groups do not block

As a workflow author, I want a dep-group that completed earlier to count as
seen, so that later dependents can run immediately.

**Package:** `jobqueue/`
**File:** `jobqueue/db.go`, `jobqueue/dependency.go`
**Test file:** `jobqueue/jobqueue_test.go`, `jobqueue/db_test.go`

**Acceptance tests:**

1. Given a job with `DepGroups: []string{"done"}` has completed, when a new job
   depending on `done` is added, then it starts in `JobStateReady` and
   `WaitingForDepGroups` is nil.
2. Given the manager from test 1 is stopped and restarted using the same db
   file without wiping it, when another job depending on `done` is added, then
   it starts in `JobStateReady`.
3. Given a test db with only `bucketDTK` entries for `legacy` and no
   `bucketDepGroups`, when `initDB` opens it, then `depGroupEverSeen("legacy")`
   returns true and `depGroupEverSeen("absent")` returns false.

### A3: Same-batch and live reblocking stay unchanged

As a workflow author, I want existing live dep-group behavior preserved, so
that current workflows keep their ordering.

**Package:** `jobqueue/`
**File:** `jobqueue/db.go`, `jobqueue/server.go`
**Test file:** `jobqueue/jobqueue_test.go`

**Acceptance tests:**

1. Given one add request contains a dependent on `batch` and a carrier with
   `DepGroups: []string{"batch"}`, when the batch is added, then the carrier is
   reservable first, the dependent has `State == JobStateDependent`, and
   `WaitingForDepGroups` is nil.
2. Given all jobs in dep-group `live` completed and a dependent on `live` is
   ready, when a new carrier with `DepGroups: []string{"live"}` is added, then
   the dependent moves back to `JobStateDependent` until the new carrier
   completes.
3. Given a chain where adding a new carrier resurrects a completed dependent,
   when the existing live-dependency regression scenario runs, then inserted,
   ready, dependent, and complete counts match the current test expectations.

### A4: Command dependencies stay unchanged

As an existing user of `--cmd_deps`, I want absent command targets to keep
today's behavior, so that this change affects dep-groups only.

**Package:** `jobqueue/`, `cmd/`
**File:** `jobqueue/dependency.go`, `cmd/add.go`
**Test file:** `jobqueue/jobqueue_test.go`, `cmd/add_test.go`

**Acceptance tests:**

1. Given an empty manager, when a job depends on
   `NewEssenceDependency("echo missing", "")`, then the job starts in
   `JobStateReady`, `WaitingForDepGroups` is nil, and it is reservable.
2. Given an empty manager, when `wr add --cmd_deps "echo missing," --rep_grp
   cmd-missing` adds command `echo actual`, then the add succeeds, stderr does
   not contain `has not been seen`, `GetByRepGroup("cmd-missing")` returns one
   job with `State == JobStateReady` and empty `WaitingForDepGroups`, and
   `Reserve(50*time.Millisecond)` returns that job.
3. Given an add request contains a job with `--cmd_deps "echo later,"` and a
   second job with command `echo later`, when the batch is added, then the
   existing command-dependency behavior is unchanged from the current
   "non-existent dependencies" test.

## B. Add-Time Warnings

### B1: Return warnings from add APIs

As a CLI caller, I want `wr add` to warn about never-seen dep-groups while
still accepting the job.

**Package:** `jobqueue/`, `cmd/`
**File:** `jobqueue/client.go`, `jobqueue/serverCLI.go`, `cmd/add.go`
**Test file:** `jobqueue/jobqueue_test.go`, `cmd/add_test.go`

Use the public API in Architecture.

**Acceptance tests:**

1. Given an empty manager, when `Client.AddWithWarnings` adds one job depending
   on dep-group `future`, then it returns `added == 1`, `existed == 0`,
   `warnings.NeverSeenDepGroups == []string{"future"}`, and nil error.
2. Given the same setup, when `wr add --deps future` adds a command, then
   stderr contains exactly one line containing
   `dependency group "future" has not been seen; dependent job(s) will wait
   until it appears`, stdout contains the normal add summary, and the command
   is accepted.
3. Given one request has two jobs depending on `future`, when the request is
   added, then `warnings.NeverSeenDepGroups == []string{"future"}` with no
   duplicate group names.
4. Given `wr add --simple --deps future` succeeds, when ids are printed to
   stdout, then the warning is printed to stderr and no warning text appears in
   stdout.
5. Given `wr add --sync --deps future` uses a test client whose
   `AddAndReturnIDsWithWarnings` returns ID `job1` plus warning `future` and
   whose terminal wait blocks, when sync add starts, then stderr receives
   exactly one never-seen warning before the wait is released, and stdout has no
   completed-job output at that point.

### B2: Suppress warnings for seen or same-batch groups

As a workflow author, I want warnings only for real never-seen waits, so that
valid batches stay quiet.

**Package:** `jobqueue/`, `cmd/`
**File:** `jobqueue/server.go`, `cmd/add.go`
**Test file:** `jobqueue/jobqueue_test.go`, `cmd/add_test.go`

**Acceptance tests:**

1. Given dep-group `done` has completed in the past, when a job depending on
   `done` is added, then `warnings.NeverSeenDepGroups` is empty and `wr add`
   prints no never-seen warning.
2. Given one add request contains a dependent on `batch` and a carrier with
   `DepGroups: []string{"batch"}`, when it is added, then warnings are empty.

## C. Status Diagnostics

### C1: Expose never-seen waits in status data

As a UI or API consumer, I want status payloads to name missing dep-groups, so
that I can diagnose a blocked job programmatically.

**Package:** `jobqueue/`
**File:** `jobqueue/job.go`, `jobqueue/serverREST.go`,
`jobqueue/serverWebI.go`
**Test file:** `jobqueue/job_test.go`, `jobqueue/rest_test.go`,
`jobqueue/serverWebI_test.go`

**Acceptance tests:**

1. Given a dependent job has `DepGroups == []string{"carrier"}` and
   `WaitingForDepGroups == []string{"future"}`, when `ToStatus` is called,
   then `JStatus.State == JobStateDependent`,
   `JStatus.DepGroups == []string{"carrier"}`, and
   `JStatus.WaitingForDepGroups == []string{"future"}`.
2. Given a REST status request returns that job, when the response JSON is
   decoded as a raw job object, then it has `"State":"dependent"`,
   `"DepGroups":["carrier"]`, and `"WaitingForDepGroups":["future"]`, and has
   no `state`, `dep_groups`, or `waiting_for_dep_groups` keys.
3. Given `/rest/v1/jobs?waiting_deps=true` is requested, when one dependent
   job waits on `future` and one dependent job waits on a live carrier, then
   the response contains only the job waiting on `future`.
4. Given the web status details panel receives a `JStatus` with
   `WaitingForDepGroups: []string{"future"}`, when it renders the job details,
   then the visible details include `Waiting for dep groups not yet seen` and
   `future`.

### C2: Display never-seen waits in CLI status

As a command-line user, I want normal status output to explain why a job is
dependent, so that a typo in `--deps` is visible.

**Package:** `cmd/`
**File:** `cmd/status.go`, `cmd/status_table.go`
**Test file:** `cmd/status_test.go`, `cmd/status_table_test.go`

**Acceptance tests:**

1. Given one job waits on never-seen dep-group `future`, when
   `wr status --identifier <rg> --output details` is run, then output contains
   `Status: waiting on dep group(s) not yet seen: future`.
2. Given the same job, when `wr status --identifier <rg> --output table` is
   run, then the Status column for that row is `waiting-deps`.
3. Given the same job, when `wr status --identifier <rg> --output plain` is
   run, then the row is `<job-key>\twaiting-deps`.
4. Given the same job has `DepGroups == []string{"carrier"}`, when
   `wr status --identifier <rg> --output json` is run, then the decoded job
   object has `"State":"dependent"`, `"DepGroups":["carrier"]`, and
   `"WaitingForDepGroups":["future"]`, and has no `state`, `dep_groups`, or
   `waiting_for_dep_groups` keys.
5. Given the same job, when `wr status --identifier <rg> --output counts` is
   run, then output includes `dependent: 1`.

### C3: Filter jobs waiting on never-seen dep-groups

As an operator, I want a selector for never-seen waits, so that I can find jobs
to fix with `wr mod` or remove.

**Package:** `cmd/`, `jobqueue/`
**File:** `cmd/status.go`, `jobqueue/client.go`, `jobqueue/server.go`
**Test file:** `cmd/status_test.go`, `jobqueue/jobqueue_test.go`

Use:

```go
func (c *Client) GetIncompleteWaitingForDepGroups(
    repgroup string,
    match RepGroupMatch,
    limit int,
    getStd bool,
    getEnv bool,
) ([]*Job, error)
```

**Acceptance tests:**

1. Given one job waits on never-seen dep-group `future`, one job is dependent
   on a live carrier, and one job is ready, when `wr status --missing_deps
   --output counts` is run, then output has `dependent: 1`, `ready: 0`, and
   `buried: 0`.
2. Given the same jobs in report group `rg-a`, when `wr status --identifier
   rg-a --missing_deps --output plain` is run, then output contains only the
   never-seen-waiting job key with `waiting-deps`.
3. Given the same jobs and report group search term `rg-`, when `wr status
   --identifier rg- --search --missing_deps --output counts` is run, then only
   matching never-seen waits are counted.
4. Given `--missing_deps` is used with `--file`, `--cmdline`, or
   `--identifier --internal`, when validation runs, then the command exits with
   the same class of validation error used for `--dependent`.

## D. Documentation

### D1: Document the behavior change

As a user upgrading wr, I want help and release notes to call out the new
semantics, so that typos in `--deps` are understood.

**Package:** `cmd/`
**File:** `cmd/add.go`, `cmd/status.go`, `CHANGELOG.md`
**Test file:** `cmd/add_test.go`, `cmd/status_test.go`

**Acceptance tests:**

1. Given `wr add -h` output is captured, then the `deps` help says dep-group
   dependencies wait even when the dep-group has not appeared yet, and that
   command dependencies from `cmd_deps` keep static behavior.
2. Given `wr status -h` output is captured, then it documents
   `--missing_deps` as showing jobs waiting on dep-groups not yet seen.
3. Given `CHANGELOG.md` is read, then the newest release section includes a
   Changed bullet saying `wr add --deps` now waits for never-seen dep-groups
   and warns that typos can block indefinitely.

## Implementation Order

1. Persistence and dependency resolution: add dep-group seen storage, migration,
   synthetic dependency keys, `WaitingForDepGroups`, and jobqueue regression
   tests for A1-A4.
2. Add warning plumbing: add `AddWarnings`, add-with-warnings client methods,
   server response support, and CLI warning output for B1-B2.
3. Status diagnostics: add status field output, REST/web exposure,
   `--missing_deps`, server filtering, and C1-C3 tests.
4. Documentation and release note: update help text and `CHANGELOG.md` for D1;
   run focused GoConvey tests, then package-wide `go test` where practical.

Phases are sequential because status filtering depends on persisted
`WaitingForDepGroups`. Within phase 3, CLI, REST, and web display tests can be
implemented in parallel after the server filter exists.

## Appendix: Key Decisions

- Keep `JobStateDependent` as the real state. `waiting-deps` is CLI display
  text only, so existing state filters, counts, REST state values, and LSF
  mapping remain compatible.
- Preserve the existing status JSON contract: `JStatus` serializes exported Go
  field names such as `State` and `DepGroups`. Add `WaitingForDepGroups`
  consistently instead of adding snake_case JSON tags.
- Use a dedicated `bucketDepGroups` even though `bucketDTK` is historical.
  Direct membership checks are clearer, faster, and rebuildable from old DBs.
- Use synthetic dependency keys instead of a new queue state. This reuses
  existing dependent queue behavior and lets current reverse dep-group
  re-evaluation remove the synthetic key when the first carrier appears.
- New add-with-warnings methods avoid breaking existing Go callers. Existing
  `Add`, `AddAndReturnIDs`, and `AddAndWait` ignore warnings exactly as older
  callers expect.
- `wr add --sync` prints warnings from `AddAndReturnIDsWithWarnings` before
  waiting. Returning them only after `AddAndWaitWithWarnings` completes is too
  late for an indefinitely blocked never-seen dep-group.
- Tests use GoConvey per project convention. Every acceptance test above maps
  to a concrete `Convey` block; no test should rely on sleeps where existing
  polling helpers can wait for server state.
