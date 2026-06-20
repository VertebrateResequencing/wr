# Simple Todos

## Workflow — how to tackle this file

**Entry point.** If you've been told "follow the instructions in this file", begin at
**Step 0**. This workflow is **idempotent and resumable**: it does the right thing
whether this is the first run or a resume after an interruption — a Claude Code 5-hour
usage-limit reset, a crash, or a wiped `/tmp`. It is run by **one orchestrator agent**
that fans the sections out to **parallel section agents** (one per `##` branch section
below). Verified mechanics in this environment: `develop` exists; isolated
`git worktree`s off `origin/develop` work; subagents can spawn their own subagents and
invoke the `bugfix`/`pr-resolver` skills; `gh` can push, open PRs, comment on / close
issues; `Monitor` can watch a PR for merge.

### Step 0 — Orient and reconcile (do this first, every run)

A previous run may have died mid-step, so **don't trust the checkboxes** — rebuild the
true state from durable sources, correct the boxes, and resume each section where it
actually is.

1. `git -C <main-repo> fetch origin --prune` (brings in `develop` and every pushed branch).
2. Read ground truth (this survives crashes, `/tmp` wipes, even a different machine):
   - PRs (canonical): `gh pr list --state all --json number,headRefName,state,url,body,mergedAt` — map each to a section by the `Solves #N` issue numbers in its body.
   - Issue state: `gh issue view <N> --json state` per section.
   - Leftover worktrees: `git -C <main-repo> worktree list`.
3. Classify each `##` section, fix its checkboxes to match, and resume from the right point:
   - **Issues closed + PR merged** → done; skip.
   - **PR merged, issue(s) still open** → run only the post-merge tail (comment "ships in the next release" + `gh issue close`; mark **Merged**/**Solved**), then rebase any still-open sibling branches.
   - **PR open** → resume the PR stage: rebase onto the latest `origin/develop` if behind, re-run `pr-resolver` (incl. any new human comments), keep it in the monitor-until-merge loop.
   - **Branch exists (on `origin` and/or a leftover worktree) but no PR** → resume implementation, then push + open the PR.
   - **Nothing exists for the section** → start it fresh.
4. Worktrees: reuse `/tmp/wr-work/<branch>` if still present; otherwise recreate it from
   `origin/<branch>` (if that branch was pushed) or fresh from `origin/develop`. Because
   section agents **push after every commit**, a lost worktree costs at most the current
   uncommitted change.
5. Dispatch: (re)spawn a section agent for every section that needs fresh work or
   implementation-resume, and resume the orchestrator's monitor-until-merge loop for
   every open or post-merge-pending PR.

### Section agent — one per `##` branch section (the headers are the branch names)

Spawn these in parallel (several `Agent` calls in one message), each given its branch
name + item list. It:

1. Gets an isolated worktree off the latest develop — reuse the one Step 0 found, else
   `git fetch origin develop` then `git -C <main-repo> worktree add
   /tmp/wr-work/<branch> -b <branch> origin/develop` (use `origin/<branch>` instead to
   resume a pushed branch). All work happens there.
2. Implements the section's items with the **`bugfix` skill** (TDD via its own
   implementor/reviewer subagents; auto-commits each fix). Tests + linter pass.
   **`git push` after each commit** so progress is durable on `origin/<branch>`.
3. Once the implementation is committed *and pushed*, reports **Implemented** to the
   orchestrator.
4. If no PR exists yet, opens one against **develop** (`gh pr create --base develop ...`)
   whose body **solves** the issues — list them `Solves #N`, **not** `Fixes/Closes #N`
   (we close them after merge with a release note).
5. Drives the PR to a good state with the **`pr-resolver` skill**: loop on CI + review
   comments until CI is green and **Copilot** is satisfied, then push and report
   **Reviewed / ready**. This is *not* the end — `pr-resolver` normally stops once
   Copilot is happy, but the PR must keep being re-run for any **new human review
   comments** right up until merge (that loop runs during monitoring).
6. Reports back — PR URL, branch, worktree path, issue numbers — then returns.

### Orchestrator

- Is the **single writer of this file's checkboxes** (so parallel section agents never
  collide editing it), and ticks a box only once the milestone is durably done (see
  Durability). On each section agent's report it ticks **Implemented** / **Reviewed**.
- Tells you, per ready section: "PR `<url>` is ready to merge (covers #a, #b)."
- **Monitors each ready PR until merged** (e.g. `Monitor` a persistent poll of
  `gh pr view <url> --json state,reviews,comments`): (a) whenever **new human review
  comments** land, re-run `pr-resolver` in that branch's worktree — do **not** stop at
  Copilot-happy; loop until merge; (b) on `state == MERGED`, continue.
- **On each merge:** comment on that section's issues — *"Fixed on `develop`; will ship
  in the next release."* — and `gh issue close` them; tick **Merged** + **Solved**.
  Then `git fetch origin develop` and **rebase every still-open section's branch** onto
  it (`git rebase origin/develop` → `git push --force-with-lease`, re-running
  `pr-resolver` if anything changed). Repeat until all sections are Merged + Solved.

Monitoring, issue-closing and cross-branch rebases live in the orchestrator (a section
agent can't reach its siblings, and one central watcher beats many agents blocked for
hours).

### Durability & resuming after a usage-limit reset

- **A box is ticked only after its work is pushed to `origin`:** **Implemented** after
  the commits are pushed; **Reviewed** after `pr-resolver`'s changes are pushed and CI
  is green; **Merged**/**Solved** only after the merge and issue closes. So a box can
  never claim work that isn't on `origin`, and a reset can't make a section look
  further along than it is.
- **Transient throttling** (HTTP 429 / "overloaded", 529) is auto-retried with backoff —
  it just slows down. The **hard 5-hour cap is a stop, not an auto-resume**: in-flight
  work halts, but nothing is corrupted because everything is committed/pushed and Step 0
  rebuilds the picture from `origin` + PRs + issues.
- **To resume after the cap resets (or any interruption): just start a fresh agent on
  this file again.** Step 0 rediscovers all pushed branches, open/merged PRs and closed
  issues and continues; at most the one in-flight uncommitted change is redone.

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
