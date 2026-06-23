# Simple Todos

## Workflow — how to tackle this file

**Entry point.** If you've been told "follow the instructions in this file", begin at
**Step 0**. This workflow is **idempotent and resumable**: it does the right thing
whether this is the first run or a resume after an interruption, a crash, or a wiped
scratch directory. It is run by **one Codex orchestrator agent** that owns every
section's state machine. The orchestrator starts **worker subagents** for bounded work
inside individual sections, but those workers **must not spawn more subagents** and must
not own whole-section lifecycle decisions.

Parallelism still happens: the orchestrator starts workers for the next runnable
milestone in several sections before waiting for any one section to finish, then advances
sections as results come back. All scratch files and worktrees live under
`<main-repo>/.tmp/`; do **not** use `/tmp`.

### Step 0 — Orient and reconcile (do this first, every run)

A previous run may have died mid-step, so **don't trust the checkboxes** — rebuild the
true state from durable sources, correct the boxes, and resume each section where it
actually is.

1. `git -C <main-repo> fetch origin --prune` (brings in `develop` and every pushed branch).
2. Read ground truth (this survives crashes, `.tmp` wipes, even a different machine):
   - PRs (canonical): `gh pr list --state all --json number,headRefName,state,url,body,mergedAt` — map each to a section by the `Solves #N` issue numbers in its body.
   - Issue state: `gh issue view <N> --json state` per section.
   - Leftover worktrees: `git -C <main-repo> worktree list`.
