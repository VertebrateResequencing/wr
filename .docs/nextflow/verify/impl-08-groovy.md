# Implementation: Groovy Evaluator (groovy.go)

**File:** `nextflowdsl/groovy.go` (~4140 lines)

## Expression Evaluation

### EvalExpr (line 577)
Main entry point. Type-switches on Expr node types:
- LiteralExpr → returns value directly
- VarExpr → looks up in vars
- BinaryExpr → `evalBinaryExpr`
- TernaryExpr → `evalTernaryExpr`
- UnaryExpr → `evalUnaryExpr`
- CastExpr → `evalCastExpr`
- ListExpr → `evalListExpr`
- MapExpr → `evalMapExpr`
- IndexExpr → `evalIndexExpr`
- MethodCallExpr → `evalMethodCallExpr`
- NullSafeExpr → `evalNullSafeExpr`
- SpreadExpr → `evalSpreadExpr`
- ClosureExpr → returns as-is (unevaluated)
- InExpr → `evalInExpr`
- RegexExpr → `evalRegexExpr`
- RangeExpr → `evalRangeExpr`
- NewExpr → `evalNewExpr`
- InstanceofExpr → `evalInstanceofExpr`
- AssertStmt / ThrowStmt / TryCatchStmt / ReturnStmt / ForStmt → statement evaluation

**Coverage:** SYN-*, STMT-*, METH-* (all expression/statement evaluation)

### interpolateGroovyString (line 634)
Resolves `"hello ${name}"` GString interpolation.
**Coverage:** SYN-string-interp

### resolveExprPath (line 678)
Resolves dotted paths like `params.input` or `workflow.projectDir`.
**Coverage:** BV-* (workflow, nextflow, task built-in properties)

## Binary Operators

### evalBinaryExpr (line 778)
Handles all binary operators:
- Arithmetic: `+`, `-`, `*`, `/`, `%`, `**`
- Comparison: `==`, `!=`, `<`, `>`, `<=`, `>=`, `<=>`
- Logical: `&&`, `||`
- Bitwise: `&`, `|`, `^`, `<<`, `>>`
- String: `+` (concatenation)
- Regex: `=~`, `==~`
- Elvis: `?:`
- Assignment: `=`, `+=`, `-=`, etc.
- `in` → `evalInExpr`
- `instanceof` → `evalInstanceofExpr`

**Coverage:** SYN-arith-*, SYN-compare-*, SYN-log-*, SYN-bitwise-*,
SYN-regex, SYN-elvis, SYN-assign-*, SYN-string-concat

## Control Flow

### evalStatement (line 255)
Evaluates statements within closure/block bodies:
- if/else, for-in, while, switch, try/catch/finally
- Variable assignment, method calls
- return, break, continue, throw, assert

**Coverage:** STMT-if, STMT-for, STMT-while, STMT-switch, STMT-try,
STMT-return, STMT-break, STMT-continue, STMT-throw, STMT-assert

### evalStatementBody (line 471)
Parses and evaluates a block body string using `parseEvalStatements`.

### isTruthy (line 1323)
Groovy truthiness: null→false, 0→false, ""→false, empty collections→false.
**Coverage:** SYN-truthy

## Type Operations

### evalCastExpr (line 1419)
Handles `(Type) expr` and `expr as Type` casts.
**Coverage:** SYN-cast

### evalRangeExpr (line 1242)
Evaluates `a..b` and `a..<b` ranges.
**Coverage:** SYN-range

### evalUnaryExpr (line 1386)
Handles `!`, `-`, `+`, `~`, `++`, `--`.
**Coverage:** SYN-unary-*

### evalIndexExpr (line 1480)
List/map indexing: `list[0]`, `map['key']`, negative indices, ranges.
**Coverage:** SYN-index

### evalNullSafeExpr (line 1564)
`obj?.property` — returns null instead of error if obj is null.
**Coverage:** SYN-null-safe

### evalSpreadExpr (line 1594)
`list*.property` — spreads property access across list elements.
**Coverage:** SYN-spread

## Method Calls

### evalMethodCallExpr (line 1629)
Dispatches method calls by receiver type:
1. Evaluate receiver
2. If string → `evalStringMethodCall`
3. If list → `evalListMethodCallExpr` (closure-based) or `evalListMethodCall`
4. If map → `evalMapMethodCall`
5. If number → `evalNumberMethodCall`
6. If static → `evalStaticMethodCall`

**Coverage:** METH-str-*, METH-list-*, METH-map-*, METH-num-*, GF-*

### evalStringMethodCall (line 1735)
~60 string methods: contains, startsWith, endsWith, replace, replaceAll,
split, trim, toUpperCase, toLowerCase, substring, indexOf, padLeft,
padRight, capitalize, etc.
**Coverage:** METH-str-*

### evalListMethodCallExpr (line 2964) / evalListMethodCall (line 3587)
List methods split into closure-based (collect, find, findAll, each, inject,
groupBy, sort, min, max, any, every, count, collectMany, withIndex,
findIndexOf, sum, unique, takeWhile, dropWhile) and plain (add, addAll,
remove, get, size, contains, isEmpty, flatten, reverse, sort, unique,
first, last, head, tail, take, drop, indexOf, join, subList, plus,
minus, intersect, disjoint, collate, combinations, transpose, indexed,
withIndex, toSet, asImmutable, collect, sum, toUnique).
**Coverage:** METH-list-*

### evalMapMethodCall (line 2257)
Map methods: get, getOrDefault, containsKey, containsValue, keySet,
values, entrySet, size, isEmpty, each, eachWithIndex, collect, find,
findAll, any, every, sort, groupBy, count, collectEntries, subMap,
plus, minus, inject, min, max, sum, findResult, take, drop, flatten,
toSorted.
**Coverage:** METH-map-*

### evalNumberMethodCall (line 2853)
Number methods: abs, intdiv, toInteger, toLong, toFloat, toDouble,
toString, times, upto, downto, power, max, min, compareTo.
**Coverage:** METH-num-*

### evalStaticMethodCall (line 1698)
Static methods: Math.*, System.getenv, Collections.*, Arrays.asList.
**Coverage:** GF-*, METH-static-*

## Constructors

### evalNewExpr (line 4087)
Dispatches `new ClassName(args)`:
- File/Path → `evalPathConstructor`
- String, Date, BigDecimal, BigInteger, ArrayList, Map, Random →
  dedicated constructors (lines 59-146)
**Coverage:** METH-new-*

### evalPathConstructor (line 4115)
Creates file path from string arg.
**Coverage:** METH-new-File, METH-new-Path, GF-file
