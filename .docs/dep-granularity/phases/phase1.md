# Phase 1: The `queue` package (section B)

Ref: [spec.md](../spec.md) sections B1, B2, B3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

This phase depends on nothing and unblocks everything else. All three items are
pure `queue` package work in `queue/queue.go` and `queue/queue_test.go`, with no
`jobqueue` change, so it is reviewable in isolation. Items are sequential: B2's
acceptance tests call B1's `SatisfyDependency`, and B3 documents both.

## Proving tests RED

Two rules from the spec's Appendix ("Testing strategy") govern every item in
every phase of this delivery:

- A test naming a symbol this work introduces cannot be run at the pre-fix
  commit, because the tree would not compile there. Prove it RED **inside the
  fixed tree with the behaviour withheld**, then restore the behaviour. All 5
  B1 tests are in this class, as are B2 tests 2 and 3. B2 test 4 names no new
  symbol, but it shares `queue_test.go` with tests that do, so it is proved the
  same way.
- A test the spec labels a regression guard has no pre-fix failure to find. Do
  not hunt for one. Demand a genuine failure for every test that is not so
  labelled. B3 test 2 is labelled a guard, and B2 test 1 is an existing test
  that must stay green unchanged.

## Items

### Item 1.1: B1 - Satisfy a dependency key that has no backing item

spec.md section: B1

Add `Queue.SatisfyDependency(ctx context.Context, key string) error` to
`queue/queue.go`. It is `Remove`'s dependant half and nothing more: take
`queue.mutex`, return an `Error` whose `Err` is `ErrQueueClosed` if the queue is
closed, call `promoteDependants(key)` (`queue/queue.go:1664`), call
`changed(SubQueueDependent, SubQueueReady, ...)` if anything was promoted,
unlock, then `readyAdded(ctx, "dependent")`. Unlike `Remove`, `key` need not
name an item in the queue, and the call returns nil when the key has no
dependants.

Tests in `queue/queue_test.go`, covering all 5 B1 acceptance tests. Test 4's
closed-queue assertion has a house helper, `shouldBeQueueError`
(`queue_test.go:1888`), which the destroyed-queue block at `:707-755` already
uses for `Remove`, `Kick` and nine others. Test 5 is the idempotence one: a
second `SatisfyDependency` on the same key returns nil and the changed callback
records **one** dependent -> ready transition in total.

**Spec reading (parked ambiguity 1), resolved.** The Appendix's "avoids one
queue-mutex acquisition **per waiter** when a group clears" and the Locking
section's "~19k separate `promoteDependants` passes over 150k waiters each"
count different baselines, and neither is a typo. Today's cost is per
**member**: `Queue.Remove` takes `queue.mutex` once per call
(`queue/queue.go:1616`) and `promoteDependants` then walks that key's whole
waiter set inside that one acquisition (`:1664-1684`), so clearing a group
costs one acquisition per member that leaves, each walking every waiter - 19k
members at prod's shape, 150k waiters. Per **waiter** is the counterfactual for
the new shape: a `depgroup:` key has no backing item to `Remove`, so without
`SatisfyDependency` the only route through the existing API is one `Update` per
waiter, at one acquisition each. Read the Locking section for the baseline
figure. No acceptance test in this delivery depends on either count.

- [x] implemented
- [x] reviewed

### Item 1.2: B2 - The two existence proxies stop asking for a backing item

spec.md section: B2

Two sites test `queue.items[dep]`, and both break for a `depgroup:` key that
never has a backing item:

- `itemHasDeps` (`queue/queue.go:802`, sole caller `Kick` at `:1599`) becomes
  `len(item.UnresolvedDependencies()) > 0`. Without it, a kicked buried job with
  an unsatisfied group dependency goes straight to ready. B2 spells out why the
  path is reachable, and notes that the change also makes `Kick` agree with
  `kickJobs`' `readyCallbackExpected` (`server.go:5721`), which already uses
  `UnresolvedDependencies()`.
- `pruneDependants` (`queue/queue.go:1126`, sole caller `updateDependencies` at
  `:1075`) drops its `queue.items[dep]` guard, so a dropped group dependency's
  waiter entry is actually pruned. Those entries leak today for any
  non-existent parent key.

`resumeSuspendedItem` (`queue/queue.go:236`) already tests
`UnresolvedDependencies()` and must not be touched. The spec states these two
are the only such proxies in the package.

Tests in `queue/queue_test.go`, covering all 4 B2 acceptance tests:

