# Delivery queue

Owner-approved order (2026-10-06). One branch at a time; one subagent at a
time. Update at every state change.

| # | Branch | PR | Target | Depends on | Status | Waits on | Next action |
|---|--------|----|--------|------------|--------|----------|-------------|
| 1 | kickorder-5e1122fd | #683 | develop | - | merged 2026-10-06 (72384e4c) | - | done; worktree and branches removed |
| 2 | reserve-runstate-2f00c336 | - | develop | #683 | phases 1-5 done; gates 6.1, 6.2 passed; 6.3 soaks ran and passed under the revised F3 (change 1.39% vs baseline 3.79% non-durable; peaks 4511/3920); review follow-ups cleared; spec-aware whole-branch review in progress | - | finish spec-aware and spec-free reviews; re-run make test and make race at the final head; push; PR; pr-resolver |
| 3 | (new) kick changes caller's *Job | - | develop | #2 merged | queued | #2 | bugfix workflow: red test, fix (kickJobs changes the *Job its caller passed, not the item's data; a modify that replaced the item's *Job leaves the kick changing the old object; pre-existing) |
| 4 | (none yet) racPending shared bool | - | - | #3 | queued, needs owner design decision | #3 | investigate and present options with a recommendation; no code until the owner chooses (see .docs/bugfixes/261006-kick-reservable-race-8213f1ec.md, third item's deferred note) |
| 5 | (new) clean-stop double run | - | develop | #2 | queued (owner approved 2026-10-07) | #2 | bugfix workflow, red test first: a job that exits 0 around a scheduled clean stop must have its completion reported, or it runs again after restart (1 in the change soak, 2 in the baseline; pre-existing on develop). In the baseline each run exited 0 just as the stop's kill reached its runner. In the change soak the run exited 0 10s before the stop began, then the runner waited 61s for its resource-checking goroutine (jobqueue/client.go about 3880-4004) and aborted on the stop's signal before reporting. The red tests cover both: a completion racing the stop's kill, and a completion report held up by the resource-check wait. Details in pr-notes.md "Production-scale soaks (item 6.3)" |
| 6 | (new) per-writer write-lock stats | - | develop | #2 | queued (owner approved 2026-10-07) | #2 | log per-writer lock use (best-effort drains, add transactions, archive folds) and reservation wait p50/p99/max, like the archive fold line; see runstate-gate/analysis/bursts-findings.md option C |
| 7 | (new) writer priority and transaction caps | - | develop | #6 | queued, needs a spec (owner approved 2026-10-07) | #6 | spec-writer: best-effort writer priority plus capped archive-fold and add-chunk sizes, designed against a re-fitted model with the soak's add and backup load (bursts-findings.md option B) |
