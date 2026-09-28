# Nextflow Conformance Foundation Specification

## Overview

Build a Go development tool that records what the pinned Nextflow sources
say, which behaviours need tests, which tests ran, and what their results
prove. Its first completion claim is `foundation-bootstrap`, over the
explicit corpus below. It must expose missing work and reject corrupted
accounting and evidence. It does not implement a Nextflow interpreter.

Target Nextflow 26.04.6 with `NXF_SYNTAX_PARSER=v2`. wr remains pure Go and
must not invoke Nextflow in production. The restored develop tree has no
Nextflow adapter. Consequently this delivery can prove its own verifier and
run a real Nextflow oracle, but cannot report any wr DSL2 runtime passes.
Full target inventory and runtime conformance are separate milestones.

Machine-readable records become the authority during implementation.
Generated Markdown presents those records without changing their meaning.
Source preservation is mechanical; interpretation requires independent
review. Neither record counts nor finite tests establish that prose is
semantically exhaustive or software is bug-free.

## Architecture

### Repository fit and boundaries

Add public package `conformance/` and developer entry point
`cmd/wr-conformance/main.go`. Follow the existing standalone
`cmd/wr-testsuite` pattern. Do not add a command to the production `wr` CLI
or a dependency from `jobqueue`, `queue`, or the production executable.
Use the standard library and existing GoConvey dependency. This work needs
no Markdown renderer, plugin framework, database, or generic test adapter.

The only new public Go API is:

```go
// Run executes the developer CLI and returns its process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int
```

The CLI owns argument parsing and delegates to private domain functions in
`conformance/`. `cmd/wr-conformance/main.go` supplies cancellation and exits
with the returned code. Context cancellation reaches network requests,
subprocess groups, pipe readers, and temporary-file cleanup.

Use these files; tests live beside their corresponding source files:

```tsv
Path	Role	Authority	Owner
conformance/model.go	Decode and validate records	Schema rules	Foundation
conformance/source.go	Acquire and verify pinned bytes	Source lock	Foundation
conformance/extract.go	Partition bytes and enumerate units	Pinned bytes	Foundation
conformance/coverage.go	Check links and scope	Reviewed records	Foundation
conformance/runner.go	Discover and run Go tests	Go JSON events	Foundation
conformance/evidence.go	Check freshness and artifacts	Recorded inputs	Foundation
conformance/oracle.go	Run pinned Nextflow cases	Actual subprocess	Foundation
conformance/render.go	Generate reports and handoffs	Validated records	Foundation
conformance/testdata/	Independent fixtures and mutations	Reviewed fixtures	Foundation
conformance/data/	Versioned target and ledger	Reviewed JSON	Foundation
conformance/data/cases/	Workflow inputs and expectations	Reviewed files	Foundation
.tmp/conformance/	Acquired blobs and run artifacts	Disposable cache	Developer
.docs/nextflow-conformance/generated/	Specs, reports, checklists	Generated view	Foundation
```

The existing job queue supplies dependencies, container execution, resources,
and job metadata. Reuse decisions belong to later runtime design after
behavioural tests. A queue priority assertion cannot establish output order.
The historical branch supplies candidate regression inputs only.

### Target, acquisition, and trust

The target lock fixes these independently checked identities:

```tsv
Object	Identity	Acquisition	Meaning
release	26.04.6	GitHub release API	Oracle version
annotated-tag	38ce286fe70b44a5907cf1f5b0b8fb13bd836721	Git object	Release tag
source-commit	232b60569865e9a4577e48c1955409238359d6ca	Git object	Source tree
parser	v2	NXF_SYNTAX_PARSER	Strict syntax
launcher	61a755edbed743cfbb568f3a6c67af68481a2f6a4d6dffcc4295e51318968281	Release asset SHA-256	nextflow
runtime-dist	182a63c74074e2dc7956ffa3c8cd59de952ed2c44394e21faf5e1736b945444c	Release asset SHA-256	nextflow-26.04.6-dist
```

Release asset hashes above come from release metadata inspected during
specification. Implementation must download and hash the bytes and record
that evidence. This spec does not claim that an oracle ran during research.

Acquire the commit's complete source tree and immutable Git tree listing.
Check Git blob IDs as well as SHA-256 file hashes; store the tree object's
identity and verify its entries against the pinned commit. Preserve file
modes, original line endings, licenses, and every file's repository path.
Reject a truncated tree response. Archive layout is not source identity.
Never substitute a mutable documentation website or the latest release.

The pinned `modules/nf-lang/build.gradle` declares
`me.sunlan:antlr4:4.13.2.6`, `org.apache.groovy:groovy:4.0.31`, and
`org.pf4j:pf4j:3.14.1`. Capture these coordinates, their POMs, source
artifacts when used for semantic review, and actual runtime JAR hashes.
Record the resolved runtime dependency closure, including transitive JARs,
from the distribution. A coordinate or a Gradle declaration alone is not
proof that those bytes were acquired. Grammar source lives in this release's
`modules/nf-lang`; it is not an unspecified external parser checkout.

`acquire` receives an existing Java 21 home. Hash every regular file and
symlink target in that tree, record `java -version`, OS and architecture,
and verify the executable comes from that tree. Never install system
packages. Prepare a private Nextflow home with the pinned distribution and
all runtime dependencies. Disable automatic updates and plugins for these
local bootstrap cases. The acquired environment must run offline with an
empty user home. Missing Java or runtime dependencies are explicit failures.

Only `acquire` may access the network. It writes a candidate lock and cache;
`validate`, `extract`, `render`, `discover`, `run`, and `verify` never fetch.
A reviewed lock is a checked-in input, not something verification repairs.
Acquisition cannot replace a reviewed lock or existing evidence on failure.
Use HTTPS, ten-second connection and sixty-second request deadlines, at
most two attempts per object, a fifteen-minute total deadline, and at most
four concurrent requests. Limit an object to 256 MiB, unpacked sources to
512 MiB and 50,000 entries, and a selected text file to 8 MiB. Fail explicitly
on a limit; never truncate data and call it complete.

Reject archive traversal, absolute member paths, duplicate members, unsafe
symlinks, non-regular executable inputs, and local paths escaping the corpus
or run root. Resolve relative include references against their source file.
Network acquisition can use reviewed immutable URLs from the lock only.

### Records and schema

All authoritative files are UTF-8 JSON, schema version `1`, with final
newline. Use sorted record arrays by ID and deterministic field order when
writing. Reject unknown fields, duplicate JSON object keys, invalid UTF-8,
trailing JSON values, duplicate IDs, empty required text, and absent required
fields. Decode numbers without conversion through floating point. IDs use
`[A-Z][A-Z0-9_]*`; file IDs use repository-relative POSIX paths. Hashes are
64 lowercase hex digits. Timestamps are RFC3339 UTC, never freshness proof.

Implement these closed schemas in `model.go`; emit matching schema documents
in `conformance/data/schema/` from that same definition. Keep schema emission
limited to these records; it is not a reusable schema framework. The fields
below are required unless explicitly optional.
A `file_ref` is `{path, sha256, bytes}`. A `span` is
`{file, start, end, sha256}`, using zero-based half-open byte offsets.
IDs referring to other records must resolve in the same target revision.

