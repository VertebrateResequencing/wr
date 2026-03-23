# Test: Groovy Methods & Imports

**Spec files:** nf-0400-groovy-methods.md
**Impl files:** impl-08-groovy.md

## Task

For each feature ID in nf-0400-groovy-methods.md, determine its classification.

### Checklist

1. Read nf-0400-groovy-methods.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- METH-default-imports, METH-bag-plus, METH-duration-toDays, METH-duration-toHours, METH-duration-toMillis
  METH-duration-toMinutes, METH-duration-toSeconds, METH-iterable-any, METH-iterable-collect, METH-iterable-collectMany
  METH-iterable-contains, METH-iterable-each, METH-iterable-every, METH-iterable-findAll, METH-iterable-groupBy
  METH-iterable-inject, METH-iterable-isEmpty, METH-iterable-join, METH-iterable-max, METH-iterable-min
  METH-iterable-size, METH-iterable-sum, METH-iterable-toList, METH-iterable-toSet, METH-iterable-toSorted
  METH-iterable-toUnique, METH-list-plus, METH-list-multiply, METH-list-getAt, METH-list-collate
  METH-list-find, METH-list-first, METH-list-getIndices, METH-list-head, METH-list-indexOf
  METH-list-init, METH-list-last, METH-list-reverse, METH-list-subList, METH-list-tail
  METH-list-take, METH-list-takeWhile, METH-list-withIndex, METH-map-plus, METH-map-getAt
  METH-map-any, METH-map-containsKey, METH-map-containsValue, METH-map-each, METH-map-entrySet
  METH-map-every, METH-map-isEmpty, METH-map-keySet, METH-map-size, METH-map-subMap
  METH-map-values, METH-memoryunit-toBytes, METH-memoryunit-toGiga, METH-memoryunit-toKilo, METH-memoryunit-toMega
  METH-memoryunit-toUnit, METH-path-divide, METH-path-leftShift, METH-path-baseName, METH-path-extension
  METH-path-name, METH-path-parent, METH-path-scheme, METH-path-simpleName, METH-path-exists
  METH-path-isDirectory, METH-path-isEmpty, METH-path-isFile, METH-path-isHidden, METH-path-isLink
  METH-path-lastModified, METH-path-relativize, METH-path-resolve, METH-path-resolveSibling, METH-path-size
  METH-path-toUriString, METH-path-eachLine, METH-path-getText, METH-path-readLines, METH-path-withReader
  METH-path-append, METH-path-setText, METH-path-write, METH-path-copyTo, METH-path-delete
  METH-path-deleteDir, METH-path-getPermissions, METH-path-mkdir, METH-path-mkdirs, METH-path-mklink
  METH-path-hard, METH-path-overwrite, METH-path-moveTo, METH-path-renameTo, METH-path-setPermissions
  METH-path-eachFile, METH-path-eachFileRecurse, METH-path-listDirectory, METH-path-listFiles, METH-path-countFasta
  METH-path-countFastq, METH-path-countJson, METH-path-countLines, METH-path-splitCsv, METH-path-splitFasta
  METH-path-splitFastq, METH-path-splitJson, METH-path-splitText, METH-record-plus, METH-record-subMap
  METH-set-plus, METH-set-minus, METH-set-intersect, METH-string-plus, METH-string-multiply
  METH-string-getAt, METH-string-contains, METH-string-endsWith, METH-string-execute, METH-string-indexOf
  METH-string-isBlank, METH-string-isEmpty, METH-string-isDouble, METH-string-isFloat, METH-string-isInteger
  METH-string-isLong, METH-string-lastIndexOf, METH-string-length, METH-string-md5, METH-string-replace
  METH-string-replaceAll, METH-string-replaceFirst, METH-string-sha256, METH-string-startsWith, METH-string-strip
  METH-string-stripIndent, METH-string-stripLeading, METH-string-stripTrailing, METH-string-substring, METH-string-toBoolean
  METH-string-toDouble, METH-string-toFloat, METH-string-toInteger, METH-string-toLong, METH-string-toLowerCase
  METH-string-toUpperCase, METH-string-tokenize, METH-tuple-getAt, METH-value-flatMap, METH-value-map
  METH-value-subscribe, METH-value-view, METH-versionnumber-getMajor, METH-versionnumber-getMinor, METH-versionnumber-getPatch
  METH-versionnumber-matches

### Output format

```
METH-default-imports: SUPPORTED | reason
...
```
