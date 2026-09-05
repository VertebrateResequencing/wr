# Phase 3: Recovery (section C)

Ref: [spec.md](../spec.md) sections C1, C2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

Items are sequential: C2 rewrites the committed transaction-cost tests onto the
API C1 finishes. The phase depends on phase 2 for `depGroupMembers` and
`dependencyKeys`, and on phase 1 for `SatisfyDependency`, which the archive hook
in item 3.1 reaches through `releaseDepGroupMembership`.

The new test file carries the go-conventions copyright header. `jobqueue` tests
use GoConvey `So()` assertions only and guard with
`if runnermode || servermode { return }`. Never put `So()` inside a loop of more
than 20 iterations; count and assert the final count - C1 tests 1, 6 and 8
assert over 500, 3,000 and 500 jobs.

The green window from phase 2 is still open: after this phase recovery and
archive maintain the group state, but the add, modify and delete paths do not
until phase 4.

## Proving tests RED

- C1 tests 1, 2 and 8 fail pre-change - they assert the new key shape, the new
  membership count and the new shared-cache read count - and they name symbols
  this work introduces, so their RED run is inside the fixed tree with the
  behaviour withheld.
- C1 tests 3-7 are regression guards that already hold pre-change: 3, 4, 5 and 6
  pin the blocked/not-blocked partition, which the Architecture's resolution
  rule keeps identical under both models, and 7 is already pinned by `c96dcbf`.
  They are here because this story rewrites the code that produces all of it.
- C2 tests 3 and 4 fail pre-change (the 70-transaction shape and the new key
  assertions) and name symbols this work introduces, so their RED run is inside
  the fixed tree with the behaviour withheld, not at the pre-fix commit. Item
  2.2 leaves both their tests red at this phase's start: the assertions item 3.2
  replaces (`:326`, `:226`, `:273`) compile and fail against the new model.
- C2 tests 1, 2 and 5 are regression guards: their counts and state strings are
  unchanged, and the point of the story is that they survive the rewrite.

## Membership hook set, and where it lands

The Architecture's Membership hook set is six sites across five bullets. This
delivery installs them in two phases:

- **recovery rebuild** - `startPriorStateRecovery` (`server.go:1433`), after
  `db.recoverIncompleteJobs`: `registerDepGroupMembers(priorJobs)`. Phase 3,
  item 3.1 (C1).
- **archive** - `archiveCompletedJob` (`serverCLI.go:1379`), after `s.q.Remove`:
  `releaseDepGroupMembership(ctx, job.Key())`. Phase 3, item 3.1 (C1).
- **add / enqueue** - `createJobs` (`server.go:5055-5059`), in two passes over
  `jobsToQueue`: `registerDepGroupMembers(jobsToQueue)`, then
  `replaceDepGroupMembership(ctx, job.Key(), job.DepGroups)` per job. Phase 4,
  item 4.1 (D1).
- **delete** - `finalizeDeletedJobs` (`server.go:5613`; the Architecture's
  `:5615` is the `db.deleteLiveJobs` line inside it), after `db.deleteLiveJobs`:
  `releaseDepGroupMembership` per deleted key. Phase 4, item 4.3 (D3).
- **modify, CLI** - `persistModifiedJobsToDB` (`serverCLI.go:1692`), between the
  `db.modifyLiveJobs` success at `:1699-1703` and the `DependenciesSet ||
  PrioritySet` guard at `:1705`, with `oldKey` taken from the `oldKeys[i]` that
  function already builds: `rekeyDepGroupMembership(ctx, oldKey, job.Key(),
  job.DepGroups)`. Phase 4, item 4.2 (D2).
- **modify, REST** - `storeModifiedJobs` (`serverREST.go:2256`), between the
  `db.modifyLiveJobs` call at `:2270` and the same guard at `:2274`, with
  `oldKey` taken from `modifiedOldKeys(modified, jobs)`, which is
  index-parallel with `jobs`. Phase 4, item 4.2 (D2).

**Spec reading (parked ambiguity 5), resolved.** The Architecture's "Live-bucket
exit points are exactly three, so the places that must maintain per-group
membership are bounded to these five" states only half the derivation. Verified
at `8b9ba00`, the live bucket has exactly three exit points - `archiveJobTx`
(`db.go:1307`), `deleteLiveJobs` (`db.go:2535`) and `deleteOldLiveJobs`
(`db.go:3457`, called from `modifyLiveJobsTx`) - and exactly two entry points -
`storeNewJobData`'s `batchStore` into `bucketJobsLive` (`db.go:2225`) and
`modifyLiveJobsTx`'s `putEncodedJobs` (`db.go:3442`). Modify is one bullet
because it is both an exit and an entry, which is why its hook is `rekey`. That
gives four running-manager bullets - add, archive, delete, modify - and the
fifth, recovery's bulk rebuild, is not a live-bucket transition at all but the
startup rebuild of the same in-memory state from that bucket. Three exits plus
two entries plus the rebuild, folded into five bullets and six sites.

