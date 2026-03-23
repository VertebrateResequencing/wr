# Test: Operators: Transforming

**Spec files:** nf-3100-operators-transforming.md
**Impl files:** impl-02-channel-operators.md

## Task

For each feature ID in nf-3100-operators-transforming.md, determine its classification.

### Checklist

1. Read nf-3100-operators-transforming.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- CO-buffer, CO-collate, CO-collect, CO-count, CO-flatMap
  CO-flatten, CO-groupTuple, CO-map, CO-max, CO-min
  CO-reduce, CO-sum, CO-toInteger, CO-toList, CO-toSortedList
  CO-transpose

### Output format

```
CO-buffer: SUPPORTED | reason
...
```
