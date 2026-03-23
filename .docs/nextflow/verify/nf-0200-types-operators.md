# Types, Literals and Operators

**Source:** https://nextflow.io/docs/latest/reference/syntax.html

## Types and Literals

### SYN-int
Integer literals: `1`, `0x1F` (hex), `0b1010` (binary), `031` (octal),
`1_000_000` (underscore separators). Type is `int` or `long`.

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
Bitwise: `&`, `|`, `^`, `~`, `<<`, `>>`, `>>>` (unsigned right shift).

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
Membership: `element in collection`, negated: `element !in collection`.
Both `in` and `!in` are distinct binary operators.

### SYN-instanceof
Type check: `value instanceof Type`, `value !instanceof Type` (negated),
`value as Type` (cast). Both `instanceof` and `!instanceof` are distinct
binary operators.

### SYN-assign-compound
Compound assignment: `+=`, `-=`, `*=`, `/=`, etc.

## Expression Precedence

### SYN-precedence
Operator precedence from highest to lowest:
1. parentheses
2. property expressions (`.`, `*.`, `?.`)
3. function calls
4. index expressions (`[]`)
5. `~`, `!`
6. `**`
7. `+`, `-` (unary)
8. `*`, `/`, `%`
9. `+`, `-` (binary)
10. `<<`, `>>>`, `>>`, `..`, `..<`
11. `as`
12. `instanceof`, `!instanceof`
13. `<`, `>`, `<=`, `>=`, `in`, `!in`
14. `==`, `!=`, `<=>`
15. `=~`, `==~`
16. `&`
17. `^`
18. `|`
19. `&&`
20. `||`
21. `?:` (ternary)
22. `?:` (elvis)
