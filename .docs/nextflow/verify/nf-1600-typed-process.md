# Typed Process Sections

**Source:** https://nextflow.io/docs/latest/reference/syntax.html (Process (typed)),
https://nextflow.io/docs/latest/reference/process.html (Inputs and outputs (typed))

New in 25.10.0. Requires `nextflow.preview.types` feature flag in every
script that uses them.

## Typed Inputs

### PSEC-typed-input
Typed process inputs use `: Type` annotations instead of qualifier keywords:
```groovy
process greet {
    input:
    greeting: String
    name: String

    script:
    """
    echo "${greeting}, ${name}!"
    """
}
```
Each input has a name and a type (resolved from the standard library).

## Stage Section

### PSEC-stage
`stage:` section — replaces legacy input qualifiers for staging:
```groovy
stage:
env 'NAME', name
stageAs value, filePattern
stdin value
```

### PSEC-stage-env
`env(name: String, value: String)` — declares an environment variable
in the task environment with the specified name and value.

### PSEC-stage-stageAs
`stageAs(value: Path, filePattern: String)` — stages a file into the
task directory under the given alias.
`stageAs(value: Iterable<Path>, filePattern: String)` — stages a
collection of files.

### PSEC-stage-stdin
`stdin(value: String)` — stages the given value as standard input to
the task script.

## Typed Outputs

### PSEC-typed-output
Typed `output:` section uses function calls instead of qualifier keywords.
An output statement can be a variable name, an assignment, or an expression
statement. If it's an expression statement, it must be the only output.
```groovy
output:
greeting = stdout()
```

### PSEC-typed-output-env
`env(name: String)` → String — returns the value of the specified
environment variable from the task environment.

### PSEC-typed-output-eval
`eval(command: String)` → String — returns stdout of the specified
command, executed in the task environment after the task script completes.

### PSEC-typed-output-file
`file(pattern: String, [options])` → Path — returns a file matching
the specified pattern from the task environment.
Options: `followLinks`, `glob`, `hidden`, `includeInputs`, `maxDepth`,
`optional`, `type`.

### PSEC-typed-output-files
`files(pattern: String, [options])` → Set\<Path\> — returns files
matching the given pattern. Same options as `file()` except `optional`.

### PSEC-typed-output-stdout
`stdout()` → String — returns standard output of the task script.

## Topic Section

### PSEC-topic
`topic:` section — consists of one or more topic statements. A topic
statement is a right-shift expression with an output value on the left
and a string on the right:
```groovy
topic:
eval('bash --version') >> 'versions'
```
