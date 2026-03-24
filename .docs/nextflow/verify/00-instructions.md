# Nextflow Feature Verification System

## Purpose

This directory contains a structured verification system for determining which
Nextflow features are supported, partially supported (gaps), unsupported, or
candidates for future implementation in the wr `nextflowdsl` package.

The system breaks verification into small, self-contained micro-tasks that an
LLM agent can complete reliably in a single session.

## File types

### Spec files (`nf-*.md`)

Describe what Nextflow defines for specific features, sourced from the official
Nextflow reference documentation. Each file:

- Has a unique sortable name (e.g. `nf-3000-operators-filtering.md`)
- Links to the official Nextflow documentation section
- Lists every feature with a unique **Feature ID** (e.g. `CO-filter`)
- Describes expected behaviour including all variants and options

### Implementation files (`impl-*.md`)

Describe what the wr implementation code actually does. Each file:

- Points to specific Go source files and function names
- Describes what each function/map/struct handles
- Notes what falls through to defaults, warnings or errors

### Test files (`test-*.md`)

Verification micro-tasks. Each file IS a task for a verifying agent. Each:

- References specific `nf-*` and `impl-*` files
- Asks specific, answerable verification questions
- Provides clear acceptance criteria
- Specifies the output format with Feature IDs

## Feature ID scheme

Every individual feature has a unique ID used across all documents:

| Prefix   | Category                          |
|----------|-----------------------------------|
| `SYN-`   | Syntax features                   |
| `STMT-`  | Statements                        |
| `METH-`  | Groovy methods                    |
| `INCL-`  | Include features                  |
| `PARAM-` | Parameter features                |
| `PSEC-`  | Process sections                  |
| `INP-`   | Input qualifiers/options          |
| `OUT-`   | Output qualifiers/options         |
| `DIR-`   | Process directives                |
| `WF-`    | Workflow features                 |
| `CF-`    | Channel factories                 |
| `CO-`    | Channel operators                 |
| `CFG-`   | Config scopes/settings            |
| `BV-`    | Built-in variables and objects    |
| `GF-`    | Global functions                  |

## How verification works

A verifying agent receives ONE test file as its task. It must:

1. Read the referenced `nf-*` file(s) to understand what Nextflow expects
2. Read the referenced `impl-*` file(s) to understand where to look in code
3. Read the **actual source code** at the cited function names (use grep to
   find current line numbers — they shift between commits)
4. For each Feature ID, classify as one of the categories below

## Classification criteria

### SUPPORTED

The feature:
- Parses without error, AND
- Produces behaviour matching Nextflow's semantics

"Matches" means: given the same inputs, wr creates jobs whose commands and
resource requests achieve the same practical effect. Exact runtime differences due
to wr's batch-job model (no long-lived channels, no daemon processes) are
acceptable as long as the final result is equivalent.

### GAP

The feature:
- Parses without error, but behaviour differs from Nextflow, OR
- Parses but a specific variant/option is silently ignored, OR
- Parses but the value is discarded (no effect on job creation), OR
- Would be straightforward to fix/complete in the existing code

Always describe _what_ differs: "parses but `by:` option is ignored" or "`cross`
always does Cartesian product, ignores closure key argument".

### UNSUPPORTED

The feature:
- Cannot work in wr's execution model (explain why), OR
- Requires a full Groovy runtime that wr doesn't provide, OR
- Is specific to a platform/service wr doesn't integrate with

Examples: `subscribe` (needs long-lived channel), `watchPath` (needs filesystem
watcher daemon), cloud-specific config scopes.

### FUTURE

The feature:
- Could theoretically work with wr's dynamic-job system
- But requires significant new implementation
- Not commonly used enough to prioritise now

### PARSE_ERROR

The feature causes a parse error — it is not in the supported operator/factory
maps, or the parser rejects the syntax.

## Important rules for verifying agents

1. **Read the actual code**, not just the impl descriptions. The impl files
   point you to specific functions — you must read the real source to verify
   behaviour. Use `grep -n 'func functionName'` to find current line numbers.

2. **"Parses without error" is necessary but NOT sufficient for SUPPORTED.**
   A directive that parses but has no effect on job creation is a GAP.

3. **Check ALL variants.** An operator may work for closures but fail for
   literal/regex/type arguments. List each variant separately when behaviour
   differs.

4. **Cite evidence.** For each classification, note the specific function name
   or code location that confirms the behaviour.

5. **Don't guess.** If you cannot determine behaviour from code inspection,
   report `UNKNOWN — could not determine from code inspection`.