3. Classify each `##` section, fix its checkboxes to match, and resume from the right point:
   - **Issues closed + PR merged** → done; skip.
   - **PR merged, issue(s) still open** → run only the post-merge tail (comment "ships in the next release" + `gh issue close`; mark **Merged**/**Solved**), then rebase any still-open sibling branches.
   - **PR open** → resume the PR stage: rebase onto the latest `origin/develop` if behind, handle CI/review comments from the orchestrator, and keep it in the monitor-until-merge loop.
   - **Branch exists (on `origin` and/or a leftover worktree) but no PR** → resume implementation, then push + open the PR.
   - **Nothing exists for the section** → start it fresh.
4. Worktrees: reuse `<main-repo>/.tmp/wr-work/<branch>` if still present; otherwise
   recreate it from `origin/<branch>` (if that branch was pushed) or fresh from
   `origin/develop`. If the branch name contains slashes, create the corresponding
   nested directory under `.tmp/wr-work/`.
5. Build a section-state table: branch, issues, worktree path, PR URL, current milestone,
   active worker (if any), and last pushed commit. Then start workers for every section
   whose next milestone is runnable. Do not wait for one section's worker before
   starting workers for other sections.

### Worker subagents

Workers are short-lived and bounded. Start them from the orchestrator with one explicit
task in one worktree: implementation, focused review, CI failure fix, PR-comment fix, or
rebase/conflict resolution. A worker may edit code, run tests/lint with timeouts, commit,
and push **only its assigned branch**. A worker must not update this file, open or close
issues, open or merge PRs, monitor siblings, or launch another subagent. If it needs more
help, it reports the need and the orchestrator starts the next worker.

For implementation work, use the `bugfix` workflow semantics at the orchestrator level:
write or confirm a failing test first where practical, implement the smallest fix, run
targeted tests and the relevant linter, and use a separate review worker when the risk or
diff size warrants it. Do **not** ask a worker to invoke the `bugfix` skill itself,
because that skill may require nested subagents in this harness.

Each successful implementation/review worker reports branch, worktree, commit SHA, pushed
remote ref, tests run, issue numbers, and any follow-up needed. Because workers push
after each durable commit, a lost `.tmp` worktree costs at most the current uncommitted
change.

### Orchestrator

- Is the **single writer of this file's checkboxes**, and ticks a box only once the
  milestone is durably done (see Durability). On worker reports it reconciles the branch
  state, verifies the pushed commit/PR state, and ticks **Implemented** / **Reviewed**.
- Opens PRs against **develop** once implementation is pushed. PR bodies must **solve**
  the issues — list them `Solves #N`, **not** `Fixes/Closes #N` (we close them after
  merge with a release note).
- Tells you, per ready section: "PR `<url>` is ready to merge (covers #a, #b)."
- **Monitors each ready PR until merged** by polling with `gh pr view` / `gh pr checks`
  or by using any available Codex automation. Whenever CI fails or **new human review
  comments** land, the orchestrator starts a PR-resolution worker in that branch's
  worktree, waits for its pushed fix, and repeats until merge. Do **not** stop at
  Copilot-happy if new human comments arrive later.
- **On each merge:** comment on that section's issues — *"Fixed on `develop`; will ship
  in the next release."* — and `gh issue close` them; tick **Merged** + **Solved**.
  Then `git fetch origin develop` and **rebase every still-open section's branch** onto
  it (`git rebase origin/develop` → `git push --force-with-lease`, starting
  PR-resolution workers if anything changed). Repeat until all sections are Merged +
  Solved.

Monitoring, issue-closing and cross-branch rebases live in the orchestrator. Keep at most
one active writer worker per branch/worktree at a time, but keep different branches moving
in parallel whenever their next milestones are independent.

### Durability & resuming after an interruption

- **A box is ticked only after its work is pushed to `origin`:** **Implemented** after
  the commits are pushed; **Reviewed** after PR-resolution/review changes are pushed and
  CI is green; **Merged**/**Solved** only after the merge and issue closes. So a box can
  never claim work that isn't on `origin`, and a reset can't make a section look further
  along than it is.
- **Transient throttling** (HTTP 429 / "overloaded", 529) is retried with backoff. A hard
  interruption is a stop, not an auto-resume; nothing is corrupted, because durable work
  is committed/pushed and Step 0 rebuilds the picture from `origin` + PRs + issues.
- **To resume after any interruption: start a fresh agent on this file again.** Step 0
  rediscovers all pushed branches, open/merged PRs and closed issues and continues; at
  most the one in-flight uncommitted change is redone.

Notes: PRs target **`develop`**, not `master`. Keep each branch to its section's
items so the diff stays reviewable (Copilot refuses very large diffs).
`chore/error-wrapping-sweep` (#301) will likely need splitting into several
per-package PRs — run each as its own branch off develop through the same flow.

---

Straightforward, unambiguous items — the implementation is determined and needs no
product decisions. Items are grouped into proposed **branches** (the section
headers). Each branch batches work that can be implemented and reviewed together —
either closely related or individually trivial — so we amortise the
local-tests → CI → Copilot-review cycle instead of paying it per tiny change.

Tick the checklist as each branch progresses.

## feat/remote-manager-env-merge

- [x] Implemented
- [x] Reviewed
- [x] Merged
- [x] Solved

- **[#512 Choose if env is used with remote manager](https://github.com/VertebrateResequencing/wr/issues/512)** — add an opt-in setting that makes `wr add` treat a remote manager like a local manager when selecting defaults. Keep the current conservative default for remote managers, where environment variables are not merged and cwd defaults to `/tmp` unless `cwd_matters` is enabled, but allow same-cluster users to keep local-style cwd and environment behaviour when it is safe and desirable. Make this defaultable via config/env var so users do not need to specify the flag every time. Add tests covering both the default remote-manager behaviour and the opt-in path.

## fix/small-fixes-batch

- [x] Implemented
- [x] Reviewed
- [x] Merged
- [x] Solved

Three trivial, independent fixes batched into one PR to amortise overhead. #507 and
#324 are small Go changes with unit tests; #310 is a frontend-only change that needs
manual/browser verification.

- **[#507 Option to only add the first x command in a file as a test](https://github.com/VertebrateResequencing/wr/issues/507)** — add a `--head N` flag to `wr add` that keeps only the first N parsed commands when reading the command file (in `cmd/add.go`, where `parseCmdFile` builds the job list). `0` (default) means all. Add a unit test.
- **[#324 Strange start/end time issue with quick-running dependent jobs](https://github.com/VertebrateResequencing/wr/issues/324)** — in `jobqueue/job.go` `ToStatus()` (~lines 978/983) emit `StartTime.UnixNano()` / `EndTime.UnixNano()` instead of `.Unix()`, so sub-second jobs no longer report `Started == Ended` despite a non-zero `Walltime`. The newer push path already uses `UnixNano` (`server.go:2847`) and the `JStatus.Started/Ended` fields are `*int64`, so this is a pure reporting fix. (Dependency ordering is already guaranteed structurally by the queue — no behaviour change there.)
- **[#310 Status webpage: times out](https://github.com/VertebrateResequencing/wr/issues/310)** — in `jobqueue/static/js/wr/websocket-handler.js`, on `onclose` attempt reconnection with a capped backoff, and on successful reconnect re-send `{Request:"current"}` to resync counts/state. Today it only reports the connection loss and requires a manual refresh.

## fix/openstack-reserved-quota-leak

- [x] Implemented
- [x] Reviewed
- [x] Merged
- [x] Solved

- **[#453 wr manager in cloud mode fails to update stats of reserved cpus when OS image is not available](https://github.com/VertebrateResequencing/wr/issues/453)** — confirmed live bug. `opst.spawn` increments `reserved{Instances,Cores,RAM,Volume}` (`jobqueue/scheduler/openstack.go:866-871`) and the only decrement is `usingQuotaCB`, which the provider goroutine runs after `usingQuotaCh <- true` (`cloud/openstack.go:953`). But `getImage`/`getFlavor` failures return early (`cloud/openstack.go:874-882`, "no OS image..." at `:355`) before that signal, so the callback never fires (also leaking a goroutine) and the scheduler's error-return path (`openstack.go:1022-1041`) doesn't decrement either — so every bad-image spawn permanently leaks the reservation until quota is exhausted and the manager locks up. **Fix:** guarantee the reservation is released on every early-return error path (e.g. have `cloud.Provider.Spawn` invoke the callback on its error return, or release in the scheduler's error branch / via `defer`). Add a regression test using the existing `debugEffect` mock-spawn hooks (`jobqueue/scheduler/openstack.go` ~`:900-905`, `:1012-1014`). No OpenStack environment is needed to reproduce or test.

## chore/error-wrapping-sweep

- [x] Implemented
- [x] Reviewed
- [x] Merged
- [x] Solved

- **[#301 Update error handling](https://github.com/VertebrateResequencing/wr/issues/301)** — mechanical modernisation: replace `err == someErr` comparisons (other than `nil` / `io.EOF`) with `errors.Is`, replace `err.(*T)` type assertions with `errors.As`, and add `%w` wrapping where it adds useful context. **Scope note:** a full-repo sweep would be far too large for one reviewable PR (Copilot will refuse), so split the work one top-level package per PR (e.g. `jobqueue`, then `cloud`, `cmd`, `fs`, `queue`, ...). The checklist above tracks the overall effort; expect several PRs under this branch theme.

  Split PRs: #513 `jobqueue` merged; #520 `cmd`, #521 `queue`/`rp`, #522
  `cloud`, and #523 `internal`/`network` are queued.

## feat/status-cli-output

- [x] Implemented
- [x] Reviewed
- [x] Merged
- [x] Solved

Two related additions to `wr status` CLI output (both centre on `cmd/status.go`), promoted from todo-complex now that the design is settled.

- **[#506 Table-like report mode for wr status](https://github.com/VertebrateResequencing/wr/issues/506)** — add a new `-o table` (alias `t`) output mode rendered with a small aligned table writer. Columns are configurable via a `WR_STATUS_FORMAT` env var using LSF `LSB_BJOBS_FORMAT`-style syntax (`FIELD:width FIELD:width ...`). When unset, the default columns are those in the issue: Command, ID, Status, Attempts, Host, Requirements group, and the count of tasks sharing that status. As with normal `wr status`, show a representative one per same-status group. CLI only — no web UI change.
- **[#288 Show scheduler issues on the command line](https://github.com/VertebrateResequencing/wr/issues/288)** — add a go-client method to fetch the scheduler warnings / bad (dead/lost) servers / dismissible messages the web UI surfaces (REST already exposes these via the warnings and bad-servers endpoints), and print them as a footer at the end of `wr status` output. Include everything the web UI can alert about.

## perf/status-count-path

- [x] Implemented
- [x] Reviewed
- [x] Merged
- [x] Solved

- **[#322 wr status: can be unexpectedly slow](https://github.com/VertebrateResequencing/wr/issues/322)** — `-o c` (and `-o summary`) currently make the server decode and ship every matching complete job just to count them (`jobqueue/db.go retrieveCompleteJobsByRepGroup` → `getJobsByRepGroup` → `limitJobs`). Add a new client/server request type that returns per-state counts for a repgroup match **without** decoding/returning full jobs, computed on demand by iterating keys (only introduce maintained counters if they can be guaranteed not to drift). Wire both `-o c` and `-o summary` to the new fast path. Add a benchmark/regression test.

## perf/mod-reverse-lookup

- [x] Implemented
- [x] Reviewed
- [x] Merged
- [x] Solved

- **[#333 wr mod: too slow on lots of jobs](https://github.com/VertebrateResequencing/wr/issues/333)** — `jobqueue/db.go modifyLiveJobs` deletes each modified job's lookup entries by scanning the **entire** `bucketRTK`/`bucketDTK`/`bucketRDTK` buckets per job (O(jobs_modified × total entries); the maintainer's own `// *** ... reverse lookup` comment is right there). Add a reverse-lookup index (job key → its lookup-bucket entries) so deletion is O(entries-per-job); maintain it on add/modify/delete, and rebuild it on first load for pre-existing DBs that lack it. Rely on the speedup alone — do **not** raise the client timeout. Add a benchmark/regression test.

## fix/rerun-dependent-jobs

- [x] Implemented
- [x] Reviewed
- [x] Merged
- [x] Solved

- **[#326 Adding a dependent job with --rerun results in unexpected behaviour](https://github.com/VertebrateResequencing/wr/issues/326)** — desired behaviour: a job added with `--rerun` should **always** be re-run even if a matching command previously completed; but if it has dependencies that are currently incomplete, it must **wait** on them exactly as it would have on first add (not run immediately, and not be skipped as a duplicate). This is scoped to how the *explicitly* re-added job is treated; wr's existing **live-dependency cascade stays as-is** — re-running a parent already auto-resurrects its downstream dependents transitively (`jobqueue/db.go` `retrieveDependentJobs`, cascade at `db.go:952-983`), which is the intended "live deps" feature, so #326 must not change it. (Selectively *disabling* that cascade would be the separate #28, which is parked in Can't Fix.) Use the simple TDD flow: first write a failing acceptance test for an explicit `--rerun` of a previously-completed dependent job (it should re-queue and wait on its incomplete deps, not be skipped as a duplicate), then fix.

## feat/log-rotation

- [x] Implemented
- [x] Reviewed
- [x] Merged
- [x] Solved

- **[#251 Implement log rotation](https://github.com/VertebrateResequencing/wr/issues/251)** — add size-based rotation to the `clog` file writer using `gopkg.in/natefinch/lumberjack`. Expose config via `WR_LOGS*`-prefixed options with the values typical/expected for log rotation (max size MB, max backups, max age days, compress). Apply rotation to **both** the manager log and the runner file logs (`--runner_filelog`).

## feat/webui-rerun-completed

- [x] Implemented
- [x] Reviewed
- [x] Merged
- [x] Solved

- **[#20 Status webpage: add rerun button](https://github.com/VertebrateResequencing/wr/issues/20)** — add a "Rerun" action/button to completed jobs in the web status UI that triggers a fresh run by re-adding the command with `rerun=true`, **reusing** the existing add+rerun mechanism (no dedicated new REST action). Available for any completed job. Show a confirmation dialog of the same style as the existing job-removal confirmation.

## perf/client-payload-trim

- [x] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

Promoted from todo-complex now that the audit is done and every decision is made (single focused theme; no spec needed).

- **[#290 Improve efficiency of client methods](https://github.com/VertebrateResequencing/wr/issues/290)** — `Archive` (`jobqueue/client.go:1742`), `Release` (`:1769`), `Bury` (`:1811`) and `Touch` (`:1681`) encode and send the whole `*Job` (including the compressed `EnvC` env blob, std-out/err, cmd, mounts, behaviours) when the server handlers only read the job key — plus `FailReason` for release/bury and `JobEndState.Stdout/Stderr` for archive/bury (see `jobqueue/serverCLI.go` `jarchive`/`jrelease`/`jbury`/`jtouch`, ~`:581-706`, which resolve the job via `cr.Job.Key()`). `Kill`/`Delete`/`Modify`/`Kick` already send keys only — follow that pattern. Trim the **wire payload only**: populate a key (plus `FailReason`/`JobEndState` where needed) in the `clientRequest` instead of the whole job and have the handlers read from it, keeping every public method signature and behaviour identical (the only stated constraint). **Coordinate `Touch` with #98** (live introspection adds peak-RAM/CPU + a stdout/err tail to the touch path) — trim Touch to "key + the fields #98 needs". Add a test asserting the built request omits the large fields (e.g. `EnvC`/`Cmd` empty) rather than checking exact wire bytes. Audit complete: these four are the only over-senders.

## fix/memory-kill-attribution

- [x] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

Promoted from todo-complex now that the OOM-detection method is decided. Single focused theme (runner + one server condition); the cgroup-confirm path is hard to exercise in CI, so use a reviewer worker to cover it.

- **[#502 Distinguish "killed for high memory" from "failed for another reason but used more memory than expected"](https://github.com/VertebrateResequencing/wr/issues/502)** — the runner's exit-handling switch tests `ranoutMem` (just `peakmem > job.Requirements.RAM`, set at `jobqueue/client.go:1225`) **before** every other reason (`client.go:1430`), so any job that merely peaked over its estimate is reported as `FailReasonRAM`, masking the real cause. Fix in three parts: (1) **only report `FailReasonRAM` when wr can attribute the death to memory** — read the cgroup OOM-kill counter where the job has its own cgroup (cgroup v2 `memory.events` `oom_kill`, v1 `memory.oom_control`; resolve via `/proc/<pid>/cgroup`), and where it isn't in its own attributable cgroup (cloud/`local` scheduler) fall back to the heuristic "child killed by SIGKILL (`WaitStatus.Signaled() && Signal()==SIGKILL`) with peak >= estimate"; (2) **otherwise report the real reason** (`FailReasonExit`/signal/disk/time) and, when peak also exceeded the estimate, append a non-authoritative note concatenating the memory message after it; (3) **decouple the auto-reschedule-with-more-RAM from `FailReason`** — the server bumps only on `case FailReasonRAM` (`server.go:1889`); change it to bump whenever `job.PeakRAM > job.Requirements.RAM`, so expected memory always grows on retry regardless of the reported reason. Scope: external OOM kills (wr-initiated kills almost never happen). Tests: the reorder/decouple/concat/SIGKILL-fallback paths TDD directly; the cgroup-confirm path needs a unit test over fabricated `memory.events` content (forcing a real kernel OOM in CI is impractical).
