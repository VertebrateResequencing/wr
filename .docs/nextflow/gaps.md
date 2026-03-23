# Nextflow Features — Gaps

These Nextflow features are not yet covered in `supported.md`,
`unsupported.md`, or `future.md`. They could potentially be implemented.

## Config Scopes

### `workflow`

Top-level workflow execution settings including `failOnIgnore` (exit
non-zero when `ignore` error strategy is used) and output publishing
options: `mode` (copy/move/link/symlink/rellink), `overwrite`,
`contentType`, `storageClass`, `tags`, `copyAttributes`, `enabled`,
`ignoreErrors`. New in Nextflow 24.10.

nf-core pipelines are beginning to migrate from `publishDir` to the
workflow output block model. The `workflow.output` sub-settings will
become increasingly common.

The `publish:` workflow section and `output {}` block target paths are
supported for translation. The `workflow` config scope's output sub-settings
(mode, overwrite, etc.) are not yet mapped — wr uses copy mode for output
publishing. Implementing mode/overwrite could be done by extending the
existing publish translation to respect these settings.

## Path/File Output Options

Output declarations like `path '*.bam', type: 'file'` accept several
named options beyond `emit:` and `optional:`. The following cause
**parse errors** when encountered:

- `type` — restrict to `'file'`, `'dir'`, or `'any'` (default: `'any'`).
  Occasionally used in nf-core to filter directory outputs.
- `glob` — enable glob pattern matching (default: `true`). Rarely set
  explicitly since the default is already true.
- `followLinks` — follow symbolic links when collecting outputs.
- `hidden` — include hidden files (dotfiles) in glob results.
- `includeInputs` — include input files in output collection.
- `maxDepth` — maximum directory traversal depth for glob.
- `arity` — min/max file count constraint (e.g. `arity: '1..*'`).
  Parsed within tuple elements but the value is discarded; errors at the
  top-level declaration.

Implementing `type` would be the highest-value addition since it appears
in some nf-core modules. The others are rarely used.

## Path/File Input Options

Input declarations like `path x, stageAs: 'custom_name'` accept:

- `name` / `stageAs` — rename the staged input file. Cause **parse
  errors**. Used occasionally in nf-core when a process needs input files
  with specific names.
- `arity` — min/max multiplicity. Parsed but discarded in tuple elements;
  errors at top-level.

## publishDir Options

The `publishDir` directive currently supports `path`, `mode`, and
`pattern`. The following options cause **parse errors**:

- `saveAs` — closure to rename published files. Used in many nf-core
  pipelines to customise output file names.
- `overwrite` — whether to overwrite existing files (default: `true`).
- `enabled` — conditionally enable/disable publishing.
- `failOnError` — fail the task if publishing fails.
