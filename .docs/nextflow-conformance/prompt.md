# Nextflow conformance foundation

## Original request (verbatim)

> This branch is about specing and implementing nextflow dsl2 support in wr. There have been many attempts at getting this to work, but there are endless gaps with LLMs unable to spec or implement everything, or even verify if everything has been spec’d or not. Following are my notes on all my interactions with the LLMs, the prompts I used, some of the LLM responses etc. It’s been a while since I did this work so I’m not really sure of the current state, or if the current approach with the scripts to figure out spec completeness is good enough, or if the planned nextflowstrict5 would get us there. Consider everything that has happened so far and suggest a way forward. Should we write the nextflowstrict5 spec now? Scrap everything and just start over with a better and fully verifable foundation? Something else? My preference is to start from scratch (revert commit to match develop branch).
>
> I’d suggest:
>
> - Freeze the target as Nextflow strict syntax as of 26.04 (or whatever might be considered stable and suitable right now)
> - Focus on creating fully capable and bug-free scripts that can be used to correctly extract nextflow dsl2 definition spec items, verify we have them all, create spec docs with UATs for each, verify we have them all, then once an implementation is done, verify there are tests for every UAT.
> - Context management will be key, so having progress checklists and breaking the problems in to ~100k token chunks for fresh context subagents to work on is important.

## Authorization (verbatim)

> Go ahead with that plan.

## Notes

### Naming correction

User instruction:

> I don't want vague things like "conformance" in the repo root, or
> "cmd/wr-conformance". Nextflow-related stuff should clearly include
> nextflow in the name.

Use `nextflowconformance/` for the Go developer package and
`cmd/wr-nextflow-conformance/` for its executable. Active documentation,
generated schema identities, default paths, fixtures, and commands must use
the explicit Nextflow names. Preserve historical review evidence and hashes
as snapshots; record the path migration separately. Use
`nextflowconformance/data` and `.tmp/nextflow-conformance` as CLI defaults,
`urn:wr:nextflow-conformance:schema:1:` for schema IDs, and
`NEXTFLOW_CONFORMANCE_` for this tool's developer environment variables.

### Accepted direction

- Preserve the previous branch and restore the working tree to develop in a
  new commit. Retain history; do not force-push or erase the earlier work.
- Build the conformance foundation before writing another broad gap-filling
  implementation spec. The immediate deliverable is a reviewed spec and
  phase plans for that foundation using the full spec-writer workflow.
- Continue with foundation implementation and adversarial validation, then
  expand the target inventory, prove a demanding runtime slice, and implement
  dependency-ordered batches against executable UATs.
- Target Nextflow 26.04.6 with NXF_SYNTAX_PARSER=v2. Pin the release commit,
  source documentation, relevant upstream tests, and oracle runtime by hash.
  Normal verification uses the pinned snapshot offline.
- The product remains pure Go and does not require Nextflow at runtime.
  Nextflow may be used as a development/test oracle.
- Account for typed processes/workflows in the inventory; explicitly settle
  their implementation milestone. Define the policy for arbitrary JVM
  libraries and plugins. Earlier exclusions are historical evidence, not
  automatically current requirements.
- Unsupported semantics produce a precise error instead of successful
  execution with changed meaning.

### Evidence and completeness

- Build traceability from pinned source to behavioural requirements, UATs,
  executable tests, and recorded results. Machine-readable records are the
  authority; Markdown specs, reports, and checklists are generated views.
- Account for every selected source file and meaningful block, including
  tables, options, examples, and warnings. Heading extraction alone is
  insufficient.
- Map source blocks to explicit behaviours and variants, or to a reviewed
  explanation of why they create no requirement. Inventory boundaries and
  exclusions remain explicit.
- Give each required behaviour concrete inputs, expected observable results,
  and relevant failure/boundary cases. Include interactions across features.
- Map every UAT to a discoverable executable test that actually runs. Missing,
  skipped, failed, and timed-out tests remain incomplete.
- Bind results to implementation revision, corpus/test hashes, environment,
  and artifacts. Changed inputs invalidate old evidence. Keep scope decisions
  separate from observed execution status.
- Extraction preserves and enumerates source content. Agents propose its
  meaning; independent reviewers check original sources for missing defaults,
  variants, interactions, and failure behaviour. Cross-check upstream grammar,
  API declarations, and tests where available.
- Completeness accounting is relative to an explicit target. Neither scripts
  nor finite tests prove arbitrary software bug-free or prose semantically
  complete. State these limits without weakening required checks.
- Differential tests compare minimal workflows under pinned Nextflow and wr:
  artifacts, values, task counts, errors, and specified ordering. Normalize
  only documented differences. Record doc/oracle disagreements as unresolved
  decisions. Parsing diagnostics do not establish runtime equivalence.
