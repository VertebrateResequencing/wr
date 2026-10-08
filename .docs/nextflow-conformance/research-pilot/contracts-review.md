# Phase 1 independent handoff review

Item 1.3: PASS. R-UAT-01 is accepted for the actual hashed, source-derived
handoff below. No blocking preservation finding remains. The permission
portability finding P01 is retained with root as its owner before reuse from
a fresh checkout. No original, observer, oracle, translation, control or wr
runtime pass follows from this review.

## Accepted artifacts and boundaries

The review used `/home/ubuntu/wr`, branch `nextflowdsl`, and pinned Nextflow
26.04.6 source commit `232b60569865e9a4577e48c1955409238359d6ca`.
Contribution review 01 accepted Item 1.1; correction review 02 accepted Item
1.2. Independent comparison preserves their complete semantic records,
including the repaired per-file document fixture identities. All 119 current
contribution files, 410 prior independent-review snapshot files and 1,098
prior report evidence files still match their recorded identities. The
unmodified passive author gate also passes against the actual final files.

The independent gate compares every JSON leaf with its exact type and checks
actual retained bytes. Its lossless relocation projection contains all 238
changed scalar path fields, with 100 distinct target paths. Original fixture
paths gain `originals/`; document fixture paths gain `documents/`; document
evidence paths resolve under `fixtures/provenance/spans/documents/`. Other
source paths, source origins, comparators, expected values, types, state,
expressions, aggregate rules and pending dispositions remain unchanged.

All 42 original predicates have independently checked added typed inputs.
Each P/M value matches its actual decoded fixture, literal identity and
accepted unit record. Static inspection preserves leading-line suppression
for P1-P8 and leading LF/indentation for M1-M3. Original Mix channel values
retain integers versus strings. CLI inputs bind the correct workflow source
and fresh/resume invocation. These are source-derived input records; genuine
Groovy/JDK dispatch and parser-normalized bytes remain Phase 2 obligations.

Original, document, strengthened, gap and control claims remain distinct.
Document Mix contains six strings; S-MIX contains three integers and three
strings. P2 retains backslash plus n in its message, P6 retains substring
comparison, and P8 retains count zero only. None of those expectations was
rewritten from an engine observation.

## Complete accounting and source evidence

The independently recomputed denominator is six methods, eight parser units,
three Mix units, 29 parser predicates, nine Mix predicates, two workflow/check
pairs, four CLI invocations and four literal CLI predicates. That is eleven
Spock units and 38 Spock predicates. Table rows and generated providers are
both explicitly zero. Seven helper observation obligations remain separate
from the 42 original predicates; all fifteen required completion records
remain pending.

The gate resolves 980 explicit references within their definition domains,
including helper, literal/decoded fixture, method/child, predicate,
invocation, completion, source and facet references. It verifies reciprocal
method-child, unit-predicate and subject-completion bindings. All 83 fixture
IDs and paths are distinct. All 151 dependency edges resolve, preserving the
142 original edges and nine document source-dependency edges.

All 83 contract fixture bytes and modes match the accepted contributions.
The 37 full source files and 123 separately identified spans match the pinned
cache directly, including source lines, half-open byte offsets, byte counts,
SHA-256, Git blobs and retained permissions. Document spans retain their
accepted staging modes. Both original author narratives preserve bytes and
modes. The two named SOURCE records bind the same archive without merging
original/document origins. No resource was acquired.

All 34 prior facet decisions, subject IDs and detection rationales are
retained exactly. Direct pinned document/test inspection confirms their
bounds. Valid arity examples do not detect missing invalid-count rejection;
only G-IN/G-OUT author those associated count-violation contracts. Topic's
`unique` and sorted `collectFile` retain distinct version checks while hiding
raw multiplicity and emission order. The full all-sent-values facet remains
partial. String Mix examples, file versus one-element file-list shape,
warning/default boundaries and all five outgoing unresolved semantic links
retain their original dispositions. Internal helper and observer mappings
remain unresolved or pending.

Arity and nullable retain `set +e`, immediate status/file expressions and
final-expression aggregates. Nullable retains tee without pipefail and its
exact 13 bytes. Topic retains both exact comparisons and the 22-byte expected
fixture with final LF. The six static control subjects retain aggregates
0, 1, 0, 1, 0, 0 and their separate stronger outcomes. These are frozen
control contracts; no Bash subject ran.

## Independent controls and Phase 2 mutability

Twenty-two bounded independent subjects passed their intended checks.
Intact preservation, accounting, typed-input, seal and extension subjects
pass. Bad subjects receive specific rejection for boolean/float/string
coercion, fixture collision, child crossbinding, premature completion,
missing P1/helper/resume/completion IDs, wrong decoded value or fixture ID,
Mix input type coercion, fresh/resume crossbinding, altered seal hash and
missing sealed artifact. These supplement the author checker rather than
using its outputs as the expected result. They do not award Phase 3's full
loss-control or runtime UAT.

The working and frozen Phase 1 manifests are byte-identical. The manifest
binds 248 artifacts; the external seal binds those artifacts plus both
manifest versions, 250 total. No circular self hash is required. This review
binds the actual seal hash externally; the seal's historical author status
remains unchanged.

