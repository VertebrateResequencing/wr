# Groovy Methods & Imports

**Source:** https://nextflow.io/docs/latest/reference/stdlib-types.html

## Features

### METH-bag-plus
`+ : (Bag<E>, Bag<E>) -> Bag<E>` — TODO: describe expected behaviour.

### METH-bag-membership
`in, !in : (E, Bag<E>) -> Boolean` — TODO: describe expected behaviour.

### METH-duration-toDays
`toDays() -> Integer` — TODO: describe expected behaviour.

### METH-duration-toHours
`toHours() -> Integer` — TODO: describe expected behaviour.

### METH-duration-toMillis
`toMillis() -> Integer` — TODO: describe expected behaviour.

### METH-duration-toMinutes
`toMinutes() -> Integer` — TODO: describe expected behaviour.

### METH-duration-toSeconds
`toSeconds() -> Integer` — TODO: describe expected behaviour.

### METH-duration-getDays
`getDays() — alias for toDays()` — TODO: describe expected behaviour.

### METH-duration-getHours
`getHours() — alias for toHours()` — TODO: describe expected behaviour.

### METH-duration-getMillis
`getMillis() — alias for toMillis()` — TODO: describe expected behaviour.

### METH-duration-getMinutes
`getMinutes() — alias for toMinutes()` — TODO: describe expected behaviour.

### METH-duration-getSeconds
`getSeconds() — alias for toSeconds()` — TODO: describe expected behaviour.

### METH-iterable-any
`any( condition: (E) -> Boolean ) -> Boolean` — TODO: describe expected behaviour.

### METH-iterable-collect
`collect( transform: (E) -> R ) -> Iterable<R>` — TODO: describe expected behaviour.

### METH-iterable-collectMany
`collectMany( transform: (E) -> Iterable<R> ) -> Iterable<R>` — TODO: describe expected behaviour.

### METH-iterable-contains
`contains( value: E ) -> Boolean` — TODO: describe expected behaviour.

### METH-iterable-each
`each( action: (E) -> () )` — TODO: describe expected behaviour.

### METH-iterable-every
`every( condition: (E) -> Boolean ) -> Boolean` — TODO: describe expected behaviour.

### METH-iterable-findAll
`findAll( condition: (E) -> Boolean ) -> Iterable<E>` — TODO: describe expected behaviour.

### METH-iterable-groupBy
`groupBy( transform: (E) -> K ) -> Map<K,Iterable<E>>` — TODO: describe expected behaviour.

### METH-iterable-inject
`inject( accumulator: (E,E) -> E ) -> E` — TODO: describe expected behaviour.

### METH-iterable-inject-3
`inject( initialValue: R, accumulator: (R,E) -> R ) -> R` — TODO: describe expected behaviour.

### METH-iterable-isEmpty
`isEmpty() -> Boolean` — TODO: describe expected behaviour.

### METH-iterable-join
`join( separator: String = '' ) -> String` — TODO: describe expected behaviour.

### METH-iterable-max
`max() -> E` — TODO: describe expected behaviour.

### METH-iterable-max-1
`max( comparator: (E) -> R ) -> E` — TODO: describe expected behaviour.

### METH-iterable-max-2
`max( comparator: (E,E) -> Integer ) -> E` — TODO: describe expected behaviour.

### METH-iterable-min
`min() -> E` — TODO: describe expected behaviour.

### METH-iterable-min-1
`min( comparator: (E) -> R ) -> E` — TODO: describe expected behaviour.

### METH-iterable-min-2
`min( comparator: (E,E) -> Integer ) -> E` — TODO: describe expected behaviour.

### METH-iterable-size
`size() -> Integer` — TODO: describe expected behaviour.

### METH-iterable-sum
`sum() -> E` — TODO: describe expected behaviour.

### METH-iterable-sum-1
`sum( transform: (E) -> R ) -> R` — TODO: describe expected behaviour.

### METH-iterable-toList
`toList() -> List<E>` — TODO: describe expected behaviour.

### METH-iterable-toSet
`toSet() -> Set<E>` — TODO: describe expected behaviour.

### METH-iterable-toSorted
`toSorted() -> List<E>` — TODO: describe expected behaviour.

