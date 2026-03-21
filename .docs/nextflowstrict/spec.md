# Nextflow DSL2 Strict Parsing and Tuple I/O Specification

## Overview

Extends the existing `nextflowdsl` package to close critical parsing
gaps (A1-A7 from the gap analysis) so that real-world DSL2 pipelines --
including nf-core workflows -- parse without error. Adds tuple I/O
translation so that processes using `tuple` declarations produce
correct wr jobs with proper input bindings and output path wiring.
Adds `includeConfig` support to the config parser and emit label
resolution for `process.out.labelName` access.

After implementation the parser accepts all syntactically valid DSL2
files for the constructs covered without parse errors. Constructs not
yet translatable produce clear warnings, not errors. Tuple I/O is
fully translated including dynamic paths via `TranslatePending`.

## Architecture

All changes are in the existing `nextflowdsl/` package. No new
packages.

### AST changes (ast.go)

```go
// TupleElement represents one element inside a tuple declaration.
type TupleElement struct {
    Kind string // "val", "path", "file", "env", "stdin", "stdout"
    Name string
    Expr Expr
    Raw  string
    Emit string // output emit label, empty if none
}

func (TupleElement) expr() {}
```

`Declaration` gains fields:

```go
type Declaration struct {
    Kind     string          // "val", "path", "file", "tuple",
                             // "stdout", "env", "stdin", "eval"
    Name     string
    Expr     Expr
    Raw      string
    Emit     string          // output emit label
    Optional bool            // output optional: true
    Elements []*TupleElement // non-nil only when Kind == "tuple"
}
```

`WorkflowBlock` gains section fields:

```go
type WorkflowBlock struct {
    Calls []*Call
    Take  []string   // take: channel names
    Emit  []*WFEmit  // emit: assignments
}

// WFEmit represents one line in a workflow emit: section.
type WFEmit struct {
    Name string // label (left of =), or bare channel name
    Expr string // raw expression (right of =), empty if bare
}
```

`Workflow` gains a field for top-level function definitions:

```go
type Workflow struct {
    Name      string
    Processes []*Process
    SubWFs    []*SubWorkflow
    Imports   []*Import
    EntryWF   *WorkflowBlock
    Functions []*FuncDef
}

// FuncDef stores a parsed function definition.
type FuncDef struct {
    Name   string
    Params []string // parameter names
    Body   string   // raw body text
}
```

### Parser changes (parse.go)

- Lexer: skip `/* ... */` block comments and `#!` shebang lines.
- `parseSection`: handle `stub:`, `exec:`, `shell:`, `when:`
  sections (parse and store raw body; warn if not translatable).
- `parseDeclarationLine`: recognise `tuple val(x), path(y)` syntax
  and `stdout`, `env(NAME)`, `stdin` types; parse `emit:` and
  `optional:` output qualifiers.
- `parseWorkflow`: recognise top-level `def` function definitions;
  skip top-level `output { ... }` blocks; skip `nextflow.*`
  feature flag assignments.
- `parseWorkflowBlockDecl`: detect and parse `take:`, `main:`,
  `emit:`, `publish:` section labels inside workflow blocks.
- Add high-priority operators to `supportedChannelOperators`:
  `branch`, `combine`, `concat`, `set`, `view`, `ifEmpty`,
  `splitCsv`, `splitFasta`, `splitFastq`, `transpose`, `unique`,
  `distinct`, `toList`, `toSortedList`, `flatten`, `count`,
  `reduce`, `multiMap`, `tap`, `dump`, `collectFile`.

### Config changes (config.go)

- `includeConfig 'path'` directive: resolve path relative to the
  config file, parse the included file, merge results.
- Skip unknown top-level config scopes (`docker`, `singularity`,
  `conda`, `env`, `manifest`, `timeline`, `report`, `trace`,
  `dag`, `executor`, `notification`, `weblog`, `tower`) with a
  warning instead of returning an error.

### Translation changes (translate.go)

