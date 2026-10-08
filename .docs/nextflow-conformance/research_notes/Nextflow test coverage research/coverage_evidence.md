# Nextflow upstream test coverage evidence

## Is there an official strict-language specification?

### Takeaway

Yes. Nextflow publishes an official language specification as its Syntax
reference, a migration guide that says the strict parser enforces it, and
an accepted architecture decision describing the formal grammar. This
research found no claim that the upstream tests exhaustively verify that
specification. [Syntax][syntax]; [strict guide][strict]; [accepted ADR][adr].

Research date: 2026-10-08. The target is Nextflow 26.04.6, source commit
`232b60569865e9a4577e48c1955409238359d6ca`, with parser v2. The release page
and pinned changelog agree on 9 July 2026. [Release][release];
[changelog][changelog].

### Cited Findings

- The pinned migration guide calls the Syntax reference the Nextflow
  language specification. It describes strict parsing as an implementation
  of DSL2 restricted to a subset of Groovy syntax. The guide says v2 is
  enabled by default in 26.04. [Strict guide, lines 3-31][strict].
- The pinned accepted ADR says the language specification enumerates every
  supported syntax construct. It links that claim directly to
  `docs/reference/syntax.md`. This is an upstream claim about the language
  description, not a claim about test completeness.
  [ADR, lines 139-163 and 311-317][adr].
- The Syntax reference describes script declarations, statements,
  expressions, and precedence. It delegates typed workflow semantics to
  `workflow-typed.md`, typed process semantics to `process-typed.md`, and
  stage/output functions to the process reference. A requirement inventory
  limited to Syntax would omit those linked descriptions.
  [Syntax, lines 199-228 and 329-347][syntax].
- The ADR explicitly separates the script grammar and config grammar.
  The guide links to Configuration for the description of the config
  language. The four pinned ANTLR files are `ScriptLexer.g4`,
  `ScriptParser.g4`, `ConfigLexer.g4`, and `ConfigParser.g4` in `nf-lang`.
  [ADR, lines 55-96][adr]; [guide, lines 649-657][config-guide];
  [grammar directory][grammars].
- The ADR says strict parsing performs AST construction, include
  resolution, name checking, type checking, and conversion to Groovy AST.
  It claims the error-checking layers guarantee AST validity. That is a
  design statement, not a reported exhaustive test result. The ADR's
  minimal-type-checking note describes its initial implementation; it
  should not override the 26.04 typed-language documentation.
  [ADR, lines 55-96][adr]; [26.04 migration][migration].

### Inferences

- Calling the upstream material only informal docs or only grammar would
  misstate its declared authority. Calling it a complete formal semantics
  would also go beyond the inspected material. It is an official prose
  syntax specification with grammar and implementation evidence and linked
  semantic documentation. [Syntax][syntax]; [ADR][adr].
- Grammar-alternative coverage, documented-behavior coverage, and runtime
  conformance answer different questions. Reaching a grammar alternative
  does not establish the semantics described in another reference.
  [Syntax semantic links][syntax]; [ADR compilation phases][adr].

### Gaps

- No inspected source defines a formal precedence rule for resolving every
  conflict between documentation, grammar, implementation, and observed
  runtime behavior. The language specification exists; such a conflict
  policy remains unestablished.
- No independent formal semantics or finite conformance standard was
  discovered. This is a bounded search result, not proof that none exists.

## What coverage evidence does upstream actually publish?

### Takeaway

The pinned project has parser tests, runtime tests, integration workflows,
executable documentation snippets, and JaCoCo report generation. None of
those inspected sources establishes exhaustive coverage of documented
language requirements. No measured coverage percentage or complete
requirement-to-test matrix was obtained. [Language tests][langtests];
[build configuration][build]; [CI][ci]; [snippet runner][snippets].

### Cited Findings

- Root `build.gradle` applies JaCoCo to all projects, finalizes tests with
  `jacocoTestReport`, makes the report depend on tests, and excludes Groovy
  closure classes from report class directories.
  [Build, lines 69-75 and 174-191][build].
