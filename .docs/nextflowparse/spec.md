# Fix All 43 Parse Errors Specification

## Overview

The `nextflowdsl/` package contains a pure-Go Nextflow DSL2 parser that
translates `.nf` workflow files and config files into `jobqueue.Job`
slices. 43 Nextflow features currently cause parse errors, preventing
processing of any pipeline that uses them.

This spec covers fixes for all 43 features, grouped by fix strategy
into 12 groups (A-L). Each fix either adds missing data, extends a
switch/map, or adds token-skipping logic. Constructs that cannot be
meaningfully translated produce warnings, never parse failures.

**Design principles:**
- Pure Go, no CGo dependencies beyond existing go.mod.
- No intermediate files written to disk.
- All changes confined to `nextflowdsl/` package.
- Tests use GoConvey style.
- Untranslatable constructs parse without error and emit stderr
  warnings via the existing `fmt.Fprintf(os.Stderr, "nextflowdsl:
  ...")` pattern.

## Architecture

**Package:** `nextflowdsl/`

**Files modified:**
- `translate.go` -- `defaultDirectiveTask()` map (Group A)
- `config.go` -- `skippedTopLevelConfigScopes` map, `parseExecutorBlock`,
  `configParser.parse` (Groups B, C, D, E)
- `parse.go` -- `supportedChannelOperators` map,
  `applyDeclarationQualifier`, `applyTupleElementQualifier`,
  `parseDeclarationLine`, `parseImport`, `parseWorkflowBlock`,
  `desugarWorkflowPipe`, `parseWorkflowStatement` (Groups F-L)
- `ast.go` -- `Declaration`, `TupleElement` struct fields (Group F)
- `groovy.go` -- `parseAssignmentStmt` (Group H)

**Test files:**
- `parse_e1_test.go` -- Groups F, G, H, I, J, K, L parse tests
- `config_test.go` -- Groups B, C, D, E config tests
- `translate_test.go` -- Group A translate tests

**Warning pattern (existing):**
```go
_, _ = fmt.Fprintf(os.Stderr,
    "nextflowdsl: <description> at line %d\n", tok.line)
```

---

## A: Add task placeholders to `defaultDirectiveTask()` (7 features)

### A1: BV-task-hash, BV-task-index, BV-task-name,
BV-task-previousException, BV-task-previousTrace, BV-task-process,
BV-task-workDir

As a pipeline author, I want `task.hash`, `task.index`, `task.name`,
`task.previousException`, `task.previousTrace`, `task.process`, and
`task.workDir` to resolve without error in dynamic directives, so that
pipelines referencing these properties parse successfully.

Add keys `"hash"`, `"index"`, `"name"`, `"previousException"`,
`"previousTrace"`, `"process"`, `"workDir"` to the map returned by
`defaultDirectiveTask()` in `translate.go`. Use `""` (empty string)
for string properties and `0` for `index`.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given a process with directive `tag "${task.hash}"`, when
   translated with `defaultDirectiveTask()`, then `resolveExprPath`
   returns `""` (empty string) without error.
2. Given a process with directive `tag "${task.index}"`, when
   translated, then `resolveExprPath` returns `0` without error.
3. Given a process with directive `tag "${task.name}"`, when
   translated, then `resolveExprPath` returns `""` without error.
4. Given a process with directive referencing `task.previousException`,
   when translated, then `resolveExprPath` returns `""` without error.
5. Given a process with directive referencing `task.previousTrace`,
   when translated, then `resolveExprPath` returns `""` without error.
6. Given a process with directive `tag "${task.process}"`, when
   translated, then `resolveExprPath` returns `""` without error.
7. Given a process with directive `publishDir "${task.workDir}"`, when
   translated, then `resolveExprPath` returns `""` without error.
8. Given the existing keys (`attempt`, `cpus`, `memory`, `exitStatus`),
   when `defaultDirectiveTask()` is called, then all four existing
   keys still have their original values (1, 1, 0, 0).

---

## B: Add scopes to `skippedTopLevelConfigScopes` (15 features)

### B1: CFG-aws, CFG-azure, CFG-charliecloud, CFG-fusion, CFG-google,
CFG-k8s, CFG-lineage, CFG-mail, CFG-nextflow, CFG-podman, CFG-sarus,
CFG-seqera, CFG-shifter, CFG-spack, CFG-workflow

