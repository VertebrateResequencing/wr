# Nextflow tooling delivery

## Authorization

On 2026-09-30 the user instructed:

> Is there a reason for the stop? If not, your job is to ensure this proceeds
> through all 6 phases. Commit and push after each phase.

Continue through context handoffs without asking the user to restart work.
Commit each completed, reviewed phase as `Implement phase <N>` and push the
current `nextflowdsl` branch to `origin`. Preserve history and use normal
fast-forward pushes. The parent orchestrator verifies and pushes each phase
commit before starting the next phase.

## Delivery checklist

- [x] Phase 1 reviewed, committed, and pushed.
- [ ] Phase 2 reviewed, committed, and pushed.
- [ ] Phase 3 reviewed, committed, and pushed.
- [ ] Phase 4 reviewed, committed, and pushed.
- [ ] Phase 5 reviewed, committed, and pushed.
- [ ] Phase 6 reviewed, committed, and pushed.
- [ ] Two consecutive clean spec-aware branch reviews.
- [ ] Two consecutive clean spec-free branch reviews.

Phase checklists and their evidence remain authoritative for implementation
and review. This checklist records delivery. The six phases build assurance
tooling; they do not implement wr's Nextflow DSL2 runtime.

## Ownership

The outermost queue owner is `/root`; its branch is `nextflowdsl` and shared
worktree is `.` relative to the main clone. This file owns delivery and any
deferred issue references. There are no deferred issue entries on recovery.
Workers report incidental findings to the parent before changing their scope.
The accepted restart pins develop baseline
`f2888015559873b0aef386ada584a5fb2674e88e` and restoration commit `0add8846`.
The accepted prompt requires retaining history without force-pushing; this
task uses normal fast-forward delivery, even if remote develop advances.

## Current work

Historical Phase 1 was committed and pushed as
ec487ed27111e61dba7d104c2579ef053e478a24, with the remote tip verified at
delivery. The four-phase research pilot passed independent review for all five
bounded research UATs and was pushed through cad2b64d. These are their original
evidence; no later core phase is complete.

The research-informed foundation revision has completed the full spec-writer
workflow. It retains the original 49 assurance UATs and adds twenty independent-
suite UATs. Two feature coverage reviews, two clean proofreading reviews and
clean independent reviews of all six revised plans passed. The resulting plans
preserve separate
source/assertion/document/native/neutral/strengthening/gap/replay claims and
zero wr runtime passes. They require current input and execution evidence rather
than importing research status.

The parent owns phase checklists, progress.md, this delivery record, commits and
pushes. Phase 2 Item 2.1 is implemented but unreviewed. Schema review 06 accepts
F1-F10 and reports F11, an assertion gap while production UTF-8 rejection is
correct. Follow the explicit prerequisite exception: freshly measure and
independently approve Item 2.2 inputs, implement and review the eleven
malformed/valid string pairs and exact guard-removal fault, then obtain a fresh
complete Item 2.1 PASS. Item 2.3 and all later dependent work wait for those
passes.

Pre-research metadata, prior F11 planning and revision generations are preserved
under .tmp/agent/nextflow-conformance/. Current complete inputs need new
approval; an old hash-bound allocation is historical. Use coherent fresh
contexts around 100k tokens, including source, skills, fixtures, changed code
and output/growth/reasoning allowances.

D2 includes the commit and dirty digest. After a completion commit changes the
bound revision, keep precommit attempts historical and execute newly bound
current attempts before claiming current execution or publishing that claim.
Reapprove changed readiness, rebuild affected inputs, and independently accept
actual results. Phase 6 makes the final fresh bundle/reconstruction/69-UAT
procedure explicit. Evidence outputs and result reviews do not change expected
truth or create input hash cycles.

All 69 foundation UATs assure tooling. Full upstream and documented-language
inventories, seven internal equivalence obligations, CLI-P8 mapping, string Mix,
document closure, both product policies and the actual durable wr runtime remain
later work. Continue through all six core phases and required branch reviews
without ending for context or agent-capacity handoffs. No independent incidental
issue is deferred.
