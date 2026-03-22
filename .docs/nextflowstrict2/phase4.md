# Phase 4: Channel Operators and Factories (B1, C1)

Ref: [spec.md](spec.md) sections B1, C1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 4.1: B1 - Parse additional channel operators [parallel with 4.2]

spec.md section: B1

Add `cross`, `splitJson`, `splitText`, `buffer`, `collate`,
`until`, `subscribe`, `sum`, `min`, `max`, `randomSample`,
`merge`, `toInteger`, `countFasta`, `countFastq`, `countJson`,
and `countLines` to `supportedChannelOperators` in parse.go.
The `countFasta`, `countFastq`, `countJson`, and `countLines`
operators emit deprecation warnings. Depends on Phase 1 for
expression parsing of operator arguments. Covering all 17
acceptance tests from B1.

- [ ] implemented
- [ ] reviewed

#### Item 4.2: C1 - Parse additional channel factories [parallel with 4.1]

spec.md section: C1

Add `fromList`, `from`, `fromSRA`, `topic`, `watchPath`,
`fromLineage`, and `interval` factory recognition in parse.go.
This item covers the parsing portion of C1 (acceptance tests
1, 3, 5, 7, 8, 9, 10). Resolution of these factories is
completed in Phase 5. Covering 7 parsing acceptance tests
from C1.

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
