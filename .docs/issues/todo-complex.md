# Complex Todos

## Workflow — how to tackle this file

**Entry point.** If you've been told "follow the instructions in this file", begin at
**Step 0**. Same **idempotent, resumable** shape as `todo-simple.md`: **one Codex
orchestrator** owns every section's state machine, and starts bounded worker subagents
for runnable milestones across several sections in parallel. The differences are that
each branch first **writes a spec**, then **implements by following the generated phase
files** rather than by using the simple bugfix flow.

Important harness constraint: worker subagents **cannot spawn their own subagents**. Do
not hand an entire section to a "section agent" and tell it to run `spec-writer`,
`orchestrator`, or `pr-resolver`. The main orchestrator runs those workflows at the top
level by starting the necessary author/implement/review/PR-fix workers itself. All
scratch files and worktrees live under `<main-repo>/.tmp/`; do **not** use `/tmp`.

The `prompt.md` → `spec.md` → `phaseN.md` pipeline has shipped in this repo (see
`.docs/sub/`, `.docs/nowrap/`), but in this harness the main orchestrator is the only
agent allowed to coordinate that pipeline across subagents.

**Branch naming (so resumes can find prior work): each branch name contains its issue
number — use `feat/issue-<N>`** (e.g. `feat/issue-502`). **#19 is folded into #197** →
a single `feat/issue-197` branch covering both; spec and implement them together.

### Step 0 — Orient and reconcile (do this first, every run)

A previous run may have died mid-step, so **don't trust the checkboxes** — rebuild true
state from durable sources, correct the boxes, resume each section where it is.

1. `git -C <main-repo> fetch origin --prune`.
2. Ground truth: `gh pr list --state all --json number,headRefName,state,url,body,mergedAt`
   (map to sections by `Solves #N`); `gh issue view <N> --json state` per section;
   `git -C <main-repo> worktree list`. For a section with no PR yet, look for its branch
   with `git ls-remote --heads origin '*issue-<N>*'`.
