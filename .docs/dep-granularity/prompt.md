FEATURE: stop expanding dep-group dependencies into member job keys, so the
manager's memory is linear in the work rather than quadratic.

Spec location: `.docs/dep-granularity/spec.md`, with phases in
`.docs/dep-granularity/phases/` (matching the repo's `.docs/reliable/`,
`.docs/reliable2/`, `.docs/issue-197/` convention).

## Why (all measured on live production, 2026-08-24/25)

Full investigation: `.docs/reliable4/prod-restart-260825.md` - read it first,
especially Findings 6 and 7. Summary of the evidence:

- The production manager has been **OOM-killed four times** (kernel `dmesg`, UID
  mercury): 2026-08-24 15:54:06 at 180.5 GB anon-rss, 19:18:56 at 181.4 GB,
  20:15:59 at 181.4 GB, and 2026-08-25 10:06:51 at 174.8 GB, on a node with
  182.7 GB of RAM. A fifth kill followed at ~12:47:45 the same day.
- `go tool pprof -top -inuse_space` against the live manager, live heap 15.65 GB
  mid-recovery: **97.55%** of it hangs off `(*Server).recoveredItemDef` -
  `(*db).retrieveIncompleteJobKeysByDepGroup.func1` 64.50% and `sortedStringSet`
  32.49%. The 150,472 decoded jobs themselves are only **376 MB (2.40%)**. The
  memory is not the jobs, it is their dependency key lists.
- By the end of recovery the live heap reached **140 GB** with the GC's next goal
  at **218 GB** (above physical RAM, so the kill was arithmetically certain), and
  a single GC cycle cost 91 s of wall clock and 365 s of CPU. That works out to
  roughly 930 KB, or ~19,000 retained dependency keys, per job.
- Recovery of 150,472 jobs took **42m56s** (12:00:37 -> 12:43:33), the manager
  served for four minutes, then died. Prod currently cannot stay up.

## The mechanism to change

A user declares "job J depends on dep group G". The code expands that into "J
depends on member j1, j2, ... jk": `Dependency.incompleteDepGroupJobKeys`
(`jobqueue/dependency.go`) calls `db.retrieveIncompleteJobKeysByDepGroup`, which
cursor-scans `bucketDTK` (`depgroupToKey`) for G's prefix and returns **every**
incomplete member key, allocating a fresh string per member. Every job
referencing G therefore gets its **own private copy** of G's whole membership,
`sortedStringSet` copies each set again, and `recoverPriorJobs` retains all of it
(in the `*queue.ItemDef`s) for all jobs before enqueuing any. Cost: O(jobs x
group membership) in both bolt lookups and bytes.

## The design the operator wants (their words, and I think they are right)

"When dealing with the dependencies, why do we need copies of jobs at all? Don't
we just need a count or even a boolean that says we have dependencies, don't
start yet?"

The target shape, to be evaluated and pinned by the spec rather than assumed:

- Keep every dependency edge at **the granularity the user declared it**: a
  dep-group edge stays `job -> group`, never expanded to members. Essence
  (Cmd+Cwd) dependencies stay individual job edges - they are genuinely per-job
  identity and are not the quadratic part.
- **Per dep group:** a count of incomplete members, plus the set of jobs waiting
  on that group.
- **Per item:** the small set (or count) of its own unsatisfied declared
  dependencies - one entry per group it named, not per member.
- When a job completes, decrement its groups' incomplete counts; when a count
  reaches zero, release that group's waiters. This is Kahn's algorithm at group
  granularity, and it makes total state O(jobs + total group memberships), each
  membership stored once.
- Consequence worth designing for deliberately: recovery then needs **no per-job
  DB lookups at all** - a job's declared dependencies are already in its decoded
  record, and each group's incomplete count can be computed in a single pass over
  the live bucket.

## Constraints the spec must honour, and open questions it must settle

1. **A boolean is not sufficient** - dependencies are satisfied incrementally, so
   the design must know when the *last* one clears.
2. **Counts vs sets is a real tradeoff, not a detail.** A per-item integer is
   smallest but needs exactly-once decrements and is fragile under job re-add,
   removal and key rename; a set is idempotent but larger. The quadratic disaster
   came from sets-of-members, so the natural split is sets at *group* granularity
   with counts *within* a group - but the spec should decide this explicitly and
   say why.
3. **The never-seen dep group must still block.** A job depending on a group that
   has never existed currently blocks via a synthetic key
   (`neverSeenDepGroupDependencyKey` / `depGroupsEverSeen` / `bucketDepGroups`).
   At group granularity this becomes a property of the group; preserve the
   behaviour exactly.
4. **The in-place key rename must keep working.** `queue/item.go:239-241`
   rewrites `item.dependencies[i]` when a job's key changes. Group edges should
   make this simpler (no member keys to rewrite), but the spec must state what
   happens to it.
5. **Do not change user-visible behaviour**: what `wr status` reports about what a
   job is waiting for, `waitingForDepGroups`, `job.setWaitingForDepGroups`,
   dependency semantics (a group dependency is satisfied when all its incomplete
   members complete), and the REST/web contract. `.docs/issue-197/spec.md` is a
   binding written contract for the REST modification endpoints - check it before
   assuming any status code or field is free.
6. **Binding project rules**: repo-root `DEVELOPERS.md`, especially rule 2 (no new
   server-wide exclusive lock on the transition path) and rule 6 (no history scan
   on startup or on a control path). Also read `developers/wrdev.sh` before
   specifying anything that touches scheduling or startup.
7. **Recovery invariants to preserve**: the single-batch enqueue
   (`recoverPriorJobs` -> `enqueueItems`, which resolves dependencies within one
   batch), `s.bgCtx` cancellation, the `recoveryPauseHook` seam, and the
   recovery-window `ErrRecovering` contract from `.docs/reliable2/spec.md` section
   H2 and `.docs/reliable/spec.md` section B (non-blocking startup, B1/B2). Do not
   reintroduce a blocking start: B1 chose non-blocking deliberately to kill a
   190 s startup stall.
8. **Migration**: existing production DBs hold `depgroupToKey`, `depgroups` and
   `reverseDepgroupToKey` buckets with at least 250,000 memberships and 6,299
   distinct groups. Whatever representation is chosen must work against those on
   upgrade **without a startup history scan** (rule 6). Say how.
