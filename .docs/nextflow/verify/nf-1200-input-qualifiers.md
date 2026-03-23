# Input Qualifiers and Options

**Source:** https://nextflow.io/docs/latest/reference/process.html#inputs-and-outputs-legacy

## Input Qualifiers

### INP-val
`val(identifier)` — receive any value as a variable.
```groovy
input: val x
```

### INP-path
`path(identifier | stageName)` — receive file(s), staged into task directory.
```groovy
input: path reads
input: path 'reads.fq'        // staged with specific name
input: path '*.fq'            // staged matching glob
```

### INP-file
`file(identifier | stageName)` — **deprecated** alias for `path`. Same
behaviour but converts non-file values to string files.

### INP-env
`env(name)` — receive value as environment variable exported in task environment.
```groovy
input: env MY_VAR
```

### INP-stdin
`stdin` — receive value as standard input to task script.
```groovy
input: stdin
```

### INP-tuple
`tuple(arg1, arg2, ...)` — receive tuple of values. Each argument is a qualifier.
```groovy
input: tuple val(id), path(reads)
```

### INP-each
`each` — cross-product input: the process runs for every combination of `each`
values with the regular inputs.
```groovy
input:
path genome
each mode
```

## Input Options

### INP-opt-stageAs
`stageAs` / `name` option on `path` — rename file in task directory:
```groovy
input: path reads, stageAs: 'input.fq'
```

### INP-opt-arity
`arity` option on `path` — expected file count, e.g. `'1'` or `'1..*'`:
```groovy
input: path reads, arity: '2'
```
