# Operators — Combining

**Source:** https://nextflow.io/docs/latest/reference/operator.html

## Features

### CO-mix
`mix(other1, other2, ...)` — merge items from multiple channels into one.
Items may appear in any order.
```groovy
c1.mix(c2, c3)
```

### CO-join
`join(other, [options])` — inner product by matching key.
Transforms `(K, V1, V2..)` and `(K, W1, W2..)` into `(K, V1, V2.., W1, W2..)`.
Options:
- `by:` — key index or list (default: [0])
- `remainder:` — emit unmatched items (default: false)
- `failOnDuplicate:` — error on duplicate keys (default: false)
- `failOnMismatch:` — error on unmatched keys (default: false)
```groovy
left.join(right)
left.join(right, by: 0, remainder: true)
```

### CO-combine
`combine(other, [options])` — cross product (Cartesian product).
Without `by`: every item from left × every item from right.
With `by:` — only combine items sharing the matching key(s), flattening pairs.
```groovy
numbers.combine(words)                    // full cross product
source.combine(target, by: 0)            // keyed cross product
```
Note: `combine` with `by:` merges and flattens pairs; `cross` does not.

### CO-concat
`concat(other1, other2, ...)` — emit all items from channels in order.
Items from channel i+1 are emitted only after all from channel i.
```groovy
a.concat(b, c)
```

### CO-cross
`cross(other [, closure])` — emit pairwise combinations with matching key.
Key default: first element of tuple, or the value itself.
Each pair is emitted as `[[left_tuple], [right_tuple]]` (NOT flattened).
Optional closure defines custom key.
Caveats: not commutative; source channels should not have duplicate keys.
```groovy
source.cross(target)
source.cross(target) { v -> v[1][0] }   // custom key
```

### CO-merge
`merge(other [, closure])` — **deprecated/non-deterministic**. Joins items
positionally. Use `combine`/`join` instead.
Optional closure transforms pairs.
```groovy
odds.merge(evens)
odds.merge(evens) { a, b -> tuple(b*b, a) }
```