9. **A stopgap is landing first, independently**: a narrower fix that resolves each
   distinct dep group once per recovery and shares the resolved slice (with
   copy-on-write at the rename site). The spec should account for building on or
   replacing it, and should state whether the stopgap's per-item O(members)
   residual is acceptable in the interim.

## Acceptance evidence the spec should demand

Memory and lookup counts are the point, so acceptance tests should assert them,
not just correctness. Note this repo's established patterns: bbolt's own
`db.bolt.Stats().TxN` counts read transactions with no new instrumentation, and
inert test counters are an accepted convention (`db.archivedDecodes` from
5c75a15, `Job.derivations` from 8087866, `db.archiveTxObserver` from f7e36bc). A
scale gate belongs in `developers/wrdev.sh`, and every new gate there must be
proven to FAIL pre-fix (three false-PASS gates were caught in an earlier batch) -
see the "HOW TO WORK ON THIS REPO" section of
`.docs/reliable4/next-steps-260819.md` for the required pattern, plus the quality
gates (`make lint` 0 issues; `unset $(env | grep -o '^OS_[A-Z_]*' | tr '\n' ' ');
timeout 1800 make test`, baseline 413 passed / 9 skipped / 29 packages at commit
bf53de0).

HOST SAFETY for anyone running anything: a real production manager runs on
farm22-ibackup01 with files under `/nfs/hgi/wr/lsf/.wr_production/` - never touch
that directory, never kill a manager you did not start, no real LSF jobs, and
keep heavy I/O off `/nfs/hgi`.

## Notes

### Scope: recovery, the add path and the queue, in one delivery

The interim slice-sharing idea is **abandoned**, and no phased-by-area delivery is
wanted. The full group-granularity model lands as one change covering all three
places the quadratic state lives, and prod restarts on it.

The reason is that sharing resolved slices cannot make prod's heap fit. The
quadratic state is also in the queue itself: `Queue.dependants
map[string]map[string]*Item` holds one entry per (member key, waiter) pair, and
`Item.remainingDeps map[string]bool` one entry per member per waiter, with
`setDependencies` rebuilding that map per item however the input slice was
obtained. At prod's shape that is ~2.9e9 map entries with or without sharing.

The `add` control path is quadratic in the same way and must be fixed in the same
change: `db.storeNewJobs` -> `retrieveDependentJobs` gathers the waiters of a new
job's dep groups, then `updateJobDependencies` and `itemDefsForNewJobs` call
`incompleteJobKeys` once per waiter, so adding one member to a group with W live
waiters and M incomplete members allocates W x M strings, twice. A recovery-only
fix would let the manager start and then die on the first `wr add` into a large
dep group.

### Representation

A dep-group edge is carried through `queue` as **one opaque synthetic key** (of
the `depgroup:G` shape), not as a new queue concept. The `queue` package keeps its
documented "depend on opaque keys, resolved when an item with that key is
removed" contract and gains exactly one additive capability: resolving a
dependency key that has no backing item, which is what `promoteDependants`
already does internally from `removeItem`. `jobqueue` owns the per-group state.

That choice reuses `Queue.dependants` unchanged as the per-group waiter set,
reduces `Item.remainingDeps` to a set of group names (one entry per declared
group, not per member), and makes the `neverSeenDepGroupDependencyKey` synthetic-
key machinery redundant. It also avoids one queue-mutex acquisition per waiter
when a group clears, which rule 2 disfavours.

Essence (Cmd+Cwd) dependencies stay individual job-key edges. They are genuinely
per-job identity, they are bounded by what the user declared, and they are not
the quadratic part.

