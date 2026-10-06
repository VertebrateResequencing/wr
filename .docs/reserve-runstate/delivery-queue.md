# Delivery queue

Owner-approved order (2026-10-06). One branch at a time; one subagent at a
time. Update at every state change.

| # | Branch | PR | Target | Depends on | Status | Waits on | Next action |
|---|--------|----|--------|------------|--------|----------|-------------|
| 1 | kickorder-5e1122fd | #683 | develop | - | merged 2026-10-06 (72384e4c) | - | done; worktree and branches removed |
| 2 | reserve-runstate-2f00c336 | - | develop | #683 | phases 1-5 done and rebased onto develop 72384e4c; kick write-order test added; phase 6 (gates, soaks) in progress | item 6.1 local gates | 6.1 local gates, 6.2 wrdev modes and sweep, 6.3 baseline and change soaks; clear pr-notes.md follow-ups; PR |
| 3 | (new) kick changes caller's *Job | - | develop | #2 merged | queued | #2 | bugfix workflow: red test, fix (kickJobs changes the *Job its caller passed, not the item's data; a modify that replaced the item's *Job leaves the kick changing the old object; pre-existing) |
| 4 | (none yet) racPending shared bool | - | - | #3 | queued, needs owner design decision | #3 | investigate and present options with a recommendation; no code until the owner chooses (see .docs/bugfixes/261006-kick-reservable-race-8213f1ec.md, third item's deferred note) |