As a pipeline author, I want top-level config scopes `aws`, `azure`,
`charliecloud`, `fusion`, `google`, `k8s`, `lineage`, `mail`,
`nextflow`, `podman`, `sarus`, `seqera`, `shifter`, `spack`,
`workflow` to be silently skipped, so that configs using these scopes
parse without error.

Add all 15 scope names to the `skippedTopLevelConfigScopes` map in
`config.go`. Adding `"nextflow"` here also fixes the 8 features in
Group C.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/config.go`
**Test file:** `nextflowdsl/config_test.go`

**Acceptance tests:**

1. Given config `aws { batch { cliPath = '/usr/bin/aws' } }`, when
   parsed with `ParseConfig`, then err is nil and a stderr warning
   contains `"aws"`.
2. Given config `azure { storage { accountName = 'x' } }`, when
   parsed, then err is nil.
3. Given config `charliecloud { enabled = true }`, when parsed, then
   err is nil.
4. Given config `fusion { enabled = true }`, when parsed, then
   err is nil.
5. Given config `google { region = 'us-east1' }`, when parsed, then
   err is nil.
6. Given config `k8s { namespace = 'default' }`, when parsed, then
   err is nil.
7. Given config `lineage { enabled = true }`, when parsed, then
   err is nil.
8. Given config `mail { smtp { host = 'x' } }`, when parsed, then
   err is nil.
9. Given config `nextflow { enable { dsl = 2 } }`, when parsed, then
   err is nil (also tests C1 features).
10. Given config `podman { enabled = true }`, when parsed, then
    err is nil.
11. Given config `sarus { enabled = true }`, when parsed, then
    err is nil.
12. Given config `seqera { endpoint = 'https://x' }`, when parsed,
    then err is nil.
13. Given config `shifter { enabled = true }`, when parsed, then
    err is nil.
14. Given config `spack { enabled = true }`, when parsed, then
    err is nil.
15. Given config `workflow { onComplete { println 'done' } }`, when
    parsed, then err is nil.
16. Given config with both `params { x = 1 }` and
    `aws { batch { cliPath = '/bin/aws' } }`, when parsed, then
    err is nil and `cfg.Params["x"]` equals `1`.

---

## C: `nextflow` scope covers enable/preview flags (8 features)

### C1: CFG-enable-configProcessNamesValidation,
CFG-enable-dsl, CFG-enable-moduleBinaries, CFG-enable-strict,
CFG-preview-output, CFG-preview-recursion, CFG-preview-topic,
CFG-preview-types

As a pipeline author, I want `nextflow.enable.*` and
`nextflow.preview.*` feature flags in config files to parse without
error, so that configs using Nextflow feature flags work.

These 8 features are all fixed by the `"nextflow"` entry added in
B1. This section provides the acceptance tests for those 8 features.

NOTE: CFG-nextflow is counted in Group B's 15. The single map
addition satisfies both B1 (for CFG-nextflow) and C1 (for these 8).

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/config.go`
**Test file:** `nextflowdsl/config_test.go`

**Acceptance tests:**

1. Given config `nextflow { enable { dsl = 2 } }`, when parsed, then
   err is nil.
2. Given config `nextflow { enable { strict = true } }`, when parsed,
   then err is nil.
3. Given config `nextflow { enable { moduleBinaries = true } }`, when
   parsed, then err is nil.
4. Given config
   `nextflow { enable { configProcessNamesValidation = true } }`,
   when parsed, then err is nil.
5. Given config `nextflow { preview { output = true } }`, when parsed,
   then err is nil.
6. Given config `nextflow { preview { recursion = true } }`, when
   parsed, then err is nil.
7. Given config `nextflow { preview { topic = true } }`, when parsed,
   then err is nil.
8. Given config `nextflow { preview { types = true } }`, when parsed,
   then err is nil.
9. Given config with `params { x = 1 }` and
   `nextflow { enable { dsl = 2 } }`, when parsed, then err is nil
   and `cfg.Params["x"]` equals `1`.

---