Per group, `jobqueue` holds a **set of live member job keys**, not a count. Each
membership is stored once (~250k entries at prod's shape), which is roughly
1/6000th of today's cost, so memory is no longer the constraint on this choice.
The deciding factor is failure mode: a lost or duplicated decrement on a counter
releases a waiter before its parents have finished, which is silent wrong-order
execution in the user's pipeline - the worst failure class available here. A set
makes re-add, key rename and `wr remove` idempotent.

Per item, state stays a set of the identifiers it declared.

### What the DB already provides

No new bucket and no migration pass are needed, and no startup history scan is
introduced. `bucketRDTK` (`reverseDepgroupToKey`) already stores one entry per
(group, waiter) pair, which is exactly the per-group waiter set the design wants.
Membership comes from `job.DepGroups` and waiters from
`job.Dependencies.DepGroups()` on the jobs recovery has **already decoded**, so
per-group live state is reconstructible with zero extra DB reads.

One exception: "has this group ever been seen" is not derivable from live jobs,
because a fully-completed group has no live member, and
`incompleteDepGroupJobKeys` distinguishes *seen with no incomplete members*
(satisfied) from *never seen* (blocked). Recovery therefore needs one
`bucketDepGroups` get per **distinct** group named by a live job - O(distinct
groups), at most 6,299 at prod's shape - inside the single existing read
transaction. Essence dependencies still need one `checkIfLive` get each, which is
per declared dependency and not quadratic.

### `bucketDTK` is retired

`bucketDTK` (`depgroupToKey`) stops being written. It is read by nothing but the
one-time `rebuildDepGroups`, and dropping the write removes that write
amplification from every add.

The bucket is **left in place, unwritten**. It is not deleted.

An earlier draft of these notes suggested deleting it so that an older binary's
`initDB` would run `rebuildDepGroups` and repair itself. **That premise is false
and the idea is withdrawn.** The rebuild trigger is `openedExistingDB &&
!hadDepGroups`, i.e. gated on `bucketDepGroups` being absent, not `bucketDTK`;
and `rebuildDepGroups` builds `bucketDepGroups` **from** `bucketDTK`, not the
reverse. An absent `bucketDTK` is simply re-created empty.

Deleting it is in fact the *more* dangerous option. `incompleteDepGroupJobKeys`
treats a group that is present in `bucketDepGroups` but has no incomplete members
as **satisfied**. An old binary reading an emptied `bucketDTK` would therefore see
every seen group as already satisfied and run **everything immediately**; reading
a stale one it loses only post-upgrade memberships. Deleting would also break live
operation on the ~150k pre-upgrade jobs, because `deleteLookupEntriesForJobKey`
returns `ErrBucketNotFound` when a reverse entry in `bucketJobLookupEntries` names
a bucket that no longer exists, and `bucketDTK` is an indexed lookup bucket, so
every existing membership has such an entry: archive, delete and modify would
start erroring unless those entries were purged first (an O(all lookup entries)
scan) or the delete path were made tolerant.

The stale bucket **does not** self-drain on archive or delete - an earlier draft
said it did, and that is withdrawn. `deleteLookupEntriesForJobKey` has exactly one
caller, `deleteOldLiveJobs` on the **modify** path; `deleteLiveJobs` explicitly
skips it ("the lookup buckets are historical") and `archiveJobTx` never calls it.
So it drains only via modify, and the "deleting it would make archive, delete and
modify error" argument above holds for **modify only**. That does not change the
decision - leaving it in place still costs nothing and is still the least-bad
downgrade - but do not repeat the self-draining claim.

Consequence the spec must still state plainly: **rolling back to a pre-change wr
binary stops being safe** once new jobs have been added, because that binary reads
`bucketDTK` and would silently see fewer edges, running jobs in the wrong order
with no error. The rollback path is therefore an operator step: stop the manager
and restore a pre-upgrade copy of the DB file.

A hard anti-downgrade mechanism was considered and **rejected** for this spec: a
same-named root-level non-bucket key would make an old binary fail at `initDB`
(`CreateBucketIfNotExists` returns `ErrIncompatibleValue`), and it is the only
such lever available since `jobqueue` has no DB schema-version marker anywhere.
It costs the same reverse-entry surgery as deleting, plus a permanent oddity in
the DB, and it gives up the stale-edge fallback.

### User-visible behaviour is unchanged

`WaitingForDepGroups` keeps its current meaning: **never-seen groups only**. The
new model could report every unsatisfied group for free, but that would change
`wr status -z` from "waiting on a group that does not exist" to "waiting on
anything", flip the `waiting-deps` display state for ordinary dependent jobs, and
alter the REST and web payloads. Record the richer version as a separate future
feature, not part of this work.

Nothing else user-visible changes either. `JStatus.Dependencies` is
`j.Dependencies.Stringify()` - declared group names and `cmd [cwd]` strings, never
expanded member keys - and `.docs/issue-197/spec.md` binds only that `deps` /
`cmd_deps` replace `Job.Dependencies` wholesale, at declared granularity, which
this design keeps. `Item.Dependencies()` and `UnresolvedDependencies()` feed only
internal predicates.

### Consequences that must be handled, not discovered

- **`wr remove`'s guard must be re-derived.** `removeDeletableJobs` skips a job
  while `s.q.HasDependents(jobKey)` is true, because `queue.Remove` *satisfies*
  dependants. With group edges a member's key is no longer in `dependants`, so the
  guard has to ask whether any of the job's `DepGroups` has waiters, preserving
  today's skip-and-walk-the-tree behaviour in `deleteJobs`.
- **`Item.ChangedKey` keeps working and gets cheaper.** Only essence edges are job
  keys, so only they can match the old key; group edges are group names and are
  never renamed. `Queue.ChangeKey` remains an O(all items) walk per rename,
  unchanged by this work.
- **`AddMany` already ignores `StartQueue` for any item with unresolved deps**, so
  a recovered *running* job whose group has gained a member lands in the dependent
  sub-queue rather than run. That is pre-existing behaviour; recovery tests must
  not accidentally "fix" it.

### Out of scope, recorded as follow-ups

- **The add path's all-time waiter scan.** `retrieveDependentJobs` decodes every
  all-time waiter of a new job's dep groups, live and archived, transitively, and
  resurrects archived ones for re-run. It is arguably rule 6's "no history scan on
  a control path", but it is O(all-time waiters) rather than O(waiters x members),
  so it is not the OOM. Preserve it byte-for-byte here to protect the
  resurrect-and-rerun semantics, and record it as a follow-up.
- **No offline live-job-reduction tool.** Prod stays down until this fix lands and
  restarts on it. Do not spend spec or review time on offline surgery.
- **Reporting all unsatisfied dep groups** in `WaitingForDepGroups` (above).

### Proof that gates the merge

In-process gates plus a new `developers/wrdev.sh` scale gate:

- A **memory** gate: retained bytes per recovered job must be independent of dep-
  group size. Memory is faithfully reproducible on this host, unlike churn, so
  this is the primary evidence.
- A **transaction** gate using bbolt's own `db.bolt.Stats().TxN`. The committed
  `jobqueue/reliable4_dependency_tx_test.go` asserts the recovery dependency pass
  costs exactly one transaction **per chunk**, and that must still hold. It is
  never "exactly one" for the whole pass - that shape was rejected in review.
- `make lint` at 0 issues and `make test` / `make race` at the current baseline.
- A `wrdev.sh` gate on a prod-shaped synthetic DB: ~150k live jobs, ~6.3k dep
  groups, one group with ~19k members, ~150k waiters. It must be **proven to FAIL
  pre-fix** from a pristine worktree (three false-PASS gates were caught in an
  earlier batch), and it must cover the **add path** as well as recovery.

A real-LSF Tier-B run is not required: memory reproduces in-process, so an LSF run
adds little to a memory claim and costs days prod does not have. The prod restart
is the final evidence.

### Build on, do not replace, the in-flight transaction fix

Checklist `.docs/bugfixes/260825-2.md` **landed** as commit `c96dcbf`: a
`depReader` interface, a `txDepReader`, and `(*db).resolveDependencies(ctx, jobs)`
that resolves the recovered jobs in **one read transaction per chunk** of
`dependencyResolutionChunkSize = 1000`, returning `[]resolvedJob{job, deps}`
pairs, plus an early return in `depGroupsEverSeen` for jobs with no dep groups.
That seam is the right place for this work to build on, and its **per-chunk**
transaction property must survive. Do not restore a whole-pass transaction: it
would hold `mmaplock.RLock()` for the length of a 21-43 minute recovery and stall
every write db-wide.


### The pre-upgrade DB copy is an operator step, not a wr feature

The rollback above depends on a copy of the DB taken before the new binary first
writes, and **nothing in wr guarantees one**. The spec must say so explicitly, and
must say that `db_bk` is **not** that copy: it is a single rolling file
(`db.backupPath`) which whichever binary is running continuously overwrites, and
`initDB` only copies backup->db when the db file is missing or will not open. The
operator's own `db_bk_precompact` was a manual `cp`.

Making wr take that snapshot itself was considered and rejected: copying a 7 GB
DB on NFS before serving would add minutes to startup, on every future upgrade,
for a one-time need, against the non-blocking-start goal of `.docs/reliable/`
section B1.

### `wr mod --dep_grps` updates the group sets, and may release waiters at modify time

`wr mod --dep_grps` changes membership at runtime (`JobModifier.SetDepGroups` ->
`applyGrouping`, with `modifyLiveJobs` rewriting the DB lookups), so the in-memory
per-group member sets **must** be updated on modify. Not updating them would wedge
that group's waiters forever, which is worse than today's behaviour.

This changes when waiters are released, and the spec must state the change plainly
rather than let it be discovered. Today, removing job J from group G leaves G's
waiters blocked on J's key until J completes, because `reflectModifiedJobInQueue`
is only called when `DependenciesSet || PrioritySet`, so the waiters keep a stale
member-key edge. Under group granularity, if removing J empties G then G's waiters
are released **at modify time**. That is the honest reading of "G has no
incomplete members". Acceptance test required: removing the last member of a group
releases that group's waiters.

### Two queue-side existence proxies must change, and one needs a new pinning test

Both test for a backing item rather than for remaining dependencies, which breaks
when a dependency key is a group name that never has an item:

- `queue.itemHasDeps` (used only by `Kick`) tests `queue.items[dep]`. With group
  keys, a kicked buried job with an unsatisfied group dependency would go straight
  to ready, where today it goes to the dependent sub-queue. The path is reachable:
  a job is buried, a new member is added to its dep group, and
  `updateJobDependencies` -> `q.Update` re-blocks it while `moveToDependentQueue`
  leaves buried items put. Switching the check to `UnresolvedDependencies()` is
  behaviour-identical today except for the never-seen sentinel, which is
  unreachable for a buried job. `queue/queue_test.go:1478-1501` **does** pin
  Kick-with-dependencies where the dependency key has a **real backing item**
  (bury, `Update` with a dep, `Kick` -> `ItemStateDependent`, `Remove` the dep ->
  `ItemStateReady`), and that test must keep passing. What is unpinned is the
  narrower case this design introduces: Kick with a dependency key that has **no
  backing item**, which is what a `depgroup:G` key always is. The spec must add
  that case.
- `pruneDependants` has the same proxy, so group-key waiter entries would never be
  pruned when a `q.Update` drops a group dependency.

### The membership hook set is bounded and known

Live-bucket exit points are exactly three - `archiveJob` (with `s.q.Remove`),
`deleteLiveJobs` (after `s.q.Remove`), and `deleteOldLiveJobs` inside
`modifyLiveJobs` (alongside `q.ChangeKey`) - so the places that must maintain
per-group membership are bounded to: add/enqueue, archive, delete, modify, and the
recovery rebuild. The spec should enumerate exactly these.

### Synthetic keys cannot leak to users

Nothing user-visible consumes `Item.Dependencies()` or
`Item.UnresolvedDependencies()`; the only `jobqueue` callers take `len()`. So a
`depgroup:G` synthetic key cannot reach status, REST or web payloads.

### The seam this builds on has already moved

`(*db).resolveDependencies` now **chunks** at `dependencyResolutionChunkSize =
1000` jobs per read transaction and returns `[]resolvedJob` pairs rather than
index-parallel slices. This was required by review: a single read transaction held
for a whole 21-42 minute recovery pass would block any write that must grow the
bolt mapping (`DB.mmap` takes `mmaplock.Lock()`, a read tx holds
`mmaplock.RLock()` for its life, and the blocked writer holds `db.rwlock`), which
is a db-wide write stall and exactly the Finding 4 signature. So the transaction
gate for this work is **O(chunks)**, not "exactly one", and must never regress to
a whole-pass transaction.

Note also that `jobqueue/reliable4_dependency_tx_test.go` currently pins
**member-key expansion** (asserting resolved keys equal the fixture's live group
member keys, and asserting the never-seen sentinel). Those key-level assertions
are the thing this work deliberately changes, so the spec must say they are to be
rewritten - while the `TxN` assertions survive unchanged.

### Per-group state is built BEFORE clients are served — and this amends spec B1

Today `Serve` sets `setRecovering(0)` and launches `serveClients` **before**
`startPriorStateRecovery` runs its synchronous live-bucket scan, so `add` is served
from the very start of the window. That is a problem for this design: once
`bucketDTK` is retired there is no DB index answering "which live jobs are in
group G", so a group the in-memory state has not yet learned looks **empty**, and
an empty *seen* group means **satisfied** - the newly added job would be released
ahead of its dependencies. Silent wrong-order execution is the failure class this
whole spec exists to remove, so it cannot be left to a race.

**Decision: build the per-group state before accepting clients.** The scan that
builds it is the live-bucket decode that already runs synchronously inside
`Serve`, so the cost is bounded by that decode - measured at **37 s and 51 s** on
prod's 150,472-job DB - and **not** by recovery, which took 21 min and 42m56s.

The spec must state the consequence rather than let it be discovered:
`.docs/reliable/spec.md` **B1 acceptance test 1 as written would fail**. B1 asks
the manager to answer ping/status/add within ~1 s of start regardless of history
or running-job count; this delays serving by the live-bucket decode. B1's actual
motivation - killing a 190 s history scan and a restart that never becomes
responsive - is not reintroduced, since the delay is bounded by live jobs rather
than history and is under a minute at prod's current size. The spec must
explicitly amend B1, restate its acceptance test against the new bound, and say
that the bound grows with live-job count so it needs a stated ceiling.

Rejected alternatives, for the record: returning `ErrRecovering` for dep-group
adds (gives `wr add` a new transient failure), and accepting the race (silent
wrong-order execution).

### `JobModifier.SetDepGroups` is maintained and pinned, though no CLI reaches it

`wr mod --dep_grps` **does not exist**: the flag wiring is commented out in
`cmd/mod.go` ("implementing dep_grps modification is complex; not done for now"),
`JobModifyViaJSON` has no `dep_grps` field (only `deps`/`cmd_deps`, which set
`Dependencies`), and `JobModifier.SetDepGroups` has **no production caller** - its
only uses are a deliberately-malformed test modifier and a comment recording it as
untested. The path is reachable only by a Go consumer hand-building a
`JobModifier`.

The group sets are still maintained on that path, and pinned through the Go client
API, so the seam is correct for whoever re-enables the flag and so the new model
cannot wedge a group's waiters if anyone calls it. Re-enabling the CLI flag is
**out of scope** here - tempting, because the "too complex" reason recorded in
`cmd/mod.go` was precisely the member-key rewriting this design removes, but it
widens a change that is blocking production. Record it as a follow-up.

Note the earlier Notes section on modify timing stands, but read it in this light:
its release-earlier consequence applies to a Go-API-only path today.

### `wr add --rerun` of a live job: dropped groups release their waiters at add time

`wr add --rerun` (and REST `rerun=true`) of a currently-live job Puts a new live
record and **adds** `bucketDTK` entries without deleting the old ones -
`prepareNewJobs` never checks the live bucket, `jobsNotAlreadyQueued` filters live
duplicates only when `ignoreComplete` is true, and `deleteLookupEntriesForJobKey`
is not called on this path. So today `bucketDTK` holds old ∪ new memberships for
that key and a dropped group's waiters stay blocked; a rebuild from the decoded
record can only see the current `DepGroups`, so those waiters **will** be released
at the next restart regardless. The recovery-side divergence is forced.

**Decision: the live path matches it** - dropping a group on a `--rerun` releases
that group's waiters at add time. This keeps the running manager and its own
restart in agreement, and is the same class of earlier-release change already
accepted for modify. State it as a documented consequence.

`modifyLiveJobs` does not have this problem: `deleteOldLiveJobs` purges the old
lookups first.

### Further facts the spec must carry

- **The transition is behaviour-neutral.** The blocked/not-blocked partition is
  identical under both models: a seen group with live members yields non-empty
  deps either way; seen with none yields empty either way; never-seen yields
  non-empty either way (sentinel vs `depgroup:G`). Nothing in the dependent
  sub-queue changes category on first start. `queue.addManyItem` routes **any**
  deps-bearing item to the dependent sub-queue, so the recovered-running-job
  caveat already recorded applies equally to recovered **buried** jobs.
- **`updateJobDependencies`' doc comment is stale**: `modifyLiveJobs` discards
  `prepareNewJobs`' `jobsToQueue`/`jobsToUpdate` (the `//nolint:dogsled` line), so
  the modify path never refreshes a group's waiters. Two consequences: under group
  granularity, adding a member by modification **extends** a currently-blocked
  waiter's wait (the mirror of the release-earlier case); and today a waiter
  blocked on a never-seen group whose group later gains a member via modification
  is **wedged forever**, because nothing removes the sentinel key and nothing
  refreshes it. Group granularity fixes that wedge as a side effect - say so.
- **The `Kick` pinning test must assert the sub-queue, not `job.State`.**
  `kickJobs` sets `State = JobStateReady` whenever `q.Kick` succeeds, including
  when `Kick` routed the item to the dependent sub-queue; the reported state comes
  from `itemToJob` deriving it from the sub-queue. Also, `kickJobs`'
  `readyCallbackExpected` already uses `UnresolvedDependencies()` while `Kick` uses
  `itemHasDeps`, so switching `itemHasDeps` makes the two **agree** where today
  they disagree for the never-seen sentinel.
- **`wr remove`'s skip-and-walk-the-tree behaviour is unpinned at the jobqueue
  level.** `queue/queue_test.go` pins `Queue.HasDependents` itself, which does not
  change; what changes is the caller's derivation in `removeDeletableJobs`. No
  jobqueue/REST/CLI test deletes a dep-group parent and asserts it is skipped, or
  deletes parent and child together and asserts both go. Needs the same pinning
  test treatment as `Kick`.
- **Rule 2 shape.** The archive/delete/add transition path now touches shared
  per-group state, and a single global mutex over the whole group map on the
  archive path is exactly what rule 2 names. Per-group or sharded locking is the
  compliant shape. Worth recording the upside: releasing a 150k-waiter group
  becomes **one** critical section instead of today's ~19k separate
  `promoteDependants` passes over 150k waiters each - strictly better.
- **The `queue` package doc needs a wording amendment** (it states dependencies
  clear when the depended-on items are `Remove()`d), but no exposed number
  changes: `Queue.Stats()` has no dependants count, the schedulers never use
  dependencies, and only `len()` is taken of `Item.Dependencies()` /
  `UnresolvedDependencies()` in non-test code.
- **Scale-gate false-PASS trap.** A prod-shaped synthetic DB must populate
  `bucketDTK`, `bucketRDTK` **and** `bucketDepGroups`, not just `bucketJobsLive`.
  Otherwise the pre-fix run resolves no member keys, never allocates
  quadratically, and the gate false-PASSes - the same failure mode as the
  `pristine10` history fixture. Build the fixture through `db.storeNewJobs`
  rather than writing buckets directly.


### Corrections and additions from round 4

- **`addManyItem` does not route *any* deps-bearing item to the dependent
  sub-queue.** It has an explicit `len(def.Dependencies) > 0 && def.StartQueue ==
  SubQueueSuspended` case that keeps such an item **suspended** with its deps set,
  and `recoveredItemDef` sets `SubQueueSuspended` for `JobStateSuspended`. Buried
  and running recovered jobs behave as recorded earlier; suspended ones do not.
- **`resumeSuspendedItem` already tests `UnresolvedDependencies()`** and so needs
  no change. The only two `queue.items[dep]` existence proxies are `itemHasDeps`
  and `pruneDependants`. `Queue.Stats().Dependant` is `depQueue.len()`, not a
  dependants count, so no exposed number changes.
- **The B1 amendment needs no second scan.** `recoverIncompleteJobs` already
  decodes every live record inside one `bolt.View`; membership comes from
  `job.DepGroups` and waiters from `job.Dependencies.DepGroups()` on those same
  decoded jobs, and the `bucketDepGroups` gets - needed only for a
  dependency-named group that turns out to have no live member - can run inside
  that same `View` after the `ForEach`. The only reorder required is moving
  `startPriorStateRecovery`'s synchronous part above `go s.serveClients(...)`.
- **The "37 s and 51 s" figures are process-start-to-post-scan**, so they include
  `initDB` opening and mmapping the 7 GB bolt file, certs, the web interface and
  the token - not the decode alone. The decode is not isolated anywhere in the
  evidence, so the spec must **measure it** before stating the ceiling it asks
  for; do not quote 37-51 s as the build cost.
- **Scale-fixture ordering matters.** Building the prod-shaped fixture through
  `db.storeNewJobs` is only tractable if the ~19k group members are stored
  **before** the ~150k waiters: `storeNewJobs` -> `retrieveDependentJobs` scans
  the waiters of the new job's own `DepGroups`, so waiters added last (having no
  DepGroups of their own) trigger no scan, whereas members added last would each
  decode all 150k waiters.

