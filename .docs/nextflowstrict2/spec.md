# Nextflow DSL2 Parser Completeness Specification

## Overview

Fills the remaining 12 gaps between the `nextflowdsl` parser and
full Nextflow DSL2 compatibility. Adds process selectors in config
(`withLabel`/`withName`), missing channel operators and factories,
Groovy expression extensions (ternary, elvis, comparisons, logical
ops, method calls, list/map literals), conditional workflow logic,
additional process directives, config `env` scope parsing, closure
parameter handling, workflow lifecycle blocks, and deprecation
compatibility.

All changes are in the existing `nextflowdsl/` and `cmd/`
packages. No new packages.

## Architecture

### AST changes (ast.go)

New expression nodes:

```go
// TernaryExpr stores a ternary (cond ? a : b) or
// elvis (a ?: b) expression.
type TernaryExpr struct {
    Cond  Expr // nil for elvis (a ?: b)
    True  Expr
    False Expr
}
func (TernaryExpr) expr() {}

// UnaryExpr stores a unary operator (e.g. !x, -x).
type UnaryExpr struct {
    Op      string
    Operand Expr
}
func (UnaryExpr) expr() {}

// MethodCallExpr stores a method call on a receiver.
type MethodCallExpr struct {
    Receiver Expr
    Method   string
    Args     []Expr
}
func (MethodCallExpr) expr() {}

// IndexExpr stores subscript access (list[0], map['key']).
type IndexExpr struct {
    Receiver Expr
    Index    Expr
}
func (IndexExpr) expr() {}

// ListExpr stores a list literal [a, b, c].
type ListExpr struct {
    Elements []Expr
}
func (ListExpr) expr() {}

// MapExpr stores a map literal [key: val, ...].
type MapExpr struct {
    Keys   []Expr
    Values []Expr
}
func (MapExpr) expr() {}

// NullExpr stores a null literal.
type NullExpr struct{}
func (NullExpr) expr() {}

// NullSafeExpr stores a null-safe navigation (x?.prop).
type NullSafeExpr struct {
    Receiver Expr
    Property string
}
func (NullSafeExpr) expr() {}

// CastExpr stores an `as` type cast (x as Integer).
type CastExpr struct {
    Operand  Expr
    TypeName string
}
func (CastExpr) expr() {}

// ClosureExpr stores a parsed closure with parameters.
type ClosureExpr struct {
    Params []string // parameter names before ->
    Body   string   // raw body text after ->
}
func (ClosureExpr) expr() {}
```

`Process` gains fields:

```go
type Process struct {
    // ... existing fields ...
    Labels        []string       // label directives
    Tag           string         // tag directive expression text
    BeforeScript  string         // beforeScript directive
    AfterScript   string         // afterScript directive
    Module        string         // module directive
    Cache         string         // cache directive value
    Directives    map[string]any // scratch, storeDir, queue, etc.
}
```

`WorkflowBlock` gains conditional support:

```go
// IfBlock represents an if/else if/else in a workflow block.
type IfBlock struct {
    Condition string       // raw condition expression
    Body      []*Call      // calls in the if-true branch
    ElseIf    []*IfBlock   // else-if branches
    ElseBody  []*Call      // calls in the else branch
}

type WorkflowBlock struct {
    Calls      []*Call
    Take       []string
    Emit       []*WFEmit
    Conditions []*IfBlock // if/else blocks in workflow
}
```

### Config changes (config.go)

New types for process selectors:

```go
// ProcessSelector holds directive overrides for a
// withLabel or withName selector.
type ProcessSelector struct {
    Kind     string           // "withLabel" or "withName"
    Pattern  string           // glob or ~regex pattern
    Settings *ProcessDefaults // directive overrides
}

// Config gains fields:
type Config struct {
    Params    map[string]any
    Profiles  map[string]*Profile
    Process   *ProcessDefaults
    Selectors []*ProcessSelector // withLabel/withName
    Env       map[string]string  // top-level env scope
}
```

### Groovy evaluator changes (groovy.go)

Extend `EvalExpr` to handle: `TernaryExpr`, `UnaryExpr`,
`MethodCallExpr`, `IndexExpr`, `ListExpr`, `MapExpr`,
`NullExpr`, `NullSafeExpr`, `CastExpr`,
`BinaryExpr` with `==`, `!=`, `>=`, `<=`, `&&`, `||`.

String method dispatch: `trim`, `size`, `toInteger`,
`toLowerCase`, `toUpperCase`, `contains`, `startsWith`,
`endsWith`, `replace`, `split`, `substring`.

List method dispatch: `size`, `isEmpty`, `first`, `last`,
`flatten`, `collect` (closure-aware, depends on H2).

### Translation changes (translate.go)

- `buildRequirements` gains selector matching: resolve
  `ProcessDefaults` by merging generic < `withLabel` <
  `withName` < process-level directives.
- `buildCommand` prepends `BeforeScript` and appends
  `AfterScript` around the process script.
- `tag` stored on process but not used in RepGroup.
- If/else blocks: evaluate condition against params; emit
  matching branch only. Unevaluable conditions emit both
  branches with distinct dep_grps.

### Channel changes (channel.go)

New factory handlers: `fromList`, `from` (deprecated alias
for `of`).

New operator entries in `supportedChannelOperators`: `cross`,
`splitJson`, `splitText`, `buffer`, `collate`, `until`,
`subscribe`, `sum`, `min`, `max`, `randomSample`, `merge`,
`toInteger`, `countFasta`, `countFastq`, `countJson`,
`countLines`.

