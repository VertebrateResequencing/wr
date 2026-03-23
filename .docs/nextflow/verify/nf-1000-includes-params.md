# Includes and Parameters

**Source:** https://nextflow.io/docs/latest/reference/syntax.html, https://nextflow.io/docs/latest/module.html

## Includes

### INCL-local
Include from local path:
```groovy
include { PROCESS_A } from './modules/process_a'
```

### INCL-alias
Include with alias:
```groovy
include { PROCESS_A as PROC_B } from './modules/process_a'
```

### INCL-multiple
Multiple includes from same module:
```groovy
include { PROC_A; PROC_B } from './modules'
```

### INCL-remote
Include from remote Git repository.

### INCL-plugin
Include from plugin:
```groovy
include { FOO } from 'plugin/nf-foo'
```

### INCL-addParams
Deprecated `addParams` / `params` option on include:
```groovy
include { FOO } from './foo' addParams(x: 1)
```

### INCL-config
`includeConfig` in config files:
```groovy
includeConfig 'custom.config'
```

### INCL-subworkflow
Include sub-workflows:
```groovy
include { MY_WORKFLOW } from './workflows/my_workflow'
```

## Parameters

### PARAM-assign
Parameter assignment at script top level:
```groovy
params.input = '/data/reads.fq'
```

### PARAM-block
Parameter block (legacy):
```groovy
params {
    input = '/data/reads.fq'
    output = './results'
}
```

### PARAM-typed-block
Typed params block (new syntax). Each parameter has a name, type, and
optional default value:
```groovy
params {
    input: Path
    save_intermeds: Boolean = false
}
```
Only one params block may be defined per script. Types are resolved against
the standard library type system.

### PARAM-cli
Command-line parameter override: `--input /other/path`.

### PARAM-config
Parameters defined in config: `params.input = '/data'`.

### PARAM-nested
Nested parameter maps: `params.options.threads = 4`.

### PARAM-interpolation
Parameter interpolation in strings: `"${params.input}"`.

## Other Top-Level

### SYN-enum
Enum definitions (DSL2 feature flag required):
```groovy
enum Color { RED, GREEN, BLUE }
```

### SYN-record
Record definitions (DSL2 feature flag required):
```groovy
record Sample { String id; Path fastq }
```