### Startup: be invisible until fully ready, and report progress out-of-band

Supersedes the earlier "build the per-group state before accepting clients"
wording, which was insufficient: `serveWebInterface` starts and is awaited
**before** `serveClients`, and `restJobsAdd` calls `s.createJobs` straight from
the HTTP handler, so REST add/modify would still reach the add path with an
unbuilt group map. Gating only the RPC readers does not close that.

**Decision: to the outside world, present exactly the state that already exists
and is well understood - "the manager is not up yet".** Nothing externally
observable comes up until the manager can serve every request correctly - meaning
**recovery has finished**, not just that the group state is built: not the
REST/web listener, not the RPC readers, and not the token file. Clients then
behave as they already do against a down manager, which is a well-trodden path
this effort has repeatedly hardened, rather than meeting a new half-up state with
its own failure modes.

This deliberately avoids all three shapes that were on the table - holding the RPC
readers while the socket answers nothing (which makes `wr runner` die at its 30 s
connect timeout, `wr lsf bsub` at 10 s), blocking only add/modify, and failing
add/modify with `ErrRecovering`. None is needed.

**"Fully ready" means recovery has COMPLETED**, not merely that the per-group
state has been built. Nothing externally observable exists until every prior job
has been re-enqueued and the manager can serve any request correctly.

