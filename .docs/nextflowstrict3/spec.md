# Nextflow DSL2 Parser Completeness Specification

## Overview

Closes all remaining gaps between the `nextflowdsl` parser and
full Nextflow DSL2 syntax compatibility. After implementation,
any syntactically valid DSL2 workflow file (excluding v25.10+
typed-process syntax) parses without error and translates
correctly where applicable.

Changes span 15 gaps: missing process directives, `each`/`eval`
I/O qualifiers, Groovy operator/expression extensions, statement
types, `params {}` block syntax, enum/record types, output block
parsing, workflow block sections, pipe operator, variable
assignments in workflow main, channel operator implementations,
config scope handling, dynamic directives, and `each`
cross-product translation.

All changes confined to `nextflowdsl/` and `cmd/` packages.
Constructs that cannot be meaningfully translated produce
warnings, never parse failures.

## Architecture

### AST changes (ast.go)

`Declaration` gains an `Each` flag:

```go
type Declaration struct {
    // ... existing fields ...
    Each bool // true when preceded by `each` qualifier
}
```

New expression nodes:

```go
// RangeExpr stores a .. or ..< range expression.
type RangeExpr struct {
    Start     Expr
    End       Expr
    Exclusive bool // true for ..<
}
func (RangeExpr) expr() {}

// SpreadExpr stores a spread-dot *.property expression.
type SpreadExpr struct {
    Receiver Expr
    Property string
}
func (SpreadExpr) expr() {}

// InExpr stores an `in` / `!in` membership test.
type InExpr struct {
    Left    Expr
    Right   Expr
    Negated bool // true for `!in`
}
func (InExpr) expr() {}

// RegexExpr stores a =~ or ==~ pattern match.
type RegexExpr struct {
    Left  Expr
    Right Expr
    Full  bool // true for ==~ (full match), false for =~
}
func (RegexExpr) expr() {}

// NewExpr stores a `new ClassName(args)` constructor.
type NewExpr struct {
    ClassName string
    Args      []Expr
}
func (NewExpr) expr() {}

// SlashyStringExpr stores a /pattern/ slashy string.
type SlashyStringExpr struct {
    Value string
}
func (SlashyStringExpr) expr() {}

// MultiAssignExpr stores `def (x, y) = expr` or
// `(x, y) = expr`.
type MultiAssignExpr struct {
    Names []string
    Value Expr
}
func (MultiAssignExpr) expr() {}
```

New statement node types for skippable statements:

```go
// AssertStmt stores `assert expr` or `assert expr : msg`.
type AssertStmt struct {
    Expr    Expr
    Message Expr // nil if no message
}

// ThrowStmt stores `throw expr`.
type ThrowStmt struct {
    Expr Expr
}

// TryCatchStmt stores `try { } catch { } finally { }`.
type TryCatchStmt struct {
    TryBody     string
    CatchClauses []CatchClause
    FinallyBody string
}

// CatchClause stores one catch block.
type CatchClause struct {
    TypeName string
    VarName  string
    Body     string
}

// ReturnStmt stores `return expr`.
type ReturnStmt struct {
    Expr Expr
}

// ForStmt stores `for (x in coll) { body }`.
type ForStmt struct {
    VarName    string
    Collection Expr
    Body       string
}

// WhileStmt stores `while (cond) { body }`.
type WhileStmt struct {
    Condition Expr
    Body      string
}

// SwitchStmt stores `switch (expr) { case ... }`.
type SwitchStmt struct {
    Expr string
    Body string
}
```

`WorkflowBlock` gains new sections:

```go
type WorkflowBlock struct {
    // ... existing fields ...
    Publish    []*WFPublish
    OnComplete string // raw body
    OnError    string // raw body
    Statements []any  // mixed Call, IfBlock, assignment, etc.
}

// WFPublish represents a publish assignment in workflow block.
// Target is the LHS, Source is the RHS of the assignment.
type WFPublish struct {
    Target string
    Source string
}
```

`Workflow` gains a `Params` block and type definitions:

```go
type Workflow struct {
    // ... existing fields ...
    ParamBlock []*ParamDecl
    Enums      []*EnumDef
    Records    []*RecordDef
    OutputBlock string // raw body of top-level output {}
}

// ParamDecl stores a typed parameter from params {} block.
type ParamDecl struct {
    Name    string
    Type    string // type annotation, empty if none
    Default Expr   // default value, nil if none
}

// EnumDef stores an enum type definition.
type EnumDef struct {
    Name   string
    Values []string
}

// RecordDef stores a record type definition.
type RecordDef struct {
    Name   string
    Fields []*RecordField
}

// RecordField stores one field in a record definition.
type RecordField struct {
    Name    string
    Type    string
    Default Expr
}
```

### Config changes (config.go)

Add `wave` to `skippedTopLevelConfigScopes`. Parse `executor {}`
scope to extract `name`, `queueSize`, `queue`,
`clusterOptions`. Store as raw `map[string]any`:

```go
type Config struct {
    // ... existing fields ...
    Executor map[string]any // executor scope key-values
}
```

### Parse changes (parse.go)

- Lexer: recognise `/pattern/` slashy strings, `=~`, `==~`,
  `<=>`, `..`, `..<`, `<<`, `>>`, `>>>`, `&`, `^`, `~`,
  `**`, `*.` (spread-dot) as token types.
- `parseDeclarationLine`: recognise `each` prefix before
  qualifier (e.g. `each val(x)`, `each path(p)`). Set
  `Declaration.Each = true`.
- `parseExprTokens`: handle `%`, `**`, `in`, `!in`,
  `instanceof`, `!instanceof`, `=~`, `==~`, `<=>`, `..`,
  `..<`, `<<`, `>>`, `>>>`, `&`, `^`, `|` (bitwise),
  `~` (unary), `*.` (spread-dot), `new` keyword,
  slashy strings, multi-variable declarations/assignments.