Translation cardinality for new operators:
- `cross` -> N*M items (Cartesian product)
- `buffer(n)`/`collate(n)` -> ceil(N/n) items
- `min`/`max`/`sum` -> 1 item (reduction)
- Others -> passthrough with warning

---

## A. Process Selectors in Config

### A1: Parse label directive on processes

As a developer, I want the `label` directive parsed and stored
on processes, so that config selectors can match by label.

Multiple `label` directives are allowed per process (each adds
to the list). `label` values are string literals.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given process with `label 'big_mem'`, when parsed, then
   `Labels` == `["big_mem"]`.
2. Given process with `label 'big_mem'` and `label 'long_time'`,
   when parsed, then `Labels` == `["big_mem", "long_time"]`.
3. Given process with no `label` directive, when parsed, then
   `Labels` is empty.

### A2: Parse withLabel/withName selectors in config

As a developer, I want `withLabel` and `withName` selectors
parsed from config `process {}` blocks, so that directive
overrides can be matched to processes.

Config syntax:
```groovy
process {
    withLabel: 'big_mem' {
        cpus = 8
        memory = '64 GB'
    }
    withName: 'ALIGN' {
        cpus = 16
    }
}
```

Patterns support glob (`*`, `?`, `[abc]`) and regex with `~`
prefix (`~ALIGN.*`). Stored as `ProcessSelector` entries in
`Config.Selectors`.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/config.go`
**Test file:** `nextflowdsl/config_test.go`

**Acceptance tests:**

1. Given config `process { withLabel: 'big_mem' { cpus = 8 } }`,
   when parsed, then `Selectors` has 1 entry with `Kind` ==
   `"withLabel"`, `Pattern` == `"big_mem"`, `Settings.Cpus` ==
   8.
2. Given config `process { withName: 'ALIGN' { memory =
   '32 GB' } }`, when parsed, then `Selectors` has 1 entry
   with `Kind` == `"withName"`, `Pattern` == `"ALIGN"`,
   `Settings.Memory` == 32768.
3. Given config with 2 selectors `withLabel: 'small' { cpus =
   1 }` and `withLabel: 'big' { cpus = 16 }`, when parsed,
   then `Selectors` has 2 entries in declaration order.
4. Given config `process { withName: '~ALIGN.*' { cpus = 4 } }`,
   when parsed, then `Selectors[0].Pattern` == `"~ALIGN.*"`.
5. Given config `process { cpus = 2 ; withLabel: 'big' {
   cpus = 16 } }`, when parsed, then `Process.Cpus` == 2 and
   `Selectors[0].Settings.Cpus` == 16.
6. Given config with `withLabel:` containing `memory`, `time`,
   `container`, and `env` overrides, when parsed, then all
   fields are populated on the selector's `Settings`.

### A3: Apply selectors at translate time

As a developer, I want selectors matched against process
labels and names at translate time, so that the most specific
directive overrides are applied.

Specificity ordering (later overrides earlier):
1. Generic `process {}` defaults
2. `withLabel` match (last-match-wins among same-specificity)
3. `withName` match (last-match-wins among same-specificity)
4. Process-level directives (always win)

Glob matching uses `filepath.Match`. Regex patterns (prefixed
with `~`) use Go `regexp.MatchString`.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

```go
// MatchSelectors returns merged ProcessDefaults for a process
// by applying matching selectors in specificity order.
func MatchSelectors(
    proc *Process,
    base *ProcessDefaults,
    selectors []*ProcessSelector,
) *ProcessDefaults
```

**Acceptance tests:**

1. Given process `ALIGN` with `label 'big_mem'`, config generic
   `cpus = 1`, selector `withLabel: 'big_mem' { cpus = 8 }`,
   when translated, then job `Requirements.Cores` == 8.
2. Given process `ALIGN` with `label 'big_mem'`, config
   `withLabel: 'big_mem' { cpus = 8 }` and
   `withName: 'ALIGN' { cpus = 16 }`, when translated, then
   job `Requirements.Cores` == 16 (withName > withLabel).
3. Given process `ALIGN` with `label 'big_mem'` and process-level
   `cpus 32`, config `withName: 'ALIGN' { cpus = 16 }`, when
   translated, then job `Requirements.Cores` == 32 (process-
   level > withName).
4. Given process `ALIGN_BWA` with config
   `withName: 'ALIGN*' { cpus = 4 }` (glob), when translated,
   then job `Requirements.Cores` == 4.
5. Given process `ALIGN_BWA` with config
   `withName: '~ALIGN.*' { cpus = 4 }` (regex), when translated,
   then job `Requirements.Cores` == 4.
6. Given process `FOO` with label 'small', config
   `withLabel: 'big' { cpus = 16 }`, when translated, then
   generic defaults apply (no match).
7. Given 2 selectors `withLabel: 'big' { cpus = 8 }` then
   `withLabel: 'big' { cpus = 12 }`, process with label 'big',
   when translated, then `Requirements.Cores` == 12
   (last-match-wins).
8. Given selector `withLabel: 'big' { memory = '64 GB' }` and
   process with label 'big' and process-level `cpus 4` (no
   memory directive), when translated, then `Requirements.Cores`
   == 4 and `Requirements.RAM` == 65536 (merged: label provides
   memory, process provides cpus).

---

## B. Missing Channel Operators

### B1: Parse additional channel operators

As a developer, I want all missing Nextflow channel operators
parsed without error, so that pipelines using them do not fail.

Add to `supportedChannelOperators`: `cross`, `splitJson`,
`splitText`, `buffer`, `collate`, `until`, `subscribe`, `sum`,
`min`, `max`, `randomSample`, `merge`, `toInteger`,
`countFasta`, `countFastq`, `countJson`, `countLines`.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `ch.cross(other)`, when parsed, then operator is
   `"cross"` with 1 channel arg.
2. Given `ch.splitJson()`, when parsed, then operator is
   `"splitJson"`.
3. Given `ch.splitText(by: 1000)`, when parsed, then operator is
   `"splitText"`.
4. Given `ch.buffer(size: 3)`, when parsed, then operator is
   `"buffer"`.
5. Given `ch.collate(5)`, when parsed, then operator is
   `"collate"` with int arg 5.
6. Given `ch.until { it == 'DONE' }`, when parsed, then operator
   is `"until"` with closure.
7. Given `ch.subscribe { println it }`, when parsed, then
   operator is `"subscribe"` with closure.
8. Given `ch.sum()`, when parsed, then operator is `"sum"`.
9. Given `ch.min()`, when parsed, then operator is `"min"`.
10. Given `ch.max()`, when parsed, then operator is `"max"`.
11. Given `ch.randomSample(10)`, when parsed, then operator is
    `"randomSample"` with int arg 10.
12. Given `ch.merge(other)`, when parsed, then operator is
    `"merge"` with 1 channel arg.
13. Given `ch.toInteger()`, when parsed, then operator is
    `"toInteger"`.
14. Given `ch.countFasta()`, when parsed, then operator is
    `"countFasta"` and a deprecation warning is emitted.
15. Given `ch.countFastq()`, when parsed, then operator is
    `"countFastq"` and a deprecation warning is emitted.
16. Given `ch.countJson()`, when parsed, then operator is
    `"countJson"` and a deprecation warning is emitted.
17. Given `ch.countLines()`, when parsed, then operator is
    `"countLines"` and a deprecation warning is emitted.

### B2: Translate new operators with cardinality effects

As a developer, I want `cross`, `buffer`, `collate`, `min`,
`max`, and `sum` to affect job cardinality during translation,
so that downstream processes get the correct number of jobs.

- `cross(ch2)`: N items x M items -> N*M items
- `buffer(size: n)` / `collate(n)`: N items -> ceil(N/n) groups
- `min()` / `max()` / `sum()`: N items -> 1 item (reduction)
- `splitJson` / `splitText`: passthrough with warning (item
  expansion depends on file content)
- `until`, `subscribe`, `randomSample`, `merge`, `toInteger`:
  passthrough with warning

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/channel.go`
**Test file:** `nextflowdsl/channel_test.go`

