# Implementation: Channel Factories (channel.go)

**File:** `nextflowdsl/channel.go`

## Core

### resolveChannelFactoryItems (line 91)
Switch that dispatches `Channel.*` factory calls:

| Case | Line | Feature |
|---|---|---|
| of | 93 | CF-of |
| from | 95 | CF-from (deprecated) |
| fromList | 99 | CF-fromList |
| value | 101 | CF-value |
| empty | 112 | CF-empty |
| fromPath | 118 | CF-fromPath |
| fromFilePairs | 135 | CF-fromFilePairs |
| fromLineage, fromSRA, interval, topic, watchPath | 152 | CF-fromLineage, CF-fromSRA, CF-interval, CF-topic, CF-watchPath (warning only) |

## Helpers

### resolveChannelLiteralItems (line 274)
Evaluates literal args via `EvalExpr`, wraps in channelItem.
Used by: `of`, `from`.

### resolveChannelFromListItems (line 288)
Evaluates list arg, flattens into individual items.
Used by: `fromList`.

### resolveChannelPattern (line 169)
Evaluates first arg as string path pattern, resolves relative to cwd.
Used by: `fromPath`, `fromFilePairs`.

### resolveFilePairs (line 191)
Glob-matches files, groups by stem into `[key, [path1, path2]]` tuples.
Used by: `fromFilePairs`.

### expandBracePatterns (line 232)
Expands `{a,b}` brace patterns for glob matching.
Used by: `resolveFilePairs`.

### flattenChannelValues (line 308)
Recursively flattens nested lists.
Used by: `fromList` to expand list items.

### resolveChannelItems (line 1122)
Higher-level dispatcher for `ChanExpr` types:
- `ChannelFactory` → `resolveChannelFactoryItems`
- `ChanRef` → looked up in resolver
- `NamedChannelRef` → select named output from upstream
Applies operator chain if present.

### ResolveChannel (line 2347)
Public API — resolves `ChanExpr` to `[]any` values.
Used for standalone channel evaluation (e.g. tests).
