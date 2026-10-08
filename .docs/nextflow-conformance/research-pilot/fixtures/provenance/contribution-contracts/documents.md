# Independent document contracts

This Item 1.2 contribution reconstructs the bounded B1 documents directly
from commit `232b60569865e9a4577e48c1955409238359d6ca`. Its document facets
were authored without runtime observations or the original author's inventory.
All executable outcomes remain unattempted. These files are research data,
with no production API or schema claim.

## Frozen evidence

`provenance.json` binds six B1 document ranges and both complete Mix includes.
Each entry retains inclusive lines, half-open byte offsets, full and span
SHA-256, byte counts, Git blob identity, Git/archive/extracted modes, origin
and dependency edges. `evidence/` retains exact span bytes. Verification
compares extracted bytes against both the pinned source lock and archive.
Resources were reused; none were acquired.

The selected ranges are strict syntax 78-123, typed workflow 3-17, operator
Mix 826-852, process input 155-177, process output 207-223 and channel topic
339-399. Both `mix.nf` and `mix.out` are complete. The latter is 11 bytes and
has no terminal newline. It illustrates one possible order and does not
promise exact display bytes for every execution.

Nine additional source spans support strengthened and harness-control
expectations. They are distinct from document evidence. Original predicate
accounting belongs to the separately authored original contribution.

## Bounded facets

`facets.json` has 34 stable facet IDs. Its typed values retain these claims.

- Strict syntax allows the eight listed top-level declaration categories.
  Statements-only snippets form implicit entry workflows. Declarations and
  statements cannot mix at the same level under the strict parser. The stated
  reason concerns top-level statements executing when a module is included.
- Typed workflows require parser v2 and a typing flag in every participating
  script. Params and output blocks can be used without that flag. These
  introductory lines do not define the complete typed workflow syntax.
- Mix returns a channel containing source items, with arbitrary emission
  order. The complete example uses strings `'1'`, `'2'`, `'3'`, `'a'`, `'b'`
  and `'z'`. Its two queue channels and one value channel remain distinct.
- Path input arguments can be identifiers or strings. Identifiers bind body
  variables; strings provide staged aliases. Arity accepts numbers or ranges
  and rejects invalid file counts. The option was added in 23.10.0. Name
  accepts a filename or pattern; stageAs aliases name. Env inputs are strings.
- Path output matches task-environment files by pattern. Its arity option was
  added in 23.10.0 and rejects invalid counts. Arity 1 emits a file; other
  arities emit a list even when it contains one file. Without arity, runtime
  count chooses file or list, with the documented mixed-channel warning.
  The bounded spans specify no default input count or followLinks value.
- Topic takes a String name and returns a Channel. Sources implicitly send
  to matching names. The typed example uses a topic section and `>>`; the
  legacy form uses the output topic option. The factory emits all sent values.
  Sorting and deduplication are not specified here. Topic arrived in 25.04.0;
  its preview flag applied to 24.04 and 24.10. A consuming process emitting
  back into its topic causes the documented hang, directly or indirectly.

The outgoing references `process-multiple-input-files`,
`syntax-workflow-typed`, `migrating-static-types` and `process-typed-topics`
remain unresolved. The link to `strict-syntax-page` has only its selected
mixing range frozen; the remainder is also unresolved. Neither these spans
nor a grammar establish full-language closure. Predicate-to-facet links await
independent review and a rationale naming the violation each detects.

## Separate document and strengthened expectations

`expectations.json` gives each claim a stable ID, typed input, comparator,
typed expected value, observation boundary, rationale and provenance.
`D-MIX-EXAMPLE` requires the six string values with order ignored.
`D-MIX-COMPLETION` requires collection completion. Exact multiplicity is the
document reading submitted for review, rather than a hidden original claim.

`S-MIX` strengthens M1 to the exact six-item multiset
`[1, 2, 3, 'a', 'b', 'z']`, each item once, with complete collection.
The first three values are integers. No string-to-integer coercion is allowed.
This differs from the string document example. Original M1 membership and
nonmembership predicates accept both a duplicate `1` and an extra `'d'`;
S-MIX rejects those witnesses. Any six-value permutation passes S-MIX.
Variadic source Mix and chained document Mix have separate provenance.
Their observer mapping remains subject to review.

`S-ARITY-FRESH-EXIT` and `S-ARITY-RESUME-EXIT` require individual zero engine
exits for the selected valid-count workflow. Its counts are 1, 2 and 1..*.
These claims promise no file contents, invalid-count coverage, scalar/list
behavior or actual cache reuse. The original check's `set +e` can let final
resume success hide fresh failure; the stronger claim retains both exits.