## D: Skip nested blocks in `parseExecutorBlock` (2 features)

### D1: CFG-executor-specific-configuration,
CFG-executor-specific-defaults

As a pipeline author, I want executor-specific nested config blocks
(e.g. `executor { local { cpus = 4 } }`) to parse without error, so
that pipelines with executor-specific configuration work.

In `parseExecutorBlock`, after consuming the identifier token, peek
at the next token BEFORE calling `parseAssignmentValue`. If the next
token is `{`, skip the nested block using brace-counting (same
approach as `skipNamedBlock` but without the leading `expectIdent`
call -- just count braces from the current `{`). Store nothing for
the nested block; it is silently skipped.

Also handle `tokenString`-keyed blocks (e.g. `'$local' { ... }`):
add a `tokenString` case in the switch that peeks at the next token;
if `{`, skip the nested block using brace-counting.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/config.go`
**Test file:** `nextflowdsl/config_test.go`

**Acceptance tests:**

1. Given config `executor { local { cpus = 4 } }`, when parsed, then
   err is nil.
2. Given config `executor { name = 'slurm' ; slurm { queue = 'x' } }`,
   when parsed, then err is nil and `cfg.Executor["name"]` equals
   `"slurm"`.
3. Given config `executor { '$local' { cpus = 8 } }`, when parsed,
   then err is nil (string-keyed nested blocks are skipped).
4. Given config `executor { name = 'local' }` (flat, no nesting),
   when parsed, then err is nil and `cfg.Executor["name"]` equals
   `"local"` (existing behaviour preserved).

---

## E: Handle bare assignments in config parser (1 feature)

### E1: CFG-unscoped-options

As a pipeline author, I want bare top-level config assignments (e.g.
`cleanup = true`) to parse without error, so that configs with
unscoped options work.

In `configParser.parse`, in the `default` branch, after
`skipUnknownTopLevelConfigScope` returns false, check if the next
token after the identifier is `=`. If so, skip the identifier and
the assignment value (consume tokens until newline/semicolon/EOF).
Emit a warning. If neither a known scope, skippable scope, nor bare
assignment, return the existing "unsupported config section" error.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/config.go`
**Test file:** `nextflowdsl/config_test.go`

**Acceptance tests:**

1. Given config `cleanup = true`, when parsed, then err is nil.
2. Given config `cleanup = true ; params { x = 1 }`, when parsed,
   then err is nil and `cfg.Params["x"]` equals `1`.
3. Given config `resume = true\ncleanup = false`, when parsed, then
   err is nil (multiple bare assignments).
4. Given config `unknownScope { }` (unknown scope without `=`), when
   parsed, then err is not nil and contains "unsupported config
   section".

---

## F: Add `name:` and `stageAs:` qualifiers (2 features)

### F1: INP-path-name, INP-path-stageAs

As a pipeline author, I want `path` input declarations with `name:`
and `stageAs:` qualifiers to parse without error, so that pipelines
using these qualifiers work.

**AST changes in `ast.go`:**
- Add field `StageName string` to `TupleElement` struct.
- Add field `StageName string` to `Declaration` struct.
- Add field `StageAs string` to `TupleElement` struct.
- Add field `StageAs string` to `Declaration` struct.

**Parser changes in `parse.go`:**

In `applyDeclarationQualifier`, add cases:
- `"name"`: parse value as expression; if `StringExpr`, store in
  `decl.StageName`; if `VarExpr` with empty `Path`, store `Root`
  in `decl.StageName`; else error.
- `"stageAs"`: same logic, store in `decl.StageAs`.

In `applyTupleElementQualifier`, add matching cases:
- `"name"`: store in `element.StageName`.
- `"stageAs"`: store in `element.StageAs`.

**Package:** `nextflowdsl/`
**Files:** `nextflowdsl/ast.go`, `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_e1_test.go`

**Acceptance tests:**

1. Given input declaration `path x, name: 'reads_*.fq'`, when parsed,
   then `decl.StageName` equals `"reads_*.fq"` and err is nil.
2. Given input declaration `path x, stageAs: 'sample.fq'`, when
   parsed, then `decl.StageAs` equals `"sample.fq"` and err is nil.
