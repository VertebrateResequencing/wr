# Phase 7: Translation Integration (A3, F2, F3, G2, E2, K1)

Ref: [spec.md](spec.md) sections A3, F2, F3, G2, E2, K1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 7.1: A3 - Apply selectors [parallel with 7.2, 7.3, 7.4]

spec.md section: A3

Implement `MatchSelectors` function in translate.go that merges
`ProcessDefaults` by applying matching selectors in specificity
order: generic < withLabel < withName < process-level. Glob
matching uses `filepath.Match`, regex patterns (prefixed `~`)
use `regexp.MatchString`. Last-match-wins among same specificity.
Also handles nested selectors from L1 (composite AND condition).
Integrate into `buildRequirements`. Depends on Phase 2 (A1) and
Phase 3 (A2, L1). Covering all 8 acceptance tests from A3 plus
L1 tests 2 and 3 (translate-time matching of nested selectors).

- [ ] implemented
- [ ] reviewed

#### Item 7.2: G2 - Merge config env into jobs [parallel with 7.1, 7.3, 7.4]

spec.md section: G2

Extend translation in translate.go to merge `Config.Env` into
every job's `EnvOverride`. Process-level `env` overrides
config-level `env` for the same key (more specific wins).
Depends on Phase 3 (G1) for env scope parsing. Covering all 3
acceptance tests from G2.

- [ ] implemented
- [ ] reviewed

#### Item 7.3: E2 - Translate conditional blocks [parallel with 7.1, 7.2, 7.4]

spec.md section: E2

Extend workflow translation in translate.go to evaluate if/else
conditions against resolved params. When statically evaluable,
only the matching branch emits jobs. When evaluation fails, both
branches emit with a warning using separate CWD subtrees
(`if_0`/`else_0`). Depends on Phase 6 (E1) for if/else parsing
and Phase 1 for expression evaluation. Covering all 4 acceptance
tests from E2.

- [ ] implemented
- [ ] reviewed

#### Item 7.4: K1 - Config container auto-detect [parallel with 7.1, 7.2, 7.3]

spec.md section: K1

Extend `cmd/nextflow.go` to read `Config.ContainerEngine` and
use it as default when `--container-runtime` flag is not
explicitly set. CLI flag always wins. Falls back to existing
default ("singularity") when neither config nor CLI provides a
value. Depends on Phase 3 (G3) for container scope parsing.
Covering all 3 acceptance tests from K1.

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).

### Item 7.5: F2 - Translate beforeScript/afterScript

spec.md section: F2

Extend `buildCommand` in translate.go to wrap the process script
with `beforeScript` and `afterScript`. Final command structure:
`<beforeScript>\n<script>\n<afterScript>`. Container wrapping
applies to the entire block as one unit. Depends on Phase 2
(F1) for directive parsing. Covering all 4 acceptance tests
from F2.

- [ ] implemented
- [ ] reviewed

### Item 7.6: F3 - Translate module directive

spec.md section: F3

Extend `buildCommand` in translate.go to prepend
`module load <name>` for the `module` directive. Colon-separated
modules (e.g. `samtools/1.17:bwa/0.7.17`) produce multiple
`module load` lines. Must integrate with F2's before/after
wrapping (module load comes before beforeScript). Depends on
Phase 2 (F1) for directive parsing and item 7.5 (F2) for
command structure. Covering all 3 acceptance tests from F3.

- [ ] implemented
- [ ] reviewed