### METH-iterable-toSorted-1
`toSorted( comparator: (E) -> R ) -> List<E>` — TODO: describe expected behaviour.

### METH-iterable-toSorted-2
`toSorted( comparator: (E,E) -> Integer ) -> List<E>` — TODO: describe expected behaviour.

### METH-iterable-toUnique
`toUnique() -> Iterable<E>` — TODO: describe expected behaviour.

### METH-iterable-toUnique-2
`toUnique( comparator: (E,E) -> Integer ) -> Iterable<E>` — TODO: describe expected behaviour.

### METH-iterable-toUnique-1
`toUnique( comparator: (E) -> R ) -> Iterable<E>` — TODO: describe expected behaviour.

### METH-list-plus
`+ : (List<E>, List<E>) -> List<E>` — TODO: describe expected behaviour.

### METH-list-multiply
`* : (List<E>, Integer) -> List<E>` — TODO: describe expected behaviour.

### METH-list-getAt
`[] : (List<E>, Integer) -> E` — TODO: describe expected behaviour.

### METH-list-membership
`in, !in : (E, List<E>) -> Boolean` — TODO: describe expected behaviour.

### METH-list-collate
`collate( size: Integer, keepRemainder: Boolean = true ) -> List<List<E>>` — TODO: describe expected behaviour.

### METH-list-collate-3
`collate( size: Integer, step: Integer, keepRemainder: Boolean = true ) -> List<List<E>>` — TODO: describe expected behaviour.

### METH-list-find
`find( condition: (E) -> Boolean ) -> E` — TODO: describe expected behaviour.

### METH-list-first
`first() -> E` — TODO: describe expected behaviour.

### METH-list-getIndices
`getIndices() -> List<Integer>` — TODO: describe expected behaviour.

### METH-list-head
`head() -> E` — TODO: describe expected behaviour.

### METH-list-indexOf
`indexOf( value: E ) -> Integer` — TODO: describe expected behaviour.

### METH-list-init
`init() -> List<E>` — TODO: describe expected behaviour.

### METH-list-last
`last() -> E` — TODO: describe expected behaviour.

### METH-list-reverse
`reverse() -> List<E>` — TODO: describe expected behaviour.

### METH-list-subList
`subList( fromIndex: Integer, toIndex: Integer ) -> List<E>` — TODO: describe expected behaviour.

### METH-list-tail
`tail() -> List<E>` — TODO: describe expected behaviour.

### METH-list-take
`take( n: Integer ) -> List<E>` — TODO: describe expected behaviour.

### METH-list-takeWhile
`takeWhile( condition: (E) -> Boolean ) -> List<E>` — TODO: describe expected behaviour.

### METH-list-withIndex
`withIndex() -> List<(E,Integer)>` — TODO: describe expected behaviour.

### METH-map-plus
`+ : (Map<K,V>, Map<K,V>) -> Map<K,V>` — TODO: describe expected behaviour.

### METH-map-getAt
`[] : (Map<K,V>, K) -> V` — TODO: describe expected behaviour.

### METH-map-membership
`in, !in : (K, Map<K,V>) -> Boolean` — TODO: describe expected behaviour.

### METH-map-any
`any( condition: (K,V) -> Boolean ) -> Boolean` — TODO: describe expected behaviour.

### METH-map-containsKey
`containsKey( key: K ) -> Boolean` — TODO: describe expected behaviour.

### METH-map-containsValue
`containsValue( value: V ) -> Boolean` — TODO: describe expected behaviour.

### METH-map-each
`each( action: (K,V) -> () )` — TODO: describe expected behaviour.

### METH-map-entrySet
`entrySet() -> Set<(K,V)>` — TODO: describe expected behaviour.

### METH-map-every
`every( condition: (K,V) -> Boolean ) -> Boolean` — TODO: describe expected behaviour.

### METH-map-isEmpty
`isEmpty() -> Boolean` — TODO: describe expected behaviour.

### METH-map-keySet
`keySet() -> Set<K>` — TODO: describe expected behaviour.

### METH-map-size
`size() -> Integer` — TODO: describe expected behaviour.

