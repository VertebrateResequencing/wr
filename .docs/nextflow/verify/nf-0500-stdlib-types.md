# Standard Library Types

**Source:** https://nextflow.io/docs/latest/reference/stdlib-types.html

These are Nextflow-specific types beyond basic Groovy. The nf-0400 file
covers Groovy-style methods on strings, lists, maps and numbers. This file
covers Nextflow-specific types and their operations/methods.

## Duration

### TYPE-duration-literal
Duration created from integer suffix: `1.h`, `24.h`, `500.ms`, `2.d`.
Suffixes: ms/milli/millis, s/sec/second/seconds, m/min/minute/minutes,
h/hour/hours, d/day/days.

### TYPE-duration-cast
Duration from cast: `1000 as Duration`, `'1h' as Duration`,
`'1day 6hours 3min 30s' as Duration`.

### TYPE-duration-arithmetic
Duration arithmetic: `+`, `-`, `*`, `/`, comparisons (`<`, `>`, `==`, etc.).
```groovy
a = 1.h; b = 2.h
assert a + a == b
assert b - a == a
assert a * 2 == b
assert b / 2 == a
```

### TYPE-duration-methods
Duration methods:
- `toDays()` / `getDays()` → Integer
- `toHours()` / `getHours()` → Integer
- `toMinutes()` / `getMinutes()` → Integer
- `toSeconds()` / `getSeconds()` → Integer
- `toMillis()` / `getMillis()` → Integer

## MemoryUnit

### TYPE-memunit-literal
MemoryUnit from integer suffix: `1.MB`, `2.GB`, `512.KB`.
Suffixes: B, KB, MB, GB, TB, PB, EB, ZB.
1 KB = 1024 B (binary convention).

### TYPE-memunit-cast
MemoryUnit from cast: `1024 as MemoryUnit`, `'1 GB' as MemoryUnit`.

### TYPE-memunit-arithmetic
MemoryUnit arithmetic: `+`, `-`, `*`, `/`, comparisons.
```groovy
a = 1.GB; b = 2.GB
assert a + a == b
assert a * 2 == b
```

### TYPE-memunit-methods
MemoryUnit methods:
- `toBytes()` / `getBytes()` → Integer
- `toKilo()` / `getKilo()` → Integer
- `toMega()` / `getMega()` → Integer
- `toGiga()` / `getGiga()` → Integer
- `toUnit(unit: String)` → Integer (unit: 'B','KB','MB','GB','TB','PB','EB','ZB')

## Path

### TYPE-path-operators
Path operators:
- `/` (division): resolves relative path, equivalent to `resolve()`
- `<<`: appends text, equivalent to `append()`

### TYPE-path-attributes
Path filesystem attributes (properties):
- `baseName` — name without extension (`file.tar.gz` → `file.tar`)
- `extension` — file extension (`file.txt` → `txt`)
- `name` — path name (`/some/path/file.txt` → `file.txt`)
- `parent` — parent path
- `scheme` — URI scheme (`s3://...` → `s3`)
- `simpleName` — name without any extensions (`file.tar.gz` → `file`)

### TYPE-path-attr-methods
Path attribute methods:
- `exists()` → Boolean
- `isDirectory()` → Boolean
- `isEmpty()` → Boolean
- `isFile()` → Boolean
- `isHidden()` → Boolean
- `isLink()` → Boolean
- `lastModified()` → Integer (Unix time ms)
- `relativize(other: Path)` → Path
- `resolve(other: String)` → Path
- `resolveSibling(other: String)` → Path
- `size()` → Integer (file size in bytes)
- `toUriString()` → String (path with protocol scheme)

### TYPE-path-reading
Path reading methods:
- `eachLine(action: (String) -> ())` — iterate lines
- `getText()` → String (also accessible as `.text` property)
- `readLines()` → List\<String\>
- `withReader(action: (BufferedReader) -> ())` — buffered reading

### TYPE-path-writing
Path writing methods:
- `append(text: String)` — append without replacing
- `setText(text: String)` — write, replacing content (also `.text = ...`)
- `write(text: String)` — same as `setText()`

### TYPE-path-fs-ops
Path filesystem operations:
- `copyTo(target: Path)` — copy file or directory
- `delete()` → Boolean — delete file/dir (non-recursive)
- `deleteDir()` → Boolean — delete dir recursively
- `getPermissions()` → String (symbolic notation e.g. `rw-rw-r--`)
- `mkdir()` → Boolean — create directory
- `mkdirs()` → Boolean — create with parents
- `mklink(linkName, [hard:, overwrite:])` → Path — create link
- `moveTo(target: Path)` — move file or directory
- `renameTo(target: String)` → Boolean
- `setPermissions(permissions: String)` → Boolean
- `setPermissions(owner, group, other: Integer)` → Boolean

### TYPE-path-listing
Path directory listing methods:
- `eachFile(action: (Path) -> ())` — iterate first-level entries
- `eachFileRecurse(action: (Path) -> ())` — iterate depth-first
- `listDirectory()` → Iterable\<Path\> (new in 26.04)
- `listFiles()` → Iterable\<Path\> (deprecated, use `listDirectory()`)

### TYPE-path-splitting
Path file splitting/counting methods:
- `countFasta()` → Integer
- `countFastq()` → Integer
- `countJson()` → Integer
- `countLines()` → Integer
- `splitCsv()` → List<?>
- `splitFasta()` → List<?>
- `splitFastq()` → List<?>
- `splitJson()` → List<?>
- `splitText()` → List\<String\>

## Other Types

### TYPE-bag
`Bag<E>` — unordered collection implementing `Iterable<E>`.
Operations: `+` (concatenation), `in`/`!in` (membership).

### TYPE-set
`Set<E>` — unordered unique collection implementing `Iterable<E>`.
Created via `list.toSet()`.
Operations: `+` (union), `-` (difference), `in`/`!in`.
Methods: `intersect(right: Iterable<E>)` → Set<E>.

### TYPE-iterable
`Iterable<E>` — trait implemented by Bag, List, Set. Provides:
`any`, `collect`, `collectMany`, `contains`, `each`, `every`, `findAll`,
`groupBy`, `inject` (2 forms), `isEmpty`, `join`, `max` (3 forms),
`min` (3 forms), `size`, `sum` (2 forms), `toList`, `toSet`,
`toSorted` (3 forms), `toUnique` (3 forms).

### TYPE-tuple
`Tuple` — fixed-length sequence with per-element types.
Created via `tuple()` function. Supports `[]` index access.

### TYPE-record
`Record` — immutable map of fields to typed values.
Created via `record()` function. Fields accessed as properties.
Operations: `+` (merge records). Methods: `subMap(keys)`.

### TYPE-value-channel
`Value<V>` — dataflow value channel. Methods:
- `flatMap(transform)` → Channel<R>
- `map(transform)` → Value<R>
- `subscribe(action)`
- `view()` / `view(transform)` → Value<V>

### TYPE-version-number
`VersionNumber` — semantic/calendar version. Methods:
- `getMajor()` / `getMinor()` / `getPatch()` → String
- `matches(condition: String)` → Boolean
  Supports `=`, `==`, `<`, `<=`, `>`, `>=`, `!=`, `<>`, `+` suffix,
  and comma-separated constraints.

### TYPE-channel
`Channel<E>` — asynchronous queue channel of values of type E.
Methods defined in the operator reference (see nf-3000..nf-3500).
