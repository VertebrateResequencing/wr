# Phase 2: Per-group state and resolution (section A)

Ref: [spec.md](../spec.md) sections A1, A2, A3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

Items are sequential. A1 and A2 must land together, because A2's resolution rule
needs A1's `hasMembers`; A3 follows, because its acceptance test 2 resolves live
jobs through A2's `dependencyKeys`. The phase depends on phase 1 for
`SatisfyDependency`, which the server-side membership helpers call in phases 3
and 4 rather than here.

New source files carry the go-conventions copyright header. `jobqueue` tests use
GoConvey `So()` assertions only and guard with
`if runnermode || servermode { return }`. Never put `So()` inside a loop of more
than 20 iterations; count and assert the final count.

## Proving tests RED

- Every A1 and A2 test names a symbol this work introduces, so the pre-fix tree
  does not compile with them. Prove each RED **inside the fixed tree with the
  behaviour withheld**, then restore the behaviour.
- A1 tests 10 and 12 fail by **hanging**, not by asserting, so they only report
  a named failure when written with the bounded harness below.
- A3 tests 1 and 5 name no new symbol, but items are sequential, so A1 and A2
  have landed before A3 starts and the pre-fix commit is out of reach. Prove
  both in the current tree with the `dgLookups` drop withheld.
- A3 tests 2 (in part), 3 and 4 are regression guards that already hold
  pre-change. Do not hunt for a pre-fix failure behind them.

## The green window opens here

Sections A through E are one delivery; these phases are review units inside it.
After this phase the tree compiles and phase 2's own acceptance tests pass, but
`s.depGroups` is present and **not yet maintained**: nothing registers a live
job as a member. Recovery and the add path therefore resolve a seen group with
no recorded member as satisfied, and dependency-bearing `jobqueue` tests will be
red until the hooks land - recovery and archive in phase 3, add, modify and
delete in phase 4. Two more are red for a different named reason: the assertion
half of C2's rewrite lands in phase 3, so
`TestReliable4DependencyResolutionUnchanged` and
`TestReliable4RecoveryDependencyPass` fail until it does (item 2.2). Attribute
every red test in that window to a named missing hook or to C2; anything else is
a real defect. F4 runs after every phase, but its `jobqueue` items 2 to 4 can
only be judged once the hooks land: run the full sweep after phase 4, and again
at the end of phase 6.

## Items

### Item 2.1: A1 - Per-group live-member sets

spec.md section: A1

New files `jobqueue/depgroups.go` and `jobqueue/depgroups_test.go`. Implement,
per the Architecture's "New symbols": `depGroupDependencyPrefix = "depgroup:"`,
`depGroupDependencyKey`, the `depGroupMembers` type with sharded group ->
members and job -> groups maps, `newDepGroupMembers`, `hasMembers`, `add`,
`remove`, `replace`, `rekey` and `memberships`. Every operation is idempotent.

Five locking rules, all load-bearing (Architecture, Locking):

- A job-key shard is always taken before a group-name shard, never the reverse.
  The shard locks are leaves relative to the existing `queue.mutex -> job ->
  statusState.mu` order, which is unchanged.
- Group-name shards are taken **one at a time and released before the next**,
  never two at once. Holding them all in `job.DepGroups` order deadlocks against
  a concurrent operation whose group list is ordered differently. No
  cross-group atomicity is needed, because each group's emptiness is
  independent of every other group's.
- `rekey` takes its two job-key shards in **ascending shard index**, and takes
  the shard once when both keys fall in it. Locking them in call order
  deadlocks two opposing rekeys (`a -> b` against `b -> a`).
- `rekey` gets per-group atomicity inside one acquisition: for each group both
  keys belong to, it adds `newKey` and drops `oldKey` under that group's single
  shard hold, so no other goroutine can observe the group between them. New
  before old, because an emptied group releases its waiters.
- No shard lock is held across a call into `queue`: `remove`, `replace` and
  `rekey` return the emptied group names and the caller takes the queue mutex
  once per emptied group.

`memberships()` is inert observability in the style of `db.archivedDecodes`
(`5c75a15`), for the F1 memory gate and a debug log line.