- `parseStatement`: recognise `assert`, `throw`, `try/catch`,
  `return`, `for`, `while`, `switch/case` and parse into
  statement nodes. Store on process/function body for
  skip-through.
- `parseTopLevel`: recognise `params { ... }` block syntax,
  `enum Name { ... }`, `record Name { ... }` definitions.
- `parseProcessDirective`: add all GAP 1 directives to the
  accepted set, storing in `Directives` map.
- Modify `errorStrategy` and `container` directive parsing to
  accept any `Expr` (including `ClosureExpr`). When the
  expression is a `StringExpr`, populate the dedicated
  `Process.ErrorStrat` / `Process.Container` fields (backward
  compat). When it is a `ClosureExpr` or other expression,
  store in `Process.Directives[name]` for deferred evaluation
  at translate time.
- `parseWorkflowBlock`: accept `publish:`, `onComplete:`,
  `onError:` sections. Store parsed content.
- Pipe operator already handled via `PipeExpr`. No changes
  needed.
- Variable assignments in workflow main already handled via
  `parseChannelAssignment`. Extend to track plain variable
  assignments whose RHS is a channel expression or process
  output reference.

### Groovy/eval changes (groovy.go)

- `evalBinaryExpr`: add `%`, `in`, `=~`/`==~`, `..`/`..<`,
  `**` evaluation. Warn on `<=>`, bitwise, shift operators.
- `evalUnaryExpr`: add `~` (bitwise NOT).
- New: `evalInExpr`, `evalRegexExpr`, `evalRangeExpr`,
  `evalSpreadExpr`.
- `evalMethodCallExpr`: add `collect`, `findAll`, `find`,
  `any`, `every`, `join`, `unique`, `sort`, `plus`,
  `minus`, `multiply`, `tokenize`, `matches`, `replaceAll`,
  `take`, `drop` for common patterns.

### Channel changes (channel.go)

Implement real resolution for operators currently in
`unsupportedCardinalityOperators`:
- `combine(other, [by: n])`: cross product, optionally keyed.
- `concat(ch1, ch2, ...)`: ordered concatenation.
- `flatten()`: flatten nested lists.
- `transpose([by: n])`: un-group tuples.
- `unique()` / `distinct()`: deduplication.
- `branch { criteria }`: passthrough with warning (all items
  to single channel).
- `multiMap { criteria }`: passthrough with warning.
- `splitCsv`, `splitJson`, `splitText`, `splitFasta`,
  `splitFastq`: TranslatePending with warning (data-dependent).
- `collectFile`: TranslatePending with warning.
- `ifEmpty(value)`: return items if non-empty, else `[value]`.
- `toList()` / `toSortedList()`: collect to single list item.
- `count([filter])`: single item with count.
- `reduce(acc, closure)`: fold items.
- `toLong()`, `toFloat()`, `toDouble()`: type-conversion
  operators (pass-through like `toInteger`).

### Translation changes (translate.go)

- `each` cross-product: when a process has `each`-flagged
  inputs, create N x M jobs where N is from regular inputs
  and M is from each channel. Each job gets unique CWD.
- Dynamic directives: already handled -- `resolveDirectiveInt`
  evaluates with `task.attempt=1`. No additional work needed
  beyond new operator support in groovy.go.
- `eval` output: append eval command to script wrapper,
  capture stdout into named variable.

---

## A. Missing Process Directives

### A1: Parse all remaining process directives

As a developer, I want all Nextflow-defined process directives
accepted by the parser, so that pipelines using any standard
directive do not cause parse errors.

New directives to add to `parseProcessDirective` accepted set,
all stored in `Process.Directives` map via `parseDirectiveExpr`
with `warnStoredDirective`:

`accelerator`, `arch`, `array`, `clusterOptions`, `conda`,
`containerOptions`, `debug` (primary; `echo` is its deprecated
alias -- both accepted), `executor`, `ext`, `fair`,
`machineType`, `maxErrors`, `maxSubmitAwait`, `penv`, `pod`,
`queue`, `resourceLabels`, `resourceLimits`, `scratch`,
`secret`, `spack`, `stageInMode`, `stageOutMode`, `storeDir`.
Also add `shell` as a directive (distinct from the `shell:`
section -- directive parsed when not followed by `:` in a
section-label position).

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given process with `accelerator 1, type: 'nvidia-tesla-v100'`,
   when parsed, then `Directives["accelerator"]` is non-nil and
   no error.
2. Given process with `arch 'linux/x86_64'`, when parsed, then
   `Directives["arch"]` is non-nil.
3. Given process with `array 100`, when parsed, then
   `Directives["array"]` is non-nil.
4. Given process with `conda 'samtools=1.17'`, when parsed, then
   `Directives["conda"]` is non-nil.
5. Given process with `containerOptions '--gpus all'`, when
   parsed, then `Directives["containerOptions"]` is non-nil.
6. Given process with `echo true`, when parsed, then
   `Directives["echo"]` is non-nil.
7. Given process with `ext foo: 'bar'`, when parsed, then
   `Directives["ext"]` is non-nil.
8. Given process with `fair true`, when parsed, then
   `Directives["fair"]` is non-nil.
9. Given process with `machineType 'n1-standard-8'`, when parsed,
   then `Directives["machineType"]` is non-nil.
10. Given process with `maxErrors 5`, when parsed, then
    `Directives["maxErrors"]` is non-nil.
11. Given process with `maxSubmitAwait '1h'`, when parsed, then
    `Directives["maxSubmitAwait"]` is non-nil.
12. Given process with `penv 'smp'`, when parsed, then
    `Directives["penv"]` is non-nil.
13. Given process with `pod [label: 'app', value: 'test']`, when
    parsed, then `Directives["pod"]` is non-nil.
14. Given process with `resourceLabels region: 'eu-west-1'`, when
    parsed, then `Directives["resourceLabels"]` is non-nil.
15. Given process with `resourceLimits cpus: 64, memory: '256.GB'`,
    when parsed, then `Directives["resourceLimits"]` is non-nil.
