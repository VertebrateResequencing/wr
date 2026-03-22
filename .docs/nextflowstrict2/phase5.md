# Phase 5: Operator/Factory Translation (B2, C1 resolution)

Ref: [spec.md](spec.md) sections B2, C1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 5.1: B2 - Operator cardinality translation [parallel with 5.2]

spec.md section: B2

Extend channel resolution in channel.go to handle cardinality
for new operators: `cross(ch2)` produces N*M items (Cartesian
product), `buffer(size: n)` / `collate(n)` produce ceil(N/n)
groups, `min()` / `max()` / `sum()` produce 1 item (reduction),
and `splitJson`, `splitText`, `until`, `subscribe`,
`randomSample`, `merge`, `toInteger` pass through with warning.
Depends on B1 from Phase 4 for operator parsing. Covering all 8
acceptance tests from B2.

- [ ] implemented
- [ ] reviewed

#### Item 5.2: C1 - Resolve additional channel factories [parallel with 5.1]

spec.md section: C1

Extend factory resolution in channel.go: `fromList(list)`
resolves like `Channel.of` (expand list items), `from(items)`
resolves like `Channel.of` (deprecated alias, emit deprecation
warning), `fromSRA` / `topic` / `watchPath` / `fromLineage` /
`interval` warn as untranslatable and return empty channel.
Depends on C1 parsing from Phase 4. Covering the 3 remaining
resolution acceptance tests from C1 (tests 2, 4, 6).

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
