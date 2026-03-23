# Groovy Methods

**Source:** https://nextflow.io/docs/latest/reference/stdlib.html,
https://nextflow.io/docs/latest/reference/stdlib-groovy.html

## Default Imports

### METH-default-imports
Classes imported by default in Nextflow scripts:
- `groovy.lang.*`
- `groovy.util.*`
- `java.io.*`
- `java.lang.*`
- `java.math.BigDecimal`
- `java.math.BigInteger`
- `java.net.*`
- `java.util.*`

All other classes must use their fully-qualified name (e.g. `groovy.json.JsonOutput`).

## String Methods

### METH-str-size
`str.size()` / `str.length()` — returns string length.

### METH-str-trim
`str.trim()` — removes leading/trailing whitespace.

### METH-str-strip
`str.strip()` — like trim but Unicode-aware.

### METH-str-contains
`str.contains(sub)` — true if `sub` is found.

### METH-str-startsWith
`str.startsWith(prefix)` — true if starts with prefix.

### METH-str-endsWith
`str.endsWith(suffix)` — true if ends with suffix.

### METH-str-indexOf
`str.indexOf(sub)` — index of first occurrence, -1 if not found.

### METH-str-replace
`str.replace(old, new)` / `str.replaceAll(regex, replacement)`.

### METH-str-split
`str.split(regex)` — splits into array by regex delimiter.

### METH-str-tokenize
`str.tokenize(delimiters)` — splits into list by character delimiters.

### METH-str-toUpperCase
`str.toUpperCase()` / `str.toLowerCase()`.

### METH-str-substring
`str.substring(start)` / `str.substring(start, end)`.

### METH-str-matches
`str.matches(regex)` — true if entire string matches regex.

### METH-str-toInteger
`str.toInteger()` / `str.toLong()` / `str.toFloat()` / `str.toDouble()`.

### METH-str-padLeft
`str.padLeft(n)` / `str.padRight(n)`.

### METH-str-reverse
`str.reverse()` — reverses the string.

### METH-str-take
`str.take(n)` / `str.drop(n)` — first/remaining n characters.

### METH-str-isInteger
`str.isInteger()` / `str.isLong()` / `str.isFloat()` / `str.isDouble()`.

### METH-str-multiply
`str.multiply(n)` or `str * n` — repeat n times.

### METH-str-readLines
`str.readLines()` — splits by newline into list.

### METH-str-stripIndent
`str.stripIndent()` — removes common leading whitespace.

## Collection Methods (List)

### METH-list-size
`list.size()` — element count.

### METH-list-first
`list.first()` / `list.last()` — first/last element.

### METH-list-get
`list.get(index)` or `list[index]` — element at index. Negative indices.

### METH-list-contains
`list.contains(element)` — true if present.

### METH-list-sort
`list.sort()` / `list.sort { it.prop }` — sort in place or by closure.

### METH-list-collect
`list.collect { transform }` — like map, returns transformed list.

### METH-list-find
`list.find { condition }` — first matching element.

### METH-list-findAll
`list.findAll { condition }` — all matching elements.

### METH-list-any
`list.any { condition }` — true if any element matches.

### METH-list-every
`list.every { condition }` — true if all match.

### METH-list-each
`list.each { action }` — iterate with side effects.

### METH-list-flatten
`list.flatten()` — deep flatten nested lists.

### METH-list-unique
`list.unique()` — remove duplicates.

### METH-list-join
`list.join(separator)` — join into string.

### METH-list-sum
`list.sum()` / `list.sum { transform }` — sum elements.

### METH-list-min
`list.min()` / `list.max()` — minimum/maximum.

### METH-list-count
`list.count(element)` / `list.count { condition }`.

### METH-list-groupBy
`list.groupBy { key }` — group into map.

### METH-list-collectEntries
`list.collectEntries { [key, value] }` — list to map.

### METH-list-inject
`list.inject(initial) { acc, v -> ... }` — fold/reduce.

### METH-list-withIndex
`list.withIndex()` — `[[elem, 0], [elem, 1], ...]`.

### METH-list-indexed
`list.indexed()` — `[0: elem, 1: elem, ...]`.

### METH-list-take
`list.take(n)` / `list.drop(n)` — first n / remaining after n.

### METH-list-plus
`list.plus(other)` / `list + other` — concatenation.

### METH-list-minus
`list.minus(other)` / `list - other` — subtraction.

### METH-list-reverse
`list.reverse()` — reversed copy.

### METH-list-transpose
`list.transpose()` — transpose list of lists.

### METH-list-combinations
`list.combinations()` — all combinations.

### METH-list-subsequences
`list.subsequences()` — all subsequences.

### METH-list-collate
`list.collate(n)` — group into sublists of n.

## Map Methods

### METH-map-size
`map.size()` — entry count.

### METH-map-keySet
`map.keySet()` / `map.values()` / `map.entrySet()`.

### METH-map-containsKey
`map.containsKey(key)` / `map.containsValue(value)`.

### METH-map-get
`map.get(key)` / `map.get(key, default)` / `map[key]` / `map.key`.

### METH-map-each
`map.each { k, v -> ... }` / `map.each { entry -> ... }`.

### METH-map-collect
`map.collect { k, v -> ... }` — map entries to list.

### METH-map-find
`map.find { k, v -> condition }` — first matching entry.

### METH-map-findAll
`map.findAll { k, v -> condition }` — matching entries.

### METH-map-groupBy
`map.groupBy { k, v -> groupKey }`.

### METH-map-subMap
`map.subMap(keys)` — subset of map.

### METH-map-plus
`map + otherMap` — merge.

## Number Methods

### METH-num-abs
`n.abs()` — absolute value.

### METH-num-round
`n.round()` / `n.round(places)`.

### METH-num-toInteger
`n.toInteger()` / `n.toLong()` / `n.toFloat()` / `n.toDouble()`.

### METH-num-intdiv
`n.intdiv(d)` — integer division.

### METH-num-times
`n.times { i -> ... }` — repeat n times.

### METH-num-upto
`n.upto(m) { i -> ... }` / `n.downto(m) { i -> ... }`.

## File/Path Methods

### METH-file-text
`file.text` — read entire file as string.

### METH-file-readLines
`file.readLines()` — list of lines.

### METH-file-baseName
`file.baseName` — filename without extension.

### METH-file-name
`file.name` / `file.getName()` — filename with extension.

### METH-file-simpleName
`file.simpleName` — name without any extensions.

### METH-file-extension
`file.extension` — file extension.

### METH-file-parent
`file.parent` / `file.getParent()` — parent directory.

### METH-file-exists
`file.exists()` — true if file exists.

### METH-file-isFile
`file.isFile()` / `file.isDirectory()` / `file.isEmpty()`.

### METH-file-size
`file.size()` — file size in bytes.

### METH-file-toAbsolutePath
`file.toAbsolutePath()` / `file.toRealPath()`.

## Constructors

### METH-new-File
`new File(path)` — create File object.

### METH-new-URL
`new URL(urlString)` — create URL object (note: usually `file()` is preferred).

### METH-new-Date
`new Date()` — current date.

### METH-new-Random
`new Random()` / `new Random(seed)` — random number generator.

### METH-new-ArrayList
`new ArrayList(collection)` — create ArrayList.

### METH-new-HashMap
`new HashMap(map)` / `new LinkedHashMap()` — create Map.

### METH-new-BigDecimal
`new BigDecimal(value)`.

### METH-new-BigInteger
`new BigInteger(value)`.
