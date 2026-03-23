# Types, Literals and Operators

**Source:** https://nextflow.io/docs/latest/reference/syntax.html

## Types and Literals

### SYN-int
Integer literals: `1`, `0x1F`, `0b1010`, `1_000_000`. Type is `int` or `long`.

### SYN-float
Floating-point literals: `1.5`, `1.5e10`, `1.5G` (BigDecimal).

### SYN-bool
Boolean literals: `true`, `false`.

### SYN-null
Null literal: `null`.

### SYN-string-lit
String literals: single-quoted `'hello'` (no interpolation), double-quoted
`"hello"` (GString interpolation).

### SYN-list
List literals: `[1, 2, 3]`. Nested lists. Empty list `[]`.

### SYN-map
Map literals: `[key: 'value']`. String keys. Variable keys with `(var): value`.
Empty map `[:]`.

### SYN-range-incl
Inclusive range: `1..5` → `[1, 2, 3, 4, 5]`.

### SYN-range-excl
Exclusive range: `1..<5` → `[1, 2, 3, 4]`.

## Operators

### SYN-arith
Arithmetic: `+`, `-`, `*`, `/`, `%` (modulo), `**` (power).

### SYN-compare
Comparison: `==`, `!=`, `<`, `<=`, `>`, `>=`, `<=>` (spaceship).

### SYN-logical
Logical: `&&`, `||`, `!`.

### SYN-bitwise
Bitwise: `&`, `|`, `^`, `~`, `<<`, `>>`.

### SYN-ternary
Ternary: `condition ? valueIfTrue : valueIfFalse`.

### SYN-elvis
Elvis: `value ?: defaultValue` (returns `value` if truthy, else `defaultValue`).

### SYN-null-safe
Null-safe navigation: `obj?.property`. Returns `null` if `obj` is null.

### SYN-spread
Spread operator: `list*.property` or `list*.method()`.

### SYN-regex-find
Find operator: `string =~ /pattern/`. Returns matcher (truthy if matches).

### SYN-regex-match
Match operator: `string ==~ /pattern/`. Returns true if entire string matches.

### SYN-membership
Membership: `element in collection`, negated: `!(element in collection)`.

### SYN-instanceof
Type check: `value instanceof Type`, `value as Type` (cast).

### SYN-assign-compound
Compound assignment: `+=`, `-=`, `*=`, `/=`, etc.
