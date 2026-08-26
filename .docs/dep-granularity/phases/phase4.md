# Phase 4: The add, modify, remove and kick paths (section D)

Ref: [spec.md](../spec.md) sections D1, D2, D3, D4

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

Items are sequential. D1 and D2 share `jobqueue/depgranularity_add_test.go`, D3
and D4 share `jobqueue/depgranularity_remove_test.go`, and D1, D2 and D3 touch
`jobqueue/server.go` or its siblings, so none of them is safe to run in
parallel. The phase depends on phase 2 for `depGroupMembers` and
`dependencyKeys`, and on phase 1 for `SatisfyDependency`; it is independent of
phase 3 in code, but shares the membership hooks, so review it after phase 3.

Phase 3's file carries the full six-site membership hook set and which phase
installs each. This phase installs the four that remain: the add path's two
passes (D1), the two modify sites (D2) and the delete site (D3). The archive and
recovery hooks landed in phase 3; do not add them again. The two `Server`
helpers those hooks call, `replaceDepGroupMembership` (D1) and
`rekeyDepGroupMembership` (D2), are new here: write them to the Architecture's
"New symbols" contract, where `depGroupMembers`' `replace`/`rekey` returns the
groups left with no live member and the server then calls
`s.q.SatisfyDependency(ctx, depGroupDependencyKey(g))` for each, holding no
shard lock across that call (Locking).

Both new test files carry the go-conventions copyright header. `jobqueue` tests
use GoConvey `So()` assertions only and guard with
`if runnermode || servermode { return }`. Never put `So()` inside a loop of more
than 20 iterations; count and assert the final count - D1 test 1 walks 500
waiters, and D1 test 2 builds 200- and 2,000-member fixtures.

**The green window from phase 2 closes here.** Once D1, D2 and D3 have landed,
every path that moves a job into or out of the live bucket maintains
`s.depGroups`, and the `jobqueue` suite is expected green again. Run F4's sweep
at the end of this phase and record `uptime` beside the result.

## Proving tests RED

- D1 tests 1, 5 and 6 are the pre-fix failures, and all three name
  `s.depGroups`, so their RED run is inside the fixed tree with the behaviour
  withheld. D1's failing-pre-fix evidence is test 1's dependency-key count: 500
  against 100,000.
- D1 tests 2, 3 and 4 are regression guards that already hold pre-change, each
  labelled so in the spec with the reason. Do not hunt for a pre-fix failure
  behind them.
- D2 tests 2 and 3 are genuine pre-fix failures that name no new symbol, so both
  can run at the pre-fix commit. Test 3's waiter is wedged forever today; test
  2's W stays blocked on J1's and J2's keys once both are modified out of `G`,
  which is the deliberate change in **when** waiters are released.
- D2 tests 1, 4 and 5 all name `s.depGroups`, so their RED run is inside the
  fixed tree with the behaviour withheld. Test 1 pins that same release-timing
  change, which the pre-fix tree contradicts; it is not a guard either.
- D3's five tests are all regression guards against the pre-fix commit; D3 has
  no pre-fix failure of its own to prove. They are the gates that fail if the
  group edges land **without** the re-derived guard, which is the failure D3
  exists to catch. Tests 2 and 4 name new symbols, so the pre-fix tree does not
  compile with them: run those two inside the fixed tree with the re-derived
  guard withheld.
- D4's two tests fail at neither the pre-fix commit nor here; they are the gates
  that fail if the group edges land without B2's `itemHasDeps` change.

## Items

### Item 4.1: D1 - Adding to a dep group is linear in the waiters

spec.md section: D1

Update the group state for every job in `storeNewJobs`' `jobsToQueue` **before**
`itemDefsForNewJobs` resolves anything, in `createJobs` after `db.storeNewJobs`
returns (`server.go:5055-5059`), so that a member and a dependent added in the
same batch see each other exactly as they do today, where `storeNewJobs` writes
the index before resolution.