**The readiness gate is the LISTENER, not the token file.** An earlier draft said
token-last would govern the whole surface. **That is wrong and is withdrawn**, for
two independent reasons:

- `generateToken` **reuses** an existing token file, and `deleteToken` removes it
  only on a known-clean stop - deliberately, so runners can reconnect after an
  unclean exit. Production restarts are OOM-kills, so `client.token` is already on
  disk before the new manager starts, which means `managerTokenReady` /
  `connectIfManagerTokenReady` (it only checks size > 0) is satisfied from the
  first instant, as are `connect()` in `cmd/root.go`, `cmd/runner.go`, `cmd/lsf.go`
  and `client.ConnectUsingConfig`.
- Deleting the token for the window is **not** a safe substitute: the in-memory
  token is the one read from that file, so a second kill mid-window would make the
  next start generate a **new** token and lock out every 24h-retrying runner.

**`configureAndListen` must move instead.** It currently runs long before
`persistToken`, `serveClients` and recovery, and it calls `sock.ListenOptions`, so
the TLS port is bound and accepting for the whole window. mangos' first `Dial` is
synchronous, so a client gets a fast `ErrNoServer` only when the port is
**closed**; against a listening-but-unread socket the dial succeeds and the ping
burns the client's entire connect timeout. That is the hang this decision exists
to avoid, so the listener is the thing to publish last. Only `expiry` is needed
early, and `earliestCertExpiry` already exists separately. Token-last is then
belt-and-braces for `manager start`'s own poll, not the gate.