```tsv
Record file	Record key	Fields beyond key	Constraints
target.json	id	version, parser, tag, commit, source_tree, profiles, decisions	One nonempty target
sources.lock.json	id	origin, revision, tree, files, artifacts, environment	One reviewed lock
blocks.json	id	span, kind, parent, children, include_refs	Extracted source units
obligations.json	id	block_ids, facet, statement, disposition, requirement_ids, rationale, review_id	Independent semantic inventory
requirements.json	id	origin, text, facets, dependencies, interactions, scope, decision_ids, uat_ids, review_id	Behaviour or product requirement
uats.json	id	requirement_ids, facet_ids, kind, readiness, fixture, expected, cases, binding, timeout_seconds, review_id	Concrete acceptance contract
reviews.json	id	author, reviewer, verdict, input_hashes, source_spans, findings	Distinct author and reviewer
decisions.json	id	question, state, affected_ids, rationale, resolution, review_id	Unresolved or resolved scope
bindings.json	id	uat_id, package, test, source_files, evidence_kind	One exact top-level Go test
batches.json	id	assigned_ids, depends_on, source_spans, inputs, commands, completion	Bounded handoff
attempts/<id>.json	id	suite, inputs, discovery, events, results, artifacts, exit, started, ended	Runner-produced evidence
```

Array contents and discriminated fields follow these rules:

- `profiles` has exactly `foundation-bootstrap`, `target-inventory`, and
  `wr-runtime`. Each holds sorted `required_ids`, `required_cases`,
  `required_reviews`, and `required_gates`. Empty required sets fail. The
  bootstrap membership is the fixed selection in A2 and E1, not whatever
  happens to be extracted. Later profiles contain the unresolved seeds in
  F1 and expand through reviewed changes.
- `files` enumerates every source-tree entry with path, Git blob ID, mode,
  SHA-256, byte count, and selection state. States are `selected`,
  `unreviewed`, or `nonsemantic`. `nonsemantic` requires rationale and an
  independent review. `unreviewed` is outstanding work, never exclusion.
- `artifacts` contains role, immutable origin, `file_ref`, and dependencies
  by artifact ID. Roles include source, launcher, runtime, JAR, Java, and
  environment-tool. Dependency cycles and missing entries fail validation.
- Block kinds are `heading`, `paragraph`, `list-item`, `table-row`,
  `definition`, `code`, `directive`, `grammar-rule`, `declaration`,
  `test-case`, `trivia`, or `unclassified`. A node with children is a
  container; leaf spans partition every byte exactly once.
- `facet` and requirement `facets` name explicit defaults, alternatives,
  overloads, errors, boundaries, examples, and interactions. An obligation
  has disposition `behaviour`, `nonrequirement`, or `pending`.
  `nonrequirement` requires a specific rationale and independent review;
  `pending` contributes to incomplete counts. Broad explanations such as
  "documentation only" cannot discharge examples or warnings.
- Requirement origin is `nextflow` or `wr`. Scope is `required`,
  `pending-decision`, or `excluded`. Exclusion requires a resolved decision
  naming affected IDs and approving reviewer. Execution status is never
  stored in this field. Empty dependencies or interactions are explicit
  arrays; review must state why interactions are absent where relevant.
- UAT kind is `foundation`, `oracle`, `differential`, or `wr-runtime`.
  Readiness is `draft` or `ready`. The following executable-contract rules
  apply to `ready`; a `draft` instead carries its measurable contract in
  `cases`, with null fixture/expected/binding/review fields. Drafts are
  outstanding work and cannot satisfy any execution or reviewed-UAT gate.
  `fixture` is a nonempty list of hashed input files and literal arguments.
  `expected` contains machine-checkable observations, not English alone.
  Each `cases` entry has ID, purpose (`happy`, `error`, `boundary`, or
  `interaction`), inputs, expected output, and an obligation-facet reference.
  Every reviewed facet links to at least one case, or a reviewed explanation
  of why no executable observation exists. That explanation does not count
  as a runtime pass. `binding` may be null only for outstanding work.
- `review` verdict is `accepted` or `changes-required`. `input_hashes`
  includes source, obligations, requirement, UAT, normalization, and test
  binding bytes actually reviewed. An accepted review becomes stale when
  any bound input changes. Reviewer identity is provenance, not automated
  proof of independence; code review verifies the separate review occurred.
- An unresolved decision has nonempty question and affected IDs, null
  resolution, and null resolution review. A resolved decision requires
  resolution text and an accepted review bound to the new record.
- Bindings allow only package paths within this module and test names
  matching `TestUAT_[A-Z0-9_]+`. `evidence_kind` matches the UAT kind.
  Multiple requirements may share a UAT only when its cases identify all
  their facets. A binding cannot claim that an oracle test executed wr.
- Attempt input fields are specified in D2. Results are computed states,
  never editable assertions. Artifacts are `file_ref` values relative to
  the attempt root. Records support no arbitrary shell command or plugin.

A placeholder description is invalid: whitespace-only text, `TODO`, `TBD`,
`placeholder`, and the sentence `TODO: describe expected behaviour.` from the
archived generator are rejected case-insensitively when used as the entire
description or as a stand-in sentence. The archived form prefixes this
sentence with a backticked heading and punctuation; reject that form too.
Do not ban these strings inside quoted source, fixtures, or explicit negative
tests. Meaningful descriptions still need review;
this check is a regression guard, not a semantic classifier.

### Commands and result contract

Commands run from the repository root. `--root` selects the corpus directory;
`--cache` selects the acquired cache. Their defaults are
`conformance/data` and `.tmp/conformance`. Test fixtures use `t.TempDir()`.
Resolve paths once and reject output inside source inputs.

```bash
go run ./cmd/wr-conformance acquire --java-home /opt/java21
# Review and commit the candidate lock before the remaining commands.
go run ./cmd/wr-conformance validate
go run ./cmd/wr-conformance extract --check
go run ./cmd/wr-conformance render --check
go run ./cmd/wr-conformance discover --suite foundation-bootstrap
go run ./cmd/wr-conformance run --suite foundation-bootstrap
go run ./cmd/wr-conformance verify --suite foundation-bootstrap
go run ./cmd/wr-conformance verify --suite target-inventory
go run ./cmd/wr-conformance verify --suite wr-runtime
```

`acquire` accepts optional `--lock-candidate PATH`; the default is inside the
cache. It prints acquisition results even if no object succeeds. `extract`
and `render` without `--check` write generated candidates atomically; their
check forms compare expected bytes without changing inputs. Semantic
records are authored and reviewed separately, never invented by extraction.
`run` discovers first, executes once with a new attempt ID, and verifies the
result. `verify` revalidates existing evidence against current inputs.
No `--allow-missing`, imported success status, or silent partial-success flag
exists. `oracle --case ID` is an internal development command for the real
oracle UATs; it cannot itself award a wr pass.

