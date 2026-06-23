# REST and Web Job Modification Specification

## Overview

Add job editing to the public REST API and status web UI. The feature exposes
the existing `wr mod` capability for incomplete, non-running jobs without
changing the token/auth model.

REST uses `PATCH /rest/v1/jobs/<keys-or-repgroups>` with optional JSON fields.
Omitted fields stay unchanged. Supplied fields are validated and applied with
`JobModifier` semantics. The response returns new-to-old key mapping data and
fresh job status rows so callers can follow jobs whose key changed.

The web UI edits one selected job at a time. It shows a Modify action only for
`delayed`, `ready`, `dependent`, and `buried` jobs. Successful edits replace
the displayed row without a manual refresh. Invalid edits show an error popup.
`reserved`, `running`, `lost`, `complete`, deleted, and unknown jobs are not
editable in v1.

## Architecture

### Packages and files

- `jobqueue/serverREST.go`: add `PATCH` handling to `restJobs`; add
  `JobModifyViaJSON`, `JobModifyResponse`, body validation, conversion to
  `JobModifier`, editable-state checks, and modified-job lookup.
  Test in `jobqueue/rest_test.go`.
- `jobqueue/job.go`: reuse `JobModifier`. Add no new mutation path unless an
  unexported helper is needed to set env overrides from `[]string` without the
  comma-separated CLI format.
- `jobqueue/serverWebI.go`: extend `JStatus` and `Job.ToStatus()` with the
  editable fields the UI needs but cannot currently read: `ReqGroup`,
  `CwdMatters`, `Override`, `Priority`, `Retries`,
  `NoRetryOverWalltime`, and job-specific `EnvOverrides`.
  Test in `jobqueue/serverWebI_test.go`.
- `jobqueue/static/status.html`: add Modify button, modal form, validation
  error display, and refreshed row rendering.
- `jobqueue/static/js/wr/action-handlers.js`,
  `jobqueue/static/js/wr/modal-handlers.js`,
  `jobqueue/static/js/wr/status-viewmodel.js`: add modify modal state, payload
  creation, `PATCH` submission, error handling, and row/key replacement.
  A new focused JS module under `jobqueue/static/js/wr/` is allowed if it keeps
  the existing module style simpler.

### REST API

Add this public request type in package `jobqueue`:

```go
type JobModifyViaJSON struct {
    MountConfigs         *MountConfigs      `json:"mounts,omitempty"`
    LimitGrps            *[]string          `json:"limit_grps,omitempty"`
    Modules              *[]string          `json:"modules,omitempty"`
    Deps                 *[]string          `json:"deps,omitempty"`
    CmdDeps              *Dependencies      `json:"cmd_deps,omitempty"`
    OnFailure            *BehavioursViaJSON `json:"on_failure,omitempty"`
    OnSuccess            *BehavioursViaJSON `json:"on_success,omitempty"`
    OnExit               *BehavioursViaJSON `json:"on_exit,omitempty"`
    Env                  *[]string          `json:"env,omitempty"`
    Other                *map[string]string `json:"other,omitempty"`
    Cmd                  *string            `json:"cmd,omitempty"`
    Cwd                  *string            `json:"cwd,omitempty"`
    ReqGrp               *string            `json:"req_grp,omitempty"`
    Group                *string            `json:"group,omitempty"`
    Memory               *string            `json:"memory,omitempty"`
    Time                 *string            `json:"time,omitempty"`
    MonitorDocker        *string            `json:"monitor_docker,omitempty"`
    WithDocker           *string            `json:"with_docker,omitempty"`
    WithSingularity      *string            `json:"with_singularity,omitempty"`
    ContainerMounts      *string            `json:"container_mounts,omitempty"`
    SchedulerQueue       *string            `json:"queue,omitempty"`
    SchedulerQueuesAvoid *string            `json:"queues_avoid,omitempty"`
    SchedulerMisc        *string            `json:"misc,omitempty"`
    CloudOS              *string            `json:"cloud_os,omitempty"`
    CloudUser            *string            `json:"cloud_username,omitempty"`
    CloudRAM             *int               `json:"cloud_ram,omitempty"`
    CloudFlavor          *string            `json:"cloud_flavor,omitempty"`
    CloudScript          *string            `json:"cloud_script,omitempty"`
    CloudConfigFiles     *string            `json:"cloud_config_files,omitempty"`
    CloudShared          *bool              `json:"cloud_shared,omitempty"`
    CPUs                 *float64           `json:"cpus,omitempty"`
    Disk                 *int               `json:"disk,omitempty"`
    Override             *int               `json:"override,omitempty"`
    Priority             *int               `json:"priority,omitempty"`
    Retries              *int               `json:"retries,omitempty"`
    NoRetryOverWalltime  *string            `json:"no_retry_over_walltime,omitempty"`
    CwdMatters           *bool              `json:"cwd_matters,omitempty"`
    ChangeHome           *bool              `json:"change_home,omitempty"`
}
```