### METH-map-subMap
`subMap( keys: Iterable<K> ) -> Map<K,V>` — TODO: describe expected behaviour.

### METH-map-values
`values() -> Bag<V>` — TODO: describe expected behaviour.

### METH-memoryunit-toBytes
`toBytes() -> Integer` — TODO: describe expected behaviour.

### METH-memoryunit-toGiga
`toGiga() -> Integer` — TODO: describe expected behaviour.

### METH-memoryunit-toKilo
`toKilo() -> Integer` — TODO: describe expected behaviour.

### METH-memoryunit-toMega
`toMega() -> Integer` — TODO: describe expected behaviour.

### METH-memoryunit-toUnit
`toUnit( unit: String ) -> Integer` — TODO: describe expected behaviour.

### METH-memoryunit-getBytes
`getBytes() — alias for toBytes()` — TODO: describe expected behaviour.

### METH-memoryunit-getGiga
`getGiga() — alias for toGiga()` — TODO: describe expected behaviour.

### METH-memoryunit-getKilo
`getKilo() — alias for toKilo()` — TODO: describe expected behaviour.

### METH-memoryunit-getMega
`getMega() — alias for toMega()` — TODO: describe expected behaviour.

### METH-path-divide
`/ : (Path, String) -> Path` — TODO: describe expected behaviour.

### METH-path-leftShift
`<< : (Path, String)` — TODO: describe expected behaviour.

### METH-path-baseName
`baseName: String` — TODO: describe expected behaviour.

### METH-path-extension
`extension: String` — TODO: describe expected behaviour.

### METH-path-name
`name: String` — TODO: describe expected behaviour.

### METH-path-parent
`parent: Path` — TODO: describe expected behaviour.

### METH-path-scheme
`scheme: String` — TODO: describe expected behaviour.

### METH-path-simpleName
`simpleName: String` — TODO: describe expected behaviour.

### METH-path-exists
`exists() -> Boolean` — TODO: describe expected behaviour.

### METH-path-isDirectory
`isDirectory() -> Boolean` — TODO: describe expected behaviour.

### METH-path-isEmpty
`isEmpty() -> Boolean` — TODO: describe expected behaviour.

### METH-path-isFile
`isFile() -> Boolean` — TODO: describe expected behaviour.

### METH-path-isHidden
`isHidden() -> Boolean` — TODO: describe expected behaviour.

### METH-path-isLink
`isLink() -> Boolean` — TODO: describe expected behaviour.

### METH-path-lastModified
`lastModified() -> Integer` — TODO: describe expected behaviour.

### METH-path-relativize
`relativize(other: Path) -> Path` — TODO: describe expected behaviour.

### METH-path-resolve
`resolve(other: String) -> Path` — TODO: describe expected behaviour.

### METH-path-resolveSibling
`resolveSibling(other: String) -> Path` — TODO: describe expected behaviour.

### METH-path-size
`size() -> Integer` — TODO: describe expected behaviour.

### METH-path-toUriString
`toUriString() -> String` — TODO: describe expected behaviour.

### METH-path-eachLine
`eachLine( action: (String) -> () )` — TODO: describe expected behaviour.

### METH-path-getText
`getText() -> String` — TODO: describe expected behaviour.

### METH-path-readLines
`readLines() -> List<String>` — TODO: describe expected behaviour.

### METH-path-withReader
`withReader( action: (BufferedReader) -> () )` — TODO: describe expected behaviour.

### METH-path-append
`append( text: String )` — TODO: describe expected behaviour.

### METH-path-setText
`setText( text: String )` — TODO: describe expected behaviour.

### METH-path-write
`write( text: String )` — TODO: describe expected behaviour.

### METH-path-copyTo
`copyTo( target: Path )` — TODO: describe expected behaviour.

### METH-path-delete
`delete() -> Boolean` — TODO: describe expected behaviour.

### METH-path-deleteDir
`deleteDir() -> Boolean` — TODO: describe expected behaviour.

### METH-path-getPermissions
`getPermissions() -> String` — TODO: describe expected behaviour.

### METH-path-mkdir
`mkdir() -> Boolean` — TODO: describe expected behaviour.