Every command writes one JSON result to stdout and diagnostics to stderr.
The result contains `schema`, `command`, `suite` (null if inapplicable),
`claim`, `complete`, `counts`, `diagnostics`, and optional `attempt_id`.
Counts include `verified_artifacts`, `selected_files`, `selected_blocks`,
`pending_blocks`, `draft_uats`,
`requirements`, `uats`, `discovered`, `executed`, `passed`, `failed`,
`skipped`, `timed_out`, `stale`, `blocked`, and `unresolved_decisions`.
Print zero counts explicitly. Diagnostic objects contain stable `code`,
`record_id`, `path`, and specific `message`; absent location fields are null.
Sort diagnostics by code, record ID, and path.

Exit codes: `0` means this command's requested claim holds; `1` means valid
inputs with incomplete or failed obligations; `2` means malformed input,
missing prerequisite, corrupt evidence, I/O failure, or bad invocation.
`validate` returning 0 only proves record validity. Its claim is
`records-valid`, not `conformant`. A failed command never emits
`complete:true`. `verify` uses the requested profile name as its claim and
lists other profiles as incomplete without folding their counts into a
misleading combined percentage. Unknown commands or suites return 2.

## Section A: Pin and enumerate sources

### A1: Verify every acquired input

As a maintainer, I want immutable source and runtime identities, so that
results can be reproduced without consulting moving upstream content.

**Package:** `conformance/`
**File:** `conformance/source.go`
**Test file:** `conformance/source_test.go`

Acquisition is a transaction. Every expected source and artifact must be
present and hash-verified before publishing a candidate lock. Preserve the
previous lock and cache generation after any failure. Offline verification
rehashes all selected files, all included files, and all runtime artifacts.
Missing files never become empty files. Additional source files appear in
the tree accounting and cannot silently alter the selected corpus.

**Acceptance tests:**

1. `A1_01`: Acquire a local HTTPS fixture with three declared blobs and a
   complete pinned tree. All three hashes match; acquisition returns 0,
   reports `verified_artifacts:3`, and a subsequent offline validation
   succeeds with the server stopped.
2. `A1_02`: Return HTTP 503 for all three blobs. Acquisition returns 2 with
   `E_FETCH`, reports zero verified blobs, publishes no candidate lock,
   and leaves an existing lock byte-identical. A single failed blob among
   two successful blobs has the same unsuccessful result.
3. `A1_03`: Change one cached byte, remove one file, change the release
   commit, and truncate the tree response in separate subcases. Validation
   returns 2 with respectively `E_SOURCE_HASH`, `E_SOURCE_MISSING`,
   `E_TARGET_IDENTITY`, and `E_TREE_INCOMPLETE`.
4. `A1_04`: Supply an archive member `../escape`, a symlink outside the
   cache, or an object one byte over its limit. Return 2 with
   `E_SOURCE_PATH` or `E_SOURCE_LIMIT`; no file appears outside the cache.
5. `A1_05`: Remove a parser runtime JAR or alter the Java tree after
   acquisition. Offline preflight returns 2 with `E_RUNTIME_MISSING` or
   `E_RUNTIME_HASH`, before starting Nextflow. Network request count is 0.

### A2: Preserve source units without declaring their meaning complete

As an inventory reviewer, I want every selected byte and meaningful unit
visible, so that omitted warnings and alternatives cannot hide behind a
heading count.

**Package:** `conformance/`
**File:** `conformance/extract.go`
**Test file:** `conformance/extract_test.go`

Extract these complete source files from the pinned commit:

- `docs/reference/syntax.md`
- `docs/reference/process.md`
- `docs/reference/operator.md`
- `docs/strict-syntax.md`
- `docs/migrations/26-04.md`
- `modules/nf-lang/src/main/antlr/ScriptParser.g4`
- `modules/nf-lang/src/main/antlr/ScriptLexer.g4`
- `modules/nf-lang/build.gradle`
- `modules/nextflow/build.gradle`
- Under `modules/nf-lang/src/test/groovy/nextflow/script/parser/`,
  `ScriptAstBuilderTest.groovy`.
- `modules/nextflow/src/test/groovy/nextflow/extension/MixOpTest.groovy`
- `modules/nextflow/src/test/groovy/nextflow/processor/TaskConfigTest.groovy`

Also acquire, extract, and enumerate the local files referenced by
`literalinclude` and `include` directives, recursively. Record all other
cross-references as edges to resolved tree paths or outstanding external
source records. Cyclic includes and missing targets fail. A cross-reference
does not automatically enlarge the semantic bootstrap boundary.

Support the syntax actually present: fenced code and MyST directives,
colon directives, nested lists, definition-list signatures and options,
Markdown tables including separator rows, HTML headings, anchors, and
ordinary prose.
Preserve unfamiliar constructs as `unclassified`; do not drop them or
pretend they are trivia. Grammar alternatives are distinct children of a
rule. Source declarations and upstream test methods retain exact spans;
unsupported source structure remains visible as `unclassified`.

Every file has a root covering `[0,file_size)`. Children partition their
parent span, in original order, with no holes or overlaps. Semantic leaves
include table cells/rows, list items, options, signatures, examples, and
warnings; whitespace and punctuation remain in explicit trivia leaves.
IDs derive from file identity, span, kind, and content hash, not heading
slug alone. Regeneration of unchanged bytes produces identical IDs.
Changes create a reconciliation list of removed/new IDs and stale reviews;
never auto-transfer an old review based on similar headings.

Bootstrap semantic review covers these regions, including their complete
nested content and included snippets:

- `docs/reference/process.md`: `process-reference-typed` through the next
  same-level anchor `process-reference-legacy`; and `process-fair` through
  the next directive's anchor.
- `docs/reference/operator.md`: `operator-map` and `operator-mix` through
  their next same-level operator anchors.
- `docs/strict-syntax.md`: `Import declarations` through the next heading
  at that level.
- `docs/migrations/26-04.md`: from the HTML heading
  `<h3>Static typing (preview)</h3>` to the next HTML h3 heading, including
  typed-process/workflow examples and feature-flag changes.
- Grammar rules `processDef`, `processBody`, `processInput`, `workflowDef`,
  `workflowBody`, `workflowTake`, and `workflowEmit`, all alternatives.
- Every test method in `MixOpTest.groovy` and the dependency declarations
  in the two selected Gradle files.

Resolve these selectors exactly once into byte spans and hashes in the
reviewed bootstrap profile. Absent or ambiguous selectors fail preparation;
authoring cannot shrink them to make a gate pass. All remaining blocks in
the selected files stay enumerated with `pending` semantic disposition.
Foundation completion reports those counts and explicitly leaves
`target-inventory` incomplete. The initial bootstrap is small in reviewed
semantics, not a claim that unreviewed text has no requirements.

**Acceptance tests:**

1. `A2_01`: Use an independently hand-labelled fixture with one heading,
   two definition signatures, three options, a two-data-row table, a nested
   warning, a code example, and an include. Extraction matches the exact
   reviewed spans and kinds for all items and reconstructs original bytes
   exactly. Verify both LF and CRLF inputs and a final line without newline.
