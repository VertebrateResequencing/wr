# Test: Groovy Methods & Imports

**Spec files:** nf-0400-groovy-methods.md
**Impl files:** impl-08-groovy.md

## Task

For each feature ID in nf-0400-groovy-methods.md, determine its classification.

### Checklist

1. Read nf-0400-groovy-methods.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- METH-bag-plus, METH-bag-membership, METH-duration-toDays, METH-duration-toHours, METH-duration-toMillis
  METH-duration-toMinutes, METH-duration-toSeconds, METH-duration-getDays, METH-duration-getHours, METH-duration-getMillis
  METH-duration-getMinutes, METH-duration-getSeconds, METH-iterable-any, METH-iterable-collect, METH-iterable-collectMany
  METH-iterable-contains, METH-iterable-each, METH-iterable-every, METH-iterable-findAll, METH-iterable-groupBy
  METH-iterable-inject, METH-iterable-inject-3, METH-iterable-isEmpty, METH-iterable-join, METH-iterable-max
  METH-iterable-max-1, METH-iterable-max-2, METH-iterable-min, METH-iterable-min-1, METH-iterable-min-2
  METH-iterable-size, METH-iterable-sum, METH-iterable-sum-1, METH-iterable-toList, METH-iterable-toSet
  METH-iterable-toSorted, METH-iterable-toSorted-1, METH-iterable-toSorted-2, METH-iterable-toUnique, METH-iterable-toUnique-2
  METH-iterable-toUnique-1, METH-list-plus, METH-list-multiply, METH-list-getAt, METH-list-membership
  METH-list-collate, METH-list-collate-3, METH-list-find, METH-list-first, METH-list-getIndices
  METH-list-head, METH-list-indexOf, METH-list-init, METH-list-last, METH-list-reverse
  METH-list-subList, METH-list-tail, METH-list-take, METH-list-takeWhile, METH-list-withIndex
  METH-map-plus, METH-map-getAt, METH-map-membership, METH-map-any, METH-map-containsKey
  METH-map-containsValue, METH-map-each, METH-map-entrySet, METH-map-every, METH-map-isEmpty
  METH-map-keySet, METH-map-size, METH-map-subMap, METH-map-values, METH-memoryunit-toBytes
  METH-memoryunit-toGiga, METH-memoryunit-toKilo, METH-memoryunit-toMega, METH-memoryunit-toUnit, METH-memoryunit-getBytes
  METH-memoryunit-getGiga, METH-memoryunit-getKilo, METH-memoryunit-getMega, METH-path-divide, METH-path-leftShift
  METH-path-baseName, METH-path-extension, METH-path-name, METH-path-parent, METH-path-scheme
  METH-path-simpleName, METH-path-exists, METH-path-isDirectory, METH-path-isEmpty, METH-path-isFile
  METH-path-isHidden, METH-path-isLink, METH-path-lastModified, METH-path-relativize, METH-path-resolve
  METH-path-resolveSibling, METH-path-size, METH-path-toUriString, METH-path-eachLine, METH-path-getText
  METH-path-readLines, METH-path-withReader, METH-path-append, METH-path-setText, METH-path-write
  METH-path-copyTo, METH-path-delete, METH-path-deleteDir, METH-path-getPermissions, METH-path-mkdir
  METH-path-mkdirs, METH-path-mklink, METH-path-mklink-hard, METH-path-mklink-overwrite, METH-path-moveTo
  METH-path-renameTo, METH-path-setPermissions, METH-path-setPermissions-3, METH-path-eachFile, METH-path-eachFileRecurse
  METH-path-listDirectory, METH-path-listFiles, METH-path-countFasta, METH-path-countFastq, METH-path-countJson
  METH-path-countLines, METH-path-splitCsv, METH-path-splitFasta, METH-path-splitFastq, METH-path-splitJson
  METH-path-splitText, METH-record-plus, METH-record-subMap, METH-set-plus, METH-set-minus
  METH-set-membership, METH-set-intersect, METH-string-plus, METH-string-multiply, METH-string-getAt
  METH-string-bitwiseNegate, METH-string-find, METH-string-match, METH-string-contains, METH-string-endsWith
  METH-string-execute, METH-string-indexOf, METH-string-indexOf-2, METH-string-isBlank, METH-string-isEmpty
  METH-string-isDouble, METH-string-isFloat, METH-string-isInteger, METH-string-isLong, METH-string-lastIndexOf
  METH-string-lastIndexOf-2, METH-string-length, METH-string-md5, METH-string-replace, METH-string-replaceAll
  METH-string-replaceFirst, METH-string-sha256, METH-string-startsWith, METH-string-strip, METH-string-stripIndent
  METH-string-stripLeading, METH-string-stripTrailing, METH-string-substring, METH-string-substring-2, METH-string-toBoolean
  METH-string-toDouble, METH-string-toFloat, METH-string-toInteger, METH-string-toLong, METH-string-toLowerCase
  METH-string-toUpperCase, METH-string-tokenize, METH-tuple-getAt, METH-value-flatMap, METH-value-map
  METH-value-subscribe, METH-value-view, METH-value-view-1, METH-versionnumber-getMajor, METH-versionnumber-getMinor
  METH-versionnumber-getPatch, METH-versionnumber-matches