3. Classify each `##` section, fix its checkboxes, resume from the right point:
   - **Issues closed + PR merged** → done; skip.
   - **PR merged, issue(s) open** → post-merge tail only (comment "ships in the next release" + close; mark **Merged**/**Solved**), then rebase still-open siblings.
   - **PR open** → resume the PR stage: rebase onto latest `origin/develop` if behind; handle CI/review comments from the orchestrator; keep monitoring to merge.
   - **Branch has committed `spec.md` + `phaseN.md`, phases unfinished / no PR** → resume **implementation** from the phase checkboxes, then push + open the PR.
   - **Branch exists but no committed spec** → resume from **spec-writing**.
   - **Nothing exists** → start fresh.
4. Worktrees: reuse `<main-repo>/.tmp/wr-work/<branch>` if present; else recreate from
   `origin/<branch>` (if pushed) or fresh from `origin/develop`. If the branch name
   contains slashes, create the corresponding nested directory under `.tmp/wr-work/`.
5. Build a section-state table: issue numbers, branch, worktree path, docs path,
   spec/phase status, PR URL, active worker (if any), and last pushed commit. Then start
   workers for every section whose next milestone is runnable. Do not wait for one
   section's worker before starting workers for other sections.

### Worker subagents

Workers are short-lived and bounded. Start them from the orchestrator with one explicit
task in one worktree: draft spec inputs, author/review spec text, author/review phase
files, implement one phase item or phase, run a focused review, fix CI/review comments,
or resolve a rebase conflict. A worker may edit code/docs, run tests/lint with timeouts,
commit, and push **only its assigned branch**. A worker must not update this file, open
or close issues, open or merge PRs, monitor siblings, or launch another subagent. If it
needs more help, it reports the need and the orchestrator starts the next worker.

For spec production, run the `spec-writer` workflow semantics at the orchestrator level.
Do **not** ask a worker to invoke `spec-writer`. The orchestrator builds the feature
description from the section text (Issue + Current knowledge + Suggested way forward +
every Question rewritten as a decision using the recorded answers), writes/updates
`.docs/<branch-slug>/prompt.md`, and starts whatever author/review workers are needed to
produce reviewed `spec.md` and `phaseN.md` files. **Only surface human-choice-required
questions:** pre-bake known answers into the prompt's `## Notes` so routine clarification
returns NONE; resolve residual ambiguity from the recorded answers or a sensible default
and append it to Notes. Surface questions to the human only when they require a product or
maintainer choice rather than implementation judgment. If the spec workflow raises a
genuine blocker, write `blocker.md`, stop that section, and surface the blocker to the
human rather than opening a PR.

For implementation, run the phase-orchestration semantics at the top level. For each
`phaseN.md` in order, the orchestrator reads the phase checklist, starts implement/review
workers for unchecked phase items, and only marks a phase item complete after its commit
is pushed and verification is reported. Keep phase order within a branch, but keep
different branches moving in parallel. When the final phase is committed and pushed,
report/tick **Implemented**.

### Orchestrator

- Is the **single writer of this file's checkboxes**, ticking **Spec produced** /
  **Implemented** / **Reviewed** only after the relevant commits are pushed and verified.
- Opens PRs against **develop** once implementation is pushed. PR titles must include
  the issue number(s). PR bodies must start with standalone `Solves #N` line(s), then
  include the generated `.docs/<branch-slug>/` spec + phase files.
- Tells you each ready PR; **monitors each ready PR until merged** by polling with
  `gh pr view` / `gh pr checks` or by using any available Codex automation. Whenever CI
  fails or **new human review comments** land, the orchestrator starts a PR-resolution
  worker in that branch's worktree, waits for its pushed fix, and repeats until merge.
  Do **not** stop at Copilot-happy if new human comments arrive later.
- On merge, comments *"Fixed on `develop`; will ship in the next release."* and
  `gh issue close`s the section issues, ticking **Merged**/**Solved** only once
  confirmed. Then `git fetch origin develop` and rebase every still-open branch onto it
  (`--force-with-lease`, starting PR-resolution workers if needed).

Monitoring, the human-comment loop, issue-closing and cross-branch rebases all live in
the orchestrator. Keep at most one active writer worker per branch/worktree at a time,
but keep different branches moving whenever their next milestones are independent.

### Durability & resuming after an interruption

- **A box is ticked only after its work is pushed to `origin`:** **Spec produced** after
  the spec + phase files are committed and pushed; **Implemented** after the phase
  commits are pushed; **Reviewed** after PR-resolution/review changes are pushed + CI
  green; **Merged**/**Solved** only after the merge and issue closes.
- **Transient throttling** (429 / "overloaded", 529) is retried with backoff. A hard
  interruption is a stop, not an auto-resume; nothing is corrupted, because the spec,
  phase files (with their per-phase checkboxes) and code are all committed/pushed.
- **To resume after any interruption: start a fresh agent on this file.** Step 0
  rediscovers pushed branches (incl. their committed spec/phase files and per-phase
  checkboxes), open/merged PRs and closed issues, and continues — at most the current
  uncommitted change is redone.

Notes: PRs target **`develop`**, not `master`. One `##` issue = one branch/PR. **#19 is
folded into #197** — spec and implement them together on #197's branch. If the spec
workflow hits a genuine blocker (a recorded answer turns out to be unimplementable), it
writes `blocker.md` and stops rather than inventing behaviour — surface that to the
human instead of opening a PR.

---

Items that are substantial enough to deserve a short spec and/or a decision before
implementation. Each has its own section and checklist; the **Questions for you**
lines now have answers recorded inline.

Items that became unambiguous and focused enough once answered have been **promoted
to `todo-simple.md`** (#506, #288, #322, #333, #326, #251, #20, #290, #502); a few were
moved to the **Can't Fix** list in `lists.md` (#287, #194, #28). What remains below now
has its decisions recorded inline and stays here only because each is a sprawling
multi-layer feature worth speccing as a project (#316, #207, #197, #98, #19).

---

## #316 Unexpected dependency behaviour

- [x] Spec produced
- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

**Issue:** `wr add --deps foo` runs immediately if nothing with dep group `foo` has been added yet (because dependencies are "live"); users expect it to wait. The maintainer floated either clearer help text or a deliberate "wait for a future dep group" mode.

**Current knowledge:** this is by-design — `--deps` waits only on dep groups that already exist/are added; there is no semantic for "hold until a group that may appear later shows up."

**Suggested way forward:** depends entirely on the choice below — either (a) clarify the `--deps` help text and docs, or (b) add an opt-in mode/flag where depending on a not-yet-existing dep group keeps the job dependent until such a group appears.

**Questions for you:**
1. Docs-only clarification, or a real behaviour change?
   - behaviour change with docs improvement
2. If behaviour: opt-in flag name and exact semantics (how long to wait, how/when it resolves, interaction with normal live deps)?
   - not opt-in. Change the behaviour to always wait
3. Does "always wait" apply only to dep-group deps (`--deps`), or also to command/essence deps (`--cmd_deps`)? Both currently run immediately when the target hasn't been added yet.
   - Dep-group deps only — change `--deps` so a dependency on a dep group that has never existed blocks until such a group appears; `--cmd_deps`/essence deps keep today's behaviour (smaller blast radius, matches the issue).
4. "Always wait" makes a dependency on a never-existing group block indefinitely (a typo'd group name, or a branch that never runs, would hang forever). How do we keep that from being a silent hang?
   - Block, but make it visible and diagnosable: at `wr add` time, warn if a depended-on dep group has never been seen (it is still accepted and will wait); and in status, surface such jobs distinctly (e.g. "waiting on a dep group not yet seen") with a filter, so they can be fixed with `wr mod` or removed. Not a silent permanent wait.
5. How is "never existed" distinguished from "existed and already completed" (which must stay satisfied), and what about manager restarts?
   - Keep a persistent record of every dep group ever seen (beyond the live-jobs bucket), persisted with the manager DB, so "never existed -> block" is separable from "existed and completed -> satisfied". Existing "live" re-evaluation is unchanged (a group whose jobs all complete satisfies dependents; new jobs added to that group re-block them). Same-batch adds (the dependency and a carrier added in one `wr add`) continue to work.

**Decision (now implementation-ready):** scope is dep-group deps only (Q3); a dep-group dependency is satisfied only once >=1 job has carried that group and all such carriers are complete, while a group that has *never* existed now blocks (needs a persistent "groups ever seen" record, Q5). To stop never-appearing groups becoming silent hangs, blocked jobs are warned at add time and shown distinctly/filterably in status (Q4). Existing live re-evaluation and same-batch adds are preserved. Ready to spec (release-note the behaviour change for existing pipelines).

---

## #207 Allow to suspend and resume non-running jobs

- [x] Spec produced
- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

**Issue:** ability to suspend and resume jobs (e.g. to push urgent work through) as a first-class state, rather than the current limit-group-to-0 workaround.

**Current knowledge:** there is no "suspended" job state today; the workaround is setting a limit to 0. A real state touches the queue, status counts, CLI selectors/filters, REST, and the web UI. You noted previously that the web UI's colour palette is constrained by the Bootstrap feature in use.

**Suggested way forward:** add a suspended state plus `wr suspend` / `wr resume` (using the usual `-i`/`-z`/`-y` selectors), ensure suspended jobs aren't scheduled, and make the state visible/filterable in status and the web UI.

**Questions for you:**
1. v1 scope — CLI only first, or CLI + web together?
   - both
2. Does "suspend" apply only to pending/ready/dependent jobs, or should it also pause running jobs (significantly harder)?
   - not running
3. How should suspend interact with dependencies and with limit groups?
   - to those systems it should seem no different to the job being in pending or ready state
4. Web UI — reuse an existing Bootstrap colour for the new state, and add a status filter for it?
   - yes, can be the delayed colour

**Why still complex:** even with every decision made, this is a sprawling feature — a new job state threaded through the queue, persistence, status counts, CLI selectors + a new `wr suspend`/`wr resume`, REST, and the web UI (colour + filter). Worth a spec and phased plan.

---

## #197 Allow job modification using web/REST interfaces (folds #19)

- [x] Spec produced
- [x] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

**Issue:** expose the modification that `wr mod` performs on the CLI via the web UI and the public REST API. Per the answers below this now **also subsumes #19** (web editing of env vars + resource requirements).

**Current knowledge:** REST currently supports only GET/POST/DELETE on jobs (no PUT/PATCH); the web UI has no modify action; the server-side modify capability already exists (used by `wr mod`).

**Suggested way forward:** add a REST PUT/PATCH endpoint that applies `wr mod`-style changes, and add web UI editing — make every field the web UI displays for a non-running incomplete job editable, with a Modify button, and report invalid edits via an error popup. No auth changes beyond the existing token.

**Questions for you:**
1. Which fields should be editable via REST/web (mirror `wr mod`: reqs, env, priority, retries, limit groups, behaviours, ...)?
   - all the fields the web UI shows when looking at a job
2. Validation / error-reporting model for invalid edits?
   - popup with error messages
3. Fold #19 into this spec, or keep them separate?
   - fold
4. Any auth considerations beyond the existing token?
   - no

**Why still complex:** large multi-layer feature (new REST verb + server validation + a full editing UX in the web interface across many fields). Spec it as a project; #19 is delivered as part of it.

---

## #98 "Live" job introspection

- [x] Spec produced
- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

**Issue:** push live peak memory / CPU (and ideally stdout/stderr) for running jobs, and provide a quick way to ssh to where a job is running (or even a web terminal).

**Current knowledge:** live walltime and live state updates already exist; the runner `touch`es the manager periodically; job subscriptions were added (#503). What's missing is the live resource-usage push during a run and the ssh-to-job convenience.

**Suggested way forward:** extend the touch/subscription path to carry current peak RAM/CPU and a fixed-size (compressed) tail of stdout/err for in-flight jobs on each heartbeat, surface it live, and display the `ssh ... && cd ...` needed to reach a running job. Gate on https/auth being enabled.

**Questions for you:**
1. Which live metrics in v1 — peak RAM/CPU only, or also a live stdout/err tail?
   - peak RAM/CPU and most recent stdout/err tail from last heartbeat
2. Push frequency / payload-size limits (this rides on every touch)?
   - on every touch at current touch frequency, compressed fixed size tail of stdout/err implies a payload size limit
3. The ssh affordance — just display the command, or an embedded web terminal (much bigger, with security implications)?
   - just display command
4. Gate any of this on https/auth being enabled?
   - yes

**Why still complex:** touches the runner (gather + cap + compress the tail and resource stats on each touch), the wire/touch protocol and subscription delivery, the server, and the web UI display — a multi-layer change worth a spec.

---

## #19 Cmd env vars and expected resource requirements should be editable (web)

- [x] Spec produced
- [x] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

**Issue:** make buried/delayed (and other non-running incomplete) commands editable from the web status page — override env vars and resource requirements (memory/time/cpu).

**Current knowledge:** the backend capability exists via `wr mod`; the web UI has no editing. This is essentially the web-editing subset of #197.

**Suggested way forward:** **folded into #197** (per the answers) — deliver web editing of every displayed field for any non-running incomplete job as part of that spec, rather than as a separate slice.

**Questions for you:**
1. Fold this into #197's spec, or deliver it as a focused first slice (just env + memory/time/cpu)?
   - fold
2. Which job states are editable — buried/delayed only, or any non-running incomplete job?
   - any non-running incomplete
3. Which fields in v1?
   - all currently displayed

**Status:** folded into #197 — kept here only to record the decision; no separate work item.