Covers all 12 A1 acceptance tests. Carry these figures and reasons exactly:

- Test 7's `rekey` ordering: an implementation that drops `old` first reports
  `g1` emptied here, and D2 acceptance test 4 (phase 4) shows what that costs at
  the server level.
- Test 10 is the only test that can hit a group-name shard deadlock: 8
  goroutines each adding and removing 500 distinct job keys across 50 groups for
  200 iterations, each `add` naming more than one group and the goroutines
  ordering their group lists differently - 8 x 500 x 200 add/remove pairs,
  roughly 1.6M sharded operations. Its deadline is
  `time.After(2 * time.Minute)`, sized against that workload (test 12's is 10 s
  over 4,000 rekeys) and against a shared node at load average 85-120 on 8 cores
  (F4 item 4). The deadline is a **hang detector, not a latency budget**: it
  costs nothing when the test passes, and if it ever fires spuriously the answer
  is a larger bound, not a load-matched A/B against a deadlock that is not
  there.
- Test 12: 4 goroutines running `rekey(ka, kb, ["g1"])` against 4 running
  `rekey(kb, ka, ["g1"])`, 500 iterations each, with `ka` and `kb` chosen to
  land in different job-key shards.
- Both harnesses have the same shape: close a channel from a goroutine that
  waits on the `sync.WaitGroup` the 8 goroutines report to, `select` between
  that channel and the deadline, and fail on the deadline branch **naming the
  deadlock**. Without it the failure is `go test`'s 10-minute package-wide
  panic. Both are `-race` clean, and both count failures and assert the final
  count.
- Test 11 pins `depGroupDependencyKey("g1") == "depgroup:g1"` and that a real
  job key - `(&Job{Cmd: "echo 1"}).Key()`, a 32-character hex string - does not
  carry the prefix. There is no reverse `depGroupFromDependencyKey`: the only
  caller the old `neverSeenDepGroupFromDependencyKey` had was
  `collectIncompleteJobKeys` (`dependency.go:423`), which A2 deletes, and D3
  needs only the forward direction.

**Spec reading (parked ambiguity 4), flagged not resolved.** The Architecture
says per-group membership is "~250k entries at prod's shape, roughly 1/6000th of
today's cost" while stating today's queue cost as "~2.9e9 map entries"; 250k
against 2.9e9 is about 1/11,600, and 1/6000 would need a ~1.5e9 baseline that
the spec does not state anywhere. The two cannot both be right against the same
baseline. Nothing here depends on it: the spec itself says memory is not the
deciding factor for sets-over-counters (the failure mode is), and no acceptance
test checks the ratio. What is checkable is the count itself, and F1 acceptance
test 3 checks it exactly against a fixture's own (group, member) pair total. Do
not carry either ratio into code comments.

- [ ] implemented
- [ ] reviewed

### Item 2.2: A2 - Dependencies resolve to group keys, not member keys

spec.md section: A2

Add `Dependencies.dependencyKeys(reader depReader, groups depGroupState)` and
the `depGroupState` interface in `jobqueue/dependency.go`, implementing the
Architecture's five-case resolution rule. Replace `incompleteJobKeys` at all
five call sites: `dependency.go:137`, `server.go:5082`, `server.go:5225`,
`serverCLI.go:1718`, `serverREST.go:2329`.

Delete `Dependencies.incompleteJobKeys` and the helpers only it uses -
`incompleteJobKeysByDependency`, `Dependency.incompleteJobKeys`,
`incompleteJobKeysWithSeen`, `Dependency.collectIncompleteJobKeys` and
`collectIncompleteJobKeys` - plus `incompleteDepGroupJobKeys`,
`neverSeenDepGroupDependencyPrefix`, `neverSeenDepGroupDependencyKey` and
`neverSeenDepGroupFromDependencyKey`. `incompleteEssenceJobKeys` stays;
`dependencyKeys` uses it for the essence half. `depReader` loses
`retrieveIncompleteJobKeysByDepGroup` and `txDepReader` the matching method,
which is what makes it impossible to keep calling the deleted function.
`db.retrieveIncompleteJobKeysByDepGroup` and
`retrieveIncompleteJobKeysByDepGroupTx` remain for `db_test.go`'s pre-upgrade
assertions and have no production caller.

