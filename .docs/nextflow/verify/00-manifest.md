# Reference Manifest

This file is the **single source of truth** for completeness checking.
Every section heading from the official Nextflow reference is listed here.
Each heading is annotated with the feature ID(s) that cover it, or `—` if
intentionally excluded with a reason.

**Methodology:** This manifest was extracted by fetching each reference page
and listing every H2/H3/H4 heading. Feature IDs are then assigned from
nf-*.md files. The validation script `00-audit.py` checks that every
heading has a covering feature ID and every feature ID maps to a heading.

---

## syntax.html — Script declarations
<!-- Source: https://nextflow.io/docs/latest/reference/syntax.html -->

| Heading | Feature ID(s) | nf- file |
|---------|--------------|----------|
| Comments | SYN-comments | nf-0100 |
| Shebang | SYN-shebang | nf-0100 |
| Feature flag | SYN-feature-flag | nf-0100 |
| Include | INCL-local, INCL-alias, INCL-multiple, INCL-plugin | nf-1000 |
| Params block | PARAM-typed-block | nf-1000 |
| Parameter (legacy) | PARAM-assign, PARAM-block | nf-1000 |
| Workflow | WF-entry, WF-named, WF-take, WF-main, WF-emit, WF-publish | nf-2000 |
| Process | PSEC-input, PSEC-output, PSEC-script, PSEC-exec, PSEC-stub, PSEC-when | nf-1100 |
| Process (typed) | PSEC-typed-input, PSEC-stage, PSEC-typed-output, PSEC-topic | nf-1600 |
| Function | SYN-function | nf-0100 |
| Enum type | SYN-enum | nf-1000 |
| Record type | SYN-record | nf-1000 |
| Output block | WF-output-block | nf-2000 |

## syntax.html — Statements

| Heading | Feature ID(s) | nf- file |
|---------|--------------|----------|
| Variable declaration | SYN-var-def, SYN-multi-assign, SYN-scoping | nf-0100, nf-0300 |
| Assignment | STMT-assign | nf-0300 |
| Expression statement | STMT-expr-statement | nf-0300 |
| assert | STMT-assert | nf-0300 |
| if/else | STMT-if | nf-0300 |
| return | STMT-return | nf-0300 |
| throw | STMT-throw | nf-0300 |
| try/catch | STMT-try | nf-0300 |

## syntax.html — Expressions

| Heading | Feature ID(s) | nf- file |
|---------|--------------|----------|
| Variable | SYN-var-def | nf-0100 |
| Number | SYN-int, SYN-float | nf-0200 |
| Boolean | SYN-bool | nf-0200 |
| Null | SYN-null | nf-0200 |
| String | SYN-string-lit, SYN-slashy, SYN-multiline | nf-0100, nf-0200 |
| Dynamic string | SYN-gstring | nf-0100 |
| List | SYN-list | nf-0200 |
| Map | SYN-map | nf-0200 |
| Closure | SYN-closure | nf-0100 |
| Index expression | SYN-index-expr | nf-0100 |
| Property expression | SYN-property-expr, SYN-null-safe, SYN-spread | nf-0100, nf-0200 |
| Function call | SYN-named-args, SYN-function | nf-0100 |
| Constructor call | SYN-constructor | nf-0100 |
| Unary expressions | SYN-logical (!) , SYN-bitwise (~), SYN-arith (+/-) | nf-0200 |
| Binary expressions | SYN-arith, SYN-compare, SYN-logical, SYN-bitwise, SYN-membership, SYN-instanceof, SYN-regex-find, SYN-regex-match | nf-0200 |
| Ternary expression | SYN-ternary | nf-0200 |
| Parentheses | — (implicit grouping) | — |
| Precedence | SYN-precedence | nf-0200 |

## syntax.html — Deprecations

| Heading | Feature ID(s) | nf- file |
|---------|--------------|----------|
| addParams/params on includes | INCL-addParams | nf-1000 |
| when: section | PSEC-when | nf-1100 |
| shell: section | PSEC-shell | nf-1100 |

## syntax.html — Non-strict (not in strict reference, but in implementation)

| Heading | Feature ID(s) | nf- file |
|---------|--------------|----------|
| for loops | STMT-for | nf-0300 |
| while loops | STMT-while | nf-0300 |
| switch/case | STMT-switch | nf-0300 |
| break | STMT-break | nf-0300 |
| continue | STMT-continue | nf-0300 |
| finally (in try) | STMT-try (finally part) | nf-0300 |

## stdlib-types.html

| Heading | Feature ID(s) | nf- file |
|---------|--------------|----------|
| Bag\<E\> | TYPE-bag | nf-0500 |
| Boolean | SYN-bool | nf-0200 |
| Channel\<E\> | TYPE-channel | nf-0500 |
| Duration | TYPE-duration-literal, TYPE-duration-cast, TYPE-duration-arithmetic, TYPE-duration-methods | nf-0500 |
| Float | SYN-float | nf-0200 |
| Integer | SYN-int | nf-0200 |
| Iterable\<E\> | TYPE-iterable | nf-0500 |
| List\<E\> | METH-list-* | nf-0400 |
| Map\<K,V\> | METH-map-* | nf-0400 |
| MemoryUnit | TYPE-memunit-literal, TYPE-memunit-cast, TYPE-memunit-arithmetic, TYPE-memunit-methods | nf-0500 |
| Path | TYPE-path-* | nf-0500 |
| Record | TYPE-record | nf-0500 |
| Set\<E\> | TYPE-set | nf-0500 |
| String | METH-str-* | nf-0400 |
| Tuple | TYPE-tuple | nf-0500 |
| Value\<V\> | TYPE-value-channel | nf-0500 |
| VersionNumber | TYPE-version-number | nf-0500 |

