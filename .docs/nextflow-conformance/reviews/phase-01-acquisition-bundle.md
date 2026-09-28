# Item 1.2 acquisition input bundle

Verdict: APPROVED for input size and scope on 2026-09-28. This approves one
fresh implementation context and a separate fresh independent review
context. It awards no implementation, acceptance-test, acquisition, or
phase pass. Item 1.1 passed `phase-01-schema-review-05.md`.

## Scope and budget

Complete Phase 1 Item 1.2, including all seven A1 UATs, the actual acquired
candidate, exact A2 bootstrap selector evidence, offline validation, and
the acquisition lint findings. A2 extraction and semantic review remain
later work. Nextflow execution remains E1 work.

Each agent has this working budget, including input, output, and reasoning:

| Use | Token cap |
| --- | ---: |
| Initial instructions, skills, and listed inputs | 46,000 |
| Supplemental source reads and acquired-source snippets | 10,000 |
| Command results, diagnostics, and evidence summaries | 8,000 |
| Reasoning, implementation edits, and final report | 26,000 |
| Total working budget | 90,000 |

The listed initial spans and all eleven applicable skill files measure
173,377 bytes. Four bytes per token gives approximately 43,344 tokens.
This is an estimate, not measured model usage; the briefing and this bundle
also consume input. Read only the applicable role skill. Stop expansion
before a projected 90,000-token total and use the split below. The user's
roughly 100,000-token ceiling is a ceiling, not a target.

The reviewer uses the same budget and reads the final changed acquisition
files in place of their initial versions. Changed-file growth, the final
diff, implementation evidence, and supplemental reads share the extra-read
allowance. Re-estimate before dispatch if the completed change exceeds it.

## Initial input spans

Paths below are relative to `/home/ubuntu/wr`. Line numbers refer to this
review's snapshot; use the named functions or headings if lines move.

Read these skills from `/home/ubuntu/.agents/skills/`:

- `agent-conduct/SKILL.md` and the assigned `go-implementor/SKILL.md` or
  `go-reviewer/SKILL.md`.
- `implementation-principles/SKILL.md`, `testing-principles/SKILL.md`, and
  `go-conventions/SKILL.md`; reviewers also read `code-smells/SKILL.md`.
- For the handoff document, `writing-for-agents/SKILL.md`,
  `unslop/SKILL.md`, and `prose-principles/SKILL.md`.
- `final-response/SKILL.md` before returning the verdict or handoff.

Read these contract and evidence spans:

- `.docs/nextflow-conformance/spec.md:23-444`, Architecture through the A2
  selection contract; and `1034-1054`, Implementation Order steps.
- `.docs/nextflow-conformance/phase1.md:1-148`, including Instructions,
  Item 1.2, and exit conditions.
- `.docs/nextflow-conformance/evidence/phase1.md:1-18,77-97,158-198`,
  historical acquisition gaps, failed transaction, and Java prerequisite.
  Its nested-JAR assumptions and incomplete Item 1.1 status are superseded.
- `.docs/nextflow-conformance/reviews/runtime-packaging-author.md:1-140`,
  the measured packaging amendment and provenance identities.
- `.docs/nextflow-conformance/evidence/nextflow-naming.md:1-100`, the naming
  map and historical commands. Review 05 supersedes its pending-review
  status and older schema mutation totals.
- `.docs/nextflow-conformance/reviews/phase-01-schema-review-05.md:1-7,80-112`,
  Item 1.1 verdict, preserved constraints, and remaining acquisition gates.

Read these implementation spans:

- `nextflowconformance/source.go:1-1186`, complete acquisition code.
- `nextflowconformance/source_test.go:1-399`, complete existing A1 tests.
- `nextflowconformance/model.go:43-99,138-183,195-437,850-882,1244-1255`,
  constants, target, tree, artifacts, environment, runtime closure, lock
  validation, decoder entry point, and relative-path validation.
- `nextflowconformance/cli.go:61-371,799-827,865-876,1064-1101`, diagnostics,
  invocation, output safety, acquire/validate callers, target identity,
  offline checks, result fields, and `Run`.
- `nextflowconformance/schema.go:323-346,424-437`, artifact packaging and
  coordinate conditions. The remaining schema implementation stays under
  the passed Item 1.1 review unless a specific acquisition dependency fails.
- `cmd/wr-nextflow-conformance/main.go:1-53`, cancellation and process exit.
- `go.mod:1-50` and `.golangci.yml:1-169`, dependencies and quality gates.
- `.tmp/agent/runtime-packaging/inspection.json`, complete small metadata
  record. Inspect directory names and sizes without printing binary data.

Inspect only diagnostic headers and the aggregate in
`.tmp/agent/nextflow-conformance/review05/lint.log`. The current baseline is
61 findings, 50 in `source.go` and 11 in `source_test.go`. Review 05 records
exactly five full-focused-suite failures, `TestUAT_A1_01` through `A1_05`;
their obsolete fixtures fail before their intended assertions. A1_06 and
A1_07 do not exist yet. This bundle review did not rerun tests or lint.

## Supplemental reads

