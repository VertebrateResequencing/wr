# Nextflow research pilot feature review 02

PASS on 2026-10-08. The [charter](charter.md) covers the [prompt](prompt.md)
with a finite, testable experiment. No actionable feature finding remains.
This verdict accepts the research contract; it awards no execution,
translation, complete-language coverage or future wr runtime pass.

Owner: `/root/nextflow_pilot_feature_review02`; queue owner: `/root`.
Worktree: `/home/ubuntu/wr`; branch: `nextflowdsl`. Target: Nextflow 26.04.6,
commit `232b60569865e9a4577e48c1955409238359d6ca`, parser v2.

## Independent source findings

The selected pinned source and document passages were read directly. The
accepted research supplies context; the earlier pilot feature verdict was
not used to decide this review. The owned [checks] independently verify
29 source files against production-lock SHA-256, byte count and Git blob
identity, and record 36 selected source spans. Provenance checks support
the source identity, rather than a claim that these cases executed.

- A1 matches all three selected parser methods. Five invalid inputs have
  twenty predicates, mixed top-level forms have four, and the two params
  inputs have five. Their count, location and message assertions total 29.
  P2 preserves a literal backslash and `n` in its diagnostic. Shared parser
  setup, source string decoding, indentation handling, `main.nf`, analysis,
  syntax-error filtering and location sorting remain explicit obligations.
- A2 matches all three Mix methods and their nine value predicates. M1
  asserts six memberships and one exclusion; M2 and M3 assert sorted-list
  equality. Dsl2Spec state reset and ScriptHelper normalization, lifecycle,
  last-result observation and mock execution are preserved. Pure-data
  evaluation independently confirms both seven-item witnesses satisfy M1
  while failing S-MIX; the six-item permutation satisfies both. This check
  is analytical predicate evidence, not an executed Nextflow mutation.
- A3 retains arity and topic workflow bytes, two checks apiece and both
  fresh/resume invocations. The genuine runner launches `bash -ex .checks`.
  Arity's `set +e` permits the final successful resume expression to hide
  a fresh failure. Topic comparisons remain enforced under errexit.
  Nullable's separate controls preserve four expressions, tee status
  without pipefail and the final-expression aggregate rule.
- Topic's fixture is exactly 22 bytes, with both terminal newline bytes
  represented correctly. Nullable's fixture is exactly 13 bytes and ends
  with two newlines. Their hashes match the charter. Ignore rules, hidden
  fixtures, writable cleanup isolation and effective configuration discovery
  are required rather than inferred from the declared container.
- B1's strict-syntax, typed-workflow, Mix, arity and topic document ranges
  support the selected requirements. Params needs no typing flag, current
  topic needs no preview flag, Mix permits arbitrary order, and its example
  uses string values. Output arity 1 emits a file; other declared arities
  emit lists. Linked semantics and full-language closure remain unresolved.
- The pinned build declarations support Java toolchain 21, Gradle 9.3.1,
  Groovy 4.0.31, Spock 2.4-groovy-4.0 and launcher 1.10.5 as starting pins.
  C1 requires actual transitive, generated-fixture and launch closure; it
  does not treat this list or the runtime distribution as a genuine harness.

## Feature coverage and acceptance

The original denominator is fixed at six Spock methods, eleven input units,
38 Spock predicates, two workflow/check pairs, four CLI invocations and four
literal CLI predicates. The selected methods contain no table rows or
generated providers. A1-A3 and R-UAT-01 require those dimensions, helper
state and assertion dispositions to remain visible instead of translating
each method into a single generic pass.

The handoff in Research artifacts and boundaries requires contracts,
inventory, fixtures and independently authored expectations to be reviewed
and hash-bound before an executable adapter. Expected values have typed
inputs, comparators, observation boundaries and source/document rationale.
Observed Nextflow behavior cannot silently rewrite expected truth. C1
requires genuine original attempts before projections, retained original
failures and reviewed instrumentation. Changed CLI, mock, internal or JVM
observers need a reviewed mapping or an unresolved/internal disposition.

Original contracts, S-MIX/S-ARITY/S-TOPIC strengthening and documented-gap
contracts retain separate origins. B2 gives concrete G-IN and G-OUT arity
violations, requiring the count violation and affected input/output to be
observed. G-SHAPE requires a type-preserving file-versus-list observation;
printed filenames alone cannot earn a mapping pass. These examples identify
gaps in the selected contracts, without alleging whole-suite absence.

C2 and R-UAT-04 require the four bad F1 subjects, two valid counterparts,
the original aggregate outcomes 0, 1, 0, 1 and the distinct stronger outcomes.
Loss controls individually cover all eleven child units, 42 original
predicates, fixtures, completion records, resume invocations and the topic
and nullable newline losses. Each rejection must identify the lost ID or
changed bytes/hash, with an intact counterpart. Infrastructure failure
cannot substitute for the required preservation failure.

R-UAT-02 requires actual prerequisite attempts and captures for scheduled
units, including specific unavailable causes. R-UAT-03 separates original,
partial, unresolved/internal, strengthened, documented-gap, oracle and
pending-wr claims. Unavailable originals stop affected runtime mappings.
Data/control work cannot earn original execution, translation or oracle
passes. No wr pass is available before a supported wr DSL boundary exists.

## Deadline and exit assessment

The four sequential two-hour stages bound the experiment at eight active
hours. Individual acquisition, build, Spock, CLI, projection and Bash-control
limits prevent unbounded prerequisite or observer work. Process-tree cleanup,
timeout captures and accounting for unrun units are explicit requirements.
The plan does not depend on successfully acquiring every JVM prerequisite
within the budget; a measured unavailable result is permitted.

Completion still requires all five UATs. Unfinished controls or independent
review leave the pilot incomplete. R-UAT-05 reconciles every denominator,
preserved assertion, adaptation defect, dependency and review-effort result.
The exit decision uses measured evidence to choose expansion of a proven
family, focused prerequisite/observer research or stopping an unsuitable
mapping. It preserves the later all-upstream and documented-language goals
without requiring a general importer or full-language denominator here.

## Artifacts and completion

Reviewed prompt SHA-256:
`ff606e778e2fa8e23901891830aa991d6f942b56e30b929ac2b62c32bd309d07`

Reviewed charter SHA-256:
`544f9c806dfa459715739596fd18ba8cb2b29f9ee669f9ed014e186213180c4a`

Owned checker SHA-256:
`37d63479381274d5547a6eb67427627c3960bc5fd8a1d0e4ec74e703afa1d091`

Check results SHA-256:
`02d2eb9882e1e643bf621718f1885a9f5412415e17b4b0f9258587f2300cbf62`

All 227 pre-review files in the protected documentation and production
baseline retain their hashes. Only this review and owned scratch were
written. No JVM, Groovy, Nextflow or wr execution, build, download, production
or charter edit, metadata change, commit or push occurred. All owned checks
completed; no tool session, process or delegated work remains live.

[checks]:
  ../../../.tmp/agent/nextflow-conformance/pilot-feature-review02/checks.json
