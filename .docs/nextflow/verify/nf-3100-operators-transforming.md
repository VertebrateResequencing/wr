# Operators — Transforming

**Source:** https://nextflow.io/docs/latest/reference/operator.html

## Features

### CO-map
`map { closure }` — transform each item. `null` values are NOT emitted.
```groovy
Channel.of(1,2,3).map { v -> v * v }  // 1, 4, 9
```

### CO-flatMap
`flatMap { closure }` — transform each item; if result is a list, emit each
element separately. If result is a map, emit each key-value pair.
```groovy
Channel.of(1,2,3).flatMap { n -> [n, n*2, n*3] }
```

### CO-flatten
`flatten` — deep-flatten nested lists in each item.
```groovy
Channel.of([1, [2, 3]], 4).flatten()  // 1, 2, 3, 4
```

### CO-collect
`collect([options]) [{ closure }]` — collect all items into a single list.
Options: `flat` (default: true), `sort` (default: false).
```groovy
Channel.of(1,2,3).collect()  // [1, 2, 3]
Channel.of('hello','world').collect { v -> v.length() }  // [5, 5]
```

### CO-groupTuple
`groupTuple([options])` — group tuples by key.
Options: `by:` (default: [0]), `size:`, `remainder:` (default: false), `sort:`.
```groovy
Channel.of([1,'A'],[1,'B'],[2,'C']).groupTuple()  // [1,[A,B]], [2,[C]]
```
`size:` enables early emission. `groupKey()` function provides per-key sizes.

### CO-transpose
`transpose([options])` — inverse of groupTuple: flatten list elements.
Options: `by:` (default: all lists), `remainder:` (default: false).
```groovy
Channel.of([1,['A','B']]).transpose()  // [1,A], [1,B]
```

### CO-toList
`toList` — collect all items into a list (single emission). Emits `[]` for
empty channels (unlike `collect` which emits nothing).

### CO-toSortedList
`toSortedList([comparator])` — collect all items into a sorted list.
Optional comparator closure.

### CO-reduce
`reduce([seed]) { acc, v -> ... }` — fold/accumulate items.
```groovy
Channel.of(1,2,3,4,5).reduce { a, b -> a + b }  // 15
Channel.of(1,2,3).reduce('init:') { acc, v -> acc + " $v" }
```
First item is initial accumulator if no seed provideed.

### CO-count
`count([filter])` — count items. Optional filter can be:
- **No args:** count all (`CO-count-noarg`)
- **Literal:** `count(1)` (`CO-count-literal`)
- **Regex:** `count(~/c/)` (`CO-count-regex`)
- **Closure:** `count { v -> v <= 'c' }` (`CO-count-closure`)

### CO-ifEmpty
`ifEmpty(value)` — emit value if source channel is empty.
```groovy
Channel.empty().ifEmpty('fallback')  // 'fallback'
```
Value can also be a closure.
