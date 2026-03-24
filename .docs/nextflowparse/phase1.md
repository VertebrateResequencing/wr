# Phase 1: Map/scope additions (Groups A, B, C, L)

Ref: [spec.md](spec.md) sections A1, B1, C1, L1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 1.1: A1 - Add task placeholders to defaultDirectiveTask()

spec.md section: A1

Add keys "hash", "index", "name", "previousException",
"previousTrace", "process", "workDir" to the map returned by
`defaultDirectiveTask()` in `translate.go`. Use "" for string
properties and 0 for index. Covering all 8 acceptance tests
from A1. Test file: `translate_test.go`.

- [ ] implemented
- [x] implemented
- [x] reviewed

#### Item 1.2: B1+C1 - Add scopes to skippedTopLevelConfigScopes

spec.md section: B1, C1

Add 15 scope names (aws, azure, charliecloud, fusion, google,
k8s, lineage, mail, nextflow, podman, sarus, seqera, shifter,
spack, workflow) to `skippedTopLevelConfigScopes` in
`config.go`. The "nextflow" entry also covers all 8 C1 features
(enable/preview flags). One map edit fixes all 23 config-scope
features. Covering all 16 acceptance tests from B1 and all 9
acceptance tests from C1. Test file: `config_test.go`.

- [ ] implemented
- [x] implemented
- [x] reviewed

#### Item 1.3: L1 - Add channel operators recurse and times

spec.md section: L1

Add "recurse" and "times" to the `supportedChannelOperators`
map in `parse.go`. They parse as standard channel operators
(pass-through during translation). Covering all 4 acceptance
tests from L1. Test file: `parse_e1_test.go`.

- [ ] implemented
- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
