# Nextflow part 2 local-origin representation handoff

Status: BLOCKED within the approved part 2 bundle on 2026-09-28. No
production code, schema, test, phase, or progress file changed. The parent
requested a separate bounded schema correction before acquisition resumes.
This is an implementation/schema mismatch, not a demonstrated contradiction
in the specification.

## Required correction

The specification requires an existing Java home and actual execution files
at `spec.md:110-122`. It requires immutable artifact origins and runtime
dependency edges at `spec.md:197-207`. It does not explicitly require HTTPS
origins for local snapshots. The HTTPS acquisition rule at `spec.md:135`
governs network access.

`nextflowconformance/model.go:231` applies `rule:"https"` to every artifact
origin, including Java and environment tools. `schema.go:366` emits the same
restriction. The CLI receives only an existing Java home; it has no origin
metadata for that home or host tools. An arbitrary local installation has
no necessarily known, immutable HTTPS download URL for each installed file.

The Java inventory cannot substitute for the required runtime edges.
`model.go:305-339` resolves those edges through artifact IDs. The environment
inventory has no artifact ID. `source.go:1283-1297` also downloads every
locked artifact from its origin, so a local snapshot needs explicit reuse
semantics as well as a truthful record representation.

Do not fabricate a download URL, assign an unrelated package URL to copied
bytes, or call the Java inventory alone a complete execution closure.

## Rejection evidence

The [manifest](nextflow-phase1-part2-local-origin-manifest.json) binds the
unchanged implementation inputs, existing review 03 CLI binary, exact probe
arguments, lock mutations, stdout, stderr, exits, and schema diagnostics.
The disposable corpus adapts the independent record fixtures to the CLI's
array-file layout. Each mutation changes artifact `JAVA` to the indicated
role and supplies a literal local-file origin.

| Local artifact role | CLI exit | CLI diagnostic | Stock schema |
| --- | ---: | --- | --- |
| `Java` | 2 | `E_INPUT`, `.artifacts.origin`, constraint `https` | Rejects origin |
| `environment-tool` | 2 | `E_INPUT`, `.artifacts.origin`, constraint `https` | Rejects origin |

These are diagnosis probes, not acceptance tests for an agreed new origin
format. Both use `timeout 10s` around the existing public CLI. The complete
inputs and outputs remain under
`.tmp/agent/nextflow-conformance/acquisition/nextflow-part2-local-origin/`.
Stock Draft 2020-12 validation independently reports exactly one origin
error for each lock. Neither probe executes Java or Nextflow.

## Fresh bounded work

Approve a correction bundle covering the artifact origin definition,
decoder and schema rule, runtime closure checks, acquisition/reuse callers,
and corresponding schema corpus and public CLI regressions. Preserve
HTTPS-only network acquisition and closed object shapes. Two alternatives
need a concrete reviewed contract before implementation:

1. Add a discriminated local snapshot origin, bound to the acquired bytes
   and their actual local provenance. Reuse and rehash that snapshot during
   locked acquisition without treating its origin as a network endpoint.
2. Add an explicit provenance manifest input for installed Java and tools,
   with verified immutable HTTPS identities where available. This changes
   the CLI contract and still needs rules for installed archive members.

The first alternative fits the current existing-home acquisition contract.
Its exact representation must be reviewed; these probes do not endorse a
bare mutable `file:` URI as immutable provenance.

A minimal proposal retains the string field and permits
`local:sha256:<64-lowercase-hex>` only for Java and environment-tool
artifacts. Require its digest to equal `file.sha256`; it names the immutable
local snapshot, not a claimed upstream download. Acquisition evidence must
retain the actual source path and resolved target separately. Existing
HTTPS origins keep their current meaning and all network restrictions.
The model, emitted schema, and local reuse path must agree on this branch.

Candidate owned implementation spans for the new bundle are
`model.go:228-250,327-437,540-565,782-800`,
`schema.go:128-151,177-211,323-346,349-373`, and
`source.go:557-618,688-846,1259-1314`. Include the existing model/schema
mutation tests and public acquisition fixture cases that exercise those
callers. This is a correction boundary proposal, not size approval.

After that correction passes independent review, resume the approved part 2
bundle. Complete opaque runtime retention, executable permissions, actual
Java/tools closure, three POMs, A1_05 through A1_07, remaining lint, actual
acquisition, selector/include evidence, and offline candidate validation.
Part 1 security regressions and the remaining closed-schema constraints
stay binding.

## Acceptance status

No new red/green implementation cycle or focused test/lint run occurred.
A1_01 through A1_04 retain their prior accepted status; this context awards
no new pass. A1_05 through A1_07, actual candidate, external execution
closure, all selector results, and final independent acceptance remain
incomplete. The two stock schema rejection probes are not the eleven-schema
and 1,190-mutation quality gate. No runtime bytes or Java tree were used for
execution or candidate acquisition, and no rehash acceptance is claimed.

Changed deliverables are this handoff, its manifest, and disposable probe
files. There are no changed production functions or test spans to review.
