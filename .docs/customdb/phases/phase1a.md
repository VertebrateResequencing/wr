# Phase 1a: Log engine

Ref: [spec.md](../spec.md) sections B1, B2, B3

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
  are not items here; they are in [phase1-gate.md](phase1-gate.md).

## Items

Phase 1 starts after the Phase 0 gate ([phase0-gate.md](phase0-gate.md))
is reviewed.

### Item 1a.1: B1 - Framing and scanning

spec.md section: B1

Add framed, CRC-checked records and a scanner that stops at the first
damage in `jobqueue/wal/frame.go` and `jobqueue/wal/scan.go`, completing
the plain-frame part item 0d.2 added for A5's recordings, including
`FuzzScan`. B1 test 2 and the block seeds need s2 blocks, so this item
also adds `AppendBlock` and the s2 and zstd decode path in
`jobqueue/wal/codec.go`, making `github.com/klauspost/compress` a direct
dependency. Cover all 4 acceptance tests from B1.

- [ ] implemented
- [ ] reviewed

### Item 1a.2: B2 - Group-commit writer

spec.md section: B2

Add the single writer goroutine that makes each batch durable with one
write and one fdatasync in `jobqueue/wal/writer.go`. Cover all 5
acceptance tests from B2. Depends on 1a.1.

- [ ] implemented
- [ ] reviewed

### Item 1a.3: B3 - Block codecs

spec.md section: B3

Complete the s2 and zstd block helpers in `jobqueue/wal/codec.go`
(zstd at `SpeedFastest`, no dictionary). Cover both acceptance tests
from B3. Depends on 1a.1.

- [ ] implemented
- [ ] reviewed
