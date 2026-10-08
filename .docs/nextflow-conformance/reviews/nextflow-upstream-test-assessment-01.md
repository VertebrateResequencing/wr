# Upstream test reuse assessment 01

## Verdict

Reuse Nextflow's pinned inputs and assertions as a second conformance corpus.
Start with a bounded translation pilot, then inventory every upstream test
unit before making a suite-completeness claim. The current six-phase
foundation can retain source provenance, obligations, cases, bindings, and
honest missing-work reports for this route. It does not currently implement
a broad upstream-test importer or certify that the upstream suite covers
every language requirement.

Passing all translated upstream tests would establish agreement for those
tests under their recorded conditions. It would not prove the whole language
specification implemented. No exhaustive requirement-to-test coverage claim
was found in the bounded material reviewed here. This is an observation about
that material, not proof that no such claim exists elsewhere.

The accepted foundation is also a separate specification. Its 49 tooling
acceptance UATs test acquisition, accounting, discovery, evidence, and other
foundation behaviour. They remain required. The seven E1 oracle cases remain
required. Upstream language tests cannot replace those tooling checks or the
wr-specific durability, recovery, and unsupported-feature requirements in F1.

## Scope and verified evidence

Assessment date: 2026-10-08. Worktree: `/home/ubuntu/wr`, branch
`nextflowdsl`. Target: Nextflow 26.04.6, source commit
`232b60569865e9a4577e48c1955409238359d6ca`, strict parser v2.

The local pinned tree was read from
`.tmp/nextflow-conformance/nextflow-generation-2687641921/source.tar.gz-tree`.
Production `sources.lock.json` and `batches.json` were parsed as JSON.
Every file in the three inventory groups below was checked against its lock
SHA-256, byte count, and Git blob ID. All 661 unique files matched.

| Locked directory | Files | Groovy files | Nextflow files |
| --- | ---: | ---: | ---: |
| `modules/nf-lang/src/test/` | 12 | 12 | 0 |
| `modules/nextflow/src/test/` | 340 | 331 | 0 |
| `tests/` | 309 | 0 | 115 |

Within the second group, `nextflow/extension/` has 39 `Test.groovy` files and
`nextflow/script/` has 50. These are subsets, not extra inventory totals.
The counts are file counts, not feature, case, assertion, or coverage counts.
They exclude other module/plugin tests, `tests-v1/`, documentation snippets,
cloud validation suites, and E2E suites. Those remain inventory candidates.

The checked counts and hashes are in
[inventory.json][inventory].
Exact pilot line spans, byte spans, full file and span hashes, helper hashes,
and the lock/batches hashes read for this assessment are in
[selected-evidence.json][selected-evidence].
These scratch records describe inspected evidence. They are not production
ledger records or execution evidence.

No build, Java execution, Nextflow execution, dependency download, adapter
implementation, commit, or push was performed. Runtime portability and v2
execution of the pilot candidates remain unverified.

## What the current design actually requires

[spec.md](../spec.md) Overview requires mechanical source preservation and
independently reviewed interpretation. It explicitly rejects record counts
or finite tests as semantic exhaustiveness proof. B1 requires an author to
read original spans and a separate reviewer to check them. The design does
not authorize extracting prose and automatically accepting invented tests.

A2 selects three upstream test files: `ScriptAstBuilderTest.groovy`,
`MixOpTest.groovy`, and `TaskConfigTest.groovy`. Its bootstrap semantic
boundary includes every MixOpTest method, but not every method in the other
two files. The production batches contain 18 bootstrap entries, including
three MixOpTest method spans. They do not contain a whole-suite translation
inventory. [phase2.md](../phase2.md) Item 2.5 preserves those 18 members and
requires upstream test methods and unknown source structures to remain
visible.

Architecture already supports source spans, reviewed facets, cases with
machine-checkable expectations, hashed fixtures, Go test bindings, oracle
and differential evidence kinds, and reviewed exclusions. That permits
upstream-derived cases. It does not supply a Groovy/Spock translator,
subcase/table-row completeness validation, a cross-engine workflow runner,
or a portable replacement for Nextflow's internal test harness.

F1 puts complete target accounting and policy resolution before the durable
runtime slice, then broad operator batches. An upstream inventory/translation
pilot belongs to that accounting work. Broad operator implementation still
depends on the durable runtime gate.

