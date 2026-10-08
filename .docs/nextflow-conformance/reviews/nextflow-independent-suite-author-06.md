# Independent Suite Author Revision 06

Verdict: RESOLVED. Both closed-record ambiguities in proofreading 03 now
have explicit field owners. The six-record audit also assigns related
prose references to declared fields.

## Scope and identities

- Worktree: `/home/ubuntu/wr`; branch: `nextflowdsl`; owner: `/root`.
- Author: `/root/nextflow_suite_spec_author06`.
- Initial spec SHA-256:
  `0ad8942ba1de97f644044ddf8ec9d8fda5001923c02ac7c8e37d2b8d2a826a0e`.
- Revised spec SHA-256:
  `d31bb8200d9b4bf690ed8d5eda50d8ad57887175c43ce6002f47e6797d642f2d`.

Authority is [the prompt][prompt] and [the accepted pilot report][pilot],
read with [its independent acceptance][pilot-review]. The author read the
actual [proofreading 03 report][proof], all six new record descriptions,
relevant existing model/review/attempt contracts, agent-conduct, completion
and liveness, spec-author, go-conventions, unslop, writing-for-agents,
prose-principles and final-response. No external research was needed.

## Proofreading ambiguities resolved

1. Observation boundary is derived, not stored. `case_id` and
   `contract_hash` identify the reviewed contract by ID and hash. Its unique
   `routes` entry matching the observation's `engine` supplies boundary.
   `route` remains `neutral`. The closed observation field list is unchanged;
   no `boundary` key is added. A missing or ambiguous matching route cannot
   satisfy this relationship.
2. Selection approval is `suite.review_id`. Dependency approval is the
   `review_id` on the `dependencies.lock.json` file referenced by the suite's
   `authority_inputs` and used by D3's attempt `dependency_lock`. Both review
   IDs must be nonnull, accepted and current before ready family execution.
   No per-family or native-selection review field is introduced. The suite
   review covers its fixed selections; the separate dependency review covers
   the execution closure.

The second fix also states where the dependency lock is bound. Naming its
review field alone would leave the suite-to-lock relationship unspecified.
Existing explicit review-input bindings and freshness checks still apply.

## Six closed-record audit

All six top-level field lists and eleven nested closed field lists retain
identical bytes. The following audit covers prose references to field owners,
not a claim of complete implementation or runtime validation.

- `upstream`: replace singular fixture prose with declared `fixtures`, an
  array of original fixture `file_ref`s. `source_spans` binds original spans.
  Parent, order, dependencies and disposition use declared fields. The
  retained rationale stays in `literal_record`; no rationale key is added.
- `mappings`: `contract_ids` names the contracts stating each preserved
  predicate; mapping `boundary` matches their selected `routes` boundary.
  Replace generic contract/observer array names with `contract_ids` and
  `observer_files`. Strength, partition and rationale still use declared
  fields; equivalence still needs an independent source argument and controls.
- `contracts`: strengthened purpose is carried by `upstream_ids` and its
  source-derived reason by `facet_ids`. These references resolve through the
  existing original-obligation and reviewed facet records. No purpose or
  reason field is added. Expected/normalization grammar and route shape stay
  unchanged; both engines still share independently reviewed expected truth.
- `observations`: derive boundary through the reviewed contract identified
  by declared `case_id` and `contract_hash`. Raw and checked result fields
  still use the declared receipt/typed grammar. Native-original attempts
  remain separate and create no neutral values or engine passes.
- `suite`: identify the dependency lock through `authority_inputs`, then
  assign selection/dependency review IDs to the two existing `review_id`
  fields. Family, native-selection, invocation, edge, pending and control
  prose uses their declared closed shapes. Required denominators stay fixed.
- `dependencies`: generated output digests belong to resource `file`
  references. Command receipts, including logical/effective argv/environment,
  belong to attempt `artifacts`; the implementation-tree digest belongs to
  execution attempts' D2 inputs. These outputs were already required; the
  change identifies their owners instead of implying undeclared recipe
  receipt/digest fields. Resource and recipe field lists remain unchanged.

No further reference to an undeclared field was found in these descriptions.
This audit does not replace independent feature review.

## Technical classification and preserved meaning

This is a technical cross-record contract clarification, not text-only
proofreading. It chooses concrete field relationships where more than one
implementation could previously have been inferred. Validators must enforce
unique engine-route resolution, matching mapping boundaries, the bound
execution lock and its current review, and the stated provenance owners.
Strengthening explicitly uses original IDs and reviewed facet IDs for its
already required purpose and source-derived reason.

No closed schema shape changes. No established behavioural obligation,
expected value/comparator, fixture/source identity, completion requirement,
selection denominator, public API or engine capability changes. All 69
acceptance bodies are byte-identical to this task's input; the original 49
obligations remain present. Nine exact replacement regions are confined to
Independent suite records and routes. Every other spec byte is identical.
Fresh feature reviews must assess these technical choices before clean
proofreading; this author awards neither review acceptance nor a runtime pass.

## Completed passive checks

- Applying the nine retained replacements reproduces the revised spec;
  reversing them reproduces the exact initial bytes and hash.
- All 69 unique acceptance IDs and their complete bodies, 17 story IDs and
  their order are unchanged. All 49 original acceptance IDs remain present.
- All six top-level and eleven nested closed field lists are byte-identical.
- Implementation Order and the appendix are byte-identical, preserving the
  six-phase order. All bytes before the edited section and from Bounded
  accounting and unfinished work onward are identical.
- Phase 1 historical evidence, unfinished pilot/wr boundaries, all four
  proofreading 02 grammar fixes and the explicit pilot F1 label are retained.
- All 1,292 saved other input paths retain their initial SHA-256 hashes,
  including package/model files, source lock, batches, pilot records,
  expectations, fixtures, prompt, phase plans and earlier review evidence.
- Spec and this report pass ASCII, final-newline, 80-column prose, whitespace,
  heading, fence-language and local/reference-link checks.

The passive checker, initial bytes, replacements, exact diff and check result
are retained in owned `author06/` beneath the revision scratch directory
named in the prompt. Run the checker from the recorded worktree:

```bash
timeout 30s python3 .tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/author06/check.py
```

These are document/preservation checks. No implementation, engine, build or
new behavioural test was run; no execution or implementation acceptance is
implied. Writes are confined to spec.md, this new report and owned author06
scratch. No other file, commit, push, nested agent or system installation was
created. All owned commands completed. No owned live process, background job,
tool session or outstanding wait remains.

[proof]: nextflow-independent-suite-proof-03.md
[prompt]: ../prompt.md
[pilot]: ../research-pilot/pilot-report.md
[pilot-review]: ../research-pilot/pilot-report-review.md
