# Nextflow research pilot clarification 01

## Result

NONE. No user-owned decision remains on the current charter frontier.
The [prompt][prompt] and [accepted research review][review] fix the finite
first group, preservation obligations, execution order and allowed outcomes.
They permit measured missing prerequisites and failed mappings. The charter
can therefore be written and reviewed without selecting the eventual wr
assurance architecture or requesting authorization for research resource
reuse.

Date: 2026-10-08. Owner: `/root/nextflow_pilot_clarification01`.
Queue owner: `/root`. Branch: `nextflowdsl`; worktree: `/home/ubuntu/wr`.
Target: Nextflow 26.04.6, commit
`232b60569865e9a4577e48c1955409238359d6ca`, parser v2.

## Findings available to the charter author

### Selected original contracts

The pinned `ScriptAstBuilderTest.groovy` supplies three selected methods.
The invalid-syntax method at lines 43-136 contains five sequential inputs
and twenty predicates. The mixed-top-level method at lines 138-153 contains
one input and four predicates. The params method at lines 155-183 contains
two sequential inputs and five predicates, with opposite outcomes. These
are eight parser inputs across three methods, rather than three cases.

The original parser is `@Shared`, created by `setupSpec()`. `TestUtils.check`
decodes its supplied Groovy string through the genuine caller, applies
`stripIndent()`, uses `main.nf`, parses and analyzes, filters
`SyntaxErrorMessage` causes, then sorts by line and column. Retaining this
state and literal contract is a preservation obligation. Replacing it with
a CLI diagnostic observer needs a reviewed mapping or an unresolved result.

`MixOpTest.groovy` lines 30-67 contains all three methods and nine original
value predicates. Seven belong to the first method's membership/absence
checks; the other methods each compare a sorted list. The class has
`@Timeout(5)`. `Dsl2Spec` resets task, script metadata and global state.
`ScriptHelper` returns normalized internal values after dataflow completion.
Its mock shell handler reports script text with exit zero. That observation
does not establish real shell execution.

Arity and topic each contain fresh and resume invocations. Arity's two
literal status expressions run after `set +e`; the final expression can
hide a fresh failure. Topic retains two byte comparisons under the runner's
`bash -ex .checks` launch. Both literal checks and original aggregate pass
rules must remain visible. Independent invocation exits and enforced checks
are separate strengthening, as established by the accepted F1 controls.

The selected topic fixture is exactly `bar: 0.9.0\nfoo: 0.1.0\n`.
The nullable fixture used by the harness controls is exactly
`empty input\n\n`. [Facts][facts] retain their byte lengths, hexadecimal
bytes and hashes. The nullable DSL execution remains outside the first
group; its four reviewed bad harness controls and paired valid controls
are already required by the prompt.

### Modes, documentation and fixtures

The selected Mix, arity and topic scripts contain no static-typing flag.
The params block can be used without `nextflow.enable.types`, according to
the pinned `docs/workflow-typed.md` lines 3-17. The pinned topic reference
states that topic became available in 25.04.0; the earlier preview flag
does not create a new 26.04.6 pilot policy choice. Explicitly recording the
selected flags is routine research work.

Useful bounded requirements are already present in the verified cache:
mixed declarations/statements in `docs/strict-syntax.md` lines 78-123;
Mix in `docs/reference/operator.md` lines 826-852; input arity in
`docs/reference/process.md` lines 155-177; output arity and scalar/list
behavior at lines 207-223; and topic in `docs/reference/channel.md` from
line 339. The Mix literal includes and their exact bytes are cached.
The author can select exact spans and retain unresolved outgoing links.
That selection supplies no complete language denominator.

`tests/nextflow.config` declares a global container image, while the original
checks runner conditions execution on flags and ignore files. It creates
writable scratch state and discovers configuration from the test layout.
The charter must record effective discovery, execution mode, tools and
fresh/resume state. A declaration alone does not establish that Docker ran.
The initial group does not require a user choice about production containers
or the future wr executor.

### Original harness prerequisites

The existing production lock has 282 artifacts: one launcher, one opaque
runtime, 249 Java files, 27 environment tools, three dependency POMs and one
source archive. Its Java environment records Temurin 21.0.12.1+1. Reuse is
already authorized, subject to byte verification and applicability.

The pinned original build uses a Java 21 toolchain, Gradle 9.3.1,
Groovy 4.0.31, Spock 2.4-groovy-4.0 and JUnit Platform launcher 1.10.5.
`nf-lang` tests depend on Nextflow test fixtures. The selected dependency
closure therefore includes more than an executable distribution and three
runtime POMs. Resolve the actual closure before claiming original execution.

Read-only ZIP inspection of the hash-verified distribution found its parser
implementation, but no Spock or JUnit Platform members, selected original
test classes, or `test/ScriptHelper`. Bounded searches found no filenames
matching Spock, JUnit Platform, Gradle 9.3.1, Jimfs, Byte Buddy or Objenesis
under `/home/ubuntu/.gradle`, `/home/ubuntu/.m2` or the repository `.tmp`.
The first two directories were absent. This does not prove absence from
every possible machine location, or establish the complete transitive
closure. It establishes that the retained distribution and searched caches
do not demonstrate a usable original test harness.

The charter can set bounded prerequisite-resolution deadlines, retain any
required acquisition and hashes in its separate research manifest, and
report unavailable original execution. Such a result completes the finite
research accounting without awarding an oracle or translation pass.

## Decisions already settled or outside this frontier

Exact methods, subcases, manual contract representation, a few documented
gap examples, observer prototypes, evidence filenames and finite deadlines
are bounded research choices for the author and reviewer. Their suitability
can be tested and recorded without asking the user implementation questions.
The required first group and controls remain fixed by the prompt.

The final assurance approach, complete normative closure, production
typed-language profile and JVM/Groovy compatibility policy remain unresolved
for the later approach decision. A pilot can preserve internal assertions
and report partial or unresolved mappings without settling those policies.
The absent future wr DSL adapter supplies pending-wr evidence, not a user
permission question or a wr execution pass.

Phase 1 remains accepted. Item 2.1 F11 and Item 2.2 remain deferred as the
prompt states. This clarification changes none of their status or artifacts.

## Verification and completion

The read-only [fact check][checker] verified 27 selected files against the
production source lock's SHA-256, byte count and Git blob identity. It also
verified 42 existing translation/requirement spans against their hashes and
inclusive line ranges. The runtime distribution matches its retained hash.
These checks establish provenance and bounded cache observations only.

The protected baseline check found root's concurrent updates to parent
`prompt.md`, `delivery.md` and `progress.md`. Root confirmed ownership and
prior preservation of those files. Production/code, specification, phase,
source-lock and batch files matched the clarification baseline.

Only this record and owned clarification scratch were written. No build,
runtime execution, dependency acquisition, production or parent metadata
change, commit or push occurred. All owned commands completed; no delegated
work, background process or tool session remains live.

[prompt]: prompt.md
[review]: ../reviews/nextflow-research-report-review-02.md
[facts]: ../../../.tmp/agent/nextflow-conformance/pilot-clarification01/facts.json
[checker]: ../../../.tmp/agent/nextflow-conformance/pilot-clarification01/check_facts.py
