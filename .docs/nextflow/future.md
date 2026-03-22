# Nextflow Features — Future Possibilities

These Nextflow features are not currently supported but could be implemented
using wr's existing dynamic workflow capabilities if there is demand. Each
would require a special job that performs the dynamic operation.

## Channel Factories

### `Channel.watchPath`

Watches a filesystem path for new files and emits them as they appear.
Used for streaming/live pipelines that process data as it arrives. Very
rare — almost exclusively used in real-time sequencing monitoring.

Could be implemented by creating a special watcher job that monitors the
filesystem and dynamically creates new downstream jobs as files appear,
using wr's existing dynamic workflow support.

### `Channel.fromSRA`

Fetches sequencing data from NCBI's Sequence Read Archive by accession
number. Requires network access and SRA toolkit.

Could be implemented by creating a custom job that calls the SRA API,
resolves the accessions, and dynamically creates downstream jobs for
each resolved file.

### `Channel.topic`

Creates a topic-based pub/sub channel — processes publish to a named
topic and subscribers receive items. A coordination mechanism for
decoupled workflow stages. Very rare — new feature (v23.11+).

Could be supported with special dynamic jobs that act as topic brokers,
collecting published items and fanning out to subscriber processes.

### `Channel.interval`

Emits incrementing integers at fixed time intervals. Example:
`Channel.interval('1 sec')`. Used for periodic polling. Very rare.

Could be supported with a special dynamic job that wakes at the
configured interval and creates downstream jobs with incrementing
values.

## Process Output Qualifiers

### `topic:` output qualifier

Publishes output to a named topic channel (pub/sub pattern). Example:
`output: topic 'results', path('*.txt')`. Paired with `Channel.topic`.
Very rare — new feature, experimental.

Would need to be implemented alongside `Channel.topic` support.

## Groovy Methods

Additional Groovy/Java stdlib methods beyond the ~30 most commonly used
ones. These are not used in nf-core but may appear in bespoke pipelines:

- `subsequences` — all subsequences of a list
- `permutations` — all permutations of a list
- `combinations` — all combinations of lists
- `execute` — run external command (String method)
- `getText` — read URL/File contents
