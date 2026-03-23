# Test: Directives — Publishing

**Spec files:** nf-1430-dir-publishing.md
**Impl files:** impl-06-translate-command.md, impl-07-config.md, impl-09-ast.md

## Task

For each feature ID in nf-1430-dir-publishing.md, determine its classification.

### Checklist

1. Check PublishDir struct (ast.go) for field support
2. Check normalizeConfigPublishDirs (config.go) for config parsing
3. Check translateProcessBindingSets (translate.go) for publish handling
4. Classify per 00-instructions.md criteria

### Features to classify

- DIR-publishDir, DIR-publishDir-path, DIR-publishDir-mode,
  DIR-publishDir-pattern, DIR-publishDir-overwrite,
  DIR-publishDir-saveAs, DIR-publishDir-enabled, DIR-publishDir-tags,
  DIR-publishDir-contentType, DIR-publishDir-failOnError,
  DIR-publishDir-multiple
- DIR-debug

### Output format

```
DIR-publishDir: SUPPORTED | reason
...
```
