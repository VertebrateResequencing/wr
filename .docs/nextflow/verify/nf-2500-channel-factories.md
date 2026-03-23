# Channel Factories

**Source:** https://nextflow.io/docs/latest/reference/channel.html

## Features

### CF-of
`Channel.of(items...)` — create channel emitting each argument.
```groovy
Channel.of(1, 2, 3)
Channel.of('a'..'z')   // range expansion
```

### CF-value
`Channel.value(x)` — create a value channel (single item, can be read multiple
times).
```groovy
Channel.value('hello')
Channel.value([1, 2, 3])
```

### CF-empty
`Channel.empty()` — create an empty channel.

### CF-from
`Channel.from(items...)` — **deprecated**, use `of` instead. Same as `of` but
flattens list arguments.
```groovy
Channel.from(1, [2, 3])  // emits: 1, 2, 3 (flattened)
```

### CF-fromList
`Channel.fromList(list)` — create channel from a list, emitting each element.
```groovy
Channel.fromList(['a', 'b', 'c'])
```

### CF-fromPath
`Channel.fromPath(pattern, [options])` — emit files matching glob pattern.
```groovy
Channel.fromPath('/data/*.fq')
Channel.fromPath('/data/**/*.fq', glob: true)
```
Options: `glob` (default: true), `type` ('file'|'dir'|'any'), `hidden`,
`maxDepth`, `followLinks`, `checkIfExists`.

### CF-fromFilePairs
`Channel.fromFilePairs(pattern, [options])` — emit `[key, [file1, file2]]`
tuples for paired files.
```groovy
Channel.fromFilePairs('/data/*_{1,2}.fq')
```
Options: `size` (default: 2), `flat`, `checkIfExists`, `followLinks`,
`maxDepth`, `glob`, `type`.

### CF-fromSRA
`Channel.fromSRA(accession, [options])` — fetch data from NCBI SRA.
Requires network access and NCBI API credentials.

### CF-watchPath
`Channel.watchPath(pattern, event)` — watch filesystem for changes.
Events: `'create'`, `'modify'`, `'delete'`. Produces an infinite channel.

### CF-interval
`Channel.interval(duration)` — emit incrementing integers at regular intervals.
Duration in milliseconds. Produces an infinite channel.

### CF-topic
`Channel.topic(name)` — create/subscribe to a named channel topic.

### CF-fromLineage
`Channel.fromLineage(query)` — query data lineage. Experimental.