**Acceptance tests:**

1. Given `ch1` (3 items) `.cross(ch2)` (2 items), when resolved,
   then result has 6 items (3*2 Cartesian product).
2. Given `ch` (10 items) `.buffer(size: 3)`, when resolved, then
   result has 4 items (ceil(10/3) groups).
3. Given `ch` (10 items) `.collate(5)`, when resolved, then
   result has 2 items.
4. Given `Channel.of(3,1,4,1,5)` `.min()`, when resolved, then
   result has 1 item with value 1.
5. Given `Channel.of(3,1,4,1,5)` `.max()`, when resolved, then
   result has 1 item with value 5.
6. Given `Channel.of(1,2,3)` `.sum()`, when resolved, then
   result has 1 item with value 6.
7. Given `ch` (5 items) `.splitJson()`, when resolved, then
   result has 5 items (passthrough) and a warning is emitted.
8. Given `ch` (5 items) `.merge(other)` (3 items), when
   resolved, then result has 5 items (passthrough) and a warning
   is emitted.

---

## C. Missing Channel Factories

### C1: Parse and resolve additional channel factories

As a developer, I want `Channel.fromList`, `Channel.from`,
`Channel.fromSRA`, `Channel.topic`, `Channel.watchPath`,
`Channel.fromLineage`, and `Channel.interval` parsed without
error, so that pipelines using them do not fail.

- `Channel.fromList(list)` -> same resolution as `Channel.of`
  (expand list items)
- `Channel.from(items)` -> same as `Channel.of` (deprecated
  alias, emit deprecation warning)
