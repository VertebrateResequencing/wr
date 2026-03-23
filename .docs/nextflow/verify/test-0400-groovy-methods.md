# Test: Groovy Methods

**Spec files:** nf-0400-groovy-methods.md
**Impl files:** impl-08-groovy.md

## Task

For each feature ID in nf-0400-groovy-methods.md, determine its classification.

### Checklist

1. Check evalStringMethodCall (groovy.go line 1735) for METH-str-*
2. Check evalListMethodCall (groovy.go line 3587) + evalListMethodCallExpr (2964) for METH-list-*
3. Check evalMapMethodCall (groovy.go line 2257) for METH-map-*
4. Check evalNumberMethodCall (groovy.go line 2853) for METH-num-*
5. Check file/path method handling for METH-file-*
6. Check evalNewExpr (groovy.go line 4087) for METH-new-*
7. Classify per 00-instructions.md criteria

### Features to classify (sample — full list in spec)

String: METH-str-contains, METH-str-startsWith, METH-str-endsWith,
METH-str-replace, METH-str-replaceAll, METH-str-split, METH-str-trim,
METH-str-toUpperCase, METH-str-toLowerCase, METH-str-substring,
METH-str-indexOf, METH-str-padLeft, METH-str-padRight, METH-str-capitalize,
METH-str-size, METH-str-length, METH-str-matches, METH-str-tokenize,
METH-str-stripIndent, METH-str-isNumber, METH-str-isInteger

List: METH-list-add, METH-list-addAll, METH-list-remove, METH-list-get,
METH-list-size, METH-list-contains, METH-list-isEmpty, METH-list-flatten,
METH-list-collect, METH-list-find, METH-list-findAll, METH-list-each,
METH-list-inject, METH-list-groupBy, METH-list-sort, METH-list-min,
METH-list-max, METH-list-sum, METH-list-any, METH-list-every,
METH-list-unique, METH-list-reverse, METH-list-first, METH-list-last,
METH-list-take, METH-list-drop, METH-list-join

Map: METH-map-get, METH-map-containsKey, METH-map-keySet, METH-map-values,
METH-map-each, METH-map-collect, METH-map-find, METH-map-findAll,
METH-map-sort, METH-map-subMap, METH-map-plus

Number: METH-num-abs, METH-num-intdiv, METH-num-toInteger, METH-num-times

Constructor: METH-new-File, METH-new-ArrayList, METH-new-HashMap,
METH-new-BigDecimal, METH-new-BigInteger, METH-new-Date, METH-new-Random,
METH-new-String

### Output format

```
METH-str-contains: SUPPORTED | reason
...
```