6. **One test file = one agent session.** Never try to verify multiple test
   files in a single session. If context is running low, finish the current
   test file and stop.

## Output format

Results for each test file go into a `## Results` section at the end of the test
file. Example:

```
## Results

- CO-filter: SUPPORTED — `evalChannelClosureBool` in channel.go evaluates closure correctly
- CO-filter-regex: GAP — filter with regex arg not evaluated; closure path only
- CO-filter-type: GAP — type qualifier filtering not implemented
- CO-first: SUPPORTED — first item returned via `items[0]`
- CO-first-condition: GAP — condition variants (regex, type, closure) ignored
```

Final consolidated results go into:
- `.docs/nextflow/supported.md` — features classified as SUPPORTED
- `.docs/nextflow/gaps.md` — features classified as GAP
- `.docs/nextflow/unsupported.md` — features classified as UNSUPPORTED
- `.docs/nextflow/future.md` — features classified as FUTURE

Each entry references its Feature ID for unambiguous identification.

## Source code overview

The implementation lives in `nextflowdsl/` with these key files:

| File             | Lines | Role                                        |
|------------------|-------|---------------------------------------------|
| `parse.go`       | ~5600 | Lexer, parser, operator/factory maps        |
| `channel.go`     | ~2400 | Channel factory + operator resolution       |
| `translate.go`   | ~6300 | AST → wr job translation                    |
| `groovy.go`      | ~5200 | Groovy expression evaluator                 |
| `config.go`      | ~1400 | Config file parsing                         |
| `ast.go`         | ~250  | AST type definitions                        |
| `params.go`      | ~400  | Parameter handling                          |
| `module.go`      | ~300  | Module/include handling                     |
| `module_load.go` | ~200  | Module loading                              |

## Nextflow reference URLs

- Syntax: https://nextflow.io/docs/latest/reference/syntax.html
- Process: https://nextflow.io/docs/latest/reference/process.html
- Channel factories: https://nextflow.io/docs/latest/reference/channel.html
- Operators: https://nextflow.io/docs/latest/reference/operator.html
- Config: https://nextflow.io/docs/latest/reference/config.html
- Standard library: https://nextflow.io/docs/latest/reference/stdlib.html
- Standard library types: https://nextflow.io/docs/latest/reference/stdlib-types.html
- Standard library namespaces: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html
- Processes (typed): https://nextflow.io/docs/latest/process-typed.html
- Feature flags: https://nextflow.io/docs/latest/reference/feature-flags.html

## Reliable creation methodology

**Problem:** LLM agents consistently miss edge features when asked to enumerate
all items from a large reference page in one pass.

**Solution — extraction-first, manifest-validated approach:**

### Phase 1: Extract manifest

1. Fetch each Nextflow reference page listed above (one at a time).
2. For each page, extract **every** H2/H3/H4 heading mechanically.
   Use `grep -oE '#{2,4} .+'` on the markdown source, or parse the HTML
   heading tags. Do NOT rely on LLM memory — read the fetched content.
3. Write each heading as a row in `00-manifest.md` with columns:
   `Heading | Feature ID(s) | nf- file`.
4. Assign a feature ID to each heading (or `—` if intentionally excluded).

### Phase 2: Create/update nf- files

5. For each nf- file, include ONLY the feature IDs that appear in the
   manifest for that file's scope. Copy the exact feature description from
   the reference page — do not paraphrase from memory.
6. Cross-check: every feature ID in the manifest must appear in exactly one
   nf- file.

### Phase 3: Create/update impl- files

7. For each impl- file, search the Go source for handling of every feature ID
   assigned to that file's scope. Use `grep -n` to find current line numbers.

### Phase 4: Create/update test- files

8. For each test- file, list every feature ID from its referenced nf- file(s).

### Phase 5: Validate

9. Run `python3 .docs/nextflow/verify/00-audit.py --verbose` to check for
   orphaned, missing, or structurally inconsistent feature IDs. The audit
   checks: nf↔test cross-references, nf↔manifest coverage, and test file
   completeness. Optionally add `--fetch` to also check live Nextflow docs
   for missing content.
10. Fix any discrepancies before declaring the file set complete.

### Key principles

- **Never enumerate features from memory.** Always read the fetched page
  content and extract headings.
- **One reference page per agent pass.** Don't try to cover multiple pages
  in one context window.
- **Manifest is the contract.** If a heading exists on a reference page,
  it must have a row in the manifest. If a feature ID exists in an nf- file,
  it must appear in the manifest.
- **Validate mechanically.** Run the validation script. Human spot-checks
  are insufficient.
