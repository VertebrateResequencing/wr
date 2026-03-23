# Directives — Publishing

**Source:** https://nextflow.io/docs/latest/reference/process.html#publishdir

## Features

### DIR-publishDir
`publishDir` — publish output files to a specified folder.
```groovy
publishDir '/data/results'
publishDir '/data/results', mode: 'copy'
```
Can be specified multiple times for different targets/patterns.

**Sub-options (all part of DIR-publishDir):**

### DIR-publishDir-path
`path:` — target directory. `publishDir '/some/dir'` is shorthand for
`publishDir path: '/some/dir'`.

### DIR-publishDir-mode
`mode:` — publishing method:
- `'symlink'` (default) — absolute symbolic link
- `'rellink'` — relative symbolic link
- `'link'` — hard link
- `'copy'` — copy files
- `'copyNoFollow'` — copy without following symlinks
- `'move'` — move files (only for terminal processes)

### DIR-publishDir-pattern
`pattern:` — glob pattern to select which output files to publish.
```groovy
publishDir '/data', pattern: '*.bam'
```

### DIR-publishDir-saveAs
`saveAs:` — closure to rename or reroute published files. Return `null` to skip.
```groovy
publishDir '/data', saveAs: { fn -> fn.replaceAll('.bam', '.sorted.bam') }
```

### DIR-publishDir-overwrite
`overwrite:` — overwrite existing files (default: `true` for normal run,
`false` for resumed run).

### DIR-publishDir-enabled
`enabled:` — enable/disable this rule (default: `true`).

### DIR-publishDir-failOnError
`failOnError:` — abort on publish failure (default: `true` since 24.03.0).

### DIR-publishDir-contentType
`contentType:` — MIME type for S3 uploads. Experimental.

### DIR-publishDir-storageClass
`storageClass:` — S3 storage class. Experimental.

### DIR-publishDir-tags
`tags:` — S3 object tags. Experimental.

### DIR-debug
`debug` — forward task stdout to pipeline stdout.
```groovy
debug true
```
Also available as deprecated `echo`.
