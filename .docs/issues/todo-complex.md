# Complex Todos

Items that are substantial enough to deserve a short spec and/or a decision from you
before implementation. Each has its own section and its own checklist; the
**Questions for you** lines are the things I'd want answered before (or as part of)
producing the spec, because they're matters of choice about how the feature should
behave.

---

## #506 Table-like report mode for wr status

- [ ] Spec produced
- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

**Issue:** Users want a columnar `wr status` output (like `bjobs`), e.g.
`Command | ID | Status | Attempts | Host | Req group | count`, ideally with
user-specified columns and widths in the style of LSF's `LSB_BJOBS_FORMAT`
(`JOBID:10 STAT:5 ...`). Like normal status it would show a representative one of each
same-status group.

**Current knowledge:** `cmd/status.go` supports `-o` modes `counts`/`summary`/`details`/`json`/`plain` (flag defined ~`cmd/status.go:504`); there is no table mode. The data needed per row is already in the status structs.

**Suggested way forward:** add `-o table` (alias `t`) producing a default column set via a small aligned table writer; optionally support a `--format` string (or env var) for custom columns + widths.

**Questions for you:**
1. v1 = a fixed, sensible default column set, or full configurable columns+widths from the start?
   - configurable with WR_STATUS_FORMAT
2. Which columns by default?
   - as requested in the issue
3. Format syntax for custom columns — LSF-style `FIELD:width ...`, or Go-template, or a simple comma list?
   - LSF-style
4. CLI only, or mirror it in the web UI too?
   - CLI only

---

## #502 Distinguish "killed for high memory" from "failed for another reason but used more memory than expected"

- [ ] Spec produced
- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

**Issue:** A job that fails for an unrelated reason but happens to exceed its expected peak RAM is reported as `FailReasonRAM` ("command used too much RAM"), masking the real cause.

**Current knowledge:** the runner sets `FailReasonRAM` / `ranoutMem` whenever peak memory exceeded the estimate; wr itself also kills jobs that exceed memory. You noted on the issue that "we can only take an educated guess." The key is that wr *knows* when it deliberately killed a job for memory, versus when the process died on its own.

**Suggested way forward:** only attribute the failure to RAM when wr actually killed the job for exceeding memory (track that decision in the runner); otherwise report the underlying exit/fail reason, optionally appending a clearly non-authoritative note that peak memory also exceeded the estimate.

**Questions for you:**
1. When a job fails for another reason **and** exceeded expected memory, what should status show — the real reason plus a "note: peak memory also exceeded the estimate" addendum, or a brand-new distinct FailReason?
   - I believe there is a mechanism that based on the FailReason the job gets rescheduled with more expected memory. That must always happen if peak memory exceeded the expected. If some other reason is known, figure out the simplest way of also giving it to the user.
2. Is detecting external OOM-killer kills (SIGKILL / exit 137, possibly via dmesg) in scope, or only wr-initiated memory kills?
   - external kills is the main scope; wr-initiated kills almost never happen in real life
3. Exact wording of the clarified message(s)?
   - if the other reason already has a message, just list/concatenate the messages

---

## #333 wr mod: too slow on lots of jobs

- [ ] Spec produced
- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

**Issue:** `wr mod` on ~11,705 jobs completes server-side but the client times out.

**Current knowledge (investigated):** `jobqueue/db.go modifyLiveJobs` deletes each modified job's lookup entries by `ForEach` over the **entire** `bucketRTK`/`bucketDTK`/`bucketRDTK` buckets, once per modified job → O(jobs_modified × total_lookup_entries). The maintainer left a `// *** ... will have to implement a reverse lookup ...` comment right there. The whole operation runs synchronously inside one paused `jmod` request under a single client deadline (default 120s, `cmd/mod.go`).

**Suggested way forward:** add a reverse-lookup index (job key → its lookup-bucket entries) so deletion becomes O(entries-per-job); maintain it on add/modify/delete. Optionally also raise the default `wr mod` timeout as a complementary mitigation. Add a benchmark/regression test.

**Questions for you:**
1. OK to add a new boltdb bucket and maintain it across all mutation paths, rebuilding it on first load for pre-existing DBs that lack it?
   - yes
2. Do you also want the client timeout raised, or rely solely on the speedup?
   - soley speedup

---

## #326 Adding a dependent job with --rerun results in unexpected behaviour

- [ ] Spec produced
- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

**Issue:** The original crash/resurrection is already fixed, but the remaining ask is: when `--rerun` re-adds a dependent job that duplicates a previously-completed one, wr should use the newly added job rather than treating it as a duplicate.

