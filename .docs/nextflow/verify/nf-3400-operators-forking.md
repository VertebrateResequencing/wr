# Operators — Forking / Routing

**Source:** https://nextflow.io/docs/latest/reference/operator.html

## Features

### CO-branch
`branch { criteria }` — route each item to one of multiple named output channels.
Each branch has a label and a boolean condition; first matching wins.
Use `true` as fallback. Optional transform expression per branch.
```groovy
Channel.of(1,2,3,40,50)
    .branch { v ->
        small: v < 10
        large: v > 10
        other: true   // fallback
    }.set { result }
result.small.view()
result.large.view()
```
`branchCriteria {}` creates reusable criteria.

### CO-multiMap
`multiMap { criteria }` — emit each item to multiple named output channels.
Unlike `branch`, EVERY output receives the item (subject to its transform).
```groovy
Channel.of(1,2,3)
    .multiMap { v ->
        doubled: v * 2
        squared: v * v
    }.set { result }
```
Multiple labels can share the same expression with shorthand:
`alpha: beta: v`.
`multiMapCriteria {}` creates reusable criteria.

### CO-tap
`tap { variable }` — assign channel to variable and pass through.
```groovy
Channel.of('a','b').tap { log1 }.map { v -> v * 2 }.tap { log2 }
```
Best practice: use standard assignment instead.

### CO-set
`set { variable }` — assign channel to variable (terminal).
```groovy
Channel.of(1,2,3).set { my_channel }
```
Equivalent to `my_channel = Channel.of(1,2,3)`.
Best practice: use standard assignment instead.