**The window is therefore the whole recovery** - 21 min and 42m56s on the two
2026-08-25 production runs. Those figures are inflated by the memory bug this
spec fixes (GC was consuming the machine), but even fixed it is O(minutes) at
150k live jobs, and it scales with live-job count.

**Why that is acceptable** (operator's reasoning, verified in the code):

- A **pending LSF runner that starts during the window** connects once with a 30 s
  timeout and `die()`s on failure (`cmd/runner.go:168-170`). But it would do
  exactly the same against a manager that is simply down, which is the status quo
  every crash already produces. Slow-starting and down are indistinguishable to
  it, so this introduces **no new loss class** - the concern that first argued
  against this option does not survive contact with that fact.
- A **runner that had connected before the manager went down keeps retrying for up
  to 24 hours**: `ClientRetryTime = 24 * time.Hour` (`jobqueue/client.go:116`). A
  20-40 minute delay is immaterial to it, and is indistinguishable from the
  operator having started the manager 20-40 minutes later with a fast startup.

**This REVERSES spec B1, it does not merely amend it.** `.docs/reliable/spec.md`
B1 is "answer ping/status/add within ~1 s of start regardless of history or
running-job count, so a `kill -9` restart is never stuck", and this decision makes
startup blocking again. The spec must own that reversal explicitly rather than
letting it read as an oversight. Note that what reverses is B1's **user story**,
not a test: an earlier draft claimed B1 acceptance test 1 would fail, and that is
**withdrawn** - `recoveryPauseHook` fires inside the background goroutine after
the synchronous decode, and the in-process tests measure responsiveness after
`serve` returns, so the reorder breaks no timing assertion. The justification is that B1's actual problem was
a **history**-sized scan (190 s and growing with 2.15M archived records, unbounded
by anything the operator controls), whereas this window is bounded by **live**
jobs, is cut dramatically by this spec's own fix, and buys correctness that B1's
ordering cannot provide - there is no way to serve the add path correctly before
the group state exists, and REST reaches that path before the RPC readers even
start.

**`Serve` must NOT block on recovery.** The test helper calls `Serve`
synchronously and `pausedRecoveringFixtureServer` only waits for the pause hook
*after* `Serve` returns, so a blocking `Serve` would deadlock the existing
recovery tests against a release that can never come. The workable shape is to
keep recovery in its background goroutine and **publish the listener, web
interface and token from that goroutine's tail**.

**Publish on recovery ENDING, not succeeding.** `recoverPriorJobsAndNote` logs and
returns on failure while `finishRecovering` still runs, so hanging publication off
success would leave a manager that is up, holds the DB lock, and is invisible
forever while `wr manager start` polls indefinitely. (The correctness-critical
half already fails loudly: a decode or build error returns from
`startPriorStateRecovery` and `Serve` errors out, which production `die()`s on.)

**Consequences the spec must state:**

- **Recovery observability MOVES from the request surface to the sidecar; it does
  not disappear.** The window stays observable - `wr manager start` reads the file
  sidecar and reports progress, and anything else may read that file. What becomes
  unreachable is specifically the **server-request** pathway: no client can send a
  request before recovery finishes, so `ErrRecovering` can no longer be delivered
  to anyone, and the `!s.isRecovering()` scheduling gate can no longer be
  exercised in production either, because no dispatch happens before the manager
  is visible. Spec B2's recovery-window contract (a reconnecting runner gets a
  retryable `ErrRecovering` rather than a terminal `ErrBadJob`) is satisfied
  vacuously rather than actively. Decide deliberately whether that machinery is
  kept as defence in depth against a future reordering, or removed, and say which -
  do not let it rot into dead code by accident.