**Current knowledge:** job uniqueness is by `Job.Key()` (cmd/cwd/mounts/image). The desired end behaviour is stated in the issue, but the edge cases (especially around dependency chains) aren't pinned down; needs a fresh reproduction against the current add/rerun path.

**Suggested way forward:** reproduce, then make the add path, under `--rerun`, reactivate/replace the completed job (and its dependents) instead of skipping it as a duplicate.

**Questions for you:**
1. When a dependent job is rerun, should its downstream dependents also rerun automatically?
   - no
2. Semantics of "use the newly added job" — reactivate the existing record in place, or remove and re-add?
   - not sure what you mean, but I believe the thing we're trying to fix here is that --rerun jobs should always rerun, but if they're dependent they should wait on currently incomplete dependencies just as they would have the first time.
3. Should `--rerun` semantics be defined for a whole dependency tree, or only the explicitly re-added jobs?
   - only explicit

---

## #322 wr status: can be unexpectedly slow

- [ ] Spec produced
- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

**Issue:** `wr status -i <substr> -z -o c` (counts only) is slow with ~185k complete jobs.

**Current knowledge (investigated):** for `-o c` the count is computed client-side after the server decodes **all** matching complete `Job` structs from boltdb and ships them over the wire (`jobqueue/db.go retrieveCompleteJobsByRepGroup` → `getJobsByRepGroup` → `limitJobs`). v0.36.4's server-side rep-group filtering only narrows which groups match; it added no count-only path. So producing ~7 integers costs O(n) decode + transfer.

**Suggested way forward:** add a server handler (and client method) that returns per-state counts for a repgroup match without decoding/returning full jobs, mirroring the WebI `stateCounts` approach; wire `-o c` (and possibly `-o summary`) to it.

**Questions for you:**
1. Compute counts on demand by iterating keys, or maintain persistent per-repgroup/state counters?
   - if on-demand count will be fast enough stick with that, only maintain counters if we can be sure they won't get out of sync
2. Acceptable to add a new request type to the client/server protocol for this?
   - yes
3. Should `-o summary` reuse the same fast path?
   - yes

---

## #316 Unexpected dependency behaviour

- [ ] Spec produced
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

---

## #290 Improve efficiency of client methods

- [ ] Spec produced
- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

**Issue:** client methods may send more data than the server needs — e.g. does `Archive()` encode and send the whole job (including `EnvC`) when the server only considers the job's key?

**Current knowledge:** needs a current audit — the recent #504 "Improve client" reworked `client/client.go` and `jobqueue/job.go`, so part of this may already be addressed; the remaining over-sending should be enumerated against what each server handler actually reads.

**Suggested way forward:** audit the client→server methods (Archive and friends), and trim each request to the minimal fields the server uses; add tests asserting payload shape where practical.

**Questions for you:**
1. Are there external go-API consumers we must stay wire-compatible with, or can we freely change the internal client/server protocol?
   - Only need to ensure existing client pkg public methods don't change behaviour
2. Scope — just the known offender(s) like `Archive()`, or a full audit of all client methods?
   - Full audit

---

## #288 Show scheduler issues on the command line

- [ ] Spec produced
- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