## Upstream harnesses and transfer boundaries

### Integration workflows

Pinned `.github/workflows/build.yml` runs unit tests via `make test` and has
integration modes `test_integration` and `test_parser_v2` in its Java 17/25
matrix. `test-ci.sh` exports the local Nextflow executable and Docker mode,
then invokes `validation/test.sh`. That script's v2 branch explicitly sets
`NXF_SYNTAX_PARSER=v2` and runs `tests/`; the v1 branch also runs `tests-v1/`.
The same script can clone external example workflows. This is CI invocation
evidence, not proof that this commit's complete CI passed.

`tests/checks/run.sh` builds `$NXF_CMD -q run ../../<workflow>`. The default
parser is v2. It runs each adjacent `.checks` shell script, or just runs the
workflow when no `.checks` exists. A bare workflow run therefore provides
success/failure coverage without a separate output assertion.

The ignore files are conditions to inventory:

- `.IGNORE` names `plugin-registry.nf`.
- `.IGNORE-DOCKER` names six Docker-dependent workflows.
- `.IGNORE-PARSER-V2` names 14 v2-only workflows. Despite its filename,
  `run.sh` consults it only when the selected parser is v1.
- Java-specific ignore files are conditional in `run.sh`; none exist in
  the locked `tests/checks/` tree reviewed here.

These conditions must be preserved per case and per target profile. A skip
for missing credentials or Docker is incomplete evidence, not a wr pass.
Upstream exclusions do not silently decide wr's supported language scope.

Some `.nf` inputs and `.expected` files can remain byte-identical for both
engines. Process arity and topic-channel pilots below have a small public
assertion contract. Their invocation still needs a wr-specific CLI adapter
and an explicit decision about `-resume`. Most inspected shell checks are
not portable unchanged: `hello`, `output-file`, and `subworkflow-take`
inspect `.nextflow.log` text such as submitted/cached process names;
`hello` also requests report, timeline, trace, and DAG output. Those are
Nextflow CLI/instrumentation contracts, not direct evidence of portable
language semantics.

Retain all upstream assertions. Classify log-format and report assertions
separately, then propose reviewed equivalents for task identity, cache reuse,
and artifacts through the supported wr boundary. Do not remove those checks
and call the whole upstream test translated. `tests/nextflow.config` also
sets a container image globally, so execution profile and config discovery
must be recorded rather than assuming every workflow is fixture-free.

### Spock scripts and parser fixtures

`Dsl2Spec.setup()` resets Nextflow globals, process state, and script metadata.
`ScriptHelper.runScript()` creates `MockSession`, selects a loader, evaluates
the script, starts the dataflow network, waits, and returns the normalized
last statement. This makes pure operator inputs and expected values useful
translation sources.

The helper uses `MockExecutorFactory`, `MockMonitor`, and `MockTaskHandler`.
For shell scriptlets, that handler assigns the script text as stdout and
exit status zero; it does not execute the shell command. Therefore a Spock
process test using this helper cannot by itself establish real staging,
shell execution, output files, or durable scheduling. Translation must
identify that dependency before choosing a supported wr boundary.

`ScriptLoaderFactory` selects v1/v2 from the parser setting. The v2 loader
explicitly captures the last statement of a snippet or entry workflow for
testing. Top-level snippet inputs in operator tests are therefore candidates
for reuse, not grounds to assume ordinary CLI file behaviour is identical.
If a public CLI needs a workflow wrapper or observer, preserve the original
snippet and record the wrapper as a reviewed transformation. Check the
transformed case against the pinned v2 oracle before claiming equivalence.

`ScriptAstBuilderTest` directly invokes the nf-lang `ScriptParser`.
`TestUtils.check` names the source `main.nf`, applies Groovy `stripIndent()`,
parses and analyzes it, collects only `SyntaxErrorMessage` causes, and sorts
errors by line and column. Translating source strings without these rules
changes the location and ordering assertions. CLI rendering is a different
boundary from this internal parser API. Preserve error count, location, and
message predicates; review how both engines expose them.