16. Given process with `spack 'samtools@1.17'`, when parsed, then
    `Directives["spack"]` is non-nil.
17. Given process with `stageInMode 'copy'`, when parsed, then
    `Directives["stageInMode"]` is non-nil.
18. Given process with `stageOutMode 'move'`, when parsed, then
    `Directives["stageOutMode"]` is non-nil.
19. Given process with all existing directives plus new ones, when
    parsed, then no error and all directives stored.
20. Given process with unknown directive `foobar 42`, when parsed,
    then warning emitted but no error.
21. Given process with `stage:` section, when parsed, then
    section is skipped and no error.
22. Given process with `topic:` output qualifier, when parsed,
    then qualifier is accepted and no error.
23. Given process with `clusterOptions '--account=mylab'`, when
    parsed, then `Directives["clusterOptions"]` is non-nil.
24. Given process with `debug true`, when parsed, then
    `Directives["debug"]` is non-nil.
25. Given process with `executor 'slurm'`, when parsed, then
    `Directives["executor"]` is non-nil.
26. Given process with `queue 'long'`, when parsed, then
    `Directives["queue"]` is non-nil.
27. Given process with `scratch true`, when parsed, then
    `Directives["scratch"]` is non-nil.
28. Given process with `secret 'MY_TOKEN'`, when parsed, then
    `Directives["secret"]` is non-nil.
29. Given process with `storeDir '/data/cache'`, when parsed,
    then `Directives["storeDir"]` is non-nil.
30. Given process with `shell '/bin/bash', '-euo', 'pipefail'`
    (directive context, no colon), when parsed, then
    `Directives["shell"]` is non-nil and `Shell` (section
    field) is empty.

---

## B. `each` Input Qualifier

### B1: Parse `each` input declarations

As a developer, I want `each val(x)` and `each path(p)` parsed
in process input sections, so that processes with cross-product
inputs parse correctly.

The `each` keyword precedes a qualifier. Set
`Declaration.Each = true`. Bare `each x` (no qualifier) is
equivalent to `each val(x)`.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given process with `input: each val(x)`, when parsed, then
   `Input[0].Kind` == `"val"`, `Input[0].Name` == `"x"`, and
   `Input[0].Each` == true.
2. Given process with `input: each path(genome)`, when parsed,
   then `Input[0].Kind` == `"path"`, `Input[0].Name` ==
   `"genome"`, and `Input[0].Each` == true.
3. Given process with `input: val(id)\neach val(x)`, when parsed,
   then `Input[0].Each` == false and `Input[1].Each` == true.
4. Given process with `input: each x`, when parsed, then
   `Input[0].Kind` == `"val"`, `Input[0].Name` == `"x"`,
   `Input[0].Each` == true.

### B2: Translate `each` cross-product

As a developer, I want processes with `each` inputs to produce
N x M jobs (cross-product), so that every combination of regular
and each-channel items is executed.

At translate time, enumerate the Cartesian product of regular
input items and each-channel items. Each combination gets a
unique CWD: `{base}/nf-work/{runId}/{proc}/{itemIdx}_{eachIdx}`.
All resulting jobs share the same dep_grp for downstream wiring.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given process with `input: val(id)\neach val(mode)` where
   id channel has 2 items `["A","B"]` and mode channel has
   3 items `["x","y","z"]`, when translated, then 6 jobs are
   created (2 x 3) with distinct CWDs.
2. Given process with `input: val(id)\neach val(mode)` producing
   6 jobs, when inspecting job commands, then each job exports
   the correct combination (e.g. `id='A'` + `mode='x'`,
   `id='A'` + `mode='y'`, etc.).
3. Given process with no `each` inputs, when translated, then
   existing behaviour preserved (no cross-product).
4. Given process with `input: each val(m1)\neach val(m2)` where
   m1 has 2 items and m2 has 3 items and regular input has 1
   item, when translated, then 6 jobs (1 x 2 x 3).

---

## C. `eval` Output Qualifier

### C1: Parse and translate `eval` output

As a developer, I want `eval(command)` output declarations
translated by appending the command to the script and capturing
its stdout, so that processes using eval outputs work.

Translation: append `\n<varname>=$(<command>)` to the script
body. If downstream needs the value, it is available as a shell
variable. The eval output is non-dynamic (known at compile
time).

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

Parsing of `eval(command)` already exists (A5 from prior spec).

**Acceptance tests:**

1. Given process with `output: eval('hostname')`, when
   translated, then job `Cmd` contains a line capturing
   hostname output, e.g. `__nf_eval_0=$(hostname)`.
2. Given process with `output: eval('wc -l < input.txt')`, when
   translated, then job `Cmd` contains
   `__nf_eval_1=$(wc -l < input.txt)`.
3. Given process with both `path` and `eval` outputs, when
   translated, then both are handled and no error.

---

## D. Groovy Expression Extensions

### D1: Parse all missing operators

As a developer, I want all Groovy/Nextflow operators tokenised
and parsed, so that no expression causes a parse error.

Operators to add to the lexer/parser:
- `%` (modulo) -- binary, same precedence as `*`/`/`
- `**` (power) -- binary, higher than `*`
- `in` / `!in` -- binary keyword operators
- `instanceof` / `!instanceof` -- binary keyword operators
- `=~` (regex find), `==~` (regex match) -- binary
- `<=>` (spaceship) -- binary comparison
- `..` (inclusive range), `..<` (exclusive range) -- binary
- `<<` (left shift), `>>` (right shift), `>>>` (unsigned
  right shift) -- binary
- `&` (bitwise AND), `^` (bitwise XOR), `|` (bitwise OR)
  -- binary
- `~` (bitwise NOT) -- unary prefix
- `*.` (spread-dot) -- postfix on receiver

