# Feature Coverage Review 02

Verdict: FAIL.

Reviewer: independent agent `/root/conformance_review_2`.
Date: 2026-09-28.
Previous reviews were not read. No specification edits were made.

## Reviewed inputs

```text
prompt.md SHA-256:
a080ae1a612102925776d0c2b18094a5ec2acaf67143a5116c36660d047d3acd
spec.md SHA-256:
b18094a7c4dd91db1b42cb14472fa25e3ae3473ba7d5a9adec218a65c8946f1a
Repository HEAD:
00a9e53ffd4b9dc4156c2a7ff8df8279811101d1
```

Paths above are relative to `.docs/nextflow-conformance/`, except HEAD.

## Finding

### F02-01: Permit absent values for reviewed expected-error cases

Location: `spec.md:750-753`, in conflict with `spec.md:735-737` and
`spec.md:783-788`.

The observer contract allows a workflow exit without observation lines only
for the explicitly empty case, after successful workflow completion. The
mandatory `ORACLE_IMPORT` case instead fails compilation before the workflow
can emit any `OBS:` values. It therefore cannot satisfy that observer rule,
even when its precise import diagnostic, location, stage, task count, and
nonzero exit all match. `ORACLE_FILE_ERROR` also needs an explicit absence-of-
values contract when missing output prevents downstream emission.

Separate value-bearing cases, successful empty-channel cases, and expected
error cases. Permit zero value lines for the latter only when the reviewed
contract declares no values and all specific error observations match. Add
an assertion that the actual import-error run passes with zero value lines,
while a missing-Java failure and an unexpected value line still fail. This
preserves the strict error contract and makes all seven required oracle
cases executable under the specified observer.

## Coverage checked

- Prompt lines 21-39 and 128-146: the foundation boundary, pinned release,
  pure-Go product, absent wr adapter, typed milestone, and JVM/plugin policy
  appear in the overview, A1, B2, and E1. The two product decisions remain
  unresolved and block affected later claims.
- Prompt lines 43-65: A1, A2, and B1 preserve selected bytes, enumerate
  meaningful leaves and variants, retain pending blocks, require independent
  original-source review, and separate interpretation from mechanical
  coverage. Requirements, UATs, bindings, and reviews have explicit links.
- Prompt lines 52-69: C1, C2, D1, and D2 require observable expectations,
  actual test discovery/execution, precise error matching, restricted
  normalization, raw artifacts, and current code/corpus/environment hashes.
  Oracle evidence cannot count as wr runtime or differential evidence.
- Prompt lines 70-80: E2 names 18 accounting mutations and three semantic
  observer mutations. Intended diagnostics, passing baselines, survivors,
  and invalid controls are explicit. A2 and E1 bound bootstrap completion.
- Prompt lines 81-89: F1 retains all nine wr requirements, including durable
  dynamic execution and crash boundaries. These are later runtime work;
  foundation tests verify their records and incomplete state.
- Prompt lines 90-98: F2 requires bounded reviewed handoffs, exact sources,
  assigned IDs, commands, deadlines, generated checklists, and a durable
  evidence ledger. Reuse requires behavioural validation.
- The architecture uses a public Go package, a thin development executable,
  existing dependencies, GoConvey acceptance bindings, and no production
  Nextflow dependency. The 47 numbered acceptance IDs are unique. This
  review found no missing prompt requirement beyond the executable-contract
  conflict above.

## Independent source checks

The release API reports the same launcher and distribution SHA-256 values
as the target table. Their reported sizes are 17,246 and 42,355,106 bytes,
respectively, below the specified per-object acquisition limit. These are
metadata checks, not downloaded-distribution or oracle execution claims.

```text
https://api.github.com/repos/nextflow-io/nextflow/releases/tags/v26.04.6
```

Eight cached source files were checked against their Git blob IDs in the
pinned, non-truncated tree listing: process and operator references, strict
syntax, migration notes, ScriptParser grammar, MixOp tests, and both selected
module Gradle files. All eight matched. The selected typed/fair/map/mix
anchors and all seven selected grammar rules exist. The typed map null
variant is recorded separately in E1.

The pinned TraceFileObserver implementation flushes each completed task row.
That supports the fair-case supervisor waiting for B's trace completion
before releasing A; no sleep-only assumption is needed. The fetched source
hash was:

```text
58ab66618b86c225264b7819620b10153b0d39d742880f1a924d2ed8b6b915b6
https://raw.githubusercontent.com/nextflow-io/nextflow/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/main/groovy/nextflow/trace/TraceFileObserver.groovy
```

No runtime or foundation implementation tests were run. This verdict reviews
coverage and executable contracts, not implementation conformance.