- Tuple I/O: when a process has `tuple` input/output declarations,
  bind each element individually. For output, wire each `path`/
  `file` element's pattern into `outputPaths`; resolve `val`
  elements statically.
- Emit label resolution: `process.out.labelName` resolves to the
  specific output declaration with matching `Emit` label, not the
  entire output set.
- `TranslatePending`: handle tuple outputs by matching each
  element's pattern against completed output paths.

---

## A. Parsing Gaps

### A1: Tuple input/output declarations

As a developer, I want the parser to handle `tuple` compound
declarations in process `input:` and `output:` sections, so
that real-world pipelines using tuple I/O parse correctly.

Tuple syntax: `tuple val(id), path(reads)`. Each element has
a qualifier (`val`, `path`, `file`, `env`) and a name or
expression. Output tuples may include `emit:` and `optional:`
qualifiers on individual elements or the entire tuple line.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given process with `input: tuple val(id), path(reads)`, when
   parsed, then `Input` has 1 declaration with `Kind` == `"tuple"`
   and `Elements` of length 2: first element `Kind` == `"val"`,
   `Name` == `"id"`; second `Kind` == `"path"`, `Name` ==
   `"reads"`.
2. Given process with `output: tuple val(id), path("${id}.bam")`,
   when parsed, then `Output` has 1 declaration with `Kind` ==
   `"tuple"` and `Elements` of length 2: first element `Kind` ==
   `"val"`, `Name` == `"id"`; second `Kind` == `"path"`, `Expr`
   is a `StringExpr` with `Value` == `"${id}.bam"`.
3. Given process with `input: tuple val(id), path(r1), path(r2)`,
   when parsed, then `Input[0].Elements` has length 3.
4. Given process with `output: path "*.bam", emit: bam`, when
   parsed, then `Output[0].Emit` == `"bam"`.
5. Given process with `output: tuple val(id), path("*.bam"),
   emit: aligned`, when parsed, then `Output[0].Emit` ==
   `"aligned"` and `Output[0].Elements` has 2 entries.
6. Given process with `output: path "out.txt", optional: true`,
   when parsed, then `Output[0].Optional` == true.
7. Given process with `input: env(MY_VAR)`, when parsed, then
   `Input[0].Kind` == `"env"` and `Input[0].Name` == `"MY_VAR"`.
8. Given process with `input: stdin`, when parsed, then
   `Input[0].Kind` == `"stdin"`.
9. Given process with input `tuple val(meta), path("reads/*",
   arity: '1..*')`, when parsed, then parse succeeds without
   error (arity qualifier is accepted and ignored).

### A2: Workflow take/emit/publish sections

As a developer, I want the parser to handle `take:`, `main:`,
`emit:`, and `publish:` section labels in workflow blocks, so
that named subworkflows with explicit I/O parse correctly.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `workflow ALIGN { take: reads_ch\n main:\n
   BWA(reads_ch)\n emit:\n bam = BWA.out.bam }`, when parsed,
   then `SubWFs[0].Body.Take` == `["reads_ch"]`,
   `SubWFs[0].Body.Calls` has 1 call to `"BWA"`, and
   `SubWFs[0].Body.Emit` has 1 entry with `Name` == `"bam"` and
   `Expr` == `"BWA.out.bam"`.
2. Given workflow with `take:` listing 2 channels `ch1` and `ch2`,
   when parsed, then `Take` has length 2.
3. Given workflow with `main:` label but no `take:` or `emit:`,
   when parsed, then `Take` is empty, `Emit` is empty, and
   `Calls` are populated from the `main:` section.
4. Given workflow with `emit:` containing a bare channel name
   (no `=`), e.g. `emit:\n result_ch`, when parsed, then
   `Emit[0].Name` == `"result_ch"` and `Emit[0].Expr` == `""`.
5. Given workflow with `publish:` section, when parsed, then
   parse succeeds without error (publish section is accepted
   and its contents are stored or skipped).

### A3: Function definitions

As a developer, I want the parser to handle top-level `def`
function definitions, so that pipelines with helper functions
parse without error.

