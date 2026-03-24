# Test: Operators: Splitting

**Spec files:** nf-3300-operators-splitting.md
**Impl files:** impl-02-channel-operators.md

## Task

For each feature ID in nf-3300-operators-splitting.md, determine its classification.

### Checklist

1. Read nf-3300-operators-splitting.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- CO-countFasta, CO-countFastq, CO-countJson, CO-countLines, CO-splitCsv
  CO-splitFasta, CO-splitFastq, CO-splitJson, CO-splitText

### Output format

```
CO-countFasta: SUPPORTED | reason
...
```

## Results

- CO-countFasta: GAP — `applyChannelOperator` does not implement a `countFasta` case; the deprecated fallback in `deprecatedChannelOperators` warns and returns `cloneChannelItems(items)` unchanged, so FASTA records are not counted.
- CO-countFastq: GAP — `applyChannelOperator` does not implement a `countFastq` case; the deprecated fallback in `deprecatedChannelOperators` warns and returns `cloneChannelItems(items)` unchanged, so FASTQ records are not counted.
- CO-countJson: GAP — `applyChannelOperator` does not implement a `countJson` case; the deprecated fallback in `deprecatedChannelOperators` warns and returns `cloneChannelItems(items)` unchanged, so JSON elements are not counted.
- CO-countLines: GAP — `applyChannelOperator` does not implement a `countLines` case; the deprecated fallback in `deprecatedChannelOperators` warns and returns `cloneChannelItems(items)` unchanged, so input lines are not counted.
- CO-splitCsv: SUPPORTED — `applyChannelOperator` dispatches `splitCsv` to `splitCSVChannelItems`, and `splitCSVFile` reads CSV files and emits rows, with `header` and `by` options implemented.
- CO-splitFasta: SUPPORTED — `applyChannelOperator` dispatches `splitFasta` to `splitFASTAChannelItems`, and `splitFASTAFile` splits FASTA input on `>` record boundaries with `by` chunking support.
- CO-splitFastq: SUPPORTED — `applyChannelOperator` dispatches `splitFastq` to `splitFASTQChannelItems`, and `splitFASTQValue`/`splitFASTQFile` split FASTQ records with `by` chunking and `pe:true` paired-end handling.
- CO-splitJson: SUPPORTED — `applyChannelOperator` dispatches `splitJson` to `splitJSONChannelItems`, and `splitJSONFile` decodes JSON and emits either array elements or the selected `path` value.
- CO-splitText: SUPPORTED — `applyChannelOperator` dispatches `splitText` to `splitTextChannelItems`, and `splitTextFile` emits line-based chunks with `by` support.
