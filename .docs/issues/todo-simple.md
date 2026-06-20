# Simple Todos

## Workflow — how to tackle this file

Execute this file with **one orchestrator agent** that fans the sections out to
**parallel section agents** (one per `##` branch section below). These mechanics have
been verified to work in this environment: the `develop` branch exists; isolated
`git worktree`s created off `origin/develop` work; subagents can spawn their own
subagents; subagents can invoke the `bugfix` and `pr-resolver` skills; `gh` can push,
open PRs, and comment on / close issues; and the `Monitor` tool can watch a PR for
merge.

**Section agent** — spawn one per branch section, all in parallel (several `Agent`
calls in a single message), each given its branch name and item list. It:

1. Makes an isolated checkout off the latest develop and creates the branch (run
   `git fetch origin develop` first):
   `git -C <main-repo> worktree add /tmp/wr-work/<branch> -b <branch> origin/develop`.
   All work happens inside that worktree.
2. Implements the section's items by invoking the **`bugfix` skill** (it runs TDD via
   its own implementor/reviewer subagents and auto-commits each fix). Make the
   affected package's tests and linter pass locally.
3. Ticks **Implemented** in its own section below. (Each section's checkboxes are
   distinct lines, so parallel edits don't collide; re-read and retry if an edit ever
   fails.)
4. `git push -u origin <branch>`, then opens a PR against **develop**:
   `gh pr create --base develop ...`. The body says it **solves** the section's
   issues, listing them as `Solves #N` — **not** `Fixes/Closes #N`, because we close
   the issues ourselves after merge with a release note.
5. Drives the PR to a good state by invoking the **`pr-resolver` skill**: loop on CI
   + review comments until CI is green and **Copilot** is satisfied, then tick
   **Reviewed** and report it ready. This is *not* the end of PR resolution — normally
   `pr-resolver` stops once Copilot is happy, but here the PR must keep being re-run
   through `pr-resolver` for any **new human review comments** right up until the
   merge is detected. That ongoing loop runs during monitoring (see the orchestrator).
6. Reports back to the orchestrator — PR URL, branch, worktree path, issue numbers —
   then returns.

**Orchestrator** — once section agents report ready:

7. Tells you, per section: "PR `<url>` is ready to merge (covers #a, #b)."
8. **Monitors each ready PR until it is merged** (e.g. `Monitor` a persistent poll of
   `gh pr view <url> --json state,reviews,comments`). Two jobs during this window:
   (a) whenever **new human review comments** land, re-run the **`pr-resolver` skill**
   in that branch's worktree to address them — do **not** stop at Copilot-happy; keep
   looping until merge; (b) when `state == MERGED`, proceed to step 9.
9. **On each merge:** comments on that section's issues — *"Fixed on `develop`; will
   ship in the next release."* — and `gh issue close`s them; ticks **Merged** and
   **Solved**. Then `git fetch origin develop` and **rebases every still-open
   section's branch** onto the new develop (`git rebase origin/develop` in each
   worktree → `git push --force-with-lease`, re-running `pr-resolver` if the rebase
   changed anything). Repeat until all sections are Merged + Solved.

Monitoring, issue-closing and the cross-branch rebase live in the orchestrator (not
inside each section agent) because a section agent can't reach its siblings to make
them rebase, and one central watcher is far more reliable than several agents each
blocked for hours — the net effect is exactly the flow described above.

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

## fix/small-fixes-batch

- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

Three trivial, independent fixes batched into one PR to amortise overhead. #507 and
#324 are small Go changes with unit tests; #310 is a frontend-only change that needs
manual/browser verification.