Functions are stored in the AST for potential future use. They
are not evaluated at translate time in this spec's scope.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `def greet(name) { return "hello ${name}" }`, when
   parsed, then `Functions` has 1 entry with `Name` == `"greet"`,
   `Params` == `["name"]`.
2. Given `def add(a, b) { a + b }` followed by a process block,
   when parsed, then both function and process are in the AST.
3. Given `def noParams() { 42 }`, when parsed, then `Params` is
   empty.

### A4: Additional process sections

As a developer, I want the parser to accept `stub:`, `exec:`,
`shell:`, and `when:` process sections, so that pipelines using
these features do not cause parse errors.

All four sections are parsed (body stored as raw text). A stderr
warning is emitted for sections not yet translatable.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

Extend `Process` with fields:

```go
type Process struct {
    // ... existing fields ...
    Stub  string // stub: section body
    Exec  string // exec: section body
    Shell string // shell: section body
    When  string // when: section condition
}
```

**Acceptance tests:**

1. Given process with `stub: "echo stub"`, when parsed, then
   `Stub` == `"echo stub"`.
2. Given process with `script:` and `stub:` sections, when
   parsed, then both `Script` and `Stub` are populated.
3. Given process with `exec: "println 'hello'"`, when parsed,
   then `Exec` == `"println 'hello'"`.
4. Given process with `shell: "echo !{var}"`, when parsed, then
   `Shell` == `"echo !{var}"`.
5. Given process with `when: "params.run_step"`, when parsed,
   then `When` == `"params.run_step"`.
6. Given process with all sections (`input:`, `output:`,
   `script:`, `stub:`, `when:`), when parsed, then all fields
   are populated and no error.

### A5: Additional I/O types and qualifiers

As a developer, I want `stdout`, `env`, `stdin`, and `eval`
I/O types plus `emit:`, `optional:`, and `topic:` qualifiers
parsed, so that real pipelines using these features do not
error.

Note: `eval(command)` output and `topic:` are parsed but not
translated (warning emitted).

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given process with `output: stdout`, when parsed, then
   `Output[0].Kind` == `"stdout"` and `Name` == `""`.
2. Given process with `output: env(MY_VAR)`, when parsed, then
   `Output[0].Kind` == `"env"` and `Output[0].Name` ==
   `"MY_VAR"`.
3. Given process with `output: eval("hostname")`, when parsed,
   then `Output[0].Kind` == `"eval"` and parse succeeds.
4. Given process with `output: path "*.bam", topic: "aligned"`,
   when parsed, then parse succeeds (topic stored or ignored).
5. Given process with both `emit:` and `optional:` on one line:
   `output: path "out.txt", emit: result, optional: true`, when
   parsed, then `Emit` == `"result"` and `Optional` == true.

### A6: Shebang, block comments, feature flags

As a developer, I want the lexer to skip `#!/usr/bin/env nextflow`
shebang lines, `/* ... */` block comments, and
`nextflow.preview.*` / `nextflow.enable.*` assignments, so that
files with these constructs parse cleanly.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given input starting with `#!/usr/bin/env nextflow\n
   process foo { script: 'echo hi' }`, when parsed, then 1
   process named `"foo"` and no error.
2. Given input `/* block comment */\nprocess foo { script:
   'echo hi' }`, when parsed, then 1 process and no error.
3. Given `/* multi\nline\ncomment */\nprocess foo { script:
   'echo hi' }`, when parsed, then 1 process and no error.
4. Given `nextflow.enable.dsl = 2\nprocess foo { script:
   'echo hi' }`, when parsed, then 1 process and no error.
5. Given `nextflow.preview.recursion = true\nprocess foo {
   script: 'echo hi' }`, when parsed, then 1 process and no
   error.
6. Given `/* unclosed block comment`, when parsed, then error
   mentions unterminated comment.

### A7: Top-level output block

As a developer, I want the parser to accept top-level
`output { ... }` blocks, so that pipelines using the new
workflow output syntax do not cause parse errors.