3. Given tuple input `tuple val(a), path(b, stageAs: 'out.txt')`,
   when parsed, then the second element's `StageAs` equals
   `"out.txt"`.
4. Given tuple input `tuple val(a), path(b, name: '*.bam')`, when
   parsed, then the second element's `StageName` equals `"*.bam"`.
5. Given input `path x, name: pattern`, when parsed with identifier
   value, then `decl.StageName` equals `"pattern"` and err is nil.
6. Given input `path x, emit: 'y', name: 'z'`, when parsed, then
   `decl.Emit` equals `"y"` and `decl.StageName` equals `"z"`.

---

## G: Skip legacy DSL1 combined input/output syntax (1 feature)

### G1: PSEC-inputs-and-outputs-legacy

As a pipeline author, I want legacy DSL1 processes (using `into` in
declarations) to produce a clear warning instead of a crash, so that
legacy pipelines degrade gracefully.

The existing check in `parseDeclarationLine` already detects `into`
and returns `"DSL 1 syntax is not supported"`. Change this from a
hard error to: emit a warning to stderr (`"nextflowdsl: DSL 1 'into'
syntax at line %d is not supported, skipping declaration\n"`), then
return `nil, nil` (nil declaration, nil error) so the caller skips
the line. In `parseDeclarations`, add the guard
`if decl == nil { continue }` after the error check to skip nil
declarations.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_e1_test.go`

**Acceptance tests:**

1. Given a process with input `val x into ch`, when parsed, then
   err is nil and a stderr warning contains `"DSL 1"`.
2. Given a process with valid input `val x` followed by legacy input
   `val y into ch`, when parsed, then err is nil, only `val x` is in
   the declarations list, and stderr contains a warning.
3. Given a process with only valid `path x` input (no `into`), when
   parsed, then err is nil and no DSL1 warning is emitted (existing
   behaviour preserved).

---

## H: Parse `def (a, b) = expr` multi-assignment (1 feature)

### H1: STMT-multi-assignment

As a pipeline author, I want `def (a, b) = [1, 2]` to parse in
workflow and Groovy statement contexts, so that multi-assignment
syntax works.

In `groovy.go`, in `parseAssignmentStmt`, when `declare` is true
and the current token after consuming `def` is `(` (tokenLParen),
read tokens through the matching `)`, expect `=`, read the value
expression, parse via `parseMultiAssignExprTokens` (prepending a
synthetic `def` token), and return an `evalMultiAssignStmt` (a new
internal type wrapping `MultiAssignExpr`). Define
`evalMultiAssignStmt` as:

```go
type evalMultiAssignStmt struct {
    expr MultiAssignExpr
}
```

In `evalStatement`, add `case evalMultiAssignStmt:` which calls
`evalMultiAssignExpr(typed.expr, scope)`.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/groovy.go`
**Test file:** `nextflowdsl/parse_e1_test.go`

**Acceptance tests:**

1. Given statement `def (a, b) = [1, 2]`, when evaluated via
   `evalStatementBody`, then vars contain `a=1`, `b=2`.
2. Given statement `def (x, y, z) = [10, 20]`, when evaluated, then
   vars contain `x=10`, `y=20`, `z=nil` (excess names get nil).
3. Given statement `(a, b) = [3, 4]` (no `def`), when evaluated, then
   vars contain `a=3`, `b=4` (already works via
   `parseMultiAssignExprTokens`; regression guard).

---

## I: Skip deprecated `addParams`/`params` after include (1 feature)

### I1: SYN-deprecated-addParams

As a pipeline author, I want `include { X } from './mod' addParams(m)`
and `include { X } from './mod' params(m)` to parse with a
deprecation warning, so that older pipelines work.

In `parseImport`, after setting `importNode.Source` and advancing
`p.pos`, check if `p.current()` is an identifier with lit `addParams`
or `params`. If so:
1. Emit a warning: `"nextflowdsl: deprecated '%s' clause in include
   at line %d, ignoring\n"`.
2. Advance past the identifier.
3. If the next token is `(`, skip to matching `)` using
   paren-counting.