- Others -> warn as untranslatable, return empty channel

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`, `nextflowdsl/channel.go`
**Test file:** `nextflowdsl/parse_test.go`,
`nextflowdsl/channel_test.go`

**Acceptance tests:**

1. Given `Channel.fromList([1,2,3])`, when parsed, then factory
   is `"fromList"`.
2. Given `Channel.fromList([1,2,3])`, when resolved, then result
   has 3 items with values 1, 2, 3.
3. Given `Channel.from(1,2,3)`, when parsed, then factory is
   `"from"`.
4. Given `Channel.from(1,2,3)`, when resolved, then result has
   3 items with values 1, 2, 3 (deprecation warning tested
   in J1).
5. Given `Channel.fromSRA('SRR1234')`, when parsed, then factory
   is `"fromSRA"`.
6. Given `Channel.fromSRA('SRR1234')`, when resolved, then
   result is empty and a warning is emitted.
7. Given `Channel.topic('myTopic')`, when parsed, then factory
   is `"topic"`.
8. Given `Channel.watchPath('/data/*.fq')`, when parsed, then
   factory is `"watchPath"`.
9. Given `Channel.interval(100)`, when parsed, then factory is
   `"interval"`.
10. Given `Channel.fromLineage('query')`, when parsed, then
    factory is `"fromLineage"`.

---

## D. Groovy Expression Extensions

### D1: Ternary and elvis operators

As a developer, I want the Groovy evaluator to handle ternary
(`cond ? a : b`) and elvis (`a ?: b`) operators, so that
dynamic directive values are resolved.

Parsing: extend `parseExprTokens` to recognise `?` and `?:`.
Evaluation: `TernaryExpr` evaluates condition; if truthy
returns `True`, else `False`. Elvis: if `True` is truthy
returns `True`, else returns `False`.

Truthiness: `null` -> false, `0` -> false, `""` -> false,
`false` -> false, empty list/map -> false, all else -> true.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/groovy.go`, `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/groovy_test.go`,
`nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `task.attempt > 1 ? '16 GB' : '8 GB'` with vars
   `{"task":{"attempt":1}}`, when evaluated, then result is
   `"8 GB"`.
2. Given `task.attempt > 1 ? '16 GB' : '8 GB'` with vars
   `{"task":{"attempt":2}}`, when evaluated, then result is
   `"16 GB"`.
3. Given `x ?: 'default'` with vars `{"x":"hello"}`, when
   evaluated, then result is `"hello"`.
4. Given `x ?: 'default'` with vars `{"x":nil}`, when
   evaluated, then result is `"default"`.
5. Given `x ?: 'default'` with vars `{"x":""}`, when
   evaluated, then result is `"default"`.
6. Given `x ?: 'default'` with vars `{"x":0}`, when evaluated,
   then result is `"default"`.

### D2: Comparison and logical operators

As a developer, I want `==`, `!=`, `>=`, `<=`, `&&`, `||`, and
`!` operators evaluated, so that directive conditions work.

Extend `BinaryExpr` evaluation for `==`, `!=`, `>=`, `<=`,
`&&`, `||`. Add `UnaryExpr` evaluation for `!`.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/groovy.go`, `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/groovy_test.go`,
`nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `1 == 1`, when evaluated, then result is `true`.
2. Given `1 != 2`, when evaluated, then result is `true`.
3. Given `3 >= 3`, when evaluated, then result is `true`.
4. Given `2 <= 3`, when evaluated, then result is `true`.
5. Given `true && false`, when evaluated, then result is `false`.
6. Given `true || false`, when evaluated, then result is `true`.
7. Given `!true`, when evaluated, then result is `false`.
8. Given `!false`, when evaluated, then result is `true`.
9. Given `'hello' == 'hello'`, when evaluated, then result is
   `true`.
10. Given `'a' != 'b'`, when evaluated, then result is `true`.
11. Given `1 == 1 && 2 > 1`, when evaluated, then result is
    `true`.

### D3: Method calls on strings and lists

As a developer, I want common Groovy method calls evaluated,
so that directive expressions like `text.trim()` and
`list.size()` work.

String methods: `trim()`, `size()`, `toInteger()`,
`toLowerCase()`, `toUpperCase()`, `contains(s)`,
`startsWith(s)`, `endsWith(s)`, `replace(old,new)`,
`split(delim)`, `substring(start)`, `substring(start,end)`.

List methods: `size()`, `isEmpty()`, `first()`, `last()`,
`flatten()`, `collect(closure)` (depends on H2 closure
evaluation).

Method chaining: `text.trim().toLowerCase()`.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/groovy.go`, `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/groovy_test.go`,
`nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `'  hello  '.trim()`, when evaluated, then result is
   `"hello"`.
2. Given `'hello'.size()`, when evaluated, then result is 5.
3. Given `'42'.toInteger()`, when evaluated, then result is 42.
4. Given `'Hello'.toLowerCase()`, when evaluated, then result
   is `"hello"`.
5. Given `'hello'.toUpperCase()`, when evaluated, then result
   is `"HELLO"`.
6. Given `'hello world'.contains('world')`, when evaluated,
   then result is `true`.
7. Given `'hello'.startsWith('hel')`, when evaluated, then
   result is `true`.
8. Given `'hello'.endsWith('llo')`, when evaluated, then result
   is `true`.
9. Given `'hello'.replace('l', 'r')`, when evaluated, then
   result is `"herro"`.
10. Given `'a,b,c'.split(',')`, when evaluated, then result is
    `["a","b","c"]` (list of strings).
11. Given `'hello'.substring(1)`, when evaluated, then result is
    `"ello"`.
12. Given `'hello'.substring(1,3)`, when evaluated, then result
    is `"el"`.
13. Given `[1,2,3].size()`, when evaluated, then result is 3.
14. Given `[].isEmpty()`, when evaluated, then result is `true`.
15. Given `[1,2,3].first()`, when evaluated, then result is 1.
16. Given `[1,2,3].last()`, when evaluated, then result is 3.
17. Given `'hello'.trim().toUpperCase()`, when evaluated, then
    result is `"HELLO"`.
18. Given `[[1,2],[3,[4,5]]].flatten()`, when evaluated, then
    result is `[1, 2, 3, 4, 5]`.
19. Given `[1,2,3].collect { it * 2 }` with H2 closure
    evaluation available, when evaluated, then result is
    `[2, 4, 6]`.

### D4: List and map literals

As a developer, I want `[1, 2, 3]` and `[key: 'value']`
literals parsed and evaluated, so that directive arguments
using collections work.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/groovy.go`, `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/groovy_test.go`,
`nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `[1, 2, 3]`, when parsed and evaluated, then result is
   `[]any{1, 2, 3}`.
2. Given `['a', 'b']`, when parsed and evaluated, then result is
   `[]any{"a", "b"}`.
3. Given `[key: 'value']`, when parsed and evaluated, then result
   is `map[string]any{"key": "value"}`.
4. Given `[a: 1, b: 2]`, when parsed and evaluated, then result
   is `map[string]any{"a": 1, "b": 2}`.
5. Given `[]` (empty list), when parsed and evaluated, then result
   is `[]any{}`.
6. Given `[:]` (empty map), when parsed and evaluated, then result
   is `map[string]any{}`.
7. Given `list[0]` with vars `{"list": []any{10,20,30}}`, when
   evaluated, then result is 10.
8. Given `map['key']` with vars `{"map": map[string]any{
   "key":"val"}}`, when evaluated, then result is `"val"`.

### D5: Null literal and task.* references

As a developer, I want `null` literal and `task.attempt` (and
other `task.*` properties) evaluated, so that retry-aware
directive expressions work.

`task.attempt` defaults to 1 at translate time. `task.cpus`
resolves to the process's cpus directive value or default.
`task.memory` resolves similarly.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/groovy.go`
**Test file:** `nextflowdsl/groovy_test.go`

