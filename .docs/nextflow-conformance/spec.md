# Nextflow Conformance Foundation Specification

## Overview

Build a Go development tool and independent behaviour suite for pinned
Nextflow and eventual wr execution. Preserve upstream assertion purposes,
strengthen weak checks, and add independently identified document gaps.
Account separately for original assertions, helpers, state, completion,
documented facets, reviewed mappings and actual results. Its first completion
claim is `foundation-bootstrap`, over the explicit corpus and proven families
below. It exposes missing work and rejects corrupted accounting and evidence.

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

The accepted [pilot report][pilot] and [independent review][pilot-review]
support finite scalar, typed-value and file observations. All five research
UATs passed at that scope, ending with `cad2b64d`. Their frozen contracts and
observations are inputs, not universal translation or fresh-checkout proof.
This revision retains every one of the original 49 foundation acceptance
obligations and adds 20, for 69 tool acceptance tests. Language coverage has
its own denominators. The seven original oracle examples remain required;
they do not prove the independent suite's upstream coverage.

## Architecture

### Repository fit and boundaries

Add public package `nextflowconformance/` and developer entry point
`cmd/wr-nextflow-conformance/main.go`. Follow the existing standalone
`cmd/wr-testsuite` pattern. Do not add a command to the production `wr` CLI or
a dependency from `jobqueue`, `queue`, or the production executable. Use the
standard library and existing GoConvey dependency. This work needs no Markdown
renderer, plugin framework or database. Use two private, fixed engine routes
that share reviewed contracts; no user-supplied adapter executable is allowed.

The only new public Go API is:

```go
// Run executes the developer CLI and returns its process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int
```

The CLI owns argument parsing and delegates to private domain functions in
`nextflowconformance/`. `cmd/wr-nextflow-conformance/main.go` supplies
cancellation and exits with the returned code. Context cancellation reaches
network requests, subprocess groups, pipe readers, and temporary-file cleanup.

Use these files; tests live beside their corresponding source files:

```tsv
Path	Role	Authority	Owner
nextflowconformance/model.go	Decode and validate records	Schema rules	Foundation
nextflowconformance/source.go	Acquire and verify pinned bytes	Source lock	Foundation
nextflowconformance/extract.go	Partition bytes and enumerate units	Pinned bytes	Foundation
nextflowconformance/coverage.go	Check links and scope	Reviewed records	Foundation
nextflowconformance/runner.go	Discover and run Go tests	Go JSON events	Foundation
nextflowconformance/evidence.go	Check freshness and artifacts	Recorded inputs	Foundation
nextflowconformance/oracle.go	Run pinned Nextflow cases	Actual subprocess	Foundation
nextflowconformance/upstream.go	Account original obligations and mappings	Reviewed source inventory	Foundation
nextflowconformance/observe.go	Decode and compare typed observations	Reviewed contracts and raw bytes	Foundation
nextflowconformance/reference.go	Launch unchanged native originals	Reviewed source and JVM closure	Foundation
nextflowconformance/package.go	Export, restore and replay portable bundles	Reviewed bundle and dependency lock	Foundation
nextflowconformance/render.go	Generate reports and handoffs	Validated records	Foundation
nextflowconformance/testdata/	Independent fixtures and mutations	Reviewed fixtures	Foundation
nextflowconformance/data/	Versioned target and ledger	Reviewed JSON	Foundation
nextflowconformance/data/cases/	Workflow inputs and expectations	Reviewed files	Foundation
.tmp/nextflow-conformance/	Acquired blobs and run artifacts	Disposable cache	Developer
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
that evidence. Later accepted pilot execution establishes only its recorded
finite cases; the E1 runs and new foundation-family runs still need evidence.

Acquire the commit's complete source tree and immutable Git tree listing.
Check Git blob IDs as well as SHA-256 file hashes; store the tree object's
identity and verify its entries against the pinned commit. Preserve file
modes, original line endings, licenses, and every file's repository path.
Reject a truncated tree response. Archive layout is not source identity.
Never substitute a mutable documentation website or the latest release.

The pinned `modules/nf-lang/build.gradle` declares
`me.sunlan:antlr4:4.13.2.6`, `org.apache.groovy:groovy:4.0.31`, and
`org.pf4j:pf4j:3.14.1`. Capture these coordinates and hashed POMs as dependency
metadata, plus source artifacts used for semantic review. Grammar source
lives in this release's `modules/nf-lang`.

The official distribution is a shell launcher followed by a shaded JAR,
42,355,106 bytes in total. It has no nested JARs and contains repeated ZIP
names, including class files. `packing.gradle` constructs it from the
launcher and `modules/nextflow/build.gradle`'s `shadowJar`, which merges
`runtimeClasspath` and `lineageImplementation`. Retain these pinned build
files as provenance. Keep the distribution byte-identical as one opaque
runtime artifact; never extract, deduplicate, rebuild, or repackage it.
Its full-file hash covers both the launcher and every bundled dependency.

Record the execution dependency closure as actual files: this distribution,
the Java tree, and required environment tools. Bundled parser and transitive
classes are covered by the distribution hash; do not invent separate or
nested JAR artifacts. Any external JAR actually required for execution must
be acquired, hashed, and included in the closure. POMs and declarations are
provenance, not proof of a resolved build dependency graph or additional
runtime files. Do not claim a complete per-component Maven inventory from
partial shaded metadata. Offline execution in E1 proves the acquired
closure suffices for the seven bootstrap cases only.

Native Spock and parser observers need additional development dependencies.
The pilot measured 578 acquired resource paths, including 575 Gradle resource
or metadata files, Gradle 9.3.1 ZIP/checksum and a Spock source JAR. Resolved
Spock is 2.4-groovy-4.0 and JUnit Platform is 1.14.1, overriding a requested
launcher 1.10.5; compiler Groovy 4.0.31 differs from Gradle's 4.0.29. Source
target 17 differs from execution JDK 21. Record actual URLs, hashes,
resolution and tool recipes in the reviewed dependency extension below.
These are development inputs, not a production JVM policy decision.
Keep the accepted source lock, runtime bytes, 160 selections and eighteen
bootstrap batches unchanged. New suite sources come from verified entries
of that same 2,856-file tree without changing their historical selection.
New dependencies have their own lock and review; original source edges are
not a resolved execution graph. Existing local caches prove no portability.

`acquire` receives an existing Java 21 home. Hash every regular file and
symlink target in that tree, record `java -version`, OS and architecture,
and verify the executable comes from that tree. Never install system
packages. Execute the pinned distribution by its absolute path; its
`NXF_PACK=dist` launcher passes that same file to Java. Acquire the separately
published `nextflow` launcher for provenance, but do not invoke its default
`one` package download path. Prepare a private Nextflow home and disable
automatic updates and plugins for these local bootstrap cases. The acquired
environment must run offline with an empty user home. Missing Java or runtime
dependencies are explicit failures.

Only `acquire` may access the network. It writes a candidate lock and cache;
`validate`, `extract`, `render`, `discover`, `run`, `verify`, `reference`,
`observe`, `package`, `restore`, and `replay` never fetch.
A reviewed lock is a checked-in input, not something verification repairs.
Acquisition cannot replace a reviewed lock or existing evidence on failure.
Use HTTPS, ten-second connection and sixty-second request deadlines, at
most two attempts per object, a fifteen-minute total deadline, and at most
four concurrent requests. Limit an object to 256 MiB, unpacked sources to
512 MiB and 50,000 entries, and a selected text file to 8 MiB. Fail explicitly
on a limit; never truncate data and call it complete.

For every archive extracted into the filesystem, reject traversal, absolute
member paths, duplicate destination paths, and unsafe symlinks before
publishing any extracted files. Never resolve collisions by keeping one
member. The opaque pinned distribution has no member extraction or member
path uniqueness requirement. Verify its exact target hash and byte count
before execution; a packaging label cannot authorize different bytes.
Reject non-regular executable inputs and local paths escaping the corpus or
run root in both cases. Resolve relative include references against their
source file. Network acquisition can use reviewed immutable URLs from the
lock only.

### Records and schema

All authoritative files are UTF-8 JSON, schema version `1`, with final
newline. Use sorted record arrays by ID and deterministic field order when
writing. Reject unknown fields, duplicate JSON object keys, invalid UTF-8,
trailing JSON values, duplicate IDs, empty required text, and absent required
fields. Decode numbers without conversion through floating point. IDs use
`[A-Z][A-Z0-9_]*`; file IDs use repository-relative POSIX paths. Hashes are
64 lowercase hex digits. Timestamps are RFC3339 UTC, never freshness proof.

Implement these closed schemas in `model.go`; emit matching schema documents
in `nextflowconformance/data/schema/` from that same definition. Keep schema
emission limited to these records; it is not a reusable schema framework. The
fields below are required unless explicitly optional. A `file_ref` is `{path,
sha256, bytes}`. A `span` is `{file, start, end, sha256}`, using zero-based
half-open byte offsets. IDs referring to other records must resolve in the
same target revision, except retained extraction history as defined below.

```tsv
Record file	Record key	Fields beyond key	Constraints
target.json	id	version, parser, tag, commit, source_tree, profiles, decisions	One nonempty target
sources.lock.json	id	origin, revision, tree, files, artifacts, environment	One reviewed lock
blocks.json (generation blob)	id	span, kind, parent, children, include_refs, cross_refs	Extracted source units
extraction.json	id	lock, batches, blocks, reviews, inputs, previous, removed_ids, added_ids, stale_review_ids	Atomic generation and reconciliation
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
- `artifacts` contains role, immutable origin, `file_ref`, `packaging`,
  `coordinate`, and dependencies by artifact ID. Roles include source,
  launcher, runtime, JAR, Java, environment-tool, and dependency-metadata.
  `packaging` is `file`, `extracted-archive`, or `opaque-dist`; only the
  exact pinned runtime uses `opaque-dist`. `coordinate` is a Maven
  `group:artifact:version` for Maven POMs and Maven source artifacts,
  otherwise null. Each artifact references actual acquired bytes. Runtime
  dependencies enumerate external execution inputs; embedded classes create
  no separate artifact IDs. POMs use role `dependency-metadata` and packaging
  `file`; they are not execution dependencies. Dependency cycles and missing
  entries fail validation. The runtime closure contains exactly one opaque
  distribution and its external execution dependencies.
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
- UAT kind is `foundation`, `oracle`, `differential`, `wr-runtime` or
  `native-original`. Native-original expected checks bind original predicates
  and required completion IDs rather than inventing neutral values. Its
  closed expected shape is `{upstream_ids, completion_ids}`, with nonempty
  arrays resolving to this family's original obligations.
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

### Extraction references and retained generations

Phase 2 adds `cross_refs` and the extraction schema, then six suite schemas
specified below: `upstream`, `mappings`, `contracts`, `observations`,
`dependencies`, and `suite`. Emit eighteen active schemas in total. Stock
JSON Schema enforces closed local shapes; decoder/corpus checks enforce
cross-record relations. Phase 1's eleven-schema acceptance at `ec487ed2`
remains historical evidence. The unfinished twelve-schema amendment and
these six additions require new tests, review and parent-input measurements.
Regenerate active fixtures explicitly; no implicit legacy conversion exists.
Before further Phase 2 implementation, reconcile retained model/schema/CLI
contracts with this revision and correct F11's malformed-UTF-8 test gap.
F11 is "Assert malformed UTF-8 inside locally valid strings" in
[schema review 06][schema-f11], not a story in this spec.
Require its eleven invalid/valid free-string pairs,
malformed bytes inside otherwise valid JSON strings, and rejection that
fails under removal of the UTF-8 guard. Production decoding currently
rejects those bytes; an unrelated JSON syntax/ID failure proves no guard.

Keep `include_refs` and its existing `{path, block_id, external_id}` shape:
exactly one destination is nonnull. Includes require verified local bytes;
a block destination names the resolved locked file, and an external ID names
an acquired source artifact at that path. Cross-references never use these
include destinations or trigger include traversal.

`cross_refs` is an array, empty when absent from the source. Each closed edge
has exactly `{span, syntax, target, destination}`. Its nonempty `span` hashes
the complete reference occurrence in original bytes, lies within its owning
block, and belongs to the deepest block containing the whole occurrence.
Store each occurrence once, ordered by `(span.start, span.end, syntax,
target)`. `syntax` is `markdown`, `myst-ref`, `myst-doc`, or `html`; `target`
is the literal destination token, without display text or enclosing syntax.
Do not normalize this stored token. A destination is one of these closed
objects; all listed fields are required and other fields are forbidden:

- `tree`: `{kind:"tree", path, fragment}`. `path` is a safe locked
  repository-relative POSIX file path, regardless of selection state.
  `fragment` is null for the whole file, otherwise a nonempty resolved
  anchor string. This asserts location only, not extraction or acceptance.
