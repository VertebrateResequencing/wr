# wr open issues — triage

Independent categorisation of all 70 open issues in
[VertebrateResequencing/wr](https://github.com/VertebrateResequencing/wr/issues)
into five lists.

Method: read every issue (body + comments), the full CHANGELOG (up to v0.36.5)
and the recent post-changelog commits (#503 job subscriptions, #504 improve
client, #505 pending/dependent status filters), and grepped the current code to
confirm what is/isn't implemented. Categories are judged from the perspective of
a coding agent working in this repo with no LSF / OpenStack / AWS / GCP / real-S3
environment.

## Can't Fix

Need an environment/credentials I don't have (LSF, OpenStack, public cloud, S3 at
scale), depend on an external repo (e.g. muxfys), or are too open-ended/external
to complete and verify here.

* [#17 Cloud deployment should work from MacOS -> linux](https://github.com/VertebrateResequencing/wr/issues/17) — the fix (bundle/download a Linux binary for the OpenStack head node) can only be validated by actually doing a cloud deploy, which needs OpenStack.
* [#22 Long-running commands should have the option of being checkpointed](https://github.com/VertebrateResequencing/wr/issues/22) — requires specialised checkpoint/restart infrastructure (links a dead internal Sanger wiki); not implementable or verifiable here.
* [#36 Alter cloud deployment max servers while live](https://github.com/VertebrateResequencing/wr/issues/36) — an OpenStack manager/scheduler change that can only be tested against OpenStack.
* [#37 Workflows: add CWL support](https://github.com/VertebrateResequencing/wr/issues/37) — a full CWL runner is a huge feature requiring external conformance suites and depends on a separate WIP cwl.go; not realistically tackleable/verifiable here.
* [#53 Cloud schedulers: better/no server reuse](https://github.com/VertebrateResequencing/wr/issues/53) — OpenStack-specific; the maintainer pushed back on the design and it needs OpenStack to implement/test.
* [#54 Feature - OpenStack volume passing between dependant jobs](https://github.com/VertebrateResequencing/wr/issues/54) — OpenStack block-storage feature; needs OpenStack.
* [#66 OpenStack - additional storage should be mounted as additional volume](https://github.com/VertebrateResequencing/wr/issues/66) — OpenStack block-device-mapping work; needs OpenStack.
* [#72 Openstack failed job should retry on a different host](https://github.com/VertebrateResequencing/wr/issues/72) — only meaningful/verifiable with multiple cloud hosts (OpenStack).
* [#73 OpenStack: Watch host resources to identify lockups](https://github.com/VertebrateResequencing/wr/issues/73) — cloud host monitoring; needs OpenStack.
* [#92 Add a website front-page to readthedocs](https://github.com/VertebrateResequencing/wr/issues/92) — the docs site exists; the remainder (nice front page, buying a domain, a video course) is external/non-code and lives in another repo.
* [#117 Task Execution Service - GA4GH APIs](https://github.com/VertebrateResequencing/wr/issues/117) — large; depends on file staging (#284) and external conformance testing, with no confirmed consumer.
* [#283 Refactor codebase](https://github.com/VertebrateResequencing/wr/issues/283) — an unbounded aspirational goal (100% coverage + full SOLID + mock integration tests for the whole codebase); not a discrete fixable task, though incremental refactoring is clearly ongoing.
* [#285 Cleanup for failed mounted jobs should delete output files](https://github.com/VertebrateResequencing/wr/issues/285) — depends on muxfys exposing which files were written, and needs S3 to implement/verify.
* [#292 Update OpenStack implementation with latest gophercloud abilities](https://github.com/VertebrateResequencing/wr/issues/292) — can only confirm the updated implementation works against OpenStack.
* [#299 Test manager resilience in cloud deployments](https://github.com/VertebrateResequencing/wr/issues/299) — explicitly author-only OpenStack tests.
* [#314 Strange db performance issue, disk dependant](https://github.com/VertebrateResequencing/wr/issues/314) — the author's own investigation points to LSF job-array switching plus slow disks; pursuing it needs LSF and specific hardware (workaround: db on a fast disk).
* [#315 Cloud deploy doesn't work with a cloudforms server?](https://github.com/VertebrateResequencing/wr/issues/315) — needs that specific cloud/cloudforms environment to reproduce.
* [#318 Warning deleting mounted files we didn't read or create](https://github.com/VertebrateResequencing/wr/issues/318) — a muxfys (external dependency) bug; needs a writable S3 mount.
* [#319 Reading files from mount can cause cmds to segfault](https://github.com/VertebrateResequencing/wr/issues/319) — likely a muxfys read bug; needs S3 and large files to reproduce.
* [#321 Jobs fail if too many need mounts at once](https://github.com/VertebrateResequencing/wr/issues/321) — per-user fuse limits at cloud scale; needs cloud + S3.
* [#323 Chunk reads from mount needs improving](https://github.com/VertebrateResequencing/wr/issues/323) — a muxfys range-read enhancement; needs S3.
* [#327 SSH to trusty servers has network timeout issues](https://github.com/VertebrateResequencing/wr/issues/327) — needs OpenStack with an (EOL) trusty image; already worked around by using bionic.
* [#328 Lost jobs when using singularity/s3, high runner cpu usage](https://github.com/VertebrateResequencing/wr/issues/328) — reproducing the reported lost-jobs behaviour needs cloud + singularity + S3 (the stdout-processing-causes-high-CPU sub-finding could be looked at separately, but the core needs the full stack).
* [#329 High memory usage when writing files to mount](https://github.com/VertebrateResequencing/wr/issues/329) — muxfys/S3 memory behaviour; needs S3.
* [#330 Runners failing due to "failed to create new OS thread"](https://github.com/VertebrateResequencing/wr/issues/330) — root cause is an OS thread/ulimit limit the author deems unfixable; a mitigation already shipped (v0.21.0 caps 0-core jobs at 2× cores), and the residual scheduler lockups need specific large-core cloud hosts to reproduce.
* [#382 Add native support for public clouds such as AWS and GCP](https://github.com/VertebrateResequencing/wr/issues/382) — needs AWS/GCP accounts and credentials.

## Invalid

Premise no longer holds, superseded, or user error.

* [#39 OpenStack scheduler: spawned servers only need 2 ports](https://github.com/VertebrateResequencing/wr/issues/39) — the author's own follow-up establishes the premise (port quota) was a misunderstanding and the change is "not necessary"; the residual security nicety would need OpenStack anyway.
* [#192 Write Nextflow support](https://github.com/VertebrateResequencing/wr/issues/192) — the author notes it's "not strictly an issue for this repository" (a Nextflow executor lives in Nextflow's codebase), and wr already supports being a Nextflow backend via its LSF/bsub emulation (added v0.13.0).
* [#400 --limit option for the wr status command is not ducumented](https://github.com/VertebrateResequencing/wr/issues/400) — it *is* documented in `wr status -h` (the maintainer quoted the help text); reads as the reporter not checking `-h`, with no follow-up.

## Solved

Already addressed by code changes since the issue was filed.

* [#15 Tests needed for WebI and Cmd](https://github.com/VertebrateResequencing/wr/issues/15) — the command line now has tests (cmd/add_test.go, cmd/status_test.go) and the web/REST server is tested (jobqueue/rest_test.go, subscription_test.go); the original "no tests at all" state is resolved (coverage still isn't exhaustive, and the frontend JS isn't unit-tested).
* [#16 Implement bash completion](https://github.com/VertebrateResequencing/wr/issues/16) — `wr completion bash` is provided (via cobra) and documented since v0.29.0.
* [#25 Status: bring up past commands and add paging](https://github.com/VertebrateResequencing/wr/issues/25) — the v0.36.0 web overhaul added repgroup search including completed jobs and "load more" paging; the CLI has `--limit`.
* [#291 Disk space learning should ignore writes inside mounts](https://github.com/VertebrateResequencing/wr/issues/291) — jobqueue/client.go measures disk only for the unique cwd's unmounted parts and the mount cache dirs, not data written through the mount (the "report as two separate numbers" sub-ask may still be open).

## Todo

Look like current gaps/problems with a clear, locally-tractable code change.

* [#19 Cmd env vars and expected resource requirements should be editable](https://github.com/VertebrateResequencing/wr/issues/19) — the underlying capability exists via `wr mod`; the remaining piece is the web editing UI (overlaps #197).
* [#20 Status webpage: add rerun button](https://github.com/VertebrateResequencing/wr/issues/20) — the web UI has retry/remove/kill actions but no rerun-completed action (action-handlers.js).
* [#28 Dependencies: choose to make them un-"live"](https://github.com/VertebrateResequencing/wr/issues/28) — a bounded jobqueue dependency-handling feature, implementable and testable locally.
* [#31 Resource Token system](https://github.com/VertebrateResequencing/wr/issues/31) — the urgent need is met by limit groups; the `rp` package is a started foundation for the fuller system (cross-manager, timed/delayed), which is tractable to continue locally.
* [#98 "Live" job introspection](https://github.com/VertebrateResequencing/wr/issues/98) — live walltime/state already update; pushing live peak RAM/CPU via the existing touch cycle and surfacing ssh-to-job details are bounded additions.
* [#194 wr mod: allow modification of dep groups and bsub mode](https://github.com/VertebrateResequencing/wr/issues/194) — explicitly stubbed-out TODOs in cmd/mod.go (dep_grps "complex; not done", bsub "not sure if it makes sense").
* [#197 Allow job modification using web/REST interfaces](https://github.com/VertebrateResequencing/wr/issues/197) — the REST API exposes only GET/POST/DELETE on jobs and the web UI has no modify action; both need adding.
* [#207 Allow to suspend and resume non-running jobs](https://github.com/VertebrateResequencing/wr/issues/207) — no suspend feature exists (limit-group workaround aside); a bounded new state + commands.
* [#251 Implement log rotation](https://github.com/VertebrateResequencing/wr/issues/251) — not implemented; a self-contained change to the clog/logging setup.
* [#284 File Staging](https://github.com/VertebrateResequencing/wr/issues/284) — well-specified in the issue; the shared-filesystem (symlink) path is implementable and testable locally even without S3.
* [#287 Extend LSF emulation](https://github.com/VertebrateResequencing/wr/issues/287) — `wr bsub -h` confirms it's still interactive-console-only with limited flags; extending the parser to accept args+cmd is wr's own code.
* [#288 Show scheduler issues on the command line](https://github.com/VertebrateResequencing/wr/issues/288) — the data already drives the web warnings; surfacing it via `wr status`/a `wr issues` command is bounded.
* [#290 Improve efficiency of client methods](https://github.com/VertebrateResequencing/wr/issues/290) — concrete: check Archive() etc. don't serialise/send unnecessary fields, send only the key.
* [#296 Update or find alternative to go-daemon](https://github.com/VertebrateResequencing/wr/issues/296) — try a newer version; static build / darwin cross-compile is testable locally.
* [#297 Make binary smaller?](https://github.com/VertebrateResequencing/wr/issues/297) — apply `-ldflags="-s -w"`/upx and measure; purely local.
* [#301 Update error handling](https://github.com/VertebrateResequencing/wr/issues/301) — mechanical modernisation to errors.Is/As and wrapping.
* [#316 Unexpected dependency behaviour](https://github.com/VertebrateResequencing/wr/issues/316) — improve `--deps` help text and/or warn when it names a dep group that doesn't exist yet; small and local.
* [#326 Adding a dependent job with --rerun results in unexpected behaviour](https://github.com/VertebrateResequencing/wr/issues/326) — the crash/resurrection is already fixed per the issue; the remaining ask is a specific behaviour tweak (use the newly added job rather than calling it a duplicate when `--rerun`).
* [#502 Distinguish between command killed due to high memory usage and failed for another reason](https://github.com/VertebrateResequencing/wr/issues/502) — localisable to the FailReasonRAM/ranoutMem logic; recent and acknowledged by the maintainer.
* [#506 Table-like report mode for wr status](https://github.com/VertebrateResequencing/wr/issues/506) — add a new output mode to cmd/status.go (currently counts/summary/details/json/plain).
* [#507 Option to only add the first x command in a file as a test](https://github.com/VertebrateResequencing/wr/issues/507) — add a `--head N` flag wired into parseCmdFile in cmd/add.go.

## Investigate

Would need to reproduce against the current code to know whether they're still
problems.

* [#12 Status webpage: refreshing while running causes problems](https://github.com/VertebrateResequencing/wr/issues/12) — a 2017 bug; the web UI has since been rewritten (now has an inflight-tracking.js specifically for count accuracy); re-test with live jobs.
* [#45 Status webpage: sometimes jobs appear pending when they aren't](https://github.com/VertebrateResequencing/wr/issues/45) — same era/class as #12; re-test in the rewritten UI.
* [#286 Impossible req warnings should appear in status](https://github.com/VertebrateResequencing/wr/issues/286) — "check that..."; the local-scheduler case is reproducible (add an impossible-req job and inspect `wr status`).
* [#295 Use build tag osusergo?](https://github.com/VertebrateResequencing/wr/issues/295) — evaluate whether the build tag actually benefits wr (a question, with one supporting use case in comments).
* [#302 Switch from gorilla to Gobwas?](https://github.com/VertebrateResequencing/wr/issues/302) — a websocket-library perf experiment; measure before deciding.
* [#303 Improve allocation efficiency?](https://github.com/VertebrateResequencing/wr/issues/303) — the author already found little to do; would re-profile the add/mod paths.
* [#309 Odd failure and status reporting of an --on_failure cmd](https://github.com/VertebrateResequencing/wr/issues/309) — reproduce the on_failure failure; the "behaviours button showed another job's commands" half may already be fixed by the web rewrite.
* [#310 Status webpage: times out](https://github.com/VertebrateResequencing/wr/issues/310) — re-test reconnect behaviour in the rewritten UI (now has a websocket-handler).
* [#313 Is there a db-backup related speed bottleneck?](https://github.com/VertebrateResequencing/wr/issues/313) — reproducible locally by adding many jobs and timing with/without backups.
* [#317 status webpage: lost contact jobs show wrong buttons](https://github.com/VertebrateResequencing/wr/issues/317) — re-test in the rewritten UI.
* [#322 wr status: can be unexpectedly slow](https://github.com/VertebrateResequencing/wr/issues/322) — reproducible locally by adding ~185k jobs and timing status.
* [#324 Strange start/end time issue with quick-running dependent jobs](https://github.com/VertebrateResequencing/wr/issues/324) — reproduce; the identical start==end timestamps look like a real reporting bug to confirm.
* [#333 wr mod: too slow on lots of jobs](https://github.com/VertebrateResequencing/wr/issues/333) — reproducible locally (add ~11k jobs and mod them); related to #290.
* [#444 Wrong Cwd reported in status while running](https://github.com/VertebrateResequencing/wr/issues/444) — specific (last 10 chars wrong); reproduce to find the code path that builds the running-job cwd.
* [#448 Limits not always working?](https://github.com/VertebrateResequencing/wr/issues/448) — the maintainer found lost jobs consume the limit while still "running"; reproduce and decide whether lost-job limit accounting should change.
* [#453 wr manager in cloud mode fails to update stats of reserved cpus when OS image is not available](https://github.com/VertebrateResequencing/wr/issues/453) — code-localisable in the OpenStack scheduler's reserved-resource accounting on spawn failure (reading the code may find it), though full verification needs OpenStack.