**Acceptance tests:**

1. Given `null`, when evaluated, then result is nil.
2. Given `null == null`, when evaluated, then result is `true`.
3. Given `task.attempt` with vars `{"task":{"attempt":1}}`, when
   evaluated, then result is 1.
4. Given `task.attempt * 2` with vars `{"task":{"attempt":3}}`,
   when evaluated, then result is 6.
5. Given `x?.property` with vars `{"x":nil}`, when evaluated,
   then result is nil (no error).
6. Given `x?.property` with vars `{"x":{"property":"val"}}`,
   when evaluated, then result is `"val"`.

### D6: Cast expressions

As a developer, I want `x as Integer` and similar cast
expressions parsed, so that pipelines using type casts do
not fail.

Supported casts: `as Integer` -> `.toInteger()`,
`as String` -> string coercion. Unknown casts produce
`UnsupportedExpr`.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/groovy.go`, `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/groovy_test.go`,
`nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `'42' as Integer`, when parsed and evaluated, then
   result is 42.
2. Given `42 as String`, when parsed and evaluated, then result
   is `"42"`.
3. Given `x as Integer` with vars `{"x":"10"}`, when evaluated,
   then result is 10.

---

## E. Conditional Logic in Workflow Blocks

### E1: Parse if/else in workflow blocks

As a developer, I want `if`/`else if`/`else` blocks parsed in
workflow blocks, so that conditional process invocation is
represented in the AST.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given workflow with `if (params.aligner == 'bwa') {
   BWA(reads) }`, when parsed, then
   `Conditions` has 1 entry with `Condition` containing
   `"params.aligner == 'bwa'"` and `Body` has 1 call to `BWA`.
2. Given workflow with `if (x) { A(ch) } else { B(ch) }`,
   when parsed, then `Conditions[0].Body` has call to `A` and
   `ElseBody` has call to `B`.
3. Given workflow with `if (x) { A(ch) } else if (y) { B(ch) }
   else { C(ch) }`, when parsed, then `Conditions[0]` has
   `ElseIf` with 1 entry for `B` and `ElseBody` with call to
   `C`.
4. Given workflow with no if/else, when parsed, then
   `Conditions` is empty.

### E2: Translate conditional workflow blocks

As a developer, I want if/else conditions evaluated against
resolved params at translate time, so that only the matching
branch produces jobs.

When the condition can be statically evaluated, only the
matching branch emits jobs. When evaluation fails, BOTH
branches emit jobs with a warning. Each branch uses a separate
CWD subtree (e.g. `{cwd}/nf-work/{runId}/if_0/{proc}` and
`{cwd}/nf-work/{runId}/else_0/{proc}`) so that outputs do not
conflict.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given workflow with `if (params.aligner == 'bwa') {
   BWA(reads) } else { BOWTIE(reads) }` and params
   `{"aligner":"bwa"}`, when translated, then jobs include
   `BWA` but not `BOWTIE`.
2. Given same workflow with params `{"aligner":"bowtie"}`, when
   translated, then jobs include `BOWTIE` but not `BWA`.
3. Given workflow with `if (complexExpr()) { A(ch) } else {
   B(ch) }` where condition cannot be evaluated, when
   translated, then jobs include BOTH `A` and `B`, and a
   warning is emitted. `A`'s Cwd contains `if_0` and `B`'s
   Cwd contains `else_0`.
4. Given workflow with no conditions, when translated, then
   existing behaviour is preserved.

---

## F. Additional Process Directives

### F1: Parse and store new directives

As a developer, I want `label`, `tag`, `cache`, `scratch`,
`storeDir`, `beforeScript`, `afterScript`, `module`, `queue`,
`clusterOptions`, `executor`, `debug`, and `secret` directives
parsed and stored, so that pipelines using them do not fail.

Directives that map to wr job fields:
- `tag`: stored on `Process.Tag`
- `beforeScript`: stored on `Process.BeforeScript`
- `afterScript`: stored on `Process.AfterScript`
- `module`: stored on `Process.Module`

Directives that are stored-and-warned (no wr mapping):
- `cache`: stored on `Process.Cache`, warn
- `scratch`: stored on `Process.Directives["scratch"]`, warn
- `storeDir`: stored on `Process.Directives["storeDir"]`, warn
  (deliberately stored-and-warned per Key Decision 16)
- `queue`, `clusterOptions`, `executor`, `debug`, `secret`:
  stored in `Process.Directives`, warn

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

Label directive parsing is tested in A1.

**Acceptance tests:**

1. Given process with `tag "sample_${id}"`, when parsed, then
   `Tag` == `"sample_${id}"`.