**Spec reading, flagged.** The Architecture lists the archive hook without
assigning it to a lettered story. C1 acceptance test 3 asserts that archiving
all M members releases all W waiters, and nothing else satisfies `depgroup:G`,
so the archive hook and `releaseDepGroupMembership` land here with C1. Phase 4
must not add them a second time.

## Items

### Item 3.1: C1 - Build per-group state from the decoded live jobs

spec.md section: C1

In `startPriorStateRecovery` (`server.go:1433`), after
`db.recoverIncompleteJobs` returns and before `setRecoveryTotal`, call
`s.registerDepGroupMembers(priorJobs)`. That pass reads only `job.DepGroups` on
jobs already decoded, so it needs no transaction of its own, and it **must not
depend on the live-bucket decode being a single transaction**:
`.docs/bugfixes/260825-3.md` item 3 proposes chunking that decode and is queued
separately, so it may land before or after these phases. Nothing is served and
there are no concurrent writers at that point, so either shape is safe here.

`db.resolveDependencies` keeps its **per-chunk** transaction property
(`dependencyResolutionChunkSize = 1000`, `dependency.go:69`) and its `ctx` check
per job. Do not restore a whole-pass transaction; C1 gives the reason (a read
transaction holds `mmaplock.RLock()` for its life, and a writer that must grow
the mapping stalls every write database-wide for the length of a 21-43 minute
recovery). `resolveDependencyChunk` gains the `*seenDepGroupCache` argument on
top of the `depGroupState` phase 2 gave it, and otherwise keeps its shape,
including that only database reads and `setWaitingForDepGroups` happen inside
the transaction.

`resolveDependencies` creates **one** `seenDepGroupCache` for the whole pass and
wraps each chunk's `txDepReader` in it, so the "ever seen" answer for a group is
read once however many jobs name it. Without it the check is per job (A2), so
150k live jobs naming one never-seen group would cost 150k gets, the bound the
Architecture states. The cache lives across chunks while the reader it wraps is
rebuilt per chunk; that is safe because `bucketDepGroups` is only ever added to
and nothing is served during recovery (E1), so no group can become seen
mid-pass. It counts the reads it makes into `db.depGroupSeenGets`, an
`atomic.Uint64` field on the `db`; `resolveDependencies` hands that counter to
`newSeenDepGroupCache`. It is inert observability in the house style of
`db.archivedDecodes` (`db.go:682`), and gives the test a before/after delta
exactly as `db.bolt.Stats().TxN` does.

Unchanged, and not to be "fixed" by a recovery test: the single-batch enqueue
(`recoverPriorJobs` -> `enqueueItems`), the `recoveryPauseHook` seam, the
recovery `ctx` cancellation, and `AddMany`'s handling of `StartQueue` for a
deps-bearing item (`queue/queue.go:890-907`), under which a recovered running or
buried job whose group has a live member lands in the dependent sub-queue while
a recovered suspended one stays suspended with its deps set.

Files: `jobqueue/server.go`, `jobqueue/dependency.go`, `jobqueue/serverCLI.go`
(the archive hook). Tests in `jobqueue/depgranularity_recovery_test.go`,
covering all 8 C1 acceptance tests. Carry these figures:

- Test 1: M = 200 members, W = 500 waiters; every waiter's item has exactly one
  dependency, `"depgroup:G"`, and `sum(len(item.Dependencies()))` over the
  waiters is `W`, not `W*M`.
- Test 2: `s.depGroups.memberships()` equals the fixture's total (group, member)
  pair count, independent of W.
- Test 6: 3,000 live jobs where every job both belongs to and depends on a chain
  of dep groups; per-state counts equal the pre-restart counts exactly, with no
  lost or duplicated keys, `-race` clean.
- Test 7: `dependencyResolutionChunkSize` lowered to 10 with 100 recovered jobs,
  `db.bolt.Stats().TxN` grows by **exactly 10** - one per chunk, not 1 and not
  100.
- Test 8: 500 recovered jobs all depending on the same never-seen group `N`,
  chunk size lowered to 10 so the pass spans 50 chunks; `db.depGroupSeenGets`
  grows by **exactly 1**, all 500 jobs get `deps == []string{"depgroup:N"}` and
  `WaitingForDepGroups == []string{"N"}`. Without the shared cache the delta is
  **500**; without the pass-long lifetime it is **50**. Those three numbers are
  what make the test discriminate.

- [x] implemented
- [x] reviewed

### Item 3.2: C2 - Rewrite the committed transaction-cost tests

spec.md section: C2

