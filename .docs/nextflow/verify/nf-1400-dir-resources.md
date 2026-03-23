# Directives — Resources

**Source:** https://nextflow.io/docs/latest/reference/process.html#directives

## Features

### DIR-cpus
`cpus` — number of logical CPUs for the task.
```groovy
process FOO {
    cpus 8
    script: "blastp -num_threads ${task.cpus}"
}
```
Accessible at runtime as `task.cpus`. Affects scheduler resource request.

### DIR-memory
`memory` — memory allocation.
```groovy
memory 2.GB
memory '512 MB'
```
Units: B, KB, MB, GB, TB. Affects scheduler resource request.

### DIR-time
`time` — maximum wall-clock time.
```groovy
time 1.h
time '2d 6h'
```
Units: ms, s, m, h, d. Affects scheduler resource request.

### DIR-disk
`disk` — local disk allocation.
```groovy
disk 2.GB
```
Same units as memory. Only used by certain executors.
