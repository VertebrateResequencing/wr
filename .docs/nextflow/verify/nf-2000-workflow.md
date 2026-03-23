# Workflow Blocks

**Source:** https://nextflow.io/docs/latest/workflow.html, https://nextflow.io/docs/latest/reference/process.html

## Features

### WF-unnamed
Unnamed/entry workflow — the pipeline's main entry point:
```groovy
workflow {
    data = Channel.of('a', 'b', 'c')
    PROCESS_A(data)
}
```

### WF-named
Named sub-workflows:
```groovy
workflow MY_PIPELINE {
    take: input_ch
    main:
        STEP_A(input_ch)
        STEP_B(STEP_A.out)
    emit:
        STEP_B.out
}
```

### WF-take
`take:` section — declares workflow input channels (named sub-workflows only).

### WF-main
`main:` section — the workflow body (optional label in unnamed workflow).

### WF-emit
`emit:` section — declares output channels (named sub-workflows only).

### WF-publish
`publish:` section — declares publish targets within workflow.

### WF-pipe
Pipe operator for chaining:
```groovy
Channel.of(1,2,3) | PROC_A | PROC_B
```

### WF-process-call
Process invocation with arguments:
```groovy
PROCESS_A(channel_a, channel_b)
```

### WF-subwf-call
Sub-workflow invocation:
```groovy
MY_PIPELINE(input_data)
```

### WF-output-access
Accessing process outputs:
```groovy
PROCESS_A.out             // default output
PROCESS_A.out.bams        // named output (emit: 'bams')
PROCESS_A.out[0]          // indexed output
```

### WF-if-else
Conditional execution in workflow body:
```groovy
workflow {
    if (params.mode == 'fast') {
        FAST_PROC(data)
    } else {
        SLOW_PROC(data)
    }
}
```

### WF-onComplete
`workflow.onComplete` handler — runs after pipeline finishes.

### WF-onError
`workflow.onError` handler — runs when pipeline encounters an error.

### WF-output-block
Top-level `output {}` block for defining publish targets:
```groovy
output {
    directory params.outdir
    'bams' {
        path '*.bam'
    }
}
```