Parsing: all produce AST nodes (`BinaryExpr` for most,
`InExpr` for `in`/`!in`, `RegexExpr` for `=~`/`==~`,
`RangeExpr` for `..`/`..<`, `SpreadExpr` for `*.`,
`UnaryExpr` for `~`, `UnsupportedExpr` as fallback).

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `10 % 3`, when parsed, then `BinaryExpr` with
   `Op` == `"%"`.
2. Given `2 ** 10`, when parsed, then `BinaryExpr` with
   `Op` == `"**"`.
3. Given `x in ['a','b']`, when parsed, then `InExpr` with
   `Negated` == false.
4. Given `x !in [1,2]`, when parsed, then `InExpr` with
   `Negated` == true.
5. Given `x instanceof String`, when parsed, then no error
   (stored as `BinaryExpr` or `UnsupportedExpr`).
6. Given `name =~ /pattern/`, when parsed, then `RegexExpr`
   with `Full` == false.
7. Given `name ==~ /^[A-Z]+$/`, when parsed, then `RegexExpr`
   with `Full` == true.
8. Given `a <=> b`, when parsed, then `BinaryExpr` with
   `Op` == `"<=>"`.
9. Given `1..10`, when parsed, then `RangeExpr` with
   `Exclusive` == false.
10. Given `0..<5`, when parsed, then `RangeExpr` with
    `Exclusive` == true.
11. Given `x << 2`, when parsed, then `BinaryExpr` with
    `Op` == `"<<"`.
12. Given `x >> 1`, when parsed, then `BinaryExpr` with
    `Op` == `">>"`.
13. Given `x >>> 1`, when parsed, then `BinaryExpr` with
    `Op` == `">>>"`.
14. Given `a & b`, when parsed, then `BinaryExpr` with
    `Op` == `"&"`.
15. Given `a ^ b`, when parsed, then `BinaryExpr` with
    `Op` == `"^"`.
16. Given `a | b` in an expression context, when parsed, then
    `BinaryExpr` with `Op` == `"|"`.
17. Given `~x`, when parsed, then `UnaryExpr` with `Op` ==
    `"~"`.
18. Given `items*.name`, when parsed, then `SpreadExpr` with
    `Property` == `"name"`.

### D2: Parse missing expression features

As a developer, I want slashy strings, `new` constructors, and
multi-variable assignments parsed, so that files with these
constructs do not error.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `/foo\/bar/`, when parsed, then `SlashyStringExpr`
   with `Value` == `"foo/bar"`.
2. Given `new File('test.txt')`, when parsed, then `NewExpr`
   with `ClassName` == `"File"` and 1 arg.
3. Given `new Date()`, when parsed, then `NewExpr` with
   `ClassName` == `"Date"` and 0 args.
4. Given `def (x, y) = [1, 2]`, when parsed, then
   `MultiAssignExpr` with `Names` == `["x","y"]`.
5. Given `(a, b) = someList`, when parsed, then
   `MultiAssignExpr` with `Names` == `["a","b"]`.

### D3: Evaluate common operators

As a developer, I want `%`, `in`/`!in`, `=~`/`==~`, `..`/`..<`,
spread-dot, and `**` evaluated, so that directive expressions
using these work at translate time.

Operators that cannot be meaningfully evaluated (`<=>`, bitwise,
shift, `instanceof`) return `UnsupportedExpr` with a warning.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/groovy.go`
**Test file:** `nextflowdsl/groovy_test.go`

**Acceptance tests:**

1. Given `10 % 3`, when evaluated, then result is 1.
2. Given `2 ** 10`, when evaluated, then result is 1024.
3. Given `'a' in ['a','b','c']` with vars containing the list,
   when evaluated, then result is true.
4. Given `'d' in ['a','b','c']`, when evaluated, then result is
   false.
5. Given `'x' !in ['a','b']`, when evaluated, then result is
   true.
6. Given `'hello123' =~ /[0-9]+/`, when evaluated, then result
   is true (pattern found).
7. Given `'hello' =~ /^[0-9]+$/`, when evaluated, then result is
   false.
8. Given `'12345' ==~ /^[0-9]+$/`, when evaluated, then result
   is true (full match).
9. Given `'abc123' ==~ /^[0-9]+$/`, when evaluated, then result
   is false.
10. Given `1..5`, when evaluated, then result is `[1,2,3,4,5]`.
11. Given `0..<3`, when evaluated, then result is `[0,1,2]`.
12. Given `items*.name` with vars
    `{"items": [{"name":"a"},{"name":"b"}]}`, when evaluated,
    then result is `["a","b"]`.
13. Given `2 ** 0`, when evaluated, then result is 1.
14. Given `10 % 0`, when evaluated, then error mentions division
    by zero.

### D4: Evaluate additional string/list methods

As a developer, I want `matches`, `replaceAll`, `tokenize`,
`findAll`, `find`, `any`, `every`, `join`, `unique`, `sort`,
`plus`, `minus`, `take`, `drop`, and `multiply` methods
evaluated, so that common Groovy patterns work in directives.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/groovy.go`
**Test file:** `nextflowdsl/groovy_test.go`

**Acceptance tests:**

1. Given `'hello123'.replaceAll('[0-9]', '')`, when evaluated,
   then result is `"hello"`.
2. Given `'hello'.matches('[a-z]+')`, when evaluated, then
   result is true.
3. Given `'a b c'.tokenize(' ')`, when evaluated, then result is
   `["a","b","c"]`.
4. Given `[1,2,3,2,1].unique()`, when evaluated, then result is
   `[1,2,3]`.
5. Given `[3,1,2].sort()`, when evaluated, then result is
   `[1,2,3]`.
6. Given `[1,2].plus([3,4])`, when evaluated, then result is
   `[1,2,3,4]`.
7. Given `[1,2,3].minus([2])`, when evaluated, then result is
   `[1,3]`.
8. Given `['a','b','c'].join('-')`, when evaluated, then result
   is `"a-b-c"`.
9. Given `[1,2,3].any { it > 2 }`, when evaluated, then result
   is true.
10. Given `[1,2,3].every { it > 0 }`, when evaluated, then
    result is true.
