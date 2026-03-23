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