### Output format

```
METH-bag-plus: SUPPORTED | reason
...
```

## Results

- METH-bag-plus: FUTURE - `evalBinaryExpr` only overloads `<<` for `[]any`; `+` falls through to integer arithmetic, so bag/list-style `+` is not implemented.
- METH-bag-membership: SUPPORTED - `evalInExpr` uses `containsValue`, which supports membership on `[]any` receivers.
- METH-duration-toDays, METH-duration-toHours, METH-duration-toMillis, METH-duration-toMinutes, METH-duration-toSeconds, METH-duration-getDays, METH-duration-getHours, METH-duration-getMillis, METH-duration-getMinutes, METH-duration-getSeconds: FUTURE - `evalMethodCallExpr` only dispatches method calls for `string`, `map[string]any`/`orderedMap`, numbers, and `[]any`; there is no Duration receiver/type support in `groovy.go`.
- METH-iterable-any, METH-iterable-collect, METH-iterable-collectMany, METH-iterable-contains, METH-iterable-every, METH-iterable-findAll, METH-iterable-groupBy, METH-iterable-inject-3, METH-iterable-isEmpty, METH-iterable-max, METH-iterable-max-1, METH-iterable-min, METH-iterable-min-1, METH-iterable-size, METH-iterable-sum, METH-iterable-sum-1, METH-iterable-toSet: SUPPORTED - `evalListMethodCallExpr`, `evalListClosurePredicateMethod`, `evalListClosureMethod`, and `evalListMethodCall` implement these operations for `[]any` receivers.
- METH-iterable-each, METH-iterable-toList, METH-iterable-toSorted, METH-iterable-toSorted-1, METH-iterable-toSorted-2, METH-iterable-toUnique, METH-iterable-toUnique-1, METH-iterable-toUnique-2: FUTURE - these method names are not implemented in `evalListMethodCallExpr` or `evalListMethodCall`.
- METH-iterable-inject: GAP - `evalListClosureMethod` only supports `inject(initialValue, closure)` and does not implement the accumulator-only variant.
- METH-iterable-join: GAP - `evalListMethodCall` implements `join(separator)` with arity 1 only, so the documented default-argument form is not fully supported.
- METH-iterable-max-2, METH-iterable-min-2: GAP - `evalListClosureMethod` supports the unary closure/scoring form for `max`/`min`, but not the two-argument comparator closure variant.
- METH-list-plus, METH-list-multiply: FUTURE - list `+` and `*` operators are not implemented in `evalBinaryExpr`; only integer arithmetic and list `<<` are handled there.
- METH-list-getAt, METH-list-membership, METH-list-find, METH-list-first, METH-list-head, METH-list-init, METH-list-last, METH-list-reverse, METH-list-tail, METH-list-take, METH-list-withIndex: SUPPORTED - `evalIndexExpr`, `containsValue`, `evalListClosurePredicateMethod`, and `evalListMethodCall` implement these list operations on `[]any`.
- METH-list-collate, METH-list-collate-3, METH-list-getIndices, METH-list-indexOf, METH-list-subList, METH-list-takeWhile: FUTURE - these list method names are absent from `evalListMethodCallExpr` and `evalListMethodCall`.
- METH-map-plus: FUTURE - map `+` as an operator is not implemented in `evalBinaryExpr`, even though `evalMapMethodCall` has a `.plus(...)` method form.
- METH-map-getAt, METH-map-membership, METH-map-any, METH-map-containsKey, METH-map-containsValue, METH-map-each, METH-map-every, METH-map-isEmpty, METH-map-keySet, METH-map-size, METH-map-subMap, METH-map-values: SUPPORTED - `evalIndexExpr`, `containsValue`, and `evalMapMethodCall` implement these map operations for `map[string]any` and `orderedMap` receivers.
- METH-map-entrySet: FUTURE - `evalMapMethodCall` has no `entrySet` case.
- METH-memoryunit-toBytes, METH-memoryunit-toGiga, METH-memoryunit-toKilo, METH-memoryunit-toMega, METH-memoryunit-toUnit, METH-memoryunit-getBytes, METH-memoryunit-getGiga, METH-memoryunit-getKilo, METH-memoryunit-getMega: FUTURE - there is no MemoryUnit receiver/type support in `evalMethodCallExpr` or elsewhere in `groovy.go`.
- METH-path-divide, METH-path-leftShift: FUTURE - `evalBinaryExpr` does not implement path `/` or path `<<`; File/Path values are created as plain strings by `evalPathConstructor`.
- METH-path-baseName, METH-path-extension, METH-path-name, METH-path-parent, METH-path-scheme, METH-path-simpleName, METH-path-exists, METH-path-isDirectory, METH-path-isEmpty, METH-path-isFile, METH-path-isHidden, METH-path-isLink, METH-path-lastModified, METH-path-relativize, METH-path-resolve, METH-path-resolveSibling, METH-path-size, METH-path-toUriString, METH-path-getText, METH-path-withReader, METH-path-append, METH-path-setText, METH-path-write, METH-path-copyTo, METH-path-delete, METH-path-deleteDir, METH-path-getPermissions, METH-path-mkdir, METH-path-mkdirs, METH-path-mklink, METH-path-mklink-hard, METH-path-mklink-overwrite, METH-path-moveTo, METH-path-renameTo, METH-path-setPermissions, METH-path-setPermissions-3, METH-path-eachFile, METH-path-eachFileRecurse, METH-path-listDirectory, METH-path-listFiles, METH-path-countFasta, METH-path-countFastq, METH-path-countJson, METH-path-countLines, METH-path-splitCsv, METH-path-splitFasta, METH-path-splitFastq, METH-path-splitJson, METH-path-splitText: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-eachLine, METH-path-readLines: GAP - because File/Path constructors return strings, these calls would hit `evalStringMethodCall` and operate on the path string text itself rather than reading filesystem contents.
- METH-record-plus: FUTURE - record `+` as an operator is not implemented in `evalBinaryExpr`.
- METH-record-subMap: SUPPORTED - `evalMapMethodCall` implements `subMap`, which is the available record/map-like behaviour in `groovy.go`.
- METH-set-plus, METH-set-minus: FUTURE - set `+` and `-` operators are not implemented in `evalBinaryExpr` for `[]any` receivers.
- METH-set-membership, METH-set-intersect: SUPPORTED - set-like `[]any` values work with `containsValue` for `in` and with `evalListMethodCall` for `intersect`.
- METH-string-plus, METH-string-multiply, METH-string-getAt, METH-string-bitwiseNegate, METH-string-find, METH-string-match, METH-string-indexOf, METH-string-indexOf-2, METH-string-isBlank, METH-string-isEmpty, METH-string-isFloat, METH-string-lastIndexOf, METH-string-lastIndexOf-2, METH-string-length, METH-string-md5, METH-string-sha256, METH-string-strip, METH-string-stripLeading, METH-string-stripTrailing, METH-string-toFloat: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-contains, METH-string-endsWith, METH-string-isDouble, METH-string-isInteger, METH-string-isLong, METH-string-replace, METH-string-replaceAll, METH-string-replaceFirst, METH-string-startsWith, METH-string-stripIndent, METH-string-substring, METH-string-substring-2, METH-string-toBoolean, METH-string-toDouble, METH-string-toInteger, METH-string-toLong, METH-string-toLowerCase, METH-string-toUpperCase, METH-string-tokenize: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-string-execute: UNSUPPORTED - `evalStringMethodCall` explicitly returns `UnsupportedExpr` for `execute()`.
- METH-tuple-getAt: SUPPORTED - tuple-like `[]any` values use `evalIndexExpr` for indexing.
- METH-value-flatMap, METH-value-map, METH-value-subscribe, METH-value-view, METH-value-view-1: FUTURE - `evalMethodCallExpr` has no receiver handling for Nextflow value/channel objects beyond strings, maps, numbers, and `[]any`.
- METH-versionnumber-getMajor, METH-versionnumber-getMinor, METH-versionnumber-getPatch, METH-versionnumber-matches: FUTURE - there is no VersionNumber receiver/type support in `groovy.go`.
- METH-duration-toHours: FUTURE - `evalMethodCallExpr` only dispatches method calls for `string`, `map[string]any`/`orderedMap`, numbers, and `[]any`; there is no Duration receiver/type support in `groovy.go`.
- METH-duration-toMillis: FUTURE - `evalMethodCallExpr` only dispatches method calls for `string`, `map[string]any`/`orderedMap`, numbers, and `[]any`; there is no Duration receiver/type support in `groovy.go`.
- METH-duration-toMinutes: FUTURE - `evalMethodCallExpr` only dispatches method calls for `string`, `map[string]any`/`orderedMap`, numbers, and `[]any`; there is no Duration receiver/type support in `groovy.go`.
- METH-duration-toSeconds: FUTURE - `evalMethodCallExpr` only dispatches method calls for `string`, `map[string]any`/`orderedMap`, numbers, and `[]any`; there is no Duration receiver/type support in `groovy.go`.
- METH-duration-getDays: FUTURE - `evalMethodCallExpr` only dispatches method calls for `string`, `map[string]any`/`orderedMap`, numbers, and `[]any`; there is no Duration receiver/type support in `groovy.go`.
- METH-duration-getHours: FUTURE - `evalMethodCallExpr` only dispatches method calls for `string`, `map[string]any`/`orderedMap`, numbers, and `[]any`; there is no Duration receiver/type support in `groovy.go`.
- METH-duration-getMillis: FUTURE - `evalMethodCallExpr` only dispatches method calls for `string`, `map[string]any`/`orderedMap`, numbers, and `[]any`; there is no Duration receiver/type support in `groovy.go`.
- METH-duration-getMinutes: FUTURE - `evalMethodCallExpr` only dispatches method calls for `string`, `map[string]any`/`orderedMap`, numbers, and `[]any`; there is no Duration receiver/type support in `groovy.go`.
- METH-duration-getSeconds: FUTURE - `evalMethodCallExpr` only dispatches method calls for `string`, `map[string]any`/`orderedMap`, numbers, and `[]any`; there is no Duration receiver/type support in `groovy.go`.
- METH-iterable-collect: SUPPORTED - `evalListMethodCallExpr`, `evalListClosurePredicateMethod`, `evalListClosureMethod`, and `evalListMethodCall` implement these operations for `[]any` receivers.
- METH-iterable-collectMany: SUPPORTED - `evalListMethodCallExpr`, `evalListClosurePredicateMethod`, `evalListClosureMethod`, and `evalListMethodCall` implement these operations for `[]any` receivers.
- METH-iterable-contains: SUPPORTED - `evalListMethodCallExpr`, `evalListClosurePredicateMethod`, `evalListClosureMethod`, and `evalListMethodCall` implement these operations for `[]any` receivers.
- METH-iterable-every: SUPPORTED - `evalListMethodCallExpr`, `evalListClosurePredicateMethod`, `evalListClosureMethod`, and `evalListMethodCall` implement these operations for `[]any` receivers.
- METH-iterable-findAll: SUPPORTED - `evalListMethodCallExpr`, `evalListClosurePredicateMethod`, `evalListClosureMethod`, and `evalListMethodCall` implement these operations for `[]any` receivers.
- METH-iterable-groupBy: SUPPORTED - `evalListMethodCallExpr`, `evalListClosurePredicateMethod`, `evalListClosureMethod`, and `evalListMethodCall` implement these operations for `[]any` receivers.
- METH-iterable-inject-3: SUPPORTED - `evalListMethodCallExpr`, `evalListClosurePredicateMethod`, `evalListClosureMethod`, and `evalListMethodCall` implement these operations for `[]any` receivers.
- METH-iterable-isEmpty: SUPPORTED - `evalListMethodCallExpr`, `evalListClosurePredicateMethod`, `evalListClosureMethod`, and `evalListMethodCall` implement these operations for `[]any` receivers.
- METH-iterable-max: SUPPORTED - `evalListMethodCallExpr`, `evalListClosurePredicateMethod`, `evalListClosureMethod`, and `evalListMethodCall` implement these operations for `[]any` receivers.
- METH-iterable-max-1: SUPPORTED - `evalListMethodCallExpr`, `evalListClosurePredicateMethod`, `evalListClosureMethod`, and `evalListMethodCall` implement these operations for `[]any` receivers.
- METH-iterable-min: SUPPORTED - `evalListMethodCallExpr`, `evalListClosurePredicateMethod`, `evalListClosureMethod`, and `evalListMethodCall` implement these operations for `[]any` receivers.
- METH-iterable-min-1: SUPPORTED - `evalListMethodCallExpr`, `evalListClosurePredicateMethod`, `evalListClosureMethod`, and `evalListMethodCall` implement these operations for `[]any` receivers.
- METH-iterable-min-2: GAP - `evalListClosureMethod` supports the unary closure/scoring form for `max`/`min`, but not the two-argument comparator closure variant.
- METH-iterable-size: SUPPORTED - `evalListMethodCallExpr`, `evalListClosurePredicateMethod`, `evalListClosureMethod`, and `evalListMethodCall` implement these operations for `[]any` receivers.
- METH-iterable-sum: SUPPORTED - `evalListMethodCallExpr`, `evalListClosurePredicateMethod`, `evalListClosureMethod`, and `evalListMethodCall` implement these operations for `[]any` receivers.
- METH-iterable-sum-1: SUPPORTED - `evalListMethodCallExpr`, `evalListClosurePredicateMethod`, `evalListClosureMethod`, and `evalListMethodCall` implement these operations for `[]any` receivers.
- METH-iterable-toList: FUTURE - these method names are not implemented in `evalListMethodCallExpr` or `evalListMethodCall`.
- METH-iterable-toSet: SUPPORTED - `evalListMethodCallExpr`, `evalListClosurePredicateMethod`, `evalListClosureMethod`, and `evalListMethodCall` implement these operations for `[]any` receivers.
- METH-iterable-toSorted: FUTURE - these method names are not implemented in `evalListMethodCallExpr` or `evalListMethodCall`.
- METH-iterable-toSorted-1: FUTURE - these method names are not implemented in `evalListMethodCallExpr` or `evalListMethodCall`.
- METH-iterable-toSorted-2: FUTURE - these method names are not implemented in `evalListMethodCallExpr` or `evalListMethodCall`.
- METH-iterable-toUnique: FUTURE - these method names are not implemented in `evalListMethodCallExpr` or `evalListMethodCall`.
- METH-iterable-toUnique-2: FUTURE - these method names are not implemented in `evalListMethodCallExpr` or `evalListMethodCall`.
- METH-iterable-toUnique-1: FUTURE - these method names are not implemented in `evalListMethodCallExpr` or `evalListMethodCall`.
- METH-list-multiply: FUTURE - list `+` and `*` operators are not implemented in `evalBinaryExpr`; only integer arithmetic and list `<<` are handled there.
- METH-list-membership: SUPPORTED - `evalIndexExpr`, `containsValue`, `evalListClosurePredicateMethod`, and `evalListMethodCall` implement these list operations on `[]any`.
- METH-list-collate-3: FUTURE - these list method names are absent from `evalListMethodCallExpr` and `evalListMethodCall`.
- METH-list-find: SUPPORTED - `evalIndexExpr`, `containsValue`, `evalListClosurePredicateMethod`, and `evalListMethodCall` implement these list operations on `[]any`.
- METH-list-first: SUPPORTED - `evalIndexExpr`, `containsValue`, `evalListClosurePredicateMethod`, and `evalListMethodCall` implement these list operations on `[]any`.
- METH-list-getIndices: FUTURE - these list method names are absent from `evalListMethodCallExpr` and `evalListMethodCall`.
- METH-list-head: SUPPORTED - `evalIndexExpr`, `containsValue`, `evalListClosurePredicateMethod`, and `evalListMethodCall` implement these list operations on `[]any`.
- METH-list-indexOf: FUTURE - these list method names are absent from `evalListMethodCallExpr` and `evalListMethodCall`.
- METH-list-init: SUPPORTED - `evalIndexExpr`, `containsValue`, `evalListClosurePredicateMethod`, and `evalListMethodCall` implement these list operations on `[]any`.
- METH-list-last: SUPPORTED - `evalIndexExpr`, `containsValue`, `evalListClosurePredicateMethod`, and `evalListMethodCall` implement these list operations on `[]any`.
- METH-list-reverse: SUPPORTED - `evalIndexExpr`, `containsValue`, `evalListClosurePredicateMethod`, and `evalListMethodCall` implement these list operations on `[]any`.
- METH-list-subList: FUTURE - these list method names are absent from `evalListMethodCallExpr` and `evalListMethodCall`.
- METH-list-tail: SUPPORTED - `evalIndexExpr`, `containsValue`, `evalListClosurePredicateMethod`, and `evalListMethodCall` implement these list operations on `[]any`.
- METH-list-take: SUPPORTED - `evalIndexExpr`, `containsValue`, `evalListClosurePredicateMethod`, and `evalListMethodCall` implement these list operations on `[]any`.
- METH-list-takeWhile: FUTURE - these list method names are absent from `evalListMethodCallExpr` and `evalListMethodCall`.
- METH-list-withIndex: SUPPORTED - `evalIndexExpr`, `containsValue`, `evalListClosurePredicateMethod`, and `evalListMethodCall` implement these list operations on `[]any`.
- METH-map-membership: SUPPORTED - `evalIndexExpr`, `containsValue`, and `evalMapMethodCall` implement these map operations for `map[string]any` and `orderedMap` receivers.
- METH-map-any: SUPPORTED - `evalIndexExpr`, `containsValue`, and `evalMapMethodCall` implement these map operations for `map[string]any` and `orderedMap` receivers.
- METH-map-containsKey: SUPPORTED - `evalIndexExpr`, `containsValue`, and `evalMapMethodCall` implement these map operations for `map[string]any` and `orderedMap` receivers.
- METH-map-containsValue: SUPPORTED - `evalIndexExpr`, `containsValue`, and `evalMapMethodCall` implement these map operations for `map[string]any` and `orderedMap` receivers.
- METH-map-each: SUPPORTED - `evalIndexExpr`, `containsValue`, and `evalMapMethodCall` implement these map operations for `map[string]any` and `orderedMap` receivers.
- METH-map-every: SUPPORTED - `evalIndexExpr`, `containsValue`, and `evalMapMethodCall` implement these map operations for `map[string]any` and `orderedMap` receivers.
- METH-map-isEmpty: SUPPORTED - `evalIndexExpr`, `containsValue`, and `evalMapMethodCall` implement these map operations for `map[string]any` and `orderedMap` receivers.
- METH-map-keySet: SUPPORTED - `evalIndexExpr`, `containsValue`, and `evalMapMethodCall` implement these map operations for `map[string]any` and `orderedMap` receivers.
- METH-map-size: SUPPORTED - `evalIndexExpr`, `containsValue`, and `evalMapMethodCall` implement these map operations for `map[string]any` and `orderedMap` receivers.
- METH-map-subMap: SUPPORTED - `evalIndexExpr`, `containsValue`, and `evalMapMethodCall` implement these map operations for `map[string]any` and `orderedMap` receivers.
- METH-map-values: SUPPORTED - `evalIndexExpr`, `containsValue`, and `evalMapMethodCall` implement these map operations for `map[string]any` and `orderedMap` receivers.
- METH-memoryunit-toGiga: FUTURE - there is no MemoryUnit receiver/type support in `evalMethodCallExpr` or elsewhere in `groovy.go`.
- METH-memoryunit-toKilo: FUTURE - there is no MemoryUnit receiver/type support in `evalMethodCallExpr` or elsewhere in `groovy.go`.
- METH-memoryunit-toMega: FUTURE - there is no MemoryUnit receiver/type support in `evalMethodCallExpr` or elsewhere in `groovy.go`.
- METH-memoryunit-toUnit: FUTURE - there is no MemoryUnit receiver/type support in `evalMethodCallExpr` or elsewhere in `groovy.go`.
- METH-memoryunit-getBytes: FUTURE - there is no MemoryUnit receiver/type support in `evalMethodCallExpr` or elsewhere in `groovy.go`.
- METH-memoryunit-getGiga: FUTURE - there is no MemoryUnit receiver/type support in `evalMethodCallExpr` or elsewhere in `groovy.go`.
- METH-memoryunit-getKilo: FUTURE - there is no MemoryUnit receiver/type support in `evalMethodCallExpr` or elsewhere in `groovy.go`.
- METH-memoryunit-getMega: FUTURE - there is no MemoryUnit receiver/type support in `evalMethodCallExpr` or elsewhere in `groovy.go`.
- METH-path-leftShift: FUTURE - `evalBinaryExpr` does not implement path `/` or path `<<`; File/Path values are created as plain strings by `evalPathConstructor`.
- METH-path-extension: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-name: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-parent: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-scheme: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-simpleName: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-exists: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-isDirectory: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-isEmpty: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-isFile: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-isHidden: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-isLink: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-lastModified: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-relativize: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-resolve: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-resolveSibling: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-size: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-toUriString: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-getText: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-readLines: GAP - because File/Path constructors return strings, these calls would hit `evalStringMethodCall` and operate on the path string text itself rather than reading filesystem contents.
- METH-path-withReader: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-append: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-setText: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-write: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-copyTo: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-delete: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-deleteDir: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-getPermissions: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-mkdir: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-mkdirs: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-mklink: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-mklink-hard: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-mklink-overwrite: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-moveTo: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-renameTo: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-setPermissions: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-setPermissions-3: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-eachFile: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-eachFileRecurse: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-listDirectory: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-listFiles: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-countFasta: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-countFastq: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-countJson: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-countLines: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-splitCsv: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-splitFasta: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-splitFastq: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-splitJson: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-splitText: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-set-minus: FUTURE - set `+` and `-` operators are not implemented in `evalBinaryExpr` for `[]any` receivers.
- METH-set-intersect: SUPPORTED - set-like `[]any` values work with `containsValue` for `in` and with `evalListMethodCall` for `intersect`.
- METH-string-multiply: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-getAt: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-bitwiseNegate: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-find: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-match: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-endsWith: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-string-indexOf: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-indexOf-2: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-isBlank: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-isEmpty: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-isDouble: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-string-isFloat: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-isInteger: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-string-isLong: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-string-lastIndexOf: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-lastIndexOf-2: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-length: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-md5: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-replace: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-string-replaceAll: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-string-replaceFirst: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-string-sha256: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-startsWith: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-string-strip: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-stripIndent: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-string-stripLeading: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-stripTrailing: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-substring: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-string-substring-2: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-string-toBoolean: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-string-toDouble: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-string-toFloat: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-toInteger: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-string-toLong: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-string-toLowerCase: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-string-toUpperCase: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-string-tokenize: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-value-map: FUTURE - `evalMethodCallExpr` has no receiver handling for Nextflow value/channel objects beyond strings, maps, numbers, and `[]any`.
- METH-value-subscribe: FUTURE - `evalMethodCallExpr` has no receiver handling for Nextflow value/channel objects beyond strings, maps, numbers, and `[]any`.
- METH-value-view: FUTURE - `evalMethodCallExpr` has no receiver handling for Nextflow value/channel objects beyond strings, maps, numbers, and `[]any`.
- METH-value-view-1: FUTURE - `evalMethodCallExpr` has no receiver handling for Nextflow value/channel objects beyond strings, maps, numbers, and `[]any`.
- METH-versionnumber-getMinor: FUTURE - there is no VersionNumber receiver/type support in `groovy.go`.
- METH-versionnumber-getPatch: FUTURE - there is no VersionNumber receiver/type support in `groovy.go`.
- METH-versionnumber-matches: FUTURE - there is no VersionNumber receiver/type support in `groovy.go`.
- METH-duration-toDays: FUTURE - `evalMethodCallExpr` only dispatches method calls for `string`, `map[string]any`/`orderedMap`, numbers, and `[]any`; there is no Duration receiver/type support in `groovy.go`.
- METH-iterable-any: SUPPORTED - `evalListMethodCallExpr`, `evalListClosurePredicateMethod`, `evalListClosureMethod`, and `evalListMethodCall` implement these operations for `[]any` receivers.
- METH-iterable-each: FUTURE - these method names are not implemented in `evalListMethodCallExpr` or `evalListMethodCall`.
- METH-iterable-max-2: GAP - `evalListClosureMethod` supports the unary closure/scoring form for `max`/`min`, but not the two-argument comparator closure variant.
- METH-list-plus: FUTURE - list `+` and `*` operators are not implemented in `evalBinaryExpr`; only integer arithmetic and list `<<` are handled there.
- METH-list-getAt: SUPPORTED - `evalIndexExpr`, `containsValue`, `evalListClosurePredicateMethod`, and `evalListMethodCall` implement these list operations on `[]any`.
- METH-list-collate: FUTURE - these list method names are absent from `evalListMethodCallExpr` and `evalListMethodCall`.
- METH-map-getAt: SUPPORTED - `evalIndexExpr`, `containsValue`, and `evalMapMethodCall` implement these map operations for `map[string]any` and `orderedMap` receivers.
- METH-memoryunit-toBytes: FUTURE - there is no MemoryUnit receiver/type support in `evalMethodCallExpr` or elsewhere in `groovy.go`.
- METH-path-divide: FUTURE - `evalBinaryExpr` does not implement path `/` or path `<<`; File/Path values are created as plain strings by `evalPathConstructor`.
- METH-path-baseName: FUTURE - `evalPathConstructor` returns plain strings and there is no path-specific property or method dispatcher in `groovy.go`.
- METH-path-eachLine: GAP - because File/Path constructors return strings, these calls would hit `evalStringMethodCall` and operate on the path string text itself rather than reading filesystem contents.
- METH-set-plus: FUTURE - set `+` and `-` operators are not implemented in `evalBinaryExpr` for `[]any` receivers.
- METH-set-membership: SUPPORTED - set-like `[]any` values work with `containsValue` for `in` and with `evalListMethodCall` for `intersect`.
- METH-string-plus: FUTURE - these operator/method/property cases are not implemented in `evalBinaryExpr`, `evalIndexExpr`, `evalUnaryExpr`, or `evalStringMethodCall`.
- METH-string-contains: SUPPORTED - these cases are implemented directly in `evalStringMethodCall`.
- METH-value-flatMap: FUTURE - `evalMethodCallExpr` has no receiver handling for Nextflow value/channel objects beyond strings, maps, numbers, and `[]any`.
- METH-versionnumber-getMajor: FUTURE - there is no VersionNumber receiver/type support in `groovy.go`.
