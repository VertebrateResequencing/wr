# Phase 8: Lifecycle and Deprecation (I1, J1)

Ref: [spec.md](spec.md) sections I1, J1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 8.1: I1 - Parse workflow lifecycle handlers [parallel with 8.2]

spec.md section: I1

Extend parse.go to recognise `workflow.onComplete` and
`workflow.onError` blocks and skip them without error, so that
pipelines using lifecycle handlers parse successfully. No AST
storage needed -- these blocks are consumed and discarded.
Depends on Phase 4 for general parsing infrastructure. Covering
all 3 acceptance tests from I1.

- [x] implemented
- [x] reviewed

#### Item 8.2: J1 - Deprecated constructs with warnings [parallel with 8.1]

spec.md section: J1

Handle deprecated-but-DSL2-valid constructs in parse.go and
channel.go: `Channel.from()` treated as `Channel.of()` with
deprecation warning, `merge` and `toInteger` operators emit
deprecation warnings, `countFasta`/`countFastq`/`countJson`/
`countLines` operators emit deprecation warnings. DSL1-only
constructs produce errors: `Channel.create()` -> parse error,
`set { item }` as DSL1-style channel assignment -> parse error
(distinct from valid DSL2 `set` operator). Depends on Phase 4
for operator/factory parsing. Covering all 5 acceptance tests
from J1.

- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
