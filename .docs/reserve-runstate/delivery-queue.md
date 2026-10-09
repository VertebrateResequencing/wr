# Delivery queue

Owner-approved order (2026-10-06); on 2026-10-08 the owner asked to finish this queue for a final bolt release, excluding non-critical bolt-specific items, before the customdb work. Delivery order now: 3, 5, 8, then 4 (owner chose a comment-only change), then 9; 10 after the release. One branch at a time; one subagent at a
time. Update at every state change.

| # | Branch | PR | Target | Depends on | Status | Waits on | Next action |
|---|--------|----|--------|------------|--------|----------|-------------|
| 1 | kickorder-5e1122fd | #683 | develop | - | merged 2026-10-06 (72384e4c) | - | done; worktree and branches removed |
| 2 | reserve-runstate-2f00c336 | #684 | develop | #683 | merged 2026-10-07 (9fc0d795) | - | done; worktree and branches removed |
| 3 | kickjob-66783567 | #685 | develop | - | merged 2026-10-08 (2dda10c8) | - | done; worktree and branches removed |
| 4 | racdoc-41b80cca | #688 | develop | #3 | merged 2026-10-08 (ae704cbe); comment-only (owner chose option A) | - | done; worktree and branches removed |
| 5 | cleanstop-6fbb5bf1 | #686 | develop | #2 | merged 2026-10-08 (f89d28d5) | - | done; worktree and branches removed |
| 6 | (new) per-writer write-lock stats | - | develop | #2 | dropped for the final bolt release (owner 2026-10-08; superseded by the customdb route, branch customdb-f397b547) | #2 | log per-writer lock use (best-effort drains, add transactions, archive folds) and reservation wait p50/p99/max, like the archive fold line; see runstate-gate/analysis/bursts-findings.md option C |
| 7 | (new) writer priority and transaction caps | - | develop | #6 | dropped for the final bolt release (owner 2026-10-08; superseded by the customdb route, branch customdb-f397b547) | #6 | spec-writer: best-effort writer priority plus capped archive-fold and add-chunk sizes, designed against a re-fitted model with the soak's add and backup load (bursts-findings.md option B) |
| 8 | statusdelta-b6642140 | #687 | develop | #3 | merged 2026-10-08 (bad1bc32) | - | done; worktree and branches removed |
| 9 | soakgate-5c0eec66 | #689 | develop | - | merged 2026-10-09 (6fcbf4ed) | - | done; worktree and branches removed |
| 10 | (new, post-release) remove the global reserve hold (option D) | - | develop | #4 | queued after the bolt release (owner 2026-10-08) | bolt release | spec-writer; red test from the racPending probe (P1 and P2) and the unreproduced released-for-RAM gap in racpending-options.md |
| 11 | (new) Execute drops its own kill/close/behaviour/unmount errors | - | develop | - | queued after the bolt release (owner 2026-10-08); found during #9's review; pre-existing (before #571) | bolt release | jobqueue/client.go Execute builds up kill, close, behaviour and unmount errors in myerr, then `myerr = outcome.myerr` overwrites them for every failed command (and for a successful one when unmount doesn't force a release), so "behaviour(s) also had problem(s)" and similar never reach the caller or runner log; no test asserts them. Confirm whether intentional; if not, bugfix with a red test |
| 12 | seedoverlap-6f57a39c (worktree ../wr-seedoverlap) | - | develop | - | found by the 0.39.0 release sweep on 6fcbf4ed; test and tooling only, not a release blocker | PR, CI, Copilot | push, open PR, pr-resolver, then ask owner to merge |
