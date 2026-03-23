# Test: Operators — Splitting

**Spec files:** nf-3300-operators-splitting.md
**Impl files:** impl-02-channel-operators.md

## Task

For each feature ID in nf-3300-operators-splitting.md, determine its classification.

### Checklist

1. Check applyChannelOperator switch for split operators
2. Check splitCSVChannelItems, splitFASTAChannelItems, etc.
3. Verify option support (header, by, sep, etc.)
4. Classify per 00-instructions.md criteria

### Features to classify

- CO-splitCsv, CO-splitCsv-header, CO-splitCsv-sep, CO-splitCsv-by,
  CO-splitCsv-strip, CO-splitCsv-charset
- CO-splitJson, CO-splitJson-path
- CO-splitText, CO-splitText-by, CO-splitText-file, CO-splitText-elem,
  CO-splitText-keepHeader, CO-splitText-charset, CO-splitText-compress,
  CO-splitText-decompress
- CO-splitFasta, CO-splitFasta-by, CO-splitFasta-file, CO-splitFasta-record,
  CO-splitFasta-size, CO-splitFasta-elem
- CO-splitFastq, CO-splitFastq-by, CO-splitFastq-pe,
  CO-splitFastq-file, CO-splitFastq-record, CO-splitFastq-elem
- CO-collectFile, CO-collectFile-name, CO-collectFile-storeDir,
  CO-collectFile-sort, CO-collectFile-seed, CO-collectFile-newLine

### Output format

```
CO-splitCsv: SUPPORTED | reason
...
```
