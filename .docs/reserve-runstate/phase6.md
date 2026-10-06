# Phase 6: Gates

Ref: [spec.md](spec.md) sections F1, F2, F3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

The items run in sequence: F1, then F2, then F3. F3 is the last step
before the PR is ready. Every run that needs a fixture uses E4's compacted
fixtures in `$G/fixtures` (`G=/nfs/hgi/wr/sb10-bigdb/runstate-gate`), never
the version-0 originals. Every run has all `OS_*` unset and `GOCACHE` off
the home directory.

## Items

### Item 6.1: F1 - Local gates

spec.md section: F1

Run all 6 F1 gates: `make lint`, `make test`, `CGO_ENABLED=1 make race`,
the tagged `go vet` and `TestReliable4InflateDBOpens` run; `make speed` and
`make speed-full` with the given `SPEED_DIR`, fixtures and ports and the
installed benchstat copied in; in statinspect, the hand-added goconvey
`require`, then the offline `go mod tidy` and `go test ./...`, committing
`go.mod` and `go.sum`; and `developers/soak/testdata/soakgate/run.sh`.
Record in the PR body the speed verdicts and A2 test 5's two
`bolt_pages/job` figures. Fix any failure in the story it belongs to.

- [ ] implemented
- [ ] reviewed

### Item 6.2: F2 - wrdev crash, recovery and big-DB modes

spec.md section: F2

With `WRDEV_ROOT=$G/f2/root` and ports 51980 to 51983, after this tree's
`wrdev.sh build`, run in sequence `crash-recovery`, `add-storm-lsf` on
`fix120k.db`, `dep-granularity-check`, and the five F2.4 big-DB modes, and
check each against the output F2 requires. This covers all 4 F2 gates.
Depends on item 6.1.

Review note: F2 does not run `soak/sweep.sh`, although E4 names the
fixtures to match it. After F2.4, also run
`developers/soak/sweep.sh $G/sweep` in full with `SWEEP_DB_DIR=$G/fixtures`,
`FIX120K=$G/fixtures/fix120k.db`, free `SWEEP_PORTS` checked with
`ss -ltn`, and `GOCACHE` off the home directory, and record its
`results.tsv` in the PR body. Any mode that fails or reports
`SKIPPED-NOFIXTURE` is investigated before F3.

- [ ] implemented
- [ ] reviewed

### Item 6.3: F3 - Production-scale LSF crash soaks

spec.md section: F3

Run the baseline soak (develop at the PR's merge-base, exported to
`$G/src-base`) and then this tree's soak (exported to `$G/src-change`),
never concurrently, each with battery10's `soak-go.sh` changed only in the
values F3's table gives, its own binary for the whole run, and a fresh
`cp -p` of `$G/fixtures/fix120k.db` just before it. Analyse both with the
change tree's tools and `soakgate.py` (`--source warning` for the
baseline, `--source d1` for the change), and check all 5 pass criteria.
Record both run directories, peak RUN, both `soakgate.py` outputs and the
verdict in the PR body. Depends on item 6.2.

Review note: an `unmapped` double counts as outside a window and so fails
criterion 3 as specified. Before reporting such a failure, investigate
each unmapped double by hand (find its runner log and reservation time)
and report what it was alongside the soakgate output; the criterion itself
is not relaxed.

- [ ] implemented
- [ ] reviewed
