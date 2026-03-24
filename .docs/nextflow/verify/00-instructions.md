# Verification Agent Instructions

You have ONE test file to complete. Follow the checklist in that file.

## Classification criteria

Classify each Feature ID as one of:

### SUPPORTED
Parses without error AND produces behaviour matching Nextflow's semantics.
"Matches" means wr creates jobs achieving the same practical effect. Runtime
differences due to wr's batch-job model are acceptable.

### GAP
Parses without error but behaviour differs — a variant/option is silently
ignored, a value is discarded, or the result doesn't match Nextflow. Always
describe _what_ differs.

### UNSUPPORTED
Cannot work in wr's execution model, requires a full Groovy runtime, or is
platform-specific. Explain why.

### FUTURE
Could theoretically work but requires significant new implementation.

### PARSE_ERROR
Causes a parse error — not in the supported maps, or the parser rejects it.

## Rules

1. **Read the actual Go source code.** The impl files point you to functions —
   use `grep -n 'func functionName'` to find current line numbers.
2. **Parsing is not enough.** A directive that parses but has no effect on job
   creation is a GAP, not SUPPORTED.
3. **Check ALL variants.** An operator may work for closures but fail for
   regex/type arguments. List each variant when behaviour differs.
4. **Cite evidence.** Note the function name or code location for each
   classification.
5. **Don't guess.** If unsure, report `UNKNOWN — could not determine from
   code inspection`.

## Output

Append a `## Results` section at the end of the test file:

```
## Results

- CO-filter: SUPPORTED — `evalChannelClosureBool` in channel.go evaluates closure correctly
- CO-filter-regex: GAP — filter with regex arg not evaluated; closure path only
- CO-first: SUPPORTED — first item returned via `items[0]`
```