```go
type JobModifyResponse struct {
    // Modified maps new internal job key to old internal job key.
    Modified map[string]string `json:"modified"`
    Jobs     []JStatus         `json:"jobs"`
}
```

Field rules:

- Omitted field: no change.
- `cmd` and `cwd`: if supplied, must be non-empty. `cmd` may target only one
  editable job.
- `env`: modifies only job-specific env overrides. `[]` clears overrides. It
  must not replace inherited environment values.
- `deps` are dep-group dependencies. `cmd_deps` use the documented command
  dependency JSON shape `{"cmd":"...","cwd":"..."}`. Supplying either replaces
  the full `Job.Dependencies` value with the supplied groups and commands.
- `memory`: parse with `bytefmt.ToMegabytes`; `time` and
  `no_retry_over_walltime`: parse with `time.ParseDuration`.
- `cpus` and `disk`: set `CoresSet` and `DiskSet`; zero is valid.
- `other`: replaces `Requirements.Other`; `{}` clears it. Named cloud and
  scheduler fields merge into `other` using existing `wr mod` keys:
  `cloud_os`, `cloud_user`, `cloud_os_ram`, `cloud_flavor`,
  `cloud_script`, `cloud_config_files`, `cloud_shared`,
  `scheduler_queue`, `scheduler_queues_avoid`, and `scheduler_misc`.
- `override` must be `0..2`; `priority` and `retries` must be `0..255`.
- `on_failure`, `on_success`, and `on_exit`: supplied empty arrays become
  `[]BehaviourViaJSON{{Nothing: true}}` for that trigger, matching
  `wr mod --on_* ""`.
- Immutable in v1: `rep_grp`, `dep_grps`, `bsub_mode`, internal key, state,
  stdout/stderr, host/PID, start/end time, attempts, exit code, failure reason,
  peak resource use, actual cwd, queue name, and bsub id.

Endpoint rules:

- Authorized like existing REST endpoints: token query parameter or
  `Authorization: Bearer <token>`.
- `PATCH /rest/v1/jobs/<id>` accepts one 32-char job key or one RepGroup.
  Comma-separated ids use existing GET path semantics.
- Empty path returns `400 Bad Request` with `job identifier is required`.
- Editable states are exactly `delayed`, `ready`, `dependent`, and `buried`.
  A target that resolves only to other states returns `409 Conflict` with
  `no editable jobs matched`.
- A target that resolves to no queued or complete job and no RepGroup returns
  `404 Not Found` with `job not found`.
- Duplicate-key edits, such as changing a command to match another live or
  complete job, return `409 Conflict` with `no jobs were modified`.
- Success returns `200 OK` and `JobModifyResponse`. `Jobs` contains one fresh
  `JStatus` for each modified job, sorted by new key for stable JSON.

## Section A: REST Job Modification