Shared setup, sequential `when`/`then` blocks, closures, `where` tables,
mock interactions, and assertions on JVM types must be inventoried. An AST
recognizer for bounded forms is preferable to regular-expression guessing.
An unrecognized form remains pending with its original span and reason.
It must block any claimed complete translation for that selected corpus.

## Bounded pilot candidates

Pilot IDs below are assessment identifiers only. Each exact span and its
SHA-256 is recorded in `selected-evidence.json`; the final section also lists
them for review. Start with P01, P03, P06 and P08. Use the other candidates
to expose semantic-analysis, multi-output, tables, configuration, resume,
and typed-feature adaptation costs before expanding the importer.

### P01: Strict parser invalid-syntax method

`ScriptAstBuilderTest.groovy`, lines 43-136, contains five independent
`when`/`then` pairs in one method. Preserve all five inputs, each expected
single error, and its location/message assertion. Expected locations are
2:14, 2:16, 3:24, 3:40, and 4:6. The invalid tokens differ between cases.
Dependencies are the shared parser and TestUtils normalization described
above. A method-level count of one would conceal four runnable subcases.
No workflow tasks should be started for this parser pilot.

### P02: Entry workflow requirement for a params block

`ScriptAstBuilderTest.groovy`, lines 155-183, has two sequential subcases:
a params-only script has one error at 1:1 with the exact entry-workflow
message; adding an empty entry workflow gives zero errors. Preserve
`greeting: String`, both inputs, and the positive case. Dependencies include
semantic analysis after parsing, not just grammar recognition. Neither
subcase can be treated as a runtime value test.

### P03: All three existing mix methods

`MixOpTest.groovy`, lines 30-67, supplies three runScript inputs. The first
asserts membership of six values and absence of `c`; it does not assert an
exact six-element multiset. Preserve that original predicate. A stronger
exact-multiset contract may be added as a separate reviewed case. The other
two compare sorted values with `[1,2,3]` and `[1,2]`, preserving multiplicity.
Dependencies are Dsl2Spec resets, the script loader, channel/value semantics,
`collect()`, and `.val` result access. A CLI observer replaces internal
channel access but cannot add an ordering guarantee to `mix`.

### P04: One multiMap method with three output channels

`MultiMapOpTest.groovy`, lines 38-67, has one input and three output-channel
assertion groups. It requires three channels, sequences `[1,2,3]`,
`[2,3,6]`, and `[3,3,3]`, and termination of each. Preserve channel names,
output association, multiplicity, and per-channel order. Translate internal
`Channel.STOP` checks into an explicit completion observation with a bounded
deadline; merely seeing three values is insufficient. The file declares an
OutputCapture rule, but this selected method does not inspect captured
stdout. Other methods in that file do, and its value-channel method asserts
a JVM `DataflowVariable` type. They need separate dispositions.

### P05: Task CPU table and two invalid-resource cases

`TaskConfigTest.groovy`, lines 286-304, has five `where` rows: default/null,
explicit 1, explicit 8, closure yielding 10 from context, and 32 capped at
24 by resourceLimits. Each row checks two getters plus `hasCpus`. Lines
668-686 supply two separate negative-value exception/message cases for
cpus and resourceLimits.cpus. These inputs and expectations are valuable,
but the tests directly construct TaskConfig. The fixture must create real
processes and observe evaluated requests at a supported boundary. Preserve
the distinction between omitted and explicitly declared cpus. A task script
printing a value alone does not prove the scheduler received that request.
This pilot needs the later resources contract and an approved mapping of
internal exceptions to public diagnostics.

### P06: Process input/output file arity workflow

`tests/process-arity.nf`, lines 1-34, produces one file, two matching files,
and three matching files with `1..*` arity, then stages them into `bar`.
The unchanged `.nf` input is a dual-engine candidate. Its `.checks` asserts
exit zero on a fresh run and a resumed run. Preserve both invocation cases.
Fixtures need a writable work root and shell `echo`/`cat`; config discovery
must be explicit. The upstream assertion is weak: it does not compare
contents or prove cache reuse. Add independently reviewed file/content
assertions as separate requirements, retaining the original success cases.
Resume support remains an explicit dependency, not an omitted argument.

### P07: Named subworkflows and process identity