11. Given `[1,2,3].findAll { it > 1 }`, when evaluated, then
    result is `[2,3]`.
12. Given `[1,2,3].find { it > 1 }`, when evaluated, then
    result is 2.
13. Given `'abc'.multiply(3)`, when evaluated, then result is
    `"abcabcabc"`.
14. Given `[1,2,3].take(2)`, when evaluated, then result is
    `[1,2]`.
15. Given `[1,2,3].drop(1)`, when evaluated, then result is
    `[2,3]`.

---

## E. Statement Types

### E1: Parse skippable statement types

As a developer, I want `assert`, `throw`, `try/catch/finally`,
`return`, `for (x in coll)`, `while`, and `switch/case`
parsed in process scripts, function bodies, and closures, so
that pipelines containing these do not error.

Parsed as AST statement nodes. In process script/function
bodies (which are stored as raw text), these are already
captured. In Groovy expression contexts (closures, workflow
body helper functions), the parser must skip them gracefully.

For workflow bodies and function definitions, the parser already
captures raw body text. The key requirement is that the brace-
matching logic handles these keywords without tripping up.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given function body `def check(x) { assert x > 0 : 'must be
   positive'\nreturn x }`, when parsed, then `Functions[0]`
   parsed without error.
2. Given function body `def safe(x) { try { risky(x) } catch
   (Exception e) { log.warn(e) } }`, when parsed, then no
   error.
3. Given function body `def loop(items) { for (x in items) {
   println x } }`, when parsed, then no error.
4. Given function body `def wait(n) { while (n > 0) { n-- } }`,
   when parsed, then no error.
5. Given function body with `switch (x) { case 1: 'one'; break;
   case 2: 'two'; break; default: 'other' }`, when parsed, then
   no error.
6. Given function body with `throw new RuntimeException('fail')`,
   when parsed, then no error.
7. Given function body with `return 42`, when parsed, then no
   error.
8. Given process `script:` section containing `for (x in
   items) { ... }`, when parsed, then `Script` contains the raw
   text and no error.
9. Given closure `{ item -> if (item == null) return null;
   item.trim() }`, when parsed, then no error.

---

## F. `params {}` Block Syntax

### F1: Parse `params {}` block

As a developer, I want `params { input: Path; save: Boolean =
false }` block syntax parsed, so that strict-mode Nextflow
parameter declarations work.

Type annotations stored as strings in `ParamDecl.Type`. Default
values parsed as `Expr`. Last-seen-wins: if both `params {}`
and `params.x = y` exist, whichever appears last wins.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `params { input = '/data' }`, when parsed, then
   `ParamBlock` has 1 entry with `Name` == `"input"`,
   `Default` evaluating to `"/data"`, and `Type` == `""`.
2. Given `params { input: Path; save: Boolean = false }`, when
   parsed, then `ParamBlock` has 2 entries: `input` with
   `Type` == `"Path"` and no default, `save` with
   `Type` == `"Boolean"` and `Default` evaluating to false.
3. Given `params.x = 1\nparams { x = 2 }`, when merged, then
   final value of `x` is 2 (block after legacy overrides).
4. Given `params { x = 1 }\nparams.x = 2`, when merged, then
   final value of `x` is 2 (legacy after block overrides).
5. Given `params { }` (empty block), when parsed, then
   `ParamBlock` is empty and no error.
6. Given `params { nested { x = 1 } }`, when parsed, then
   param key is `"nested.x"` with value 1.

---

## G. Enum and Record Types

### G1: Parse enum definitions

As a developer, I want `enum Day { MONDAY, TUESDAY, ... }`
parsed and stored, so that pipelines using typed enums do not
error.

Enum values referenced as `Day.MONDAY` should resolve to the
string `"MONDAY"` in expression evaluation.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `enum Day { MONDAY, TUESDAY, WEDNESDAY }`, when parsed,
   then `Enums[0].Name` == `"Day"` and `Enums[0].Values` ==
   `["MONDAY","TUESDAY","WEDNESDAY"]`.
2. Given enum followed by a process, when parsed, then both enum
   and process are in the AST.
3. Given expression `Day.MONDAY` with enum `Day` defined, when
   evaluated, then result is `"MONDAY"`.
4. Given `enum Empty { }`, when parsed, then `Enums[0].Values`
   is empty and no error.

### G2: Parse record definitions

As a developer, I want `record FastqPair { ... }` parsed and
stored, so that pipelines using typed records do not error.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `record FastqPair { id: String; fastq_1: Path }`, when
   parsed, then `Records[0].Name` == `"FastqPair"` and
   `Records[0].Fields` has 2 entries.
2. Given record with default value `record Cfg { threads: Integer
   = 4 }`, when parsed, then `Fields[0].Default` evaluates to 4.
3. Given record followed by a process, when parsed, then both are
   in the AST.

---

## H. Output Block

### H1: Parse top-level output block properly

As a developer, I want the top-level `output { ... }` block
parsed and its raw body stored (not just skipped), so that the
AST captures the output definition.

The existing parser already skips `output {}` blocks (A7 from
prior spec). Extend to store the raw body text in
`Workflow.OutputBlock`.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `output { samples { path 'fastq' } }`, when parsed,
   then `OutputBlock` contains `"samples { path 'fastq' }"` (or
   similar raw capture) and no error.
2. Given `output { samples { path 'fastq'; index { path
   'index.csv' } } }`, when parsed, then `OutputBlock` is
   non-empty and no error.
3. Given no `output` block, when parsed, then `OutputBlock` is
   empty.

---

## I. Workflow Block Sections

### I1: Parse `publish:` statement content

As a developer, I want `publish:` section content parsed as
`WFPublish` entries, so that publish wiring is captured.

Currently `publish:` lines are read and discarded. Store them
as `WFPublish` entries on `WorkflowBlock`.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given workflow with `publish:\nmessages = messages`, when
   parsed, then `Publish` has 1 entry with `Target` ==
   `"messages"` and `Source` == `"messages"`.