- The pinned CI Build lane executes `make test` for Java 17 and 25. It
  uploads `**/build/reports/tests/test`. The inspected upload paths do not
  include JaCoCo report directories. CI also uploads integration and
  validation archives. This confirms report generation configuration and
  upload configuration, not a numeric coverage result.
  [CI, lines 37-40, 88-118, and 186-199][ci].
- JaCoCo defines counters from bytecode and optional debug information.
  Its branch counter covers `if` and `switch` branches and excludes
  exception handling. Its line counter records whether at least one
  instruction associated with a line ran. These metrics do not directly
  measure documented language requirements. [JaCoCo counters][jacoco].
- The pinned `nf-lang` test directory contains 12 Groovy test files. The
  inventory includes script/config AST builders, name/include resolution,
  type checking, formatters, config option inference, and path helpers.
  The retained inventory counts quoted feature declarations only, so it
  is not an executed-test count. For example, the script AST builder
  asserts a diagnostic's count, line, column, and message for invalid
  syntax. [Test directory][langtests]; [script AST test][asttest];
  [local counted inventory][evidence].
- The main runtime module separately contains v1/v2 script-loader tests,
  v1/v2 config-parser tests, and v2 process-file-input/output tests.
  Their presence establishes additional test locations, not completeness.
  [Runtime script tests][runtimetests]; [runtime config tests][configtests];
  [runtime parameter tests][paramtests].
- CI's integration matrix distinguishes legacy integration, parser-v2,
  documentation, AWS, Azure, Google, and Wave modes. Legacy integration
  sets v1 and runs both `tests/` and `tests-v1/`; parser-v2 sets v2 and runs
  `tests/`. The shell harness contains ignore lists and parser-specific
  exclusions. Cloud modes can skip when credentials are missing, and the
  integration matrix can skip for a `[ci fast]` commit. A release gate
  accepts a skipped integration job.
  [CI, lines 123-134 and 252-254][ci];
  [validation runner, lines 48-126][validation];
  [integration runner, lines 73-99][integration].
- Static recursive enumeration found 115 `.nf` files under `tests/`, 91
  under `tests-v1/`, and 97 under `docs/snippets/`. The retained JSON lists
  their paths. These are file counts; they do not count shell assertions,
  nested modules, parameter rows, enabled CI cases, or passing tests.
  [Pinned tests][tests]; [legacy tests][legacytests];
  [snippet directory][snippetdir]; [inventory][evidence].
- Documentation snippets execute a default `*.nf` glob. The runner checks
  sorted output when a matching `.out` exists and prints a skipped message
  when no expected output exists. It does not enumerate the Markdown
  reference's every fenced example, and sorting removes output-order
  evidence from that comparison. [Snippet runner, lines 3-27][snippets].
- An upstream developer reported finding config-v2 issues by testing
  nf-core configs, including parameter propagation and evaluating disabled
  profiles. That historical PR was merged on 6 March 2025. It is evidence
  of regression discovery outside an existing suite, not evidence of a
  current unfixed defect or an exhaustive test strategy. [PR 5854][pr5854].
- The pinned changelog records strict-parser fixes in the target's release
  history, including task hashes, enums, config inclusions, tuple
  assignments, and nested Groovy class resolution. Those fixes show
  concrete regression subjects; they do not quantify remaining gaps.
  [Pinned changelog][changelog].

### Inferences

- JaCoCo percentages, even if recovered, would complement a language
  requirement audit. They could identify unexecuted compiler/runtime code;
  they could not establish that each documented behavior has a meaningful
  assertion, that every valid/invalid combination was exercised, or that
  outputs are correct. [JaCoCo metric definitions][jacoco];
  [AST assertion example][asttest].
- Passing all discovered upstream tests would support agreement on the
  assertions actually retained and run. It would not inherit exhaustive
  language coverage from file counts, a green CI badge, or the ADR's claim
  that the specification enumerates syntax. [CI][ci]; [ADR][adr].