- Test 1 is `queue_test.go:1478-1501` inside `TestQueue` ("You can update
  dependencies") and must stay green **unchanged**.
- Test 2 carries the pre-change difference: pre-change the kicked item reaches
  `ItemStateReady`; after the change it is `ItemStateDependent` with
  `Stats().Dependant == 1`, and `SatisfyDependency` then makes it ready.
- Test 3 asserts the **dependants map** (`HasDependents`), and asserts it
  before satisfying anything. B2 explains why an item-state assertion there
  cannot fail pre-change (`Update` has already rebuilt `remainingDeps`, so the
  stale entry's `resolveDependency` deletes nothing) and why a `HasDependents`
  check taken after a `SatisfyDependency` would pass pre-change too
  (`promoteDependants` deletes the whole `dependants[key]` entry,
  `queue/queue.go:1684`). Do not weaken it to either shape.
- Test 4: after an `Update` to no dependencies, `HasDependents("depgroup:g1")`
  is false.

- [x] implemented
- [x] reviewed

### Item 1.3: B3 - Package documentation

spec.md section: B3

Amend `queue/queue.go:44-47` and the `Add` doc at `:616-619`: a dependency
clears when an item with that key is `Remove()`d **or** when the key is passed
to `SatisfyDependency()`, and a dependency key need not ever name an item.
`Queue.ChangeKey` stays an O(all items) walk per rename, unchanged by this work;
`Item.ChangedKey` gets cheaper only because essence edges are now the only job
keys and group names are never renamed.

Covers both B3 acceptance tests. Test 1 is a lint and doc-comment check -
`go vet ./queue/` and `make lint` clean, and the amended doc mentions
`SatisfyDependency` - not a Go test. Test 2 is a regression guard that already
holds pre-change: with an item depending on `depgroup:g1` and an item keyed
`k1`, `ChangeKey("k1", "k2")` leaves the dependent's `UnresolvedDependencies()`
as `[]string{"depgroup:g1"}`, because `Item.ChangedKey`
(`queue/item.go:229-249`) rewrites a dependency only when it equals `old`. The
guard is against an implementation that starts rewriting dependency keys
wholesale.

- [x] implemented
- [x] reviewed

## Phase gate

- `go vet ./queue/` clean and `make lint` at 0 issues.
- F4 acceptance test 1: `TestQueue` (including `queue_test.go:1478-1501`) and
  the rest of `queue/queue_test.go` pass, `-race` included.
- This phase leaves the whole tree green. The mid-delivery window in which the
  `jobqueue` suite is not green opens in phase 2 and closes in phase 4; see
  those files.


## Phase 1 outcome (2026-08-26)

Implemented and reviewed; `make lint` 0 issues, `go vet` clean, `queue` package
green including `-race` x5, and `make test` **424 passed - 9 skipped - 29
packages** (baseline 421 plus the three new test functions).

The reviewer mutation-proved the three guarantees that matter: restoring
`lockExistingItem` breaks four of the five B1 tests (so "a dependency key need not
name an item" is pinned), restoring `itemHasDeps`' existence proxy breaks B2 test
2 with exactly the failure B2 predicts, and restoring `pruneDependants`' guard
breaks B2 tests 3 and 4 independently. B2 test 1 is provably untouched: five diff
hunks, all pure insertions, zero deleted lines.

Two judgement calls upheld. `cleanorder -min-diff` puts `SatisfyDependency` at
`queue.go:310`, an odd home for an exported method, but the readable alternative
reshuffles 278 lines of unrelated declarations - the odd placement is the price of
`-min-diff`. And `itemHasDeps` keeping a receiver it no longer uses is *middle man*
under code-smells, but both the spec and the phase file name that function as the
site that changes, so inlining it would be a divergence.

### Follow-ups recorded, not done

- **`delete(queue.dependants, key)` (`queue/queue.go:1719`) is untested by
  anything.** Deleting that line leaves the whole `queue` package green. It is
  what stops a satisfied group key's waiter entry lingering in `queue.dependants`.
  Pre-existing and untouched by this phase; B1 test 5 is the natural home, and
  closing it costs three lines (`HasDependents(depG1)` false after the two
  satisfies).
- `promoteDependants`' `dep.state == ItemStateDependent` check is redundant with
  `Item.resolveDependency`'s own state guard (`queue/item.go:277`).
- The destroyed-queue enumeration remains non-exhaustive: `AddMany`, `ChangeKey`,
  `Suspend`, `Resume` and `HasDependents` all return `ErrQueueClosed` and are not
  asserted there. Pre-existing.
- `TestQueueChangeKeyLeavesItemlessDependency` keys its dependant `key2`
  (`"key_2"`) while the rename target is `keyG2` (`"key2"`) - no collision, easy to
  misread.
