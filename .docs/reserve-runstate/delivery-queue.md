# Delivery queue

Owner-approved order (2026-10-06). One branch at a time; one subagent at a
time. Update at every state change.

| # | Branch | PR | Target | Depends on | Status | Waits on | Next action |
|---|--------|----|--------|------------|--------|----------|-------------|
| 1 | kickorder-5e1122fd | #683 | develop | - | merged 2026-10-06 (72384e4c) | - | done; worktree and branches removed |
| 2 | reserve-runstate-2f00c336 | - | develop | #683 | phases 1-5 done; gates 6.1, 6.2 passed; 6.3 soaks FAILED criterion 2 (change 1.39% vs baseline 3.79% non-durable, bar 0.034%) and neither reached scale (peaks 4511/3920) | owner decision after investigation | investigate what holds the write lock/slows commits during the change soak's non-durable bursts (from $G/soak-change and $G/analysis logs); report findings and options; no re-runs or code until owner decides |
| 3 | (new) kick changes caller's *Job | - | develop | #2 merged | queued | #2 | bugfix workflow: red test, fix (kickJobs changes the *Job its caller passed, not the item's data; a modify that replaced the item's *Job leaves the kick changing the old object; pre-existing) |
| 4 | (none yet) racPending shared bool | - | - | #3 | queued, needs owner design decision | #3 | investigate and present options with a recommendation; no code until the owner chooses (see .docs/bugfixes/261006-kick-reservable-race-8213f1ec.md, third item's deferred note) |
| 5 | (new) clean-stop double run | - | develop | #2 | queued (owner approved 2026-10-07) | #2 | bugfix workflow, red test first: at a scheduled clean stop a job exited 0 just as the stop's kill reached its runner, so its completion was never reported and it ran again after restart (1 in the change soak, 2 in the baseline; pre-existing on develop; details in pr-notes.md "Production-scale soaks (item 6.3)") |