- `external`: `{kind:"external", uri, artifact_id}`. `uri` is the absolute
  URI resolved from the token, retaining query and fragment. `artifact_id`
  is null for an outstanding external source record. Only an exact match
  of the URI without its fragment to an acquired source artifact's locked
  origin permits its ID. Verify that artifact's actual bytes. Never use
  the source archive as provenance for a web page. Pending edges contain
  no hash, byte count, file path, or invented acquisition identity.
- `unresolved-local`: `{kind:"unresolved-local", reason, candidates}`.
  `reason` is `missing-path`, `missing-label`, `missing-fragment`,
  `ambiguous`, or `unsupported-target`. `candidates` is empty except for
  `ambiguous`, which requires at least two distinct candidates of one kind:

  - `path`: `{kind:"path", path, fragment}` identifies an extensionless
    path match. `path` is a safe locked file path; `fragment` is the decoded
    requested fragment or null, not a claim that the fragment resolved.
    Order and deduplicate by `(path, fragment)`, null before strings.
  - `definition`: `{kind:"definition", span, target}` identifies one
    definition occurrence. `span` hashes the complete defining construct
    in verified original bytes, excluding trailing line endings: a label,
    HTML id/name attribute, complete heading, or reference-style definition.
    `target` is its literal explicit label or HTML anchor value, computed
    heading anchor, or literal reference-style
    destination token, respectively. Keep markup escapes in literal tokens.
    Order and deduplicate by `(span.file, span.start, span.end, target)`;
    validate the span hash separately. Two definitions in one file remain
    distinct even when their tokens match. For competing reference-style
    definitions, retain each destination token, including external URIs;
    resolve it only if the definition is unique. No definition candidate
    asserts acquired bytes, extraction, or semantic acceptance.

Compare strings by UTF-8 byte order. Definition candidates need no extracted
block and may refer to unselected locked files. Ambiguity never picks one
candidate or follows competing targets. External destinations in ambiguous
reference-style definitions therefore remain pending inventory.

These external edges are the outstanding external source records required
by A2; they need no separate source-record file. Dangling artifact IDs,
illegal destination shapes, unsafe paths and mismatched spans fail with
exit 2 and `E_REFERENCE` (`E_SOURCE_PATH` for path escape). An unresolved
local edge is valid pending inventory, not a dangling record ID.

Resolve against the locked original bytes only. Index explicit MyST labels
`(name)=`, HTML `id`/anchor `name` values, and Markdown/HTML heading anchors
in locked documentation files. Verify each inspected file against the lock;
indexing never changes its selection or creates extracted blocks. Here
locked documentation files are regular files under `docs/` ending in `.md`.
Ignore anchor-like text in code examples and comments. Explicit labels are
exact, case-sensitive strings; duplicate definitions remain ambiguous.
For heading anchors, strip markup delimiters and HTML tags, retain displayed
text, ASCII-lowercase it, remove ASCII punctuation except `-`/`_`, replace each
whitespace run with `-`, and trim `-`. Retain other Unicode characters.
Assign colliding heading bases suffixes `-1`, `-2`, ... in byte order,
skipping already assigned heading anchors. Explicit/heading collisions
remain ambiguous rather than taking precedence.

MyST `ref` resolves its label globally; `doc` resolves a document path.
For `caption <target>`, only `target` is the destination token. Markdown
inline/reference-style links, autolinks, images, and HTML `href`/`src`
attributes use the same path/URI rules; resolve reference-style definitions
within their document, retaining an unresolved edge for a missing or
ambiguous definition. In reference-style links, `target` retains the use's
literal label and resolution follows its definition; the occurrence span
covers the use. Ignore references inside code examples and comments.
Decode markup escapes/entities for resolution only. A URI with a scheme
is external; a network-path URI starting `//` uses `https:`. Preserve
external URI spelling otherwise.
For local paths, split the fragment, percent-decode path/fragment once,
resolve relative to the referring file, and treat `/` as source-tree root.
Reject NUL, backslashes and escape above that root. Empty paths name the
current file. For an extensionless documentation path, try the exact file
and then the path with `.md`; zero matches are `missing-path`, multiple
matches are `ambiguous`. Resolve a fragment only within that destination
file; zero matches are `missing-fragment`. Bare `ref` misses are
`missing-label`. Malformed escapes, local queries and unhandled local
target syntax are `unsupported-target`. Never guess a similar label,
consult rendered websites, or fetch a target.

`extraction.json` is the sole publication point for extracted records.
It is a closed object with `schema:1`, an ID and the fields in the records
table. `lock`, `batches`, `blocks`, `reviews`, and `inputs` are `file_ref`
values for immutable snapshots under `<root>/extraction/`; `previous` is
null on first extraction or a `file_ref` to the preceding manifest. JSON
snapshot paths are `extraction/<sha256>.json`, relative to the corpus root;
hashes and byte counts cover exact saved bytes. `blocks` contains the sorted
block-record array. `reviews` snapshots `reviews.json`, or `[]\n` if absent;
it cannot create a review. `lock` and `batches` snapshot the exact inputs.
Save the old manifest as an immutable blob before replacing it.

`inputs` snapshots the closed object `{schema:1, current, reviewed}`. Its
shape is part of the extraction schema's definitions, not a thirteenth
schema. It binds every file used in review reconciliation, including its
original bytes:

- `current` is a sorted unique array of `{kind, path, snapshot}` entries,
  one per `(kind, path)` in the union of current and retained reviews'
  `input_hashes`. `kind` is the exact input category: `source`, `obligations`,
  `requirement`, `uat`, `normalization`, `binding`, `upstream`, `mapping`,
  `contract`, `dependency`, or `suite`. `path` is the original
  safe relative input path. For `source`, read the candidate lock's source
  tree or acquired source artifact at that path. For other kinds, read
  relative to the corpus root. `snapshot` is a `file_ref` for the current
  bytes, or null if that input is absent. Source/cache failures for a path
  still declared by the candidate lock retain A1's errors; absence from the
  new lock instead gives null. Permission and other I/O errors fail, rather
  than masquerading as absence.
- `reviewed` is a sorted unique array of `{review_id, kind, path, snapshot}`,
  one entry for every bound file of each current or retained review.
  `snapshot` is a nonnull `file_ref` whose hash and byte count match that
  review's original binding. Retain exact bytes from the matching current
  input or a reachable predecessor snapshot. If neither supplies them,
  return 2 with `E_EVIDENCE_MISSING`; a hash is not replacement evidence.
  Within a review, duplicate `(kind, path)` bindings are invalid. Reject
  extra or missing catalog entries; they cannot enlarge or shrink the
  review-derived closure.
- Sort these arrays by their listed identity fields, excluding `snapshot`.
  Payload paths are `extraction/<sha256>.blob`; store arbitrary original
  bytes unchanged, without JSON decoding/re-encoding. Equal payloads may
  share a blob. Catalog entries preserve their original logical paths.
  Every source span must name a bound source input; verify it against that
  review's retained bytes and the first generation's lock that contains
  that review. At introduction, each reviewed source must match that lock.
  Obligation input payloads decode as obligation records for block-reference
  inspection, resolving historical links through retained generations.

Bind each separately stored reviewed expectation, fixture, normalization,
or binding file explicitly in its appropriate review input category. A hash
inside another JSON file does not retain those reviewed bytes. The closure
is exactly these explicit bindings plus the manifest's lock, batches,
blocks, reviews and predecessor; there is no recursive walk of arbitrary
JSON references. Corpus review inputs cannot name `extraction.json`,
`extraction/`, root `blocks.json`, `reviews.json`, `attempts/` or generated
views, including through symlinks. Blocks are bound through source spans and
obligation IDs. These restrictions keep the hash graph acyclic; a semantic
record may refer to its review by stable ID. Invalid bindings return 2 with
`E_REFERENCE`; unsafe paths retain `E_SOURCE_PATH`.

A manifest ID is `EXTRACT_` plus uppercase SHA-256 of the six snapshot hashes
in order `lock`, `batches`, `blocks`, `reviews`, `inputs`, `previous`, joined
with LF and no trailing LF; use `-` for null `previous`. Reconciliation arrays
are derived from those bound inputs, never an additional input to the ID.

`removed_ids`, `added_ids`, and `stale_review_ids` are sorted unique ID
arrays. Compare the previous and new block arrays by ID: removed and added
are exact set differences, or empty/all new IDs on first extraction.
Evaluate the union of retained review snapshots and current reviews; a
review ID must never be reused with different record bytes. A review is
stale when any of these conditions holds:

- Any original bound hash/byte count differs from its `current` catalog
  entry, including null; compare candidate inputs, not previous inputs.
- Any source span no longer matches the candidate source bytes.
- Any block ID in its findings or originally bound obligation records is
  absent from candidate blocks but exists in a retained generation.

Resolve finding IDs by record type; only block IDs enter the block-presence
comparison. An unknown ID with neither current nor retained provenance is
`E_REFERENCE`. Inspect the originally reviewed obligation bytes, even after
their active file has been replaced. Include previously stale IDs still
stale; a new review cannot rename or erase an old entry. Recompute this list
from the catalog and chain; stored lists cannot declare their own correctness.
Never transfer review approval to a replacement block. A semantic-only
change therefore publishes a successor with empty removed/added arrays and
the exact newly computed stale-review list.

Retain every preceding manifest and every reachable snapshot and payload.
The chain must be acyclic, hash-valid and end in a null predecessor.
Reproduce each generation's reconciliation using its own catalog, reviews,
blocks and predecessor, never today's mutable semantic files. Historical
removed IDs resolve only through retained generations. Historical reviews
must match retained review bytes and their original lock/spans; their stale
references are evidence, not invalid current links or current acceptance.
A review can satisfy a current gate only when present in current reviews,
accepted, independent and not stale. Appearance in a retained snapshot alone
neither grants nor removes current acceptance. Current obligations and other
active links still require current IDs.

Prepare immutable blobs and the complete manifest before replacing
`extraction.json` by one atomic rename on the same filesystem. Flush writes;
recheck lock, batches, reviews, every `current` catalog input (including
recorded absence), all inspected source inputs, and the previous publication
before replacement. A changed input or concurrent publication returns 2 with
`E_INPUT_CHANGED`. Readers capture the manifest once and follow only its
hash-bound snapshots; they never mix generations. Failure before publication
preserves the previous manifest and every reachable blob. Unreferenced
staged blobs award nothing. The reviewed lock, batches, semantic records and
reviews remain unchanged.

The active lock/batches/reviews snapshots and `current` catalog must match
current input bytes or recorded absence; only historical manifests bind
older states. Reconciliation reads those captured bytes. Full validation
also performs the current semantic-link and required-file checks; a missing
semantic input can mark a historical review stale without satisfying B1.
With no reviews, both catalog arrays are empty; extraction does not require
an initialized semantic ledger.

Before assigning a new predecessor, compare the candidate lock, batches,
blocks, reviews and inputs snapshots to the active generation. If all five
match and its recomputed reconciliation is valid, extraction is a
byte-identical no-op with no new predecessor or reconciliation entry.
The predecessor is deliberately excluded from this comparison. Never repair
an invalid active reconciliation by appending a new generation.

Full corpus loading reads blocks through this manifest; an active root
`blocks.json` alongside it is an error, not an alternative authority.
`extract --check` derives blocks, edges and the input catalog from verified
inputs, compares the active snapshots, and recomputes every generation's
reconciliation using its retained predecessor. It never creates a successor
or writes files. Current-input drift, missing/changed generated blocks,
edges, lists or catalog entries return 1 with `E_EXTRACTION_MISMATCH` and
identify the affected file/span or ID; malformed record shapes return 2.
A missing manifest is a mismatch. In check mode, an active generated-block
or reconciliation mismatch takes the exit 1 path even if its saved hash no
longer matches. Full validation of damaged generation snapshots returns 2
with `E_ARTIFACT_HASH` or `E_EVIDENCE_MISSING`. Missing/damaged historical
snapshots or input payloads also return 2 with those codes in check mode;
these cannot be regenerated from current inputs. Source/cache integrity
errors retain A1's exit 2 diagnostics. A malformed/cyclic predecessor chain
returns 2 with `E_REFERENCE`. History cannot be truncated to make a check
pass.

A placeholder description is invalid: whitespace-only text, `TODO`, `TBD`,
`placeholder`, and the sentence `TODO: describe expected behaviour.` from the
archived generator are rejected case-insensitively when used as the entire
description or as a stand-in sentence. The archived form prefixes this
sentence with a backticked heading and punctuation; reject that form too.
Do not ban these strings inside quoted source, fixtures, or explicit negative
tests. Meaningful descriptions still need review;
this check is a regression guard, not a semantic classifier.

### Independent suite records and routes

