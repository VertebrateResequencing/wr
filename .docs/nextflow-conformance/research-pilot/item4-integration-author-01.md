# Phase 4 report provenance integration author handback 01

The working research manifest now adds exactly phase4_report from the staged
report author contribution. All 23 previous values and scalar types remain
canonically unchanged. Report, author handback, output hash map, actual author
completion, expectations and earlier pending-acceptance fields retain their
bytes. Independent report review and R-UAT-05 remain pending.

## Authorized transition and generations

Root authorized this short integration after the actual report author FINAL,
root's report/source review and a passive reconciliation PASS. Worktree is
/home/ubuntu/wr, branch nextflowdsl. This report and integration scratch were
absent before work. No runtime or prior evidence was rerun or changed.

The exact generation 23 working bytes were archived before extension in
before-working-manifest-23.json. Their SHA-256 is:

```text
724e4a56c4a3143f14e886de16acbce9a12d7ada1a3e3416340ea3717bf8bb2f
```

The current generation 24 working manifest SHA-256 is:

```text
d76cab9fb10e4ef9067294c2bc657d76200ec5b90c531c5e0334f7b33eee882b
```

The sole new key is phase4_report. Its value equals the staged
manifest-contribution.json phase4_report_author value without semantic changes.
The new integration plan binds staged contribution, author output map and actual
author completion externally. No self hash or additional acceptance field is
introduced. The generation 23 archive matches the original report baseline and
author completion's working-manifest binding.

pilot-report.md and item4.1-author-01.md state a current 23-field manifest at
the time of their author FINAL. Those are preserved historical author-baseline
statements. Current integration has 24 fields. No report, historical checker,
snapshot, completion or pending review field was rewritten to conceal that
transition. The original report's pending R-UAT-05 remains correct until the
independent reviewer accepts it.

## Composed passive verification

Run from /home/ubuntu/wr:

```bash
timeout 60s python3 .tmp/agent/nextflow-conformance/research-pilot/report04/integration/verify_integrated.py
```

The owned verifier checks the exact before/after working hashes, canonical
preservation of all 23 keys and exact equality of the sole added contribution.
It verifies every staged artifact, author output-map binding and original
completion binding against actual unchanged files.

It imports the preserved original report checker without invoking its main
entrypoint or writing bytecode. Three explicit owned adapters redirect only
reads, hashes and baseline binding of the working-manifest path to its exact
generation 23 archive. All source, fixture, resource, original/result, capture,
expectation, root transition and other protected paths remain actual current
reads. Composed reconciliation must equal the retained original index with exact
JSON scalar types. This is reuse of historical obligations against their
archived working generation, not a current PASS from the unchanged historical
whole-manifest gate. The separately checked generation 24 transition accounts
for the sole authorized current-path exception.

That composition preserves the 31,868-path author baseline except the explicit
current-manifest transition, seventeen frozen fields, 249 other sealed artifacts
and all 30,960 unchanged historical D01 bindings plus root's two recorded
marker/status transitions. It rechecks all 42 original predicate expectations,
83 fixtures, 123 source identities, 34 reviewed facet/subject relationships, 578
resource hashes and 378 deduplicated command metadata records. These are passive
provenance/relationship checks; no execution, whole-harness equivalence, raw
Spock oracle, translation, wr or full-language pass follows. P01 and ignored
local evidence/portable-path packaging limits remain unchanged.

Python syntax and the applicable report mechanics are checked separately. No
unavailable linter, static-type or pipeline tool is claimed as a pass. No
supported behavior changes, so no new behavioral test is appropriate.
[Integration evidence][evidence] retains plan, exact archived bytes, transition,
verifier, raw verification streams, command metadata, output bindings and actual
final completion.

## Effort, ownership and completion

The integration starts at the actual clock in integration-plan.json.
completion.json records the actual final clock and elapsed author interval. It
lies inside root's ongoing Phase 4 interval and is not added again to root's
active time. Stage deadline remains 17:43:58.834315 UTC, including independent
review.

Writes are confined to the single authorized working-manifest addition, this new
report and report04/integration scratch. No existing report, author
completion/output map, source expectation, fixture, result, frozen manifest,
historical gate, core, root marker/status, lock or bootstrap artifact was
edited. No engine, observer, control, build, acquisition, commit, push or child
agent was started. Every owned command finishes before actual FINAL handback; no
owned live process, tool session, wait, job or child remains. Root owns
independent report review, R-UAT-05 acceptance and delivery.

[evidence]:
  ../../../.tmp/agent/nextflow-conformance/research-pilot/report04/integration/