### A1: Modify one editable job by key

As a REST client, I want to patch a queued job by key, so that I can make the
same safe edits as `wr mod` without shelling out.

**Package:** `jobqueue/`
**File:** `jobqueue/serverREST.go`
**Test file:** `jobqueue/rest_test.go`

Use:

```http
PATCH /rest/v1/jobs/<job-key>
Authorization: Bearer <token>
Content-Type: application/json
```

**Acceptance tests:**

1. Given a ready job with command `echo rest old`, cwd `/tmp`, req group
   `rest-old`, RAM `50M`, time `1m`, cores `1`, disk `0`, priority `1`,
   retries `1`, override `0`, limit groups `["old:1"]`, and env override
   `["REST_MOD=old"]`, when `PATCH /rest/v1/jobs/<oldKey>` is sent with:

   ```json
   {
     "cmd": "echo rest new",
     "cwd": "/tmp/rest-new",
     "cwd_matters": true,
     "change_home": true,
     "req_grp": "rest-new",
     "memory": "64M",
     "time": "2m",
     "cpus": 0.5,
     "disk": 2,
     "priority": 7,
     "retries": 4,
     "override": 2,
     "limit_grps": ["new:2"],
     "modules": ["module-a"],
     "env": ["REST_MOD=new", "REST_EXTRA=1"],
     "other": {"scheduler_queue": "short"},
     "on_exit": [{"nothing": true}]
   }
   ```

   then the response is `200 OK`, `Modified` has length `1`, the sole map
   value is `<oldKey>`, the sole map key equals `Jobs[0].Key`, and that key is
   not `<oldKey>`.
2. Given the response from test 1, when `GET /rest/v1/jobs/<newKey>?env=true`
   is sent, then it returns one job with `Cmd == "echo rest new"`,
   `CwdBase == "/tmp/rest-new"`, `ReqGroup == "rest-new"`,
   `CwdMatters == true`, `HomeChanged == true`, `ExpectedRAM == 64`,
   `ExpectedTime == 120`, `Cores == 0.5`, `RequestedDisk == 2`,
   `Priority == 7`, `Retries == 4`, `Override == 2`,
   `LimitGroups == []string{"new:2"}`, `Modules == []string{"module-a"}`,
   `Env` contains `REST_MOD=new` and `REST_EXTRA=1`, `OtherRequests` contains
   `scheduler_queue:short`, and `Behaviours` equals
   `{"on_exit":[{"nothing":true}]}`.
3. Given the response from test 1, when `GET /rest/v1/jobs/<oldKey>` is sent,
   then the decoded status slice has length `0`.
4. Given a ready job with priority `1`, when
   `PATCH /rest/v1/jobs/<key>?token=<token>` supplies `{"priority": 9}`
   without an `Authorization` header, then the response is `200 OK`, the
   returned job has `Priority == 9`, and a later `GET` shows priority `9`.
5. Given the same endpoint without a token or bearer header, when `PATCH` is
   sent, then the response is `401 Unauthorized`.

### A2: Validate edits and editable states

As a REST client, I want invalid or unsafe edits rejected clearly, so that I do
not mistake a no-op for success.

**Package:** `jobqueue/`
**File:** `jobqueue/serverREST.go`
**Test file:** `jobqueue/rest_test.go`

**Acceptance tests:**

1. Given a delayed job with priority `1`, when `PATCH` supplies
   `{"priority": 9}`, then the response is `200 OK`, the returned job has
   `State == "delayed"` and `Priority == 9`, and the stored job is still
   delayed.
2. Given a dependent job with req group `dep-old`, when `PATCH` supplies
   `{"req_grp": "dep-new"}`, then the response is `200 OK`, the returned job
   has `State == "dependent"` and `ReqGroup == "dep-new"`, and the stored job
   is still dependent.
3. Given a buried job with retries `1`, when `PATCH` supplies
   `{"retries": 3}`, then the response is `200 OK`, the returned job has
   `State == "buried"` and `Retries == 3`, and the stored job is still buried.