Add the following closed version-1 records to `model.go` and emitted schemas.
Array files sort by ID; `suite.json` and each observation file are objects.
All keys below are required; nullable fields are identified below. `file_ref`,
`span`, ID, path and freshness rules above apply. Additional field names are
invalid. Record names identify private types, not new exported APIs.

```tsv
Record file	Key	Fields beyond schema/id	Purpose
upstream.json	id	family, origin_id, kind, parent, order, source_spans, fixtures, dependencies, literal_record, disposition, review_id	Original obligations
mappings.json	id	upstream_ids, contract_ids, boundary, strength, source_spans, observer_files, preserves, unresolved_ids, rationale, review_id	Reviewed translation claim
contracts.json	id	origin, upstream_ids, facet_ids, fixture, expected, normalization, completion_ids, routes, review_id	Shared independently authored truth
attempts/<id>/observation.json	id	attempt_id, engine, route, case_id, contract_hash, raw, exit, values, artifacts, tasks, diagnostics, completion	Neutral observed data
suite.json	id	authority_inputs, families, original_edges, pending, required_controls, review_id	Fixed bounded selection and future dependencies
dependencies.lock.json	id	base_lock, resources, recipes, environment, review_id	Development closure extension
```

Preserve literal pilot origin IDs such as `P1-COUNT` in `origin_id`. New local
IDs use `UP_` plus uppercase hex of UTF-8 `family + ":" + origin_id`, a
bijective encoding into the existing ID alphabet. Other records use existing
ID syntax. `literal_record` binds the exact original JSON entry as an
immutable UTF-8 blob; it preserves native expressions, comparator, typed
inputs, literal/raw/decoded forms and origin. The original frozen files are
also retained as authority inputs. A separate reviewed lossless projection
checks all fields, scalar types and relationships; generated IDs alone prove
no preservation. New records never rewrite pilot inventories or expectations.

- Upstream `kind` is `method`, `unit`, `predicate`, `helper`,
  `helper-obligation`, `state`, `invocation`, `completion`, `fixture`,
  `table-row` or `provider`. `parent` is null for a family's top-level
  method/helper/state/fixture records; other parents resolve to an upstream
  record in the same family. `order` is a zero-based integer preserving
  original method/subcase/invocation order. `source_spans` binds original
  spans; `fixtures` is an array of original fixture `file_ref`s.
  `dependencies` contains original obligation IDs, not inferred runtime
  JARs. `disposition` is `neutral-reviewed`, `original-only` or `unresolved`,
  with an accepted review and linked mapping or explicit retained rationale
  in `literal_record`. Every selected item is counted; internal and helper
  records are never excluded because another engine lacks their objects.
- Mapping `strength` is `equivalent-observable`, `partial`, `original-only`
  or `unresolved`. `preserves` and `unresolved_ids` partition its listed
  upstream IDs. `contract_ids` links the contracts stating each preserved
  ID's exact observed predicate; mapping `boundary` matches their selected
  `routes` boundary. Equivalent-observable requires an independent source
  argument and discriminating controls for that predicate, not just
  agreement on one run. Partial mappings keep uncovered IDs incomplete.
  Original-only/unresolved mappings may have empty `contract_ids` and
  `observer_files` arrays; their rationale is required. Source review accepts
  design strength; current actual attempts establish execution separately.
  Neither strength nor native success asserts raw original/candidate equality.
  Such equality requires genuine captures on both routes, an accepted
  instrumentation review and matching complete raw observations.
- Contract `origin` is `upstream`, `strengthened`, `document-gap` or
  `document`. Upstream contracts have nonempty upstream IDs; document and
  gap contracts have nonempty reviewed obligation facet IDs. Strengthening
  names its original purpose in `upstream_ids` and its source-derived reason
  in `facet_ids`. A document-gap is missing from the selected reviewed
  mappings, not a claim of absence from all upstream tests. `fixture` is a
  nonempty array of `file_ref`.
  `expected` and `normalization` use C3's closed neutral grammar.
  `completion_ids` names all required collection, engine and supervision
  completions. `routes` contains closed `{engine, boundary, observer_files,
  binding_id}` objects. `engine` is `nextflow` or `wr`; boundary is
  `cli-workflow` or `parser-scalars`. `binding_id` is null only for the
  unavailable wr route; its observer array is then empty. Both routes refer
  to the same contract ID/hash. There is no route-specific expected truth.
- Observations use `engine` above, with `route:"neutral"`. `case_id` and
  `contract_hash` identify the reviewed contract by ID and hash. Derive
  boundary from its unique `routes` entry matching `engine`; boundary is not
  an observation field.
  `raw` contains named `file_ref` receipts for stdout, stderr, trace,
  callback events and relevant files. Values and completion use C3's types.
  Decode and compare raw receipts independently of a supplied summary.
  A `native-original` attempt instead retains native XML, original checks,
  per-invocation exits and supervision; it emits no invented neutral values.
  Add evidence kind `native-original` to attempt/binding vocabulary. Existing
  foundation/oracle/differential/wr-runtime kinds retain their meanings.
- Suite `authority_inputs` binds frozen contracts, inventories, expectations,
  pilot reports, source snapshots and result provenance with `file_ref`s.
  For ready family execution it also includes the `dependencies.lock.json`
  `file_ref` used by D3's attempt `dependency_lock`.
  Each family is `{id, upstream_ids, contract_ids, native_selection,
  required_native, required_neutral}`. `native_selection` is an array of
  `{kind, source, names, invocations, closure}`. Kind is `spock` or `cli-pair`;
  source is a `file_ref`; names is the ordered exact feature-name array,
  empty for CLI pairs. Invocations are ordered `{id, argv, cwd}` objects
  with safe logical cwd and literal argv, empty for Spock; closure is the
  complete helper/config `file_ref` array. Selection accepts no regex.
  Required ID arrays
  are fixed before execution; missing IDs cannot shrink the denominator.
  `original_edges` preserves every source-derived edge as `{from, to, kind,
  source_spans}` with exact origin IDs and reviewed endpoint resolution.
  It is distinct from resolved resource dependencies. `pending` entries
  are `{id, kind, affected_ids, depends_on, question, source_spans}`.
  Kinds are `mapping`, `internal`, `document-execution`, `document-closure`,
  `policy` or `target-inventory`. Every question and affected set is nonempty.
  `required_controls` is an array of `{id, manifest}` binding each required
  control to its reviewed manifest `file_ref`. Selection review is the
  suite's `review_id`; dependency review is that lock's `review_id`. Both
  must be nonnull accepted, current review IDs for ready family execution.
- Dependency `base_lock` is the accepted source-lock `file_ref`; extension
  validation cannot change it. Resources are `{id, role, origins, file,
  coordinate, requested, resolved, dependencies, cache_paths, git_mode,
  historical_mode, recipe_id, base_id}`. Roles are `acquired`, `tool`, `source`,
  `metadata`,
  `generated` or `reused`. Acquired origins are actual immutable HTTPS URLs;
  reused resources have a nonnull `base_id` naming a base-lock resource;
  other resources have null base IDs. Generated resources have a nonnull
  `recipe_id`; other resources have null recipe IDs. Generated inputs do
  not acquire invented download origins. Coordinates, requested and
  resolved versions are strings or null, preserving conflict resolution.
  Cache paths are `{root, path}` objects with safe relative paths beneath
  named `gradle`, `go`, `java`, `tools` or `source` roots. Git mode is `100644`,
  `100755`, `120000` or null;
  historical mode is recorded octal text or null. Symlink bytes store target
  text; execution validates the target stays within its approved tool tree.
  Recipes are `{id, tool_id, argv, cwd, environment, inputs, outputs,
  timeout_seconds}` with fixed allowlisted tool/argv templates in package
  code. Environment is a sorted array of `{key, value}`. Source compilation
  retains output digests in generated resources' `file` references and
  command receipts in attempt `artifacts`. Recipe inputs and outputs are
  resource-ID arrays, cwd names a safe logical root/path, and tool_id
  resolves to a locked executable resource. Rebuilding cannot inherit an old
  successful build status. No recipe evaluates record text as shell.
  Recipe path/environment values may use only literal `@ROOT_ID@` tokens
  for the seven bundle roots. Expand those tokens into verified restored
  paths before argv construction; reject unknown tokens and retain both
  logical and effective argv/environment in those command receipts.
  Source/Go execution attempts also bind the fresh checked-out implementation
  tree by its D2 input digest.

Review bindings explicitly retain these new record and observer inputs using
catalog categories `upstream`, `mapping`, `contract`, `dependency` and
`suite`; observer code uses `binding`, fixtures/expectations use `uat`.
Extend local schema enums and reconciliation tests in Phase 2. Original
source spans remain bound to `source`. Observed attempts remain evidence,
not review catalog inputs that create a cycle. D2/D3 bind all raw artifacts
and accepted result-review receipts in the attempt's evidence closure.

Private Nextflow routes run the fixed pinned distribution, or the genuine
compiled upstream parser/Spock boundary when named by the family. The future
wr route must execute an independently reviewed supported wr boundary.
Until it exists, requests return `E_ADAPTER_UNAVAILABLE`, with no observation
or pass. A fixture or supplied observation can test a comparator with kind
`foundation`; it cannot acquire an engine identity. All observer and wrapper
sources ship under `nextflowconformance/data/tools/`, with reviewed build
recipes and generated output receipts in the cache. No script in ignored
research scratch is an executable foundation dependency.

### Bounded accounting and unfinished work

The initial independent-suite selection preserves the complete finite pilot
accounting: six Spock methods, eleven P/M units, 42 original predicates,
ten helpers, seven helper/internal observation obligations, fifteen original
completions, four fresh/resume invocations, 83 fixtures, 37 full source files,
123 source/span identities, 151 original/document dependency edges, 34
reviewed document facets and 28 separate expectation records. Preserve all
shared-state/selection requirements and zero table/provider counts explicitly.
Use independently reviewed origin-to-local maps for contract IDs as well;
for example `S-MIX` becomes `S_MIX`. Detect alias collisions with
`E_DUPLICATE_ID`, retaining the original identity and literal record.
These counts describe source accounting, not accepted neutral observations.
For the selected Mix methods, preserve MockSession, last-result, reset,
normalization, five-second timeout, network lifecycle and error identity.
Native helper assertions may pass while raw identities remain unobserved.

The three executable families in E3 have narrower neutral proof boundaries.
Remaining selected source obligations, including every A2 bootstrap method
and semantic region, retain reviewed accounting or pending disposition.
Suite documents from the pilot are reviewed extensions of the bounded
selection; they do not change the original A2 selectors or batches.

Keep these later completion dependencies visible with their frozen identities:

- `CLI_P8_MAPPING`: original P8 input and expected parser count zero remain
  unchanged. The CLI attempt failed for required runtime parameter greeting.
  Empty syntax rows cannot discharge `P8-COUNT`. Preserve the successful
  native/JVM count separately and require a parser-boundary mapping review.
- `STRING_MIX_EXECUTION`: `D-MIX-EXAMPLE` and `D-MIX-COMPLETION` are unexecuted.
  Their independent contract is the multiset of strings `1,2,3,a,b,z`, one
  each, with collection completion. Numeric M1 execution supplies no pass.
- `INTERNAL_EQUIVALENCE`: retain all seven helper-observation obligations:
  `H-M-LAST-SESSION`, `H-M-LAST-MAINSCRIPT`, `H-M-RUN-NETWORK`,
  `H-M-RUN-ERROR`, `H-P-SHARED-INSTANCE`, `H-P-TESTUTILS-ORDER` and
  `H-CLI-RUNNER-STATUS`. Whole JVM/AST/mock/state equivalence stays unresolved
  or original-only. Source-observable scalar preservation cannot discharge
  session identity, lifecycle order or same-error-object propagation.
- `DOCUMENT_CLOSURE`: preserve each outgoing semantic link separately:
  `process-multiple-input-files`, `syntax-workflow-typed`,
  `migrating-static-types`, `process-typed-topics`, and the unreviewed
  strict-syntax remainder. Location resolution is not semantic acceptance.
- `FULL_TARGET_INVENTORY`: later inventory must enumerate all target upstream
  tests and their assertion/helper/state/completion obligations, then compare
  them independently with the documented-language inventory. Preserve
  unreviewed/native-only/internal obligations in the denominator. It cannot
  complete with missing mappings or reviewed unresolved dispositions.

Foundation accounting can pass with these explicitly pending dependencies.
Full independent coverage requires every upstream purpose to have a reviewed
preserved equivalent, internal equivalents reviewed or still incomplete,
and every required document facet to have executable current coverage.
Successful native-only regressions do not satisfy that neutral coverage gate.
Typed milestone and JVM/plugin policy remain the two B2 product decisions;
none of this accounting converts them into exclusions.

### Commands and result contract

Commands run from the repository root. `--root` selects the corpus directory;
`--cache` selects the acquired cache. Their defaults are
`nextflowconformance/data` and `.tmp/nextflow-conformance`. Test fixtures use
`t.TempDir()`. Resolve paths once and reject output inside source inputs.

