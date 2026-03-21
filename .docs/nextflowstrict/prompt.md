# Feature: Complete Nextflow DSL2 Parsing and Execution Support

## Context

We have an existing `nextflowdsl` Go package (implemented per
`.docs/nextflowdsl/spec.md`) that parses Nextflow DSL 2 workflow files and
translates them into `jobqueue.Job` slices for wr execution. The package
currently handles a subset of DSL2 syntax. We need to fill in the gaps so that
real-world DSL2 pipelines (including nf-core pipelines) parse without error and
execute correctly.

The official Nextflow DSL2 syntax reference is at
https://nextflow.io/docs/latest/reference/syntax.html with sub-pages for
process directives, channel factories, and operators.

## General Principles (must be maintained)

- Pure Go — no CGo dependencies for this package, no shelling out to Nextflow.
- No unnecessary intermediate files written to disk.
- No separate state store — crash recovery relies on wr's job persistence.
- Dynamic workflows use wr's live dependency feature + `--follow` polling.
- `Override=0` always — wr's resource learning is the primary mechanism.
- Existing tests must continue to pass; new features add new tests.
- The existing `nextflowdsl/` package is extended in-place (no new package).

## Gap Analysis: What's Missing

The gaps below were identified by comparing our implementation against the
official Nextflow syntax reference (https://nextflow.io/docs/latest/reference/syntax.html),
the process reference (https://nextflow.io/docs/latest/reference/process.html),
the channel factories reference (https://nextflow.io/docs/latest/reference/channel.html),
and the operators reference (https://nextflow.io/docs/latest/reference/operator.html).

### A. Parsing Gaps — Syntax That Causes Parse Errors

#### A1. Tuple I/O (CRITICAL — used by >90% of real pipelines)

Process inputs and outputs commonly use `tuple`:

```nextflow
input:
tuple val(id), path(reads)

output:
tuple val(id), path("${id}.bam")
```

Our parser only handles standalone `val`, `path`, `file` declarations, not
`tuple(...)` compound declarations. This alone will cause failure on virtually
all nf-core and most real-world pipelines.

#### A2. Workflow `take:` / `emit:` / `publish:` Sections

Named subworkflows commonly use:

```nextflow
workflow ALIGN {
    take:
    reads_ch

    main:
    BWA_MEM(reads_ch)

    emit:
    bam = BWA_MEM.out.bam
}
```

Our parser does not handle `take:`, `emit:`, or `publish:` section labels in
workflow blocks. The `main:` label itself may not be handled. This prevents
proper modular workflow parsing.

#### A3. Function Definitions

```nextflow
def getGenomeAttribute(attribute) {
    params.genomes && params.genomes[params.genome] ?
        params.genomes[params.genome][attribute] : null
}
```

Function definitions with `def` are common in real pipelines. Not parsed.

#### A4. Additional Process Sections

- `stub:` section — used for dry-run/testing, increasingly common
- `exec:` section — alternative to `script:` for Groovy-native processes
- `shell:` section — deprecated but still used (uses `!{var}` interpolation)
- `when:` section — deprecated but still used for conditional execution

#### A5. Additional Process Input/Output Types

- `stdout` output — common
- `env(name)` input and output
- `stdin` input
- `eval(command)` output
- Output `emit:` labels (e.g., `path "*.bam", emit: bam`)
- Output `optional: true`
- Output `topic:` labels

#### A6. Shebang, Block Comments, Feature Flags

- `#!/usr/bin/env nextflow` shebang line — should be skipped
- `/* ... */` block comments — should be skipped
- `nextflow.preview.recursion = true` feature flag assignments — should be
  skipped or stored

#### A7. Top-level Output Block

```nextflow
output {
    samples {
        path 'fastq'
    }
}
```

New workflow output definition syntax. Should be parsed (can be ignored for
execution since wr handles output differently).

#### A8. Enum and Record Types (lower priority — new in 26.04)

```nextflow
enum Day { MONDAY, TUESDAY }
record FastqPair { id: String; fastq_1: Path }
```

New type system features. Lower priority but should at least not cause parse
errors.

### B. Expression/Groovy Gaps

#### B1. Ternary and Elvis Operators (CRITICAL)

```nextflow
memory { 8.GB * task.attempt }
cpus { task.attempt > 1 ? 8 : 4 }
errorStrategy { task.exitStatus in [137,140] ? 'retry' : 'finish' }
container { workflow.containerEngine == 'singularity' ? 'path.sif' : 'image:tag' }
ext.args = params.option ?: '--default'
```

Extremely common in dynamic directives. Our Groovy evaluator only handles
simple literals, arithmetic, comparisons, and string interpolation.

#### B2. List and Map Literals

```nextflow
[id, reads]
[key: 'value', other: 123]
```

Fundamental Groovy constructs used everywhere in tuple construction and
channel operations.

#### B3. Variable Declarations and Assignments

```nextflow
def reads_ch = Channel.fromFilePairs(params.reads)
def (id, reads) = item
```

`def` keyword for variable declarations in workflow blocks.

#### B4. Control Flow

```nextflow
if (params.aligner == 'bwa') {
    BWA_MEM(reads_ch)
} else {
    BOWTIE2(reads_ch)
}
```

Conditional process execution in workflows.

#### B5. Method Calls and Property Access

```nextflow
file.getName()
reads.size()
params.genomes[params.genome].fasta
x instanceof String
```

Common Groovy method calls and property access patterns.

#### B6. Closures with Parameters

```nextflow
.map { meta, reads -> [meta, reads.flatten()] }
.filter { meta, reads -> reads.size() >= 2 }
```

Multi-parameter closures used extensively with operators.

### C. Channel Factory Gaps

Currently supported: `of`, `value`, `empty`, `fromPath`, `fromFilePairs` (5/12).

Missing:
- `Channel.from()` — deprecated but still widely used in existing pipelines
- `Channel.fromList()` — common
- `Channel.fromSRA()` — used in genomics pipelines
- `Channel.watchPath()` — used for streaming/monitoring pipelines
- `Channel.topic()` — newer feature for implicit channel sharing
- `Channel.interval()` — periodic emission
- `Channel.fromLineage()` — new data lineage feature

Priority: `fromList` and `from` are commonly encountered. Others are niche.

### D. Channel Operator Gaps

Currently supported: `collect`, `filter`, `map`, `flatMap`, `first`, `last`,
`take`, `groupTuple`, `join`, `mix` (10/48).

Missing operators grouped by real-world frequency:

**High priority (commonly used):**
- `branch` — multi-output routing by condition
- `combine` — cross product of two channels
- `concat` — ordered concatenation  
- `set` — assign channel to variable
- `view` — debugging/logging (should be no-op or warning for wr)
- `ifEmpty` — default value for empty channels
- `splitCsv` — parse CSV into records
- `splitFasta` / `splitFastq` — split sequence files
- `transpose` — inverse of groupTuple
- `unique` / `distinct` — deduplication
- `toList` / `toSortedList` — collect to list
- `flatten` — flatten nested lists
- `count` — count items
- `reduce` — fold/accumulate
- `multiMap` — split to multiple named outputs
- `tap` — tee a channel
- `dump` — debug logging (no-op for wr)
- `collectFile` — collect items to files

**Medium priority (less common but valid):**
- `cross` — cross product with key matching
- `merge` — positional merge (deprecated-ish)
- `buffer` / `collate` — windowing
- `max` / `min` / `sum` — aggregation
- `randomSample` — random subset
- `splitJson` / `splitText` — split structured text
- `toInteger` — type conversion
- `until` — take until condition
- `subscribe` — terminal callback (may not translate to wr)

**Low priority (bioinformatics-specific counting):**
- `countFasta` / `countFastq` / `countJson` / `countLines`

### E. Process Directive Gaps

Currently handled: `cpus`, `memory`, `time`, `disk`, `container`, `maxForks`,
`errorStrategy`, `maxRetries`, `env`, `publishDir` (10 directives).

Unrecognized directives are already gracefully skipped with a warning, so these
don't cause parse errors. However, some have execution significance:

**Should be actively handled:**
- `tag` — useful for logging/identification
- `label` — used for config-based resource selection
- `conda` — alternative to container for environment management  
- `module` — environment modules
- `scratch` — use local scratch disk
- `storeDir` — persistent result caching
- `cache` — control caching behavior
- `maxErrors` — total error budget across instances
- `debug` / `echo` — forward stdout
- `beforeScript` / `afterScript` — pre/post hooks
- `secret` — secret management
- `ext` — custom directive namespace
- `containerOptions` — additional container flags
- `accelerator` — GPU requests
- `resourceLabels` — cloud resource tags
- `resourceLimits` — resource ceilings
- `fair` — ordered output emission
- `shell` — custom shell
- `stageInMode` / `stageOutMode` — file staging modes

**Can remain as skip-with-warning (executor-specific):**
- `executor` — wr is the executor
- `queue` — wr manages queuing
- `clusterOptions` — wr doesn't use cluster schedulers
- `penv` — SGE-specific
- `pod` — Kubernetes-specific
- `machineType` — cloud-specific
- `arch` — architecture specification
- `array` — job array batching
- `maxSubmitAwait` — scheduling timeout
- `spack` — Spack package manager

### F. Config Parsing Gaps

Our config parser handles `params {}`, `process {}`, and `profiles {}`. Missing:

- `includeConfig 'path'` — include other config files (very common)
- `docker { enabled = true }` scope
- `singularity { enabled = true }` scope  
- `conda { enabled = true }` scope
- `env {}` scope — environment variables
- `manifest {}` scope — pipeline metadata
- `timeline {}`, `report {}`, `trace {}`, `dag {}` scopes — reporting
- `executor {}` scope
- `params {}` with nested maps and ternary expressions
- Config closures and conditional blocks
- `-c` / `-params-file` loading chain

### G. Translation/Execution Gaps

Even for parsed constructs, translation gaps exist:

- Tuple I/O binding to jobs (most critical once parsed)
- Subworkflow `take:/emit:` wiring into the dependency DAG
- `branch`/`multiMap` multi-output operator translation
- `combine`/`cross`/`concat` multi-channel operator translation
- Conditional workflow logic (`if/else` → conditional job creation)
- Dynamic directive evaluation with ternary/elvis (retry scaling etc.)
- `splitCsv`/`splitFasta`/`splitFastq` creating fan-out jobs
- `collectFile` creating fan-in jobs
- `stdout` output capture and forwarding
- Proper `emit:` label resolution for `process.out.name` access

## Notes

- Focus on parsing robustness first: the parser should accept all valid DSL2
  without error, even if some constructs are not yet translated to wr jobs.
  Untranslatable constructs should produce clear warnings, not errors.
- For translation, prioritize the constructs that appear in > 80% of real
  pipelines: tuple I/O, workflow take/emit, common operators, dynamic
  directives.
- The existing test suite and spec in `.docs/nextflowdsl/spec.md` document the
  current behavior — new work extends it, not replaces it.
- Consult the Nextflow official syntax reference at
  https://nextflow.io/docs/latest/reference/syntax.html for authoritative
  grammar details.
- This spec covers one subsystem of the larger work. Separate specs will be
  created for other subsystems (Groovy evaluator, channel operators, config
  parser). This spec focuses on the core parsing completeness and tuple I/O
  translation.
- Tuple I/O should be fully supported including dynamic translation via
  TranslatePending (not just parse-only).
- The Groovy expression evaluator should reach "core" level: full
  ternary/elvis/property access/method calls, but not closures or complex type
  system. Closures remain as raw text evaluated by pattern matching.
- For channel operators: parse all high-priority operators without error, but
  only translate a smaller MVP subset. Untranslatable operators produce clear
  warnings.
- Acceptance tests use synthetic minimal workflows exercising each feature, not
  real nf-core pipelines.
- Module and config resolution should support both local includes and GitHub
  remote modules (the existing module.go already handles this; extend as
  needed for includeConfig).
- This spec's scope is: critical parsing gaps (A1-A7) + tuple I/O translation.
  Groovy evaluator enhancements, channel operator additions, and config parser
  improvements are deferred to separate follow-up specs.
- AST types may be refactored as needed (breaking changes to exported fields
  are acceptable). Callers such as cmd/nextflow.go will be updated accordingly.
- For channel operators: this spec only ensures new high-priority operators
  parse without error. Translation remains at the current 10 operators. New
  operator translation is deferred to the channel operators spec.
- The goal of this spec is: after implementation, the parser should accept
  syntactically valid DSL2 files without parse errors for the constructs
  covered (A1-A7). Constructs not yet translatable produce clear warnings, not
  errors. Tuple I/O is fully translated including dynamic paths.
- Dynamic directives referencing task context (task.attempt, task.exitStatus)
  should continue the current approach: evaluate statically where possible,
  fall back to defaults with Override=0 for task-context references.
- Function definitions should be parsed and stored in the AST for potential
  inline resolution when evaluating expressions.
- Process sections stub:, when:, exec:, and shell: should all be parsed to
  prevent errors, with a warning if they are not yet translatable.
- Workflow emit label resolution (process.out.labelName) should be fully
  resolved in the dependency DAG.
- includeConfig is in scope for this spec: the config parser should follow
  includeConfig directives to load referenced config files.