An isolated copy accepted a real working-manifest extension with pending
acquisition/capture records while preserving every frozen manifest, seal and
other sealed artifact identity. Rewriting Phase 1 resource history rejects.
Changing seal acceptance metadata changes its externally bound SHA-256 and
rejects. Thus later acceptance belongs in review records, not a rewritten
seal. The author's Phase 1-only gate rejects the permitted working-manifest
extension at `verify-handoff.py:242`; Phase 2 must check frozen history and
new records separately rather than require the working hash to stay frozen.
No actual acquisition or runtime capture is claimed by the extension control.

## P01: Local permission evidence is not portable Git identity

This is a nonblocking limitation of the bounded local freeze. Root owns its
routing before the artifacts or passive gate are reused from a fresh Git
checkout. `verify-handoff.py:231` compares full filesystem modes, and
`phase1-handoff-seal.json:12` records `0o664` for `contracts.md`. Seventy-nine
sealed records use that non-executable mode. Git preserves the executable
bit, but does not preserve group-write permission.

An intact copied handoff passes the author gate. Changing only copied
`contracts.md` from `0o664` to ordinary checkout `0o644` preserves bytes,
SHA-256 and Git blob. Actual `git diff --no-index` with `core.fileMode=true`
returns zero with no diff. The author gate then fails at line 231; the
independent seal gate identifies `contracts.md:mode`. Restoring the observed
permission passes again. The accepted source and fixture files were untouched.

Meaningful executable-mode evidence remains required. Four sealed runner
source artifacts have `0o755`. A separate copy changed the runner fixture to
`0o644` without changing bytes. Git reports `100755 -> 100644`; the author
gate rejects `FX-SOURCE-23 mode`, and the independent seal gate rejects the
same artifact's mode. Ignoring every permission check would lose that
required guarantee.

P01 does not block this local R-UAT-01 handoff: its actual bytes, source
identities, observed modes and complete accounting are verified, and Phase 2
can extend the working manifest here while recovering unchanged Phase 1
history. Fresh-checkout reuse must explicitly restore the recorded local
permissions or distinguish historical filesystem/archive permission evidence
from portable Git artifact identity, retaining executable-bit checks. No
Nextflow/runtime failure or portable-checkout validation pass is inferred.
The archived and extracted source permission fields remain historical evidence
and must not be erased to hide this limitation.

## Immutable SHA-256 bindings

The actual hashes accepted by this review are:

```text
contracts.md
b1076f4a2ce3a02e94bb1e4426939d5563cdbe4e40bd1b29763fab3828545490
inventory.json
fb47f83383638da61c369645e8083ba6d9484591b327d6ec1089beeb892eb159
expectations.json
2bcada9124beaeb5374f0adb740d2408cc4816c3b8219747affc056d97a3024b
research-manifest.json
2ac04fa4c27a17753afe39d9dcbf6489bafd5027475799099b0359787f0052d0
phase1-frozen-research-manifest.json
2ac04fa4c27a17753afe39d9dcbf6489bafd5027475799099b0359787f0052d0
phase1-handoff-seal.json
fd14d9c7c7b8c8ab7f37f99b9c406434b13b21c534bc24c563f0aff1c9356957
```

The seal supplies the individual immutable fixture/source bindings. R-UAT-01
acceptance applies to these Phase 1 versions. Later working-manifest hashes
must preserve and reference this frozen history rather than replace it.

## Review effort, quality gates and completion

This is independent handoff review round 1. The actual start clock was
2026-10-08T12:37:19Z; the substantive decision clock was
2026-10-08T12:53:08.073526Z, 949.074 seconds or 15.818 wall minutes.
`groups.json` retains actual successive boundaries for instructions/code,
semantic/path/input/source review, controls/Phase 2 mutability and final
source/quality/decision review. These intervals do not overlap. Automated
check intervals are recorded separately inside the evidence and are already
within the review wall interval; they are not added as extra review effort.
P, M, CLI, document, gap and control claims were reviewed together; no
invented individual-contract minutes are allocated. The stage deadline is
2026-10-08T13:34:06Z. The final completion record accounts for report and
artifact verification after the decision clock.

Both author scripts and both reviewer scripts parse under Python 3.12.3.
Manual code review and actual data controls are the applicable bounded
gates. Ruff and pyright are unavailable and were not run; no lint or strict
static-type pass is claimed. Pipeline nf-test/nf-core and Go build gates do
not apply to this data-only handoff under the charter's explicit scope.

[Review evidence][evidence] retains scripts, lossless new-field/path
projections, raw semantic outputs, mode/extension controls, source bindings,
clock groups and immutable/output hashes. No merge or freeze command,
Nextflow, JVM, Groovy, original/control Bash harness, build, download,
adapter, production change, commit, push or child agent occurred. Writes
were confined to this report and owned review scratch. Root retains queue,
phase transitions and delivery ownership. All owned checks finished; no
background process, tool wait, job or child remains live.

[evidence]:
 ../../../.tmp/agent/nextflow-conformance/research-pilot/handoff-review01/