```bash
go run ./cmd/wr-nextflow-conformance acquire --java-home /opt/java21
# Review and commit the candidate lock before the remaining commands.
go run ./cmd/wr-nextflow-conformance validate
go run ./cmd/wr-nextflow-conformance extract --check
go run ./cmd/wr-nextflow-conformance render --check
go run ./cmd/wr-nextflow-conformance discover --suite foundation-bootstrap
go run ./cmd/wr-nextflow-conformance run --suite foundation-bootstrap
go run ./cmd/wr-nextflow-conformance verify --suite foundation-bootstrap
go run ./cmd/wr-nextflow-conformance verify --suite target-inventory
go run ./cmd/wr-nextflow-conformance verify --suite wr-runtime
```

`acquire` accepts optional `--lock-candidate PATH`; the default is inside the
cache. It prints acquisition results even if no object succeeds. `extract`
and `render` without `--check` write generated candidates atomically; their
check forms compare expected bytes without changing inputs. Semantic
records are authored and reviewed separately, never invented by extraction.
`extract` uses a command-specific loader for the genuine reviewed lock and
batches, without requiring `target.json` or the B1 ledger. Reuse closed
record decoding, path safety, pinned identity and offline preflight checks;
verify every batch span and the exact eighteen-member bootstrap selection.
Defer only resolution of batch `assigned_ids` into the semantic ledger.
Its claim is `source-enumerated`; complete extraction may return 0 with
pending semantics and references. Count every selected semantic block
without reviewed accounting as pending, including recognized constructs.
`validate` retains full target/profile/reference checks. B1 must create
actual obligations and linked draft cases before production target/profile
initialization can succeed. Extraction cannot supply fixture semantics.
`run` discovers first, executes once with a new attempt ID, and verifies the
result. `verify` revalidates existing evidence against current inputs.
No `--allow-missing`, imported success status, or silent partial-success flag
exists. `oracle --case ID` is an internal development command for the real
oracle UATs; it cannot itself award a wr pass.

Every command writes one JSON result to stdout and diagnostics to stderr.
The result contains `schema`, `command`, `suite` (null if inapplicable),
`claim`, `complete`, `counts`, `diagnostics`, and optional `attempt_id`.
Counts include `verified_artifacts`, `selected_files`, `selected_blocks`,
`pending_blocks`, `pending_references`, `draft_uats`,
`requirements`, `uats`, `discovered`, `executed`, `passed`, `failed`,
`skipped`, `timed_out`, `stale`, `blocked`, and `unresolved_decisions`.
`pending_references` counts occurrences of unresolved-local edges and
external edges with null artifact IDs. Report tree destinations in
unreviewed files through existing tree accounting; resolving a location
cannot discharge semantic work. Print zero counts explicitly. Diagnostic
objects contain stable `code`, `record_id`, `path`, and specific `message`;
absent location fields are null.
Sort diagnostics by code, record ID, and path.

Exit codes: `0` means this command's requested claim holds; `1` means valid
inputs with incomplete or failed obligations; `2` means malformed input,
missing prerequisite, corrupt evidence, I/O failure, or bad invocation.
`validate` returning 0 only proves record validity. Its claim is
`records-valid`, not `conformant`. A failed command never emits
`complete:true`. `verify` uses the requested profile name as its claim and
lists other profiles as incomplete without folding their counts into a
misleading combined percentage. Unknown commands or suites return 2.

The suite operations use the same public `Run` entry point and JSON result
contract. `reference --family ID` runs only that family's fixed original
selection. `observe --case ID --engine nextflow|wr` executes the contract's
reviewed route and records a new attempt. Both require the reviewed extension
lock and offline preflight; no arbitrary executable/argv flags exist.
`package --output DIR`, `restore --bundle DIR`, and `replay --bundle DIR`
use F3's bundle contract. Output/restore destinations must be new or empty;
no command overwrites published authority records or historical attempts.
Restore checks the checked-out corpus against the bundle and stages only
cache/evidence roots; corpus replacement requires a separate reviewed change.
Restored evidence remains historical replay, not a new execution attempt.

`acquire --dependency-lock PATH` may acquire a reviewed dependency extension
into a new cache generation. Verify every fixed origin/hash and actual
resource/cache recipe before publication; emit an extension acquisition
receipt without modifying the base source lock or selections. Initial
extension preparation is a reviewed engineering input using the pilot's
actual closure, not an automated conversion of requested Maven versions.
Only `acquire` fetches. Restore/replay/build never resolves missing downloads.

```bash
go run ./cmd/wr-nextflow-conformance reference --family NF_MIX
go run ./cmd/wr-nextflow-conformance observe --case S_MIX --engine nextflow
go run ./cmd/wr-nextflow-conformance observe --case S_MIX --engine wr
go run ./cmd/wr-nextflow-conformance package --output .tmp/suite-bundle
go run ./cmd/wr-nextflow-conformance restore --bundle .tmp/suite-bundle
go run ./cmd/wr-nextflow-conformance replay --bundle .tmp/suite-bundle
```

Extend result counts with separate `upstream` counts by kind,
`document_facets`, `mapped_predicates`, `pending_mappings`, `native_passed`,
`neutral_passed`, `strengthened_passed`, `document_gap_passed`,
`replayed_artifacts` and `wr_passed`. Each accounting group has `required`,
`accounted` and `pending`; execution groups have required/executed/pass/fail/
missing/skip/timeout/stale counts. A disposition never increments execution.
Keep original `counts` fields for the foundation runner. Add `pending_ids`,
`families`, `dependency_lock` and `fresh_checkout` to suite results; their
objects contain IDs, recorded hashes and Boolean proof states, not editable
success claims. All diagnostics retain the existing exit-code rules.
Artifact replay's complete claim is `artifacts-replayed`, with zero new
engine execution counts. `source-enumerated`, native-original success,
record validity and bounded foundation completion retain distinct claims.

## Section A: Pin and enumerate sources

### A1: Verify every acquired input

As a maintainer, I want immutable source and runtime identities, so that
results can be reproduced without consulting moving upstream content.

**Package:** `nextflowconformance/`
**File:** `nextflowconformance/source.go`
**Test file:** `nextflowconformance/source_test.go`

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
   returns 2 with `E_SOURCE_HASH`, `E_SOURCE_MISSING`, `E_TARGET_IDENTITY`,
   and `E_TREE_INCOMPLETE`, respectively.
4. `A1_04`: In separate extracted-archive subcases, supply `../escape`, an
   absolute member path, two members with the same destination path, or a
   symlink outside the cache. Each returns 2 with `E_SOURCE_PATH`, publishes
   no candidate, preserves the old lock/cache, and writes nothing outside
   the cache. An object one byte over its limit returns 2 with
   `E_SOURCE_LIMIT` and the same transaction guarantees.
5. `A1_05`: Remove the opaque runtime distribution or alter the Java tree
   after acquisition. Offline preflight returns 2 with `E_RUNTIME_MISSING`
   or `E_RUNTIME_HASH`, respectively, before starting Nextflow.
   Network request count is 0.
6. `A1_06`: Acquire the actual pinned distribution with its repeated ZIP
   names and no nested JARs. Acquisition returns 0 and retains all
   42,355,106 bytes with the target hash unchanged. The candidate records
   one `opaque-dist` runtime artifact, no invented bundled JAR artifacts,
   hashed dependency POMs, and its external execution dependencies. Offline
   validation succeeds; no distribution member is written to the cache.
7. `A1_07`: Independently mutate one shell-prefix byte and one shaded-JAR
   byte of the acquired distribution. Offline preflight returns 2 with
   `E_RUNTIME_HASH`, starts no Nextflow process, and makes zero network
   requests in each subcase. Changing only the packaging label or replacing
   the lock's hash with the altered hash cannot bypass the pinned target
   identity check; validation returns 2 with `E_TARGET_IDENTITY`.

### A2: Preserve source units without declaring their meaning complete

As an inventory reviewer, I want every selected byte and meaningful unit
visible, so that omitted warnings and alternatives cannot hide behind a
heading count.

**Package:** `nextflowconformance/`
**File:** `nextflowconformance/extract.go`
**Test file:** `nextflowconformance/extract_test.go`

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
Publish blocks, cross-references and retained reconciliation through the
extraction manifest defined in Architecture. Changes never auto-transfer
an old review based on similar headings.

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
   has nonzero pending blocks outside the bootstrap boundary. The real
   `process-page` reference resolves to unselected `docs/process.md` with
   fragment `process-page`; the Google Cloud URL at process.md bytes
   `[11678,11742)` has an external edge with null artifact ID. Its edge span
   covers the link occurrence within that source region. The Groovy library
   URL in strict-syntax.md is also pending. No network request occurs, no
   destination is selected/extracted solely because of a cross-reference,
   and the source lock and eighteen bootstrap members stay byte-identical.
3. `A2_03`: Remove a warning from generated blocks, collapse the two
   `stageAs` signatures, or delete one grammar alternative. `extract
   --check` returns 1 with `E_EXTRACTION_MISMATCH`, naming the missing span.
   It does not rebuild its expected list from the corrupted output. Deleting
   an external edge or altering reconciliation IDs produces the same exit
   and code. A genuine isolated source-generation change records exact
   removed/added IDs and stale review IDs, retains both generations and
   unchanged review bytes, and passes `--check`. A third change retains both
   predecessors. Repeating either extraction is a byte-identical no-op.
   Add independent isolated fixtures for each review input category. With
   only accepted review `R` bound to the input, change that input alone, leaving
   review bytes unchanged (use a reviewed lock change for source bytes).
   A semantic-only edit leaves blocks, lock and batches unchanged; `--check`
   first returns 1 with `E_EXTRACTION_MISMATCH`. Extraction then publishes
   a new `inputs` hash and ID, empty removed/added arrays and exactly `["R"]`
   as stale IDs. `--check` returns 0; repeating extraction changes no bytes.
   Change the obligation input a second time: both prior reconciliations
   still reproduce from retained payloads, including the original obligation
   block IDs. In independent subcases, delete a current bound semantic file
   (record null, stale `R`, successful extraction/check), delete a retained
   reviewed payload (exit 2, `E_EVIDENCE_MISSING`), and introduce a review
   whose original bound bytes are unavailable (same failure, no publication).
   Reject generated-output review bindings with exit 2 and `E_REFERENCE`.
   Failure/cancellation before atomic publication preserves the old manifest;
   a reader during publication sees one complete generation. Changing only
   a bound semantic file, or creating a captured absent file, at a barrier
   before the publication recheck returns 2 with `E_INPUT_CHANGED` and leaves
   the old manifest unchanged. Every check subcase leaves all input and
   generated files byte-identical.
4. `A2_04`: A duplicate heading has two distinct IDs; an unknown directive
   produces `unclassified` and blocks semantic completion for its region.
   A missing include yields `E_INCLUDE_MISSING`; a cycle yields
   `E_INCLUDE_CYCLE`; both return 2 without publishing. Add closed-schema
   and CLI subcases for each cross-reference kind: reject unknown fields,
   invented pending hashes, dangling artifact IDs, and path escape. Resolve
   an explicit label, local fragment, and extensionless `.md` path exactly.
   Duplicate explicit labels in separate files produce one `ambiguous`
   edge with both sorted definition candidates. In independent fixtures:

   - Unselected `docs/dup.md` contains `(same)=\n(same)=\n`. A selected
     document's `{ref}` use of `same` has two definition candidates with
     target `same` and spans `[0,7)` and `[8,15)`, hashing those exact bytes.
   - Replace that unselected file with `(same)=\n# same\n`: the explicit
     label and heading collide, with target `same` and spans `[0,7)` and
     `[8,14)`. Neither fixture selects or extracts `docs/dup.md`.
   - A selected document contains `[x][r]\n\n` followed by
     `[r]: https://a.example/\n[r]: https://b.example/\n`. The use at
     `[0,6)` has two definition candidates at `[8,31)` and `[32,55)`, with
     literal targets `https://a.example/` and `https://b.example/` in that
     order. Using the same URI twice still retains two candidates. Neither
     case creates an artifact or fetches a URI.
   - With locked `docs/item` and `docs/item.md`, a selected document's
     relative link to `item` has two path candidates in that order, each
     with null fragment and no definition span.

   Each ambiguous use counts as one pending reference. Missing
   labels/paths/fragments remain pending with their respective reasons.
   No unresolved cross-reference becomes an include failure or an acquired
   artifact. None of these cases enlarges selection.

## Section B: Review behavioural meaning and scope

### B1: Link source facets to independently reviewed obligations

As a reviewer, I want requirements checked against original source spans,
so that a self-consistent generated manifest cannot conceal omitted meaning.

