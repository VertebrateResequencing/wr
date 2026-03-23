# Output Qualifiers and Options

**Source:** https://nextflow.io/docs/latest/reference/process.html#inputs-and-outputs-legacy

## Output Qualifiers

### OUT-val
`val(value)` — emit a value. Can reference any variable from process body.
```groovy
output: val x
```

### OUT-path
`path(pattern, [options])` — emit files matching the glob pattern from task directory.
```groovy
output: path 'results/*.txt'
output: path '*.bam'
```
Glob patterns evaluated against task working directory after execution.

### OUT-file
`file(pattern)` — **deprecated** alias for `path`.

### OUT-env
`env(name)` — emit the value of an environment variable from the task.
```groovy
output: env MY_RESULT
```

### OUT-stdout
`stdout` — emit the task's standard output.
```groovy
output: stdout
```

### OUT-eval
`eval(command)` — emit stdout of a command run after the main script.
```groovy
output: eval('samtools --version')
```

### OUT-tuple
`tuple(arg1, arg2, ...)` — emit tuple of values. Each arg is a qualifier.
```groovy
output: tuple val(id), path('*.bam')
```

### OUT-topic
`topic: '<name>'` — send output to a named channel topic.

## Output Options

### OUT-opt-emit
`emit: <name>` — name the output channel (accessible via `.out.<name>`):
```groovy
output:
path '*.bam', emit: 'bams'
```

### OUT-opt-optional
`optional: true` — task won't fail if output is missing:
```groovy
output: path 'maybe.txt', optional: true
```

### OUT-opt-type
`type` option on `path` — restrict to `'file'`, `'dir'`, or `'any'` (default).

### OUT-opt-glob
`glob: true|false` — whether pattern is interpreted as glob (default: `true`).

### OUT-opt-followLinks
`followLinks: true|false` — return link targets instead of links (default: `true`).

### OUT-opt-hidden
`hidden: true|false` — include hidden files (default: `false`).

### OUT-opt-includeInputs
`includeInputs: true|false` — include input files matching the pattern (default: `false`).

### OUT-opt-maxDepth
`maxDepth` — maximum directory depth for matching (default: unlimited).

### OUT-opt-arity
`arity` — expected file count (e.g. `'1'`, `'1..*'`). If set to `'1'`, emits
a single file; otherwise always emits a list.