- **This makes the sidecar the primary operator channel during startup**, so its
  content is not a nicety: it is the only way to tell a slow start from a hang for
  however many minutes recovery takes. Specify what it reports (phase, and
  `restored`/`total` once known) and how often it is updated.
- **`wr manager start` is the only client that must work during the window**, and
  it already does: `waitForLiveManagerStartupWith` polls indefinitely, tells a
  dead daemon from a slow one via the child process handle, and reports progress
  from the file sidecar. Extend the sidecar with the new phases so a multi-minute
  startup shows progress rather than silence.
- **Pin the 24-hour reconnect survival with a test.** The whole justification rests
  on it, and `ClientRetryTime` being a constant is not the same as proving a runner
  survives a long absence and resumes correctly.

**`wr manager start` needs no new mechanism.**
`waitForLiveManagerStartupWith` already: loops indefinitely (its `timeout`
parameter only schedules the first "still waiting" report); distinguishes "not up
yet" from "the daemon exited" via the child process handle, so a genuine failure
is still prompt; polls with a short per-attempt connect deadline; and reports
slow-startup progress by reading the **file sidecar**
(`internal.ReadDBUpgradeStatus` / `DBUpgradeStatusPath`), which is exactly the
channel a not-yet-serving manager needs. The spec should extend that sidecar (or
add a sibling using the same pattern) to carry the new startup phases - live-bucket
decode, dependency-state build, recovery `restored`/`total` - so an operator
watching `wr manager start` sees progress rather than silence. That overlaps
`.docs/bugfixes/260825-3.md` item 2, which is making the same reporter's output
visible; keep the two consistent.

**Two hazards the spec must state.**

1. **A restart cron can double-start.** The operator's environment has a cron that
   restarts the manager (it did so on 2026-08-25 at ~09:20). Extending the window
   in which the manager is unreachable extends the window in which such a cron may
   conclude it is dead and launch a second one. The spec must say what happens
   then - bolt's exclusive file lock is the backstop, and `manager start` has its
   own up-check - and must verify that the loser fails cleanly and leaves the
   winner's DB untouched, rather than assuming it.
2. **The window is unmeasured.** The build folds into the live-bucket decode that
   `recoverIncompleteJobs` already performs in one `bolt.View`, so it needs no
   second scan, but the decode's own duration has never been isolated (the 37 s and
   51 s figures in `.docs/reliable4/prod-restart-260825.md` are
   process-start-to-post-scan and include `initDB` mmapping a 7 GB file). Measure
   it, state the ceiling, and say how it scales with live-job count - because this
   decision converts that duration into total unavailability.


### Round 5 corrections and additions

- **No doomed runners are spawned during the window.** Runner dispatch is already
  gated on recovery: `if rc != "" && !s.isRecovering()`. Scheduler groups are
  still built, but no `bsub` happens until recovery finishes - the same instant
  serving begins. So the window submits nothing that could start and die.
- **`wr manager status` will report "non-responsive" for the whole window**, and
  will `die()`: the daemonized child writes its pid file before `Serve`, so a pid
  exists while the RPC cannot answer. That is a second reason the sidecar is the
  operator channel, and the spec should say what `wr manager status` ought to do
  instead (read the sidecar, as `manager start` does).
- **Membership-hook enumeration needs two additions.** The modify hook has **two**
  independent call sites, each recomputing `incompleteJobKeys` -
  `serverCLI.go:1718` and `serverREST.go:2329` (the four production call sites in
  total are `server.go:5082`, `server.go:5225`, `serverCLI.go:1718`,
  `serverREST.go:2329`). And the add hook must register memberships for all of
  `storeNewJobs`' `jobsToQueue`, which includes archived waiters **resurrected**
  by `retrieveDependentJobs`, not only the input jobs.
- **Tolerate a chunked live-bucket decode.** `.docs/bugfixes/260825-3.md` item 3
  proposes chunking `recoverIncompleteJobs`' single `bolt.View`. If it lands, the
  Notes' "the `bucketDepGroups` gets can run inside that same `View`" no longer
  describes one transaction. That is safe here - nothing is served and there are no
  concurrent writers - but the spec must not depend on a single decode transaction.

### Double-start hardening: bound `bolt.Open`, because `-f` has no pid file at all

Verified mechanism today: a second `wr manager start` during the window is **not**
stopped by the pid-file lock. `wr`'s own `reborn` **deletes** the locked pid file
and retries, which succeeds against a new inode, so the imposter spawns and its
pid replaces the live manager's. It then blocks in `bolt.Open` **forever** -
`initDB` passes no `Timeout`, and bbolt retries the flock every 50 ms
indefinitely - so the "db won't open, restore from backup" branch is never
reached. The winner's DB is untouched, but the hung imposter acquires it the
instant the winner exits, which collides with this spec's documented rollback
procedure (stop the manager, restore a pre-upgrade DB copy): the imposter would
start writing to the file being restored.

**With `--foreground`/`-f` there is no pid file and no flock at all.** The `-f`
branch calls `startJQ` directly and never calls `daemonize`, so:

