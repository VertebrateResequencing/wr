# Phase 3: Config Selectors (A2, L1, G1, G3)

Ref: [spec.md](spec.md) sections A2, L1, G1, G3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 3.1: A2 - Parse withLabel/withName selectors [parallel with 3.2, 3.3]

spec.md section: A2

Add `ProcessSelector` struct (Kind, Pattern, Settings) and
`Selectors []*ProcessSelector` field on `Config` in config.go.
Extend config parsing to handle `withLabel:` and `withName:`
selector blocks inside `process {}`, including glob and regex
patterns (prefixed with `~`). Depends on Phase 2 for label
directive parsing. Covering all 6 acceptance tests from A2.

- [ ] implemented
- [x] implemented
- [x] reviewed

#### Item 3.2: G1 - Parse env scope in config [parallel with 3.1, 3.3]

spec.md section: G1

Add `Env map[string]string` field to `Config` in config.go.
Extend config parsing to handle `env { KEY = 'value' }` scope.
Independent of selector parsing. Covering all 3 acceptance tests
from G1.

- [ ] implemented
- [x] implemented
- [x] reviewed

#### Item 3.3: G3 - Parse container scope settings [parallel with 3.1, 3.2]

spec.md section: G3

Add `ContainerEngine string` field to `Config` in config.go.
Extend config parsing to handle `docker { enabled = true }`,
`singularity { enabled = true }`, and `apptainer { enabled =
true }` scopes. Last-defined-wins among multiple container scopes.
Independent of selector and env parsing. Covering all 5 acceptance
tests from G3.

- [ ] implemented
- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).

### Batch 2 (parallel, after batch 1 is reviewed)

#### Item 3.4: L1 - Parse nested selectors

spec.md section: L1

Add `Inner *ProcessSelector` field to `ProcessSelector` in
config.go. Extend config selector parsing to handle one level of
nesting (e.g. `withLabel: 'big' { withName: 'ALIGN' { ... } }`).
Composite selectors require both conditions to match. Depends on
A2 (item 3.1) for base selector parsing. Covering all 3
acceptance tests from L1 (test 2 and 3 involve translate-time
matching, verified in Phase 7 A3).

- [ ] implemented
- [x] implemented
- [x] reviewed