The block is parsed and skipped (wr handles output via
publishDir).

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `output { samples { path 'fastq' } }\nprocess foo {
   script: 'echo hi' }`, when parsed, then 1 process and no
   error.
2. Given `output { }`, when parsed, then no error and 0
   processes.
3. Given `output { deeply { nested { block { } } } }`, when
   parsed, then no error.

---

## B. Tuple I/O Translation

### B1: Tuple input binding

As a developer, I want tuple input declarations to bind each
element individually from upstream channel items, so that
multi-element inputs produce correct shell variable exports.

When a process has `input: tuple val(id), path(reads)` and
receives a 2-element list `["sampleA", "/data/a.fq"]` from
upstream, the job's `Cmd` exports `id='sampleA'` and
`reads='/data/a.fq'`.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given process with `input: tuple val(id), path(reads)` and
   upstream channel providing `["s1", "/data/s1.fq"]`, when
   translated, then job `Cmd` contains `export id='s1'` and
   `export reads='/data/s1.fq'`.
2. Given process with `input: tuple val(id), path(r1), path(r2)`
   and upstream providing `["s1", "/data/r1.fq", "/data/r2.fq"]`,
   when translated, then `Cmd` contains exports for all 3
   variables.
3. Given process with `input: val x` (non-tuple, unchanged
   behaviour), when translated, then existing behaviour is
   preserved.

### B2: Tuple output path wiring

As a developer, I want tuple output declarations to wire each
`path`/`file` element's pattern into the output path list, so
that downstream processes receive correct file references.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given process A with `output: tuple val(id),
   path("${id}.bam")` and a downstream process B, when A is
   translated with input `id='s1'`, then A's output paths
   include `{cwd}/s1.bam` and B's `Cmd` references that path.
2. Given process with `output: tuple val(id), path("*.bam")`,
   when translated, then the stage is marked as pending
   (dynamic) because it contains a `path` element.
3. Given process with `output: tuple val(id), val(count)`
   (all val), when translated, then stage is NOT pending (static
   val-only output).

### B3: Tuple output in TranslatePending

As a developer, I want `TranslatePending` to handle tuple outputs
by matching each element's pattern against completed paths, so
that dynamic tuple pipelines resolve correctly.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given pending process B awaiting A whose tuple output is
   `tuple val(id), path("${id}.bam")`, when `TranslatePending`
   is called with completed paths `["/work/s1.bam"]`, then B's
   job `Cmd` references `/work/s1.bam`.
2. Given pending process with upstream tuple output
   `tuple val(id), path("*.bam")` and completed paths
   `["/work/a.bam", "/work/b.bam"]`, when `TranslatePending` is
   called, then downstream jobs reference both paths.

---

## C. Emit Label Resolution

### C1: Parse and resolve emit labels

As a developer, I want `process.out.labelName` references to
resolve to the specific output declaration matching the `emit:`
label, so that subworkflows can wire individual named outputs.

This requires the translator to index process outputs by their
emit labels and resolve `out.labelName` references against that
index when wiring downstream inputs.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given process A with `output: path "*.bam", emit: bam` and
   `output: path "*.bai", emit: idx`, and downstream process B
   receiving `A.out.bam`, when translated, then B's `Cmd`
   references only A's `*.bam` output path, not `*.bai`.
2. Given process A with `output: path "*.bam", emit: bam` and
   downstream referencing `A.out.bam`, when A's stage is pending
   and `TranslatePending` is called with completed paths
   `["/work/x.bam", "/work/x.bai"]`, then B receives only the
   `.bam` path (filtered by emit label pattern).
3. Given process with no `emit:` labels on output, when
   downstream references `process.out`, then all output paths
   are provided (existing behaviour preserved).

---

## D. Config: includeConfig

### D1: Parse and follow includeConfig directives

As a developer, I want the config parser to follow
`includeConfig 'path'` directives, so that modular config
files load correctly.

Paths are resolved relative to the including config file's
directory. Circular includes are detected and return an error.
`ParseConfig` gains a new variant:

