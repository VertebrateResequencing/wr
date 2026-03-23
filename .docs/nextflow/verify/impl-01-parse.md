# Implementation: Parsing & Lexing (parse.go)

**File:** `nextflowdsl/parse.go` (~3110 lines)

## Data Maps

### supportedChannelOperators (line 47)
Map of 51 operator names accepted during parsing:
buffer, branch, collate, collect, collectFile, combine, concat, count,
countFasta, countFastq, countJson, countLines, cross, distinct, dump,
filter, first, flatMap, flatten, groupTuple, ifEmpty, join, last, map,
max, merge, min, mix, multiMap, randomSample, reduce, set, splitCsv,
splitFasta, splitFastq, splitJson, splitText, subscribe, sum, tap, take,
toDouble, toFloat, toInteger, toList, toLong, toSortedList, transpose,
until, unique, view.

**Coverage:** CO-* features — determines which operators are parsed.

### supportedChannelFactories (line 101)
Map of 12 factory names: empty, from, fromFilePairs, fromLineage, fromList,
fromPath, fromSRA, interval, of, topic, value, watchPath.

**Coverage:** CF-* features — determines which `Channel.*` calls are parsed.

## Lexer

### lex (line 147)
Tokenizes Nextflow/Groovy source into token stream. Handles:
- Comments (line `//`, block `/* */`), strings (single, double, triple-quoted, slashy, dollar-slashy)
- Numbers, identifiers, all punctuation and operators
- GString interpolation `${}` inside strings

**Coverage:** SYN-comments, SYN-string-*, SYN-int, SYN-float, SYN-operators

## Expression Parsing

### parseExprTokens (line 493) → entry point
Full Groovy expression parser using recursive descent:
- parseTernaryExprTokens (599) → SYN-ternary
- parseLogicalOrExprTokens (952) / parseLogicalAndExprTokens (1130) → SYN-log-*
- parseEqualityExprTokens (1158) / parseComparisonExprTokens (1321) → SYN-compare-*
- parseAdditiveExprTokens (1138) / parseMultiplicativeExprTokens (1166) → SYN-arith-*
- parsePowerExprTokens (1199)
- parseUnaryExprTokens (1407) → SYN-unary-*
- parseCastExprTokens (1428) → SYN-cast
- parsePrimaryExprTokens (1462) → literals, lists, maps, closure, method calls
- parseRangeExprTokens (1337) → SYN-range
- parseRegexExprTokens (1263) → SYN-regex
- parseNullSafeExprTokens (1711) → SYN-null-safe
- parseSpreadExprTokens (1755) → SYN-spread
- parseMethodCallExprTokens (1795) → METH-*
- parseIndexExprTokens (1923) → SYN-index
- parseCollectionExprTokens (1978) → SYN-list-literal, SYN-map-literal
- parseClosureExpr (1629) → SYN-closure

## Workflow / Process Parsing

### parseWorkflow (line 2445)
Top-level parser entry. Parses:
- `include` imports → INCL-*
- `process` definitions → PSEC-*, INP-*, OUT-*, DIR-*
- `workflow` blocks → WF-*
- `params` blocks → PARAM-*
- `output` blocks → WF-output-block
- `enum` definitions → SYN-enum
- `record` definitions → SYN-record
- Function definitions → SYN-function (via parseFunctionDef at 2646)

### parseTupleDeclaration (line 725)
Parses `tuple(val(x), path('*.txt'))` input/output declarations.
**Coverage:** INP-tuple, OUT-tuple

### parseTupleElement (line 754) + applyTupleElementQualifier (886)
Parses individual qualifiers: val, path, env, stdin, stdout.
**Coverage:** INP-val, INP-path, INP-env, INP-stdin, OUT-val, OUT-path, OUT-env, OUT-stdout

### parseTopLevelParamsBlock (line 2880) / parseTopLevelParamAssignment (3059)
Parses `params { ... }` blocks and `params.x = val` assignments.
**Coverage:** PARAM-block, PARAM-dot-assign

### parseParamDecl (line 2955)
Parses typed param declarations in `params` block (preview types feature).
**Coverage:** PARAM-typed

### skipFeatureFlagAssignment (line 3110)
Silently skips `nextflow.enable.*` / `nextflow.preview.*` assignments.
**Coverage:** CFG-feature-flags