Test-only, in `jobqueue/reliable4_dependency_tx_test.go`. Rewrite the three
tests that resolve dependencies - `TestReliable4DependencyFreeTxCost` (`:144`),
`TestReliable4DependencyResolutionUnchanged` (`:198`) and
`TestReliable4RecoveryDependencyPass` (`:291`). The fourth,
`TestReliable4RecoveryDependencyState` (`:521`), calls neither
`incompleteJobKeys` nor `resolveDependencies`, so it only has to keep passing.
None of the four is deleted and none has an assertion weakened.

Three things force the rewrite, and item 2.2 has already made all three, because
A2's deletions stop the package compiling without them: every
`Dependencies.incompleteJobKeys` call (`:165`, `:182`, `:215-277`, `:312`,
`:463`) becomes `dependencyKeys(reader, groups)`; every `db.resolveDependencies`
call (`:320`, `:336`, `:370`, `:397`, `:471`) gains the `depGroupState`
argument; and `newDepTxFixture` builds a bare `*db` with no `Server`, so it must
also build a `depGroupMembers` from its live jobs - `depTxLiveGroup` holding
`first` and `second` and nothing else, since `gone` was deleted from the live
bucket - and hand it to both sides. `depTxSoResolvesAsPerJob` (`:457-471`) is a
valid comparison only when both sides resolve against the same one.

This item owns the assertion work item 2.2 was told to leave alone.
`depTxFixture.liveGroupKeys` stops being an expected value and becomes the
fixture's source of that group's members, so its doc comment ("sorted as
`incompleteJobKeys` returns them") goes, along with its two member-key expansion
assertions (`:226`, `:273`).

One assertion is actively falsified and needs replacing, not rewording.
`So(perJobTx, ShouldBeGreaterThan, len(jobs))` (`:326`) says per-job resolution
costs more than one transaction per job. Under the new model, per-job resolution
of `depTxRecoveryJobs`' seven kinds costs 0 for no dependencies, 0 for a group
with live members (answered from `hasMembers`, with no seen check), and 1 each
for the other five - the seen check for a group with no live member, or the
`checkIfLive` for an essence dependency. Over `depTxResolutions = 100` jobs that
is **70 transactions**, so the old assertion reads `70 > 100` and fails. Replace
it with that shape stated directly, which is the property worth pinning: a
dep-group dependency whose group has a live member costs no database read at
all.

`TestReliable4RecoveryDependencyPass` keeps its per-chunk shape, including the
"exactly one transaction per chunk" form. "Exactly one for the whole pass" was
rejected in review and must not return.
`TestReliable4RecoveryDependencyState`'s three state strings (`ready waiting:`,
`dependent waiting:`, `dependent waiting:reliable4-deptx-e2e-future`) are
unchanged, because `WaitingForDepGroups` semantics are unchanged; C2 records why
both its `Connect` calls survive E1 as they stand.

Covers all 5 C2 acceptance tests, whose numbers are `passTx ==
depTxWantChunks(len(jobs))` at `depTxChunkSize` with `wantChunks > 1` and
`wantChunks < len(jobs)`, `Stats().OpenTxN == 0` after a cancelled pass, `TxN`
growing by 0 over 100 no-dependency resolutions and by exactly 100 over 100
essence-only ones, the 70 above, and `recoveryTx < len(jobs)`.

- [x] implemented
- [x] reviewed

## Phase gate

- `go vet ./jobqueue/` clean; `make lint` at 0 issues.
- This phase's acceptance tests pass, plus C2's four named tests and phases 1
  and 2's gates.
- Add-path, modify-path and delete-path dependency tests stay red until phase 4;
  see phase 2's "green window" note.


## Phase 3 outcome (2026-08-26)

Implemented and reviewed PASS, no blocking findings. `make lint` 0 issues, `go vet
-tags netgo` clean, `-race` clean on all nine new/reworked tests including the
3,000-job chain. Full suite with the eleven green-window reds skipped: **422
passed - 20 skipped - 29 packages**, reconciling three independent ways (415 from
phase 2, plus the two C2 reds now green, plus five new tests; 22 - 2 = 20 skips;
and the per-package skip breakdown). Skips removed afterwards, tree byte-identical.

### C2: the consolidation went the safe direction

The review brief framed this backwards and the reviewer caught it. **Nothing was
removed from `TestReliable4DependencyResolutionUnchanged`** - Convey inventories
are unchanged for all four spec-named tests (2/7/6/2). All six removals came out
of *phase 2's* `TestDepGranularityDependencyKeys` (8 Conveys -> 2), so the
duplicate copy was deleted and the spec-named test kept its assertions.

Checked property by property, two of the survivors are *stronger* than what they
replaced: the essence-on-dead case now asserts `waiting` as well as `keys`, and
`perJobTx` asserts `== 70` where the old test only asserted `> 100`. The 70 is
derivable rather than magic - 100 jobs over 7 dependency kinds gives residues 0-1
fifteen times each (30 read-free) and residues 2-6 fourteen times each (70
reading). The two Conveys kept in the phase 2 test are exactly the non-duplicates.