```go
// ParseConfigFromPath parses a config file by path, following
// includeConfig directives relative to the file's directory.
func ParseConfigFromPath(path string) (*Config, error)

// ParseConfigFromPathWithParams parses a config file by path
// with external params.
func ParseConfigFromPathWithParams(
    path string,
    externalParams map[string]any,
) (*Config, error)
```

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/config.go`
**Test file:** `nextflowdsl/config_test.go`

**Acceptance tests:**

1. Given main config containing `includeConfig 'base.config'`
   and `base.config` has `params { input = '/data' }`, when
   parsed via `ParseConfigFromPath`, then
   `Config.Params["input"]` == `"/data"`.
2. Given main config with `params { x = 1 }` and included config
   with `params { x = 2 ; y = 3 }`, when parsed, then `x` == 2
   (included overrides) and `y` == 3.
3. Given `includeConfig 'missing.config'` where file does not
   exist, when parsed, then error mentions the missing path.
4. Given circular include (A includes B, B includes A), when
   parsed, then error mentions circular include.
5. Given config with `includeConfig 'sub/nested.config'` where
   `sub/nested.config` includes `includeConfig 'deep.config'`
   (relative to sub/), when parsed, then deep config values are
   merged.

### D2: Skip unknown config scopes

As a developer, I want the config parser to skip unknown
top-level config scopes (e.g. `docker`, `singularity`, `env`,
`manifest`) with a warning instead of returning an error, so
that real-world config files parse successfully.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/config.go`
**Test file:** `nextflowdsl/config_test.go`

**Acceptance tests:**

1. Given config `docker { enabled = true }\nparams { x = 1 }`,
   when parsed, then `Config.Params["x"]` == 1 and no error.
2. Given config with `manifest { name = 'test' }`, when parsed,
   then no error.
3. Given config with `env { FOO = 'bar' }`, when parsed, then
   no error.
4. Given config with `singularity { enabled = true }`, when
   parsed, then no error.
5. Given config with `timeline { enabled = true }\n
   report { enabled = true }`, when parsed, then no error.
6. Given config with `includeConfig 'x.config'` followed by
   `docker { enabled = true }`, when parsed via
   `ParseConfigFromPath` (with x.config containing
   `params { a = 1 }`), then `Params["a"]` == 1 and docker
   scope is skipped.

---

## E. Expanded Channel Operator Parsing

### E1: Accept high-priority operators without error

As a developer, I want all high-priority channel operators to
parse without error, so that real-world operator chains do not
cause parse failures.

Operators added to `supportedChannelOperators`: `branch`,
`combine`, `concat`, `set`, `view`, `ifEmpty`, `splitCsv`,
`splitFasta`, `splitFastq`, `transpose`, `unique`, `distinct`,
`toList`, `toSortedList`, `flatten`, `count`, `reduce`,
`multiMap`, `tap`, `dump`, `collectFile`.

Translation of these operators remains at the existing 10;
new operators are parsed but produce a warning if they affect
job cardinality in ways the translator cannot handle. `view`
and `dump` are no-ops (debug operators). `set` assigns to a
channel variable.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `ch.branch { foo: it > 5 }`, when parsed, then operator
   is `"branch"` with closure.
2. Given `ch.combine(other)`, when parsed, then operator is
   `"combine"` with 1 channel arg.
3. Given `ch.concat(a, b)`, when parsed, then operator is
   `"concat"` with 2 channel args.
4. Given `ch.set { result }`, when parsed, then operator is
   `"set"`.
5. Given `ch.view()`, when parsed, then operator is `"view"`.
6. Given `ch.ifEmpty('default')`, when parsed, then operator is
   `"ifEmpty"` with 1 arg.
7. Given `ch.splitCsv(header: true)`, when parsed, then operator
   is `"splitCsv"`.
8. Given `ch.transpose()`, when parsed, then operator is
   `"transpose"`.
9. Given `ch.flatten()`, when parsed, then operator is
   `"flatten"`.
10. Given `ch.reduce { a, b -> a + b }`, when parsed, then
    operator is `"reduce"` with closure.