2. Given workflow with `publish:\na = ch_a\nb = ch_b`, when
   parsed, then `Publish` has 2 entries with `Target`/`Source`
   pairs `("a","ch_a")` and `("b","ch_b")`.
3. Given workflow with no `publish:` section, when parsed, then
   `Publish` is empty.

### I2: Parse `onComplete:` and `onError:` in workflow blocks

As a developer, I want `onComplete:` and `onError:` sections
in workflow blocks parsed and stored, so that pipelines using
lifecycle hooks inside workflow blocks do not error.

These are distinct from the top-level `workflow.onComplete {}`
handlers (already handled). Inside a named workflow block:
```groovy
workflow FOO {
    main: ...
    onComplete: println 'done'
    onError: println 'failed'
}
```

Store raw body text. Emit warning that wr does not support
lifecycle hooks.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given workflow with `onComplete:\nprintln 'done'`, when
   parsed, then `OnComplete` is non-empty and no error.
2. Given workflow with `onError:\nprintln 'failed'`, when
   parsed, then `OnError` is non-empty and no error.
3. Given workflow with `main:` and `onComplete:` sections, when
   parsed, then both `Calls` and `OnComplete` are populated.
4. Given workflow with no lifecycle sections, when parsed, then
   `OnComplete` and `OnError` are empty.

---

## J. Pipe Operator

### J1: Verify pipe operator support

As a developer, I want `channel | foo | bar` pipe syntax to
parse and desugar correctly.

The pipe operator is already implemented via `PipeExpr` and
`desugarWorkflowPipe`. Verify it handles the full pattern
`channel.of(1,2,3) | foo | bar | view`.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `workflow { Channel.of(1,2,3) | foo | bar }`, when
   parsed, then `Calls` has 2 entries: `foo` then `bar`, with
   `bar`'s input being `foo`'s output.
2. Given `workflow { reads | ALIGN | SORT | view }`, when
   parsed, then `Calls` has entries for `ALIGN`, `SORT`; `view`
   is handled as a terminal no-op.
3. Given `workflow { ch | process_a }`, when parsed, then 1 call
   to `process_a` with `ch` as input.

---

## K. Variable Assignments in Workflow Main

### K1: Track channel-valued assignments

As a developer, I want variable assignments in workflow `main:`
whose RHS is a channel expression or process output tracked as
named channels, so that downstream references resolve correctly.

This extends the existing `parseChannelAssignment` to cover
patterns like:
- `ch_input = Channel.fromPath(params.input)`
- `filtered = ch_input.filter { it.size() > 0 }`
- `result = PROCESS_A.out.bam`

Disambiguation: RHS contains `Channel.*`, `*.out`,
`*.out.*`, or a known channel variable. Plain assignments
(`x = 42`) silently ignored for wiring.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

The existing implementation already handles `ident = chanExpr`.
Verify it works for the above patterns.

**Acceptance tests:**

1. Given workflow `main:\nch = Channel.fromPath('/data/*.fq')
   \nFOO(ch)`, when parsed, then `FOO`'s arg resolves to the
   `Channel.fromPath` factory.
2. Given workflow `main:\nresult = ALIGN.out.bam\nSORT(result)`,
   when parsed, then `SORT`'s arg references `ALIGN.out.bam`.
3. Given workflow `main:\nfiltered = ch.filter { it > 0 }
   \nPROC(filtered)`, when parsed, then `PROC`'s arg resolves to
   the filtered channel chain.
4. Given workflow `main:\nx = 42\nFOO(ch)`, when parsed, then
   `x = 42` is silently ignored and `FOO` call parsed correctly.
5. Given `include { FOO } from 'plugin/nf-hello'`, when parsed,
   then import parsed with `Source` == `"plugin/nf-hello"` and
   a warning emitted (JVM plugins not supported).

---

## L. Channel Operator Implementations

### L1: Implement cardinality-changing operators

As a developer, I want `combine`, `concat`, `flatten`,
`transpose`, `unique`, `distinct`, `ifEmpty`, `toList`,
`toSortedList`, `count`, and `reduce` properly implemented in
channel resolution, so that downstream job counts are correct.

Move these from `unsupportedCardinalityOperators` to real
implementations in `applyChannelOperator`.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/channel.go`
**Test file:** `nextflowdsl/channel_test.go`

**Acceptance tests:**

1. Given `ch1` (2 items) `.combine(ch2)` (3 items), when
   resolved, then result has 6 items (Cartesian product).
2. Given `ch1` (2 items) `.concat(ch2)` (3 items), when
   resolved, then result has 5 items in order: ch1 first, then
   ch2.
3. Given `Channel.of([1,[2,3]],[4,5]).flatten()`, when resolved,
   then result has 5 items: 1, 2, 3, 4, 5.
4. Given `Channel.of([1,'a'],[1,'b'],[2,'c']).unique()`, when
   resolved, then result has 3 items (all unique).
5. Given `Channel.of(1,2,1,3,2).unique()`, when resolved, then
   result has 3 items: 1, 2, 3.
6. Given `Channel.of(1,1,2,2,3,1,1).distinct()`, when resolved,
   then result has 4 items: 1, 2, 3, 1 (consecutive dedup).
7. Given `Channel.empty().ifEmpty('default')`, when resolved,
   then result has 1 item with value `"default"`.
8. Given `Channel.of(1,2).ifEmpty('default')`, when resolved,
   then result has 2 items (original, not default).
9. Given `Channel.of(3,1,2).toList()`, when resolved, then
   result has 1 item: list `[3,1,2]`.
10. Given `Channel.of(3,1,2).toSortedList()`, when resolved,
    then result has 1 item: list `[1,2,3]`.
11. Given `Channel.of(1,2,3).count()`, when resolved, then
    result has 1 item with value 3.
12. Given `Channel.of(1,2,3).reduce { a, v -> a + v }`, when
    resolved, then result has 1 item with value 6.
13. Given `Channel.of('1.5','2.5').toFloat()`, when parsed and
    resolved, then no error.
14. Given `Channel.of('100','200').toLong()`, when parsed and
    resolved, then no error and items pass through.
15. Given `Channel.of('1.5','2.5').toDouble()`, when parsed and
    resolved, then no error and items pass through.
16. Given `Channel.of([1,['a','b']],[2,['c']]).transpose()`,
    when resolved, then result has 3 items: `[1,'a']`,
    `[1,'b']`, `[2,'c']`.
17. Given `Channel.of([1,'a'],[2,'b']).combine(
    Channel.of([1,'x'],[1,'y'],[2,'z']), by: 0)`, when
    resolved, then result has 3 items: `[1,'a','x']`,
    `[1,'a','y']`, `[2,'b','z']`.

### L2: Data-dependent operators as TranslatePending

As a developer, I want `splitCsv`, `splitJson`, `splitText`,
`splitFasta`, `splitFastq`, `collectFile`, `branch`, and
`multiMap` to produce TranslatePending when cardinality is
unknown, so that translation does not crash.

These operators pass through items at compile time with a
warning. At runtime, `TranslatePending` resolves actual items.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/channel.go`
**Test file:** `nextflowdsl/channel_test.go`

