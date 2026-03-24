# Test: Channel Factories

**Spec files:** nf-2500-channel-factories.md
**Impl files:** impl-03-channel-factories.md

## Task

For each feature ID in nf-2500-channel-factories.md, determine its classification.

### Checklist

1. Read nf-2500-channel-factories.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- CF-empty, CF-from, CF-fromLineage, CF-fromList, CF-fromPath
  CF-fromFilePairs, CF-fromSRA, CF-interval, CF-of, CF-topic
  CF-value, CF-watchPath

### Output format

```
CF-empty: SUPPORTED | reason
...
```

## Results

- CF-empty: SUPPORTED — `resolveChannelFactoryItems` handles `empty` by requiring zero args and returning no items, which matches an empty channel.
- CF-from: SUPPORTED — `resolveChannelFactoryItems` maps deprecated `from` to `resolveChannelLiteralItems`, so each argument is emitted as a channel item with only a compatibility warning.
- CF-fromLineage: GAP — `resolveChannelFactoryItems` sends `fromLineage` to `warnUntranslatableChannelFactory` and returns `nil, nil`, so it parses but always resolves to an empty channel.
- CF-fromList: SUPPORTED — `resolveChannelFromListItems` evaluates the single list argument and emits each element as a separate channel item via `flattenChannelValues`.
- CF-fromPath: GAP — `resolveChannelPattern` only accepts one string argument, and `resolveChannelFactoryItems` uses `filepath.Glob` to emit cleaned string paths; Nextflow option maps and richer path semantics are not implemented.
- CF-fromFilePairs: GAP — `resolveChannelFactoryItems` accepts the factory, but `resolveFilePairs` returns only sorted file-path slices per group and omits the tuple key that Nextflow `fromFilePairs` emits; extra options are also not handled.
- CF-fromSRA: GAP — `resolveChannelFactoryItems` routes `fromSRA` to `warnUntranslatableChannelFactory` and returns an empty channel instead of resolving SRA inputs.
- CF-interval: GAP — `resolveChannelFactoryItems` routes `interval` to `warnUntranslatableChannelFactory` and returns an empty channel, so no timed emissions occur.
- CF-of: SUPPORTED — `resolveChannelFactoryItems` dispatches `of` to `resolveChannelLiteralItems`, which evaluates each argument and emits it as a channel item.
- CF-topic: GAP — `resolveChannelFactoryItems` routes `topic` to `warnUntranslatableChannelFactory` and returns an empty channel, so topic-based channel behaviour is absent.
- CF-value: SUPPORTED — `resolveChannelFactoryItems` evaluates the single argument for `value` and wraps it in one `channelItem`, matching a single-value channel.
- CF-watchPath: GAP — `resolveChannelFactoryItems` routes `watchPath` to `warnUntranslatableChannelFactory` and returns an empty channel, so filesystem watch events are never produced.