**Issue:** scheduler problems (can't create an OpenStack server, out of quota, etc.) appear in the web UI / REST warnings but not on the CLI. The issue suggests surfacing them in `wr status` or via a dedicated `wr issues` command.

**Current knowledge:** the data exists server-side and is exposed over REST (`restWarnings` / bad-servers endpoints) and in the web UI; the CLI client would need a method to fetch and render it.

**Suggested way forward:** add a client method to fetch current scheduler warnings / bad servers and surface them on the CLI.

**Questions for you:**
1. Integrate into `wr status` (header/footer section or a flag), a dedicated `wr issues` command, or both?
   - footer of `wr status`
2. What to include — scheduler warnings, bad/lost servers, reasons jobs are stuck pending?
   - all the things the web UI can alert about

---

## #251 Implement log rotation

- [ ] Spec produced
- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

**Issue:** the manager log file should be rotated and size-limited, user-configurable.

**Current knowledge:** the `clog` package writes to a configured log file with no rotation policy.

**Suggested way forward:** add size-based rotation (max size / number of backups / max age) and wire it into `clog`'s file writer with new config options.

**Questions for you:**
1. OK to add the de-facto-standard `gopkg.in/natefinch/lumberjack` dependency, or do you prefer a hand-rolled size-based rotator (you've historically kept dependencies minimal)?
   - lumberjack
2. Config option names and defaults (max size MB, max backups, max age, compress?)
   - WR_LOGS prefix, whatever is typical and expected when people use log rotation
3. Rotate only the manager log, or also the runner file logs (`--runner_filelog`)?
   - both

---

## #207 Allow to suspend and resume non-running jobs

- [ ] Spec produced
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

---

## #197 Allow job modification using web/REST interfaces

- [ ] Spec produced
- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

**Issue:** expose the modification that `wr mod` performs on the CLI via the web UI and the public REST API.

**Current knowledge:** REST currently supports only GET/POST/DELETE on jobs (no PUT/PATCH); the web UI has no modify action; the server-side modify capability already exists (used by `wr mod`). This overlaps heavily with #19 (the web-editing subset).

**Suggested way forward:** add a REST PUT/PATCH endpoint that applies `wr mod`-style changes, and add web UI editing (editable fields on eligible jobs with a Modify button). Likely specced together with #19.

**Questions for you:**
1. Which fields should be editable via REST/web (mirror `wr mod`: reqs, env, priority, retries, limit groups, behaviours, ...)?
   - all the fields the web UI shows when looking at a job
2. Validation / error-reporting model for invalid edits?
   - popup with error messages
3. Fold #19 into this spec, or keep them separate?
   - fold
4. Any auth considerations beyond the existing token?
   - no

---

## #98 "Live" job introspection

- [ ] Spec produced
- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

**Issue:** push live peak memory / CPU (and ideally stdout/stderr) for running jobs, and provide a quick way to ssh to where a job is running (or even a web terminal).

**Current knowledge:** live walltime and live state updates already exist; the runner `touch`es the manager periodically; job subscriptions were added (#503). What's missing is the live resource-usage push during a run and the ssh-to-job convenience.

**Suggested way forward:** extend the touch/subscription path to carry current peak RAM/CPU (and optionally a tail of stdout/err) for in-flight jobs and surface it live; add display of the `ssh ... && cd ...` needed to reach a running job.

**Questions for you:**
1. Which live metrics in v1 — peak RAM/CPU only, or also a live stdout/err tail?
   - peak RAM/CPU and most recent stdout/err tail from last heartbeat
2. Push frequency / payload-size limits (this rides on every touch)?
   - on every touch at current touch frequency, compressed fixed size tail of stdout/err implies a payload size limit
3. The ssh affordance — just display the command, or an embedded web terminal (much bigger, with security implications)?
   - just display command
4. Gate any of this on https/auth being enabled?
   - yes

---

## #20 Status webpage: add rerun button

- [ ] Spec produced
- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

**Issue:** add a "rerun" button to completed commands in the web status UI (today only buried jobs have a retry action).

**Current knowledge:** the web action-handlers cover retry (buried) / remove / kill / confirm-dead but not rerun-of-completed; the REST API has a `rerun` option when adding jobs, and wr intentionally ignores re-added completed jobs unless `--rerun`. So "rerun from the web" means re-submitting the command with rerun semantics. There is a design tension with the "completed jobs are ignored unless --rerun" model.

**Suggested way forward:** add a Rerun action on completed jobs that re-adds/reruns the command via the existing rerun mechanism.

**Questions for you:**
1. Exact behaviour — re-add the same command with `rerun=true` (a fresh run)? With a confirm dialog and what feedback?
   - fresh run, same sort of confirmation as when removing a job
2. Available for any completed job, or only ones present in the current view/search?
   - any completed job
3. Reuse the existing add+rerun, or add a dedicated REST action?
   - reuse

---

## #19 Cmd env vars and expected resource requirements should be editable (web)

- [ ] Spec produced
- [ ] Implemented
- [ ] Reviewed
- [ ] Merged
- [ ] Solved

**Issue:** make buried/delayed (and other non-running incomplete) commands editable from the web status page — override env vars and resource requirements (memory/time/cpu).

**Current knowledge:** the backend capability exists via `wr mod`; the web UI has no editing. This is essentially the web-editing subset of #197.

**Suggested way forward:** add inline editable fields (including pop-ups such as env vars) on eligible jobs, with a Modify button that calls the (new) modify endpoint — best specced together with #197.

**Questions for you:**
1. Fold this into #197's spec, or deliver it as a focused first slice (just env + memory/time/cpu)?
   - fold
2. Which job states are editable — buried/delayed only, or any non-running incomplete job?
   - any non-running incomplete
3. Which fields in v1?
   - all currently displayed
