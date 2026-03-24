# Test: Groovy & Java Imports

**Spec files:** nf-0450-groovy-imports.md
**Impl files:** impl-08-groovy.md

## Task

For each feature ID in nf-0450-groovy-imports.md, determine its classification.

### Checklist

1. Read nf-0450-groovy-imports.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- METH-groovy-lang, METH-groovy-util, METH-java-io, METH-java-lang, METH-java-math-BigDecimal
  METH-java-math-BigInteger, METH-java-net, METH-java-util

### Output format

```
METH-groovy-lang: SUPPORTED | reason
...
```

## Results

- METH-groovy-lang: UNSUPPORTED — `evalStaticMethodCall` only handles `Integer.parseInt` and `evalNewExpr` only recognizes `File`, `Path`, `URL`, `Date`, `BigDecimal`, `BigInteger`, `ArrayList`, `HashMap`, `LinkedHashMap`, and `Random`; there is no `groovy.lang.*` class support, which would require Groovy runtime types beyond this evaluator.
- METH-groovy-util: UNSUPPORTED — there is no `groovy.util.*` handling in `evalStaticMethodCall` or `evalNewExpr` in `nextflowdsl/groovy.go`, so these classes are not modelled by wr's evaluator.
- METH-java-io: GAP — `evalNewExpr` supports `File`/`Path` via `evalPathConstructor`, but it reduces them to cleaned path strings rather than Java objects, and no other `java.io.*` classes are implemented.
- METH-java-lang: GAP — the evaluator supports a small subset (`Integer.parseInt` in `evalStaticMethodCall`, plus `Integer`/`String` handling in `matchesGroovyType` and `evalCastExpr`), but the rest of `java.lang.*` such as `Math` and `System` is not implemented.
- METH-java-math-BigDecimal: GAP — `evalNewExpr` recognizes `BigDecimal`, but `evalBigDecimalConstructor` converts the value with `strconv.ParseFloat`, so wr returns `float64` rather than Groovy/Java `BigDecimal` semantics.
- METH-java-math-BigInteger: GAP — `evalNewExpr` recognizes `BigInteger`, but `evalBigIntegerConstructor` converts the value with `strconv.ParseInt`, so wr returns `int64` rather than arbitrary-precision `BigInteger` semantics.
- METH-java-net: GAP — `evalNewExpr` recognizes `URL`, but routes it through `evalStringConstructor`, so wr treats it as a plain string and implements no other `java.net.*` types.
- METH-java-util: GAP — `evalNewExpr` supports only a subset (`Date`, `ArrayList`, `HashMap`, `LinkedHashMap`, `Random`), with `Random` stubbed by `evalRandomConstructor` to `int64(0)` and no broader `java.util.*` class or utility support.
