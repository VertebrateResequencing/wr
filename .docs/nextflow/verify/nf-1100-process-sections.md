# Process Sections

**Source:** https://nextflow.io/docs/latest/reference/process.html

## Features

### PSEC-input
`input:` section — declares process inputs. Contains qualifier declarations.
```groovy
process FOO {
    input:
    val x
    path reads

    script:
    "echo $x"
}
```

### PSEC-output
`output:` section — declares process outputs for downstream consumption.
```groovy
output:
path 'results/*'
val x
```

### PSEC-script
`script:` section — the Bash script to execute. Can use `$variable` or
`${expression}` interpolation. The `script:` label is optional if it is the
last section.
```groovy
script:
"""
echo hello ${name}
"""
```

### PSEC-shell
`shell:` section — like script but uses `!{var}` for Nextflow variables,
leaving `${var}` for Bash. Requires `'` (single-quoted) heredoc content.
```groovy
shell:
'''
echo !{name}
echo ${BASH_VAR}
'''
```

### PSEC-exec
`exec:` section — native Groovy execution (no external command).
```groovy
exec:
result = input.toUpperCase()
```

### PSEC-stub
`stub:` section — a lightweight script for dry-run / stub execution.
```groovy
stub:
"""
touch output.txt
"""
```

### PSEC-when
`when:` guard — conditionally skip process execution.
```groovy
when:
params.doStep == true
```
The process is skipped (not executed) when the condition is false.
Downstream channels receive no items for skipped binding sets.
