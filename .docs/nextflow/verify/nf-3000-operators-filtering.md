# Operators — Filtering

**Source:** https://nextflow.io/docs/latest/reference/operator.html

## Features

### CO-filter
`filter` — emit items satisfying a condition. Condition can be:
- **Closure:** `filter { v -> v > 2 }` (`CO-filter-closure`)
- **Literal:** `filter('a')` — keep items equal to literal (`CO-filter-literal`)
- **Regex:** `filter(~/^a.*/)` — keep items matching pattern (`CO-filter-regex`)
- **Type:** `filter(Number)` — keep items of given type (`CO-filter-type`)

### CO-first
`first` — emit first item, optionally matching a condition.
- **No args:** `first()` — first item (`CO-first-noarg`)
- **Regex:** `first(~/aa.*/)` (`CO-first-regex`)
- **Type:** `first(String)` (`CO-first-type`)
- **Closure:** `first { v -> v > 3 }` (`CO-first-closure`)

### CO-last
`last` — emit the last item from the channel.
No variants; always emits the final item.

### CO-take
`take(n)` — emit the first N items.
```groovy
Channel.of(1..10).take(3)   // 1, 2, 3
```
`take(-1)` takes all values.

### CO-unique
`unique` — emit unique items (removes ALL duplicates, not just consecutive).
- **No args:** natural equality
- **Closure:** `unique { v -> v % 2 }` — uniqueness by transformed value

### CO-distinct
`distinct` — emit items removing CONSECUTIVE duplicates only.
- **No args:** natural equality
- **Closure:** `distinct { v -> v % 2 }` — by transformed value

### CO-until
`until { condition }` — emit items until condition is true (item satisfying
condition is NOT emitted).
```groovy
Channel.of(3, 2, 1, 5, 1, 5).until { v -> v == 5 }  // 3, 2, 1
```
