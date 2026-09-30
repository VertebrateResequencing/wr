# Nextflow local-origin model implementation

IMPLEMENTED. Independent review is pending. This correction does not award
phase 1 completion or cover acquisition, snapshot safety, or public CLI digest
failures; reuse owns those checks.

Artifact origins now accept strict HTTPS or `local:sha256:` followed by
64 lowercase hexadecimal digits. Local origins require Java or
environment-tool role, file packaging, null coordinate, and a digest matching
`file.sha256`. Lock and tree origins retain strict HTTPS. The generated lock
schema enforces syntax and shape. Its `x-wr-record-rules` annotation identifies
digest equality as a decoder relational check.

## Verification

The evidence directory is
`.tmp/agent/nextflow-conformance/local-origin-model/`.
Its `commands.json` records every exact command, exit, log path, and log size.
Full logs and the initial owned-file snapshots remain in that directory.

| Check | Exit | Result |
| --- | ---: | --- |
| `red-go` | 1 | Valid local origins fail before the correction. |
| `red-stock` | 1 | Stock schema rejects the valid Java local origin. |
| `green-targeted` | 0 | New decoder cases pass. |
| `schema-generation` | 0 | All eleven generated schemas match. |
| `green-stock` | 0 | Eleven schemas and 1,243 mutations pass. |
| `green-final` | 0 | Focused model, closure, security and CLI tests pass. |
| `format-final` | 0 | Edited Go files pass cleanorder. |
| `lint-final` | 1 | Fourteen deferred source.go findings; zero owned findings. |
| `audit-final` | 0 | Baseline preservation and schema boundaries pass. |

The 53 new common cases cover both accepted roles, malformed local and
network origins, all five forbidden roles with positive HTTPS controls,
packaging, coordinate, required strings, and lock/tree rejection. Four
independent mutations change either the local digest or `file.sha256` for
each permitted role. Go rejects them with the digest-specific error; stock
schema accepts them. These relational cases stay outside the common cases.

The initial audit expected definitions under `$defs`; the generator inlines
them under `properties`. The corrected audit checks the actual artifact
schema location. Both audit logs are retained.

## Review inputs

`source-correction.diff` is the complete seven-file source/schema/fixture
change, including initially untracked files: 35,652 bytes.
`changed-functions.txt` contains all eight changed/new functions: 7,296 bytes.
`measurements.json` records function line spans, per-case sizes, schema
condition sizes, fixture sizes, and evidence sizes. `new-cases.json` contains
only the 53 added cases: 11,728 compact bytes. The fixtures are 3,139 and
4,187 bytes. The audit preserved all 1,190 baseline cases, the other ten
schemas byte for byte, and module and lint configuration files.

`correction.diff` adds this report to the source diff. No production wr,
acquisition, specification, or phase/progress files were edited. No commit or
push was made.