4. If the next token is `{` (map literal), skip to matching `}` using
   brace-counting.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_e1_test.go`

**Acceptance tests:**

1. Given `include { X } from './mod' addParams(foo: 1)`, when parsed,
   then err is nil, import source is `"./mod"`, import names contain
   `"X"`, and stderr contains `"deprecated"` and `"addParams"`.
2. Given `include { X } from './mod' params(bar: 2)`, when parsed,
   then err is nil, import source is `"./mod"`, and stderr contains
   `"deprecated"` and `"params"`.
3. Given `include { X } from './mod'` (no trailing clause), when
   parsed, then err is nil and no deprecation warning (existing
   behaviour preserved).
4. Given `include { X } from './mod' addParams([a: 1, b: 2])`, when
   parsed, then err is nil (parenthesised map argument skipped).

---

## J: Parse deprecated loop constructs with warning (2 features)

### J1: SYN-deprecated-for-loop, SYN-deprecated-while-loop

As a pipeline author, I want `for` and `while` loops in workflow
`main` sections to parse with a deprecation warning, so that
pipelines using deprecated loop syntax do not crash.

In `parseWorkflowBlock`, in the `"main"` case, before calling
`parseWorkflowStatement`, check for `current.lit == "for"` or
`current.lit == "while"`. If matched:

**For `for`:**
1. Emit warning: `"nextflowdsl: deprecated 'for' loop at line %d,
   skipping\n"`.
2. Advance past `for`.
3. Skip parenthesised condition: expect `(`, count parens to `)`.
4. Skip body: expect `{`, count braces to `}`.

**For `while`:**
1. Emit warning: `"nextflowdsl: deprecated 'while' loop at line %d,
   skipping\n"`.
2. Advance past `while`.
3. Skip parenthesised condition: expect `(`, count parens to `)`.
4. Skip body: expect `{`, count braces to `}`.

No AST nodes needed. The loop body is discarded.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_e1_test.go`

**Acceptance tests:**

1. Given workflow main containing
   `for (x in [1,2,3]) { FOO(ch) }`, when parsed, then err is nil
   and stderr contains `"deprecated"` and `"for"`.
2. Given workflow main containing
   `while (x < 3) { BAR(ch) }`, when parsed, then err is nil and
   stderr contains `"deprecated"` and `"while"`.
3. Given workflow main with `FOO(ch)` followed by
   `for (x in items) { BAR(ch) }` followed by `BAZ(ch)`, when
   parsed, then err is nil, block contains calls for `FOO` and `BAZ`
   (loop body skipped), and stderr contains a warning.
4. Given workflow main with only `FOO(ch)` (no loops), when parsed,
   then err is nil and no deprecation warning (existing behaviour
   preserved).

---

## K: Handle `&` parallel operator in workflow desugaring (1 feature)

### K1: WF-and

As a pipeline author, I want `A & B` in workflow pipe expressions to
produce parallel calls, so that pipelines using the `&` operator
parse correctly.

In `desugarWorkflowPipe`, before the existing `PipeExpr` check, check
if the expression (or any pipe stage) is a `BinaryExpr` with
`Op == "&"`. If so, flatten the `&` tree into multiple `Call` entries
sharing the same input channel.

Specifically, extend `desugarWorkflowPipe`:
- If the top-level expr is a `BinaryExpr` with `Op == "&"`, collect
  all leaf operands (recursively flattening nested `&` exprs). Each
  leaf must be a `ChanRef`. Produce one `Call` per leaf, all with an
  empty `Args` slice (no explicit input -- the workflow wiring will
  provide it).
- If a `PipeExpr` stage is a `BinaryExpr` with `Op == "&"`, flatten
  that stage into multiple calls sharing the previous stage's output
  as input.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_e1_test.go`

**Acceptance tests:**

1. Given workflow statement `ch | A & B`, when parsed, then err is nil
   and the calls list contains two entries: one with Target `"A"` and
   one with Target `"B"`, both with input from `ch`.
2. Given workflow statement `ch | A & B & C`, when parsed, then err is
   nil and the calls list contains three entries with Targets `"A"`,
   `"B"`, `"C"`.
3. Given workflow statement `ch | A | B` (no `&`, just pipe), when
   parsed, then err is nil and calls are sequential (existing pipe
   behaviour preserved).
