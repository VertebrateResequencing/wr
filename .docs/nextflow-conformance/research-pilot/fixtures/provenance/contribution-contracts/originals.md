# Frozen original contracts

Phase 1 Item 1.1. Original-only contribution for pinned Nextflow 26.04.6, commit
`232b60569865e9a4577e48c1955409238359d6ca`. All execution and projection
dispositions are pending. This record freezes assertions and source bytes. It
awards no runtime, oracle, translation or wr pass.

## Denominators and inventory

Six Spock methods contain eight parser units P1-P8 and three Mix units M1-M3.
Parser has 29 predicates; Mix has nine. Two workflow/check pairs have four
invocations and four literal CLI predicates. Table rows and generated providers
are both zero. Each predicate, helper, invocation, completion record and fixture
has a stable ID in inventory.json and expectations.json.

## Literal bytes and parser state

fixtures/P*.literal.groovy retains delimiter-to-delimiter expressions. The .raw
files retain their un-decoded bodies. The .decoded.nf files apply only genuine
selected Groovy literal rules: the backslash-LF after the triple-single opening
suppresses the leading LF. Nested triple-double script strings remain plain
content. Indentation, blank lines and twelve terminal spaces remain in decoded
bytes. There is no interpolation in these selected outer literals.

TestUtils.check applies contents.stripIndent(), names the source main.nf, parses
and analyzes, then reads the returned source error collector. It keeps
SyntaxErrorMessage causes and sorts by start line then column. A collector
without errors yields an empty list. Exceptions remain separate observations.
The .normalized.nf fixtures record the selected ASCII twelve-space stripIndent
derivation, including the final LF. Runtime dispatch and observed input bytes
require Phase 2 verification; these data checks cannot award that pass.

setupSpec creates one @Shared ScriptParser. Keep that instance across methods in
declared source order and subcases P1-P5, P6, then P7-P8. No new reset is added.
The compiler source map and parser source/analysis implementation are frozen as
supporting state dependencies. Preserve genuine feature selection/order and any
resulting state behavior rather than assuming a clean parser per unit.

P1-P7 each retain count, start line, start column and original message
separately. P8 retains only count zero. P6 uses contains; other messages use
equality. P2 source diagnostic contains two backslashes followed by n; Groovy
decoding gives backslash+n bytes, never a newline. Message hexadecimal bytes are
recorded per expectation.

## Literal parser predicates

### P1

```groovy
errors.size() == 1
errors[0].getStartLine() == 2
errors[0].getStartColumn() == 14
errors[0].getOriginalMessage() == "Unexpected input: '3'"
```

### P2

```groovy
errors.size() == 1
errors[0].getStartLine() == 2
errors[0].getStartColumn() == 16
errors[0].getOriginalMessage() == "Unexpected input: '\\n'"
```

### P3

```groovy
errors.size() == 1
errors[0].getStartLine() == 3
errors[0].getStartColumn() == 24
errors[0].getOriginalMessage() == "Unexpected input: 'val'"
```

### P4

```groovy
errors.size() == 1
errors[0].getStartLine() == 3
errors[0].getStartColumn() == 40
errors[0].getOriginalMessage() == "Unexpected input: 'emit'"
```

### P5

```groovy
errors.size() == 1
errors[0].getStartLine() == 4
errors[0].getStartColumn() == 6
errors[0].getOriginalMessage() == "Unexpected input: ','"
```

### P6

```groovy
errors.size() == 1
errors[0].getStartLine() == 1
errors[0].getStartColumn() == 1
errors[0].getOriginalMessage().contains "Statements cannot be mixed with script declarations"
```

### P7

```groovy
errors.size() == 1
errors[0].getStartLine() == 1
errors[0].getStartColumn() == 1
errors[0].getOriginalMessage() == "Params block cannot be defined without an entry workflow"
```

### P8

```groovy
errors.size() == 0
```

## Mix input, lifecycle and predicates

M1 has numeric queue [1,2,3], string queue [a,b] and string value z. M2 has
numeric value 1 and queue [2,3]. M3 has numeric values 1 and 2. Preserve integer
versus string types. Mix outer triple-single literals keep their leading LF,
twelve-space indentation and terminal twelve spaces. No TestUtils stripIndent is
added.

Before each feature Dsl2Spec resets TaskProcessor, ScriptMeta and Global, then
initializes NF. BaseSpec retains begin/close logging. The class timeout is five
seconds. runScript initializes and starts MockSession, constructs ScriptBinding,
selects the loader, parses text and runs it. The v2 loader captures the final
statement. ScriptHelper normalizes that result before firing, awaiting and
destroying the dataflow network, then throws session.error if present. Keep
every lifecycle observation and completion separately from the final value
predicates.