2. `A2_02`: Extract the real files twice. IDs and bytes match; each root
   covers its original file, and every leaf partition sums to the file
   length. Included map/mix snippets resolve to verified blobs. The report
   has nonzero pending blocks outside the bootstrap boundary.
3. `A2_03`: Remove a warning from generated blocks, collapse the two
   `stageAs` signatures, or delete one grammar alternative. `extract
   --check` returns 1 with `E_EXTRACTION_MISMATCH`, naming the missing span.
   It does not rebuild its expected list from the corrupted output.
4. `A2_04`: A duplicate heading has two distinct IDs; an unknown directive
   produces `unclassified` and blocks semantic completion for its region.
   A missing include yields `E_INCLUDE_MISSING`; a cycle yields
   `E_INCLUDE_CYCLE`; both return 2.

## Section B: Review behavioural meaning and scope

### B1: Link source facets to independently reviewed obligations

As a reviewer, I want requirements checked against original source spans,
so that a self-consistent generated manifest cannot conceal omitted meaning.

**Package:** `conformance/`
**File:** `conformance/coverage.go`
**Test file:** `conformance/coverage_test.go`

An author proposes obligations while reading the original source. A separate
reviewer checks the original pinned spans, including surrounding text,
defaults, overloads, examples, warnings, grammar, declarations, and upstream
tests. The reviewer supplies findings and accepted obligations; extraction
never manufactures an accepted review. Review IDs bind exact input hashes.
Check that every bootstrap semantic leaf has an obligation or reviewed
nonrequirement explanation. Container coverage cannot discharge children.

For a paragraph containing several behaviours, create distinct facets even
though they share its span. Every required obligation resolves through a
requirement and UAT case. Verify bidirectional links and reject orphan UATs,
requirements without origin spans or wr provenance, unresolved references,
and contradictory scope. Check dependencies for cycles and list applicable
interaction UATs. Semantic review can expose gaps that static validation
cannot detect; the report keeps that distinction.

**Acceptance tests:**

1. `B1_01`: A source paragraph declares a default and an error. Independent
   obligations `DEFAULT` and `ERROR` map to two UAT cases; coverage returns
   0 for that region. Remove `ERROR`'s mapping while leaving its paragraph
   covered. Return 1 with `E_FACET_UNCOVERED` and one incomplete facet.
2. `B1_02`: In the real typed section, preserve separate `Path` and
   `Iterable<Path>` `stageAs` overloads, all `file()` option defaults, the
   `files()` exception to `optional`, and the feature flag. Collapse the
   overloads in requirements only. Return 1 with `E_FACET_UNCOVERED` even
   when all source blocks still have mappings.
3. `B1_03`: A fixture with ten descriptions equal to the archived generator
   template fails `E_PLACEHOLDER` ten times, returns 2, and awards zero
   reviewed requirements. An empty target, empty source set, or empty
   bootstrap required list fails `E_EMPTY_CORPUS` with exit 2.
4. `B1_04`: A duplicate ID, dangling UAT link, and dependency cycle each
   fail with `E_DUPLICATE_ID`, `E_REFERENCE`, and `E_DEPENDENCY_CYCLE`,
   respectively. No generated completion checklist is published.
5. `B1_05`: An author approves their own semantic review or an accepted
   review predates an altered expectation. Return 1 with
   `E_REVIEW_INDEPENDENCE` or `E_REVIEW_STALE`; preserve the old review as
   historical evidence and do not count it as current acceptance.

### B2: Keep policy decisions separate from observations

As a product owner, I want unresolved compatibility policy visible, so that
it cannot disappear into exclusions or observed test success.

**Package:** `conformance/`
**File:** `conformance/model.go`
**Test file:** `conformance/model_test.go`

Seed unresolved decisions `D_TYPED_MILESTONE` and `D_JVM_PLUGIN_POLICY`.
The first names typed processes, workflows, records, and feature-flag
handling and asks which implementation milestone owns them. The second
names fully qualified JVM calls, `lib` code, arbitrary Java/Groovy
libraries, and Nextflow plugins and asks the supported compatibility policy.
Neither is an exclusion. Their affected requirements remain in the target
inventory even when an oracle successfully executes one example.

A bootstrap profile may complete its accounting checks while these product
decisions remain unresolved. `target-inventory` cannot claim a settled
scope, and `wr-runtime` cannot claim completion for affected semantics.
Future exclusions require explicit decisions; archived exclusions confer
no authorization. Any temporary unsupported execution must return a
precise feature/location diagnostic and nonzero exit, with no successful
changed-meaning result. Implementing those errors belongs to runtime work.

**Acceptance tests:**

1. `B2_01`: Seed both decisions and their affected requirements. Validation
   succeeds, reports two unresolved decisions, and runtime verification
   returns 1 with `E_SCOPE_UNRESOLVED`. Neither item is counted excluded.
2. `B2_02`: Mark an affected requirement `excluded` without a resolved
   decision and accepted review. Validation returns 2 with
   `E_SCOPE_EXCLUSION`. Changing its latest observation to pass does not
   change this outcome.
3. `B2_03`: Resolve a fixture decision with reviewer and bound hashes.
   Scope validation accepts that resolution; an existing failed runtime
   observation remains failed. No scope operation creates a pass event.

## Section C: Define UATs and discover their executable tests

### C1: Store executable expectations and generate readable specifications

As an implementor, I want concrete inputs and observable results for every
assigned behaviour, so that I can write a meaningful failing test.

**Package:** `conformance/`
**File:** `conformance/render.go`
**Test file:** `conformance/render_test.go`

Expectations use a fixed observation format with fields `exit`, `values`,
`artifacts`, `tasks`, and `diagnostics`. Each field is explicitly checked or
has a reviewed `not-applicable` reason; absent fields are invalid. Values
are typed JSON, with mode `sequence` or `multiset`. Multisets preserve
multiplicity. Artifact checks specify relative logical name, byte count,
SHA-256, and optionally exact UTF-8 content. Task checks specify logical
IDs, expected count, and declared ordering constraints. Diagnostics specify
stage, category, source location when known, and required message literals.
Matching a nonzero exit alone is insufficient for an expected error.
An error contract with no emitted values declares `values: []`; this is a
checked empty sequence, not `not-applicable`. Zero value lines pass only
when all declared error, task, exit, and artifact checks also pass.
Observation records retain raw event/file references. Verification replays
the fixed observation decoder against those bytes and checks the permitted
normalizations; a supplied summary cannot replace the original sequence.

Normalization is an explicit per-UAT list from this closed set:
`run-root` replaces only the actual generated root prefix;
`line-ending` converts CRLF to LF in a named text observation;
`multiset` applies only to a named unordered value field. Store before and
after values. No global sorting, whitespace stripping, wildcard regex,
error suppression, or artifact exclusion. Expected bytes are authored from
source and review; execution never overwrites them with observed output.
Doc/oracle disagreement creates an unresolved decision and incomplete UAT.

