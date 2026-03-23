# Test: Standard Library Types

**Spec files:** nf-0500-stdlib-types.md
**Impl files:** impl-08-groovy.md, impl-01-parse.md

## Task

For each feature ID in nf-0500-stdlib-types.md, determine its classification.

### Checklist

1. Check if Duration/MemoryUnit literals are parsed (impl-01-parse.md)
2. Check if Duration/MemoryUnit arithmetic is evaluated (impl-08-groovy.md)
3. Check if Path methods are handled (impl-08-groovy.md method dispatch)
4. Check if collection types (Bag, Set, Tuple, Record) are handled
5. Classify per 00-instructions.md criteria

### Features to classify

- TYPE-duration-literal, TYPE-duration-cast, TYPE-duration-arithmetic,
  TYPE-duration-methods
- TYPE-memunit-literal, TYPE-memunit-cast, TYPE-memunit-arithmetic,
  TYPE-memunit-methods
- TYPE-path-operators, TYPE-path-attributes, TYPE-path-attr-methods,
  TYPE-path-reading, TYPE-path-writing, TYPE-path-fs-ops,
  TYPE-path-listing, TYPE-path-splitting
- TYPE-bag, TYPE-set, TYPE-iterable
- TYPE-tuple, TYPE-record
- TYPE-value-channel, TYPE-version-number, TYPE-channel

### Output format

```
TYPE-duration-literal: SUPPORTED | reason
...
```