4. Given a ready job, when `PATCH` supplies `{"priority": 256}`, then the
   response is `400 Bad Request`, the body contains
   `priority value (256) is not in the range 0..255`, and the stored priority
   is unchanged.
5. Given a ready job, when `PATCH` supplies `{"cmd": ""}`, then the response is
   `400 Bad Request`, the body is `cmd cannot be empty\n`, and the stored
   command is unchanged.
6. Given a running job, when `PATCH` supplies `{"priority": 9}`, then the
   response is `409 Conflict`, the body is `no editable jobs matched\n`, and
   the stored priority is unchanged.
7. Given a complete job, when `PATCH` supplies `{"retries": 9}`, then the
   response is `409 Conflict`, the body is `no editable jobs matched\n`, and
   no ready job is created.
8. Given a reserved job, when `PATCH` supplies `{"priority": 9}`, then the
   response is `409 Conflict`, the body is `no editable jobs matched\n`, and
   the stored priority is unchanged.
9. Given a lost job, when `PATCH` supplies `{"priority": 9}`, then the
   response is `409 Conflict`, the body is `no editable jobs matched\n`, and
   the stored priority is unchanged.
10. Given no job or RepGroup matches
    `0123456789abcdef0123456789abcdef`, when `PATCH` supplies
    `{"priority": 9}`, then the response is `404 Not Found`, the body is
    `job not found\n`, and no job is created.
11. Given a ready job with env override `REST_CLEAR=1`, when `PATCH` supplies
    `{"env": []}`, then the response is `200 OK`; a later
    `GET /rest/v1/jobs/<key>?env=true` returns one job whose `Env` does not
    contain `REST_CLEAR=1`.
12. Given two ready jobs in the same manager, when job A is patched with
    `{"cmd": "<job B command>"}`, then the response is `409 Conflict`, the body
    is `no jobs were modified\n`, job A keeps its old command, and job B is
    unchanged.

### A3: Modify multiple jobs without changing identity fields

As a REST client, I want to patch several matching editable jobs, so that I can
adjust common mutable settings for a RepGroup.

**Package:** `jobqueue/`
**File:** `jobqueue/serverREST.go`
**Test file:** `jobqueue/rest_test.go`

**Acceptance tests:**

1. Given three editable jobs in RepGroup `rest-bulk` and one running job in the
   same RepGroup, when `PATCH /rest/v1/jobs/rest-bulk` supplies
   `{"priority": 8, "limit_grps": ["bulk:1"]}`, then the response is
   `200 OK`, `Modified` has length `3`, `Jobs` has length `3`, every returned
   job has `Priority == 8` and `LimitGroups == []string{"bulk:1"}`, and the
   running job's priority and limit groups are unchanged.
2. Given two editable jobs in RepGroup `rest-bulk-cmd`, when
   `PATCH /rest/v1/jobs/rest-bulk-cmd` supplies `{"cmd": "echo same"}`, then
   the response is `400 Bad Request`, the body is
   `cmd can only be modified for one job\n`, and both commands are unchanged.

## Section B: Web UI Modification

### B1: Expose editable status fields to the UI

As a web user, I want the modify dialog pre-filled from the selected job, so
that I can review current values before editing them.

**Package:** `jobqueue/`
**File:** `jobqueue/serverWebI.go`
**Test file:** `jobqueue/serverWebI_test.go`

Extend `JStatus`:

```go
type JStatus struct {
    // existing fields unchanged
    ReqGroup            string
    EnvOverrides        []string
    Override            uint8
    Priority            uint8
    Retries             uint8
    NoRetryOverWalltime float64
    CwdMatters          bool
}
```

**Acceptance tests:**

