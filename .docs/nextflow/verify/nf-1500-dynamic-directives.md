# Dynamic Directives and Task Object

**Source:** https://nextflow.io/docs/latest/reference/process.html#task-properties,
https://nextflow.io/docs/latest/process.html#dynamic-directives

## Dynamic Directives

### DIR-dynamic
Any directive can be a closure that is evaluated at runtime:
```groovy
memory { 2.GB * task.attempt }
errorStrategy { task.exitStatus == 137 ? 'retry' : 'terminate' }
queue { task.attempt > 1 ? 'long' : 'short' }
```
The closure has access to the `task` object and `params`.

## Task Object Properties

### BV-task-attempt
`task.attempt` — the current retry attempt number (starts at 1).

### BV-task-exitStatus
`task.exitStatus` — exit code of the task. Available in script/shell blocks and
errorStrategy closures.

### BV-task-hash
`task.hash` — the unique task hash. Only in `exec:` blocks.

### BV-task-index
`task.index` — process-level sequential task index.

### BV-task-name
`task.name` — the task name. Only in `exec:` blocks.

### BV-task-previousException
`task.previousException` — exception from previous attempt. Only when
`task.attempt > 1`.

### BV-task-previousTrace
`task.previousTrace` — trace record from previous attempt. Only when
`task.attempt > 1`.

### BV-task-process
`task.process` — the name of the process.

### BV-task-workDir
`task.workDir` — the task working directory path. Only in `exec:` blocks.

### BV-task-directives
Directive values accessible as `task.<directive>`:
`task.cpus`, `task.memory`, `task.time`, `task.disk`, `task.container`,
`task.ext`, `task.queue`, etc.