`tests/subworkflow-take.nf`, lines 1-37, uses take/emit and composes flow1
with flow2. Its `.checks` requires one run each of flow1:foo, flow1:bar,
flow2:foo, and flow2:bar, then one cached occurrence of each on resume.
The input can remain unchanged. The checks require adaptation from Nextflow
logs to supported task/run identity and cache evidence. Keep all eight
process-name/count assertions associated with their two invocations.
Adding final value observation would be a separate stronger contract.

### P08: Topic channels producing an exact file

`tests/topic-channel.nf`, lines 1-35, has two producer processes, a topic,
`unique`, and sorted `collectFile`. Its `.checks` compares `versions.txt`
with the byte-identical `.expected` on a fresh run and resume. The expected
file contains bar 0.9.0 followed by foo 0.1.0, with exact line endings.
Inputs, shell scripts, and the expected file can remain unchanged; use the
engine adapter only for invocation and artifact lookup. Dependencies include
topic closure, distinctness, deterministic sorting, storeDir, local shell
execution, and resume. This is a useful later operator/process integration
case, outside bootstrap operator implementation.

### P09: Typed nullable path as a policy-sensitive case

`tests/nullable-path.nf`, lines 1-36, explicitly enables types, emits an
optional missing file, consumes `Path?`, uses stageAs, and expects the exact
stdout file containing `empty input` and a blank line. Its checks run fresh
and resumed cases. Preserve the type flag and original expected bytes.
This is v2-only according to `.IGNORE-PARSER-V2`; it cannot be converted to
an untyped test or absorbed into the seven static-typing-disabled E1 cases.
Its typed-feature policy and runtime readiness remain separate milestones.
The shell pipeline uses `tee` without local pipefail, so `$?` does not alone
prove the engine's exit status. A translated runner should capture both
engine exit and exact bytes, recording the additional exit assertion.

## Completeness accounting for later work

Maintain two independently reviewed inventories. Neither substitutes for
the other:

1. Every pinned upstream file, method, sequential subcase, table row,
   generated iteration, assertion group, helper, config, fixture, expected
   file, invocation variant, and skip condition receives an identity and
   disposition. Runnable units link to translated cases; unsupported or
   internal-only units retain their precise reason and pending/exclusion
   review. File or method coverage cannot discharge its child units.
2. Every in-scope language requirement from documentation, grammar,
   defaults, alternatives, errors, interactions, and policy decisions links
   to a reviewed test contract or an explicit missing-test explanation.
   Requirements may link to several upstream cases. Uncovered requirements
   still need new tests. wr-only durability and recovery requirements have
   their own provenance and tests.

For each translated unit, retain original file/span hashes, decoded literal
input, fixture closure, source assertions, normalization, adapter/wrapper
changes, and exact executable binding. Require a reviewer to account for
each original assertion. Classify strengthened expectations separately.
Do not regenerate expected values by running wr; compare both engines to
the preserved reviewed contract as well as to each other's observations.

Enumerate finite table rows individually with their setup context. Generated
Spock iterations require the data-provider expression, deterministic seed
when applicable, and actual expansion evidence; count unresolved providers
as pending. Preserve sequential state where the original shares it. A fresh
parser per subcase is a reviewed change, not an automatic simplification.

Importer parse failures and unknown assertion syntax fail closed for the
selected corpus. Keep unrecognized bytes and diagnostics visible. A
supported feature that fails remains a failure; unsupported language must
fail with a feature/location diagnostic and no success receipt as F1 states.
A reviewed scope exclusion has an affected-ID list and rationale. Missing
runtime bindings, dependencies, fixtures, or environment prerequisites count
as incomplete rather than excluded or passing.

Report totals separately for files, methods, subcases, rows, assertions,
unclassified units, reviewed transformations, pending units, exclusions,
oracle runs, wr runs, and differential passes. Also report requirement
facets with no tests. Whole-suite translation rates cannot be inferred from
this nine-candidate sample, and no rate is proposed here.

The focused revision should give each claim a checkable gate:

- Complete file inventory requires equality with the pinned tree's reviewed
  test scope and a disposition for every file, including helpers and data.
  Independent reconstruction must detect an omitted file or changed hash.
- Complete test-unit inventory requires independent review of method,
  subcase, row, assertion, and invocation decomposition. Unclassified source
  or unresolved data-provider expansion prevents this stronger claim.