1. Given a ready job with `ReqGroup == "web-req"`, `Override == 2`,
   `Priority == 11`, `Retries == 5`,
   `NoRetryOverWalltime == 3*time.Minute`, `CwdMatters == true`, and
   `ChangeHome == true`, with env override `WEB_ONLY=old`, when a websocket
   details request returns its `JStatus`, then the JSON has
   `ReqGroup == "web-req"`, `Override == 2`, `Priority == 11`,
   `Retries == 5`, `NoRetryOverWalltime == 180`, `CwdMatters == true`,
   `HomeChanged == true`, and `EnvOverrides == []string{"WEB_ONLY=old"}`.
2. Given the same job is fetched through `GET /rest/v1/jobs/<key>`, then the
   decoded `JStatus` has the same values for `ReqGroup`, `EnvOverrides`,
   `Override`, `Priority`, `Retries`, `NoRetryOverWalltime`, and
   `CwdMatters` as test 1.

### B2: Edit one selected job from the details panel

As a web user, I want a Modify action on editable jobs, so that I can change a
single job from the status page.

**Package:** `jobqueue/`
**File:** `jobqueue/static/status.html`
**Test file:** `jobqueue/serverWebI_test.go`

**Acceptance tests:**

1. Given the details panel contains one `ready` job with command
   `echo web old`, cwd `/tmp/web-old`, `CwdMatters == false`,
   `HomeChanged == false`, `ReqGroup == "web-old"`, RAM `64M`, time `1m`,
   cores `1`, disk `0`, priority `1`, retries `1`, override `0`,
   `NoRetryOverWalltime == 0`, limit groups `["old:1"]`, modules
   `["oldmod"]`, dependencies `["old-dep", "echo dep [/tmp/dep-old]"]`,
   behaviours `{"on_exit":[{"nothing":true}]}`, other requests
   `["scheduler_queue:old"]`, mounts
   `[{"Mount":"oldmnt","Targets":[{"Profile":"old","Path":"old/data"}]}]`,
   `MonitorDocker == "old-docker"`, `WithDocker == ""`,
   `WithSingularity == "old.sif"`, and `ContainerMounts == "/old:/old"`,
   when the user opens Modify, then modal controls for every listed mutable
   field are pre-filled with exactly those values. Env is covered by B3.
2. Given the modal from test 1, when the user changes every listed field and
   submits, then the browser sends exactly one
   `PATCH /rest/v1/jobs/<oldKey>` with bearer token auth and a body that
   decodes to exactly:

   ```json
   {
     "cmd": "echo web new",
     "cwd": "/tmp/web-new",
     "cwd_matters": true,
     "change_home": true,
     "req_grp": "web-new",
     "memory": "128M",
     "time": "3m",
     "cpus": 2,
     "disk": 5,
     "priority": 12,
     "retries": 6,
     "override": 2,
     "no_retry_over_walltime": "10m",
     "limit_grps": ["new:2"],
     "modules": ["mod-a", "mod-b"],
     "deps": ["dep-a"],
     "cmd_deps": [{"cmd": "echo dep", "cwd": "/tmp/dep"}],
     "on_failure": [{"cleanup": true}],
     "on_success": [{"remove": true}],
     "on_exit": [{"nothing": true}],
     "other": {"cloud_os": "Ubuntu 22", "scheduler_queue": "short"},
     "mounts": [{
       "Mount": "mnt",
       "Targets": [{"Profile": "p", "Path": "bucket/data"}]
     }],
     "monitor_docker": "dock-new",
     "with_docker": "ubuntu:22.04",
     "with_singularity": "",
     "container_mounts": "/data:/data"
   }
   ```

3. Given the `PATCH` in test 2 returns `200 OK` with `Modified` mapping
   `<newKey>` to `<oldKey>` and one `JStatus` matching the new values, then the
   modal closes, the details observable replaces the old row with the returned
   row, no row with `<oldKey>` remains, and the visible row shows every new
   value from test 2, including cwd flags, req group, time, disk, limit groups,
   modules, dependencies, behaviours, other requests, mounts, container fields,
   monitor/with container values, override, and no-retry-over-walltime.