### METH-path-mkdirs
`mkdirs() -> Boolean` — TODO: describe expected behaviour.

### METH-path-mklink
`mklink( linkName: String, [options] ) -> Path` — TODO: describe expected behaviour.

### METH-path-mklink-hard
`mklink.hard: Boolean` — TODO: describe expected behaviour.

### METH-path-mklink-overwrite
`mklink.overwrite: Boolean` — TODO: describe expected behaviour.

### METH-path-moveTo
`moveTo( target: Path )` — TODO: describe expected behaviour.

### METH-path-renameTo
`renameTo( target: String ) -> Boolean` — TODO: describe expected behaviour.

### METH-path-setPermissions
`setPermissions( permissions: String ) -> Boolean` — TODO: describe expected behaviour.

### METH-path-setPermissions-3
`setPermissions( owner: Integer, group: Integer, other: Integer ) -> Boolean` — TODO: describe expected behaviour.

### METH-path-eachFile
`eachFile( action: (Path) -> () )` — TODO: describe expected behaviour.

### METH-path-eachFileRecurse
`eachFileRecurse( action: (Path) -> () )` — TODO: describe expected behaviour.

### METH-path-listDirectory
`listDirectory() -> Iterable<Path>` — TODO: describe expected behaviour.

### METH-path-listFiles
`listFiles() -> Iterable<Path>` — TODO: describe expected behaviour.

### METH-path-countFasta
`countFasta() -> Integer` — TODO: describe expected behaviour.

### METH-path-countFastq
`countFastq() -> Integer` — TODO: describe expected behaviour.

### METH-path-countJson
`countJson() -> Integer` — TODO: describe expected behaviour.

### METH-path-countLines
`countLines() -> Integer` — TODO: describe expected behaviour.

### METH-path-splitCsv
`splitCsv() -> List<?>` — TODO: describe expected behaviour.

### METH-path-splitFasta
`splitFasta() -> List<?>` — TODO: describe expected behaviour.

### METH-path-splitFastq
`splitFastq() -> List<?>` — TODO: describe expected behaviour.

### METH-path-splitJson
`splitJson() -> List<?>` — TODO: describe expected behaviour.

### METH-path-splitText
`splitText() -> List<String>` — TODO: describe expected behaviour.

### METH-record-plus
`+ : (Record, Record) -> Record` — TODO: describe expected behaviour.

### METH-record-subMap
`subMap( keys: Iterable<String> ) -> Record` — TODO: describe expected behaviour.

### METH-set-plus
`+ : (Set<E>, Iterable<E>) -> Set<E>` — TODO: describe expected behaviour.

### METH-set-minus
`- : (Set<E>, Iterable<E>) -> Set<E>` — TODO: describe expected behaviour.

### METH-set-membership
`in, !in : (E, Set<E>) -> Boolean` — TODO: describe expected behaviour.

### METH-set-intersect
`intersect( right: Iterable<E> ) -> Set<E>` — TODO: describe expected behaviour.

### METH-string-plus
`+ : (String, String) -> String` — TODO: describe expected behaviour.

### METH-string-multiply
`* : (String, Integer) -> String` — TODO: describe expected behaviour.

### METH-string-getAt
`[] : (String, Integer) -> char` — TODO: describe expected behaviour.

### METH-string-bitwiseNegate
`~ : (String) -> Pattern` — TODO: describe expected behaviour.

### METH-string-find
`=~ : (String, String) -> Matcher` — TODO: describe expected behaviour.

### METH-string-match
`==~ : (String, String) -> Boolean` — TODO: describe expected behaviour.

### METH-string-contains
`contains( str: String ) -> Boolean` — TODO: describe expected behaviour.

### METH-string-endsWith
`endsWith( suffix: String ) -> Boolean` — TODO: describe expected behaviour.

### METH-string-execute
`execute() -> Process` — TODO: describe expected behaviour.

### METH-string-indexOf
`indexOf( str: String ) -> Integer` — TODO: describe expected behaviour.

### METH-string-indexOf-2
`indexOf( str: String, fromIndex: Integer ) -> Integer` — TODO: describe expected behaviour.

### METH-string-isBlank
`isBlank() -> Boolean` — TODO: describe expected behaviour.