That update is **two passes**, and the order is load-bearing:
`registerDepGroupMembers(jobsToQueue)` records every job's declared groups
first, then `replaceDepGroupMembership(ctx, job.Key(), job.DepGroups)` per job
applies the drops and satisfies the groups left empty. One `replace` pass would
release G's waiters early whenever a batch both drops G from one job and adds
another member of G: the drop lands first, G momentarily has no member, and its
waiters go ready ahead of the new member. After the register pass, no group
named anywhere in the batch is empty, so the drop pass can only empty groups the
batch no longer declares at all. `registerDepGroupMembers` is therefore shared
with recovery's bulk rebuild (phase 3) rather than being recovery-only; it only
adds, so it releases nothing on either path.

`itemDefsForNewJobs` (`server.go:5069`) and `gatherDependencyUpdates`
(`server.go:5220`) then resolve through `dependencyKeys`, which phase 2 already
swapped in at their call sites `:5082` and `:5225`.

The modify path needs no batch equivalent: one `JobModifier` applies the same
`DepGroups` to every job in the call (`applyGrouping`, `job.go:2016`), so no job
in a modify batch can leave a group that another job in the same batch joins.

Preserve `retrieveDependentJobs` byte-for-byte, including its all-time
transitive waiter scan and its resurrect-and-rerun of archived waiters; it is
O(all-time waiters), not O(waiters x members), so it is not the OOM, and it is
recorded as follow-up G1. `warnings.NeverSeenDepGroups` is still built from the
`waitingForDepGroups` of the originally input jobs and is unchanged.

Correct `updateJobDependencies`' stale doc comment to name its one production
caller, `queueNewJobItems` (`server.go:5111`), and the one source it is really
given, `storeNewJobs`' `jobsToUpdate`. The current comment names
`storeNewJobs()` and `db.modifyLiveJobs()`, but `modifyLiveJobs` discards
`prepareNewJobs`' `jobsToQueue`/`jobsToUpdate` (the `//nolint:dogsled` line), so
the modify path never refreshes a group's waiters through it.

File: `jobqueue/server.go`. Tests in `jobqueue/depgranularity_add_test.go`,
covering all 6 D1 acceptance tests. Carry these figures and reasons:

- Test 1: a live group `G` with 200 members and 500 live waiters; after one more
  `Client.Add` into `G`, every waiter's item has exactly one dependency,
  `"depgroup:G"`, the total dependency-key count across all waiters is **500**
  (pre-fix: 100,000) and `s.depGroups.memberships()` grew by exactly 1.
- Test 2 is a regression guard: the 2,000-member add's `db.bolt.Stats().TxN`
  delta is no more than **1.25x** the 200-member one.
  `retrieveIncompleteJobKeysByDepGroup` (`db.go:3036`) opens exactly one `View`
  per waiter whatever the membership, so the pre-fix ratio is already ~1.0 - the
  quadratic cost there is allocated strings, not transactions. Keep the guard
  against an implementation that reads per member.
- Test 5 pins a documented consequence, not a bug: re-adding a job with
  `--rerun` (`ignoreComplete == false`) declaring `DepGroups: []` instead of
  `["G"]` leaves `G` with no live member and releases the waiter **at add
  time**. D1 gives the reason (the old lookups are never deleted on that path,
  so a rebuild from the decoded record would release those waiters at the next
  restart regardless; matching it at add time keeps the running manager and its
  own restart in agreement).
- Test 6 is the two-pass ordering test: one `Client.Add` carrying both J
  re-added with `--rerun` and no `DepGroups` and a new job declaring
  `DepGroups: ["G"]`, with **the drop listed first** - the order a single-pass
  `replace` gets wrong. W stays dependent, `hasMembers("G")` stays true with the
  new job as the only member, and W becomes ready only once that new job
  completes.

- [ ] implemented
- [ ] reviewed