Render requirement specs, UAT cases, provenance, scope decisions, evidence
links, and checklists from records. Include stable IDs in every generated
item. Group generated pages by batch and dependency order. Escape source
Markdown and HTML so examples cannot alter the document structure. Use one
h1, ASCII prose, 80-column wrapping, and no placeholder headings. Preserve
non-ASCII fixture bytes in linked artifacts instead of altering them.

**Acceptance tests:**

1. `C1_01`: Render a requirement with two cases, one failed binding, and
   one unresolved decision. The generated page lists both case IDs and
   exact expectations, contains an unchecked item, and links that decision.
   Repeat rendering byte-identically; `render --check` returns 0.
2. `C1_02`: Remove one UAT, replace an expected artifact hash, or hand-edit
   a generated checkbox. Return respectively `E_UAT_MISSING`,
   `E_REVIEW_STALE`, or `E_RENDER_MISMATCH`; exit is nonzero.
3. `C1_03`: Compare `[1,1,2]` with `[1,2,2]` in multiset mode: fail.
   Compare `[2,1]` with `[1,2]` in sequence mode: fail. Compare those two
   lists in reviewed multiset mode: pass. A proposed global-sort rule is
   rejected with `E_NORMALIZATION` before comparison.
4. `C1_04`: Reviewed comparison fixtures for the import and missing-output
   contracts in E1 each have zero value lines and all required error
   observations; both pass. Independently replace each fixture's diagnostic
   with a missing-Java error, retaining its nonzero exit, or append one
   `OBS:1` line to its raw stdout. Every changed fixture fails
   `E_EXPECTATION`. Expected files remain byte-identical. These checks are
   foundation evidence; E1_01 supplies the actual oracle evidence.

### C2: Require exact discoverable Go test bindings

As a maintainer, I want each UAT bound to a real selected test, so that a
filename or an invented test name cannot count as coverage.

**Package:** `conformance/`
**File:** `conformance/runner.go`
**Test file:** `conformance/runner_test.go`

Use `go list -json` to resolve package files under the current build settings
and `go test -list` to discover top-level tests. Every required UAT has one
exact package/test binding. Run with `-count=1 -json -tags netgo` and an
anchored, regexp-escaped test selector. Record discovery output and actual
argv. Reject package patterns and arbitrary commands in bindings. Verify
selected test source is active in the package under those build settings;
a build-tagged-out test is missing, not exempt.

All acceptance tests in this spec map one-to-one to GoConvey functions named
`TestUAT_<acceptance-ID>` in the listed test files. Unit fixtures can use
helper subprocesses and temporary tiny Go packages to challenge discovery
and event handling. Mark their evidence `foundation`; these fixtures cannot
be promoted to oracle or wr-runtime proof. Assertions must exercise public
CLI/results or other observable boundaries, not mere source text presence.

**Acceptance tests:**

1. `C2_01`: Discover two fixture UAT bindings to `TestUAT_ONE` and
   `TestUAT_TWO`. Report exactly two discovered tests. A declaration named
   only in a comment or a file path is not discovered.
2. `C2_02`: Bind to missing `TestUAT_THREE`, a test excluded by current
   build constraints, or an invalid package in separate subcases. Return
   1 with `E_TEST_MISSING` or 2 with `E_TEST_DISCOVERY`; executed count is 0.
3. `C2_03`: Bind `TestUAT_ONE` beside `TestUAT_ONE_EXTRA`. The runner
   executes only `TestUAT_ONE`. A binding containing regex metacharacters
   fails schema validation instead of broadening selection.
4. `C2_04`: Map a required runtime UAT to an oracle-only or fixture test.
   Validation returns 2 with `E_EVIDENCE_KIND`, even if that test passes.

## Section D: Record execution and invalidate old evidence

### D1: Derive status from complete execution events

As a reviewer, I want proof that each test started and finished, so that
zero-test runs and skipped tests remain incomplete.

**Package:** `conformance/`
**File:** `conformance/runner.go`
**Test file:** `conformance/runner_test.go`

Capture raw Go JSON events, stdout, stderr, exit, and artifact receipts from
one runner-owned invocation. A passing test requires matching `run` and
terminal `pass` events for its exact package/name, package pass, successful
process exit, and all its required observation artifacts. All selected
subtests must finish without skip/fail. Unknown, duplicate, contradictory,
truncated, or unmatched terminal events invalidate the attempt. A test that
starts but never finishes is incomplete. Package success alone is not test
success. No events or "no tests to run" means zero executed UATs.

Use per-UAT deadlines from 1 to 180 seconds and a twenty-minute suite
ceiling. Kill the entire process group at the deadline, drain bounded pipes,
and wait for child cleanup. Limit each log to 16 MiB; exceeding the limit
fails `E_OUTPUT_LIMIT` and terminates the process instead of truncating a
successful record. Mark an interrupted attempt incomplete. Atomic rename
publishes its final manifest after all logs are closed and hashed. An
in-progress manifest is never considered passing evidence.

**Acceptance tests:**

1. `D1_01`: Execute one real passing fixture with run/pass/package-pass
   events and exit 0. Counts are executed 1 and passed 1; its raw event log
   and manifest hashes verify.
2. `D1_02`: Execute separate fixtures that skip, fail, exceed a 1-second
   limit, exit 0 with no matching test, and omit a required observation.
   Each returns 1 with respectively `E_TEST_SKIPPED`, `E_TEST_FAILED`,
   `E_TEST_TIMEOUT`, `E_TEST_NOT_RUN`, and `E_OBSERVATION_MISSING`. None
   increments passed; timeout cleanup leaves no child process alive.
3. `D1_03`: Feed malformed/truncated JSON, an unmatched pass, duplicate
   terminal events, or a passing test in a failing package through the
   recorder boundary. Return 2 with `E_TEST_EVENTS`, or 1 with
   `E_TEST_FAILED` for the failing package. Reject success in every case.
4. `D1_04`: Interrupt the runner between log creation and manifest publish.
   `verify` returns 1 with `E_ATTEMPT_INCOMPLETE`. A previous attempt from
   different inputs cannot substitute for the interrupted one.

### D2: Bind evidence to the code, corpus, tests, and environment

As a maintainer, I want changes to invalidate affected results, so that old
passes cannot certify a new implementation or altered expectation.

**Package:** `conformance/`
**File:** `conformance/evidence.go`
**Test file:** `conformance/evidence_test.go`

An attempt records these inputs before execution and rechecks them after:
Git commit; dirty source content digest; target and lock hashes; every
semantic record and review hash; workflow, input, expectation, and
normalization hashes; executable and active Go source hashes; `go.mod` and
`go.sum`; Go compiler identity; build tags and flags; OS/architecture;
locale/timezone; explicit child environment; Java/runtime/dependency hashes
when applicable; runner version; and discovery output hash.