- Complete translation requires every required inventoried unit and
  assertion to have a reviewed executable counterpart, with hashed fixture
  closure and normalization. Pending units prevent completion. Reviewed
  exclusions remain counted and named in the profile's denominator report.
- Translation validation must reject an omitted row, a swallowed unknown
  assertion, missing fixture data, changed expected bytes, a lost negative
  assertion, and a dropped resume invocation for their intended reason.
- v2 applicability requires pinned-parser evidence and reviewed flags,
  config, and exclusions. v1-only tests retain their inventory identity and
  a target-specific disposition; they do not silently become v2 passes.
- Oracle correctness requires actual pinned execution and agreement with
  preserved expectations. A transformed case needs review of the original
  and transformed contracts. Missing prerequisites remain incomplete.
- Differential completion requires fresh actual executions of both engines,
  compared to the same reviewed contract. An unavailable wr adapter yields
  missing runtime evidence. Foundation fixture passes cannot satisfy it.

Requirement accounting has its own gate over doc/grammar facets. Completing
any of the upstream gates leaves uncovered language facets outstanding.

## Recommended delivery boundary

Keep the accepted six-phase foundation and its current 49 UATs, seven oracle
cases, and 18 bootstrap batches intact. Its obligations/cases/evidence
records can accommodate reviewed upstream-derived examples without changing
the completion claim. This report completes assessment only.

Before Item 2.2, use the existing spec-review workflow for a focused revision
that adds the upstream inventory and translation contracts. This requires
reviewed specification changes rather than a small implementation-only patch.
Preserve accepted Phase 1 and schema work. Specify the upstream unit inventory,
bounded recognizers and fail-closed diagnostics, fixture closure,
assertion-preserving translations, parser normalization, cross-engine CLI
adapter, dual-engine evidence, and independent review gates. Prototype the
four starting pilots before committing to a general translator. Expand by
observed harness form, then assess additional module/plugin/doc suites.

The revision should make importer completeness and requirement coverage
separate claims, each with explicit denominators. It must not advertise
full language implementation from a test-suite pass. A later runtime plan
still needs the durable slice and wr-specific acceptance tests. The active
schema review can finish before the dependent extraction work proceeds under
the revised contract.

## Exact pilot identities

Paths in this section are relative to the pinned tree. Line intervals are
inclusive; byte spans are zero-based and half-open. Full source-file hashes
and helper/fixture identities are in the linked selected evidence. Span
hashes below cover the exact original bytes, including whitespace.

```tsv
ID	Lines	Byte span	Span SHA-256
P01	43-136	[1194,3440)	fec22247e7f942a0115868f6cdf1a6892c45fa4a19224f766182b9bd3c714fb7
P02	155-183	[3912,4601)	91820e5ff07602420e898123df73b6d857ab8c5b0591f92cd6aa6f932cb6bb9d
P03	30-67	[827,1716)	036fb214afef77850b68464ec9980be945096566388013f3c5e0658852786655
P04	38-67	[1011,1735)	3f4e56c39c6e6c8940f78e37a84ac542036a0fe513c9cece0df9c98dfb919e4f
P05	286-304	[8124,8669)	2859eb4a5980e7669fa81d846280da3af0414b9f4c15fa13f73b907c0778829c
P05_NEG	668-686	[19463,20147)	3ca542796b9be0b93850da703d52e91532165a756757902e554006cdde78d86d
P06	1-34	[0,601)	1d91a4f5d985df4d562d98e6f5b308240e6d0ab633aafdfd0e57fa8e61db16c3
P07	1-37	[0,435)	0ac72a046ae6a012da525b2b807191b6672dd67120e6f9e67795a4adac9054ad
P08	1-35	[0,441)	98bbaf627a283ea5db42215aab490c9a7a3f00c0aeef6c10bdc4da98bceced97
P09	1-36	[0,451)	1c8b22d3abe37bff3c764fc449f66ed4ae1303d4d770d38a3c7337326386a581
```

[inventory]: ../../../.tmp/agent/nextflow-conformance/upstream-test-assessment01/inventory.json
[selected-evidence]: ../../../.tmp/agent/nextflow-conformance/upstream-test-assessment01/selected-evidence.json
