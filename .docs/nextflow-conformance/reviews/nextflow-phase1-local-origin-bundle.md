# Phase 1 local snapshot origin correction bundle

Verdict: APPROVED for scope and input size on 2026-09-28, on branch
`nextflowdsl`. Dispatch one fresh Go implementor, followed by a separate
fresh Go reviewer. This approval awards no implementation or acceptance
pass. Item 1.1 remains reopened until the correction receives PASS.

## Contract and correction boundary

The spec requires existing local Java and actual execution files at
`spec.md:110-129`, HTTPS network acquisition at `spec.md:130-149`, and
immutable artifact origins and runtime edges at `spec.md:197-207`.
These clauses do not contradict each other. The universal artifact HTTPS
rule in the implementation excludes a required source of execution files.
No specification change is needed or authorized.

Approve this representation in the existing required string `origin`:

- Existing HTTPS origins retain their exact syntax and network behavior.
  Lock and tree origins remain HTTPS-only.
- `local:sha256:<digest>` denotes acquired snapshot bytes, with exactly
  64 lowercase hexadecimal digest characters. It is permitted only for
  `Java` and `environment-tool` roles, with `file` packaging and null
  coordinate. The digest must equal `file.sha256`.
- A local origin is an immutable content identity, not a URI to fetch or
  a claim about an upstream vendor. It supplies no filesystem path.
  `file.path` locates the existing snapshot inside the cache.
- Each local artifact references a regular file. Java symlinks remain in
  the Java inventory, with their link bytes and targets accounted for by
  that inventory. A symlink cannot masquerade as a regular local artifact.
  Runtime dependencies continue to include the Java and environment-tool
  artifact IDs under the existing closure rules.

Keep one authoritative origin syntax rule shared by decoder and emitted
schema. Role and packaging conditions must reject local origins on source,
launcher, runtime, JAR, and dependency-metadata artifacts. Reject malformed
local strings, uppercase digest characters, empty or extra components,
whitespace, trailing newlines, query/fragment suffixes, and file/HTTP URIs.
Existing closed shapes, required fields, coordinates, packaging identity,
dependency cycles, and missing or extraneous execution edges stay binding.

Stock Draft 2020-12 schema checks syntax, role, packaging, and nullability.
It cannot compare two arbitrary instance strings. Enforce origin digest
equality in the decoder and describe that relational check through the
existing `x-wr-record-rules` annotation. Put equality-mismatch cases in
decoder/CLI relational tests, separate from the common stock-schema
mutation corpus. State this distinction in evidence; do not claim that
an annotation enforces equality or weaken the equality check for parity.

For locked acquisition, retain the local artifact's existing snapshot path
and rehash its actual bytes and length before publication. Reuse the
existing containment and regular-file checks. Preserve its bytes and mode;
no new copy is needed. Missing, altered, non-regular, escaped, or symlinked
snapshot paths fail explicitly. Cover symlinked path ancestors and the
cache boundary as well as the final component. Retained files participate
in candidate-input collision protection. Failures preserve previous locks,
cache, evidence, and snapshot permissions. HTTPS artifacts still use the
existing bounded HTTPS fetch path; local origins never reach it.

Source provenance belongs in acquisition evidence, separately from content
identity. Record the actual source path, resolved target, acquired snapshot
path, SHA-256, byte count, and mode from observed files. Never manufacture
an upstream URL. This correction proves representation and reuse using
local files; part 2 must bind the same evidence to the actual Java/tools
that it copies and uses. Inventories alone do not supply runtime edges.

## Owned changes and deferred work

Owned production changes are the artifact origin rule and conditions in
`model.go` and `schema.go`, the lock schema generated from them, and local
snapshot dispatch in `acquireLockedArtifact`. Small helpers are permitted
where those responsibilities require them. Existing verification and
publication helpers may change only if a regression demonstrates that
local snapshot reuse requires the correction. Include their full changed
functions and callers in independent review.