The dirty source digest covers tracked and untracked Go source, embedded
inputs, module/build configuration, and all corpus inputs. Use `go list`
file/dependency enumeration and the repository input inventory; include
files outside the bound package that can affect execution. Generated views,
cache, and attempt outputs are the only excluded output classes. Keep the
exclusion list fixed in the tool; records cannot add arbitrary exclusions.
A clean commit ID alone cannot stand in for content hashes. Changing an
input during a run yields `E_INPUT_CHANGED` and no pass.

Start child processes from an allowlisted environment. Record every passed
key/value, except secrets are forbidden in bootstrap test environments.
Include PATH tool resolutions, shell binary, Java and Go binaries, and
executor settings. User Nextflow config, plugins, startup scripts, and
ambient credentials must not influence bootstrap observations. Record
normalized environment values separately from the complete effective
values used to assess freshness.

`verify` recomputes current inputs and artifact hashes. A stale attempt stays
on disk as history but cannot count as current. Select the newest completed
attempt for the exact suite/input key; a newer failure with that same key
supersedes an older pass. Never search backwards for a convenient success.
Validate every result against raw events and observed artifacts again.
Hand-edited status fields do not override that derivation.

**Acceptance tests:**

1. `D2_01`: Verify a completed fixture without changes: pass. Independently
   change implementation bytes without changing Git HEAD, a corpus block,
   test bytes, expected bytes, normalization, build tags, or a runtime JAR.
   Each becomes stale with `E_EVIDENCE_STALE` and passed count 0.
2. `D2_02`: Delete a raw event log or change artifact bytes. Return 2 with
   `E_EVIDENCE_MISSING` or `E_ARTIFACT_HASH`. Editing manifest status to
   `passed` does not change the failure.
3. `D2_03`: Modify input bytes while a barrier-controlled test is running.
   The attempt finishes with `E_INPUT_CHANGED`, exit 1, and no current pass.
4. `D2_04`: Record a passing attempt followed by a failing attempt with
   identical input hashes. Verification selects the failure. Restore the
   first attempt's timestamp to a later value; selection still uses the
   runner's monotonic attempt sequence, not editable wall-clock time.
5. `D2_05`: Change only a generated Markdown page. `render --check` fails,
   but the recorded runtime input key is unchanged. Re-rendering restores
   view consistency without pretending a test ran again.

## Section E: Prove the foundation on real sources and hostile fixtures

### E1: Run real pinned oracle cases with honest claim boundaries

As an implementor, I want a small actual oracle baseline, so that later
runtime comparisons have tested observation and error contracts.

**Package:** `conformance/`
**File:** `conformance/oracle.go`
**Test file:** `conformance/oracle_test.go`

Run the acquired distribution with Java 21, strict parser v2, local executor,
fixed two-task concurrency, no plugins, isolated work and home directories,
and network access denied by the test environment. Record the actual
version output, argv, environment, trace, stdout, stderr, and produced files.
An offline flag alone is not proof that a missing dependency did not fetch.
The validation environment must enforce network denial and record its
mechanism; inability to enforce it leaves the offline-oracle gate incomplete.

Each case is an actual `.nf` file and config, independently reviewed before
execution. Static typing is disabled for the seven executable bootstrap
cases. The distinct typed `map` null behaviour remains a separate inventoried
facet with an explicit flag-enabled UAT contract, pending its milestone.
Use these minimum cases; their expected results are author-written contracts,
not reported observations from this specification:

```tsv
Case	Input	Expected observation	Comparison
ORACLE_MAP	channel.of(1,2,3).map { it * 2 }	values [2,4,6], tasks 0, exit 0	Sequence
ORACLE_MAP_NULL	channel.of(1,2,3).map { it == 2 ? null : it }	values [1,3], tasks 0, exit 0	Sequence
ORACLE_MIX	channel.of(1,1).mix(channel.of(2))	values [1,1,2], tasks 0, exit 0	Multiset
ORACLE_EMPTY	channel.empty().map { it * 2 }	values [], tasks 0, exit 0	Sequence
ORACLE_IMPORT	import groovy.json.JsonSlurper before workflow	values [], compile diagnostic at import, tasks 0, nonzero exit	Diagnostic contract
ORACLE_FAIR	Two file-producing tasks A then B, fair true	values [A,B], two files A\n and B\n, tasks 2	Emission sequence
ORACLE_FILE_ERROR	Required path absent after successful task script	values [], missing-output diagnostic, tasks 1, script exit 0, workflow nonzero exit	Diagnostic contract
```

Minimal source for the first case:

```nextflow
workflow {
    channel.of(1, 2, 3)
        .map { it * 2 }
        .view { value -> "OBS:${value}" }
}
```

The observer accepts only explicitly prefixed values, checks their schema,
and retains all raw output. Value-bearing cases require all declared values.
`ORACLE_EMPTY` requires zero value lines, successful workflow completion,
and zero trace tasks. `ORACLE_IMPORT` and `ORACLE_FILE_ERROR` require zero
value lines and their reviewed error contracts, including diagnostic stage,
category, source location, message literals, task count, and nonzero workflow
exit. The missing-output case also requires proof of task script exit 0.
Absent values alone cannot establish an expected error. Missing required
values, extra values, or malformed observation lines fail; the observer
does not filter away unexpected observation lines.

For `ORACLE_FAIR`, submit A then B with `maxForks 2` and task tags A/B. A
waits on a file released by the Go supervisor only after the Nextflow
trace records B's completion. B creates its output and exits. The
supervisor then releases A. Assert the trace proves B completed before A,
while the downstream output observer receives A then B. Keep trace task
IDs and output artifact hashes. Use bounded polling under the case
deadline; absent completion evidence is a harness failure. A sleep-only
scheduling assumption is insufficient. Checking submission priority or
sorting collected outputs fails this UAT.

For file errors, distinguish process script exit 0 from workflow failure
collecting a missing required output. Diagnostic literals are captured from
the first real oracle run and independently reviewed against the source;
until approved, the case is incomplete. Do not weaken the diagnostic
contract to accept every nonzero exit. The same review procedure settles
exact parser diagnostic spelling for the import case.

The typed and JVM/plugin source obligations remain recorded with pending
implementation milestones. The real bootstrap requirements above have
executable oracle UATs; runtime counterparts retain missing bindings.
Foundation completion requires the oracle cases but never requires faking
those wr bindings. `verify --suite wr-runtime` returns 1 and
`E_ADAPTER_UNAVAILABLE` on the restored tree. Unit comparison fixtures are
labelled foundation evidence, and must not appear as differential passes.

**Acceptance tests:**

1. `E1_01`: Acquire actual pinned source and distribution bytes and record
   their verified hashes. Run all seven cases offline. All actual
   observations match independently reviewed expectations; the import and
   missing-output runs each emit zero value lines and pass their specific
   error contracts, including the missing-output task's script exit 0. The
   manifest identifies version 26.04.6 and parser v2. A missing prerequisite
   fails the test; there is no skip or simulated oracle substitute.
2. `E1_02`: In the actual fair run, trace order is B then A and observed
   emission order is A then B; each task produces exactly one file with
   the declared bytes. Removing downstream emission evidence makes the
   UAT incomplete even when priority and task counts match.
