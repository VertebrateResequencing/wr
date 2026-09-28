# Phase 1 review 2

Verdict: PASS

Reviewed `phase1.md` against `spec.md` Implementation Order step 1,
Architecture, A1, the A2 bootstrap selectors, and D2's attempt input fields.
No plan changes were needed.

## Findings

- A1 is the only assigned story. Item 1.1 covers all five acceptance IDs
  in `conformance/source_test.go`, using `TestUAT_<ID>` names. Shared schema
  checks belong in `conformance/model_test.go` without taking later stories'
  acceptance IDs or completion gates.
- All eleven closed record schemas, nested constraints, D2 attempt fields,
  and emission from the same definitions are required. Valid fixtures cover
  records whose real reviewed contents arrive in later phases.
- The plan puts failing acquisition, empty-corpus, and malformed-record
  checks before implementation. It requires transactional acquisition,
  immutable tree accounting, archive safety, bounded requests, and offline
  rehashing, including runtime and Java corruption checks through A1_05.
- Actual pinned source, launcher, distribution, dependency closure, and an
  existing Java 21 installation are required. Independent review checks
  measured hashes and the exact bootstrap selectors before lock acceptance.
  Missing prerequisites leave the item incomplete.
- CLI JSON and exit-contract evidence is required, including zero counts
  after acquisition failure. Offline preflight follows acquisition with the
  network fixtures stopped. Focused package checks cover both A1 tests and
  shared schema checks; they do not invoke the unrelated full wr test suite.

## Verification

Checked ASCII, 80-column prose, heading levels, fenced-block languages,
trailing whitespace, blank lines, continuous item numbering, both item
checkboxes, and all five A1 acceptance IDs. Checks passed. This is a plan
review; acquisition and implementation tests were not executed.

## SHA-256 records

Reviewed and unchanged `phase1.md`:

```text
09877f92597061f865cbb34dfe570df3722c715abaf5a89f694b5621bfeb25c6
```

Reviewed `spec.md`:

```text
80a0fb713aedc6492e4b56432c4c319a71075c0d5bd90a8096a4ca4ecb6cff8e
```
