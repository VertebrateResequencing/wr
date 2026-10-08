# Independent Suite Author Revision 07

Verdict: RESOLVED. The two provenance labels raised in proofreading 04 now
have definitions and links at their uses. No further comparable undefined
pilot-generation or provenance shorthand was found in spec prose.

## Scope and identities

- Worktree: `/home/ubuntu/wr`; branch: `nextflowdsl`; owner: `/root`.
- Author: `/root/nextflow_suite_spec_author07`.
- Initial spec SHA-256:
  `d31bb8200d9b4bf690ed8d5eda50d8ad57887175c43ce6002f47e6797d642f2d`.
- Revised spec SHA-256:
  `53691311d01e970d6b6676ce8cb4989d121e1e2df7b6143689dcb00e0685649b`.

Authority is [the prompt][prompt], [the accepted pilot report][pilot] and
[its independent acceptance][pilot-review]. Read the actual
[proofreading 04 report][proof], the original independent findings, supervisor
correction author/sidecar/review evidence, the frozen research manifest and
handoff seal. Applied agent-conduct, completion and liveness, spec-author,
go-conventions, unslop, writing-for-agents, prose-principles and final-response.
No external research was needed.

## Definitions and provenance audit

1. D3 defines [PREREQ-F1][prereq-f1] as the historical supervisor defect that
   left a detached TERM-ignoring child alive while recording
   `completed=true`. Its link resolves to the original independent finding.
   [Correction review 02][correction] accepts a separately retained
   pidfd/subreaper supervisor. Historical failure and corrected evidence
   remain distinct. The accepted pilot explicitly distinguishes this defect
   from the charter's F1 Bash controls; E3's existing F1 definition remains.
2. F3 defines [P01][p01] as the historical finding that Git does not preserve
   group-write permissions. Its link resolves to the independent permission
   finding. The [historical seal][seal] contains exactly 79 records with mode
   `0o664`, matching that finding and the accepted pilot. Existing F3 prose
   still distinguishes full historical modes from portable executable-bit
   identity and retains all historical metadata unchanged.

Scanned the spec for comparable prose labels and checked their definitions
or authority bindings. F11 is already defined by its named schema finding and
review link. F1 is already defined by its Bash subject inventory and result
field. P/M/CLI unit labels, S/G contract labels and H helper-obligation IDs
are original structured identities bound to the frozen inventories and E3
family authority; they do not need repeated dictionaries. Pending-work IDs
are defined by their own bullets. R01 and D01 are historical pilot findings
but do not appear in spec prose. Generation counts and revision hashes name
explicit historical records or commits; no additional provenance label needs
an expansion. This audit is limited to shorthand provenance references and
awards no independent feature-review or implementation verdict.

## Classification and preservation

This is text-only provenance copyediting. The two definitions restate accepted
historical evidence and point to its original independent findings. They add
no design choice, schema field, validator rule, execution obligation,
expectation, denominator, API or engine capability. P01's existing F3
portable-identity rule is unchanged.

Three exact replacements cover two prose regions and two reference-link
additions. Applying them reproduces the revised spec; reversing them
reproduces the exact input bytes and hash. Every unrelated spec byte remains
identical. The passive checker also verifies:

- All 69 unique acceptance IDs and complete bodies are byte-identical. All
  49 original acceptance IDs remain present; all 17 story IDs retain order.
- The entire independent-suite record section is byte-identical, preserving
  all six top-level and eleven nested closed field lists and revision 06's
  field-owner clarifications.
- Implementation Order and appendix prose are byte-identical. Their only
  following change is the two new reference definitions.
- All four proofreading 02 grammar fixes and the explicit F1 Bash label are
  retained. Original source, fixture, expected-result and selection identities
  remain unchanged.
- All 1,296 other enumerated protected input paths retain their initial
  SHA-256 hashes, including prompt, phase plans, prior reviews, pilot records,
  package/model files, source lock and batches.
- Spec and this report pass ASCII, final-newline, 80-column prose, whitespace,
  heading, named-fence and local/reference-link checks.

Initial bytes, the replacement list, exact diff, preservation manifest,
checker and check result are retained in owned `author07/` below the revision
scratch directory named in the prompt. Run from the recorded worktree:

```bash
timeout 30s python3 .tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/author07/check.py
```

These are passive document/preservation checks. No implementation, engine,
build or new behavioural test was run. Writes are confined to spec.md, this
new report and owned author07 scratch. No unrelated file, commit, push,
nested agent or system installation was changed or created. All owned
commands completed. No owned live process, background job, tool session,
child agent or outstanding wait remains.

[proof]: nextflow-independent-suite-proof-04.md
[prompt]: ../prompt.md
[pilot]: ../research-pilot/pilot-report.md
[pilot-review]: ../research-pilot/pilot-report-review.md
[prereq-f1]: ../research-pilot/prerequisites-review-01.md
[correction]: ../research-pilot/prerequisites-review-02.md
[p01]: ../research-pilot/contracts-review.md
[seal]: ../research-pilot/phase1-handoff-seal.json