3. `E1_03`: Run bootstrap verification with the restored wr tree. Oracle
   evidence is reported as oracle only; wr-runtime returns 1 with
   `E_ADAPTER_UNAVAILABLE` and zero wr runtime passes. A fixture executable
   printing expected values cannot change that result.
4. `E1_04`: Supply a deliberately contradictory expectation for the map
   case. The actual oracle result fails `E_EXPECTATION`, retains raw
   observations, and produces an unresolved doc/oracle disagreement record
   candidate. The tool cannot approve the candidate or alter expected data.

### E2: Require every deliberate corruption to fail for its intended reason

As a maintainer, I want negative controls for the verifier, so that its
completion claim includes evidence that it detects known false success.

**Package:** `conformance/`
**File:** `conformance/coverage.go`
**Test file:** `conformance/adversarial_test.go`

Maintain a reviewed mutation manifest with mutation ID, starting fixture
hash, one change, invoked command, expected exit and diagnostic, and the
acceptance ID it protects. Run each from an independent temporary copy of a
known-valid fixture. First verify the unmodified fixture passes. A crash,
unrelated schema failure, or any nonzero exit without the intended code
does not kill the mutation. Store mutation results as foundation evidence.

Use these required controls. Exit codes follow the cited acceptance test;
all mutations must fail with the named diagnostic. Change the intended
accounting or observation layer, rather than merely breaking JSON syntax.

```tsv
Mutation	Change	Expected diagnostic	Protected acceptance
M_SOURCE_SECTION	Delete extracted warning	E_EXTRACTION_MISMATCH	A2_03
M_OVERLOAD	Collapse requirement overloads	E_FACET_UNCOVERED	B1_02
M_PLACEHOLDER	Replace descriptions with old template	E_PLACEHOLDER	B1_03
M_EMPTY	Empty required corpus	E_EMPTY_CORPUS	B1_03
M_DUPLICATE_ID	Repeat a record ID	E_DUPLICATE_ID	B1_04
M_UAT	Remove required UAT	E_UAT_MISSING	C1_02
M_TEST	Bind nonexistent test	E_TEST_MISSING	C2_02
M_SKIP	Selected test skips	E_TEST_SKIPPED	D1_02
M_FAIL	Selected test fails	E_TEST_FAILED	D1_02
M_TIMEOUT	Selected test exceeds deadline	E_TEST_TIMEOUT	D1_02
M_ZERO	No matching test executes	E_TEST_NOT_RUN	D1_02
M_EXPECTED	Alter reviewed expectation	E_REVIEW_STALE	C1_02
M_DIRTY	Change uncommitted code	E_EVIDENCE_STALE	D2_01
M_ARTIFACT	Delete raw evidence	E_EVIDENCE_MISSING	D2_02
M_FETCH	All source fetches fail	E_FETCH	A1_02
M_MULTISET	Drop one repeated value	E_EXPECTATION	C1_03
M_FAIR	Sort a supplied fair summary over raw B,A	E_EXPECTATION	E2_03
M_DISAGREEMENT	Contradict actual oracle output	E_EXPECTATION	E1_04
```

Also exercise selected semantic mutations through the observation checker:
a mapper retaining nulls with static typing disabled, a mix observer
deduplicating values, and an observer sorting fair output. These
deliberately faulty fixture subjects test the checker. The report
explicitly says they are not mutated wr implementations. Once a wr adapter
exists, its runtime milestone must add real implementation mutations for
those behaviours.

**Acceptance tests:**

1. `E2_01`: Run all 18 required accounting controls. The clean baseline
   passes, and every mutation produces its declared code and nonzero exit.
   Report `mutations:18`, `killed:18`, `survived:0`, `invalid:0`.
2. `E2_02`: Change one mutation to return an unrelated error or remove the
   mutation's input change. Its result becomes invalid or survived;
   foundation verification returns 1 with `E_MUTATION_NOT_KILLED`.
3. `E2_03`: Run all three semantic observer mutations. The null case,
   duplicate-preserving multiset case, and fair sequence case each fail
   `E_EXPECTATION`. Dropping duplicates or sorting sequence observations
   cannot be added to normalization to make them pass.

## Section F: Preserve the route to inventory and durable runtime work

### F1: Seed later milestones without awarding them completion

As a maintainer, I want later work retained as requirements with explicit
dependencies, so that foundation success does not erase the product goal.

**Package:** `conformance/`
**File:** `conformance/coverage.go`
**Test file:** `conformance/milestones_test.go`

Create these machine-readable seed requirements with origin `wr`, source
provenance to the accepted prompt, scope `required`, and runtime bindings
null. They remain outside the foundation's execution denominator but inside
the target ledger and runtime profile. Their draft UAT contracts describe these
observable outcomes; fixture detail must be independently reviewed before
those later milestones can execute or complete.

- `WR_IMMUTABLE_RUN`: submission captures workflow, includes, parameters,
  config, input identities, and runtime-relevant options. Change/delete
  the caller's files after accepted submission and stop the submitting
  CLI. The accepted run produces the original definition's results.
- `WR_DURABLE_EXPANSION`: file-producing parent discovers three children;
  one child branch is empty, remaining tasks finish out of input order,
  and a downstream task consumes actual files. Expect the declared output
  multiset and exact logical task set after recovery.
- `WR_CRASH_BOUNDARIES`: kill CLI and manager separately before and after
  durable submission acknowledgment, child-plan persistence, task enqueue,
  and expansion acknowledgment. Retry the same submission identity after
  restart. Each accepted logical task exists exactly once, every durable
  continuation reaches a terminal state, and no output disappears. Store
  ledger/task IDs and crash receipts; external side effects are not
  claimed exactly once merely because logical tasks are deduplicated.
- `WR_CONTAINERS`: execute a task using a pinned container image and prove
  its file, environment, and exit observations came from that container.
- `WR_RESOURCES`: record evaluated per-task resources and prove the
  scheduler receives the declared requests, including invalid-value errors.
- `WR_GROUPING`: preserve workflow/run/task grouping through retries and
  restarts and expose those IDs through the supported manager/CLI boundary.
- `WR_OUTPUT_ACCESS`: locate and read declared outputs after submitting CLI
  exit and manager restart, with missing-output failures distinguished.
- `WR_INTERMEDIATES`: scalar-only dataflow creates no serialized per-item
  intermediate files; file-producing tasks retain required task files and
  durable state. Specify and inspect permitted file categories in that
  later UAT rather than asserting that all disk use is absent.
- `WR_UNSUPPORTED`: unsupported semantics fail with feature and source
  location, nonzero exit, and no success receipt or changed-meaning output.

Dependency order is foundation, complete target accounting and policy
resolution, durable dynamic runtime slice, then broad operator batches.
Inventory expansion can overlap runtime design; broad operator
implementation cannot bypass the durable-slice gate. The runtime slice must
cover every supported invocation mode determined by that later design.
No current queue test satisfies these Nextflow run-level requirements.

**Acceptance tests:**