**Package:** `nextflowconformance/`
**File:** `nextflowconformance/coverage.go`
**Test file:** `nextflowconformance/coverage_test.go`

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
requirements without origin spans or wr provenance, dangling record links,
and contradictory scope. Check dependencies for cycles and list applicable
interaction UATs. Semantic review can expose gaps that static validation
cannot detect; the report keeps that distinction. Pending source
cross-references are valid inventory and stay visible; they do not
invalidate bootstrap accounting by
themselves. Unresolved external/local references prevent full target
inventory completion with exit 1 and `E_SOURCE_REFERENCE_PENDING`.
Use the generation's input catalog for historical review provenance and
staleness. Only current accepted reviews meeting the Architecture rules
satisfy required-review gates; retained historical copies alone cannot.
Check current review freshness against captured current inputs even before
re-extraction; manifest drift cannot hide `E_REVIEW_STALE`. Require matching
extraction snapshots before awarding a completion claim.

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
   historical evidence and do not count it as current acceptance. A review
   referring to a removed block remains inspectable through extraction
   history; the same unknown ID without that history returns `E_REFERENCE`.
   For an obligation-only or UAT-only edit under A2_03, review `R` remains
   byte-identical. Before and after re-extraction, it yields `E_REVIEW_STALE`
   and contributes zero current accepted reviews. After re-extraction,
   read-only `extract --check` succeeds; a second unchanged extraction is a
   no-op. A current accepted, independent review with matching inputs can
   satisfy its gate even when its identical record also appears in history.
   Removing it from current reviews leaves historical evidence only and
   cannot satisfy that gate. An unresolved source edge remains valid but
   keeps target inventory incomplete with `E_SOURCE_REFERENCE_PENDING`.

### B2: Keep policy decisions separate from observations

As a product owner, I want unresolved compatibility policy visible, so that
it cannot disappear into exclusions or observed test success.

**Package:** `nextflowconformance/`
**File:** `nextflowconformance/model.go`
**Test file:** `nextflowconformance/model_test.go`

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

### B3: Preserve upstream purposes independently of document coverage

As a suite maintainer, I want every original obligation and mapping strength
accounted for, so that portable value tests cannot erase internal purposes.

**Package:** `nextflowconformance/`
**File:** `nextflowconformance/upstream.go`
**Test file:** `nextflowconformance/upstream_test.go`

Initialize genuine records from the frozen pilot and pinned source, with an
independently reviewed lossless projection. Account for original expressions,
helpers, state, selection order, completions, fixtures and native verdicts
separately. Preserve original comparator strength while adding new contracts
for strengthening and document gaps. Retain the pilot's accepted historical
results; record newly packaged runs as distinct attempts. Source/file copying
or projection success awards accounting only, never execution or equivalence.

Report distinct required/accounted/pending counts for each upstream kind,
reviewed document facets, neutral preserved predicates and current results.
A source-derived predicate with no neutral boundary remains original-only or
unresolved and blocks whole-target coverage. Unknown structures/providers
remain pending; an extraction tool cannot relabel them as nonsemantic.

**Acceptance tests:**

1. `B3_01`: Project the actual frozen pilot into the extension records.
   Independent comparison preserves all six methods, eleven units, 42
   predicate comparators/values/types, ten helpers, seven helper-observation
   obligations, fifteen completions, four invocations, 83 fixture identities,
   37 full files, 123 spans, 151 source edges, 34 facets and 28 expectations.
   Table/provider counts are explicitly zero. State/order/config fields and
   all frozen hashes match. No neutral execution pass is created.
2. `B3_02`: From a valid independently reviewed fixture, separately remove a
   selected predicate, helper obligation, shared-parser state entry, resume
   invocation, completion or fixture. `verify` returns 1 with
   `E_UPSTREAM_UNACCOUNTED` and the exact missing origin ID; the fixed
   required count remains unchanged. Deleting an internal entry has the
   same result. A genuine native pass cannot conceal any loss.
3. `B3_03`: For M1, `[1,2,3,"a","b","z",1]` and
   `[1,2,3,"a","b","z","d"]` pass its original seven predicates and fail
   S-MIX's exact multiset. A permutation of the six values passes both.
   Each report keeps upstream and strengthening results distinct. Removing
   G-IN's document-facet link keeps that facet incomplete with
   `E_FACET_UNCOVERED`, even when every original predicate passes.
4. `B3_04`: Seed CLI_P8_MAPPING, STRING_MIX_EXECUTION, all seven internal
   obligations and five separate document closure dependencies. Bounded
   accounting reports them pending, without excluding their affected IDs.
   Promoting CLI-P8's absent syntax rows, numeric M1, a native feature pass
   or a resolved link location into the respective missing neutral/document
   pass returns 1 with `E_MAPPING_UNPROVEN`. `target-inventory` stays
   incomplete, with exact missing IDs and both unresolved product decisions.
5. `B3_05`: Validate all eighteen active schemas and cross-record fixtures
   under the revised contracts; preserve the archived eleven-schema
   acceptance, source lock and eighteen batches byte-identically. F11's
   eleven otherwise valid free-string pairs reject bytes `61 FF 62` and
   accept `61 EF BF BD 62` at the documented supported entry points. The
   isolated guard-removal fault fails all eleven malformed-byte assertions.
   A reconciliation report names retained model/schema/CLI differences,
   active fixture changes and fresh parent-input hashes. Phase 2 cannot
   advance to extraction without independent acceptance of that correction
   and reconciliation; return 1 with `E_CONTRACT_RECONCILIATION` otherwise.

## Section C: Define UATs and discover their executable tests

### C1: Store executable expectations and generate readable specifications

As an implementor, I want concrete inputs and observable results for every
assigned behaviour, so that I can write a meaningful failing test.

**Package:** `nextflowconformance/`
**File:** `nextflowconformance/render.go`
**Test file:** `nextflowconformance/render_test.go`

Neutral expectations use C3's authoritative closed observation format.
The fields `exit`, `values`, `artifacts`, `tasks`, and `diagnostics` are
explicitly checked or have a reviewed `not-applicable` reason. Absent fields
are invalid. C3 also defines completion, predicates, checked-field wrappers
and tagged values. Values are typed JSON, with mode `sequence` or `multiset`.
Multisets preserve
multiplicity. Artifact checks specify relative logical name, byte count,
SHA-256, and optionally exact UTF-8 content. Task checks specify logical
IDs, expected count, and declared ordering constraints. Diagnostics specify
stage, category, source location when known, and required message literals.
Matching a nonzero exit alone is insufficient for an expected error.
An error contract with no emitted values checks C3's empty emission sequence;
it is not `not-applicable`. Zero value lines pass only when all declared
error, task, exit, and artifact checks also pass.
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
   a generated checkbox. Return `E_UAT_MISSING`, `E_REVIEW_STALE`, or
   `E_RENDER_MISMATCH`, respectively; exit is nonzero.
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

**Package:** `nextflowconformance/`
**File:** `nextflowconformance/runner.go`
**Test file:** `nextflowconformance/runner_test.go`

Use `go list -json` to resolve package files under the current build settings
and `go test -list` to discover top-level tests. Every required UAT has one
exact package/test binding. Run with `-count=1 -json -tags netgo` and an
anchored, regexp-escaped test selector. Record discovery output and actual
argv. Reject package patterns and arbitrary commands in bindings. Verify
selected test source is active in the package under those build settings;
a build-tagged-out test is missing, not exempt.

All 69 acceptance tests in this spec map one-to-one to GoConvey functions named
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

### C3: Share reviewed typed contracts across engine routes

As an implementor, I want one neutral expected contract for both engines,
so that runtime observations cannot choose or weaken expected truth.

**Package:** `nextflowconformance/`
**File:** `nextflowconformance/observe.go`
**Test file:** `nextflowconformance/observe_test.go`

The observation grammar retains C1's five checked fields and adds explicit
completion. The first six fields are checked or have a reviewed not-applicable
reason.
Represent checked fields as `{check:true, value}` and inapplicable fields as
`{check:false, reason}`; reason is nonempty text. Expected exit is an integer
or `{nonzero:true}`. Observed exit is the actual integer. Expected has exactly
`exit`, `values`, `artifacts`, `tasks`, `diagnostics`, `completion` and
`predicates`; observed fields have values directly, not check wrappers. Values
contain
`{mode, items}`; mode is `sequence` or `multiset`. An item is exactly one
closed tagged value: `{type:"null"}`, `{type:"boolean", value:bool}`,
`{type:"integer", value:decimal-string}`, `{type:"string", value:string}`,
`{type:"file", logical_path, bytes, sha256}`, or `{type:"list", items}`.
Integer strings use `0|-?[1-9][0-9]*`, avoiding floating-point loss. Lists
preserve nesting and order; multiset applies only to the named outer field.
No file is a string or a list merely because rendered names match.

`values []` in prose or E1's table means zero emissions in this format.
A checked empty emission sequence has this expected `values` field:

```json
{"check": true, "value": {"mode": "sequence", "items": []}}
```

Its observed `values` field is:

```json
{"mode": "sequence", "items": []}
```

One emitted empty list instead has this observed `values` field:

```json
{"mode": "sequence", "items": [{"type": "list", "items": []}]}
```

Its expected form uses the same checked wrapper. It fails comparison against
zero emissions. These examples specify only `values`; every other required
expected and observed field remains present. Bare `values: []` is invalid.

Expected artifacts are `{logical_path, bytes, sha256, content}` with nullable
UTF-8 content; observed artifacts add `raw`, their `file_ref`. Task values
are `{count, logical_ids, order}`; order is an array of `{before, after}`
logical-ID edges, checked against raw trace receipts. Diagnostic values are
`{items, parser_rows}`. `items` is an array of
`{stage, category, source, literals, association}`.
`source` is null or `{path, line, column}` with one-based positions.
`association` is null or `{process, path, declared, actual, script_exit}`;
count fields and script_exit are integers or null. Required literals remain
exact, independently source-reviewed strings. Observer decoding failures are
harness errors, distinct from subject diagnostics. Never infer error category
from nonzero exit or parser count zero from an empty stdout stream.

Completion is `{collection, engine, supervisor}`. Each expected member is
`{check:true, value:true}` or reviewed inapplicable; observations retain actual
booleans plus receipt references. A collected prefix with correct values but
no terminal collection callback is incomplete. Engine exit and supervisor
cleanup cannot substitute for collection termination, or for each other.
Observed completion members are
`{value, receipts}`, with Boolean value and a nonempty `file_ref` array for
checked members; only inapplicable members may have null value/empty receipts.

Predicates are an array of `{upstream_id, field, comparator, expected}`.
`field` is a JSON Pointer into the neutral values/diagnostic object;
comparator is `equals`, `contains`, `not-contains`, `sorted-equals` or
`substring`. Expected is a tagged C3 value; a list contains tagged elements.
`predicates` is an array without a check wrapper, empty only when there are
no preserved upstream predicates in this contract. Non-parser diagnostic
items retain source/name association without asserting a JVM class identity.
Evaluate each preserved original predicate independently, retaining M1's
membership/exclusion and M2/M3's sorted equality. Contract value checks can
add stronger exact-multiset requirements without rewriting these predicates.
An unsupported native-object comparator stays unresolved, rather than being
coerced into this set. Invalid pointers/comparators fail record validation.
For a known integer/string diagnostic scalar, the fixed decoder wraps its
native JSON field in the corresponding tag before predicate comparison.
It never parses a string as a number or discards an existing value tag.

Parser scalars use integer/string fields in an explicitly named ordered
list of `{unit_id, count, line, column, message}`; line/column/message are
null only for count zero. Preserve P6's substring comparator separately from
all exact message comparisons. Parser rows reside in the diagnostic value's
`parser_rows` member, explicitly null for non-parser contracts. Native
SyntaxException/JVM objects are not neutral values. Each decoder is fixed in
code, reviewed against original sources and bounded to named families.

Normalization keeps C1's closed rules. Tag values before string rendering;
retain full raw and normalized observations. Expected and observation files
are separate, and runtime code opens expected inputs read-only. Genuine
native attempts, authored controls and raw original/candidate comparisons
retain their own provenance. A manually supplied comparator fixture is
foundation evidence, even if its bytes equal a real engine result.

**Acceptance tests:**

1. `C3_01`: Compare integer `1`, string `"1"`, a file with logical path
   `one.txt`, and a one-element list containing that file. Only identical
   tags/values/shapes pass. Multiset `[1,1,2]` differs from `[1,2,2]`;
   nested-list order differs even when outer multiset is enabled. An integer
   `9007199254740993` survives encode/decode exactly. Deterministic generated
   tagged values round-trip without loss; generated permutations preserve
   exactly the multiplicities under multiset comparison.
2. `C3_02`: A complete six-item Mix observation passes its reviewed contract.
   Delete the collection terminal receipt, engine exit receipt or supervisor
   stopped receipt in separate subcases. Return 1 with
   `E_OBSERVATION_INCOMPLETE`, name the missing completion, and award zero
   passes. Extra/malformed tagged values fail `E_OBSERVATION_FORMAT`.