- Test the verifier with deliberate corruption: removed source sections,
  collapsed overloads, duplicate IDs, missing UATs, skipped tests, altered
  expectations, stale evidence, and failed source fetches. Require the
  intended failure for each. Use independently reviewed extraction fixtures
  and selected behavioural mutations.

### Delivery and context management

- Bootstrap the foundation on representative real cases and deliberately
  broken inputs before expanding the whole inventory. Its spec must define
  a bounded, testable completion criterion.
- Inventory wr-specific requirements as well as Nextflow semantics: durable
  dynamic execution, containers, resources, grouping, output access, and
  avoiding unnecessary intermediate files.
- Before broad operator implementation, prove a file-producing dynamic
  workflow with fan-out, empty branches, out-of-order completion, and
  manager/CLI crashes at submission and expansion boundaries. Require an
  immutable submitted run definition, no lost continuation, and no duplicate
  logical tasks. This is a later runtime milestone, not a claim that restoring
  develop already supplies a Nextflow implementation.
- Treat approximately 100k tokens as a per-agent ceiling including tool
  output and reasoning space, not a desired batch size. Split work by coherent
  semantics and dependencies. Fresh agents receive exact source excerpts,
  assigned IDs, relevant paths, commands, and completion criteria.
- Keep a durable ledger with evidence links and unresolved decisions.
  Generate checklists from it. Verify evidence before marking work complete.
- Existing code and tests may be reused only after behavioural validation.
  Preserve useful regression inputs without accepting old expected results
  automatically.

### Repository baseline and historical evidence

- Previous HEAD: c753f39eeba093772ec4df858e620ae67aee209a.
- Archive branch: codex/archive-nextflowdsl-2026-09-28.
- Develop baseline: f2888015559873b0aef386ada584a5fb2674e88e.
- Restoration commit: 0add8846. Its tree equals the recorded develop tree.
- Historical files are available through git show on the archive branch.
  Inspect only relevant files; do not import the whole history into context.
- The old audit counted 570 IDs despite every generated reference description
  containing a placeholder. Its manifest was derived from those same files.
  In-memory probes showed empty input and unsupported evidence claims passed;
  all source fetches failing still produced a successful coverage exit.
- The old fair requirement and tests checked job priority, which cannot prove
  ordered output emission. Dynamic continuation also depended on invocation
  mode and re-reading mutable source. These are regression ideas, not a new
  implementation design.
- Strict4 was implemented, strict5 was absent, and consolidated classifications
  predated nextflowparse. Do not use the old classifications as current proof.

### Primary source starting points

- Release: https://github.com/nextflow-io/nextflow/releases/tag/v26.04.6
- Tagged source: https://github.com/nextflow-io/nextflow/tree/v26.04.6
- Documentation paths in that source include docs/reference/syntax.md,
  docs/reference/process.md, docs/strict-syntax.md, and docs/migrations/26-04.md.
- Verify exact source commits and dependencies during research. A URL alone
  does not establish a pinned corpus or a successful oracle run.

### Clarification round 1 findings

- No unsettled user decision blocks the foundation spec. Its acceptance claim
  is limited to validation on a declared bootstrap corpus. Complete target
  inventory and wr runtime conformance are separate later milestones.
- The typed-process/workflow implementation milestone and JVM/library/plugin
  compatibility policy remain named unresolved product decisions. They block
  affected scope and implementation completion; they are not exclusions.
- Nextflow v26.04.6 resolves through annotated tag
  38ce286fe70b44a5907cf1f5b0b8fb13bd836721 to release commit
  232b60569865e9a4577e48c1955409238359d6ca. Hash acquired artifacts separately.
- Relevant external parser/runtime dependencies must also be pinned. The
  release repository alone is not proof that its whole dependency corpus has
  been captured.
- Use Go and the existing repository conventions for the foundation unless
  research identifies a concrete reason otherwise. Schemas, bootstrap cases,
  storage layout, and commands are engineering decisions for the spec author.
- Restored wr supplies queue, container, and dependency primitives but no
  Nextflow adapter. Foundation tests must not label a fixture or simulated
  adapter as proof that wr executes Nextflow.

### Runtime packaging evidence from phase 1

- Actual acquisition found the official pinned distribution is a launcher
  followed by a shaded JAR with repeated ZIP names and no nested JARs.
  Independent inspection verified the exact published whole-file SHA-256.
  See reviews/runtime-packaging-author.md for measured artifact evidence.
- Retain and execute the verified distribution unchanged as an opaque
  artifact. Record actual external execution dependencies; do not fabricate
  separate embedded JARs. Capture Maven coordinates/POMs as provenance.
- Strict path and duplicate-destination rejection still applies to extracted
  archives. Opaque runtime integrity must detect launcher-prefix and payload
  tampering, and an artifact's packaging label cannot bypass pinned identity.
- This corrects an engineering assumption in the foundation spec without
  changing the release target or broadening the runtime compatibility scope.