S-TOPIC requires both individual zero exits and both exact file comparisons.
The expected file is 22 bytes, `bar: 0.9.0\nfoo: 0.1.0\n`, with newline
bytes represented by the escapes here. Its SHA-256 is
`ce5e4c500ca731aa86fa5e5a3856b9bdbe3c64f685d0e51b2ba5d1886b13bceb`.
Sorting, unique values and these version strings come from the selected
workflow/check source. They are not general topic documentation guarantees.
Fresh and resumed file captures remain separate; neither proves cache reuse.

## Authored gap inputs

Four new `.nf` fixtures retain exact authored bytes before any execution.
They use parser v2 with static typing disabled and add no preview flags.
Actual executor/container mode remains unresolved until capture. The gap
workflows contain input or production steps only; no engine adapter or
type-observation code has been authored.

| Contract | Declared arity | Actual files | Required observation |
| --- | --- | --- | --- |
| G-IN | 2 | One existing input file | Associated input count violation |
| G-OUT | 2 | One produced `one.txt` | Associated output count violation |
| G-SHAPE-FILE | 1 | One produced `one.txt` | One file value |
| G-SHAPE-LIST | 1..* | One produced `one.txt` | One-element file list |

G-IN and G-OUT require a nonzero engine exit with evidence associating the
declared and actual counts with the named process and affected path input
or output. Parse failure, missing fixtures/tools or an unrelated failure
cannot satisfy them. Documentation promises failure, not exact error text.
The diagnostic observer still needs independent mapping review.

G-SHAPE requires successful completed invocations and a type-preserving
observation before filename display or string coercion. Identical printed
filenames cannot prove file versus list. The expected representation identifies
shape and basename without assuming a JVM class or absolute work path.
An independent reviewer must accept that mapping before awarding equivalence.
These cases make no claim about the rest of the upstream test suite.

## C2 controls only

`controls.json` freezes nullable's 13-byte `empty input\n\n` fixture and
four separate expressions at source lines 8, 9, 17 and 18. Its SHA-256 is
`d4aa42007b1cbfce672a372a1a97587ffdd4102f52dc3b200314a8081206a019`.
Both byte-identical arity and nullable check scripts are retained as fixtures.
The genuine runner's `bash -ex .checks` call is frozen at line 49.

The original F1 aggregate expectations for the four invalid subjects are
0, 1, 0 and 1. Both valid counterpart subjects have aggregate zero.
Every stronger invalid gate rejects with its specific invocation/predicate;
both stronger valid gates accept. Expectations were derived statically from
the scripts; no Bash control was executed in this contribution.

Nullable uses `set +e` and pipelines ending in tee without pipefail. The two
pipeline-status expressions therefore observe tee status, which can hide
nonzero engine exits. Earlier byte failures continue. The final resume-byte
expression determines aggregate exit. Arity's final resume-status expression
likewise determines its aggregate. Control inputs use a one-newline nullable
output as the explicit wrong bytes and zero tee exits.

These controls concern Bash failure propagation only. Nullable typed DSL
execution, Nextflow success and wr execution remain outside their claim.

## Handoff and verification

`inventory-contribution.json` separates DOCUMENT, STRENGTHENED,
DOCUMENTED-GAP and CONTROL origins. It adds no original denominator.
`manifest-contribution.json` binds source and authored artifact identities.
`author-record.json` records time, effort, retained file hashes and checks.

Each of the 13 fixture files has its own stable ID in both fixture inventories.
The Mix members use `FIX-MIX-DOC-NF` and `FIX-MIX-DOC-OUT`. Nullable uses
`FIX-NULLABLE-CONTROL-CHECKS` and `FIX-NULLABLE-CONTROL-EXPECTED`. Topic uses
`FIX-TOPIC-CHECKS` and `FIX-TOPIC-EXPECTED`. These are per-file identities;
there is no shared bundle ID. Expectation and control `fixture_references`
resolve each ID with its path. Manifest fixture artifacts carry `fixture_id`.
The data gate rejects duplicate or missing IDs, path disagreements and
references that differ between the expectation and inventory records.

Run `python3 verify.py` from this staging directory to verify pinned full and
span bytes, archive and Git identities, complete includes, typed expectations,
gap counts, control expressions and document mechanics. This is a data-only
gate. Engine observations, executable observers, mapping acceptance, joint
expectation/preservation review and execution passes remain unresolved.