- The strongest defensible conclusion from current evidence is
  "exhaustiveness has not been established." The evidence does not support
  either "the suite is exhaustive" or "every undocumented test gap is a
  known missing behavior."

### Gaps

- No upstream requirement-to-test matrix, semantic coverage denominator,
  complete conformance suite declaration, or exhaustive test claim was
  discovered in the bounded local and web searches. Full search expressions
  and retrieval failures are retained in [search log][searchlog]. Absence
  from these results is not proof of absence.
- Exact release-commit CI outcomes, skipped-case counts, JaCoCo HTML/XML,
  and numerical percentages were not obtained. The accessible recent
  STABLE-26.04 Actions page did not expose a 26.04.6 match. No artifacts
  were downloaded, builds run, or maintainer contacts made.
- This research did not map every documentation requirement to every
  upstream assertion or prove a specific semantic requirement uncovered.
  It therefore cannot estimate a semantic coverage percentage.

## What does parser v2 include, and what can wr claim?

### Takeaway

Parser v2 is the strict DSL2 script and config front end. At the target it
also admits gated typed-language features and continues to use Groovy/JVM
execution and extension mechanisms. Selecting v2 is not a complete scope
statement for language or runtime conformance. [ADR][adr];
[typed processes][typedprocess]; [typed workflows][typedworkflow];
[Groovy preservation][preserve].

### Cited Findings

- The pinned runtime defaults `NXF_SYNTAX_PARSER` to v2 and uses separate
  script-loader and config-parser factories for v1/v2.
  [NF, lines 29-36][nf]; [script loader factory][scriptfactory];
  [config parser factory][configfactory].
- The strict guide says legacy DSL2 accepts arbitrary Groovy syntax,
  while v2 accepts a subset for scripts/configs. It also documents v2-only
  language features beginning in 25.10. Legacy DSL2 and strict DSL2 should
  therefore be separate compatibility scopes. [Guide, lines 13-31][strict].
- Typed processes are marked preview and require v2 plus
  `nextflow.enable.types = true` in each using script. Typed workflows
  require that same flag; their `params` and `output` blocks can be used
  without it. The 26.04 migration labels static typing a preview feature.
  [Typed processes, lines 3-41][typedprocess];
  [typed workflows, lines 3-17][typedworkflow];
  [26.04 migration, lines 93-99][migration].
- The guide describes moving arbitrary Groovy code into `lib/`, which
  supports full Groovy, or plugins that provide custom included functions.
  The ADR names replacing the Groovy runtime as a non-goal and says scripts
  continue through Groovy compilation and execution.
  [Guide, lines 723-732][preserve]; [ADR, lines 37-47 and 86-96][adr].
- The target `nf-lang` build declares ANTLR `4.13.2.6`, Groovy `4.0.31`,
  and PF4J `3.14.1`. Grammar source is compiled during the Java build.
  These are concrete implementation dependencies, not an independent
  language conformance guarantee. [Module build, lines 16-39][langbuild].
- The existing wr lock preserves 2,856 files for the exact pinned commit.
  Its 18 batches are explicitly named bootstrap batches. A retained source
  inventory and bootstrap selections should not be called all-language
  test coverage. The research verified 23 cited files against their locked
  SHA-256 hashes without changing the lock or batches.
  [wr lock][wrlock]; [wr batches][wrbatches]; [hash manifest][evidence].

### Inferences

- A later wr assurance design needs explicit choices for untyped strict
  scripts, strict config, typed preview constructs, Groovy/JVM library
  calls, `lib/`, plugins, and environment-dependent runtime integrations.
  One parser flag does not answer those choices. [Strict guide][strict];
  [config guide][config-guide]; [typed processes][typedprocess];
  [Groovy preservation][preserve].
