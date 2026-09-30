# Phase 1 runtime split input bundle

Verdict: APPROVED for runtime-only scope and allocation on 2026-09-28.
This approves sizing, not implementation or acceptance. Use a fresh runtime
implementor and a separate fresh independent reviewer. The stopped
implementor reports no source or test edits.

## Scope

Apply the core spans and requirements in
[the part 2 bundle](phase-01-acquisition-part2-bundle.md), restricted to its
first split: A1_05 through A1_07, opaque executable runtime acquisition in
pinned and locked modes, real Java snapshot/version/inventory, genuine
required tools and execution edges, all three actual POMs, launcher/build
provenance, identity mutations, and the 14 deferred lint findings. Preserve
the accepted local-origin model and reuse corrections, A1_01 through A1_04,
and the three-blob fixture. Runtime fixture offline validation remains
required, including A1_06 with actual pinned bytes and fixtures stopped.

Run the full focused package/CLI tests, stock schema checks, and unchanged
lint analyzers under the versions and bounds in the part 2 bundle. The
accepted local-origin reviews update the baseline to 30 passing test
functions, 11 schemas, and 1,243 cases; they retain the 14 deferred findings.
Their PASS verdicts cover model and inert reuse behavior, not actual Java or
tool execution. This sizing review did not rerun those checks.

After runtime implementation and independent review PASS, use fresh contexts
for real production candidate acquisition, independent artifact acceptance,
exact A2 selectors/includes, and candidate offline validation. Reapprove that
second input bundle. Both splits are required to close Item 1.2; runtime
PASS alone cannot close Item 1.2 or Phase 1.

## Allocation

This allocation replaces the original part 2 allocation for each runtime
implementation or review context. The approximately 100,000-token ceiling
remains a ceiling, not a target.

| Use | Token cap |
| --- | ---: |
| Initial spans, role skills, instructions, and bundles | 48,000 |
| Supplemental reads, source/test growth, and complete final diff | 22,000 |
| Command output and evidence summaries | 6,000 |
| Reasoning, edits, and handoff | 14,000 |
| Total | 90,000 |

The scope measurement is `nextflow-part2-scope-measurement.json` under
`.tmp/agent/nextflow-conformance/acquisition-part2/` from the repository root.
It records 18,297 accepted supplemental bytes and 9,383 bytes across nine
currently affected functions. Its
10,000-token supplemental constraint is superseded here. The caller estimates
9,000 more bytes already read. At four bytes per estimated token, the
22,000-token allowance holds 88,000 bytes. The proposed remaining work is
12,000 bytes of net source/test growth, 32,000 bytes of complete diff, and
16,000 bytes of further source/evidence reads. These total 87,297 bytes with
the prior reads, leaving only 703 bytes of estimated headroom. These are
decimal byte estimates, not measured token usage or approved actual growth.

## Pre-review measurement gate

Before review dispatch, record actual bytes for every supplemental read,
every new or changed complete function and test, the complete diff including
untracked files, and required evidence. Map each input to its initial or
supplemental allocation. Count repeated reads when they enter context again.
Use complete changed spans to check coverage; net growth alone cannot prove
that all review inputs fit. Include this bundle and required review reports
in the measurement. Remeasure moved spans instead of trusting old lines.

Keep supplemental reads at most 2,000 estimated tokens per read. Print compact
diagnostics and keep full command logs on disk. Retain the 14,000-token
reasoning/edit/handoff allowance and 6,000-token output allowance. Stop and
reapprove a narrower bundle before a required read or expanded change would
exceed an allocation or the projected 90,000-token total. The 703-byte margin
does not authorize additional unmeasured input.

This review used the existing sizing bundle, measurement, and accepted
[model review](nextflow-phase1-local-origin-model-review-01.md) and
[reuse review](nextflow-phase1-local-origin-reuse-review-01.md). It reread no
production source and changed only this report. No commits were made.
