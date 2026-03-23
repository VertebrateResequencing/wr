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
