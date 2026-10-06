# Delivery queue

Owner-approved order (2026-10-06). One branch at a time; one subagent at a
time. Update at every state change.

| # | Branch | PR | Target | Depends on | Status | Waits on | Next action |
|---|--------|----|--------|------------|--------|----------|-------------|
| 1 | kickorder-5e1122fd | #683 | develop | - | merged 2026-10-06 (72384e4c) | - | done; worktree and branches removed |
| 2 | reserve-runstate-2f00c336 | - | develop | #683 | phase 5 in progress (5.1-5.3 reviewed, 5.4 in review, 5.5 to do), then phase 6 gates and soaks | item 5.4 review | rebase onto develop 72384e4c; add the kick write-order test (pr-notes.md); finish 5.4, 5.5, phase 6; clear pr-notes.md follow-ups; PR |
| 3 | (new) kick changes caller's *Job | - | develop | #2 merged | queued | #2 | bugfix workflow: red test, fix (kickJobs changes the *Job its caller passed, not the item's data; a modify that replaced the item's *Job leaves the kick changing the old object; pre-existing) |
| 4 | (none yet) racPending shared bool | - | - | #3 | queued, needs owner design decision | #3 | investigate and present options with a recommendation; no code until the owner chooses (see .docs/bugfixes/261006-kick-reservable-race-8213f1ec.md, third item's deferred note) |
