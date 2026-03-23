# Statements

**Source:** https://nextflow.io/docs/latest/reference/syntax.html

> **Note:** Items marked ⚠️NON-STRICT are available in non-strict Groovy mode
> but excluded from the official Nextflow strict syntax reference. They are
> included here because the wr implementation handles them.

## Features

### STMT-if
`if`/`else if`/`else` blocks:
```groovy
if (x > 0) { ... }
else if (x == 0) { ... }
else { ... }
```

### STMT-for
⚠️NON-STRICT — not in syntax.html (strict mode).
`for` loops:
```groovy
for (x in collection) { ... }
for (int i = 0; i < n; i++) { ... }
```

### STMT-while
⚠️NON-STRICT — not in syntax.html (strict mode).
`while` loops:
```groovy
while (condition) { ... }
```

### STMT-switch
⚠️NON-STRICT — not in syntax.html (strict mode).
`switch`/`case` statements:
```groovy
switch (x) {
    case 1: ...; break
    case ~/regex/: ...; break
    default: ...
}
```

### STMT-try
`try`/`catch` (strict syntax). The `finally` clause is ⚠️NON-STRICT.
```groovy
try { ... }
catch (Exception e) { ... }
finally { ... }
```

### STMT-assert
`assert` statement:
```groovy
assert x > 0 : "x must be positive"
```

### STMT-throw
`throw` statements:
```groovy
throw new RuntimeException("error")
```

### STMT-return
Explicit `return` and implicit last-expression return.

### STMT-break
⚠️NON-STRICT — not in syntax.html (strict mode).
`break` to exit loops and switch cases.

### STMT-continue
⚠️NON-STRICT — not in syntax.html (strict mode).
`continue` to skip to next iteration.