### METH-string-isEmpty
`isEmpty() -> Boolean` — TODO: describe expected behaviour.

### METH-string-isDouble
`isDouble() -> Boolean` — TODO: describe expected behaviour.

### METH-string-isFloat
`isFloat() -> Boolean` — TODO: describe expected behaviour.

### METH-string-isInteger
`isInteger() -> Boolean` — TODO: describe expected behaviour.

### METH-string-isLong
`isLong() -> Boolean` — TODO: describe expected behaviour.

### METH-string-lastIndexOf
`lastIndexOf( str: String ) -> Integer` — TODO: describe expected behaviour.

### METH-string-lastIndexOf-2
`lastIndexOf( str: String, fromIndex: Integer ) -> Integer` — TODO: describe expected behaviour.

### METH-string-length
`length() -> Integer` — TODO: describe expected behaviour.

### METH-string-md5
`md5() -> String` — TODO: describe expected behaviour.

### METH-string-replace
`replace( target: String, replacement: String ) -> String` — TODO: describe expected behaviour.

### METH-string-replaceAll
`replaceAll( regex: String, replacement: String ) -> String` — TODO: describe expected behaviour.

### METH-string-replaceFirst
`replaceFirst( regex: String, replacement: String ) -> String` — TODO: describe expected behaviour.

### METH-string-sha256
`sha256() -> String` — TODO: describe expected behaviour.

### METH-string-startsWith
`startsWith( prefix: String ) -> Boolean` — TODO: describe expected behaviour.

### METH-string-strip
`strip() -> String` — TODO: describe expected behaviour.

### METH-string-stripIndent
`stripIndent() -> String` — TODO: describe expected behaviour.

### METH-string-stripLeading
`stripLeading() -> String` — TODO: describe expected behaviour.

### METH-string-stripTrailing
`stripTrailing() -> String` — TODO: describe expected behaviour.

### METH-string-substring
`substring( beginIndex: Integer ) -> String` — TODO: describe expected behaviour.

### METH-string-substring-2
`substring( beginIndex: Integer, endIndex: Integer ) -> String` — TODO: describe expected behaviour.

### METH-string-toBoolean
`toBoolean() -> Boolean` — TODO: describe expected behaviour.

### METH-string-toDouble
`toDouble() -> Float` — TODO: describe expected behaviour.

### METH-string-toFloat
`toFloat() -> Float` — TODO: describe expected behaviour.

### METH-string-toInteger
`toInteger() -> Integer` — TODO: describe expected behaviour.

### METH-string-toLong
`toLong() -> Integer` — TODO: describe expected behaviour.

### METH-string-toLowerCase
`toLowerCase() -> String` — TODO: describe expected behaviour.

### METH-string-toUpperCase
`toUpperCase() -> String` — TODO: describe expected behaviour.

### METH-string-tokenize
`tokenize( delimiters: String ) -> List<String>` — TODO: describe expected behaviour.

### METH-tuple-getAt
`[] : (Tuple, Integer) -> ?` — TODO: describe expected behaviour.

### METH-value-flatMap
`flatMap( transform: (V) -> Iterable<R> ) -> Channel<R>` — TODO: describe expected behaviour.

### METH-value-map
`map( transform: (V) -> R ) -> Value<R>` — TODO: describe expected behaviour.

### METH-value-subscribe
`subscribe( action: (V) -> () )` — TODO: describe expected behaviour.

### METH-value-view
`view() -> Value<V>` — TODO: describe expected behaviour.

### METH-value-view-1
`view( transform: (V) -> String ) -> Value<V>` — TODO: describe expected behaviour.

### METH-versionnumber-getMajor
`getMajor() -> String` — TODO: describe expected behaviour.

### METH-versionnumber-getMinor
`getMinor() -> String` — TODO: describe expected behaviour.

### METH-versionnumber-getPatch
`getPatch() -> String` — TODO: describe expected behaviour.

### METH-versionnumber-matches
`matches( condition: String ) -> Boolean` — TODO: describe expected behaviour.

### METH-default-imports
`Default import packages (groovy.lang.*, java.io.*, etc.)` — TODO: describe expected behaviour.