## process.html — Task properties

| Heading | Feature ID(s) | nf- file |
|---------|--------------|----------|
| task.attempt | BV-task-attempt | nf-4500 |
| task.exitStatus | BV-task-exitStatus | nf-4500 |
| task.hash | BV-task-hash | nf-4500 |
| task.index | BV-task-index | nf-4500 |
| task.name | BV-task-name | nf-4500 |
| task.previousException | BV-task-previousException | nf-4500 |
| task.previousTrace | BV-task-previousTrace | nf-4500 |
| task.process | BV-task-process | nf-4500 |
| task.workDir | BV-task-workDir | nf-4500 |

## process.html — Inputs and outputs (typed)

| Heading | Feature ID(s) | nf- file |
|---------|--------------|----------|
| Stage directives | PSEC-stage-env, PSEC-stage-stageAs, PSEC-stage-stdin | nf-1600 |
| Outputs | PSEC-typed-output-env, PSEC-typed-output-eval, PSEC-typed-output-file, PSEC-typed-output-files, PSEC-typed-output-stdout | nf-1600 |

## process.html — Inputs and outputs (legacy)

| Heading | Feature ID(s) | nf- file |
|---------|--------------|----------|
| val (input) | INP-val | nf-1200 |
| file (input, deprecated) | INP-file | nf-1200 |
| path (input) | INP-path | nf-1200 |
| env (input) | INP-env | nf-1200 |
| stdin (input) | INP-stdin | nf-1200 |
| tuple (input) | INP-tuple | nf-1200 |
| val (output) | OUT-val | nf-1300 |
| file (output, deprecated) | OUT-file | nf-1300 |
| path (output) | OUT-path | nf-1300 |
| env (output) | OUT-env | nf-1300 |
| stdout (output) | OUT-stdout | nf-1300 |
| eval (output) | OUT-eval | nf-1300 |
| tuple (output) | OUT-tuple | nf-1300 |
| Generic options (emit, optional, topic) | OUT-emit, OUT-optional, OUT-topic | nf-1300 |

## process.html — Directives

| Heading | Feature ID(s) | nf- file |
|---------|--------------|----------|
| accelerator | DIR-accelerator | nf-1400 |
| afterScript | DIR-afterScript | nf-1400 |
| arch | DIR-arch | nf-1400 |
| array | DIR-array | nf-1400 |
| beforeScript | DIR-beforeScript | nf-1400 |
| cache | DIR-cache | nf-1400 |
| clusterOptions | DIR-clusterOptions | nf-1400 |
| conda | DIR-conda | nf-1401 |
| container | DIR-container | nf-1401 |
| containerOptions | DIR-containerOptions | nf-1401 |
| cpus | DIR-cpus | nf-1401 |
| debug | DIR-debug | nf-1401 |
| disk | DIR-disk | nf-1401 |
| echo (deprecated) | DIR-echo | nf-1401 |
| errorStrategy | DIR-errorStrategy | nf-1402 |
| executor | DIR-executor | nf-1402 |
| ext | DIR-ext | nf-1402 |
| fair | DIR-fair | nf-1402 |
| label | DIR-label | nf-1402 |
| machineType | DIR-machineType | nf-1402 |
| maxErrors | DIR-maxErrors | nf-1403 |
| maxForks | DIR-maxForks | nf-1403 |
| maxRetries | DIR-maxRetries | nf-1403 |
| maxSubmitAwait | DIR-maxSubmitAwait | nf-1403 |
| memory | DIR-memory | nf-1403 |
| module | DIR-module | nf-1403 |
| penv | DIR-penv | nf-1404 |
| pod | DIR-pod | nf-1404 |
| publishDir | DIR-publishDir | nf-1404 |
| queue | DIR-queue | nf-1404 |
| resourceLabels | DIR-resourceLabels | nf-1404 |
| resourceLimits | DIR-resourceLimits | nf-1404 |
| scratch | DIR-scratch | nf-1405 |
| secret | DIR-secret | nf-1405 |
| shell | DIR-shell | nf-1405 |
| spack | DIR-spack | nf-1405 |
| stageInMode | DIR-stageInMode | nf-1405 |
| stageOutMode | DIR-stageOutMode | nf-1405 |
| storeDir | DIR-storeDir | nf-1405 |
| tag | DIR-tag | nf-1405 |
| time | DIR-time | nf-1405 |

## operator.html

Covered by nf-3000 through nf-3500. See those files for individual
operator Feature IDs (CO-*).

## channel.html — Channel factories

| Heading | Feature ID(s) | nf- file |
|---------|--------------|----------|
| Channel.of | CF-of | nf-2500 |
| Channel.value | CF-value | nf-2500 |
| Channel.empty | CF-empty | nf-2500 |
| Channel.from (deprecated) | CF-from | nf-2500 |
| Channel.fromList | CF-fromList | nf-2500 |
| Channel.fromPath | CF-fromPath | nf-2500 |
| Channel.fromPath options | CF-fromPath | nf-2500 |
| Channel.fromFilePairs | CF-fromFilePairs | nf-2500 |
| Channel.fromSRA | CF-fromSRA | nf-2500 |
| Channel.watchPath | CF-watchPath | nf-2500 |
| Channel.interval | CF-interval | nf-2500 |
| Channel.topic | CF-topic | nf-2500 |
| Channel.fromLineage | CF-fromLineage | nf-2500 |
| channel.STOP | CF-STOP | nf-2500 |

## config.html

Covered by nf-4000. See that file for individual config Feature IDs (CFG-*).
