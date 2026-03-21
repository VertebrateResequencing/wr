# Phase 1: Lexer hardening

Ref: [spec.md](spec.md) sections A6

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 1.1: A6 - Shebang, block comments, feature flags

spec.md section: A6

Harden the lexer in `nextflowdsl/parse.go` to skip `#!/usr/bin/env
nextflow` shebang lines, `/* ... */` block comments (including
multi-line and unterminated detection), and `nextflow.enable.*` /
`nextflow.preview.*` feature flag assignments. This is foundational
for all subsequent parsing phases. Covering all 6 acceptance tests
from A6.

- [x] implemented
- [x] reviewed
