# Phase 1 input bundle review

Verdict: APPROVED. Scope is item 1.1 only. No split is required for the
proposed input bundle.

Approved inputs, relative to `/home/ubuntu/wr/`:

- `.docs/nextflow-conformance/phase1.md`.
- `.docs/nextflow-conformance/spec.md`, lines 23-412 for Architecture,
  records, CLI contract, A1, and A2 selectors; lines 649-708 for D2 attempt
  fields; lines 983-1032 for Implementation Order.
- `go.mod`, `.golangci.yml`, and `cmd/wr-testsuite/main.go`.
- `/home/ubuntu/.agents/skills/agent-conduct/SKILL.md`.
- `/home/ubuntu/.agents/skills/go-implementor/SKILL.md`.
- `/home/ubuntu/.agents/skills/go-conventions/SKILL.md`.
- `/home/ubuntu/.agents/skills/implementation-principles/SKILL.md`.
- `/home/ubuntu/.agents/skills/testing-principles/SKILL.md`.

These inputs total 62,830 characters. A conservative three characters per
token estimate is about 21,000 tokens. Allow 25,000 for inputs and brief,
20,000 for implementation and tests, 15,000 for bounded command output, and
25,000 for reasoning. The estimated 85,000 total leaves 15,000 tokens below
the roughly 100,000-token limit. This is a planning estimate, not measured
model token usage. Read acquired files only at the exact selectors and
dependency declarations needed for acquisition review; keep full trees and
download logs out of context.

Implementation covers `conformance/model.go`, `conformance/source.go`,
their tests, matching `conformance/data/schema/` documents, and
`cmd/wr-conformance/main.go`. Review all five A1 UATs and closed-schema
checks, including D2 input fields. Later semantic and execution gates remain
in their assigned phases. If actual work exceeds the allowance, split at
closed-schema/CLI validation and A1 acquisition acceptance boundaries, with
independent review of each handoff.

This approves input size only. The caller reports Java and Nextflow absent
from PATH, Docker available, and unprivileged `unshare` blocked. Phase 1
still requires an existing verified Java 21 tree and actual pinned artifact
acquisition before completion. No acquired bytes, candidate lock, offline
environment, or oracle execution is approved by this review.