Owned test changes belong in `model_test.go`, `source_test.go`, the
independent schema mutation corpus, and narrowly needed lock fixtures.
Keep the common schema runner unchanged unless a concrete new test need
requires it. Update only the generated `sources.lock.v1.json` schema if
the generator's other outputs are unchanged. Do not hand-edit generated
schema rules or add a new CLI argument, record field, dependency, or public
API for this correction.

The existing part 2 bundle continues to own opaque runtime handling,
executable permissions on downloaded runtime files, pinned acquisition's
actual Java/tools closure construction, POM acquisition, A1_05 through
A1_07 completion, the real candidate, and selector/include acceptance.
Read its full scope before work to preserve that boundary. Its statement
that Item 1.1 remains closed is superseded by this correction. Do not
complete part 2 inside this correction or count local fixture acquisition
as proof of actual Java execution or a complete production closure.

The fourteen deferred lint findings in six existing source functions stay
with part 2. This correction must add none. Clean new and changed owned
code; record before/after diagnostics by function. Do not suppress a new
finding or expand into an unrelated cleanup to obtain a zero global count.

## Approved initial inputs

Paths below are relative to `/home/ubuntu/wr`. Spans are inclusive at this
approval. Relocate by function or heading after edits. Read applicable
`AGENTS.md` files if introduced; none were found for this checkout during
this review. Read the assigned role skill and these shared skills from
`/home/ubuntu/.agents/skills/`:

- `agent-conduct`, `go-conventions`, `implementation-principles`, and
  `testing-principles`.
- `writing-for-agents`, `unslop`, `prose-principles`, and `final-response`.
- `go-implementor` for implementation, or `go-reviewer` and `code-smells`
  for review. Read each skill's `SKILL.md`.

| Input | Inclusive spans | Bytes |
| --- | --- | ---: |
| `.docs/nextflow-conformance/spec.md` | 23-263, 1034-1054 | 16,428 |
| `.docs/nextflow-conformance/phase1.md` | 1-148 | 7,939 |
| Local-origin handoff, named below | All | 5,659 |
| Part 2 bundle, named below | All | 11,810 |
| Part 1 review 03, named below | All | 5,520 |
| `nextflowconformance/model.go` | Model spans below | 22,587 |
| `nextflowconformance/schema.go` | 1-445 | 16,039 |
| `nextflowconformance/source.go` | Source spans below | 20,954 |
| `nextflowconformance/model_test.go` | Model test spans below | 14,685 |
| `nextflowconformance/source_test.go` | 1-891 | 27,574 |
| `nextflowconformance/cli.go` | CLI spans below | 10,313 |
| `testdata/records/sources.lock.json`, under package | All | 2,668 |
| `testdata/check_schemas.py`, under package | All | 1,350 |
| `cmd/wr-nextflow-conformance/main.go` | All | 1,683 |
| `go.mod` | 1-50 | 1,984 |
| `.golangci.yml` | 1-169 | 3,165 |

The three named documents are under `.docs/nextflow-conformance/`:

- `evidence/nextflow-phase1-part2-local-origin-handoff.md`.
- `reviews/phase-01-acquisition-part2-bundle.md`.
- `reviews/phase-01-acquisition-part1-review-03.md`.

Exact code spans:

- Model: `1-99,138-437,482-565,782-990,1244-1271`. These include constants,
  artifact and environment records, closure, string rules, and decoding.
- Source: `1-186,313-398,441-618,651-854,1259-1420`. These include
  transaction/publication, physical file checks, Java snapshot copy/read,
  pinned/locked callers, artifact saving, and bounded HTTPS fetching.
- Model tests: `1-199,249-277,297-385,482-562,619-650`.
- CLI: `61-371,799-827,865-876,1064-1101`.

Inspect `nextflowconformance/testdata/schema-cases.json` by script, selecting
only records whose `kind` equals `sources.lock`. There are 48 baseline
cases. Serializing their zero-based indices and records with Python's
default `json.dumps` measures 7,315 bytes. The full file is 267,067 bytes;
do not dump it. Run the whole corpus for verification. Inspect generated
schemas and probe manifests by script, printing selected properties,
counts, paths, and hashes rather than the complete documents.

