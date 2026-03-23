# Syntax Basics

**Source:** https://nextflow.io/docs/latest/reference/syntax.html

## Features

### SYN-comments
Line comments (`//`) and block comments (`/* */`).

### SYN-shebang
`#!/usr/bin/env nextflow` shebang line at script start.

### SYN-var-def
Variable declarations with `def`: `def x = 1`. Both typed and untyped.

### SYN-multi-assign
Multi-assignment: `def (a, b) = [1, 2]`.

### SYN-closure
Closure syntax: `{ -> body }`, `{ it -> body }`, `{ a, b -> body }`.
Implicit `it` parameter. Closures as last argument.

### SYN-function
Function definitions: `def foo(x, y) { return x + y }`.
Default parameter values. Return type inference.

### SYN-gstring
GString interpolation: `"hello ${name}"`, `"value $x"`.
Lazy vs eager evaluation. Multi-line strings with `"""`.

### SYN-slashy
Slashy strings for regex: `~/pattern/`.

### SYN-multiline
Multi-line strings: triple-quoted `'''` and `"""`.

### SYN-string-concat
String concatenation with `+`.

### SYN-feature-flag
Feature flag declaration at script top level:
```groovy
nextflow.preview.recursion = true
nextflow.enable.dsl = 2
```
Target must be a valid feature flag name; source must be a literal.

### SYN-constructor
Constructor call expression: `new TypeName(args)`. Supports
fully-qualified (`new java.util.Date()`) and simple names (`new Date()`).
The set of available types is defined by the standard library.

### SYN-named-args
Named arguments in function calls are collected into a map as the first
positional argument:
```groovy
file('hello.txt', checkIfExists: true)
// equivalent to:
file([checkIfExists: true], 'hello.txt')
```
Argument name must be an identifier or string literal.

### SYN-index-expr
Index expression: `myList[0]`, `myMap['key']`. The left expression and
a right expression in square brackets.

### SYN-property-expr
Property expression with dot (`.`), spread dot (`*.`), or safe dot (`?.`):
```groovy
myFile.text         // dot
myFiles*.text       // spread: myFiles.collect { it.text }
myFile?.text        // safe: null if myFile is null
```
Property can be an identifier or string literal.

### SYN-scoping
Variable scoping rules:
- Function/closure scope: parameters and locals exist for the call duration
- Workflow sections: `take` inputs exist for the body; `main` vars exist
  in `main`, `emit`, and `publish` sections.
- Process sections: input vars exist for the entire body; `script`/`exec`/`stub`
  vars exist only in their section, EXCEPT vars without `def` also exist
  in `output:`.
- `if`/`else` branches: vars declared inside exist only in that branch.
- No redeclaration: a variable cannot be declared with the same name as
  another variable in the same scope or enclosing scope.