11. Given `ch.collectFile(name: 'output.txt')`, when parsed,
    then operator is `"collectFile"`.
12. Given `ch.tap { branch_ch }`, when parsed, then operator is
    `"tap"`.
13. Given `ch.dump(tag: 'debug')`, when parsed, then operator is
    `"dump"`.
14. Given `ch.multiMap { it -> foo: it; bar: it }`, when parsed,
    then operator is `"multiMap"`.
15. Given `ch.unique()`, when parsed, then operator is `"unique"`.
16. Given `ch.toList()`, when parsed, then operator is `"toList"`.
17. Given `ch.count()`, when parsed, then operator is `"count"`.
18. Given `ch.subscribe { ... }` (not in high-priority list),
    when parsed, then error mentions `"unsupported operator:
    subscribe"`.

---

## Implementation Order

### Phase 1: Lexer hardening (A6)

Shebang, block comments, feature flags. Foundation for all
subsequent parsing. No dependencies on other phases.

### Phase 2: Process section parsing (A4, A5, A1)

Sequential. A4 (stub/exec/shell/when sections) first, then A5
(additional I/O types and qualifiers), then A1 (tuple I/O).
A1 depends on A5 for the expanded type vocabulary. All extend
the existing process parser.

### Phase 3: Workflow block parsing (A2, A3, A7)

A2 (take/emit/publish), A3 (function defs), A7 (output block)
are independent of each other but all modify the top-level
parser. Can be parallel within phase. Depends on Phase 1 for
block comment/shebang handling.

### Phase 4: Expanded operator parsing (E1)

Adds operators to `supportedChannelOperators` map and updates
`parseChannelOperatorArgs` for operators taking channel args
(`combine`, `concat`, `tap`). Independent of Phases 2-3.
Depends on Phase 1.

### Phase 5: Config improvements (D1, D2)

Sequential: D2 (skip unknown scopes) first, then D1
(`includeConfig`). D1 depends on D2 because included files
may contain unknown scopes. Independent of Phases 2-4.

### Phase 6: Translation (B1, B2, B3, C1)

Sequential: B1 (tuple input binding) first, B2 (tuple output
wiring), B3 (tuple TranslatePending), C1 (emit label
resolution). Depends on Phase 2 for tuple AST types and Phase
3 for emit labels.

---

## Appendix: Key Decisions

1. **Parse-first strategy.** Parsing robustness is prioritised:
   the parser should accept valid DSL2 without error even if
   translation is incomplete. Untranslatable constructs produce
   warnings, not errors.
2. **AST breaking changes acceptable.** Exported fields on
   `Declaration`, `WorkflowBlock`, `Process`, and `Workflow`
   change. Callers (`cmd/nextflow.go`) are updated accordingly.
3. **Tuple elements are a slice, not a map.** Order matters for
   positional binding to upstream channel items.
4. **Emit labels are string-matched.** `process.out.bam` matches
   the output declaration whose `Emit` == `"bam"`. If no match,
   fall back to existing full-output behaviour with a warning.
5. **includeConfig paths are relative to the including file.**
   Matches Nextflow's resolution semantics. Circular includes
   detected via a visited-path set.
6. **Unknown config scopes are skipped.** Use the existing
   `skipNamedBlock` helper. Emit a stderr warning.
7. **New operators parse but do not translate.** Only `view` and
   `dump` are actively handled as no-ops. Other new operators
   pass through to translation, which falls back to existing
   channel item passthrough behaviour and warns if the operator
   would change cardinality.
8. **Testing.** GoConvey for all unit tests per go-conventions.
   Acceptance tests use synthetic minimal workflows. Existing
   tests must continue to pass.
9. **Function definitions stored but not evaluated.** `FuncDef`
   AST nodes are available for future expression inlining but
   this spec does not implement function call resolution.
10. **stub/exec/shell/when sections stored as raw strings.**
    Same approach as the existing `Script` field. Translation
    uses `Script` preferentially; `stub` is used only if a
    `--stub-run` flag is added in a future spec.