Normalization keeps channel read-channel creation, value-source selection,
singleton ChannelOut unwrapping and array/list element normalization. Mock
scriptlets return script text and status zero; this supplies no proof of real
shell execution. Helper observation IDs retain the loader assert session and
assert mainScript preconditions, parser instance identity, error sorting,
network sequence, error propagation and runner aggregate status separately from
the 42 selected predicates. A future neutral observer needs reviewed equivalence
or an unresolved/internal disposition for these mechanisms.

M1 reads runScript(...).val and checks six memberships plus exclusion of c. It
does not count items or forbid extra values other than c. Both [1,2,3,a,b,z,1]
and [1,2,3,a,b,z,d] are analytical witnesses satisfying its seven literal
conditions; no Groovy run is claimed. Strengthened exact-six multiset
requirements belong to the independent document contribution. M2/M3 call
result.val.sort() and compare exact numeric lists, retaining multiplicity.

### M1

```groovy
1 in result
2 in result
3 in result
'a' in result
'b' in result
'z' in result
!('c' in result)
```

### M2

```groovy
result.val.sort() == [1,2,3]
```

### M3

```groovy
result.val.sort() == [1,2]
```

## Fresh and resumed CLI originals

The complete workflow, checks, expected bytes, runner, configuration and ignore
files are frozen in fixtures/source/. Original commands remain $NXF_RUN and
$NXF_RUN -resume. The runner sets NXF_RUN to $NXF_CMD -q run ../../<script>,
executes bash -ex .checks in each check directory, reads its status, and
writes/report-propagates failure. Its destructive cleanup must run only in an
owned disposable layout. Hidden .checks and .expected survive the rm -rf * glob.

Arity calls set +e. Its two [[ $? == 0 ]] || false expressions consume each
immediately preceding engine status and retain independent outcomes. The final
resume expression can yield aggregate zero after fresh failure. Original
aggregate and stronger per-invocation gates remain different claims. Source
valid counts are 1, 2 and 1..*. No contents, invalid-count, scalar/list or
cache-reuse assertion is added to the original.

Topic leaves inherited errexit active. Its two cmp versions.txt .expected ||
false expressions compare fresh and resumed files. A failed unguarded engine
invocation or final false stops later checks. Keep each engine exit, reached
comparison, file bytes and completion separately. The original byte oracle is 22
bytes, bar: 0.9.0\nfoo: 0.1.0\n, including the final LF. SHA-256 is
ce5e4c500ca731aa86fa5e5a3856b9bdbe3c64f685d0e51b2ba5d1886b13bceb. No cache-reuse
proof follows from these comparisons.

The declared quay.io/nextflow/tests container does not establish that containers
execute. Actual Nextflow, Java, TEST_JDK, WITH_DOCKER, cwd, discovered configs,
effective executor/container and command/environment require runtime capture.
Export NXF_SYNTAX_PARSER=v2; preserve original flags and add no typing/topic
preview flag. Available ignore files are frozen; the selected check-directory
members and absent .IGNORE-JAVA-* enumeration are recorded.

## Original aggregate and completion

Each Spock feature succeeds only when every reached literal condition succeeds
and the feature completes under its original lifecycle. A failing earlier then
block can prevent later subcases from running; preserve those unrun
dispositions. Exception, timeout, skip, signal and non-completion never become
value passes. Each CLI pair keeps its original checks subprocess aggregate, each
literal predicate outcome and independent invocation exit. Retain complete
source semantics even when a proposed observer cannot expose an internal
observation.

## Provenance and pending prerequisites

provenance.json binds reused source archive, locked full-file SHA-256 and byte
count, canonical Git blob, modes, inclusive lines, byte offsets and each span
SHA-256. Fixtures bind derived and copied bytes separately. No resource was
acquired. Full test/helper files preserve imports; pinned build, settings,
wrapper and module declarations preserve the prerequisite starting points.
Actual resolved transitive dependencies, generated classes, compiler/test launch
and JUnit ordering remain Phase 2 work.

No Nextflow, Groovy, JVM, Gradle or Bash original/control execution occurred.
The bounded author script only materializes declared research data and verifies
source identity. Independent validation checks source spans, fixture bytes,
accounting and Markdown mechanics; those limited checks cannot prove engine
behavior. R-UAT-01 remains owned by the independent handoff review and root.