- the pid-file guard does not exist in the mode the operator uses for diagnostics
  (this is why, after the 2026-08-25 11:59 `-f` start, `pid` still named the dead
  09:22 process);
- `wr manager stop` cannot find an `-f` manager by pid file, so stopping one
  depends on the RPC - which does not answer during the window, leaving kill-by-
  hand as the only option;
- `wr manager status` on an `-f` manager reports "stopped" during the window (no
  valid pid, no connection), which is closer to the truth than the daemonized
  path's `die()` with "supposed to be running with pid N, but is non-responsive".

**Decision: bound `bolt.Open` with a timeout, and do not change `reborn`'s
pid-file behaviour - but the bound MUST NOT reach the restore-from-backup
branch.**

**This is the hazard, and it is worse than the one being fixed.** `initDB`'s
`openedExistingDB` branch treats **any** `bolt.Open` error as "corrupt (?) db
file": if `db_bk` exists and opens, it does `os.Remove(dbFile)`, copies the backup
over it, and opens the new file. Add `Options.Timeout` naively and a second
manager started during the window gets `bolt.ErrTimeout`, **unlinks the live DB
out from under the running manager** (which keeps writing to a deleted inode and
loses everything at exit), and comes up as a **second live manager** on a stale
backup - on a fresh inode, so the flock now protects nothing. Two managers both
submitting to LSF, and the winner's DB destroyed, including mid-rollback. Today's
"the restore branch is never reached" is a shortcoming; **reaching it is the real
danger.** So the bound must map a lock timeout to a clean fatal error - "another
manager holds this database" - and never enter the restore path.
`offlineDBOpenTimeout` is safe precedent only because that caller merely returns
the error. The open timeout is the only mechanism that works
uniformly - it does not care about daemon mode, pid files, or who started what -
and it removes the hazard that actually matters, a hung process seizing the DB
mid-rollback. Hardening `reborn` instead would produce a guard that silently does
not apply under `-f`. Note `offlineDBOpenTimeout` already exists as precedent for
exactly this (the offline compact subcommand bounds its open so a fooled up-check
fails cleanly rather than blocking forever).

The spec must also state the `-f` gaps above as known behaviour, and say what
`wr manager status` should do during the window in **both** modes - reading the
sidecar, as `manager start` already does, is the obvious answer and makes the two
modes consistent.


### Round 6 findings the spec must handle (clarification loop returned NONE)

- **Shutdown during the window nil-derefs.** `s.httpServer` is set only in
  `serveWebInterface` and dereferenced unguarded in `shutdownHTTPServer`, so a
  SIGTERM before the web interface starts panics. This is reachable, not
  hypothetical: `wr manager stop` reads the pid file and SIGTERMs it, and the pid
  file exists before `Serve` - so `wr manager stop`, and therefore the documented
  rollback procedure, does work during the window and will hit this.
- **The sidecar does not exist on a plain restart, and `Serve` deletes it at the
  worst moment.** `keepPostUpgradeStartupStatus` writes the status file **only**
  `if upgradedOnOpen`, and the remover it returns is deferred in `Serve` - so the
  file is removed exactly when `Serve` returns, i.e. as recovery begins and the
  sidecar becomes the only operator channel. Both the conditional write and the
  removal timing must change for "the sidecar is the primary operator channel"
  to be true.
- **Publication timing, per failure mode.** A panic is covered:
  `recoverInBackground`'s `internal.LogPanic(ctx, ..., true)` calls `os.Exit(1)`,
  so the process dies and `manager start`'s child handle reports it promptly -
  **provided publication is a plain tail statement and not a defer**, since a
  defer registered after `LogPanic`'s would publish a listener microseconds before
  the exit. Two modes to handle explicitly: `recoverPriorJobsAndNote` returns early
  on `ctx.Err()` during shutdown, so the tail **is** reached while the socket is
  being torn down and publication must be skipped there; and defers run LIFO, so a
  tail statement publishes **before** `finishRecovering`, leaving a window with the
  listener up while `isRecovering()` is still true. That same ordering is what
  keeps `waitUntilRecovered`-gated tests race-free, so neither order is free -
  choose deliberately and say why.
- **Moving `configureAndListen` moves its error path too.** Nothing between its
  current call site and `Serve`'s return touches the socket, and `expiry` has
  exactly one use (`certExpired` into `handleSignals`), so splitting
  `earliestCertExpiry` off is clean. But `tls.LoadX509KeyPair` and the port bind
  both live in `listenTLS`: today a port-already-in-use or a bad keypair fails fast
  through `Serve`'s error return and `wr manager start` dies cleanly. At the tail
  they become failures 20-40 minutes in, inside a goroutine with no error path, and
  must explicitly kill the process.
- **Tests that connect right after `serve()` now race publication.** `Serve`
  returning no longer implies a bound listener, the test helper only retries on
  error, and `Connect`/`dialClientSocket` fails immediately with `ErrNoServer`
  against a closed port with no retry. Two committed recovery-window tests connect
  while recovery is deliberately **paused** - `reliable2_dbcompat_test.go:193`
  (spec H2 acceptance test 1, `ErrRecovering` to a reconnecting runner) and `:240`
  (H2 test 2, nothing reservable during the window) - so they can never connect at
  all and must be reframed as server-state assertions. This also **supersedes** the
  earlier note that "the in-process tests measure responsiveness after `serve`
  returns, so the reorder breaks no timing assertion": that was true of the
  withdrawn reorder, not of this decision.
- **`-f` mode announces a manager that will not listen for minutes.**
  `startJQ` calls `logStarted(server.ServerInfo, token)` immediately after `Serve`
  returns, printing "manager started" and a web URL. Fix or reword it.
- **Scheduler messages raised during recovery are silently discarded**, since
  moving `serveWebInterface` late also moves `SetMessageCallBack` /
  `SetBadServerCallBack` and the casters. Harmless mechanically (the openstack
  implementation nil-checks both, `caster.Broadcasting` is a no-op, `Send` with no
  members drops) but say so rather than losing them by accident.
- `wr cloud deploy` blocks on the remote `manager start`, which loops
  indefinitely, so a long window extends deploy - but a fresh cloud DB has no live
  jobs, so it is ~0 in practice.
