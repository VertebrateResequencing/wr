# Phase 2e: Retire bbolt and documentation

Ref: [spec.md](../spec.md) sections F5, F6, A1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.
Implementors and reviewers follow `testing-principles` for test work, and
reviewers apply `go-test-strength` to the tests each item adds.

Brief every subagent with these project constraints:

- Run one subagent at a time. Items run sequentially; there are no
  parallel batches, because of the user's rate limits.
- Work only on branch `customdb-f397b547` in the main clone. Do not
  switch branches or create worktrees for this work; `go-test-strength`
  mutants go in a scratch copy as `agent-conduct` describes.
- Commit messages carry no attribution lines: no `Co-Authored-By` and no
  `Generated with` trailers.
- Kill only exact PIDs that the agent started and verified. Never use
  `pkill -f` or any pattern kill, and guard empty variables in cleanup.
- Run `make test` and `make race` with every `OS_*` variable unset and an
  empty scratch `HOME` (DEVELOPERS.md, "Fast test suite"), for example:

  ```bash
  (unset $(compgen -v | grep '^OS_'); HOME=$(mktemp -d -p <scratchpad>) make test)
  ```

- Put NFS work and measurements under `/nfs/hgi/wr/sb10-bigdb/customdb/`
  (`.docs/customdb/README.md`, "Where to run on NFS").
- This sub-phase is complete when every item is reviewed and `make test`,
  `make race` and `make lint` pass.
- Gates that need the LSF farm, daytime NFS runs or multi-hour soaks
  are not items here; they are in [phase2-gate.md](phase2-gate.md).

## Items

### Item 2e.1: F5 - Retire bbolt

spec.md section: F5

Delete `db.go` and the files under "Packages and files",
`developers/soak/bboltexp/`, `.docs/reliable2/harness/statinspect/`,
the bbolt-only tests, the `wrdev.sh` modes `freelist-check`,
`remap-stall-check` and `compact-fixture`, and `go.etcd.io/bbolt` from
`go.mod` unless only the converter's read-only reader needs it. Replace
A1 test 1's file list as F5 test 1 says. Cover both acceptance tests from
F5.

- [ ] implemented
- [ ] reviewed

### Item 2e.2: F6 - Documentation

spec.md section: F6

Update `CHANGELOG.md` (Unreleased, each section ordered by impact),
`cmd/conf.go` and `DEVELOPERS.md` as F6 lists, covering what changed on
disk and how to roll back. Cover all 3 acceptance tests from F6. Depends
on 2e.1.

- [ ] implemented
- [ ] reviewed