### The moved failure was real C1 progress, not a defect in disguise

`TestReliable4RecoveryDependencyState`'s failure moved from `:711`/`:730` to
`:693`. Verified by stashing the change and re-running at `3390f23`: there, `:706`
(`states == beforeRestart`) *passed* because both sides were equally wrong. Now
`beforeRestart` says children are "ready" - wrong, captured through the un-hooked
add path - while the actual post-restart state says "dependent", which is
correct. The old `:730` half now passes. Dropping the archive hook makes that same
Convey fail at `:733`, which confirms the mechanism independently.

The other ten reds have byte-identical failing file:line:message at HEAD, so phase
3 changed none of them, and every one reduces to the D1 signature (an edge that
must block resolves as satisfied). Green shards re-run and confirmed: Execution a
and c, Modify b.

### `rc == ""`: verified in the code, and load-bearing

`jobqueueTestInit` sets no `RunnerCmd`; the only two dispatch sites are
`readyAddedCallback`'s `if rc != "" && !s.isRecovering()` (`server.go:4656`) and
`scheduleRunners`' early return (`:6427`). So nothing executes these jobs behind
the tests' backs, and no `Pause`/`Resume` is needed - pausing would in fact have
blocked the `Reserve` that C1 test 3 depends on. This matters more than it looks:
the chain-scale fixture's first server leaves ~3,000 wrongly-ready jobs, which a
runner command would have started running.

Consequence: the pre-existing comment in `TestReliable4RecoveryDependencyState`
about "a manager-spawned runner may win one first" is **verifiably wrong**; the
real reason `reserved == nil` is tolerated there is a Reserve timeout under load.

### The cache

Spec signatures unchanged. `cachedSeenDepReader` is an extra unlisted type,
introduced because `cleanorder` had no fixpoint while the two types named each
other; the memo is shared by reference and `resolveDependencies` builds exactly
one cache per pass, before the chunk loop. Single production caller, single
goroutine, so the unlocked memo is safe as documented.

Eight mutations all produced named failures. One refinement worth keeping: the
spec's "without the shared cache the delta is 500" is only observable if the
counting wrapper is retained - bypassing the wrapper gives 0, keeping it but never
consulting the memo gives 500. The `== 1` assertion falsifies all three, so it
discriminates as intended, just via the second route.

### Non-blocking, carried forward

- **`TestJobqueueProduction` failed once** at `jobqueue_test.go:7645`
  (`GetByEssence` nil after a DB-preserving restart) at load 116.5, then passed on
  re-run and 3/3 in isolation. Nameable mechanism: the test's `serve` helper does
  not wait for recovery, so a post-restart `GetByEssence` can race the recovery
  window - which is what E2's `Serving()` wait closes. **Phase 5/6 should confirm
  E2 covers it.**
- **Phase 4 should tighten C1 test 6 to the two-queue comparison** the acceptance
  text names, once D1 makes a pre-restart queue the right reference. Today it uses
  fixture-derived counts, because `bucketDepGroups` is written by
  `storeNewJobData`'s `batchStore` (`db.go:2222`), so a same-batch dependency on a
  live group resolves as satisfied at add time - the substitute still
  discriminates (dropping the recovery hook flips Ready 3 -> 3000).
- **The archive hook is skipped when `s.q.Remove` errors**, after the DB archive
  has committed, leaving a phantom member that keeps `hasMembers(G)` true and
  wedges G's waiters until a restart. Narrow, and phase 4's D3 delete hook covers
  the concurrent-delete route; the placement is what the spec dictates, so this is
  a note for phase 4's review.
- **The inline reason at `serverCLI.go:1390` is wrong**: it says the hook goes
  after `q.Remove` "because satisfying a group's key takes the queue mutex", but no
  queue mutex is held at either point. The real reasons are that the archive must
  have committed and the job must be out of the queue before its waiters are
  promoted.
- `startPriorStateRecovery`'s doc comment does not mention the new group-state
  rebuild; `dgrReserveWait` lacks the house "hang detector, not a latency budget"
  phrasing; `So(depTxReadingJobs+depTxReadFreeJobs, ShouldEqual, len(jobs))` is a
  constant identity no behaviour change can falsify; `dgrMissingItems`' doc
  overstates what the helper alone catches.
- Still open from phase 2's list, unassigned: narrowing `incompleteEssenceJobKeys`
  to `([]string, error)`, `dgRekeyDeadline`'s missing note, the `add(depGroups,
  "")` empty-key asymmetry, and `reliable4_dependency_tx_test.go:182`'s historical
  reference to the deleted `incompleteJobKeys` (left deliberately - it is a "used
  to" statement that still explains why the no-dependency guard exists).
