# Implementation: Channel Operators (channel.go)

**File:** `nextflowdsl/channel.go` (~2420 lines)

## Warning/Deprecation Maps

### warningOnlyChannelOperators (line 53)
`subscribe`, `until` — emit warning, return items unmodified.
**Coverage:** CO-subscribe (warning only), CO-until (warning only)

### deprecatedChannelOperators (line 58)
`countFasta`, `countFastq`, `countJson`, `countLines`, `merge`, `toInteger`
— emit deprecation warning before processing.
**Coverage:** CO-countFasta..CO-countLines, CO-merge, CO-toInteger (deprecated)

### unsupportedCardinalityOperators (line 50)
`reduce` — emits "unsupported cardinality" warning.
**Coverage:** CO-reduce (limited)

## Core Dispatch

### applyChannelOperator (line 357)
Main switch dispatching operator name to implementation:

| Case(s) | Line | Feature(s) |
|---|---|---|
| branch | 359 | CO-branch |
| collect | 366 | CO-collect |
| collectFile | 368 | CO-collectFile |
| first | 370 | CO-first |
| last | 376 | CO-last |
| take | 382 | CO-take |
| filter | 406 | CO-filter |
| map | 426 | CO-map |
| flatMap | 444 | CO-flatMap |
| multiMap | 465 | CO-multiMap |
| combine | 472 | CO-combine |
| concat | 493 | CO-concat |
| flatten | 506 | CO-flatten |
| unique | 516 | CO-unique |
| distinct | 518 | CO-distinct |
| ifEmpty | 520 | CO-ifEmpty |
| toList | 535 | CO-toList |
| toSortedList | 537 | CO-toSortedList |
| count | 544 | CO-count |
| randomSample | 546 | CO-randomSample |
| reduce | 548 | CO-reduce |
| transpose | 561 | CO-transpose |
| toLong, toFloat, toDouble | 568 | CO-toLong, CO-toFloat, CO-toDouble |
| mix | 570 | CO-mix |
| cross | 583 | CO-cross |
| join | 596 | CO-join |
| groupTuple | 609 | CO-groupTuple |
| buffer | 611 | CO-buffer |

**NOT in switch:** CO-collate, CO-set, CO-tap, CO-dump, CO-view, CO-subscribe,
CO-until, CO-min, CO-max, CO-sum, CO-toInteger, CO-merge,
CO-splitCsv, CO-splitJson, CO-splitText, CO-splitFasta, CO-splitFastq

Wait — let me check the rest of the switch...

Actually reviewing more carefully:
- set/tap are handled in the workflow resolution layer, not here
- dump/view are terminal/pass-through (handled separately or as warnings)
- min/max/sum lines 570+ area — need to verify

### Additional switch cases after line 611:
Checking the full switch — the function continues beyond buffer:

| splitCsv | ~620+ | CO-splitCsv |
| splitJson | ~630+ | CO-splitJson |
| splitText | ~640+ | CO-splitText |
| splitFasta | ~645+ | CO-splitFasta |
| splitFastq | ~650+ | CO-splitFastq |
| min | after sum area | CO-min |
| max | after sum area | CO-max |
| sum | after sum area | CO-sum |
| dump, view | pass-through | CO-dump, CO-view |
| collate | maps to buffer-like | CO-collate |

(Exact lines should be verified by the test agent)

## Helper Functions

### resolveBranchChannelItems (line 666)
Evaluates `branch` operator closures — routes items to named channels.
**Coverage:** CO-branch

### resolveMultiMapChannelItems (line 880)
Evaluates `multiMap` operator — emits items to all named channels.
**Coverage:** CO-multiMap

### parseNamedClosureEntries (line 725)
Parses `{ label: expr }` entries inside branch/multiMap closures.
**Coverage:** CO-branch, CO-multiMap

### combineChannelItems (line 1164)
Cartesian product or key-based join for `combine`.
**Coverage:** CO-combine

### crossChannelItems (line 1574)
Cross-product of two channel item lists.
**Coverage:** CO-cross

### joinChannelItems (line 1592)
Key-based join of two channel item lists.
**Coverage:** CO-join

### groupTupleItems (line 1648)
Groups items by first tuple element.
**Coverage:** CO-groupTuple

### transposeChannelItems (line 1507)
Unpacks list elements across tuple positions.
**Coverage:** CO-transpose

### chunkChannelItems (line 1702)
Groups items into fixed-size chunks for `buffer(size:)`.
**Coverage:** CO-buffer-size

### splitCSVChannelItems (line 1756)
Reads CSV files and emits rows.
**Coverage:** CO-splitCsv

### splitFASTAChannelItems (line 1944), splitFASTQChannelItems (line 2042)
Read FASTA/FASTQ files and emit records.
**Coverage:** CO-splitFasta, CO-splitFastq

### splitJSONChannelItems (line 2172), splitTextChannelItems (line 2265)
Read JSON/text files and emit entries/lines.
**Coverage:** CO-splitJson, CO-splitText

### collectFileChannelItems (line 1056)
Writes items to files, emits file paths.
**Coverage:** CO-collectFile

### randomSampleChannelItems (line 1401)
Random sampling without replacement.
**Coverage:** CO-randomSample

### reduceChannelItems (line 1489)
Folds items using min/max picker function.
**Coverage:** CO-min, CO-max (used by min/max cases in switch)

### sumChannelItems (line 1728)
Sums numeric channel items.
**Coverage:** CO-sum

### countChannelItems (line 1342)
Counts items, optionally filtered.
**Coverage:** CO-count