- **[#507 Option to only add the first x command in a file as a test](https://github.com/VertebrateResequencing/wr/issues/507)** — add a `--head N` flag to `wr add` that keeps only the first N parsed commands when reading the command file (in `cmd/add.go`, where `parseCmdFile` builds the job list). `0` (default) means all. Add a unit test.
- **[#324 Strange start/end time issue with quick-running dependent jobs](https://github.com/VertebrateResequencing/wr/issues/324)** — in `jobqueue/job.go` `ToStatus()` (~lines 978/983) emit `StartTime.UnixNano()` / `EndTime.UnixNano()` instead of `.Unix()`, so sub-second jobs no longer report `Started == Ended` despite a non-zero `Walltime`. The newer push path already uses `UnixNano` (`server.go:2847`) and the `JStatus.Started/Ended` fields are `*int64`, so this is a pure reporting fix. (Dependency ordering is already guaranteed structurally by the queue — no behaviour change there.)
- **[#310 Status webpage: times out](https://github.com/VertebrateResequencing/wr/issues/310)** — in `jobqueue/static/js/wr/websocket-handler.js`, on `onclose` attempt reconnection with a capped backoff, and on successful reconnect re-send `{Request:"current"}` to resync counts/state. Today it only reports the connection loss and requires a manual refresh.

## fix/openstack-reserved-quota-leak

- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

- **[#453 wr manager in cloud mode fails to update stats of reserved cpus when OS image is not available](https://github.com/VertebrateResequencing/wr/issues/453)** — confirmed live bug. `opst.spawn` increments `reserved{Instances,Cores,RAM,Volume}` (`jobqueue/scheduler/openstack.go:866-871`) and the only decrement is `usingQuotaCB`, which the provider goroutine runs after `usingQuotaCh <- true` (`cloud/openstack.go:953`). But `getImage`/`getFlavor` failures return early (`cloud/openstack.go:874-882`, "no OS image..." at `:355`) before that signal, so the callback never fires (also leaking a goroutine) and the scheduler's error-return path (`openstack.go:1022-1041`) doesn't decrement either — so every bad-image spawn permanently leaks the reservation until quota is exhausted and the manager locks up. **Fix:** guarantee the reservation is released on every early-return error path (e.g. have `cloud.Provider.Spawn` invoke the callback on its error return, or release in the scheduler's error branch / via `defer`). Add a regression test using the existing `debugEffect` mock-spawn hooks (`jobqueue/scheduler/openstack.go` ~`:900-905`, `:1012-1014`). No OpenStack environment is needed to reproduce or test.

## chore/error-wrapping-sweep

- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

- **[#301 Update error handling](https://github.com/VertebrateResequencing/wr/issues/301)** — mechanical modernisation: replace `err == someErr` comparisons (other than `nil` / `io.EOF`) with `errors.Is`, replace `err.(*T)` type assertions with `errors.As`, and add `%w` wrapping where it adds useful context. **Scope note:** a full-repo sweep would be far too large for one reviewable PR (Copilot will refuse), so split the work one top-level package per PR (e.g. `jobqueue`, then `cloud`, `cmd`, `fs`, `queue`, ...). The checklist above tracks the overall effort; expect several PRs under this branch theme.

## feat/status-cli-output

- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

Two related additions to `wr status` CLI output (both centre on `cmd/status.go`), promoted from todo-complex now that the design is settled.

- **[#506 Table-like report mode for wr status](https://github.com/VertebrateResequencing/wr/issues/506)** — add a new `-o table` (alias `t`) output mode rendered with a small aligned table writer. Columns are configurable via a `WR_STATUS_FORMAT` env var using LSF `LSB_BJOBS_FORMAT`-style syntax (`FIELD:width FIELD:width ...`). When unset, the default columns are those in the issue: Command, ID, Status, Attempts, Host, Requirements group, and the count of tasks sharing that status. As with normal `wr status`, show a representative one per same-status group. CLI only — no web UI change.
- **[#288 Show scheduler issues on the command line](https://github.com/VertebrateResequencing/wr/issues/288)** — add a go-client method to fetch the scheduler warnings / bad (dead/lost) servers / dismissible messages the web UI surfaces (REST already exposes these via the warnings and bad-servers endpoints), and print them as a footer at the end of `wr status` output. Include everything the web UI can alert about.

## perf/status-count-path

- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

- **[#322 wr status: can be unexpectedly slow](https://github.com/VertebrateResequencing/wr/issues/322)** — `-o c` (and `-o summary`) currently make the server decode and ship every matching complete job just to count them (`jobqueue/db.go retrieveCompleteJobsByRepGroup` → `getJobsByRepGroup` → `limitJobs`). Add a new client/server request type that returns per-state counts for a repgroup match **without** decoding/returning full jobs, computed on demand by iterating keys (only introduce maintained counters if they can be guaranteed not to drift). Wire both `-o c` and `-o summary` to the new fast path. Add a benchmark/regression test.

## perf/mod-reverse-lookup

- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

- **[#333 wr mod: too slow on lots of jobs](https://github.com/VertebrateResequencing/wr/issues/333)** — `jobqueue/db.go modifyLiveJobs` deletes each modified job's lookup entries by scanning the **entire** `bucketRTK`/`bucketDTK`/`bucketRDTK` buckets per job (O(jobs_modified × total entries); the maintainer's own `// *** ... reverse lookup` comment is right there). Add a reverse-lookup index (job key → its lookup-bucket entries) so deletion is O(entries-per-job); maintain it on add/modify/delete, and rebuild it on first load for pre-existing DBs that lack it. Rely on the speedup alone — do **not** raise the client timeout. Add a benchmark/regression test.

## fix/rerun-dependent-jobs

- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

- **[#326 Adding a dependent job with --rerun results in unexpected behaviour](https://github.com/VertebrateResequencing/wr/issues/326)** — desired behaviour: a job added with `--rerun` should **always** be re-run even if a matching command previously completed; but if it has dependencies that are currently incomplete, it must **wait** on them exactly as it would have on first add (not run immediately, and not be skipped as a duplicate). This is scoped to how the *explicitly* re-added job is treated; wr's existing **live-dependency cascade stays as-is** — re-running a parent already auto-resurrects its downstream dependents transitively (`jobqueue/db.go` `retrieveDependentJobs`, cascade at `db.go:952-983`), which is the intended "live deps" feature, so #326 must not change it. (Selectively *disabling* that cascade would be the separate #28, which is parked in Can't Fix.) Use the bugfix skill's TDD: first write a failing acceptance test for an explicit `--rerun` of a previously-completed dependent job (it should re-queue and wait on its incomplete deps, not be skipped as a duplicate), then fix.

## feat/log-rotation

- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

- **[#251 Implement log rotation](https://github.com/VertebrateResequencing/wr/issues/251)** — add size-based rotation to the `clog` file writer using `gopkg.in/natefinch/lumberjack`. Expose config via `WR_LOGS*`-prefixed options with the values typical/expected for log rotation (max size MB, max backups, max age days, compress). Apply rotation to **both** the manager log and the runner file logs (`--runner_filelog`).

## feat/webui-rerun-completed

- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

- **[#20 Status webpage: add rerun button](https://github.com/VertebrateResequencing/wr/issues/20)** — add a "Rerun" action/button to completed jobs in the web status UI that triggers a fresh run by re-adding the command with `rerun=true`, **reusing** the existing add+rerun mechanism (no dedicated new REST action). Available for any completed job. Show a confirmation dialog of the same style as the existing job-removal confirmation.
