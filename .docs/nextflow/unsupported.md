# Unsupported Features

Nextflow features that cannot or will not be implemented in wr due to fundamental architectural differences (e.g. wr runs shell commands, not JVM code).

**Total: 4 features**

## Directives: Scheduler
Source: https://nextflow.io/docs/latest/reference/process.html

### DIR-penv
`penv`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** there is no `penv` handling in the directive application list or in `buildRequirements` in `nextflowdsl/translate.go`; this is a scheduler-specific directive for SGE-style environments, and wr does not implement an SGE scheduler path here.

## Groovy & Java Imports
Source: https://nextflow.io/docs/latest/reference/stdlib-groovy.html

### METH-groovy-lang
`groovy.lang.*`
Reference: https://nextflow.io/docs/latest/reference/stdlib-groovy.html

**Current behaviour:** `evalStaticMethodCall` only handles `Integer.parseInt` and `evalNewExpr` only recognizes `File`, `Path`, `URL`, `Date`, `BigDecimal`, `BigInteger`, `ArrayList`, `HashMap`, `LinkedHashMap`, and `Random`; there is no `groovy.lang.*` class support, which would require Groovy runtime types beyond this evaluator.

### METH-groovy-util
`groovy.util.*`
Reference: https://nextflow.io/docs/latest/reference/stdlib-groovy.html

**Current behaviour:** there is no `groovy.util.*` handling in `evalStaticMethodCall` or `evalNewExpr` in `nextflowdsl/groovy.go`, so these classes are not modelled by wr's evaluator.

## Groovy Methods & Imports
Source: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-execute
`execute() -> Process`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html