1. `F1_01`: The initial ledger contains all nine named wr seed requirements,
   both policy decisions, and all bootstrap semantics. Every seed has a
   linked draft UAT contract and null runtime binding. Runtime verification is
   incomplete and lists all nine seed IDs; no seed is excluded.
2. `F1_02`: Assign a broad operator batch a completed state without current
   durable-slice evidence. Return 1 with `E_MILESTONE_DEPENDENCY`. A queue
   test pass or oracle-only pass does not satisfy that dependency.
3. `F1_03`: Mark `WR_CRASH_BOUNDARIES` complete using parser diagnostics or
   submission-priority tests. Return 2 with `E_EVIDENCE_KIND`; its checklist
   remains unchecked and missing crash artifacts are named.

### F2: Generate bounded handoffs and a durable evidence ledger

As an agent orchestrator, I want resumable batches with exact input and
completion references, so that fresh context does not lose unresolved work.

**Package:** `conformance/`
**File:** `conformance/render.go`
**Test file:** `conformance/render_test.go`

Each batch defines assigned IDs, dependencies, exact source excerpts and
hashes, relevant files, commands with deadlines, UAT IDs, unresolved
questions, and a required completion profile. Generate a briefing and
checklist from those records. State approximately 100k tokens as a ceiling
including tool output and reasoning, not a target size or a guarantee from
character counts. Split at coherent semantic/dependency boundaries; a
reviewer must approve the proposed input bundle as small enough to leave
reasoning space. No automated token guess can waive that review.

The durable ledger preserves attempts, decisions, reviews, pending
reconciliations, and artifact links. Generate each checked box by re-running
verification of the referenced evidence. Hand editing a checkbox or setting
an arbitrary ledger `complete` field cannot award completion. A broken
artifact link keeps its item incomplete. Historical entries remain visible
with stale/failed status after subsequent edits.

**Acceptance tests:**

1. `F2_01`: Generate a batch with two assigned IDs, one dependency, and one
   unresolved question. The briefing contains those exact IDs, source
   excerpts with hashes, executable commands, deadlines, and its completion
   claim. Regeneration is byte-identical.
2. `F2_02`: Delete one linked artifact or change an assigned input hash.
   Its generated checkbox becomes unchecked and states missing or stale
   evidence. A hand-edited checked box makes `render --check` fail.
3. `F2_03`: A batch missing its bounded-input review or containing a
   dependency cycle fails with `E_BATCH_REVIEW` or `E_DEPENDENCY_CYCLE`.
   It cannot generate an approved implementation handoff.

## Implementation Order

1. Implement A1 and closed schema validation. Acquire pinned source and
   runtime artifacts into a candidate cache. Review actual hashes and the
   bootstrap selectors before accepting the lock. Write failing tests for
   fetch-all-failed, empty corpus, and malformed records first.
2. Implement A2, B1, and B2 sequentially. Use independent extraction fixtures
   before interpreting the real bootstrap regions. Author and independently
   review obligations from original sources; seed unresolved decisions.
3. Implement C1 and C2. Create executable expectation records and exact Go
   test bindings. Import every numbered acceptance test in this spec into
   foundation requirement/UAT records with its unchanged ID and source
   provenance. Compare that import against this spec in independent review.
   Subsequent changes update records first and regenerate the views.
4. Implement D1 and D2. Prove execution-event and freshness failures before
   relying on any generated completion report. Preserve raw evidence.
5. Implement E1 with the actual pinned oracle, then E2's full mutation suite.
   Resolve harness defects and review exact diagnostic literals. An external
   prerequisite failure blocks this gate; do not replace it with fixtures.
6. Implement F1 and F2, regenerate all views, and execute the final gate.
   Reviewers can review independent semantic batches concurrently after
   source locking; dependent implementation steps remain sequential.

The final `foundation-bootstrap` gate requires all numbered acceptance tests
above, all seven real oracle cases, all 18 accounting mutations, all three
semantic observer mutations, current independent reviews of every bootstrap
obligation, byte-complete extraction, matching generated views, and current
artifact hashes. It must report zero missing/skipped/failed/timed-out/stale
foundation UATs. It must also report both unresolved product decisions,
nonzero pending target inventory, no wr adapter, and zero wr runtime passes.
Those outstanding facts are part of the expected honest foundation result.

Run relevant GoConvey tests with `CGO_ENABLED=1`, `-tags netgo`, and
`-count=1`, followed by repository lint and required repository tests. Bound
individual commands with `timeout`; allow longer repository-wide checks
only with a recorded deadline and continuing progress updates. Suggested
foundation commands after offline acquisition are:

```bash
timeout 20m go run ./cmd/wr-conformance run --suite foundation-bootstrap
timeout 2m go run ./cmd/wr-conformance verify --suite foundation-bootstrap
timeout 2m go run ./cmd/wr-conformance render --check
timeout 20m env CGO_ENABLED=1 go test -tags netgo -count=1 ./conformance/...
timeout 10m golangci-lint run
```

The foundation runner must not recursively invoke its own outer integration
command. UAT tests call the specific CLI operation or oracle helper under
test; runner tests execute only temporary fixture subjects.

## Appendix: Key Decisions

### Completion and evidence limits

This specification defines a bounded foundation milestone, not a substitute
for whole-language inventory. Coverage has separate denominators for source
bytes, reviewed semantic facets, concrete UAT cases, discovered tests, and
current passing executions. No single percentage merges them.

Review records and Git review are the trust boundary for intentional scope
and expectation changes. Hashes detect accidental drift and stale evidence;
they do not defend against a malicious author rewriting every record and
its trusted history. Independent review and behavioural mutations address
the self-consistent-but-wrong failure mode without promising semantic proof.

The tool fails closed on missing data. It can show useful diagnostics after
partial failure, but never award the affected completion claim. Reports
separate scope, discovery, execution, and freshness. The deferred product
decisions remain questions, not silent exclusions.

### Source research and reuse

Research used the [release metadata][release] and [pinned source tree][tree].
The full immutable commit is recorded in the target table. The file list in
A2 identifies the documentation, grammar, dependency declarations, and tests
read during research.

The exact tag/commit and artifact checks supersede URL availability. The
release contains explicit typed-process/workflow material, and strict syntax
still permits some JVM class access. Those findings require inventory and
policy decisions rather than inheritance of old exclusions.

Current repository integration points are `cmd/wr-testsuite`,
`jobqueue/job.go`, `queue/dependency_queue.go`, `go.mod`, and `.golangci.yml`.
The archive is `codex/archive-nextflowdsl-2026-09-28`; read individual files
with `git show` when validating historical regressions. Do not copy its
manifest or expected output as the new authority.

Implementors and reviewers must apply the installed `go-implementor`,
`go-reviewer`, `go-conventions`, `implementation-principles`, and
`testing-principles` skills. Every acceptance test has a meaningful failing
command before implementation and a corresponding GoConvey test afterwards.
The spec-writer workflow owns review and later phase plans.

[release]: https://github.com/nextflow-io/nextflow/releases/tag/v26.04.6
[tree]: https://github.com/nextflow-io/nextflow/tree/232b605
