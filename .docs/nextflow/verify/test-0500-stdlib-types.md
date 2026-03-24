# Test: Standard Library Types

**Spec files:** nf-0500-stdlib-types.md
**Impl files:** impl-08-groovy.md

## Task

For each feature ID in nf-0500-stdlib-types.md, determine its classification.

### Checklist

1. Read nf-0500-stdlib-types.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- TYPE-bag, TYPE-boolean, TYPE-channel, TYPE-duration, TYPE-float
  TYPE-integer, TYPE-iterable, TYPE-list, TYPE-map, TYPE-memoryunit
  TYPE-path, TYPE-operations, TYPE-filesystem-attributes, TYPE-reading, TYPE-writing
  TYPE-filesystem-operations, TYPE-splitting-files, TYPE-record, TYPE-set, TYPE-string
  TYPE-tuple, TYPE-value, TYPE-versionnumber

### Output format

```
TYPE-bag: SUPPORTED | reason
...
```

## Results

- TYPE-bag: FUTURE — `evalNewExpr`, `evalCastExpr`, and `matchesGroovyType` have no `Bag` handling; adding a bag type would require new constructor/type/method support rather than using an existing evaluator type.
- TYPE-boolean: SUPPORTED — booleans evaluate directly through `EvalExpr`, logical operators are handled in `evalBinaryExpr`, truthiness is implemented in `isTruthy`, and `matchesGroovyType` recognizes `Boolean`.
- TYPE-channel: FUTURE — there is no dedicated `Channel` support in `evalNewExpr`, `evalCastExpr`, `evalStaticMethodCall`, or `matchesGroovyType`; `evalMethodCallExpr` only dispatches concrete receivers like `[]any`, maps, strings, and numbers.
- TYPE-duration: FUTURE — no `Duration` constructor, cast, static helper, or type check exists in `evalNewExpr`, `evalCastExpr`, `evalStaticMethodCall`, or `matchesGroovyType`.
- TYPE-float: GAP — float values are partially supported because `evalNumberMethodCall` handles `float64`, but `evalBinaryExpr` arithmetic and ordered comparison paths fall back to integer-only helpers such as `requireIntegerOperand` and `compareOrderedOperands`.
- TYPE-integer: SUPPORTED — integer arithmetic and ordered comparisons are implemented in `evalBinaryExpr`, casts in `evalCastExpr`, `Integer.parseInt` in `evalStaticMethodCall`, and numeric helpers in `evalNumberMethodCall`.
- TYPE-iterable: GAP — looping works for slice/array-like values via `iterValues` and `closureTupleValues`, but there is no explicit `Iterable` type support in `matchesGroovyType`, `evalCastExpr`, or receiver dispatch.
- TYPE-list: SUPPORTED — list literals are built by `evalListExpr`, indexing is implemented in `evalIndexExpr`, list methods are handled by `evalListMethodCallExpr` and `evalListMethodCall`, and `matchesGroovyType` recognizes `List`.
- TYPE-map: SUPPORTED — map literals are built by `evalMapExpr`, indexing is implemented in `evalIndexExpr`, map methods are handled by `evalMapMethodCall`, and `matchesGroovyType` recognizes `Map`.
- TYPE-memoryunit: FUTURE — no `MemoryUnit` handling appears in `evalNewExpr`, `evalCastExpr`, `evalStaticMethodCall`, or `matchesGroovyType`.
- TYPE-path: GAP — `evalNewExpr` and `evalPathConstructor` construct cleaned path strings, but `evalMethodCallExpr` then treats the result as a plain string receiver rather than a real `Path` object.
- TYPE-operations: GAP — path values only get generic string dispatch through `evalMethodCallExpr` and `evalStringMethodCall`; there is no path-specific operation layer in `resolvePropertyPath` or method dispatch.
- TYPE-filesystem-attributes: FUTURE — `resolvePropertyPath` and `lookupVariablePart` only resolve map-like/object-like lookups, and there is no implementation for path attributes such as file name, extension, or size.
- TYPE-reading: GAP — `evalStringMethodCall` implements `readLines` and `eachLine` against the string value itself, so a path returned by `evalPathConstructor` would be split as text instead of reading file contents.
- TYPE-writing: FUTURE — there are no write or append operations for path/string receivers in `evalStringMethodCall` or elsewhere in the cited evaluator code.
- TYPE-filesystem-operations: FUTURE — there is no evaluator support for path operations such as existence checks, deletion, directory creation, or listing; `evalStringMethodCall` contains no such methods.
- TYPE-splitting-files: FUTURE — the evaluator only implements plain string splitting (`split`, `tokenize`) in `evalStringMethodCall`; there are no file-based `split*` helpers.
- TYPE-record: FUTURE — `Record` is not implemented in `evalNewExpr`, `evalCastExpr`, `matchesGroovyType`, or receiver dispatch.
- TYPE-set: GAP — `evalListMethodCallExpr` supports `asType(Set|HashSet|LinkedHashSet)` and `evalListMethodCall` supports `toSet`, but both return deduplicated `[]any`, and there is no dedicated set type in `matchesGroovyType` or `evalNewExpr`.
- TYPE-string: SUPPORTED — string interpolation is implemented in `interpolateGroovyString`, string methods in `evalStringMethodCall`, casts in `evalCastExpr`, and `matchesGroovyType` recognizes `String`.
- TYPE-tuple: GAP — tuple-like values work only as raw slice/array data via `closureTupleValues`, `transposeList`, and `evalMultiAssignExpr`; there is no dedicated `Tuple` constructor or type.
- TYPE-value: FUTURE — there is no dedicated `Value` type support in `evalNewExpr`, `evalCastExpr`, `evalStaticMethodCall`, `matchesGroovyType`, or receiver dispatch.
- TYPE-versionnumber: FUTURE — no `VersionNumber` constructor, cast, static helper, or type check exists in the cited evaluator implementation.
