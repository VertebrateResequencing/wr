# Phase 5: Tools and docs

Ref: [spec.md](spec.md) sections E1, E2, E3, E5, E4

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

E1, E2, E3 and E5 touch separate files and depend only on phases 1 to 4,
so they form one parallel batch. E4 comes after, since its test 1 uses
E1's `dbstart -schema`.

## Items

### Batch 1 (parallel)

#### Item 5.1: E1 - dbstart overlays run-state records [parallel with 5.2, 5.3, 5.4]

spec.md section: E1

In `developers/soak/dbstart/main.go`, overlay a `jobRunState` record whose
first 4 bytes are the big-endian CRC-32C of the live value onto the same
`rec`, keeping the output columns; change `run` to
`run(path string, out io.Writer) error`; add `dbstart -schema <db>`.
Covering all 5 acceptance tests from E1, in
`developers/soak/dbstart/main_test.go`. Depends on phase 2's record format
and, for test 4, phase 3's reserve write.

Review note for test 5: the test needs both the printed text and the exit
code. Rather than re-executing the binary, put the `-schema` handling in a
function that takes the path and output and error writers and returns the
exit code, which `main` passes to `os.Exit`, and test that function
directly. A subprocess test is acceptable only if the function approach
cannot cover the malformed-stamp exit.

- [ ] implemented
- [ ] reviewed

#### Item 5.2: E2 - statinspect clearlive empties run-state records [parallel with 5.1, 5.3, 5.4]

spec.md section: E2

In `.docs/reliable2/harness/statinspect/main.go`, make `clearLive` also
delete every `jobRunState` key in the same transaction, if the bucket
exists, and print `jobRunState keys: before=%d after=%d`; count `after` so
it sees the deletes (fixing the `jobslive` line too). Change it to
`clearLive(path string, out io.Writer) error`. Add GoConvey to that
module's `go.mod` by hand as F1.5 describes (offline, no `go get`).
Covering both acceptance tests from E2, in
`.docs/reliable2/harness/statinspect/main_test.go`.

- [ ] implemented
- [ ] reviewed

#### Item 5.3: E3 - CHANGELOG and compact help [parallel with 5.1, 5.2, 5.4]

spec.md section: E3

Add the two `### Changed` entries and the `### Fixed` entry under
`## [Unreleased]` in `CHANGELOG.md`, first in each section, as given in
E3, and replace the last paragraph of `managerCompactCmd.Long` in
`cmd/manager.go`. Covering E3's 1 acceptance test, in
`cmd/manager_test.go`, plus E3's review check of the CHANGELOG positions.
Depends on phases 1 and 3, whose behaviour the entries describe.

- [ ] implemented
- [ ] reviewed

#### Item 5.4: E5 - Soak gate classification [parallel with 5.1, 5.2, 5.3]

spec.md section: E5

Write `developers/soak/soakgate.py` with the `--source d1|warning` CLI,
rotated-log refusal, segmenting, window, totals, doubles, missing-job and
peak-RUN rules and the exact output lines E5 gives, and add its analysis
step to `developers/soak/README.md`. Add one case directory per acceptance test
under `developers/soak/testdata/soakgate/` (input outdir, runner logs,
`doubles.tsv`, `dbstart.tsv`, `args`, `expected.txt`) and `run.sh`, which
runs every case and diffs (F1.6). Covering all 11 acceptance tests from
E5. Depends on phase 4's D1 line.

Review notes:

- Timestamps: `manager.log` and runner log lines carry `t=` in ISO 8601
  with a zone offset (`%Y-%m-%dT%H:%M:%S%z`, as `anyway.py` parses it),
  while `stall.log` holds epoch seconds. soakgate converts every `t=` value
  to whole epoch seconds before comparing it with a window. The test
  fixtures write ISO `t=` lines whose epoch values are exactly the ones
  E5's tests quote (1000, 1005, 1180, ...), and at least one case uses a
  non-zero zone offset so the conversion is exercised.
- Unmapped doubles: an unmapped double counts as outside a window, which
  is conservative and stays as specified. The README step says that each
  `unmapped` double must be investigated by hand (find its runner log and
  reservation) before it is reported as a failure of the change.

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item under the
`subagents` skill's shared concurrency limits.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).

### Item 5.5: E4 - Fixture compaction

spec.md section: E4

Add `wrdev.sh compact-fixture <src> <dst>` with its refusals, list it in
`usage` and `main`, and repoint at compacted copies every text the E4
grep finds in `developers/wrdev.sh` and `developers/speed.sh`, plus the
skip messages and header comments of the three `reliable4_*` test files
E4 names. Then build the gate's fixtures in
`$G/fixtures` (`pristine6`, `pristine10`, `prod.db` with `compact-fixture`,
and `fix120k.db` with `add-storm-fixture`), following E4's rules for
`WRDEV_ROOT`, explicit free ports, `wrdev.sh build` first, unset `OS_*`
and the `/tmp/claude-11346/gocache-runstate-gate` Go cache. There is no
test file: run all 3 acceptance tests from E4 end to end and record their
output. Depends on item 5.1's `dbstart -schema` and phase 1's compact.

Review note on test 3 versus F2.2: both run
`WRDEV_PRISTINE_DB=$G/fixtures/fix120k.db wrdev.sh add-storm-lsf`. Run it
to completion here rather than stopping it once `fixture manifest OK` is
printed, record that line, and leave the full gate verdict to F2.2, which
runs it again.

- [ ] implemented
- [ ] reviewed