Measured initial inputs total 170,358 bytes before skills and selected
schema cases. Shared plus role skills total 36,148 bytes for implementation
and 40,386 bytes for review. Including selected cases gives 213,821 and
218,059 bytes respectively, before this bundle and dispatch instructions.
At four bytes per token these are estimates of 53,456 and 54,515 tokens,
not measured model usage. Instructions, this bundle, and modest tokenization
variation must fit the initial allowance below.

## Context budgets and split boundary

Use the same bounds for each fresh implementor and reviewer:

| Use | Token cap |
| --- | ---: |
| Initial inputs, role instructions, and this bundle | 60,000 |
| Supplemental reads, full changed functions/tests, diff, and evidence | 7,000 |
| Command output and retained evidence summaries | 4,000 |
| Reasoning, edits, and verdict | 17,000 |
| Total working budget | 88,000 |

The roughly 100,000-token ceiling is not a target. Keep supplemental reads
under 2,000 tokens each. Remeasure after implementation, before dispatching
review. Include every new or changed production function, test, schema
condition, fixture case, and affected caller, plus the complete correction
diff and evidence. Existing span approval does not authorize unlimited
growth. Count new files even if untracked and absent from `git diff`.

If projected inputs or remaining reasoning cannot fit, stop at a recorded
handoff and reapprove two sequential fresh contexts: first model/schema
representation and relational checks, then locked snapshot reuse and CLI
regressions. Each needs independent review. The first alone cannot close
Item 1.1; its correction PASS requires both boundaries. An enlarged pinned
acquisition change belongs to part 2 and needs its own remeasured bundle.

## Acceptance and evidence

Write meaningful failing behavior tests before production changes. Require
these results before the independent reviewer returns PASS:

1. Decoder and stock schema accept both local roles with matching valid
   data and all existing HTTPS forms. Their shared mutation cases reject
   every wrong role, packaging, coordinate, and malformed origin. Cover
   local origins on lock and tree objects as rejection cases. Decoder and
   public CLI separately reject a valid-looking digest unequal to the
   file hash. Preserve all eleven schemas and the 1,190 baseline mutations.
2. Public locked acquisition succeeds with regular local Java/tool snapshot
   files connected to runtime edges. Read its emitted candidate and actual
   files: origin, path, digest, length, mode, and closure agree. Assert local
   objects make zero fetch calls while expected HTTPS objects still fetch.
   Stop the fixture server and prove offline validation of the candidate.
   Label inert Java fixtures accurately; they prove no Java execution.
3. Public failures cover missing files, independent content/length changes,
   directories or another non-regular input, symlink final components,
   symlink ancestors, and paths escaping the cache. Capture diagnostic
   codes, JSON, stderr, exit, and request counts. Check old candidate/cache
   snapshots and modes after failure. Reuse existing candidate-collision
   cases and add the retained-local-file case where needed.
4. Existing A1_01 through A1_04 and their security regressions remain green.
   Run focused package/CLI tests and the full stock schema checker, with
   complete logs on disk and bounded summaries. Keep missing fixture inputs
   explicit; obtain their provisioning paths from part 1 evidence if needed.
5. Run unchanged focused lint analyzers and report exact remaining counts
   and owners. New or changed owned code has no findings. The independent
   reviewer rehashes observed local files and verifies the provenance
   evidence instead of trusting reported hashes.

Use Go 1.27.1, `CGO_ENABLED=1`, `-tags netgo`, and `-count=1`. Existing lint
is `.tmp/agent/bin/golangci-lint` v2.12.2 under `GOTOOLCHAIN=go1.26.3`.
Use `timeout 10m` for focused package tests and lint, and bounded timeouts
for schema checks and CLI probes. Full unrelated wr tests are outside
scope. Record red/green commands and statuses, changed function/test spans,
schema counts, local provenance, regression results, and remaining part 2
work in a compact Nextflow-named evidence handoff.

This size review inspected the contracts and relevant call paths; it did
not rerun implementation tests or award acquisition evidence. It writes
only this bundle and makes no production, phase, spec, commit, or push
change.
