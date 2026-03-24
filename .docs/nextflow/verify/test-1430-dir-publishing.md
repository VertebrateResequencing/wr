# Test: Directives: Publishing

**Spec files:** nf-1430-dir-publishing.md
**Impl files:** impl-05-translate-directives.md

## Task

For each feature ID in nf-1430-dir-publishing.md, determine its classification.

### Checklist

1. Read nf-1430-dir-publishing.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- DIR-publishDir, DIR-stageInMode, DIR-stageOutMode

### Output format

```
DIR-publishDir: SUPPORTED | reason
...
```

## Results

- DIR-publishDir: GAP — `applyPublishDirBehaviours` and `buildPublishDirCommand` in `nextflowdsl/translate.go` do add publish actions, but `parsePublishDir` in `nextflowdsl/parse.go` defaults the mode to `"copy"` and `publishDirActionCommand` only treats `link` and `move` specially, falling back to copy for other modes. That does not match Nextflow `publishDir` semantics, whose default is symlink-based publishing and which supports additional mode variants/options.
- DIR-stageInMode: GAP — `parseDirectiveExpr` in `nextflowdsl/parse.go` accepts and stores `stageInMode` in `proc.Directives`, but `warnStoredDirective` marks it as stored without translation support and there is no corresponding handling in `nextflowdsl/translate.go`, so it parses but has no effect on job staging.
- DIR-stageOutMode: GAP — `parseDirectiveExpr` in `nextflowdsl/parse.go` accepts and stores `stageOutMode` in `proc.Directives`, but `warnStoredDirective` marks it as stored without translation support and there is no corresponding handling in `nextflowdsl/translate.go`, so it parses but has no effect on output staging.
