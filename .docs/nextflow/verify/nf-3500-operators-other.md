# Operators — Other

**Source:** https://nextflow.io/docs/latest/reference/operator.html

## Viewing / Debugging

### CO-view
`view([options]) [{ closure }]` — print items to stdout AND pass through.
Optional closure transforms display. Option: `newLine:` (default: true).
```groovy
Channel.of(1,2,3).view { v -> "val: $v" }
```

### CO-dump
`dump([options])` — print items only when `-dump-channels` is specified.
Options: `tag:` (required for selective dumping), `pretty:` (JSON format).
```groovy
Channel.of(1,2,3).dump(tag: 'debug1')
```

## Buffering / Grouping

### CO-buffer
`buffer` — collect items into subsets. Multiple variants:
- `buffer(closingCondition)` — emit subset when condition met. Condition:
  literal, regex, type, or closure. (`CO-buffer-close`)
- `buffer(openingCondition, closingCondition)` — bounded subsets
  (`CO-buffer-open-close`)
- `buffer(size: n, remainder: true|false)` — fixed-size chunks
  (`CO-buffer-size`)
- `buffer(size: n, skip: m, remainder: true|false)` — sliding window
  (`CO-buffer-skip`)

### CO-collate
`collate(size [, remainder]) ` — group items into sublists.
- `collate(size)` — groups of N, remainder emitted by default (`CO-collate-size`)
- `collate(size, false)` — discard partial group
- `collate(size, step [, remainder])` — sliding window (`CO-collate-step`)

Equivalent to `buffer(size: n, remainder: true|false)`.

## Aggregation

### CO-min
`min([closure])` — emit minimum value.
Closure can be mapper or comparator (2-arg).

### CO-max
`max([closure])` — emit maximum value.
Closure can be mapper or comparator (2-arg).

### CO-sum
`sum([closure])` — emit sum. Optional closure transforms before summing.

### CO-randomSample
`randomSample(n [, seed])` — emit N random items (without replacement).

## Type Conversion

### CO-toInteger
`toInteger` — convert string items to integers. Equivalent to `map { v -> v as Integer }`.

### CO-toLong
`toLong` — convert to long.

### CO-toFloat
`toFloat` — convert to float.

### CO-toDouble
`toDouble` — convert to double.

## Terminal / Side-Effect

### CO-subscribe
`subscribe([handlers])` — invoke function per item. Terminal (no output).
Handlers: `onNext:`, `onComplete:`, `onError:`.
```groovy
Channel.of(1,2,3).subscribe { v -> println v }
```

## Deprecated Counting

### CO-countFasta
`countFasta` — count records in FASTA. Equivalent to `splitFasta | count`.

### CO-countFastq
`countFastq` — count records in FASTQ. Equivalent to `splitFastq | count`.

### CO-countJson
`countJson` — count records in JSON. Equivalent to `splitJson | count`.

### CO-countLines
`countLines` — count lines in text. Equivalent to `splitText | count`.