**Acceptance tests:**

1. Given `ch.splitCsv(header: true)`, when resolved, then items
   pass through unchanged and a warning is emitted.
2. Given `ch.splitFasta(by: 1)`, when resolved, then items pass
   through unchanged and a warning is emitted.
3. Given `ch.branch { small: it < 10; big: it >= 10 }`, when
   resolved, then items pass through (all to single channel)
   and a warning is emitted.
4. Given `ch.collectFile(name: 'out.txt')`, when resolved, then
   items pass through and a warning is emitted.
5. Given `ch.multiMap { it -> foo: it; bar: it }`, when
   resolved, then items pass through and a warning is emitted.
6. Given `ch.splitJson(path: 'data')`, when resolved, then
   items pass through unchanged and a warning is emitted.
7. Given `ch.splitText(by: 10)`, when resolved, then items
   pass through unchanged and a warning is emitted.
8. Given `ch.splitFastq(by: 1, pe: true)`, when resolved,
   then items pass through unchanged and a warning is emitted.

---

## M. Config Scope Handling

### M1: Parse `executor {}` scope with key extraction

As a developer, I want the `executor {}` config scope parsed
with key-value extraction, so that executor type, queue, and
resource limits are available for translation.

Currently `executor` is in `skippedTopLevelConfigScopes` and
its content is discarded. Instead, parse to
`Config.Executor` as `map[string]any`.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/config.go`
**Test file:** `nextflowdsl/config_test.go`

**Acceptance tests:**

1. Given config `executor { name = 'slurm'; queueSize = 100 }`,
   when parsed, then `Config.Executor["name"]` == `"slurm"` and
   `Config.Executor["queueSize"]` == 100.
2. Given config `executor { queue = 'long' }`, when parsed, then
   `Config.Executor["queue"]` == `"long"`.
3. Given config with no `executor {}` scope, when parsed, then
   `Config.Executor` is nil or empty.
4. Given config `executor { name = 'local' }` inside a profile
   `profiles { hpc { executor { name = 'slurm' } } }`, when
   parsed with profile `hpc`, then executor name is `"slurm"`.

### M2: Parse remaining config scopes

As a developer, I want all standard config scopes from the
Nextflow reference parsed into raw `map[string]any` storage
(or cleanly skipped without error), so that pipelines using
any scope do not cause parse errors.

Scopes: `conda`, `dag`, `manifest`, `notification`, `report`,
`timeline`, `tower`, `trace`, `wave`, `weblog`. All stored
as raw key-value maps or skipped cleanly.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/config.go`
**Test file:** `nextflowdsl/config_test.go`

**Acceptance tests:**

1. Given config `wave { enabled = true }\nparams { x = 1 }`,
   when parsed, then `Config.Params["x"]` == 1 and no error.
2. Given config `tower { enabled = true }`, when parsed, then
   no error.
3. Given config `conda { enabled = true }`, when parsed, then
   no error.
4. Given config `dag { overwrite = true }`, when parsed, then
   no error.
5. Given config `manifest { name = 'my-pipeline' }`, when
   parsed, then no error.
6. Given config `notification { enabled = true }`, when parsed,
   then no error.
7. Given config `report { enabled = true }`, when parsed, then
   no error.
8. Given config `timeline { enabled = true }`, when parsed,
   then no error.
9. Given config `trace { enabled = true }`, when parsed, then
   no error.
10. Given config `weblog { url = 'http://example.com' }`, when
    parsed, then no error.
11. Given config with all scopes `conda {}\ndag {}\nmanifest {}
    \nnotification {}\nreport {}\ntimeline {}\ntower {}
    \ntrace {}\nwave {}\nweblog {}`, when parsed, then no
    error.

---

## N. Dynamic Directives

### N1: Evaluate dynamic directive closures

As a developer, I want directive closures like
`memory { 2.GB * task.attempt }` evaluated with `task.*`
defaults at translate time, so that resource requests are
computed correctly.

The existing `resolveDirectiveInt` already evaluates expressions
with `task.attempt = 1` and other task defaults. Extend to
handle closure-wrapped directives: detect `ClosureExpr` in
directive value, unwrap, and evaluate the body with task vars.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given process with `memory { 2048 * task.attempt }`, when
   translated with default `task.attempt = 1`, then
   `Requirements.RAM` == 2048.
2. Given process with `cpus { params.cpus ?: 4 }` and params
   `{}` (no cpus param), when translated, then
   `Requirements.Cores` == 4.
3. Given process with `errorStrategy { task.exitStatus in
   [137,140] ? 'retry' : 'terminate' }`, when translated, then
   `ErrorStrat` resolves to a string (either `"retry"` or
   `"terminate"` depending on evaluation with defaults).