2. Given process with `beforeScript 'module load samtools'`,
   when parsed, then `BeforeScript` == `"module load samtools"`.
3. Given process with `afterScript 'cleanup.sh'`, when parsed,
   then `AfterScript` == `"cleanup.sh"`.
4. Given process with `module 'samtools/1.17'`, when parsed,
   then `Module` == `"samtools/1.17"`.
5. Given process with `cache 'lenient'`, when parsed, then
   `Cache` == `"lenient"`.
6. Given process with `scratch true`, when parsed, then
   `Directives["scratch"]` is non-nil (no error).
7. Given process with `storeDir '/cache/outputs'`, when parsed,
   then `Directives["storeDir"]` is non-nil.
8. Given process with `queue 'long'`, when parsed, then
   `Directives["queue"]` is non-nil.
9. Given process with `debug true`, when parsed, then
   `Directives["debug"]` is non-nil.
10. Given process with `clusterOptions '--mem=8G'`, when parsed,
    then `Directives["clusterOptions"]` is non-nil.
11. Given process with `executor 'slurm'`, when parsed, then
    `Directives["executor"]` is non-nil.
12. Given process with `secret 'MY_TOKEN'`, when parsed, then
    `Directives["secret"]` is non-nil.

### F2: Translate beforeScript/afterScript

As a developer, I want `beforeScript` and `afterScript`
translated by wrapping the process command, so that pre/post
scripts execute inside the same container context.

The final command structure:
`<beforeScript>\n<script>\n<afterScript>`

Container wrapping applies to the entire block as one unit.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given process with `beforeScript 'module load samtools'` and
   script `'samtools sort input.bam'`, when translated, then
   job `Cmd` contains `module load samtools` before
   `samtools sort input.bam`.
2. Given process with `afterScript 'cleanup.sh'` and script
   `'run.sh'`, when translated, then job `Cmd` contains
   `run.sh` before `cleanup.sh`.
3. Given process with both `beforeScript` and `afterScript`,
   when translated, then job `Cmd` has all three parts in
   order: before, script, after.
4. Given process with no `beforeScript`/`afterScript`, when
   translated, then existing behaviour is preserved.

### F3: Translate module directive

As a developer, I want the `module` directive translated by
prepending `module load <name>` to the command, so that HPC
environment modules are loaded before execution.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given process with `module 'samtools/1.17'`, when translated,
   then job `Cmd` starts with `module load samtools/1.17\n`.
2. Given process with `module 'samtools/1.17:bwa/0.7.17'`
   (colon-separated), when translated, then job `Cmd` starts
   with `module load samtools/1.17\nmodule load bwa/0.7.17\n`.
3. Given process with no `module` directive, when translated,
   then no `module load` prefix.

---

## G. Config Environment Scope

### G1: Parse env scope in config

As a developer, I want the `env {}` config scope parsed and
stored, so that environment variables are available at translate
time.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/config.go`
**Test file:** `nextflowdsl/config_test.go`

**Acceptance tests:**

1. Given config `env { FOO = 'bar' ; BAZ = 'qux' }`, when
   parsed, then `Config.Env["FOO"]` == `"bar"` and
   `Config.Env["BAZ"]` == `"qux"`.
2. Given config with no `env {}` scope, when parsed, then
   `Config.Env` is nil or empty.
3. Given config with `env { FOO = 'bar' }` and
   `params { x = 1 }`, when parsed, then both `Env` and
   `Params` are populated.

### G2: Merge config env into jobs

As a developer, I want config `env` scope variables merged
into every job's environment, so that global environment
settings apply to all processes.

Process-level `env` overrides config-level `env` for the same
key (more specific wins).

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given config `env { FOO = 'bar' }` and process with no env
   directive, when translated, then job `EnvOverride`
   (decompressed) contains `FOO=bar`.
2. Given config `env { FOO = 'global' }` and process with
   `env FOO: 'local'`, when translated, then job `EnvOverride`
   contains `FOO=local` (process-level wins).
3. Given config `env { A = '1' ; B = '2' }` and process with
   `env A: 'override'`, when translated, then job `EnvOverride`
   contains `A=override` and `B=2`.

### G3: Parse container scope settings

As a developer, I want `docker.enabled`, `singularity.enabled`,
and `apptainer.enabled` parsed from config, so that the
container runtime can be auto-detected when not specified via
CLI flag.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/config.go`
**Test file:** `nextflowdsl/config_test.go`

Add to `Config`:

```go
type Config struct {
    // ... existing fields ...
    ContainerEngine string // "docker", "singularity",
                           // "apptainer", or ""
}
```

**Acceptance tests:**

1. Given config `docker { enabled = true }`, when parsed, then
   `Config.ContainerEngine` == `"docker"`.
2. Given config `singularity { enabled = true }`, when parsed,
   then `Config.ContainerEngine` == `"singularity"`.
3. Given config `apptainer { enabled = true }`, when parsed,
   then `Config.ContainerEngine` == `"apptainer"`.
4. Given config with no container scope or all disabled, when
   parsed, then `Config.ContainerEngine` == `""`.
5. Given config `docker { enabled = true }` and `singularity {
   enabled = true }`, when parsed, then
   `Config.ContainerEngine` == `"singularity"` (last-wins).

---

## H. Closure Parameter Handling

### H1: Parse closure parameters

As a developer, I want closure parameters (before `->`) parsed
and stored, so that simple closures can be evaluated.

Extends existing closure capture to recognise `{ params -> body }`
syntax. Multi-parameter: `{ a, b -> body }`. The implicit `it`
parameter is inferred when no `->` is present.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `ch.map { item -> item.id }`, when parsed, then
   operator closure has `Params` == `["item"]` and `Body`
   containing `"item.id"`.