3. `C3_03`: Both engine routes resolve one contract ID and expected-file hash.
   Execute Nextflow and retain raw data; requesting wr on the restored tree
   returns 1 with `E_ADAPTER_UNAVAILABLE` and zero wr passes. Changing a
   route's expected hash fails validation with `E_CONTRACT_DIVERGENCE`.
   Contradictory source-reviewed expected data fails `E_EXPECTATION` and
   creates an unresolved disagreement candidate; expected bytes stay exact.
4. `C3_04`: Replay frozen P1-P7 scalar contracts, retaining exact count,
   location and message and P6 substring comparison. Changing P2's literal
   backslash+n into LF fails `E_EXPECTATION`. CLI-P8's captured required
   greeting failure produces `E_MAPPING_UNPROVEN` for P8 count, while its
   separate JVM parser row can satisfy count zero. G-IN/G-OUT generic tool,
   parser or unrelated-process failures fail `E_EXPECTATION`; matching
   associated declared two/actual one errors pass their comparison fixtures.
   No fixture result is recorded as Nextflow execution.

## Section D: Record execution and invalidate old evidence

### D1: Derive status from complete execution events

As a reviewer, I want proof that each test started and finished, so that
zero-test runs and skipped tests remain incomplete.

**Package:** `nextflowconformance/`
**File:** `nextflowconformance/runner.go`
**Test file:** `nextflowconformance/runner_test.go`

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
   Each returns 1 with `E_TEST_SKIPPED`, `E_TEST_FAILED`, `E_TEST_TIMEOUT`,
   `E_TEST_NOT_RUN`, and `E_OBSERVATION_MISSING`, respectively. None
   increments passed; timeout cleanup leaves no child process alive.
3. `D1_03`: Feed malformed/truncated JSON, an unmatched pass, duplicate
   terminal events, or a passing test in a failing package through the
   recorder boundary. Return 2 with `E_TEST_EVENTS`, or 1 with
   `E_TEST_FAILED` for the failing package. Reject success in every case.
4. `D1_04`: Interrupt the runner between log creation and manifest publication.
   `verify` returns 1 with `E_ATTEMPT_INCOMPLETE`. A previous attempt from
   different inputs cannot substitute for the interrupted one.

### D2: Bind evidence to the code, corpus, tests, and environment

As a maintainer, I want changes to invalidate affected results, so that old
passes cannot certify a new implementation or altered expectation.

**Package:** `nextflowconformance/`
**File:** `nextflowconformance/evidence.go`
**Test file:** `nextflowconformance/evidence_test.go`

An attempt records these inputs before execution and rechecks them after:
Git commit; dirty source content digest; target and lock hashes; every
semantic record and review hash; workflow, input, expectation, and
normalization hashes; executable and active Go source hashes; `go.mod` and
`go.sum`; Go compiler identity; build tags and flags; OS/architecture;
locale/timezone; explicit child environment; Java/runtime/dependency hashes
when applicable; runner version; and discovery output hash. Include the
active extraction manifest, its input catalog and every reachable snapshot
and payload in corpus inputs. Also hash current bound inputs, including
absence, so drift cannot hide behind an unchanged manifest. Retained
generation records are authoritative evidence, not excluded cache or
generated Markdown views.

The dirty source digest covers tracked and untracked Go source, embedded
inputs, module/build configuration, and all corpus inputs. Use `go list`
file/dependency enumeration and the repository input inventory; include
files outside the bound package that can affect execution. Generated views,
cache, and attempt outputs are the only excluded output classes. Keep the
exclusion list fixed in the tool; records cannot add arbitrary exclusions.
A clean commit ID alone cannot stand in for content hashes. Changing an
input during a run yields `E_INPUT_CHANGED` and no pass.

Start child processes from an allowlisted environment. Record every passed
key/value. Secrets are forbidden in bootstrap test environments.
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
   test bytes, expected bytes, normalization, build tags, or the distribution.
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

### D3: Keep native, neutral and replay results distinct

As a reviewer, I want route-specific genuine evidence and mapping limits,
so that native tests or portable replays cannot fabricate neutral execution.

**Package:** `nextflowconformance/`
**File:** `nextflowconformance/reference.go`
**Test file:** `nextflowconformance/reference_test.go`

Extend D1/D2 attempts with required `engine`, `route`, `family_id`,
`contract_ids`, `mapping_ids`, `dependency_lock`, `selection`, and
`result_review` fields. `engine` is `nextflow`, `wr` or null; family ID is
an ID or null; contract/mapping IDs are arrays; dependency lock and result
review are `file_ref`s or null. Selection has the suite's native-selection
shape or is null. Inapplicable foundation-fixture fields are explicitly null
or empty arrays, not inferred engine identities. Route is `native-original`,
`neutral`
or `artifact-replay`. `result_review` is a hashed review receipt or null
until reviewed. Native results retain per-feature XML, source order,
unchanged assertions/helpers, original aggregates, invocation exits and
completed supervision. Complete native XML/assertion execution can establish
original predicates reached, but not the raw values of successful assertions.
Missing builds, features, fixtures, resume runs or completions stay incomplete.

Neutral results bind the shared expected contract, reviewed observer and
mapping source argument, actual input/environment/dependency identities and
raw typed receipts. Store statuses per contract and mapped obligation.
Unresolved/partial mappings remain incomplete regardless of successful
execution. Replay verifies already recorded observations; executed/passed
engine counts cannot increase. A newer genuine failed attempt supersedes
an older genuine pass with the same D2 input/route key. Replay attempts
cannot supersede or revive genuine execution attempts.

Launch each native selection under bounded supervision with original
five-second Mix feature timeouts. Allow 300 seconds per native specification,
120 seconds per neutral/CLI invocation, 540 seconds per original CLI runner,
and 1,800 seconds for a recorded offline compile recipe. Ordinary D1 UATs
retain their 180-second ceiling. E3/F3 integration bindings alone may name
up to 3,600 seconds and a one-hour suite ceiling for compile/reconstruction;
record their distinct deadlines. Supervision tracks owned descendants,
including detached and TERM-ignoring children and children created during
termination, escalates KILL, drains bounded streams, waits and verifies all
owned stopped states. Unknown cleanup yields no completed attempt. Keep
historical [PREREQ-F1][prereq-f1] evidence distinct from corrected
supervisor evidence. PREREQ-F1 is the defect that left a detached
TERM-ignoring child alive while recording `completed=true`.

**Acceptance tests:**

1. `D3_01`: A genuine native selected-feature attempt with complete XML,
   original selection order and stopped supervision records original passes
   only. A separate neutral attempt with reviewed mapping/contract and raw
   typed receipts records only its preserved observable predicates.
   Successful native session assertions, MockSession or engine exit cannot
   create neutral identity/lifecycle/raw-original equality results. Removing
   a feature, resume invocation or terminal receipt returns 1 with
   `E_NATIVE_INCOMPLETE` and names the original ID.
2. `D3_02`: Change observer code, mapping source argument, extension resource,
   build recipe or selected native feature order independently. Prior
   affected evidence becomes stale with `E_EVIDENCE_STALE` and zero current
   passes. Deleting raw callback/XML bytes yields `E_EVIDENCE_MISSING`.
   Replaying identical artifacts reports `artifact-replay`, executed zero
   and zero new engine passes. A newer failed genuine attempt still wins.
3. `D3_03`: A controlled subject starts a detached TERM-ignoring descendant
   which starts another child during termination. At its one-second deadline
   the supervisor kills and reaps all owned children; the result is timeout,
   exit 1 and `E_TEST_TIMEOUT`, with stopped receipts for every child and
   zero passes. A fabricated stopped summary without those receipts returns
   `E_OBSERVATION_INCOMPLETE`; unrelated subject processes are untouched.

## Section E: Prove the foundation on real sources and hostile fixtures

### E1: Run real pinned oracle cases with honest claim boundaries

As an implementor, I want a small actual oracle baseline, so that later
runtime comparisons have tested observation and error contracts.

**Package:** `nextflowconformance/`
**File:** `nextflowconformance/oracle.go`
**Test file:** `nextflowconformance/oracle_test.go`

Run the unchanged acquired distribution through its embedded launcher with
Java 21, strict parser v2, local executor, fixed two-task concurrency,
no plugins, isolated work and home directories, and network access denied
by the test environment. Record the actual
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
collecting a missing required output. Author diagnostic literals from pinned
error/formatter source and independently review them before execution.
Until approved, the case is incomplete. Actual runs can reveal a spelling or
semantic disagreement; preserve it and require source-based resolution,
rather than copying observed output into expected truth. The same procedure
settles exact parser spelling for import. A generic nonzero exit never
satisfies either diagnostic contract.

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

**Package:** `nextflowconformance/`
**File:** `nextflowconformance/coverage.go`
**Test file:** `nextflowconformance/adversarial_test.go`

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

### E3: Demonstrate measured families with original and stronger gates

As a suite maintainer, I want finite real family demonstrations, so that the
new accounting and typed observer design has engine evidence beyond E1.

**Package:** `nextflowconformance/`
**File:** `nextflowconformance/oracle.go`
**Test file:** `nextflowconformance/families_test.go`

Freeze these three family selections in `suite.json`, preserving exact pilot
identities/contracts through B3. Native originals run unchanged against the
verified pinned source using the independently reviewed JVM closure. Neutral
observers preserve original workflow/process/shell bytes, adding only reviewed
callback/collection instrumentation in separate authored workflow files.
They use independent shared contracts frozen before actual observation.
For exact source-derived inputs/messages, family preparation binds unchanged
pilot `contracts.md`, `inventory.json`, `expectations.json` and every linked
original/document fixture. Their accepted lossless projection is the initial
family contract authority; E3 does not reconstruct expectations from logs.

- `NF_PARSER`: the three selected ScriptAstBuilderTest features containing
  sequential P1-P8, one original shared parser and original TestUtils.check.
  Native completion covers eight units and 29 predicates. A separately
  published JVM scalar observer uses the genuine parser/TestUtils boundary
  in the same P1-P8 order, checking all 29 scalar predicates and all eight
  authored normalization fixtures. CLI-P1-P7 also check their 28
  count/location/message projections using the reviewed formatter source.
  Run and retain CLI-P8's genuine required-greeting runtime failure as an
  unresolved mapping attempt. Do not supply a greeting default. The JVM
  P8 count zero pass stays separate from that failed CLI mapping.
- `NF_MIX`: the three pinned Mix methods M1-M3, preserving original reset,
  MockSession/helper/config closure and five-second feature deadlines.
  Three independently authored entry workflows preserve the selected
  expressions and tag callback values before rendering. Check the original
  nine predicates, exact S-MIX typed six-item multiset and separate collection
  completion. M1 contains integers 1,2,3 and strings a,b,z; M2/M3 retain
  sorted numeric equality and multiplicity. This boundary does not execute
  the separate chained six-string document example or prove internal helpers.
- `NF_FILES`: unchanged arity and topic workflow/check pairs, each with
  fresh and resume invocations. Preserve all four original predicates and
  original Bash aggregates. Independently check every invocation exit and
  topic's two exact 22-byte `bar: 0.9.0\nfoo: 0.1.0\n` files, with final LF.
  Add four frozen gap contracts: G-IN and G-OUT require associated input or
  output declared-two/actual-one errors; G-OUT also proves script exit zero
  and `one.txt` bytes `one\n`. G-SHAPE-FILE and G-SHAPE-LIST distinguish a
  file from a one-element List<file> before rendering, each with one outer
  item and the same four-byte file. No raw topic multiplicity/order, cache
  correctness, actual container execution or invalid-range coverage follows.

These family launches reproduce a bounded design. Historical logs and
compiled outputs need not be byte-identical. Preserve semantic expected
bytes/types and exact source inputs; record fresh environment and generated
output digests. The fifteen primary neutral units are eight parser, three
Mix and four gap units. Eight CLI parser probes are separate mapping routes;
four original CLI invocations also supply independently evaluated stronger
contracts without being counted as new neutral launches. Reports keep these
denominators explicit. The earlier E1 seven-case gate remains independent.

Each family requires independent selection/observer/expectation review,
genuine native execution, typed neutral execution, completed supervision and
paired loss controls. A selected unavailable original is incomplete, not
translated away. Whole-suite automatic importing is later engineering work
with the same preservation gate, not assumed from these manually selected
families. No reviewed runtime result is imported as expected truth.

**Acceptance tests:**