4. Given process with non-closure directive `cpus 4`, when
   translated, then existing behaviour preserved.
5. Given process with `container { "image:${task.attempt}" }`,
   when translated, then container resolves to `"image:1"`
   (default `task.attempt = 1`).

---

## O. `each` Cross-Product Translation Details

### O1: Cross-product CWD and dep_grp structure

As a developer, I want each cross-product job to have a unique
CWD so that file outputs do not collide, and all jobs to share
the same dep_grp for downstream dependency wiring.

CWD pattern:
`{base}/nf-work/{runId}/{process}/{regIdx}_{eachIdx}`

Where `regIdx` is the regular-input item index and `eachIdx` is
the each-channel item index.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given process FOO with `input: val(id)\neach val(mode)` where
   id has items `["A","B"]` and mode has `["x","y"]`, when
   translated, then 4 jobs with CWDs ending in `FOO/0_0`,
   `FOO/0_1`, `FOO/1_0`, `FOO/1_1`.
2. Given the same setup, all 4 jobs share the same dep_grp
   `nf.<runId>.FOO`.
3. Given process with `each` and downstream process BAR, when
   translated, then BAR depends on all FOO jobs' dep_grp.

---

## Implementation Order

### Phase 1: Groovy Expression Parsing (D1, D2)

Parse all missing operators and expression features. Foundation
for all subsequent evaluation work. No external dependencies.

### Phase 2: Groovy Evaluation (D3, D4)

Evaluate `%`, `**`, `in`/`!in`, `=~`/`==~`, ranges,
spread-dot, and additional methods. Depends on Phase 1.

### Phase 3: Process Directive Parsing (A1)

Add all missing directives to accepted set. Independent of
Phases 1-2 (directives are stored as expressions, not evaluated
at parse time).

### Phase 4: Input/Output Parsing (B1, C1 parse, E1, F1, G1, G2)

B1 (`each` parsing), E1 (statement types), F1 (`params {}`
block), G1 (enum), G2 (record). Can be parallel within phase.
Depends on Phase 1 for expression parsing.

### Phase 5: Workflow Block Sections (H1, I1, I2, J1, K1)

H1 (output block storage), I1 (publish), I2
(onComplete/onError), J1 (pipe verification), K1 (variable
assignment tracking). Mostly verification of existing behaviour
plus minor extensions. Can be parallel.

### Phase 6: Channel Operator Implementation (L1, L2)

Implement real resolution for cardinality-changing operators.
Depends on Phase 2 for closure evaluation in `reduce`.

### Phase 7: Config Extensions (M1, M2)

Parse `executor {}` with extraction, add remaining scopes
(`conda`, `dag`, `manifest`, `notification`, `report`,
`timeline`, `tower`, `trace`, `wave`, `weblog`). No
dependencies on other phases.

### Phase 8: Translation (B2, C1 translate, N1, O1)

B2 (`each` cross-product), C1 (`eval` output translation), N1
(dynamic directive closures), O1 (cross-product CWD). Depends on
Phases 4 and 6.

---

## Appendix: Key Decisions

1. **Parse-first strategy maintained.** All 15 gaps prioritise
   parse acceptance. Unknown operators/directives/statements
   produce warnings, never parse errors.
2. **`each` flag on Declaration.** Simpler than a separate
   `InputDecl` type. Cross-product computed at translate time,
   not deferred via TranslatePending.
3. **New expression nodes.** `InExpr`, `RegexExpr`, `RangeExpr`,
   `SpreadExpr`, `NewExpr`, `SlashyStringExpr`,
   `MultiAssignExpr` added to AST. Other operators use existing
   `BinaryExpr`/`UnaryExpr`/`UnsupportedExpr`.
4. **Statement types as raw body.** Process scripts and function
   bodies already stored as raw text. Statement-level parsing
   only needed for closure/expression contexts.
5. **`params {}` last-seen-wins.** Consistent with Nextflow
   override semantics. No type validation at translate time.
6. **Enum values as strings.** `Day.MONDAY` evaluates to
   `"MONDAY"`. Sufficient for equality checks in directives.
7. **`executor {}` extracted, other scopes remain skipped.**
   `executor` has translation-relevant keys; others are metadata.
8. **Dynamic directive closures.** Detect `ClosureExpr` in
   directive value, unwrap body, evaluate with task defaults.
   Existing `resolveDirectiveInt` infrastructure handles this.
   Parse-time change: `errorStrategy` and `container` must
   accept any `Expr`, not just `StringExpr`, falling back to
   `Directives[name]` for non-string expressions. `tag`
   already accepts any expression via `parseDirectiveText`.
   `label` remains `StringExpr`-only (always a literal config
   selector in Nextflow).
9. **Channel operator promotions.** `combine`, `concat`,
   `flatten`, `transpose`, `unique`, `distinct`, `ifEmpty`,
   `toList`, `toSortedList`, `count`, `reduce` move from
   warning-only to real implementations. `branch`, `multiMap`,
   `splitCsv`, etc. remain passthrough with warnings.
10. **Backward compatibility.** All existing tests must pass.
    New directives go into the existing `Directives` map. No
    changes to `Process` struct layout for existing fields.
11. **Testing.** GoConvey for all tests per go-conventions.
    Synthetic minimal test cases, not real pipeline snippets.
12. **Config scopes.** `conda`, `dag`, `manifest`,
    `notification`, `report`, `timeline`, `tower`, `trace`,
    `wave`, `weblog` added to `skippedTopLevelConfigScopes`
    for compatibility with all standard Nextflow config scopes.
13. **`eval` output.** Append shell capture to script body.
    Non-dynamic output -- command text known at compile time.
14. **Output block.** Store raw body text in
    `Workflow.OutputBlock`. Full structural parsing deferred to
    a future spec if needed.
15. **Cross-product CWD.** `{regIdx}_{eachIdx}` suffix ensures
    unique working directories per combination.