2. Given `ch.filter { a, b -> a > b }`, when parsed, then
   closure has `Params` == `["a", "b"]`.
3. Given `ch.map { it * 2 }`, when parsed, then closure has
   `Params` == `[]` (implicit `it`).
4. Given `ch.map { -> 42 }`, when parsed, then closure has
   `Params` == `[]` and `Body` containing `"42"`.

### H2: Evaluate simple closures during channel resolution

As a developer, I want simple closures evaluated during channel
resolution, so that `map` and `filter` operators produce correct
results for common patterns.

Closure evaluation binds each named parameter (or implicit `it`)
to the current channel item before evaluating the body via
`EvalExpr`. For `map`, the evaluated result replaces the item.
For `filter`, the evaluated result is tested for truthiness;
falsy items are excluded. Complex or unsupported closure bodies
fall back to passthrough with a warning.

Parameter binding: the evaluator creates a variable scope with
the closure's `Params` names mapped to the current channel item.
For multi-parameter closures, the item must be a list/tuple; each
element is bound to the corresponding parameter in order.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/channel.go`, `nextflowdsl/groovy.go`
**Test file:** `nextflowdsl/channel_test.go`,
`nextflowdsl/groovy_test.go`

**Acceptance tests:**

1. Given `ch.map { item -> item.id }` where ch items are maps
   with key `"id"` having values `["a", "b", "c"]`, when
   resolved, then result items are `["a", "b", "c"]`.
2. Given `ch.filter { it > 3 }` where ch has items `[1, 5, 2,
   7]`, when resolved, then result has items `[5, 7]`.
3. Given `ch.map { it * 2 }` where ch has items `[1, 2, 3]`,
   when resolved, then result has items `[2, 4, 6]`.
4. Given `ch.map` with complex unsupported closure body (e.g.
   multi-statement or method chains the evaluator cannot
   handle), when resolution attempted, then result is
   passthrough (original items unchanged) and a warning is
   emitted.

---

## I. Workflow-Level Features

### I1: Parse workflow lifecycle handlers

As a developer, I want `workflow.onComplete` and
`workflow.onError` blocks parsed and skipped, so that pipelines
using lifecycle handlers do not fail.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `workflow.onComplete { println 'Done' }\nprocess foo {
   script: 'echo hi' }`, when parsed, then 1 process and no
   error.
2. Given `workflow.onError { println 'Failed' }\nprocess foo {
   script: 'echo hi' }`, when parsed, then 1 process and no
   error.
3. Given `workflow.onComplete { }\nworkflow.onError { }`, when
   parsed, then no error and 0 processes.

Note: named workflow `main:` entry point selection is already
implemented in the existing parser. No additional work needed.

---

## J. Deprecation Compatibility

### J1: Parse deprecated constructs with warnings

As a developer, I want deprecated-but-DSL2-valid constructs
parsed with deprecation warnings, so that older pipelines work.

Deprecated constructs (produce warning, continue parsing):
- `Channel.from()` -> treated as `Channel.of()`
- `merge` operator -> passthrough with warning
- `toInteger` operator -> passthrough with warning
- `countFasta`, `countFastq`, `countJson`, `countLines`
  operators -> passthrough with deprecation warning

DSL1-only constructs (produce error):
- `Channel.create()` -> parse error
- `into` keyword -> parse error (already handled)
- `set` keyword for channel assignment -> distinct from `set`
  operator (operator is valid DSL2)

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`, `nextflowdsl/channel.go`
**Test file:** `nextflowdsl/parse_test.go`,
`nextflowdsl/channel_test.go`

**Acceptance tests:**

1. Given `Channel.from(1,2,3)`, when resolved, then result has
   3 items and a deprecation warning is emitted to stderr.
2. Given `Channel.create()`, when parsed, then error mentions
   DSL1-only construct.
3. Given `ch.merge(other)`, when parsed, then operator is
   `"merge"` (no error) and a deprecation warning is emitted.
4. Given `ch.toInteger()`, when parsed, then operator is
   `"toInteger"` (no error) and a deprecation warning is
   emitted.
5. Given `set { item }` as a DSL1-style channel assignment,
   when parsed, then error mentions DSL1-only construct.

---

## K. Config Container Auto-Detection in cmd

### K1: Use config container engine when no CLI flag

As a developer, I want the `cmd/nextflow.go` layer to use
`Config.ContainerEngine` when `--container-runtime` is not
explicitly set, so that config-driven container settings work.

**Package:** `cmd/`
**File:** `cmd/nextflow.go`
**Test file:** `cmd/nextflow_test.go`

**Acceptance tests:**

1. Given config with `docker { enabled = true }` and no
   `--container-runtime` flag, when `wr nextflow run` is
   invoked, then `TranslateConfig.ContainerRuntime` ==
   `"docker"`.
2. Given config with `singularity { enabled = true }` and
   `--container-runtime docker` flag, when invoked, then
   `TranslateConfig.ContainerRuntime` == `"docker"` (CLI wins).
3. Given config with no container scope and no CLI flag, when
   invoked, then `TranslateConfig.ContainerRuntime` ==
   `"singularity"` (existing default).

---

## L. Selector Nesting in Config

### L1: Parse nested selectors

As a developer, I want nested selectors like
`withLabel: 'big' { withName: 'foo' { ... } }` parsed, so
that composite selectors work.

Nesting limited to one level. Composite selectors require
both conditions to match. Represented as a single
`ProcessSelector` with both a label and name pattern.

