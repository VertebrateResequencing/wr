# Proofreading Review 01

Verdict: FIXED

Spec: `.docs/nextflow-conformance/spec.md`

Before SHA-256:
`15c9b78860dd205e9d454a996d528cfee596dd6df15e81ba063123ba45ecfd86`

After SHA-256:
`80a0fb713aedc6492e4b56432c4c319a71075c0d5bd90a8096a4ca4ecb6cff8e`

## Corrections

- Changed all six lettered section headings to the required
  `## Section <Letter>:` form.
- Put `Path` and `Iterable<Path>` in code spans in B1_02 so Markdown does
  not interpret the generic type argument as an HTML tag.
- Removed leading spaces from ten record paths in the schema TSV table.
- Rejoined broken prose lines in the placeholder rule, fair-case protocol,
  and semantic-mutation explanation without changing their wording.

## Validation

Read the entire spec. Checked all 12 stories and 47 acceptance tests for
sequential numbering, matching sections, resolved references, and inclusion
in the implementation order. Checked ASCII text, 80-column prose wrapping,
whitespace, heading levels, and fenced-code languages. Technical contracts
and acceptance IDs are unchanged. No feature-coverage review was performed.