4. Given workflow statement `A & B` (no pipe prefix, top-level `&`),
   when parsed, then err is nil and the calls list contains entries
   for `"A"` and `"B"`.

---

## L: Add to `supportedChannelOperators` (2 features)

### L1: WF-recurse, WF-times

As a pipeline author, I want `.recurse()` and `.times()` channel
operators to parse without error, so that pipelines using recursion
syntax work.

Add `"recurse"` and `"times"` to the `supportedChannelOperators` map
in `parse.go`. They parse as standard channel operators (pass-through
during translation).

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_e1_test.go`

**Acceptance tests:**

1. Given workflow expression `FOO.out.recurse(10)`, when parsed, then
   err is nil and the channel chain contains operator `"recurse"`.
2. Given workflow expression `FOO.out.times(5)`, when parsed, then
   err is nil and the channel chain contains operator `"times"`.
3. Given workflow expression `FOO.out.map { it * 2 }` (existing
   operator), when parsed, then err is nil (regression guard).
4. Given workflow expression `FOO.out.nonexistent(1)`, when parsed,
   then err contains `"unsupported operator"` (non-allowlisted
   operators still rejected).

---

## Implementation Order

### Phase 1: Map/scope additions (Groups A, B, C, L)

Add entries to existing maps. No control flow changes. Each is
independent and can be implemented in parallel.

- **A1**: Add 7 keys to `defaultDirectiveTask()` in `translate.go`.
- **B1+C1**: Add 15 scopes (including `nextflow`) to
  `skippedTopLevelConfigScopes` in `config.go`. One edit covers all
  23 config-scope features (15 from B + 8 from C).
- **L1**: Add `"recurse"` and `"times"` to
  `supportedChannelOperators` in `parse.go`.

### Phase 2: Config parser fixes (Groups D, E)

Extend `parseExecutorBlock` and `configParser.parse`. Depends on
Phase 1 (B/C) being in place so tests can combine scopes.

- **D1**: Nested block skipping in `parseExecutorBlock`.
- **E1**: Bare assignment skipping in `configParser.parse`.

### Phase 3: Declaration and import fixes (Groups F, G, I)

Extend parse.go switch statements and import logic. Independent of
Phase 2.

- **F1**: Add `StageName`/`StageAs` fields and qualifier cases.
- **G1**: Change DSL1 `into` from hard error to warning+skip.
- **I1**: Skip deprecated `addParams`/`params` after import source.

### Phase 4: Statement and workflow fixes (Groups H, J, K)

More complex control flow changes. Depends on Phase 3 patterns.

- **H1**: Multi-assignment `def (a, b) = expr` in `groovy.go`.
- **J1**: Deprecated `for`/`while` loop skipping in workflow blocks.
- **K1**: `&` parallel operator desugaring in `desugarWorkflowPipe`.

---

## Appendix: Key Decisions

**Testing strategy:** Each group gets GoConvey tests verifying (a) no
parse error and (b) correct warnings on stderr where applicable.
Synthetic minimal test inputs per the GoConvey pattern in existing
`*_test.go` files. All existing tests must continue to pass.

**Warning format:** Follow the existing `fmt.Fprintf(os.Stderr,
"nextflowdsl: ...")` convention. Warnings are informational; they do
not affect the returned error.

**Struct field naming:** `StageName` and `StageAs` on `Declaration`
and `TupleElement` follow existing field naming (`Emit`, `Optional`).
`StageName` avoids collision with the existing `Name` field (which
holds the variable binding name).

**`&` operator semantics:** Real fork -- each operand becomes a
separate `Call` with the same input. Not a no-op.

**`recurse`/`times` semantics:** Parse-only (pass-through). No
runtime recursion. They appear in the channel chain AST but have no
special translation behaviour.

**Deprecated loops:** Token-consuming skip only. No AST nodes, no
evaluation of loop bodies. Variables assigned inside loops are not
available after the loop.

**DSL1 legacy:** Warning + skip (nil declaration). Not an error.

**Feature count:** A=7, B=15, C=8, D=2, E=1, F=2, G=1, H=1, I=1,
J=2, K=1, L=2. Total = 43. CFG-nextflow (in B's 15) and the 8
features in C share the same `"nextflow"` map entry.
