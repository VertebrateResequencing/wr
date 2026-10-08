# Delivery queue

Owner-approved order (2026-10-06); on 2026-10-08 the owner asked to finish this queue for a final bolt release, excluding non-critical bolt-specific items, before the customdb work. Delivery order now: 3, 5, 8, then 4 (investigation and options), then 9. One branch at a time; one subagent at a
time. Update at every state change.

| # | Branch | PR | Target | Depends on | Status | Waits on | Next action |
|---|--------|----|--------|------------|--------|----------|-------------|
| 1 | kickorder-5e1122fd | #683 | develop | - | merged 2026-10-06 (72384e4c) | - | done; worktree and branches removed |
| 2 | reserve-runstate-2f00c336 | #684 | develop | #683 | merged 2026-10-07 (9fc0d795) | - | done; worktree and branches removed |
| 3 | kickjob-66783567 | #685 | develop | - | merged 2026-10-08 (2dda10c8) | - | done; worktree and branches removed |
| 4 | (none yet) racPending shared bool | - | - | #3 | queued, needs owner design decision | #3 | investigate and present options with a recommendation; no code until the owner chooses (see .docs/bugfixes/261006-kick-reservable-race-8213f1ec.md, third item's deferred note) |
| 5 | cleanstop-6fbb5bf1 | #686 | develop | #2 | merged 2026-10-08 (f89d28d5) | - | done; worktree and branches removed |
| 6 | (new) per-writer write-lock stats | - | develop | #2 | dropped for the final bolt release (owner 2026-10-08; superseded by the customdb route, branch customdb-f397b547) | #2 | log per-writer lock use (best-effort drains, add transactions, archive folds) and reservation wait p50/p99/max, like the archive fold line; see runstate-gate/analysis/bursts-findings.md option C |
| 7 | (new) writer priority and transaction caps | - | develop | #6 | dropped for the final bolt release (owner 2026-10-08; superseded by the customdb route, branch customdb-f397b547) | #6 | spec-writer: best-effort writer priority plus capped archive-fold and add-chunk sizes, designed against a re-fitted model with the soak's add and backup load (bursts-findings.md option B) |
| 8 | statusdelta-b6642140 (worktree ../wr-statusdelta) | - | develop | #3 | fixes committed (owner chose option A: per-queue change sequence); pushing | PR, CI, Copilot | push, open PR, pr-resolver, ask owner to merge |
| 9 | (new) soakgate killed-run doubles | - | develop | - | queued (owner 2026-10-08: separate tooling branch, after release-critical items) | #5 | soakgate.py should not count a first run as a double when its runner logged "killed by user request" or the job was buried before its next run (the soak job script records exit 0 before exiting, so a stop's kill can land between); test, then run the tool end to end on the existing soak evidence |