`reader.depGroupsEverSeen` is called only for declared groups with no live
member, so a job whose groups all have live members opens no read transaction
for the seen check. The returned key slice is built **fresh per job**: no slice
may be shared between items, because `Item.Dependencies()` (`queue/item.go:206`)
returns the live backing slice and `Item.ChangedKey` (`:229`) mutates it in
place. `job.setWaitingForDepGroups` and the `sortedStringSet` ordering of both
returned slices are unchanged.

Tests in `jobqueue/reliable4_dependency_tx_test.go`, using A2's stated fixture
(group `L` with 2 live members; group `S` seen with its only member deleted from
the live bucket; group `N` never added; live job with cmd `C`; archived job with
cmd `D`; `depGroupMembers` populated from the fixture's live jobs). Covers all 8
A2 acceptance tests. Two carry figures worth keeping exact: test 7 grows a group
from 500 to 5,000 live members and `len(deps)` stays 1 both times, and test 8
asserts `db.bolt.Stats().TxN` is unchanged over 100 resolutions of a job with no
dependencies, which is the `c96dcbf` early return being preserved.

**Compile constraint (spec reading).** The call site outside the Membership hook
set's four, `dependency.go:137`, sits inside `resolveDependencyChunk`, so
replacing it forces the `groups depGroupState` parameter onto
`resolveDependencyChunk` and `resolveDependencies` and onto their one production
caller, `recoverPriorJobs` (`server.go:3941`, calling at `:3966`). That
signature change belongs to C1; land the parameter here because nothing compiles
without it, and leave C1 to add the `*seenDepGroupCache` argument, the
`db.depGroupSeenGets` counter and the `registerDepGroupMembers` call that fills
the state. For the same reason the `depGroups *depGroupMembers` field and its
`newDepGroupMembers()` initialisation in the `Server` literal
(`server.go:3524`) land here: the four `server.go`, `serverCLI.go` and
`serverREST.go` call sites need something to pass. The hooks that maintain the
field arrive in phases 3 and 4.

**Compile constraint (committed tests).** The deletions above also break this
item's own test file. `jobqueue/reliable4_dependency_tx_test.go` calls
`Dependencies.incompleteJobKeys` at `:165`, `:182`, `:215-277`, `:312` and
`:463`, names `neverSeenDepGroupDependencyKey` at `:244` and `:274`, and calls
`db.resolveDependencies` at `:320`, `:336`, `:370`, `:397` and `:471`, so no
test in the package compiles - phase 2's own acceptance tests included - until
those move onto `dependencyKeys(reader, groups)`, `depGroupDependencyKey` and
the new `resolveDependencies` signature, and `newDepTxFixture` builds a
`depGroupMembers` from its live jobs (`depTxLiveGroup` holding `first` and
`second` and nothing else, since `gone` was deleted from the live bucket). Land
exactly that mechanical half here, and beyond the two symbol swaps change no
assertion: C2 (item 3.2) owns `liveGroupKeys` becoming the fixture's source
rather than an expected value, the two member-key expansion assertions (`:226`,
`:273`) and the replacement for `So(perJobTx, ShouldBeGreaterThan, len(jobs))`
(`:326`). Those three compile and fail at run time, which is why
`TestReliable4DependencyResolutionUnchanged` and
`TestReliable4RecoveryDependencyPass` are red until phase 3.
`TestReliable4DependencyFreeTxCost` goes green here: its two counts hold
unchanged under the new model.

**Spec reading (parked ambiguity 2), resolved.** Both line numbers are right and
neither is a typo. At `8b9ba00`, `itemDefsForNewJobs` is declared at
`server.go:5069` and its `incompleteJobKeys` call is at `:5082`;
`gatherDependencyUpdates` is declared at `:5220` and its call is at `:5225`.
D1 names the two functions with `:5069` and `:5220`, so use those when naming
them, and `:5082` and `:5225` are correct wherever the spec labels them call
sites - which is what the Architecture item 3 bullet, this story's
five-call-site list, and the Membership hook set's four-call-site list all do.

- [ ] implemented
- [ ] reviewed

### Item 2.3: A3 - `bucketDTK` stops being written

spec.md section: A3

In `jobqueue/db.go`, drop `dgLookups` from `prepareNewJobs`' per-job
`job.DepGroups` loop, from `storeNewJobData`'s `batchStore` list, and from
`newJobLookups` and `putAllLookups`. Keep the bucket created by `initDB`, keep
it in `indexedLookupBuckets()` and `isIndexedLookupBucket`, and keep
`rebuildDepGroups` (`db.go:4298`) and its `openedExistingDB && !hadDepGroups`
trigger (`db.go:1021`) exactly as they are. The bucket is left in place,
unwritten: the Architecture gives three reasons why deleting it is the more
dangerous option - `rebuildDepGroups` builds `bucketDepGroups` **from**
`bucketDTK`, so an absent one is re-created empty and repairs nothing; an old
binary reading an emptied one treats every seen group as satisfied and runs
everything immediately, whereas a stale one loses only post-upgrade
memberships; and `deleteLookupEntriesForJobKey` returns `ErrBucketNotFound` on
the ~150k pre-upgrade jobs whose reverse entries name it. The rollback path
stays an operator step.

Two committed `jobqueue/db_test.go` tests are pinned to `bucketDTK` being
written and must be updated rather than deleted or weakened.

`TestDBReverseLookupIndex` (`db_test.go:292`) changes four assertions, only
three of which are counts:

- `:326` `parentLookups` 2 -> 1 (the parent's repgroup entry stays, its
  dep-group entry goes).
- `:327` `childLookups` **unchanged** at 2: the child's two entries are a
  repgroup entry and a reverse dep-group (`bucketRDTK`) entry, and `rdgLookups`
  keeps being written.
- `:364` **inverts** rather than dropping by one:
  `retrieveIncompleteJobKeysByDepGroup("new-parent-dg")` returns empty, so
  `ShouldContain, newParentKey` becomes `ShouldHaveLength, 0` beside the
  existing `oldDepKeys` assertion.
- `:369-370` `countLookupEntriesByJobKey` and
  `countReverseLookupEntriesByJobKey` for the new parent key both 2 -> 1.

`TestDBDepGroups`' second `Convey` (`db_test.go:478`) is reframed to write its
legacy `bucketDTK` entry directly -
`replaceLookupRebuildTestBucket(tx, bucketDTK, []byte("legacy"+dbDelimiter+
jobKey))`, the pattern `db_test.go:1391` and `:1480` already use - which is the
truer fixture, since those entries are pre-upgrade data by definition. Its
`depGroupEverSeen("legacy") == true` and `("absent") == false` assertions stay
exactly as they are. Its first `Convey`, on `prepareNewJobs`' `depGroupsSeen`,
is untouched: `depGroupsSeen` is not `dgLookups` and keeps being written.

Tests in `jobqueue/db_test.go` and `jobqueue/depgroups_test.go`, covering all 5
A3 acceptance tests. Labels: test 1 fails pre-change. Test 2's no-rebuild and
key-preservation halves hold identically pre-change - the rebuild trigger and
the bucket's contents are untouched by this work - so only its `dependencyKeys`
resolution call is new; it is asserted rather than assumed because it is the
story's whole upgrade claim. Tests 3 and 4 are regression guards that already
hold pre-change. Test 5 is `TestDBReverseLookupIndex`, updated.

- [ ] implemented
- [ ] reviewed

## Phase gate

- `go build ./...` and `go vet ./jobqueue/` clean; `make lint` at 0 issues.
- This phase's own acceptance tests pass, `-race` included for A1 tests 10 and
  12.
- The `queue` package stays green (phase 1's gate).
- Dependency-bearing `jobqueue` tests are expected red until phase 4, and C2's
  two rewritten tests until phase 3; see "The green window opens here" above.