1. `E3_01`: With the real pinned source, reviewed dependency extension and
   enforced network denial, execute all six unchanged native features and
   four fresh/resume CLI invocations. Record zero native failures/skips,
   42 reached original predicates and fifteen completion receipts. Execute
   the fifteen primary neutral units, eight additional CLI parser probes
   and stronger checks on the four CLI invocations. Check 29 JVM parser
   scalars, 28 CLI-P1-P7 scalars, nine typed Mix predicates, S-MIX/completion,
   six S-ARITY/S-TOPIC exit/byte contracts and all four gap cases. Required
   family contracts pass; CLI-P8 remains unresolved with its actual runtime
   failure. No helper-equivalence, string-Mix, container, wr or whole-target
   pass is created. Missing prerequisites fail rather than skip this UAT.
2. `E3_02`: For every family, remove one selected predicate, fixture,
   required completion or reviewed observer receipt from an independent
   intact copy. Each intact gate passes; each altered gate fails for its
   specific missing ID/hash or observation with
   `E_UPSTREAM_UNACCOUNTED`, `E_ARTIFACT_HASH` or
   `E_OBSERVATION_INCOMPLETE`. Reuse all 155 frozen pilot loss subjects and
   their intact counterparts: eleven unit, 42 predicate, 83 fixture, two
   resume, fifteen completion and two final-LF losses. Report 155 rejected,
   155 accepted and zero invalid controls. These are accounting/byte losses,
   not mutated engine implementations.
3. `E3_03`: Execute the six frozen pilot failure-propagation (F1) Bash
   subjects from `control_contracts.subjects` in
   [inventory.json](research-pilot/inventory.json), recorded as `F1_subjects`
   in [control results](research-pilot/control-results.json), with the unchanged
   arity/nullable checks. Original aggregate exits in contract order are
   `0,1,0,1,0,0`; stronger gates reject precisely the four bad subjects and
   accept both valid ones. Keep per-invocation exits, exact nullable bytes
   `empty input\n\n`, pipeline statuses and completion separately. Run the
   three analytical M1 witnesses from B3_03: both seven-item witnesses pass
   original predicates and fail S-MIX; the permutation passes both. A tool
   failure is an invalid control with `E_MUTATION_NOT_KILLED`, not a killed
   subject. Bash/analytical subjects award no Nextflow or nullable DSL pass.
4. `E3_04`: Alter a neutral observer to stringify integers, deduplicate Mix,
   accept a correct prefix without completion, accept generic count errors,
   or collapse file/list shape in separate fixture subjects. Use S-MIX for
   integer/string loss, ORACLE_MIX's `[1,1,2]` for duplicate loss, the complete
   S-MIX prefix without its terminal receipt, G-IN's unrelated-process error,
   and G-SHAPE-LIST converted to a bare file, respectively. Each intact
   baseline passes. All five mutations fail `E_EXPECTATION` or
   `E_OBSERVATION_INCOMPLETE` for their intended reason; report five killed
   and zero survived/invalid. Sorting/weakening expected contracts cannot
   repair them. Require current reviewed mappings and raw observations
   before these controls can support family acceptance.

## Section F: Preserve the route to inventory and durable runtime work

### F1: Seed later milestones without awarding them completion

As a maintainer, I want later work retained as requirements with explicit
dependencies, so that foundation success does not erase the product goal.

**Package:** `nextflowconformance/`
**File:** `nextflowconformance/coverage.go`
**Test file:** `nextflowconformance/milestones_test.go`

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

**Package:** `nextflowconformance/`
**File:** `nextflowconformance/render.go`
**Test file:** `nextflowconformance/render_test.go`

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
with stale/failed status after subsequent edits. Render extraction generation
IDs, exact removed/added block IDs, stale review IDs, and pending source
references from the retained manifest chain, without rewriting old reviews.
Render each historical reconciliation from its retained input catalog;
later semantic edits cannot change an earlier generation's stale-review
list. Label current review eligibility separately.

**Acceptance tests:**

1. `F2_01`: Generate a batch with two assigned IDs, one dependency, and one
   unresolved question. The briefing contains those exact IDs, source
   excerpts with hashes, executable commands, deadlines, and its completion
   claim. Regeneration is byte-identical.
2. `F2_02`: Delete one linked artifact or change an assigned input hash.
   Its generated checkbox becomes unchecked and states missing or stale
   evidence. A hand-edited checked box makes `render --check` fail. After
   two extraction changes, including semantic-only edits, the ledger still
   shows both reconciliations and the original stale review. Replacing the
   live obligation file again cannot alter either historical reconciliation.
   A missing predecessor or retained input payload keeps the item unchecked.
3. `F2_03`: A batch missing its bounded-input review or containing a
   dependency cycle fails with `E_BATCH_REVIEW` or `E_DEPENDENCY_CYCLE`.
   It cannot generate an approved implementation handoff.

### F3: Deliver reconstructible tooling and fresh-checkout offline proof

As a maintainer, I want a published runnable suite and portable evidence,
so that local research scratch cannot be the only reproducible foundation.

**Package:** `nextflowconformance/`
**File:** `nextflowconformance/package.go`
**Test file:** `nextflowconformance/package_test.go`

Publish all Go, JVM observer, supervisor and wrapper sources, reviewed fixture
layouts, exact native feature selection and build/cache recipes with the
suite. Native source comes from the verified pinned tree. Compile the genuine
unchanged selected specs/helpers with recorded toolchain and classpath;
source/generated/acquired identities remain separate. A vendored prebuilt
fake original or executable that prints expectations cannot satisfy replay.

`package --output DIR` writes a new portable content-addressed directory
with `bundle.json` as atomic publication point. The suite schema defines
its closed shape: `{schema:1, id, revision, suite, dependency_lock, roots,
entries, attempts, review_id}`. `suite` and `dependency_lock` are `file_ref`s;
`revision` is `{commit, dirty_sha256}` from D2. Roots are an array of
`{id, path, historical_prefixes}`, with exactly `corpus`, `source`, `gradle`,
`go`, `java`, `tools` and `evidence` IDs and safe bundle-relative directories.
Historical prefixes are an explicit array of old absolute root strings.
Entries are `{root, path, kind, payload, git_mode, historical_mode}` with
`kind` regular-file or symlink, hash-addressed payload bytes and approved
symlink targets. Attempts list retained attempt manifests and raw receipts
as `file_ref`s. Bundle ID hashes canonical manifest bytes with its ID field
omitted; no self-hash is stored. Reject absolute authority paths, path escape,
duplicate destinations, absent payloads or mismatched bytes before publication.
Historical captures may retain absolute cwd/argv as raw evidence text, with
the corresponding historical prefix in its named root's metadata;
no current file reference resolves through that old prefix. Keep their raw
bytes unchanged rather than falsely presenting them as portable executions.

`restore --bundle DIR` verifies the complete manifest and materializes a new
cache under `--cache` without fetches or hidden global cache fallback. It
reconstitutes the reviewed Gradle URL/cache metadata and actual acquired
resources, Go module/build prerequisites, Java file/link tree, pinned source,
observer sources and tool executable bits. Include every actual transitive
build/runtime input required by the recipes, including Bash/tools and their
loader/library inputs. Record the finite host kernel/OS boundary separately;
no unrecorded executable or library from the old machine supplies replay. Retain
actual resolved versions
and origins rather than substituting requested versions. Copying 578 blobs
without enough Gradle metadata to execute offline is insufficient. Hash
restored inputs before compilation. Generated classes are rebuilt in a new
workspace from their source/tool recipes, with actual fresh command receipts;
matching old generated-byte hashes is not a substitute for compiling them.

[P01][p01] is the historical finding that Git does not preserve group-write
permissions. Its full modes remain immutable metadata and archived local
preservation evidence. Portable source identity uses Git's executable bit,
regular-file/symlink kind, content and symlink target. All 79 historical
nonexecutable 0o664 records remain 0o664 in historical metadata; a new checkout
at 0o644 is valid portable identity. Changing an executable 100755 to 100644
fails. State separately whether historical full-mode reproduction was done;
portable replay cannot claim it by comparing only the executable bit.
Full-mode loss has no authority to change historical sealed manifests.

`replay --bundle DIR` validates retained evidence with the published decoder
and reports `artifact-replay`, no new execution. F3's fresh-checkout gate also
executes new genuine commands after restore. Use a disposable fresh Git
worktree of the reviewed implementation revision at a different absolute
path, clean cache and HOME/XDG/GRADLE_USER_HOME/GOPATH/GOMODCACHE roots, the
same supported OS/architecture and two enforced execution lanes. Both deny
access to the original workspace's source/evidence, ignored scratch and
ambient caches. Read-only shared Git metadata may identify the reviewed
revision; record it separately as a control input. Toolchain paths resolve
only to verified bundle inputs. Record actual mount/access rules, process
identities, network enforcement and cleanup receipts for each lane.

- The offline lane denies acquisition/application traffic, DNS and fixture
  endpoints. Only the owned Gradle IPC defined below is permitted. Restore
  the genuine reviewed closure here; build Go developer tools, test binaries
  and JVM specs/observers from published sources, then execute all genuine
  E1/E3 cases and controls. Run the 61 nonrecursive bindings other than
  A1_01-A1_07 and F3_03 in this lane. Nextflow workflows, Go tools and native
  test application code receive no network exception.
- The fixture lane runs A1_01-A1_07 with real local HTTPS servers and actual
  acquisition requests. Give it the hashed offline-built test binaries and
  reviewed fixture/tool inputs read-only, plus separate empty writable
  corpus, cache, home and temporary roots. Enforce loopback-only networking
  in a private network namespace with no external route or DNS access.
  Record each fixture listener's address, port, server/TLS identity, declared
  request method/path, response status and body hash, and actual request
  count by UAT/subcase. Every delivered request must match its current
  fixture declaration; other requests fail the gate. Only test-owned HTTPS
  listeners, including F3_03's boundary fixture, may serve requests. No proxy,
  host service or dependency repository is reachable. Fixture responses use
  reviewed local bytes or the specified error response, never upstream
  fetches. Acquisition results remain in fixture-owned writable roots.
  No fixture response or writable root may supply a genuine build/engine
  input; the offline lane cannot read or mount them. No genuine build or
  engine executes in the fixture lane.

Use Linux Docker containers with `--network none`, separate PID/mount/network
namespaces, read-only root filesystems, all capabilities dropped and
`no-new-privileges`. The existing Docker service is a declared host control
prerequisite, alongside its kernel, OCI runtime and seccomp support; record
their versions, identities and actual container configuration. No daemon
socket, host network/PID namespace, host service/device or old root is
mounted inside a lane. Mount only verified inputs read-only and each lane's
declared writable roots, plus recorded kernel-provided `/proc`, `/dev` and
tmpfs mounts. Use literal loopback addresses with empty resolver
configuration. Record namespace IDs, interface/routes and mount inventories;
only loopback exists and no external route exists. Load a content-addressed
OCI root filesystem produced offline from the reviewed bundle's locked
tools, loaders and libraries. Its build receipt and digest are dependency
extension inputs; an ambient image tag or an image pull cannot supply it.
The existing Docker service does not establish that this reconstruction or
the following supervisor has passed.

Publish a fixed Linux syscall supervisor with the other tool sources. Its
parent traces its own descendants from launch, including every thread,
fork/clone and exec, using `ptrace` with exit-kill and fork/clone/exec events.
Parent-child tracing requires no added container capability. Publish/hash the
seccomp profile that permits this tracing and denies namespace escape,
untraced asynchronous socket I/O and raw/packet sockets. The supervisor
rejects tracee attempts to detach tracing or transfer sockets to unowned
processes. An unsupported kernel/profile or lost descendant is incomplete
`E_OFFLINE_UNAVAILABLE`, never permission to broaden the socket allowance.
Enforce decisions before connection or data delivery, covering socket
creation/bind/listen/accept/connect, datagram sends, and all socket write,
vector-write and send paths. Follow descriptor duplication/inheritance and
close; reject an unhandled socket I/O path. Denied operations return `EACCES`
with a supervisor-owned decision receipt; an ordinary connection failure
cannot supply that receipt. This is a fixed Gradle/fixture policy, not a
configurable general protocol framework.

The independently reviewed IPC policy binds Gradle 9.3.1/JDK resources and
the measured compile/native recipes, retaining `--offline`, `--no-daemon`,
`--max-workers=1` and `-Dorg.gradle.jvmargs=-Xmx2g`. That JVM setting forked a
single-use daemon in the pilot. Preserve selected specs, helpers, classpaths,
feature order, native configuration and completion obligations. These flags
do not prove zero sockets. IPC permission applies only while these genuine
recipes run; other offline commands receive a deny-all socket policy.

- Identify each client, single-use daemon and worker by current descendant
  PID/start-time, parent, executable hash, actual main class/argv, classpath
  and recipe ID. The Java executable hash alone is insufficient. Record
  socket inode/descriptor generation, local address/port, transport, owner,
  peer identity and the admission receipt. Closing a socket or exiting its
  owner revokes admission, including across PID/port reuse.
