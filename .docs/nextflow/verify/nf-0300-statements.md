# Statements

**Source:** https://nextflow.io/docs/latest/reference/syntax.html

## Features

### STMT-if
`if`/`else if`/`else` blocks:
```groovy
if (x > 0) { ... }
else if (x == 0) { ... }
else { ... }
```

### STMT-for
`for` loops:
```groovy
for (x in collection) { ... }
for (int i = 0; i < n; i++) { ... }
```

### STMT-while
`while` loops:
```groovy
while (condition) { ... }
```

### STMT-switch
`switch`/`case` statements:
```groovy
switch (x) {
    case 1: ...; break
    case ~/regex/: ...; break
    default: ...
}
```

### STMT-try
`try`/`catch`/`finally`:
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
`break` to exit loops and switch cases.

### STMT-continue
`continue` to skip to next iteration.
