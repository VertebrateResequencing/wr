# Phase 1: Groovy Methods and Evaluation

Ref: [spec.md](spec.md) sections K1, L1, M1, M2, M3, M4, O1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 1.1: K1 - Evaluate common `new` constructors [parallel with 1.2, 1.3, 1.4, 1.5, 1.6, 1.7]

spec.md section: K1

Implement `evalNewExpr` in `nextflowdsl/groovy.go` to
special-case `new File(path)`, `new URL(str)`, `new Date()`,
`new BigDecimal(str)`, `new BigInteger(str)`,
`new ArrayList(collection)`, `new HashMap(map)`,
`new LinkedHashMap(map)`, and `new Random()`. Unknown
constructors remain `UnsupportedExpr`. See spec for full
constructor table, covering all 11 acceptance tests from K1.

- [ ] implemented
- [ ] reviewed

#### Item 1.2: L1 - Evaluate destructuring assignments [parallel with 1.1, 1.3, 1.4, 1.5, 1.6, 1.7]

spec.md section: L1

Implement `evalMultiAssignExpr` in `nextflowdsl/groovy.go` to
handle `def (x, y) = [1, 2]` style destructuring. Evaluate
RHS, assert list, assign by index. Short lists yield nil for
missing vars; long lists ignore extras. Covering all 5
acceptance tests from L1.

- [ ] implemented
- [ ] reviewed

#### Item 1.3: M1 - String methods [parallel with 1.1, 1.2, 1.4, 1.5, 1.6, 1.7]

spec.md section: M1

Add to `evalStringMethodCall` in `nextflowdsl/groovy.go`:
`replaceFirst`, `padLeft`, `padRight`, `capitalize`,
`uncapitalize`, `isNumber`, `isInteger`, `isLong`, `isDouble`,
`isBigDecimal`, `toBoolean`, `stripIndent`, `readLines`,
`count`, `toLong`, `toDouble`, `toBigDecimal`, `eachLine`,
`execute` (warn-only). See spec for full method table,
covering all 24 acceptance tests from M1.

- [ ] implemented
- [ ] reviewed

#### Item 1.4: M2 - List methods [parallel with 1.1, 1.2, 1.3, 1.5, 1.6, 1.7]

spec.md section: M2

Add to list method evaluation in `nextflowdsl/groovy.go`:
`inject`, `withIndex`, `indexed`, `groupBy`, `countBy`,
`count`, `collectMany`, `collectEntries`, `transpose`, `head`,
`tail`, `init`, `pop`, `push`, `add`, `addAll`, `remove`,
`contains`, `intersect`, `disjoint`, `toSet`, `reverseEach`,
`eachWithIndex`, `reverse`, `sum`, `max`, `min`, `asType`,
`spread`. See spec for full method table, covering all 37
acceptance tests from M2.

- [ ] implemented
- [ ] reviewed

#### Item 1.5: M3 - Map methods [parallel with 1.1, 1.2, 1.3, 1.4, 1.6, 1.7]

spec.md section: M3

Implement `evalMapMethodCall` in `nextflowdsl/groovy.go` to
handle `map[string]any` receivers: `findAll`, `find`, `any`,
`every`, `groupBy`, `collectEntries`, `plus`, `minus`, `sort`,
`inject`, `size`, `isEmpty`, `keySet`, `values`, `containsKey`,
`containsValue`, `subMap`, `collect`, `each`, `getOrDefault`.
Closure-based methods receive `(key, value)` pairs. See spec
for full method table, covering all 24 acceptance tests from
M3.

- [ ] implemented
- [ ] reviewed

#### Item 1.6: M4 - Number methods [parallel with 1.1, 1.2, 1.3, 1.4, 1.5, 1.7]

spec.md section: M4

Implement `evalNumberMethodCall` in `nextflowdsl/groovy.go` to
handle int/int64/float64 receivers: `abs`, `round`, `intdiv`,
`toInteger`, `toLong`, `toDouble`, `toBigDecimal`. See spec for
full method table, covering all 9 acceptance tests from M4.

- [ ] implemented
- [ ] reviewed

#### Item 1.7: O1 - Evaluate assert and throw statements [parallel with 1.1, 1.2, 1.3, 1.4, 1.5, 1.6]

spec.md section: O1

Implement `evalAssertStmt` and `evalThrowStmt` in
`nextflowdsl/groovy.go`. `assert expr` evaluates expression
and emits warning if false/null; `assert expr : 'message'`
uses custom message. `throw new Exception('msg')` emits
warning with message. Never halt translation. Error-level
only for compile-time-constant failures. Covering all 7
acceptance tests from O1.

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