- Upstream assertions are useful initial obligations and regression
  witnesses. Establishing documented-language coverage requires a separate
  reviewed mapping from versioned requirements to preserved assertions,
  with unsupported requirements and unverified mappings visible. This is
  a recommendation derived from the missing matrix evidence; it is not an
  upstream mandate or a final wr architecture decision.

### Gaps

- Mutable docs pages were opened for discovery and context. All target
  conclusions above use exact pinned source bytes. Current website text,
  recent master changelog search results, VS Code project coverage claims,
  and nf-core strict-health statistics were not used to assign target
  semantic coverage. The VS Code feature counts pipeline test associations,
  and the nf-core reports concern pipeline lint health, not Nextflow's
  own exhaustive language tests. [Current VS Code README][vscode].
- The ADR date says 2025-05-08, while its PR merged in May 2026 and describes
  retrospective documentation. Its initial-type-checking note therefore
  remains historical design context. The pinned typed references are the
  target evidence for current typed features. [ADR][adr]; [PR 7150][pr7150].
- The research ends here without choosing translation mechanics or revising
  the spec. Those decisions belong to the caller's separate assertion reuse
  and requirement coverage work.

[syntax]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/reference/syntax.md#L199-L347
[strict]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/strict-syntax.md#L3-L31
[adr]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/adr/20250508-strict-syntax-parser.md
[release]: https://github.com/nextflow-io/nextflow/releases/tag/v26.04.6
[changelog]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/changelog.txt
[config-guide]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/strict-syntax.md#L649-L657
[grammars]: https://github.com/nextflow-io/nextflow/tree/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/src/main/antlr
[migration]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/migrations/26-04.md#L93-L99
[langtests]: https://github.com/nextflow-io/nextflow/tree/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/src/test
[build]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/build.gradle#L174-L191
[ci]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/.github/workflows/build.yml
[snippets]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/snippets/test.sh#L3-L27
[jacoco]: https://www.jacoco.org/jacoco/trunk/doc/counters.html
[asttest]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/src/test/groovy/nextflow/script/parser/ScriptAstBuilderTest.groovy#L30-L56
[runtimetests]: https://github.com/nextflow-io/nextflow/tree/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/test/groovy/nextflow/script/parser
[configtests]: https://github.com/nextflow-io/nextflow/tree/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/test/groovy/nextflow/config/parser
[paramtests]: https://github.com/nextflow-io/nextflow/tree/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/test/groovy/nextflow/script/params/v2
[validation]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/validation/test.sh#L48-L126
[integration]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/run.sh#L73-L99
[tests]: https://github.com/nextflow-io/nextflow/tree/232b60569865e9a4577e48c1955409238359d6ca/tests
[legacytests]: https://github.com/nextflow-io/nextflow/tree/232b60569865e9a4577e48c1955409238359d6ca/tests-v1
[snippetdir]: https://github.com/nextflow-io/nextflow/tree/232b60569865e9a4577e48c1955409238359d6ca/docs/snippets
[pr5854]: https://github.com/nextflow-io/nextflow/pull/5854
[typedprocess]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/process-typed.md#L3-L41
[typedworkflow]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/workflow-typed.md#L3-L17
[preserve]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/strict-syntax.md#L723-L732
[nf]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/main/groovy/nextflow/NF.groovy#L29-L36
[scriptfactory]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/main/groovy/nextflow/script/ScriptLoaderFactory.groovy#L33-L44
[configfactory]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/main/groovy/nextflow/config/ConfigParserFactory.groovy#L32-L43
[langbuild]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/build.gradle#L16-L39
[wrlock]: /home/ubuntu/wr/nextflowconformance/data/sources.lock.json
[wrbatches]: /home/ubuntu/wr/nextflowconformance/data/batches.json
[evidence]: /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-coverage-evidence/pinned-evidence.json
[searchlog]: /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-coverage-evidence/search-log.json
[vscode]: https://github.com/nextflow-io/vscode-language-nextflow#project-view
[pr7150]: https://github.com/nextflow-io/nextflow/pull/7150