4. Given a job state is `delayed`, `ready`, `dependent`, or `buried`, when the
   row is rendered, then the Modify action is available.
5. Given a job state is `reserved`, `running`, `lost`, or `complete`, when the
   row is rendered, then the Modify action is unavailable.

### B3: Edit env overrides only

As a web user, I want env editing to affect only job-specific overrides, so
that inherited manager environment values are not accidentally submitted.

**Package:** `jobqueue/`
**File:** `jobqueue/static/js/wr/modal-handlers.js`
**Test file:** `jobqueue/serverWebI_test.go`

**Acceptance tests:**

1. Given a job status has effective `Env` containing `PATH=/bin`,
   `INHERITED=base`, and `WEB_ONLY=old`, and has
   `EnvOverrides == []string{"WEB_ONLY=old"}`, when the user opens Modify,
   then the env override editor is pre-filled with exactly `WEB_ONLY=old` and
   does not include `PATH=/bin` or `INHERITED=base`.
2. Given the env editor from test 1, when the user changes the row to
   `WEB_ONLY=new` and submits, then the `PATCH` body contains
   `{"env": ["WEB_ONLY=new"]}` and does not contain `PATH=/bin` or
   `INHERITED=base`; after success the visible effective env still contains
   `PATH=/bin` and `INHERITED=base`, and contains `WEB_ONLY=new`.
3. Given the env editor from test 1, when the user clears all override rows and
   submits, then the `PATCH` body contains `{"env": []}`; after success
   `EnvOverrides` is empty, effective env still contains `PATH=/bin` and
   `INHERITED=base`, and effective env does not contain `WEB_ONLY=old`.

### B4: Report web edit failures

As a web user, I want failed edits shown in the UI, so that I know the job was
not changed.

**Package:** `jobqueue/`
**File:** `jobqueue/static/js/wr/modal-handlers.js`
**Test file:** `jobqueue/serverWebI_test.go`

**Acceptance tests:**

1. Given the Modify dialog is open for a ready job, when the server returns
   `400 Bad Request` with body
   `priority value (300) is not in the range 0..255\n`, then the dialog remains
   open, an error popup displays that exact text without the trailing newline,
   and the details row is unchanged.
2. Given the Modify dialog is open for a job that becomes running before
   submit, when the server returns `409 Conflict` with body
   `no editable jobs matched\n`, then the dialog remains open, an error popup
   displays `no editable jobs matched`, and the details row is unchanged.

## Implementation Order

1. Status and schema foundations (B1, A2): extend `JStatus`/`ToStatus()`, add
   `JobModifyViaJSON`, validation, conversion to `JobModifier`, and bad-value
   coverage.
2. REST endpoint (A1, A3): wire `PATCH`, editable-state filtering, conflict
   handling, modified job refetch, key mapping, and REST integration tests.
3. Web UI (B2, B3, B4): add Modify action/modal, payload creation, `PATCH`
   submission, success row replacement, and error popup behavior. Depends on
   items 1 and 2.

## Appendix: Key Decisions

- Use `PATCH`, not `PUT`, because requests are partial edits and omitted fields
  must remain unchanged.
- Use existing token or bearer auth only; no auth model or permission change.
- Keep v1 web editing to one selected job. REST may edit multiple jobs for
  non-identity fields, matching the existing `wr mod` server behavior.
- Treat env editing as job-specific overrides only. Inherited environment is
  read-only unless existing job modify logic gains safe full-env support later.
- Do not expose bulk web edit, running-job edit, RepGroup rename, dep-group
  membership edit, or bsub-mode edit in v1.
- Tests use GoConvey and user-visible behavior: REST HTTP responses, decoded
  `JStatus`, websocket details, and browser-observable modify state. Follow
  `go-implementor`, `go-reviewer`, and `testing-principles`.