- Admit only client/daemon and daemon/worker TCP messaging pairs from
  the pinned Gradle connector. Derive each dynamic endpoint from its current
  daemon registry entry or serialized worker launch address, then match it
  to the actual supervised tool-owned bind. Retain those original address
  bytes and their pinned decoder identity. Provisional tool binds/listens
  may exist while Gradle publishes its address, but no connection, accept
  or bytes are allowed until that match. Hold provisional tool accepts at
  syscall entry while other traced threads publish the address; release
  after admission or fail at the recipe deadline. A log label or loopback
  port alone cannot declare an endpoint. Require the literal `Gradle Magic`
  connection preamble before admitting subsequent bytes, handling split
  writes; it is an extra check, never the sole admission criterion.
- Allow cache-lock UDP binds, including Gradle's wildcard bind, only for
  those tool roles. Delivery requires a loopback destination with an owned
  current lock-listener socket and matching lock-owner record in this run's
  restored cache. Validate the pinned ten-byte payload: version 1, big-endian
  lock ID and type 1, 2 or 3 for unlock request/request confirmation/release
  confirmation. Record every bind and actual datagram, including zero sends
  when no contention occurs. No UDP DNS or arbitrary datagram is admitted.
- All other listeners/connections are denied, including an application or
  HTTP repository in a Gradle JVM, other descendants, fixture endpoints and
  host services. An address declaration cannot override role, socket or
  protocol checks. Inherited anonymous pipes/socketpairs carry only owned
  tool/control IPC; named Unix endpoints cannot supply host/dependency data.

Current genuine compilation and native attempts must pass under this exact
policy. Historical recipes, disassembly and declared rules supply no such
pass. Retain actual allowed TCP flows, UDP binds/datagrams and denied probes
separately from dependency/acquisition/engine requests. Record zero delivered
offline acquisition/dependency/application requests, zero external traffic
and DNS, and zero old-source/cache reads. If genuine tooling needs another
channel, the gate stays incomplete until a pinned, independently reviewed
policy revision and new attempts prove it; no fake tool or socket-free
replacement may supply the result.

Keep C2's actual `go list`/`go test` discovery, flags, selectors and JSON
events. For the seven fixture bindings, the Go driver and compiler remain
in the offline lane; a fixed published `go test -exec` supervisor launches
only the compiled test executable in the fixture lane. Record its fixed
argv, binary hash and separate driver/compiler/test process identities.
Fixture completion receipts return to the outer evidence supervisor only;
the offline build/engine input roots receive no fixture-written files.

Fixture setup acquisition is counted separately from the operation under
test. A1_01 retains exactly three actual HTTPS requests for its three blobs,
then stops its server and records zero requests during offline validation.
A1_02 retains actual 503 responses and its transaction/retry assertions; the
retained all-failed subcase makes two requests and each single-failed subcase
makes four, excluding separately recorded successful setup acquisition.
All other A1 subcases record their declared acquisition/setup requests and
retain their existing zero-request preflight/validation assertions. Fixtures
may serve actual pinned bytes already restored as reviewed test inputs;
preloading the acquisition output or substituting a mocked transport cannot
replace the required real HTTPS acquisition. Fixture passes are foundation
test evidence, never genuine engine or dependency-acquisition proof.

The outer F3_03 supervisor composes exactly those seven fixture results and
61 offline results, each bound to the same current reviewed implementation,
contracts and restored bundle, plus its own reconstruction/boundary proof.
The final gate has 69 bindings; children never invoke F3_03 or the parent
suite runner recursively. Raw lane receipts are hashed attempt artifacts,
not editable success claims. Record allowed delivered fixture requests,
denied connection/read probes, zero delivered external requests and zero
old-source/cache reads separately. Allowed Gradle IPC is counted separately;
offline dependency/acquisition/engine requests are zero.
Unexpected fixture traffic, cross-lane input transfer or any delivered
undeclared offline/external request fails the proof. Inability to enforce a
network/access boundary fails `E_OFFLINE_UNAVAILABLE`; an offline flag alone
is insufficient. Retain every descendant's exit/reap receipt, closed socket
inventory and container stopped/removed receipt on success, failure and
timeout. A live daemon/worker, missing receipt or surviving namespace leaves
the gate incomplete. Do not install system packages.

Tool and harness reconstruction is part of foundation completion. Later full
upstream inventory, internal equivalence and production wr implementation
remain separate F1 milestones, with the pending dependencies above.

**Acceptance tests:**

1. `F3_01`: Export an accepted fixture bundle, relocate it to a new root,
   remove access to the old root and restore offline into an empty cache.
   All logical references, raw captures and reviewed dependency identities
   verify; artifact replay reports zero executions and zero new engine passes.
   A missing acquired blob/cache-metadata input returns 2 with
   `E_DEPENDENCY_MISSING`, changed bytes return `E_ARTIFACT_HASH`, and an
   absolute/current escaping path returns `E_SOURCE_PATH`. No fetch or
   fallback to a user's cache occurs, and failure publishes no restore.
2. `F3_02`: In a fresh Git checkout, all 79 recorded nonexecutable files at
   0o644 retain valid content/executable identity and historical 0o664 mode
   metadata. Report portable mode verified and historical full-mode replay
   not performed. Remove one required executable bit or change a symlink
   target: fail `E_EXECUTABLE_IDENTITY`. All frozen historical hashes and
   archived full-mode records remain byte-identical.
3. `F3_03`: Restore the genuine reviewed dependency closure at a different
   absolute root with empty user/global caches and no old-workspace access.
   Offline Go and native JVM compilation, six selected native features,
   four original CLI invocations, E1/E3 required neutral cases and controls
   all complete from published sources. New actual command and cleanup
   receipts and all 68 nonrecursive bindings pass in their specified lanes.
   A1_01 records three real fixture acquisition requests and zero subsequent
   validation requests; A1_02 records actual 503 responses and its specified
   retry counts. All delivered fixture requests match their declarations;
   offline acquisition/dependency/engine requests, delivered external
   traffic/DNS and old source/cache reads are zero. Current daemon/worker
   TCP messaging succeeds with owned endpoint/preamble receipts; lock UDP
   binds and actual datagram counts are separate IPC evidence. Actual
   enforcement, all descendant exits and stopped-container receipts exist.
   In a separate boundary subcase, remove the resolved Spock resource from
   a separately restored offline cache. A declared fixture serves its exact
   bytes via HTTPS at `/F3_UNDECLARED_SPOCK`; one fixture-lane GET returns 200
   and the matching resource hash, with output confined to the fixture root.
   From the offline sandbox, connection probes to that listener and an
   external address, and read probes to the fixture output and old cache,
   are denied and deliver no requests/bytes. Genuine native-build preflight
   returns 2 with `E_DEPENDENCY_MISSING`, naming Spock, starts no affected
   build/engine and awards zero passes. Restoring Spock solely from its
   reviewed bundle restores the genuine offline build/engine pass. The
   IPC control also starts a supervised undeclared HTTP repository in a
   separate boundary container with the same offline image, namespaces,
   mounts and IPC policy. It serves the exact missing Spock resource at
   `/F3_UNDECLARED_SPOCK` and returns 200 with its hash to one explicit
   supervisor-only readiness GET, whose bytes cannot reach build roots.
   This one diagnostic listener/GET is marked control traffic and excluded
   from tool admission. Revoke that diagnostic permission before launching
   the genuine Gradle recipe with this repository configured. Preserve the
   endpoint's same usable address and owner; its readiness receipt rules
   out an absent service. Even the genuine Gradle daemon/worker roles cannot
   connect or deliver HTTP to it. After restoring the reviewed closure, a
   separately reviewed control init script makes one literal URL connection
   probe from the actual Gradle daemon, asserts the supervisor's denial and
   continues the unchanged native selection. Record that denied connection,
   zero build/dependency requests at the server and no acquired resource.
   With Spock still missing, preflight returns `E_DEPENDENCY_MISSING` before
   an affected build; with the reviewed closure restored, a new genuine
   build/native attempt passes with its owned IPC and zero repository
   requests. A namespace-only or Java-executable-only allowance must fail
   this control. Stop/reap the diagnostic server and every descendant. The
   outer F3_03 plus the 68 current results satisfy the 69-UAT foundation
   gate, with specified pending dependencies, two product decisions and
   zero wr passes. No ignored pilot script or local absolute executable is
   used. This gate awards fresh-checkout foundation proof only.
4. `F3_04`: Remove the published observer source, native source dependency,
   resolved Spock/JUnit resource, Gradle metadata or Go prerequisite in
   independent replay setups. Each fails `E_DEPENDENCY_MISSING` before its
   affected execution and produces no substituted engine pass. A build or
   capture timeout is incomplete. Setting `complete:true` or replaying old
   successful captures cannot make the fresh-checkout gate pass. Missing
   sandbox enforcement fails `E_OFFLINE_UNAVAILABLE`, with incomplete proof.

## Implementation Order

1. Retain historical A1 and eleven-schema Phase 1 acceptance. Acquisition
   and identity obligations remain required; reviewed extensions belong to
   later phases. Implement A1 and closed schema validation for new inputs.
   Acquire pinned source and
   runtime artifacts into a candidate cache. Review actual hashes and the
   bootstrap selectors before accepting the lock. Write failing tests for
   fetch-all-failed, empty corpus, and malformed records first.
2. Reconcile retained contracts and F11, then implement A2, B1, B2 and B3
   sequentially. Introduce reviewed suite/dependency inventories without
   changing the accepted lock or bootstrap batches. Independently test
   the amended block/extraction schemas and generation loader in Phase 2;
   Phase 1 acceptance does not cover them. Use independent extraction fixtures
   before interpreting the real bootstrap regions. Author and independently
   review obligations from original sources; seed unresolved decisions.
3. Implement C1, C2 and C3. Create executable expectation records and exact Go
   test bindings. Import every numbered acceptance test in this spec into
   foundation requirement/UAT records with its unchanged ID and source
   provenance. Compare that import against this spec in independent review.
   Subsequent changes update records first and regenerate the views.
4. Implement D1, D2 and D3. Prove execution-event and freshness failures before
   relying on any generated completion report. Preserve raw evidence.
5. Implement E1 with the actual pinned oracle, E2's full mutation suite and
   E3's native/neutral proven families and paired controls. Resolve harness
   defects and review exact diagnostic literals. An external
   prerequisite failure blocks this gate; do not replace it with fixtures.
6. Implement F1, F2 and F3, prove fresh-checkout offline replay, regenerate
   all views, and execute the final gate.
   Reviewers can review independent semantic batches concurrently after
   source locking; dependent implementation steps remain sequential.

The final `foundation-bootstrap` gate requires all 69 acceptance tests,
seven actual E1 oracle cases, eighteen E2 accounting controls, three E2
semantic observer controls, E3's three proven families with current native
and neutral evidence, F3's fresh-checkout proof, and current independent
reviews of every selected accounting/mapping/expectation input. Require
byte-complete extraction, checked reference edges, retained reconciliation,
matching generated views and current artifact hashes. Report zero missing,
skipped, failed, timed-out or stale foundation UATs and required family
executions. Preserve distinct accounting, native, neutral, strengthened,
document-gap and replay counts; no combined language-coverage percentage.

The expected successful bounded report still lists both product decisions,
CLI-P8's failed CLI mapping, seven unresolved helper/internal equivalence
obligations, unexecuted string Mix contracts, five document closure links,
other pending target inventory, no wr adapter and zero wr runtime passes.
Those records are required dependencies of later completion, not successful
foundation executions. `target-inventory` and `wr-runtime` remain incomplete.
Historical Phase 1 completion remains accepted; Phases 2-6 and all additions
need implementation and independent acceptance before this gate can pass.

Run relevant GoConvey tests with `CGO_ENABLED=1`, `-tags netgo`, and
`-count=1`, followed by repository lint and required repository tests. Bound
individual commands with `timeout`; allow longer repository-wide checks
only with a recorded deadline and continuing progress updates. Suggested
foundation commands after offline acquisition are:

```bash
timeout 20m go run ./cmd/wr-nextflow-conformance run --suite foundation-bootstrap
timeout 2m go run ./cmd/wr-nextflow-conformance verify --suite foundation-bootstrap
timeout 2m go run ./cmd/wr-nextflow-conformance render --check
timeout 20m env CGO_ENABLED=1 go test -tags netgo -count=1 ./nextflowconformance/...
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
current passing executions. Upstream predicates, helpers, state and
completion have separate denominators from documented facets and gap cases.
Native assertion success, neutral mapping strength and raw-value comparison
are separate claims. No single percentage merges them.

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

[pilot]: research-pilot/pilot-report.md
[pilot-review]: research-pilot/pilot-report-review.md
[schema-f11]: reviews/nextflow-phase2-schema-review-06.md
[prereq-f1]: research-pilot/prerequisites-review-01.md
[p01]: research-pilot/contracts-review.md