Extend `ProcessSelector`:

```go
type ProcessSelector struct {
    Kind      string
    Pattern   string
    Settings  *ProcessDefaults
    Inner     *ProcessSelector // nested selector, nil if none
}
```

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/config.go`
**Test file:** `nextflowdsl/config_test.go`

**Acceptance tests:**

1. Given config `process { withLabel: 'big' { withName: 'ALIGN' {
   cpus = 32 } } }`, when parsed, then `Selectors` has 1 entry
   with `Kind` == `"withLabel"`, `Pattern` == `"big"`, and
   `Inner.Kind` == `"withName"`, `Inner.Pattern` == `"ALIGN"`,
   `Inner.Settings.Cpus` == 32.
2. Given process `ALIGN` with label 'big', config with nested
   selector `withLabel: 'big' { withName: 'ALIGN' { cpus = 32 } }`,
   when translated, then `Requirements.Cores` == 32.
3. Given process `SORT` with label 'big', config with nested
   selector as above, when translated, then nested selector does
   NOT match (name does not match 'ALIGN'), generic defaults
   apply.

---

## Implementation Order

### Phase 1: Groovy Expression Extensions (D1-D6)

Foundation for all subsequent evaluation. Sequential within
phase: D4 (list/map literals) first, D5 (null/task.*), D2
(comparison/logical), D1 (ternary/elvis), D3 (method calls),
D6 (casts). No external dependencies.

### Phase 2: Process Directive Parsing (F1, A1)

Parse `label`, `tag`, `beforeScript`, `afterScript`, `module`,
`cache`, and other directives. A1 (parse label) is part of
F1. Depends on Phase 1 for expression evaluation of directive
values.

### Phase 3: Config Selectors (A2, L1, G1, G3)

Sequential: A2 (parse withLabel/withName), L1 (nested
selectors), G1 (env scope), G3 (container scope). Depends on
Phase 2 for label directive parsing.

### Phase 4: Channel Operators and Factories (B1, C1)

B1 (parse new operators) and C1 (parse new factories) are
independent. Depends on Phase 1 for expression parsing of
operator/factory arguments.

### Phase 5: Operator/Factory Translation (B2, C1 resolution)

Translate new operators with cardinality effects and resolve
new factories. Depends on Phase 4.

### Phase 6: Closure Parsing and Workflow Conditions (H1, H2, E1)

H1 (parse closure parameters), H2 (evaluate simple closures),
and E1 (parse if/else in workflow blocks). H2 depends on H1.
E1 is independent. Depends on Phase 1.

### Phase 7: Translation Integration (A3, F2, F3, G2, E2, K1)

Sequential: A3 (apply selectors), F2 (beforeScript/afterScript
translation), F3 (module translation), G2 (merge config env),
E2 (translate if/else), K1 (container auto-detect in cmd).
Depends on all prior phases.

### Phase 8: Lifecycle and Deprecation (I1, J1)

I1 (workflow lifecycle handlers) and J1 (deprecation
compatibility) are independent. Depends on Phase 4 for
operator/factory parsing.

---

## Appendix: Key Decisions

1. **Parse-first strategy maintained.** Parser accepts valid DSL2
   without error even if translation is incomplete.
   Untranslatable constructs produce warnings, not errors.
2. **Selector specificity**: generic < withLabel < withName <
   process-level. Last-match-wins among same specificity.
   Matches official Nextflow behaviour.
3. **Groovy evaluator remains best-effort.** Complex closures
   that cannot be evaluated return `UnsupportedExpr` and fall
   back to defaults with Override=0.
4. **task.attempt defaults to 1.** wr's resource learning
   (Override=0) handles subsequent attempts. Static evaluation
   at translate time is sufficient.
5. **Non-evaluable if/else emits both branches.** Each branch
   uses a separate CWD subtree so outputs do not conflict. Both
   branches execute; unnecessary jobs complete quickly.
6. **beforeScript/afterScript wrap inside container.** The
   entire before+script+after block runs as one unit.
7. **tag does not affect RepGroup.** RepGroup naming is
   deterministic (`nf.<wf>.<runid>.<proc>`). Tag is metadata
   only.
8. **cache/scratch stored and warned.** wr handles caching via
   job deduplication. No translation needed.
9. **Config env merges under process env.** Process-level env
   overrides config-level env for same key.
10. **cross operator produces N*M items.** Each combination is
    a list `[item1, item2]`. buffer(n) produces ceil(N/n)
    groups. min/max/sum produce 1 item.
11. **Deprecated DSL2 constructs warn; DSL1 constructs error.**
    `Channel.from()` works (warning). `Channel.create()` fails
    (error).
12. **Testing.** GoConvey for all unit tests per go-conventions.
    Each acceptance test maps to a GoConvey `Convey`/`So` block.
    Existing tests must continue to pass.
13. **Nested selectors limited to one level.** Matches practical
    usage. `withLabel` containing `withName` (or vice versa)
    creates a composite AND condition.
14. **Container engine auto-detection.** CLI flag always wins.
    Config `docker/singularity/apptainer { enabled = true }`
    sets the default. Last-defined-wins among multiple container
    scopes.
15. **Glob vs regex patterns.** Glob uses `filepath.Match`.
    Regex uses `~` prefix and Go `regexp.MatchString`.
    Case-sensitive matching (matches Nextflow).
16. **storeDir is stored-and-warned, not translated.** Unlike
    `publishDir`, `storeDir` is intentionally not translated
    because wr's job deduplication serves a similar caching
    role. The directive value is stored for potential future
    use.