### Item 4.2: D2 - Modify maintains the group sets

spec.md section: D2

Hook both modify call sites with `rekeyDepGroupMembership(ctx, oldKey,
job.Key(), job.DepGroups)` per job, immediately after `db.modifyLiveJobs`
succeeds and **before** the `DependenciesSet || PrioritySet` guard, which a
`DepGroups`-only modification never passes. The Architecture's Membership hook
set gives the exact regions and the source of `oldKey` at each: in
`persistModifiedJobsToDB` (`serverCLI.go:1692`), between the `db.modifyLiveJobs`
success at `:1699-1703` and the guard at `:1705`, with `oldKey` the `oldKeys[i]`
that function already builds; and in `storeModifiedJobs` (`serverREST.go:2256`),
between the `db.modifyLiveJobs` call at `:2270` and the guard at `:2274`, with
`oldKey` the matching entry of `modifiedOldKeys(modified, jobs)`, which is
index-parallel with `jobs`. `modified` maps **new key -> old key** at both
sites, so `job.Key()` is already the new key.

The guarded halves - `reflectModifiedJobInQueue` (`serverCLI.go:1717`) and
`updateModifiedQueueJobs` (`serverREST.go:2327`) - are the wrong place: a
`DepGroups`-only modification never reaches them, and that is the case that must
not wedge a group's waiters.

`rekey`, not `replace`, for two reasons. A modify can change the key, and a
`replace` on the new key alone would wedge the group on a phantom old member
(test 4). And `rekey`'s ordering - new key recorded before old key dropped - is
what stops a group that survives the rename from transiently emptying and
releasing its waiters early. Dropping the old key is not optional: nothing else
removes it.

`wr mod --dep_grps` does not exist (the flag wiring is commented out in
`cmd/mod.go:172`, `JobModifyViaJSON` has no `dep_grps` field, and
`JobModifier.SetDepGroups`, `job.go:1753`, has no production caller), so the
path is reachable only by a Go consumer hand-building a `JobModifier`. Maintain
the group sets on it anyway and pin it through the Go client API. Re-enabling
the flag is out of scope (G4).

This changes **when** waiters are released, deliberately: today, removing job J
from group G leaves G's waiters blocked on J's key until J completes; under
group granularity, if removing J empties G then G's waiters are released at
modify time. The mirror case also holds - adding a member by modification
extends a currently-blocked waiter's wait.

Files: `jobqueue/serverCLI.go`, `jobqueue/serverREST.go`. Tests in
`jobqueue/depgranularity_add_test.go`, covering all 5 D2 acceptance tests. Test
4 is the one A1 test 7 points at: after a key-changing modify that keeps
`DepGroups: ["G"]`, `s.depGroups` lists the **new** key as G's member and not
the old one, `hasMembers("G") == true`, `memberships()` is 1 (no phantom), and W
is still dependent - neither released at modify time nor wedged, with completing
the modified J releasing it. Test 5 drives the REST path's `storeModifiedJobs`
in-package (the technique E6 uses for `getij`, so no HTTP harness is needed)
with a `JobModifier` that sets neither dependencies nor priority; it is the
assertion that the hook sits above the guard rather than inside it.

- [ ] implemented
- [ ] reviewed

### Item 4.3: D3 - `wr remove`'s dependant guard is re-derived

spec.md section: D3

`removeDeletableJobs` (`server.go:5557`) skips a job while
`s.q.HasDependents(jobKey)` is true, because `queue.Remove` satisfies
dependants. With group edges a member's key is no longer in `dependants`, so the
guard must **also** ask `s.q.HasDependents(depGroupDependencyKey(g))` for each
`g` in `job.DepGroups`, in addition to the existing check on the job key, which
still covers essence dependants. Everything else in `removeDeletableJobs` and in
`deleteJobs`' skip-and-walk-the-tree loop is unchanged.