The 10,000-token cap covers all extra source reads combined. Use bounded
searches and exact spans, with at most 2,000 tokens per read. Useful first
pointers are `model_test.go:176-199,249-296,482-507,619-652` for artifact
tests and corpus helpers, and individual records under
`nextflowconformance/testdata/records/` when constructing valid fixtures.

Inspect acquired trees, complete candidate locks, Java inventories, and
schemas with scripts. Retain full machine-readable results on disk; print
counts, hashes, unresolved selectors, diagnostic headers, and failing
records. Do not dump `tree.json`, generated schemas, the 1,190 schema
mutations, Java file listings, or the complete acquired source tree into
context. A changed schema contract requires a new bounded review of that
change; it does not authorize weakening the passed Item 1.1 constraints.

For selectors, inspect only the twelve paths in spec A2, recursively
referenced local include paths, `packing.gradle`, and root `build.gradle`.
Use scripts to locate complete boundaries and hash their original bytes.
Print bounded snippets around those boundaries and a manifest of paths,
start/end offsets, hashes, match counts, and include targets. Check every
selector at spec lines 419-442, including the two process regions, both
operators, imports, the HTML migration heading, seven grammar rules, all
MixOp test methods, and both Gradle dependency regions. Missing or ambiguous
selectors leave the candidate unaccepted. This preparation must preserve
the later A2 extraction and semantic-review obligations.

## Required evidence and guardrails

Record the fetch-all-failed red command before changing its behaviour.
Exercise A1_01 through A1_07 through observable CLI results and artifacts,
with independent HTTPS fixtures for transaction, tree, path, request-limit,
and offline checks. The three-blob fixture must retain its specified count
without a decoder bypass or a changed production target identity.

Keep the official distribution opaque. Its exact size is 42,355,106 bytes;
its SHA-256 is
`182a63c74074e2dc7956ffa3c8cd59de952ed2c44394e21faf5e1736b945444c`.
The measured prefix is 17,247 bytes, with 24,898 ZIP entries, 23,238 names,
1,629 repeated names, and no nested JARs. Preserve all bytes without member
extraction or repackaging. Test prefix and shaded-payload mutations plus
packaging-label and replacement-lock-hash bypass attempts.

The files under `.tmp/agent/runtime-packaging/` are retained research
inputs, not a successful candidate cache. Rehash reused bytes and record
their provenance. The parent independently rechecked the distribution and
launcher hashes and ran the existing Java binary on 2026-09-28; that does
not replace Item 1.2's evidence. Use the existing Java home
`.tmp/agent/java21/jdk-21.0.12.1+1`, without system installs. The historical
archive identity is recorded in `evidence/phase1.md:173-198`.

Hash the actual Java tree, required environment tools, and any genuine
external JAR. Record POMs for the three pinned coordinates as
`dependency-metadata` with `file` packaging. Retain the separate launcher,
`packing.gradle`, and `modules/nextflow/build.gradle` as provenance. POMs
and shaded metadata do not establish a resolved Maven execution graph.

New evidence paths and artifact names must identify Nextflow. Use the
current corpus and cache defaults, `nextflowconformance/data` and
`.tmp/nextflow-conformance`, and `cmd/wr-nextflow-conformance`. Preserve
existing locks, caches, and evidence on transaction failure. An unavailable
real prerequisite leaves the affected acceptance test incomplete.

The final handoff must contain all seven UAT results, focused package and
CLI test results, clean acquisition lint, measured acquisition JSON/stderr
and exits, candidate path and hash, artifact identities, exact selector
evidence, and offline validation with acquisition fixtures stopped. Rehash
the actual acquired bytes during independent review before accepting the
candidate. Keep the candidate distinct from an accepted checked-in lock.

Run the bounded Phase 1 exit commands with Go 1.27.1 for tests. Use the
existing `.tmp/agent/bin/golangci-lint` v2.12.2 with
`GOTOOLCHAIN=go1.26.3` for analyzer compatibility and unchanged checks.
Keep full logs on disk and inspect bounded summaries. Full unrelated wr
tests are outside this item. Acquisition success awards no oracle pass.

## Split fallback

Split before exceeding the budget, or when the next required evidence
cannot fit the supplemental-read cap. Keep implementation sequential and
give each part a fresh implementor and independent reviewer:

1. A1_01 through A1_04: valid fixture contract, transaction preservation,
   immutable tree accounting, bounded HTTPS, extracted-archive safety, and
   their lint findings. Use the shared contract plus transaction, source,
   tree, fetch, and corresponding test spans. Adapt only the minimum shared
   packaging support needed to exercise these tests honestly.
2. A1_05 through A1_07: opaque runtime, actual external execution closure,
   dependency provenance, identity/preflight mutations, actual acquisition,
   exact selector evidence, and offline candidate review. Read part 1's
   accepted diff and relevant interfaces, then runtime, Java, dependency,
   and corresponding test spans. Finish remaining acquisition lint and run
   the complete focused suite, including part 1 regressions.

Re-estimate each split bundle before dispatch, retaining at least 30,000
tokens for implementation/review work and evidence after initial reading.
Part 1 may pass only its assigned acceptance boundary; Item 1.2 and Phase 1
remain incomplete until part 2 and final independent candidate review pass.
