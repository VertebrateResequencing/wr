# Local-origin model correction input bundle

APPROVED on 2026-09-28 for fresh implementation and independent review.
This supersedes the oversized model bundle; do not reread it. Item 1.1
requires independent model and reuse PASS verdicts. No pass is awarded here.

## Contract and ownership

Change only artifact origin validation, schema generation, the generated
lock schema, and corresponding model/schema tests and fixtures. Keep the
required string field. Permit `local:sha256:<64 lowercase hex digits>` only
for Java/environment-tool roles, file packaging, and null coordinate.
Require the digest to equal `file.sha256`. This names acquired regular-file
snapshot bytes; it supplies no path or upstream download claim.

Preserve strict HTTPS for network origins, including lock/tree origins.
Reject local origins on every other role, malformed strings, whitespace,
suffixes, uppercase digests, and file/HTTP URIs. Keep closed shapes and
existing packaging, coordinate, pinned identity, and closure checks.
Stock schema enforces syntax/role/packaging/nullability. Decoder enforces
cross-field digest equality; document that existing relational-check
distinction through `x-wr-record-rules`, without claiming schema enforcement.
Acquisition/CLI failures belong to reuse; actual Java/tools and A1_05-07
stay in part 2.

## Initial reads and budget

Paths are under `/home/ubuntu/wr`. Read full
`SKILL.md` files under `/home/ubuntu/.agents/skills/` for agent-conduct,
go-conventions, implementation-principles, testing-principles,
writing-for-agents, unslop, prose-principles, and final-response. Add
go-implementor, or go-reviewer plus code-smells. These total 36,148 bytes
for implementation and 40,386 for review. Check applicable AGENTS; none
were present at approval.

Read these inclusive spans; relocate by function after edits:

| File | Lines | Bytes |
| --- | --- | ---: |
| `.docs/nextflow-conformance/spec.md` | 110-122,197-207 | 1,671 |
| `nextflowconformance/model.go` | See below | 3,362 |
| `nextflowconformance/schema.go` | See below | 4,528 |
| `nextflowconformance/model_test.go` | 140-158,297-327 | 1,551 |
| Package `testdata/records/sources.lock.json` | 1-107 | 2,668 |
| Package `testdata/check_schemas.py` | 1-31 | 1,350 |

Model spans: `55-64,228-250,540-560,782-801,1256-1271`, covering role/packaging
constants, artifact/checkArtefact, validTextRule, HTTPS, and rule tables.
Schema spans: `147-176,224-249,323-346,360-374,420-423,438-445`, covering
applySchemaRule, recordConstraints, artefactConditions, scalarSchemaRules,
when/property, and schemaFullPattern. Reuse existing test helpers.

Source spans total 15,130 bytes; with skills, 51,278 implementor / 55,516
reviewer. Add this report. Four bytes/token is an estimate. Each role has
a 55,000-token working cap: 20,000 initial reads including this report;
10,000 supplemental reads/growth/diff; 5,000 command output; 15,000
reasoning/edits; 5,000 harness/brief instructions. Keep 30,000 after initial
reads and instructions. Reapprove before projected consumption exceeds a
category. The earlier 32,768-context claim was unsupported and is withdrawn.
Observed required reads were about 14,470 tokens plus 1,607 supplemental;
expected changed functions/tests/cases need 5,000-7,000 more. This allocation
covers those inputs below the roughly 100,000-token ceiling.

## Proof and review inputs

Write red then green GoConvey tests for both accepted local roles, all
forbidden roles/forms, lock/tree rejection, and independent digest mismatch.
Keep mismatch tests outside common stock-schema cases. Select existing
origin cases dynamically from schema-cases.json; inspect names/paths and
needed values only, never its full 267 KB. New cases must exercise decoding
and stock validation. Preserve all eleven schemas and 1,190 baseline cases.

Run bounded focused package/CLI tests and stock schema checks; run unchanged
focused lint. Use Go 1.27.1, CGO_ENABLED=1, netgo, count=1. Lint uses existing
`.tmp/agent/bin/golangci-lint` with GOTOOLCHAIN=go1.26.3. Use timeout 10m for
Go/lint and timeout 2m for schemas. No new owned lint findings are allowed;
the fourteen accepted source-function findings remain deferred to part 2.

Before review, remeasure all changed/new functions, tests, fixture cases,
schema conditions and red/green evidence, including untracked files. Read
full changed functions, replacing overlapping baseline spans. Inspect the
schema/diff mechanically. Growth and extra reads
share 10,000 tokens. If insufficient, report a measured blocker before
dispatch. Prior reviews cover unchanged closure/CLI/security behavior;
rerun focused regressions. Reuse must prove public digest failure and
snapshot safety. Return compact evidence and verdict.