This item also installs the delete membership hook: `releaseDepGroupMembership`
per deleted key in `finalizeDeletedJobs` (`server.go:5613`; the Architecture's
`:5615` is the `db.deleteLiveJobs` line inside it) after `db.deleteLiveJobs`,
which test 2 asserts through `s.depGroups.hasMembers("G") == false`.

This behaviour is unpinned at the `jobqueue` level today: `queue_test.go` pins
`Queue.HasDependents` itself, which does not change, but no jobqueue, REST or
CLI test deletes a dep-group parent and asserts it is skipped, or deletes parent
and child together and asserts both go.

File: `jobqueue/server.go`. Tests in `jobqueue/depgranularity_remove_test.go`,
covering all 5 D3 acceptance tests: the parent-only delete is skipped and the
child stays dependent; both go when listed together in either order, leaving
`s.q.Stats().Items == 0` (with the parent listed first, the skip-and-walk loop
retries the skipped parent); a parent with no waiters at all deletes cleanly and
`SatisfyDependency("depgroup:G")` with no dependants causes no error; and one of
two members deleted alone is still skipped.

- [ ] implemented
- [ ] reviewed

### Item 4.4: D4 - `Kick` reports the sub-queue it landed in

spec.md section: D4

No production change: B2 (phase 1) carries it. `kickJobs` (`server.go:5712`)
sets `State = JobStateReady` whenever `q.Kick` succeeds, including when `Kick`
routed the item to the dependent sub-queue, and the state a client sees comes
from `itemToJob` deriving it from the sub-queue. So the pinning test must assert
the **sub-queue**, not `job.State` immediately after the kick.

Tests in `jobqueue/depgranularity_remove_test.go`, covering both D4 acceptance
tests: a buried job whose dep group has no live member (so it has no
dependencies) kicks to ready and is reservable; and a buried job depending on
group `G`, re-blocked by a new member of `G` added afterwards while
`detachForDependentMove` leaves it buried, kicks into `ItemStateDependent` and
`s.q.Stats().Dependant`, then becomes ready when the new member completes.
Neither fails at the pre-fix commit - test 1's job has no dependencies at all,
and test 2's pre-change dependency is a member job key that does have a backing
item.

- [ ] implemented
- [ ] reviewed

## Spec reading (parked ambiguity 3), resolved

The Architecture's closing line, "The four production dependency-recompute call
sites are `server.go:5082`, `server.go:5225`, `serverCLI.go:1718` and
`serverREST.go:2329`; the two membership hook sites above are distinct from
them, and sit earlier in the same functions' call chains", undercounts. Verified
at `8b9ba00`, **three** of the six membership hook sites sit earlier in those
same call chains: the add hook in `createJobs`, which precedes both `:5082`
(inside `itemDefsForNewJobs`) and `:5225` (inside `gatherDependencyUpdates`, via
`queueNewJobItems` -> `updateJobDependencies`); the CLI modify hook in
`persistModifiedJobsToDB`, which precedes that function's call to
`reflectModifiedJobInQueue` at `:1712` and so the recompute at `:1718`; and the
REST modify hook in `storeModifiedJobs`, which precedes that function's call to
`updateModifiedQueueJobs` at `:2275` and so the recompute at `:2329`. Archive
and delete are in none of the four chains. Recovery's rebuild is in none of them
either, but it does precede a fifth recompute the closing line does not count,
`dependency.go:137` inside `resolveDependencyChunk`, which is phase 3's business
(C1). Read "the two" as the two **modify** hook sites, which is where confusion
with the guarded halves is a live hazard; the add hook is the third and is
equally distinct.

## Phase gate

- `go vet ./jobqueue/` clean; `make lint` at 0 issues.
- This phase's acceptance tests pass, plus phases 1-3's gates.
- F4's sweep: the `jobqueue` suite is green again after this phase. Record
  `uptime` beside every result, and never accept a "known flake" attribution
  without a load-matched A/B against a pristine worktree.
