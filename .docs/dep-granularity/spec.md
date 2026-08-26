# Dep-Group Dependency Granularity Specification

## Overview

The production manager has been OOM-killed five times. `dmesg -T` on
farm22-ibackup01 (UID mercury) records four: 2026-08-24 15:54:06 at 180.5 GB
anon-rss, 19:18:56 at 181.4 GB, 20:15:59 at 181.4 GB, and 2026-08-25 10:06:51 at
174.8 GB, on a node with 182.7 GB of RAM; a fifth followed at ~12:47:45. `go
tool pprof -top -inuse_space` against the live manager at a 15.65 GB heap
mid-recovery put **97.55%** of it under `(*Server).recoveredItemDef`:
`(*db).retrieveIncompleteJobKeysByDepGroup.func1` 64.50% and `sortedStringSet`
32.49%. The 150,472 decoded jobs are **376 MB (2.40%)**. The memory is not the
jobs, it is their dependency key lists, at roughly 930 KB or ~19,000 retained
keys per job. Recovery of those 150,472 jobs took 42m56s and the manager died
four minutes after it finished. Production cannot stay up.

The cause is that a declared dep-group edge is expanded into member job keys. A
user says "job J depends on dep group G"; `Dependency.incompleteDepGroupJobKeys`
(`jobqueue/dependency.go:178`) turns that into "J depends on j1, j2, ... jk" by
cursor-scanning `bucketDTK` for G's prefix, allocating a fresh string per
member, so every job referencing G gets its own private copy of G's whole
membership. `sortedStringSet` copies each set again, and `recoverPriorJobs`
retains all of it in the `*queue.ItemDef`s for all jobs before enqueuing any.
Cost: O(live jobs x group membership) in both bolt lookups and bytes.

This spec keeps every dependency edge at the granularity the user declared it. A
dep-group edge travels through `queue` as **one opaque synthetic key** of the
`depgroup:G` shape; `jobqueue` holds, per group, the set of live member job
keys, each membership stored once. Total state becomes O(jobs + total group
memberships). Recovery then needs no per-job dep-group database lookup at all,
because a job's declared dependencies are already in its decoded record. The
change lands as one delivery across all three places the quadratic state lives -
recovery, the `queue` package and the `add` control path - because a
recovery-only fix would let the manager start and then die on the first `wr add`
into a large dep group. It also changes the startup model: nothing externally
observable comes up until recovery has finished, which **reverses** the user
story of `.docs/reliable/spec.md` B1 (section E).

## Architecture

### The three places the quadratic state lives

All three must change together.

1. **Recovery.** `recoverPriorJobs` (`server.go:3941`) ->
   `db.resolveDependencies` (`dependency.go:450`) ->
   `Dependencies.incompleteJobKeys` (`dependency.go:280`) ->
   `incompleteDepGroupJobKeys` (`dependency.go:178`) ->
   `retrieveIncompleteJobKeysByDepGroupTx` (`db.go` prefix scan of `bucketDTK`
   filtered by `bucketJobsLive`). Every result slice is retained in a
   `queue.ItemDef` (`recoveredItemDef`, `server.go:4023`) until `enqueueItems`
   (`server.go:4934`).
2. **The queue.** `Queue.dependants map[string]map[string]*Item`
   (`queue/queue.go:337`) holds one entry per (member key, waiter) pair, and
   `Item.remainingDeps map[string]bool` (`queue/item.go:79`) one entry per
   member per waiter, rebuilt per item by `setDependencies`
   (`queue/item.go:256`) however the input slice was obtained. At prod's shape
   that is ~2.9e9 map entries, with or without any slice sharing - which is why
   sharing resolved slices was abandoned.
3. **The add path.** `db.storeNewJobs` -> `retrieveDependentJobs` gathers the
   waiters of a new job's dep groups, then `itemDefsForNewJobs`
   (`server.go:5082`) and `updateJobDependencies` -> `gatherDependencyUpdates`
   (`server.go:5225`) each call `incompleteJobKeys` once per waiter. Adding one
   member to a group with W live waiters and M incomplete members allocates W x
   M strings, twice.

### Target representation

- A dep-group edge is carried through `queue` as one opaque synthetic key,
  `depgroup:` + the group name. It is not a new `queue` concept: the package
  keeps its documented "depend on opaque keys" contract and gains exactly one
  additive capability (B1).
- Essence (Cmd+Cwd) dependencies stay individual job-key edges. They are per-job
  identity, bounded by what the user declared, and not the quadratic part.
- `Queue.dependants` is reused unchanged as the per-group waiter set: one entry
  per (group, waiter). `Item.remainingDeps` reduces to one entry per declared
  group.
- Per group, `jobqueue` holds a **set of live member job keys**, not a count.
  Each membership is stored once (~250k entries at prod's shape, roughly
  1/6000th of today's cost), so memory is not the deciding factor. The deciding
  factor is the failure mode: a lost or duplicated decrement on a counter
  releases a waiter before its parents have finished, which is silent
  wrong-order execution in the user's pipeline. A set makes re-add, key rename
  and `wr remove` idempotent.
- Per item, state stays a set of the identifiers it declared.
- The `neverSeenDepGroupDependencyPrefix` machinery (`dependency.go:39`, `:198`,
  `:432`) becomes redundant: a `depgroup:G` key for a never-seen group already
  blocks, because nothing satisfies it until G gains and then loses members.

Job keys are 128-bit FarmHash hex strings (`byteKey`, `utils.go`), so no job key
can collide with the `depgroup:` prefix.

### Resolution rule

For each declared dependency of a job:

- dep group G with a live member: emit `depgroup:G`.
- dep group G with no live member, G ever seen: emit nothing (satisfied).
- dep group G with no live member, G never seen: emit `depgroup:G` and add G to
  `WaitingForDepGroups`.
- essence E whose key is live: emit that job key.
- essence E not live: emit nothing.

This partition is identical to today's: a seen group with live members yields
non-empty deps either way; seen with none yields empty either way; never-seen
yields non-empty either way (sentinel versus `depgroup:G`). Nothing in the
dependent sub-queue changes category on first start.

"Has G a live member" is answered from memory. "Was G ever seen" is one
`bucketDepGroups` get, needed only for the groups that have no live member.

### What the database already provides

No new bucket, no migration pass, and no startup history scan, so
DEVELOPERS.md rule 6 holds.

- `bucketRDTK` (`reverseDepgroupToKey`) already stores one entry per (group,
  waiter) pair and keeps its only reader, `retrieveDependentJobs`
  (`db.go:2885`), unchanged.
- Membership comes from `job.DepGroups`, waiters from
  `job.Dependencies.DepGroups()`, on jobs that have already been decoded.
- `bucketDepGroups` answers "ever seen". The seen check is per job, so with no
  dedup it would cost one `b.Get` per (job, no-live-member group) pair - which
  at prod's shape is one per live job naming a never-seen group, not one per
  group. Recovery therefore shares one `seenDepGroupCache` across the whole
  pass, bringing it back to one get per **distinct** group named by a live
  job's dependencies that turns out to have no live member: at most 6,299 at
  prod's shape. The cache changes no transaction count: recovery's gets already
  ride the chunk's open read transaction. On the add path there is no such
  cache, and each waiter that names a no-live-member group costs the one
  `db.depGroupsEverSeen` read transaction it costs today - bounded by the
  waiters in that one call, and unchanged by group membership.
- Essence dependencies still need one `checkIfLive` get each: per declared
  dependency, not quadratic.

### `bucketDTK` is retired, not deleted

`bucketDTK` (`depgroupToKey`) stops being written: drop `dgLookups` from
`prepareNewJobs` (`db.go` per-job `job.DepGroups` loop), from
`storeNewJobData`'s `batchStore` list, and from `newJobLookups`/`putAllLookups`.
Its only remaining production reader is the one-time `rebuildDepGroups`, and
dropping the write removes that write amplification from every add.

The bucket is **left in place, unwritten**. It is not deleted, and it stays in
`indexedLookupBuckets()` and `isIndexedLookupBucket` so pre-existing entries
stay consistent under `deleteLookupEntriesForJobKey` and the lookup-index
rebuild.

Deleting it would be more dangerous, not less. `initDB`'s rebuild trigger is
`openedExistingDB && !hadDepGroups`, gated on `bucketDepGroups` being absent,
and `rebuildDepGroups` builds `bucketDepGroups` **from** `bucketDTK`, so an
absent `bucketDTK` is simply re-created empty and repairs nothing. Worse, an old
binary reading an emptied `bucketDTK` would treat every seen group as satisfied
and run everything immediately, whereas a stale one loses only post-upgrade
memberships. Deleting would also break the modify path on the ~150k pre-upgrade
jobs: `deleteLookupEntriesForJobKey` returns `ErrBucketNotFound` when a reverse
entry in `bucketJobLookupEntries` names a missing bucket, and every existing
membership has such an entry.

The stale bucket does not self-drain. `deleteLookupEntriesForJobKey` has exactly
one caller, `deleteOldLiveJobs` on the modify path; `deleteLiveJobs` explicitly
skips it ("the lookup buckets are historical") and `archiveJobTx` never calls
it.

A hard anti-downgrade mechanism was considered and rejected: a same-named
root-level non-bucket key would make an old binary fail at `initDB`
(`CreateBucketIfNotExists` returns `ErrIncompatibleValue`), and it is the only
lever available since `jobqueue` has no DB schema-version marker. It costs the
same reverse-entry surgery as deleting, plus a permanent oddity in the DB, and
it gives up the stale-edge fallback.

**Rolling back to a pre-change wr binary stops being safe** once new jobs have
been added, because that binary reads `bucketDTK` and would silently see fewer
edges, running jobs in the wrong order with no error. The rollback path is an
operator step: stop the manager and restore a pre-upgrade copy of the DB file.
`db_bk` is **not** that copy - it is a single rolling file (`db.backupPath`)
that whichever binary is running continuously overwrites, and `initDB` only
copies backup -> db when the db file is missing or will not open. Nothing in wr
guarantees a pre-upgrade snapshot; the operator's own `db_bk_precompact` was a
manual `cp`. Making wr take that snapshot itself was rejected: copying a 7 GB DB
on NFS before serving would add minutes to every future upgrade for a one-time
need.

### User-visible dependency reporting is unchanged

`WaitingForDepGroups` keeps its current meaning: **never-seen groups only**. The
new model could report every unsatisfied group for free, but that would change
`wr status --missing_deps` from "waiting on a group that does not exist" to
"waiting on anything", flip `cmd/status_table.go`'s `waiting-deps` display state
for ordinary dependent jobs, and alter the REST and web payloads. That richer
report is a separate future feature (section G3).

Nothing else about dependency reporting changes. `JStatus.Dependencies` is
`j.Dependencies.Stringify()` - declared group names and `cmd [cwd]` strings,
never expanded member keys - and `.docs/issue-197/spec.md` binds only that
`deps`/`cmd_deps` replace `Job.Dependencies` wholesale at declared granularity,
which this design keeps. `Item.Dependencies()` and `UnresolvedDependencies()`
feed only internal predicates, and non-test `jobqueue` callers take only `len()`
of them, so a `depgroup:G` key cannot reach status, REST or web payloads.
`Queue.Stats().Dependant` is `depQueue.len()`, not a dependants count, so no
exposed number changes.

### New symbols

    // jobqueue/depgroups.go

    // depGroupDependencyPrefix prefixes a dep group name to form the opaque
    // queue dependency key for that group. Job keys are 128-bit FarmHash hex
    // strings, so no job key can collide with it.
    const depGroupDependencyPrefix = "depgroup:"

    func depGroupDependencyKey(depGroup string) string

    // depGroupMembers holds, for each dep group with at least one live member
    // job, the keys of those members, and for each live job the groups it is a
    // member of. Both maps are sharded so the archive, delete, modify and add
    // paths never contend on one server-wide lock (DEVELOPERS.md rule 2).
    type depGroupMembers struct{ ... }

    func newDepGroupMembers() *depGroupMembers

    // hasMembers says whether depGroup has at least one live member job.
    func (m *depGroupMembers) hasMembers(depGroup string) bool

    // add records jobKey as a live member of each of depGroups. Idempotent.
    func (m *depGroupMembers) add(depGroups []string, jobKey string)

    // remove drops jobKey from every group it is a member of, returning the
    // groups left with no live member. Idempotent.
    func (m *depGroupMembers) remove(jobKey string) []string

    // replace makes newGroups jobKey's membership, returning the groups left
    // with no live member. Idempotent.
    func (m *depGroupMembers) replace(
        jobKey string, newGroups []string,
    ) []string

    // rekey is replace across a job key change: newKey becomes a member of
    // newGroups, and any membership held under oldKey is then dropped. It
    // returns the groups left with no live member. The new key is recorded
    // BEFORE the old one is dropped, so a group both keys belong to never
    // transiently empties - the ordering matters, because an emptied group
    // releases its waiters. Idempotent; oldKey == newKey behaves as replace.
    func (m *depGroupMembers) rekey(
        oldKey, newKey string, newGroups []string,
    ) []string

    // memberships returns the total number of (group, member) pairs held.
    func (m *depGroupMembers) memberships() int

    // jobqueue/dependency.go

    // depGroupState answers whether a dep group has a live member job, without
    // a database read.
    type depGroupState interface {
        hasMembers(depGroup string) bool
    }

    // dependencyKeys returns the queue dependency keys for these Dependencies -
    // one depgroup:G key per unsatisfied declared group, one job key per live
    // essence dependency - plus the declared groups that have never been seen.
    func (d Dependencies) dependencyKeys(
        reader depReader, groups depGroupState,
    ) (deps []string, waitingForDepGroups []string, err error)

    // resolveDependencies and resolveDependencyChunk both take groups, because
    // dependencyKeys cannot answer "has G a live member" without it. The pass
    // builds one seenDepGroupCache and hands it to every chunk.
    func (db *db) resolveDependencies(
        ctx context.Context, jobs []*Job, groups depGroupState,
    ) ([]resolvedJob, error)

    func (db *db) resolveDependencyChunk(
        ctx context.Context, jobs []*Job, groups depGroupState,
        cache *seenDepGroupCache,
    ) ([]resolvedJob, error)

    // seenDepGroupCache memoises "was this dep group ever seen" for the length
    // of one resolution pass, so a never-seen group named by 150,000 live jobs
    // costs one bucketDepGroups get rather than 150,000. bucketDepGroups is
    // only ever added to, and nothing is served during recovery (E1), so a
    // cached answer cannot go stale within a pass. It counts the reads it
    // actually makes into the counter it is given.
    type seenDepGroupCache struct{ ... }

    func newSeenDepGroupCache(gets *atomic.Uint64) *seenDepGroupCache

    // depReader wraps reader so that depGroupsEverSeen is answered from the
    // cache, asking reader only about groups the pass has not asked about yet.
    // The wrapper is rebuilt per chunk (each chunk has its own transaction)
    // while the cache lives for the whole pass.
    func (c *seenDepGroupCache) depReader(reader depReader) depReader

    // jobqueue/server.go

    // Server field
    depGroups *depGroupMembers

    // registerDepGroupMembers records each job as a live member of its
    // DepGroups, releasing nothing: it only adds, so no group can empty. It is
    // recovery's bulk rebuild (C1) and the add path's first pass (D1). Call
    // before resolving any dependency against this state.
    func (s *Server) registerDepGroupMembers(jobs []*Job)

    // releaseDepGroupMembership drops jobKey from every group it is a member of
    // and satisfies the queue dependency key of every group thereby emptied.
    // Must not be called while holding the queue mutex.
    func (s *Server) releaseDepGroupMembership(
        ctx context.Context, jobKey string,
    )

    // replaceDepGroupMembership makes newGroups jobKey's membership and
    // satisfies the queue dependency key of every group thereby emptied. On the
    // add path it is the second of two passes, after registerDepGroupMembers
    // has recorded the whole batch's groups, so it only ever applies drops
    // there (D1).
    func (s *Server) replaceDepGroupMembership(
        ctx context.Context, jobKey string, newGroups []string,
    )

    // rekeyDepGroupMembership is replaceDepGroupMembership across a key
    // change, for the modify path: newKey takes newGroups before oldKey's
    // membership is dropped, so a group both keys belong to is never seen as
    // empty and its waiters are not released early.
    func (s *Server) rekeyDepGroupMembership(
        ctx context.Context, oldKey, newKey string, newGroups []string,
    )

    // Serving is closed once the manager is externally reachable (RPC listener
    // bound and read, web interface up, token file written), or once shutdown
    // begins, whichever is first.
    func (s *Server) Serving() <-chan struct{}

    // jobqueue/db.go

    // db field: depGroupSeenGets counts the dep groups a resolution pass
    // actually read from bucketDepGroups - one per distinct group with no live
    // member, not one per job naming it. resolveDependencies passes it to
    // newSeenDepGroupCache. It is INERT observability in the style of
    // db.archivedDecodes (db.go:682), and lives on the db so a test counts only
    // its own server's reads.
    depGroupSeenGets atomic.Uint64

    // managerDBOpenTimeout bounds how long the manager's own initDB waits for
    // the BoltDB file lock, so a second manager started during the startup
    // window fails cleanly instead of blocking forever and then seizing the
    // database the instant the first one exits. A var, not a const, so a test
    // can lower it (E7).
    var managerDBOpenTimeout = 30 * time.Second

    // ErrDBLocked is returned by initDB when the database file lock is held by
    // another process. It short-circuits the restore-from-backup branch, which
    // would otherwise unlink a live manager's database.
    var ErrDBLocked = errors.New("another wr manager holds this database")

    // queue/queue.go

    // SatisfyDependency resolves the dependency on key for every item that
    // depends on it, moving to the ready sub-queue any item whose last
    // dependency this was. Unlike Remove, key need not name an item in the
    // queue.
    func (queue *Queue) SatisfyDependency(ctx context.Context, key string) error

`depReader` loses `retrieveIncompleteJobKeysByDepGroup`; `txDepReader` loses the
matching method. `db.retrieveIncompleteJobKeysByDepGroup` and
`retrieveIncompleteJobKeysByDepGroupTx` remain for `db_test.go`'s pre-upgrade
assertions and have no production caller.

### Membership hook set

Live-bucket exit points are exactly three, so the places that must maintain
per-group membership are bounded to these five:

- **add / enqueue**, in `createJobs` after `db.storeNewJobs` returns and before
  `itemDefsForNewJobs` (`server.go:5055-5059`), in **two passes** over
  `jobsToQueue`: `registerDepGroupMembers(jobsToQueue)`, then
  `replaceDepGroupMembership(ctx, job.Key(), job.DepGroups)` per job. The first
  pass only adds, so it can empty nothing; the second applies each job's drops
  and satisfies whatever it leaves empty. A single `replace` pass would release
  a group's waiters early whenever one job in the batch drops a group that
  another job in the same batch joins (D1).
- **archive**, in `archiveCompletedJob` (`serverCLI.go:1379`) after
  `s.q.Remove`: `releaseDepGroupMembership(ctx, job.Key())`.
- **delete**, in `finalizeDeletedJobs` (`server.go:5613`) after
  `db.deleteLiveJobs`: `releaseDepGroupMembership` per deleted key.
- **modify**, `rekeyDepGroupMembership(ctx, oldKey, job.Key(), job.DepGroups)`
  per job, in the region of each path that runs for **every** modification:
  - `persistModifiedJobsToDB` (`serverCLI.go:1692`), between the
    `db.modifyLiveJobs` success at `:1699-1703` and the
    `if !cr.Modifier.DependenciesSet && !cr.Modifier.PrioritySet { return }` at
    `:1705`. `oldKey` is the `oldKeys[i]` this function already builds from
    `modified[job.Key()]`.
  - `storeModifiedJobs` (`serverREST.go:2256`), between the
    `db.modifyLiveJobs` call at `:2270` and the `if modifier.DependenciesSet ||
    modifier.PrioritySet` guard at `:2274`. `oldKey` is the matching entry of
    `modifiedOldKeys(modified, jobs)`, which is index-parallel with `jobs`.

  The guarded halves - `reflectModifiedJobInQueue` (`serverCLI.go:1717`) and
  `updateModifiedQueueJobs` (`serverREST.go:2327`) - are the wrong place: a
  `DepGroups`-only modification never reaches them, and that is the case that
  must not wedge a group's waiters (D2).
- **recovery rebuild**, in `startPriorStateRecovery` (`server.go:1433`) after
  `db.recoverIncompleteJobs`: `registerDepGroupMembers(priorJobs)`.

`modified` maps **new key -> old key** at both modify sites, so `job.Key()` is
already the new key and the old one must be read from `modified`. Dropping the
old key is not optional: nothing else removes it, so a key-changing modify
would leave a phantom member keeping `hasMembers(G)` true and wedging G's
waiters forever. `.docs/issue-197/spec.md` makes `cmd` and `cwd` modifiable
(only `dep_grps` is "Immutable in v1") and either changes the key, so this is a
live path.

`jobsToQueue` includes archived waiters **resurrected** by
`retrieveDependentJobs`, not only the input jobs, and those carry their own
`DepGroups`. The production dependency-recompute call sites are the recovery
pass's `dependency.go:137` plus the four control-path ones, `server.go:5082`,
`server.go:5225`, `serverCLI.go:1718` and `serverREST.go:2329` - the five A2
replaces. The two membership hook sites above are distinct from them, and sit
earlier in the same functions' call chains.

### Locking

`depGroupMembers` shards both its maps: the group -> members map by group name,
the job -> groups map by job key. A job-key shard is always taken before a
group-name shard, never the reverse. No shard lock is held across a call into
`queue`: `remove`/`replace`/`rekey` return the emptied group names and the
caller then takes the queue mutex once per emptied group. The existing lock
order `queue.mutex -> job -> statusState.mu` is unchanged, and the shard locks
are leaves relative to it.

A single `add`, `replace` or `rekey` also touches one group-name shard **per
declared group**, and those are taken **one at a time and released before the
next** - never two at once. Holding them all and taking them in
`job.DepGroups` order deadlocks against a concurrent operation whose group list
is ordered differently. No cross-group atomicity is needed, because each
group's emptiness is independent of every other group's. `rekey` needs
per-group atomicity and gets it inside one acquisition: for each group both
keys belong to, it adds `newKey` and drops `oldKey` under that group's single
shard hold, so no other goroutine can observe the group between them.

`rekey` is the only operation that touches **two** job-key shards, so those
need an order of their own: when `oldKey` and `newKey` fall in different
shards, take them in **ascending shard index**, and when they fall in the same
shard take it once. Without that, two concurrent opposing rekeys - `a -> b` on
one goroutine and `b -> a` on another - deadlock. Ascending shard index is a
total order over the shards, so any number of concurrent rekeys is safe. A1
acceptance test 12 exercises exactly that pair; test 10 covers concurrent add
and remove, and is the one that exercises the group-name order above.

This satisfies DEVELOPERS.md rule 2: the archive/delete/add transition path
takes a per-shard mutex, never a server-wide exclusive one. The upside is worth
recording: releasing a 150k-waiter group becomes **one** critical section
instead of today's ~19k separate `promoteDependants` passes over 150k waiters
each.

### Quality gates

- `make lint` at 0 issues.
- `unset $(env | grep -o '^OS_[A-Z_]*' | tr '\n' ' '); timeout 1800 make test`
  at the baseline measured at the branch point `8b9ba00`, not the older 413
  passed / 9 skipped / 29 packages recorded at `bf53de0` (F4 item 4).
- `unset $(env | grep -o '^OS_[A-Z_]*' | tr '\n' ' '); timeout 2400 make race`
  with 0 data races.
- The `developers/wrdev.sh dep-granularity-check` scale gate (F3), proven to
  FAIL from a pristine worktree at the pre-fix commit.
- New source files carry the go-conventions copyright header; GoConvey `So()`
  assertions only; tests guard with `if runnermode || servermode { return }`.

A real-LSF Tier-B run is not required: memory reproduces in-process and the
figures above are memory figures, so an LSF run adds little to a memory claim
and costs days production does not have. The prod restart is the final evidence.

---

## A. Group-granularity dependency state in `jobqueue`

### A1: Per-group live-member sets

As the manager, I want to know which dep groups still have live member jobs from
memory rather than from a `bucketDTK` prefix scan, so that resolving a dep-group
dependency costs one map lookup instead of one string per member.

Implement `depGroupMembers` (Architecture) with sharded group -> members and job
-> groups maps. Every operation is idempotent: adding a job twice to a group
leaves one entry, removing an absent job is a no-op, and `replace` with an
unchanged group list returns no emptied groups.

`rekey` records the new key before dropping the old one, and does both under
one hold of each affected group's shard. The reverse order would empty a group
whose only member is being renamed, and an emptied group releases its waiters -
the silent wrong-order execution this spec exists to prevent. It also takes its
two job-key shards in ascending shard index, and its group-name shards one at a
time (Locking); without either rule, concurrent operations deadlock.

`memberships()` exists for the memory gate and for a debug log line; it is inert
observability in the style of `db.archivedDecodes` (`5c75a15`).

**Package:** `jobqueue/`
**File:** `jobqueue/depgroups.go`
**Test file:** `jobqueue/depgroups_test.go`

**Acceptance tests:**

1. Given a fresh `depGroupMembers`, when `add(["g1","g2"], "k1")` runs, then
   `hasMembers("g1") == true`, `hasMembers("g2") == true`, `hasMembers("g3") ==
   false` and `memberships() == 2`.
2. Given `add(["g1"], "k1")` called three times, then `memberships() == 1` and
   `remove("k1")` returns `["g1"]` exactly once; a second `remove("k1")` returns
   an empty slice and `memberships() == 0`.
3. Given `add(["g1"], "k1")` and `add(["g1"], "k2")`, when `remove("k1")` runs,
   then it returns an empty slice and `hasMembers("g1") == true`; when
   `remove("k2")` then runs, it returns `["g1"]` and `hasMembers("g1") ==
   false`.
4. Given `add(["g1","g2"], "k1")`, when `replace("k1", ["g2","g3"])` runs, then
   it returns `["g1"]`, `hasMembers("g1") == false`, `hasMembers("g2") == true`,
   `hasMembers("g3") == true` and `memberships() == 2`.
5. Given `add(["g1"], "k1")`, when `replace("k1", ["g1"])` runs, then it returns
   an empty slice and `memberships() == 1`.
6. Given `add(["g1"], "k1")`, when `replace("k1", nil)` runs, then it returns
   `["g1"]` and `memberships() == 0`.
7. Given `add(["g1"], "old")` as `g1`'s only member, when `rekey("old", "new",
   ["g1"])` runs, then it returns an empty slice, `hasMembers("g1") == true`,
   `memberships() == 1`, and `remove("old")` afterwards returns an empty slice
   (the old key holds nothing). An implementation that drops `old` first
   reports `g1` emptied here, and D2 acceptance test 4 shows what that costs at
   the server level.
8. Given `add(["g1","g2"], "old")`, when `rekey("old", "new", ["g2","g3"])`
   runs, then it returns `["g1"]`, `hasMembers("g1") == false`,
   `hasMembers("g2") == true`, `hasMembers("g3") == true` and `memberships() ==
   2`.
9. Given `add(["g1"], "k1")`, when `rekey("k1", "k1", ["g1"])` runs, then it
   returns an empty slice and `memberships() == 1` (oldKey == newKey behaves as
   `replace`).
10. Given 8 goroutines each adding and removing 500 distinct job keys across 50
    groups for 200 iterations, each `add` naming more than one group and the
    goroutines ordering their group lists differently, when they finish, then
    `memberships() == 0`, no group reports `hasMembers`, and the test is
    `-race` clean. This is the only test that can hit a group-name shard
    deadlock, so it needs the same bounded harness as test 12, with a deadline
    sized to its own workload: close a channel from a goroutine that waits on
    the `sync.WaitGroup` the 8 goroutines report to, `select` between that
    channel and a `time.After(2 * time.Minute)`, and fail on the deadline
    branch naming the deadlock. The bound is 2 minutes rather than test 12's
    10 s because the workload is far larger - 8 x 500 x 200 add/remove pairs is
    roughly 1.6M sharded operations, against test 12's 4,000 rekeys - and this
    is a shared node at load average 85-120 on 8 cores (F4). The deadline is a
    hang detector, not a latency budget: it costs nothing when the test passes,
    and if it ever fires spuriously the answer is a larger bound, not a
    load-matched A/B against a deadlock that is not there. Without it the
    failure is `go test`'s 10-minute package-wide panic. Count failures and
    assert the final count, never `So()` inside the loop.
11. Given `depGroupDependencyKey("g1")`, then it returns `"depgroup:g1"`, and
    a real job key - `(&Job{Cmd: "echo 1"}).Key()`, a 32-character hex string -
    does not carry the `depgroup:` prefix, so no job key can collide with a
    group key. There is no reverse `depGroupFromDependencyKey`: the only caller
    the old `neverSeenDepGroupFromDependencyKey` had was
    `collectIncompleteJobKeys` (`dependency.go:423`), which A2 deletes, and D3
    needs only the forward direction.
12. Given two job keys `ka` and `kb` chosen to land in different job-key
    shards, and 4 goroutines running `rekey(ka, kb, ["g1"])` against 4 running
    `rekey(kb, ka, ["g1"])` for 500 iterations each, when they finish, then all
    8 goroutines returned, `memberships() == 1`, `hasMembers("g1") == true`,
    and the test is `-race` clean. An implementation that locks the two
    job-key shards in call order rather than ascending shard index deadlocks
    here, so this test fails by **hanging** rather than by asserting, and it
    needs the same bounded harness E7 gives its own hanging tests: close a
    channel from a goroutine that waits on the `sync.WaitGroup` the 8
    goroutines report to, `select` between that channel and a `time.After(10 *
    time.Second)`, and fail on the deadline branch naming the deadlock.
    Without it the failure is `go test`'s 10-minute package-wide panic. Count
    failures and assert the final count.

### A2: Dependencies resolve to group keys, not member keys

As the manager, I want a dep-group dependency to resolve to one opaque key per
declared group, so that a job's retained dependency state is proportional to
what the user declared rather than to the group's membership.

Add `Dependencies.dependencyKeys(reader depReader, groups depGroupState)` per
the Architecture resolution rule and replace `incompleteJobKeys` at all five
call sites (`dependency.go:137`, `server.go:5082`, `server.go:5225`,
`serverCLI.go:1718`, `serverREST.go:2329`). Delete
`Dependencies.incompleteJobKeys` itself and the helpers only it uses -
`incompleteJobKeysByDependency`,
`Dependency.incompleteJobKeys`, `incompleteJobKeysWithSeen`,
`Dependency.collectIncompleteJobKeys` and `collectIncompleteJobKeys` - along
with `incompleteDepGroupJobKeys`, `neverSeenDepGroupDependencyPrefix`,
`neverSeenDepGroupDependencyKey` and `neverSeenDepGroupFromDependencyKey`.
`incompleteEssenceJobKeys` stays: `dependencyKeys` uses it for the essence
half. Nothing can keep calling `incompleteJobKeys`, because `depReader` loses
the `retrieveIncompleteJobKeysByDepGroup` it is built on - which is why the
three committed tests that call it are rewritten rather than left alone (C2).
`reader.depGroupsEverSeen` is called only for the declared groups with no live
member, so a job whose groups all have live members opens no read transaction
for the seen check.

That check is per job, so `dependencyKeys` on its own costs one
`bucketDepGroups` get per (job, no-live-member group) pair. That is fine on the
add path, where the pairs are bounded by the waiters in one call, but over a
150k-job recovery pass a single never-seen group would cost 150k gets. Recovery
therefore wraps its reader in a shared `seenDepGroupCache` (C1), which brings
the pass back to one get per distinct such group. `dependencyKeys` itself takes
no cache argument and needs no change to gain this: the cache is a `depReader`
wrapper.

The returned key slice is built fresh per job. `Item.Dependencies()`
(`queue/item.go:206`) returns the live backing slice and `Item.ChangedKey`
(`queue/item.go:229`) mutates it in place, so no slice may be shared between
items - another reason the earlier slice-sharing design was abandoned.

`job.setWaitingForDepGroups` and the `sortedStringSet` ordering of both returned
slices are unchanged.

**Package:** `jobqueue/`
**File:** `jobqueue/dependency.go`
**Test file:** `jobqueue/reliable4_dependency_tx_test.go`

**Acceptance tests** (fixture: group `L` with 2 live members, group `S` seen
with its only member deleted from the live bucket, group `N` never added, live
job with cmd `C`, archived job with cmd `D`; `depGroupMembers` populated from
the fixture's live jobs):

1. Given `Dependencies{NewDepGroupDependency("L")}`, when `dependencyKeys` runs,
   then `deps == []string{"depgroup:L"}` and `waitingForDepGroups ==
   []string{}`.
2. Given `Dependencies{NewDepGroupDependency("S")}`, then `deps == []string{}`
   and `waitingForDepGroups == []string{}`.
3. Given `Dependencies{NewDepGroupDependency("N")}`, then `deps ==
   []string{"depgroup:N"}` and `waitingForDepGroups == []string{"N"}`.
4. Given `Dependencies{NewEssenceDependency(C, "")}`, then `deps` is the single
   job key of `C` and `waitingForDepGroups == []string{}`.
5. Given `Dependencies{NewEssenceDependency(D, "")}`, then `deps == []string{}`.
6. Given `Dependencies{NewDepGroupDependency("L"), NewDepGroupDependency("N"),
   NewEssenceDependency(C, "")}`, then `deps` is the sorted set `{"depgroup:L",
   "depgroup:N", key(C)}` (3 entries) and `waitingForDepGroups ==
   []string{"N"}`.
7. Given a group `B` with 500 live members and `Dependencies{
   NewDepGroupDependency("B")}`, then `len(deps) == 1`; given the same group
   grown to 5,000 live members, then `len(deps)` is still 1 (the count does not
   scale with membership).
8. Given `var none Dependencies` (no dependencies), then `deps == []string{}`,
   `waitingForDepGroups == []string{}` and `db.bolt.Stats().TxN` is unchanged
   over 100 resolutions (the `c96dcbf` early return is preserved).

### A3: `bucketDTK` stops being written

As an operator upgrading a production DB, I want the retired index to stop
consuming write bandwidth on every add while remaining readable by an older
binary, so that the upgrade needs no migration pass and the documented rollback
still has stale edges to fall back on.

Drop `dgLookups` from `prepareNewJobs`, `storeNewJobData`, `newJobLookups` and
`putAllLookups`. Keep the bucket created by `initDB`, keep it in
`indexedLookupBuckets()` and `isIndexedLookupBucket`, and keep
`rebuildDepGroups` and its `openedExistingDB && !hadDepGroups` trigger exactly
as they are.

Two committed `jobqueue/db_test.go` tests are pinned to `bucketDTK` being
written and must be updated rather than deleted or weakened.

`TestDBReverseLookupIndex` (`db_test.go:292`) is the only coverage of
lookup-entry bookkeeping across a key-changing modify. Four of its assertions
change, and only three of them are counts:

- `So(parentLookups, ShouldEqual, 2)` (`:326`) becomes 1. The parent holds a
  repgroup entry and a dep-group entry; only the dep-group one goes.
- `So(childLookups, ShouldEqual, 2)` (`:327`) is **unchanged**. The child's two
  entries are a repgroup entry and a *reverse* dep-group (`bucketRDTK`) entry,
  and `rdgLookups` keeps being written.
- `So(newDepKeys, ShouldContain, newParentKey)` (`:364`) does not drop by one,
  it **inverts**: `retrieveIncompleteJobKeysByDepGroup("new-parent-dg")`
  returns empty, so it becomes a `ShouldHaveLength, 0` beside the existing
  `oldDepKeys` assertion.
- `countLookupEntriesByJobKey(tx, newParentKey)` and
  `countReverseLookupEntriesByJobKey(tx, newParentKey)` (`:369-370`) both go
  from 2 to 1, since `modifyLiveJobs` writes the new key's lookups through
  `prepareNewJobs`.

`TestDBDepGroups`' second `Convey` (`db_test.go:478`, "Opening an old DB
rebuilds seen dep groups from historical dep group lookups") cannot stay green
unaltered: it builds its `bucketDTK` entries by calling `storeNewJobs` with
`DepGroups: []string{"legacy"}`, and once that write is gone `rebuildDepGroups`
has nothing to rebuild from, so its `depGroupEverSeen("legacy")` assertion
fails. Reframe it to write the legacy `bucketDTK` entry directly, in the
pattern `db_test.go:1391` and `:1480` already use -
`replaceLookupRebuildTestBucket(tx, bucketDTK, []byte("legacy"+dbDelimiter+
jobKey))` - which is the truer fixture anyway, since those entries are
pre-upgrade data by definition. Its assertions (`depGroupEverSeen("legacy") ==
true`, `("absent") == false`) stay exactly as they are. Its first `Convey`, on
`prepareNewJobs`' `depGroupsSeen`, is untouched: `depGroupsSeen` is not
`dgLookups` and keeps being written.

**Package:** `jobqueue/`
**File:** `jobqueue/db.go`
**Test file:** `jobqueue/db_test.go`, `jobqueue/depgroups_test.go`

**Acceptance tests:**

1. Given a fresh DB, when a job with `DepGroups: ["g1"]` is stored via
   `storeNewJobs`, then `bucketDTK` holds no key with prefix `g1_::_`, while
   `bucketDepGroups` holds `g1` and `bucketRDTK` is unchanged in shape for a job
   that declares a dep-group dependency.
2. A regression guard: given a DB written by the pre-change code (a committed
   or generated fixture with `bucketDTK` populated), when it is opened by
   `initDB`, then no rebuild runs (`db.upgradedOnOpen == false`), the
   pre-existing `bucketDTK` keys are still present, and every live job's
   dependencies resolve correctly through `dependencyKeys`. The no-rebuild and
   key-preservation halves hold identically pre-change - the rebuild trigger
   and the bucket's contents are untouched by this work - so only the
   resolution call is new. It is the story's whole upgrade claim, so it is
   asserted rather than assumed.
3. A regression guard that already holds pre-change: given a DB whose
   `bucketDTK` holds pre-upgrade entries written directly with
   `replaceLookupRebuildTestBucket`, when `bucketDepGroups` is deleted and the
   DB reopened, then `rebuildDepGroups` runs and rebuilds `bucketDepGroups`
   from those stale entries exactly as before: `depGroupEverSeen("legacy") ==
   true` and `depGroupEverSeen("absent") == false`. Writing the bucket directly
   is what makes it hold either way, since the `openedExistingDB &&
   !hadDepGroups` trigger (`db.go:1021`) and `rebuildDepGroups` (`db.go:4298`)
   are both untouched by this work. This is `TestDBDepGroups`' second `Convey`,
   reframed off `storeNewJobs` (F4 item 3).
4. A regression guard that already holds pre-change: given a job stored with
   `DepGroups: ["g1"]` and then modified via `modifyLiveJobs` so its key
   changes, then `deleteLookupEntriesForJobKey` returns no error and no orphan
   reverse entry for the old key remains
   (`countReverseLookupEntriesByJobKey(tx, oldKey) == 0`). It guards the
   "leave `bucketDTK` in place, do not delete it" decision: deleting the bucket
   is what would make this path return `ErrBucketNotFound`.
5. `TestDBReverseLookupIndex`, updated, passes: a parent in one dep group has 1
   lookup entry and a child depending on that group still has 2; after the
   key-changing modify, the new parent key has 1 lookup entry and 1 reverse
   entry, the old key has 0 of each, and
   `retrieveIncompleteJobKeysByDepGroup("new-parent-dg")` is empty.

---

## B. The `queue` package

### B1: Satisfy a dependency key that has no backing item

As the manager, I want to tell the queue that an opaque dependency key is now
satisfied without removing an item, so that a dep-group edge can be released in
one critical section when the group's last live member leaves.

Add `Queue.SatisfyDependency(ctx, key)`. It is `Remove`'s dependant half: take
`queue.mutex`, error with `ErrQueueClosed` if closed, call
`promoteDependants(key)`, `changed(SubQueueDependent, SubQueueReady, ...)` if
anything was promoted, unlock, then `readyAdded(ctx, "dependent")`. It returns
nil when the key has no dependants.

**Package:** `queue/`
**File:** `queue/queue.go`
**Test file:** `queue/queue_test.go`

**Acceptance tests:**

1. Given three items added with `Dependencies: []string{"depgroup:g1"}` and no
   item keyed `depgroup:g1`, then all three are in `ItemStateDependent` and
   `Stats().Dependant == 3`; when `SatisfyDependency(ctx, "depgroup:g1")` runs,
   then it returns nil, all three reach `ItemStateReady`, `Stats().Ready == 3`,
   `Stats().Dependant == 0`, and the ready-added callback fired.
2. Given an item depending on both `depgroup:g1` and `depgroup:g2`, when
   `SatisfyDependency(ctx, "depgroup:g1")` runs, then the item is still
   `ItemStateDependent` and `UnresolvedDependencies()` is
   `[]string{"depgroup:g2"}`; when `SatisfyDependency(ctx, "depgroup:g2")` then
   runs, the item is `ItemStateReady`.
3. Given `SatisfyDependency(ctx, "depgroup:nobody")` on a key with no
   dependants, then it returns nil and `Stats()` is unchanged.
4. Given a destroyed queue, when `SatisfyDependency` is called, then it returns
   an error whose `Err` is `ErrQueueClosed`.
5. Given `SatisfyDependency(ctx, "depgroup:g1")` called twice, then the second
   call returns nil and no item transitions twice (the changed callback records
   one dependent -> ready transition in total).

### B2: The two existence proxies stop asking for a backing item

As the manager, I want `Kick` and dependency pruning to test remaining
dependencies rather than the existence of a depended-on item, so that a group
key which never has an item behaves like any other dependency.

Two sites test `queue.items[dep]` and both break for group keys:

- `itemHasDeps` (`queue/queue.go:802`, sole caller `Kick` at `:1599`): change to
  `len(item.UnresolvedDependencies()) > 0`. Without this, a kicked buried job
  with an unsatisfied group dependency would go straight to ready. The path is
  reachable: a job is buried, a new member is added to its dep group, and
  `updateJobDependencies` -> `q.Update` re-blocks it while
  `detachForDependentMove` leaves buried items put. The change also makes `Kick`
  agree with `kickJobs`' `readyCallbackExpected`, which already uses
  `UnresolvedDependencies()` (`server.go:5721`).
- `pruneDependants` (`queue/queue.go:1126`, sole caller `updateDependencies` at
  `:1075`): drop the `queue.items[dep]` guard so a dropped group dependency's
  waiter entry is actually pruned. Today those entries leak for any non-existent
  parent key.

`resumeSuspendedItem` (`queue/queue.go:236`) already tests
`UnresolvedDependencies()` and needs no change. These two are the only such
proxies in the package.

**Package:** `queue/`
**File:** `queue/queue.go`
**Test file:** `queue/queue_test.go`

**Acceptance tests:**

1. `queue_test.go:1478-1501` (inside `TestQueue`, "You can update dependencies")
   stays green unchanged: bury item five, `Update` it with dependency `key1`
   which **has** a backing item, `Kick` -> `ItemStateDependent`, `Remove(key1)`
   -> `ItemStateReady`.
2. Given an item buried and then `Update`d with `Dependencies:
   []string{"depgroup:g1"}`, where no item is keyed `depgroup:g1`, when `Kick`
   runs, then the item is in `ItemStateDependent` and `Stats().Dependant == 1`
   (pre-change it would be `ItemStateReady`); when `SatisfyDependency(ctx,
   "depgroup:g1")` then runs, it becomes `ItemStateReady`.
3. Given an item added with `Dependencies: []string{"depgroup:g1"}`, when it is
   `Update`d with `Dependencies: []string{"depgroup:g2"}`, then
   `HasDependents("depgroup:g1") == false` and `HasDependents("depgroup:g2") ==
   true`, the item is still `ItemStateDependent`, and `SatisfyDependency(ctx,
   "depgroup:g2")` then makes it `ItemStateReady`. Assert the dependants map,
   and assert it before satisfying anything. An item-state assertion here
   cannot fail pre-change: `Update` has already rebuilt `remainingDeps` to
   `{depgroup:g2}` (`queue/item.go:256`), so the stale `g1` entry's
   `resolveDependency` (`:272`) deletes nothing and the item stays dependent
   either way. And `promoteDependants` deletes the whole `dependants[key]`
   entry (`queue/queue.go:1684`), so a `HasDependents` check after a
   `SatisfyDependency` would pass pre-change too.
4. Given an item added with `Dependencies: []string{"depgroup:g1"}` and then
   `Update`d with no dependencies, then `HasDependents("depgroup:g1") == false`.

### B3: Package documentation

As a maintainer, I want the package doc to describe the contract the package
actually offers, so that the next reader does not conclude a dependency can only
clear via `Remove`.

Amend `queue/queue.go:44-47` and the `Add` doc at `:616-619`: a dependency
clears when an item with that key is `Remove()`d **or** when the key is passed
to `SatisfyDependency()`, and a dependency key need not ever name an item.
`Queue.ChangeKey` stays an O(all items) walk per rename, unchanged by this work;
`Item.ChangedKey` gets cheaper because only essence edges are job keys and group
names are never renamed.

**Package:** `queue/`
**File:** `queue/queue.go`
**Test file:** `queue/queue_test.go` (acceptance test 2; test 1 is a lint and
  doc-comment check, not a Go test)

**Acceptance tests:**

1. Given `go vet ./queue/` and `make lint`, then both are clean and the amended
   doc comment mentions `SatisfyDependency`.
2. A regression guard that already holds pre-change, not a pre-fix failure:
   given an item depending on `depgroup:g1` and an item keyed `k1` in the
   queue, when `ChangeKey("k1", "k2")` runs, then the dependent item's
   `UnresolvedDependencies()` is still `[]string{"depgroup:g1"}`.
   `Item.ChangedKey` (`queue/item.go:229-249`) rewrites a dependency only when
   it equals `old`, so a group name was never rewritten by a rename either way.
   The guard is against an implementation that starts rewriting dependency keys
   wholesale.

---

## C. Recovery

### C1: Build per-group state from the decoded live jobs

As an operator restarting the manager, I want recovery to resolve dependencies
without a per-job dep-group database lookup, so that its memory is linear in the
live jobs rather than quadratic in group membership.

In `startPriorStateRecovery` (`server.go:1433`), after
`db.recoverIncompleteJobs` returns and before `setRecoveryTotal`, call
`s.registerDepGroupMembers(priorJobs)`. That pass reads only `job.DepGroups` on
jobs already decoded, so it needs no transaction of its own and must not depend
on the live-bucket decode being a single transaction
(`.docs/bugfixes/260825-3.md` item 3 proposes chunking it; nothing is served and
there are no concurrent writers, so either shape is safe here).

`db.resolveDependencies` keeps its **per-chunk** transaction property
(`dependencyResolutionChunkSize = 1000`) and its `ctx` check per job. Do not
restore a whole-pass transaction: a read transaction holds `mmaplock.RLock()`
for its life, and a writer that must grow the mapping takes `mmaplock.Lock()`
while holding `db.rwlock`, stalling every write database-wide for the length of
a 21-43 minute recovery. `resolveDependencyChunk` gains the `depGroupState`
and `*seenDepGroupCache` arguments and otherwise keeps its shape, including
that only database reads and `setWaitingForDepGroups` happen inside the
transaction.

`resolveDependencies` also creates one `seenDepGroupCache` for the whole pass
and wraps each chunk's `txDepReader` in it, so the "ever seen" answer for a
group is read once however many jobs name it. Without that, the check is per
job (A2) and 150k jobs naming one never-seen group would cost 150k gets, which
is the bound the Architecture states. The cache lives across chunks while the
reader it wraps is rebuilt per chunk; that is safe because `bucketDepGroups` is
only ever added to and nothing is served during recovery (E1), so no group can
become seen mid-pass. The cache counts the reads it makes into
`db.depGroupSeenGets`, which `resolveDependencies` hands to
`newSeenDepGroupCache`: a cache built inside the pass has nothing that exposes
it, and a counter on the `*db` is the house pattern (`db.archivedDecodes`,
`db.go:682`) and gives the test a before/after delta exactly as
`db.bolt.Stats().TxN` does.

The single-batch enqueue (`recoverPriorJobs` -> `enqueueItems`, which resolves
dependencies within one `AddMany` batch), the `recoveryPauseHook` seam and the
recovery `ctx` cancellation are unchanged.

`AddMany` already ignores `StartQueue` for a deps-bearing item except when
`StartQueue == SubQueueSuspended` (`queue/queue.go:890-907`), so a recovered
**running** or **buried** job whose group has a live member lands in the
dependent sub-queue while a recovered **suspended** one stays suspended with its
deps set. That is pre-existing behaviour; recovery tests must not "fix" it.

**Package:** `jobqueue/`
**File:** `jobqueue/server.go`, `jobqueue/dependency.go`
**Test file:** `jobqueue/depgranularity_recovery_test.go`

**Acceptance tests.** Tests 1, 2 and 8 fail pre-change: they assert the new key
shape, the new membership count and the new shared-cache read count. Tests 3-7
are regression guards that already hold pre-change - 3, 4, 5 and 6 pin the
blocked/not-blocked partition, which the Architecture's resolution rule keeps
identical under both models, and 7 is already pinned by `c96dcbf`. They are
here because this story rewrites the code that produces all of it.

1. Given a DB with a group `G` of M live members and W live waiters on `G` (M =
   200, W = 500), when a server recovers it, then every waiter's queue item has
   exactly one dependency, `"depgroup:G"`, and `sum(len(item.Dependencies()))`
   over all waiters is `W`, not `W*M`.
2. Given the same DB, when recovery finishes, then `s.depGroups.memberships()`
   equals the total number of (group, member) pairs among the live jobs (M plus
   any other group memberships in the fixture), independent of W.
3. Given the same DB, when recovery finishes, then all W waiters are in the
   dependent sub-queue (`s.q.Stats().Dependant == W`) and the M members are
   ready; when all M members are archived, then all W waiters become ready.
4. Given a DB whose only live job depends on a group that was seen but has no
   live member, when recovery finishes, then that job is ready and its
   `WaitingForDepGroups` is empty.
5. Given a DB whose only live job depends on a never-added group, when recovery
   finishes, then that job is in the dependent sub-queue and its
   `WaitingForDepGroups` names that group.
6. Given a DB with 3,000 live jobs where every job both belongs to and depends
   on a chain of dep groups, when recovery finishes, then the recovered queue's
   per-state counts equal the pre-restart counts exactly, with no lost or
   duplicated keys, and the test is `-race` clean.
7. Given `dependencyResolutionChunkSize` lowered to 10 and 100 recovered jobs,
   when the recovery dependency pass runs, then `db.bolt.Stats().TxN` grows by
   exactly 10 (one per chunk) - not 1, and not 100.
8. Given 500 recovered jobs that all depend on the same never-seen group `N`,
   and `dependencyResolutionChunkSize` lowered to 10 so the pass spans 50
   chunks, when the pass runs, then `db.depGroupSeenGets` grows by exactly 1,
   all 500 jobs get `deps == []string{"depgroup:N"}`, and all 500 have
   `WaitingForDepGroups == []string{"N"}`. Without the shared cache the delta
   is 500, and without the pass-long lifetime it is 50.

### C2: Rewrite the committed transaction-cost tests

As a maintainer, I want the transaction-cost regression suite to keep asserting
transaction counts while asserting the new key shape, so that neither property
can regress unnoticed.

The three tests in `jobqueue/reliable4_dependency_tx_test.go` that resolve
dependencies are rewritten: `TestReliable4DependencyFreeTxCost` (`:144`),
`TestReliable4DependencyResolutionUnchanged` (`:198`) and
`TestReliable4RecoveryDependencyPass` (`:291`). The fourth,
`TestReliable4RecoveryDependencyState` (`:521`), calls neither
`incompleteJobKeys` nor `resolveDependencies`, so it only has to keep passing
(test 5). None of the four is deleted and none has an assertion weakened. Three
things force the rewrite:

- Every `Dependencies.incompleteJobKeys` call in the file (`:165`, `:182`,
  `:215-277`, `:312`, `:463`) becomes `dependencyKeys(reader, groups)`, because
  A2 deletes that function and `depReader` loses the
  `retrieveIncompleteJobKeysByDepGroup` it was built on.
- Every `db.resolveDependencies` call (`:320`, `:336`, `:370`, `:397`, `:471`)
  gains the `depGroupState` argument.
- `newDepTxFixture` builds a bare `*db` with no `Server`, so it must also build
  a `depGroupMembers` from its live jobs - `depTxLiveGroup` holding `first` and
  `second` and nothing else, since `gone` was deleted from the live bucket -
  and hand it to both sides. `depTxSoResolvesAsPerJob` (`:457-471`) is a valid
  comparison only when the per-job side and the pass side resolve against the
  same one. `depTxFixture.liveGroupKeys` stops being an expected value and
  becomes the fixture's source of that group's members, so its doc comment
  ("sorted as `incompleteJobKeys` returns them") goes with it, along with its
  two member-key expansion assertions (`:226`, `:273`).

What those tests assert survives. `TestReliable4DependencyFreeTxCost` keeps both
counts. `TestReliable4RecoveryDependencyPass` keeps its per-chunk shape,
including the "exactly one transaction per chunk" form - "exactly one for the
whole pass" was rejected in review and must not return.
`TestReliable4DependencyResolutionUnchanged`'s key-level assertions are the one
thing this work deliberately changes, so they are rewritten per A2.

One assertion is actively falsified and needs a replacement rather than a
rewording. `So(perJobTx, ShouldBeGreaterThan, len(jobs))` (`:326`) says per-job
resolution costs more than one transaction per job. Under the new model, per-job
resolution of `depTxRecoveryJobs`' seven kinds costs 0 for no dependencies, 0
for a group with live members (answered from `hasMembers`, with no seen check),
and 1 each for the other five - the seen check for a group with no live member,
or the `checkIfLive` for an essence dependency. Over `depTxResolutions = 100`
jobs that is 70 transactions, so the old assertion reads `70 > 100` and fails.
Replace it with that shape stated directly, which is the property worth pinning:
a dep-group dependency whose group has a live member costs no database read at
all.

`TestReliable4RecoveryDependencyState`'s state strings (`ready waiting:`,
`dependent waiting:`, `dependent waiting:reliable4-deptx-e2e-future`) are
unchanged, because `WaitingForDepGroups` semantics are unchanged. Both its
`Connect` calls survive E1 as they stand: the first (`:538`) is against the
first, non-paused server, which the `serve` helper now waits on (E2), and the
second (`:562`) already follows `release()` and `waitUntilRecovered`
(`:557-558`), which E1's publish-before-`finishRecovering` order makes
race-free.

**Package:** `jobqueue/`
**File:** none (test-only)
**Test file:** `jobqueue/reliable4_dependency_tx_test.go`

**Acceptance tests.** Tests 3 and 4 fail pre-change: the 70-transaction shape
and the new key assertions are what this work introduces. Tests 1, 2 and 5 are
regression guards - their counts and state strings are unchanged, and the whole
point of the story is that they survive the rewrite.

1. `TestReliable4DependencyFreeTxCost`, rewritten onto `dependencyKeys`, keeps
   both counts: `TxN` grows by 0 over 100 resolutions of a job with no
   dependencies, and by exactly 100 over 100 essence-only resolutions.
2. `TestReliable4RecoveryDependencyPass`, rewritten onto `dependencyKeys` and
   the new `resolveDependencies` signature, keeps `passTx ==
   depTxWantChunks(len(jobs))` at `depTxChunkSize`, `wantChunks > 1`,
   `wantChunks < len(jobs)`, and `Stats().OpenTxN == 0` after a cancelled pass.
3. In the same test, per-job resolution of the 100 `depTxRecoveryJobs` costs
   exactly 70 transactions - one for each job that names a group with no live
   member or carries an essence dependency, none for the 30 that have no
   dependencies or name only `depTxLiveGroup` - while the pass over the same
   jobs costs `depTxWantChunks(len(jobs))`.
4. `TestReliable4DependencyResolutionUnchanged` asserts the A2 key shapes and no
   longer references `neverSeenDepGroupDependencyKey` (the symbol is gone).
5. `TestReliable4RecoveryDependencyState` passes with `recoveryTx < len(jobs)`
   and its three job-state strings unchanged.

---

## D. The add, modify, remove and kick control paths

### D1: Adding to a dep group is linear in the waiters

As a user adding a job into a dep group that many jobs already wait on, I want
the add to cost one dependency key per waiter rather than one per waiter per
member, so that `wr add` does not kill the manager.

Update the group state for every job in `storeNewJobs`' `jobsToQueue`
**before** `itemDefsForNewJobs` resolves anything, so that a member and a
dependent added in the same batch see each other exactly as they do today
(where `storeNewJobs` writes the index before resolution). Then
`itemDefsForNewJobs` (`server.go:5069`) and `gatherDependencyUpdates`
(`server.go:5220`) call `dependencyKeys`.

That update is **two passes**, and the order is load-bearing:
`registerDepGroupMembers(jobsToQueue)` records every job's declared groups
first, then `replaceDepGroupMembership(ctx, job.Key(), job.DepGroups)` per job
applies the drops and satisfies the groups left empty. One `replace` pass would
release G's waiters early whenever a batch both drops G from one job (test 5's
`--rerun`) and adds another member of G: the drop lands first, G momentarily has
no member, and its waiters go ready ahead of the new member. This is the add
path's version of what `rekey`'s new-before-old ordering does for one job in D2.
After the register pass, no group named anywhere in the batch is empty, so the
drop pass can only empty groups the batch no longer declares at all.
`registerDepGroupMembers` is therefore shared with recovery's bulk rebuild (C1)
rather than being recovery-only; it only adds, so it releases nothing on
either path.

The modify path needs no batch equivalent: one `JobModifier` applies the same
`DepGroups` to every job in the call (`applyGrouping`, `job.go:2016`), so no job
in a modify batch can leave a group that another job in the same batch joins.

`warnings.NeverSeenDepGroups` is still built from the `waitingForDepGroups` of
the originally input jobs and is unchanged.

`retrieveDependentJobs` is preserved byte-for-byte, including its all-time
transitive waiter scan and its resurrect-and-rerun of archived waiters. It is
O(all-time waiters), not O(waiters x members), so it is not the OOM; it is
recorded as a follow-up (G1).

`updateJobDependencies`' doc comment is stale - it names `storeNewJobs()` and
`db.modifyLiveJobs()` as its sources, but `modifyLiveJobs` discards
`prepareNewJobs`' `jobsToQueue`/`jobsToUpdate` (the `//nolint:dogsled` line), so
the modify path never refreshes a group's waiters through it. Correct the
comment to name its one production caller, `queueNewJobItems`
(`server.go:5111`), and the one source it is really given, `storeNewJobs`'
`jobsToUpdate`.

**Package:** `jobqueue/`
**File:** `jobqueue/server.go`
**Test file:** `jobqueue/depgranularity_add_test.go`

**Acceptance tests:**

1. Given a live group `G` with 200 members and 500 live waiters on `G`, when one
   more job is added to `G` via `Client.Add`, then every waiter's item has
   exactly one dependency (`"depgroup:G"`), the total dependency-key count
   across all waiters is 500, and `s.depGroups.memberships()` grew by exactly 1.
2. A regression guard that already holds pre-change, not a pre-fix failure:
   repeat the setup with 200 and with 2,000 members, and the 2,000-member add's
   `db.bolt.Stats().TxN` delta is no more than 1.25x the 200-member one.
   `retrieveIncompleteJobKeysByDepGroup` (`db.go:3036`) opens exactly one `View`
   per waiter whatever the membership, so the pre-fix ratio is already ~1.0: the
   quadratic cost there is allocated strings, not transactions. Keep the guard
   against an implementation that reads per member; D1's failing-pre-fix
   evidence is test 1's key count, 500 against 100,000.
3. A regression guard that already holds pre-change, not a pre-fix failure:
   given a batch containing both a member of new group `H` and a job depending
   on `H`, when they are added in one `Client.Add` call in either order, then
   the dependent job is in the dependent sub-queue and the member is ready; when
   the member completes, the dependent becomes ready. Today `storeNewJobs`
   writes the `bucketDTK` index before resolution, so the two already see each
   other; the guard is that the two-pass update preserves that.
4. A regression guard that already holds pre-change, not a pre-fix failure:
   given a job depending on never-seen group `N`, when a member of `N` is later
   added, then the dependent job is still blocked and its `WaitingForDepGroups`
   is now empty; when that member completes, the dependent becomes ready. This
   is the **add** path, which unblocks correctly today: `retrieveDependentJobs`
   finds the waiter through `bucketRDTK` and `updateJobDependencies` ->
   `q.Update` replaces the sentinel key with the new member's key. The wedge is
   on the **modify** path, which D2 acceptance test 3 pins.
5. Given a job in group `G` with a waiter, when the job is added again with
   `--rerun` (`ignoreComplete == false`) declaring `DepGroups: []` instead of
   `["G"]`, then `G` has no live member and the waiter is released **at add
   time**. This is a documented consequence, not a bug: `wr add --rerun` of a
   live job Puts a new live record without deleting the old lookups
   (`prepareNewJobs` never checks the live bucket, `jobsNotAlreadyQueued`
   filters live duplicates only when `ignoreComplete` is true, and
   `deleteLookupEntriesForJobKey` is not called on this path), so a rebuild from
   the decoded record would release those waiters at the next restart
   regardless. Matching it at add time keeps the running manager and its own
   restart in agreement.
6. Given live group `G` whose only member is job J, and a live waiter W on `G`,
   when one `Client.Add` call carries both J re-added with `--rerun` and no
   `DepGroups` (dropping `G`) and a new job declaring `DepGroups: ["G"]`, then W
   is still in the dependent sub-queue, `s.depGroups.hasMembers("G") == true`
   with the new job as `G`'s only member, and W becomes ready only once that new
   job completes. List the drop first in the batch, which is the order a
   single-pass `replace` gets wrong.

### D2: Modify maintains the group sets

As a Go client modifying a job's dep groups at runtime, I want the manager's
per-group state to follow, so that a group's waiters are never wedged forever.

`wr mod --dep_grps` **does not exist**: the flag wiring is commented out in
`cmd/mod.go:172`, `JobModifyViaJSON` has no `dep_grps` field, and
`JobModifier.SetDepGroups` (`job.go:1753`) has no production caller. The path is
reachable only by a Go consumer hand-building a `JobModifier`. Maintain the
group sets on it anyway and pin it through the Go client API, so the seam is
correct for whoever re-enables the flag. Re-enabling that flag is out of scope
(G4), though the "too complex" reason recorded in `cmd/mod.go` was precisely the
member-key rewriting this design removes.

Hook both modify call sites with `rekeyDepGroupMembership(ctx, oldKey,
job.Key(), job.DepGroups)` per job, immediately after `db.modifyLiveJobs`
succeeds and **before** the `DependenciesSet || PrioritySet` guard, which a
`DepGroups`-only modification never passes. The Architecture's Membership hook
set gives the exact regions, the source of `oldKey` at each, and why the old
key must be dropped.

`rekey`, not `replace`, for two reasons. A modify can change the key, and a
`replace` on the new key alone would wedge the group on a phantom old member
(acceptance test 4). And `rekey`'s ordering - new key recorded before old key
dropped - is what stops a group that survives the rename from transiently
emptying and releasing its waiters early.

This changes **when** waiters are released. Today, removing job J from group G
leaves G's waiters blocked on J's key until J completes, because
`reflectModifiedJobInQueue` is not called for a `DepGroups`-only modification.
Under group granularity, if removing J empties G then G's waiters are released
at modify time. That is the honest reading of "G has no incomplete members". The
mirror case also holds: adding a member by modification **extends** a
currently-blocked waiter's wait.

**Package:** `jobqueue/`
**File:** `jobqueue/serverCLI.go`, `jobqueue/serverREST.go`
**Test file:** `jobqueue/depgranularity_add_test.go`

**Acceptance tests:**

1. Given group `G` whose only live member is job J, and a live job W depending
   on `G`, when J is modified via a `JobModifier` with
   `SetDepGroups([]string{"other"})`, then W becomes ready at modify time and
   `s.depGroups.hasMembers("G") == false`.
2. Given group `G` with two live members J1 and J2 and a waiter W, when J1 is
   modified out of `G`, then W is still in the dependent sub-queue; when J2 is
   also modified out, then W becomes ready.
3. Given a waiter W blocked on never-seen group `G` - the only way a waiter is
   blocked on a group with no live member under either model - when a live job
   is modified **into** `G`, then W is still blocked and becomes ready only
   once that job completes. This is a pre-fix failure, not a guard: today W is
   wedged forever, because `modifyLiveJobs` discards `prepareNewJobs`'
   `jobsToQueue`/`jobsToUpdate` so nothing refreshes W's dependencies and
   nothing removes its never-seen sentinel key. Group granularity removes that
   wedge as a side effect.
4. Given group `G` whose only live member is job J, and a waiter W, when J is
   modified so that its key changes (a new `Cmd`) while its `DepGroups` stay
   `["G"]`, then `s.depGroups` lists the **new** key as `G`'s member and not
   the old one, `hasMembers("G") == true`, `memberships()` is 1 (no phantom),
   and W is still in the dependent sub-queue - it is neither released at modify
   time nor wedged: completing the modified J releases it.
5. Given the same setup driven through the REST path's `storeModifiedJobs`
   in-package (the technique E6 uses for `getij`, so no HTTP harness is
   needed), with a `JobModifier` that sets neither dependencies nor priority,
   then the same holds - which is the assertion that the hook sits above the
   `DependenciesSet || PrioritySet` guard rather than inside it.

### D3: `wr remove`'s dependant guard is re-derived

As a user deleting jobs, I want a job that other jobs depend on through a dep
group to be skipped exactly as it is today, so that removing it does not release
its dependants early.

`removeDeletableJobs` (`server.go:5557`) skips a job while
`s.q.HasDependents(jobKey)` is true, because `queue.Remove` **satisfies**
dependants. With group edges a member's key is no longer in `dependants`, so the
guard must also ask whether any of the job's `DepGroups` has waiters:
`s.q.HasDependents(depGroupDependencyKey(g))` for each `g` in `job.DepGroups`,
in addition to the existing check on the job key (which still covers essence
dependants). Everything else in `removeDeletableJobs` and in `deleteJobs`'
skip-and-walk-the-tree loop is unchanged.

This behaviour is unpinned at the `jobqueue` level today: `queue_test.go` pins
`Queue.HasDependents` itself, which does not change, but no jobqueue, REST or
CLI test deletes a dep-group parent and asserts it is skipped, or deletes parent
and child together and asserts both go. Add that coverage.

**Package:** `jobqueue/`
**File:** `jobqueue/server.go`
**Test file:** `jobqueue/depgranularity_remove_test.go`

**Acceptance tests.** Every skip-or-delete outcome below already holds
pre-change: today C's item depends on P's own job key, so
`HasDependents(jobKey)` answers correctly and the skip-and-walk loop already
behaves this way. Only tests 2 and 4 name anything new. So these are
regression guards against the pre-fix commit, and the gates that fail if the
group edges land without the re-derived guard, which is the failure D3 exists
to catch - it has no pre-fix failure of its own to prove.

1. Given live job P in group `G` and live job C depending on `G`, when
   `Client.Delete` is asked to remove only P, then nothing is deleted, P is
   still in the queue, and C is still in the dependent sub-queue.
2. Given the same P and C, when `Client.Delete` is asked to remove both in one
   call (C listed first), then both are deleted, `s.q.Stats().Items == 0` and
   `s.depGroups.hasMembers("G") == false`.
3. Given the same P and C, when they are supplied in the other order (P first),
   then both are still deleted (the skip-and-walk loop retries the skipped
   parent).
4. Given live job P in group `G` with no waiters at all, when P is deleted, then
   it is removed and `SatisfyDependency("depgroup:G")` having no dependants
   causes no error.
5. Given live jobs P1 and P2 both in group `G` and a waiter C, when P1 is
   deleted alone, then P1 is skipped (G still has a waiter) - matching today's
   behaviour, where P1's key has a dependant.

### D4: `Kick` reports the sub-queue it landed in

As a user kicking a buried job that still has an unsatisfied dep-group
dependency, I want it to go back to waiting rather than to ready, so that it
does not run ahead of its parents.

`kickJobs` (`server.go:5712`) sets `State = JobStateReady` whenever `q.Kick`
succeeds, including when `Kick` routed the item to the dependent sub-queue; the
state a client sees comes from `itemToJob` deriving it from the sub-queue. So
the pinning test must assert the **sub-queue**, not `job.State` immediately
after the kick.

**Package:** `jobqueue/`
**File:** none (B2 carries the code change)
**Test file:** `jobqueue/depgranularity_remove_test.go`

**Acceptance tests.** Neither fails at the pre-fix commit. In test 1 B has no
dependencies at all, so `itemHasDeps` is false under either model; in test 2
B's pre-change dependency is a member job key that does have a backing item, so
`itemHasDeps` already routes it to the dependent sub-queue. Both are regression
guards against that commit, and the gates that fail if the group edges land
without B2's `itemHasDeps` change.

1. Given a buried job B whose dep group `G` has no live member (so B has no
   dependencies), when B is kicked, then it reaches the ready sub-queue and is
   reservable.
2. Given a buried job B depending on group `G`, and a new member of `G` added
   afterwards so `updateJobDependencies` re-blocks B while leaving it buried,
   when B is kicked, then its queue item is in `ItemStateDependent` and
   `s.q.Stats().Dependant` includes it; when the new member completes, B becomes
   ready.

---

## E. Startup: invisible until recovery completes

Every story here except E7 describes, survives or measures a consequence of E1,
so its acceptance tests are proved against a tree with E1 landed rather than
against the pre-fix commit. Several of them need no RED run of their own,
because the startup window they exercise does not exist at that commit, and a
reviewer applying "prove every gate FAILs pre-fix" should not chase them: E1
acceptance tests 6 and 7 (the fast-fail certificate paths, which `Serve`
already returns today); all three E3 tests (`serveWebInterface` and
`serveClients` already run before recovery, so there is nothing to nil-deref
and nothing to wait for); E4 acceptance test 6 (a brand-new DB writes no
sidecar today, so the file is already absent after a failed start); E5
acceptance tests 3 and 4 (`currentManagerDBUpgradeStatus` already rejects a
sidecar whose recorded PID is not running, and already reports nothing when
there is no sidecar file, both unchanged by this work); all three E6 tests
(in-package `getij` during a paused recovery already returns `ErrRecovering`,
and the symbols are already present, though acceptance test 2 as reframed
waits on `<-server.Serving()`, which the pre-fix tree has no declaration of,
so that one cannot be compiled there let alone run); E7 acceptance tests 4, 5
and 6 (restore-from-backup, a prompt open and an unbounded wait are all
unchanged); and both E8 tests (`ClientRetryTime` is already 24 hours). The
rest fail pre-change on their own, though those naming a symbol this work
introduces - among them E1 acceptance tests 2-5 - are proved RED inside the
fixed tree with the behaviour withheld, because the pre-fix tree would not
compile with them. E7 acceptance tests 1-3 are proved the same way and go
further: in the RED run they fail by **hanging** rather than by asserting, so
they only report a named failure when written with the bounded harness E7
specifies.

### E1: Publish the externally observable surface at the end of recovery

As an operator, I want the manager to present the state that already exists and
is well understood - "not up yet" - until it can serve every request correctly,
so that no client ever meets a half-up manager whose group state is incomplete.

**This reverses `.docs/reliable/spec.md` B1's user story.** B1 is "answer
ping/status/add within ~1 s of start regardless of history or running-job count,
so a `kill -9` restart is never stuck", and this decision makes startup blocking
again. The reversal is deliberate. B1's actual problem was a **history**-sized
scan (190 s and growing with 2.15M archived records, unbounded by anything the
operator controls). This window is bounded by **live** jobs, is cut by this
spec's own fix, and buys correctness B1's ordering cannot provide: once
`bucketDTK` is retired there is no database index answering "which live jobs are
in group G", so a group the in-memory state has not yet learned looks **empty**,
and an empty *seen* group means **satisfied** - the newly added job would be
released ahead of its dependencies. Silent wrong-order execution is the failure
class this whole spec exists to remove, so it cannot be left to a race. Gating
only the RPC readers does not close it either: `serveWebInterface` starts and is
awaited before `serveClients`, and `restJobsAdd` calls `s.createJobs` straight
from the HTTP handler.

That "an empty seen group means satisfied" reasoning has exactly one
running-manager counterpart, and it pre-dates this work:
`registerDepGroupMembers` runs after `db.storeNewJobs` has committed
`depGroupsSeen`, so a concurrent `createJobs` - each request gets its own
goroutine (`server.go:1635`) - can resolve a dependency on that group in the
gap and see it seen with no live member. Today's database has the same window,
because `storeNewJobData` (`db.go:2209`) already launches `bucketDepGroups` and
`bucketDTK` as separate concurrent batch stores. It is therefore not a
regression, and this design neither reopens nor closes it.

Rejected alternatives, for the record: returning `ErrRecovering` for dep-group
adds (gives `wr add` a new transient failure); blocking only add and modify;
holding the RPC readers while the socket answers nothing (mangos' first `Dial`
is synchronous, so a client gets a fast `ErrNoServer` only when the port is
**closed** - against a listening-but-unread socket the dial succeeds and the
ping burns the client's whole connect timeout, which is the hang this decision
exists to avoid); and accepting the race.

**`Serve` must not block on recovery.** The `serve` test helper calls `Serve`
synchronously and `pausedRecoveringFixtureServer` waits for the pause hook only
*after* `Serve` returns, so a blocking `Serve` deadlocks against a release that
can never come. Keep recovery in its background goroutine and publish from that
goroutine's tail.

New `Serve` order (current line numbers for orientation):

1. Unchanged through `initDB` (`:3452`), the db-close defer (`:3466`), the
   sidecar (E4), `xrep.NewSocket()` (`:3469`) and its close defer (`:3474`).
2. Replace `configureAndListen` (`:3478`) with a non-binding
   `prepareListener(sock, interruptTime, caFile, certFile, keyFile)` that sets
   `OptionMaxRecvSize` and `OptionRecvDeadline`, calls `earliestCertExpiry`, and
   loads the TLS keypair and CA into a `*tls.Config`. It returns the expiry
   time, that `*tls.Config`, and an error. Everything that can fail on bad
   input fails here, fast, through `Serve`'s error return, exactly as today: an
   expired certificate and a bad `tls.LoadX509KeyPair` both still make
   `wr manager start` die cleanly. Only the port bind
   (`sock.ListenOptions("tls+tcp://0.0.0.0:"+port, ...)`) moves.
3. Unchanged through the `Server` literal, `createQueue` (`:3582`),
   `certExpired` (`:3586`) and `go s.handleSignals` (`:3588`).
4. `startPriorStateRecovery(bgCtx, config, db)` (`:3624`) moves up to here and
   also calls `registerDepGroupMembers` (C1).
5. `Serve` returns. It no longer calls `serveWebInterface`, `persistToken` or
   `serveClients`. `setRecovering(0)` (`:3610`) moves into
   `startPriorStateRecovery` as its **first** statement, before
   `db.recoverIncompleteJobs`, so `isRecovering()` is true from before the
   decode until `finishRecovering`. The order within that function is not free:
   `setRecovering` resets `recoveryTotal` and `recoveryRestored`, while
   `setRecoveryTotal` (`server.go:1323`) deliberately does not touch the flag,
   so calling `setRecovering` after it would zero the total it just filled in
   and make acceptance test 3's `recoveryProgress() == (0, 3)` read `(0, 0)`.
   `Serve` keeps its `finishRecovering()` on the `startPriorStateRecovery`
   error path (`:3629`), which now clears a flag set inside the call it
   guards.

Publication, in `recoverInBackground` (`server.go:1462`), as the **last plain
statement** of the function body - not a defer:

1. `wgk := s.wg.Add(1)`, then `go s.serveWebInterface(..., wgk, ready)` and
   `<-ready` (which also sets `s.httpServer`, starts the casters, and installs
   `SetBadServerCallBack` / `SetMessageCallBack`).
2. `persistToken(config.TokenFile, token)`.
3. Bind the RPC listener with the prepared `tlsConfig`.
4. `wgk = s.wg.Add(1)`, then `go s.serveClients(..., wgk, ...)`, and record
   that the readers started (E3).
5. Remove the startup sidecar (E4) and `close(s.serving)`.

**Publication registers its own waitgroup keys**, each `s.wg.Add(1)`
immediately before its `go`, replacing the two `wg.Add(1)` calls `Serve` makes
today (`server.go:3592` and `:3613`). Keys issued in `Serve` and handed to
publication would be outstanding on every run where publication does not run -
shutdown in the window (E3, E2 acceptance test 2) and the `publishExit` path
(acceptance test 5) - and `shutdown`'s `s.wg.Wait(ServerShutdownWaitTime)`
(`server.go:6824`) would then never return, because `waitgroup.Wait(d)` blocks
on `sync.WaitGroup.Wait()` and its duration only schedules the "not Done" log.
That is the same fact E2 acceptance test 2 and E3 acceptance test 1 rely on for
`bgWG`. A `Stop` that never returns is worse than the breakages E3 lists, which
are a prompt panic and a wait bounded at 5 s (`waitForClientHandling`,
`server.go:1700-1710`), so the keys stay with the goroutines that `Done` them.

`serveWebInterface` and `serveClients` need `config`, the socket, the
`*tls.Config` and the token, so `Serve` stores them on the `Server` (or passes
them into `recoverInBackground`, which already takes `config`). The
`*waitgroup.WaitGroup` they also take is already on the `Server` as `s.wg`
(`server.go:3552`).

**They also need `Serve`'s `ctx`, not the ambient `bgCtx`.** Both goroutines
are handed `Serve`'s `ctx` today (`server.go:3594`, `:3615`) and shutdown never
cancels it, whereas `stopBackgroundStartupTasks` cancels `bgCtx` at the **top**
of `shutdown` (`server.go:6803`) - before `waitForRunnersToDie`,
`scheduler.Cleanup`, and `close(s.stopClientHandling)` (`server.go:6844`), the
point at which the readers are actually meant to stop. Publishing on `bgCtx`
would hand both goroutines a context cancelled while they still have to serve.
So `Serve` stores its own `ctx` alongside the rest, and publication uses that.

**Publish on recovery ENDING, not succeeding.** `recoverPriorJobsAndNote` logs
and returns on failure, so hanging publication off success would leave a manager
that is up, holds the DB lock and is invisible forever while `wr manager start`
polls indefinitely. The correctness-critical half still fails loudly: a decode
or group-build error returns from `startPriorStateRecovery` and `Serve` errors
out, which production `die()`s on.

**Skip publication when the context is cancelled.** `recoverPriorJobsAndNote`
returns early on `ctx.Err()` during shutdown, so the tail **is** reached while
the socket is being torn down. Guard publication on `bgCtx.Err() == nil`.

**Why a tail statement and not a defer.** A panic during recovery is covered by
`recoverInBackground`'s `defer internal.LogPanic(ctx, ..., true)`, which calls
`os.Exit(1)`; a publication *defer* registered after it would publish a listener
microseconds before that exit. A tail statement cannot. The consequence of a
tail statement is that publication happens **before** `finishRecovering`, which
is a defer, leaving a sub-millisecond window with the listener up while
`isRecovering()` is still true. That order is chosen deliberately: it is what
keeps `waitUntilRecovered`-gated tests race-free, because `isRecovering() ==
false` then implies a bound listener. The window is harmless - every prior job
is already enqueued by then, and any request that did miss the queue gets the
retryable `ErrRecovering` that `.docs/reliable2/spec.md` H2 specifies (E6).

**A transient bind failure must not kill the process.** Retry the RPC listener
bind every 500 ms for up to 5 s before giving up. That is the budget the `serve`
test helper already uses, and for exactly this failure: its comment
(`jobqueue_test.go:1393-1395`) is "a retry for 5s on failure. This allows time
for a server that we recently stopped in a prior test to really not be listening
on the ports any more." Today an in-use RPC port fails in `configureAndListen`
-> `sock.ListenOptions` and returns through `Serve`, so the helper retries. Move
the bind here and it happens in the recovery goroutine, after `Serve` has
returned, where a bare exit would kill the whole `go test` binary instead. The
race is live in the committed suite: `reliable4_dependency_tx_test.go:551-554`
stops a server and re-`serve`s on the same ports inside one test, and each test
in `reliable2_dbcompat_test.go` stops a server on the ports the next one
binds.

If the bind still fails after that budget, or `persistToken` fails (not port
contention, so not retried), log at error level and exit the process through a
`publishExit` package var defaulting to `os.Exit`, in the style of
`recoveryPauseHookForTest`. An invisible manager holding the DB lock is worse
than a dead one.

**Publication returns immediately after calling `publishExit`.** With the real
`os.Exit` the difference is unobservable, but a test double returns, and none
of the statements after the bind - `go s.serveClients(...)`, removing the
sidecar, `close(s.serving)` - may run against an unbound socket. So a server
that reaches `publishExit` is left with no listener and with `s.serving` never
closed, which is the state E1 acceptance test 5 asserts.

The web interface needs no such treatment: its bind error is raised inside
`runHTTPServer`, which only logs it, and `serveWebInterface` signals `ready`
regardless, so publication is not gated on the web port.

**Window size.** The whole recovery: 21 min and 42m56s on the two 2026-08-25
production runs. Those figures are inflated by the memory bug this spec fixes,
but even fixed it is O(minutes) at 150k live jobs and it scales with live-job
count. The window is **unmeasured** at its component level: the "37 s and 51 s"
figures in `.docs/reliable4/prod-restart-260825.md` are
process-start-to-post-scan and include `initDB` mmapping a 7 GB file, so they
must not be quoted as the decode or build cost. E9 measures it and states the
ceiling.

**Why the window is acceptable** (verified in the code):

- A pending LSF runner that starts during the window connects once with a 30 s
  timeout and `die()`s on failure (`cmd/runner.go:158-171`). It would do exactly
  the same against a manager that is simply down, which is the status quo every
  crash already produces. Slow-starting and down are indistinguishable to it, so
  this introduces **no new loss class**.
- A runner that had connected before the manager went down keeps retrying for up
  to `ClientRetryTime = 24 * time.Hour` (`jobqueue/client.go:116`). A 20-40
  minute delay is immaterial and is indistinguishable from the operator having
  started the manager 20-40 minutes later.
- No doomed runners are spawned during the window: runner dispatch is already
  gated on `if rc != "" && !s.isRecovering()` (`server.go:4618`). Scheduler
  groups are still built, but no `bsub` happens until recovery finishes, which
  is the same instant serving begins.
- `wr cloud deploy` blocks on the remote `manager start`, which loops
  indefinitely, so a long window extends deploy - but a fresh cloud DB has no
  live jobs, so it is ~0 in practice.

**Package:** `jobqueue/`
**File:** `jobqueue/server.go`
**Test file:** `jobqueue/depgranularity_startup_test.go`

**Acceptance tests:**

1. Given `Serve` with `recoveryPauseHookForTest` blocking, when `Serve` returns,
   then `Connect` to the manager port fails with `ErrNoServer`, an HTTP GET of
   the web port fails to connect, and `isRecovering() == true`.
2. Given the same paused server, when the hook is released and
   `<-server.Serving()` returns, then `Connect` succeeds, `Ping` succeeds and
   the web port accepts a TLS connection; and `isRecovering() == false` once
   `waitUntilRecovered(server)` (`server_startup_test.go:344`) has returned
   true. The wait is not tidiness. Publication is a tail statement and
   `finishRecovering` is a defer, so `Serving()` closes a sub-millisecond
   before the flag clears; asserting the flag straight off `Serving()` would
   contradict the ordering "Why a tail statement and not a defer" establishes,
   and would be relying on the `Connect`, `Ping` and TLS assertions to consume
   that window.
3. Given a DB with 3 prior incomplete jobs and a paused hook, when `Serve`
   returns, then `s.q.Stats().Items == 0` and `recoveryProgress()` reports `(0,
   3)`; after release and `<-server.Serving()`, `recoveryProgress()` reports
   `(3, 3)` and all 3 are reservable.
4. Given a paused-recovery server whose queue the test destroys with
   `s.q.Destroy()` before releasing the hook, so recovery's `enqueueItems` fails
   with `queue.ErrQueueClosed` inside `recoverPriorJobsAndNote`, when recovery
   ends, then publication still happens (`<-server.Serving()` returns and
   `Connect` succeeds, since `handlePing` reads only `s.ServerInfo`) and
   "prior-state recovery failed" is logged. A corrupted job record is the wrong
   seam for this: it fails at decode inside `db.recoverIncompleteJobs`, which
   `startPriorStateRecovery` (`server.go:1433`) calls synchronously, so `Serve`
   returns the error and publication never runs at all - the opposite of what
   this test is for.
5. Given a `publishExit` test double and a port the test binds and holds for
   the whole bind-retry budget, when publication runs, then `publishExit` is
   called with a non-zero code exactly once, only after the 5 s budget has
   elapsed, and the double's invocation is asserted after the fact (not inside
   a loop). Because publication returns straight after `publishExit` rather
   than falling through, the server is then left unpublished: a `select` on
   `Serving()` with a 200 ms timeout takes the timeout branch, and `Connect`
   fails with `ErrNoServer`. **The order of three steps makes or breaks this
   test.** Observe `publishExit`, then close the test's own listener, then
   assert `Connect`. Asserting `Connect` while the test still holds the port
   neither works nor discriminates.

   It does not work because `Connect` would hang. `dialClientSocket` calls
   `sock.DialOptions`, which mangos performs synchronously
   (`mangos/v3@v3.4.2/internal/core/socket.go:215-222`, and nothing here sets
   `OptionDialAsynch`); the `tls+tcp` dialer is built with a bare
   `&net.Dialer{}` (`transport/tlstcp/tlstcp.go:309`) and dials through
   `tls.DialWithDialer` (`:54`), so the TLS handshake has no deadline. A plain
   listener completes the TCP handshake from the backlog and then never speaks
   TLS, so the handshake never returns. `Connect`'s `timeout` argument does not
   help: `setConnectSocketOptions` (`client.go:764-781`) sets only the mangos
   send and receive deadlines, which apply after the dial. An implementor who
   closed the listener later would get a wedged test and the 10-minute
   package-wide panic E7's harness exists to avoid.

   It does not discriminate because, while the test owns the port, a failed
   `Connect` says nothing about whether publication bound anything. The
   assertion only carries meaning once the test's listener is gone and
   publication has already returned, so nothing can bind afterwards.

   Closing first is also what lets `Stop` return. `shutdown` calls
   `waitForPortsClosed` unconditionally (`server.go:6826`), and that loop
   (`:6987-6998`) polls until nothing answers on `s.ServerInfo.Port` or
   `s.ServerInfo.WebPort`, both filled from the config in the `Server` literal
   (`:3529-3530`) whether or not publication ever bound them; with the test's
   listener still on the manager port every probe succeeds and `Stop` never
   returns, wedging the rest of the package run (E3). With the listener closed
   first, `Stop(ctx, true)` returns.

   Given the same double and a port the test releases 1 s into the window, then
   the retry binds, `<-server.Serving()` returns, and `publishExit` is never
   called.
6. Given an expired certificate, when `Serve` is called, then it returns an
   error before launching recovery and `server` is nil - the fast-fail path is
   preserved.
7. Given a `certFile`/`keyFile` pair that do not match, when `Serve` is called,
   then it returns a `tls.LoadX509KeyPair` error rather than failing 20 minutes
   later inside the recovery goroutine.

### E2: `Serve`'s callers wait for publication

As a caller of `Serve`, I want a race-free way to know the manager is reachable,
because `Serve` returning no longer implies a bound listener.

Add `Server.Serving() <-chan struct{}`, closed at the end of publication and
also closed by `beginShutdown` so a caller never waits forever on a server that
is being stopped. `cmd`'s `startJQ` (`cmd/manager.go:1324`) waits on it before
`logStarted(server.ServerInfo, token)`, which today prints "manager started" and
a web URL immediately - in `-f` mode that would announce a manager that will not
listen for minutes.

**Every direct caller that then talks to the server must wait on `Serving()`,
and one that does not fails on every run rather than intermittently.**
Publication step 1 is `go s.serveWebInterface(...)` and `<-ready`, and
`serveWebInterface` sits on `<-time.After(serverListenWait)` (`server.go:3662`)
with `serverListenWait = 10 * time.Millisecond` (`server.go:125`) before it
signals ready. The RPC bind is step 3, so the port stays closed for at least
10 ms after `Serve` returns, and `dialClientSocket` (`client.go:740-757`) turns
the failed dial straight into `ErrNoServer` with no retry - the immediate
failure E1 acceptance test 1 relies on. The direct callers at `8b9ba00`, each
handled here or in the story named:

- `startJQ` (`cmd/manager.go:1324`, `Serve` at `:1377`), above.
- The in-package `serve` helper (`jobqueue_test.go:1396`), below.
- `client/testing.Serve` (`client/testing/server.go:240`), which calls
  `jobqueue.Serve` at `:243` and again inside `serveWithRetries` (`:256`). This
  one is **exported**, so it is published API and not just a fixture: its 9
  in-repo callers (`client/client_test.go`, 8; `client/testing/server_test.go`,
  1) use the returned server immediately, and an out-of-repo caller has no other
  signal to wait on.
- `startStatusTestServer` (`cmd/status_test.go:1319`, `Serve` at `:1337`). Its
  14 call sites - `cmd/status_test.go` (9), `cmd/add_test.go` (4),
  `cmd/manager_test.go` (1) - connect, or run a CLI command that connects,
  immediately after; the first is `cmd/status_test.go:257`.
- `startQueueCommandTestServer` (`cmd/suspend_test.go:234`, `Serve` at `:243`),
  whose only caller `withQueueCommandTestServer` (`:210`) connects at `:224`.
- `TestREST` (`jobqueue/rest_test.go:920`), which calls `Serve` at `:986` and
  `Connect` at `:989`, asserting a nil error at `:990`.
- `jobqueue/testdata/dbcompat/gen.go:95`, the committed fixture generator, which
  Serves and then `populate`s through a client. It carries `//go:build ignore`
  and is run by hand, so no test run catches it: the next regeneration of
  `db.golden` would, which is far too late.
- `jobqueue/server_startup_test.go:70` and `:254`: E4 reframes both onto
  `Serving()`.
- `measureCompletedHistoryStartup` (`jobqueue/reliable2_startup_test.go:114`,
  `Serve` at `:123`) is the one direct caller that needs no wait, and must not
  be given one. It times `Serve` itself, never connects, and hands its server
  straight to `Stop` or `waitUntilRecovered`.

`jobqueue/reliable4_dependency_tx_test.go` is deliberately not in that list: it
calls `Serve` only through the `serve` helper (`:531`) and
`pausedRecoveringFixtureServer` (`:554`), so the helper edits above carry it.
`:538` and `:562` are its `Connect` sites, and C2 records why both survive as
they stand.

**A missed helper looks like a flake and invites the wrong fix.** F4 acceptance
test 4 records that this suite has load-dependent victims, so a systematic
`ErrNoServer` across `cmd` reads as one more of them. The tempting repair is a
dial retry inside `Connect`: a production change to paper over a test-harness
gap, and one that would blunt E1 acceptance test 1, which needs `ErrNoServer`
back immediately. Fix the helper.

The `serve` test helper (`jobqueue_test.go:1396`) keeps its 5 s retry on a
`Serve` error and also waits on `server.Serving()` on success, as do
`client/testing.Serve` with its own 5 s retry and `startStatusTestServer` and
`startQueueCommandTestServer` with their 20-attempt "address already in use"
retries. What none of those retries covers any more is the RPC port bind, which
moves past `Serve`'s return into the recovery goroutine: an in-use port there is
no longer a `Serve` error the helper can see, and with no retry of its own it
would reach `publishExit` and end the `go test` binary. Port contention, and
only port contention, is the failure these helpers keep passing through
unchanged, because publication carries that retry on the same 500 ms / 5 s
budget (E1). Nothing carries the wait for them; each helper adds its own.
`prepareListener`'s failures - an expired certificate, a mismatched keypair -
stay on `Serve`'s error return, so those retries still cover them exactly as
today.

Split the current body out as `serveWithoutPublication` for
`pausedRecoveringFixtureServer` (`jobqueue/reliable2_dbcompat_test.go:304`),
through which the tests that deliberately observe the window open their server,
and which must not wait.

**`Serve`'s own doc and the package example state the old contract and must be
amended too.** `server.go:3379-3381` says `Serve` "makes it start listening on
localhost at the configured port for `Connect()`ions from clients, and then
handles those clients", which after this story describes what the recovery
goroutine eventually does rather than what `Serve` returning means. Amend it to
say that `Serve` returns once startup has been validated and prior-state
recovery has begun; that the listener, the web interface and the token file are
published only when recovery ends; and that a caller which then talks to the
server must wait on `Serving()`. `jobqueue/doc.go:55-71`'s Server example runs
`jobqueue.Serve` straight into `server.Block()`, so it needs the same wait
added. `Serve` is exported and an out-of-repo caller has no other signal, which
is the same reason B3 amends the `queue` package doc.

**Package:** `jobqueue/`, `cmd/`, `client/testing/`
**File:** `jobqueue/server.go`, `jobqueue/doc.go`, `cmd/manager.go`,
`client/testing/server.go`
**Test file:** `jobqueue/depgranularity_startup_test.go`,
`client/testing/server_test.go`. The other helper edits land in
`jobqueue/jobqueue_test.go`, `jobqueue/rest_test.go`, `cmd/status_test.go`,
`cmd/suspend_test.go` and `jobqueue/testdata/dbcompat/gen.go`, and F4 acceptance
tests 2 and 4 are what prove them.

**Acceptance tests:**

1. Given a server started with no prior jobs, when `<-server.Serving()` returns,
   then `Connect` succeeds on the first attempt (no retry).
2. Given a paused-recovery server and a goroutine that releases the pause hook
   once `Stop(ctx, true)` has been entered - the same arrangement E3 acceptance
   test 1 and E4 acceptance test 4 use - then `<-server.Serving()` returns
   because shutdown closed it rather than because publication ran (publication
   is skipped once `bgCtx` is cancelled, and `stopBackgroundStartupTasks` calls
   `bgCancel` before it waits), and `Stop` returns within 10 s. The release is
   required, not tidiness: `stopBackgroundStartupTasks` (`server.go:1603-1611`)
   calls `bgWG.Wait(ServerShutdownWaitTime)` and that wait does not time out
   (the duration only schedules the log of unfinished tasks), so a `Stop` with
   recovery parked at the hook never returns. A test that skipped the release
   would pass while leaking a wedged `Stop`, the held bolt file lock and both
   held ports into the rest of the package run.
3. Given `Serving()` observed twice, then the second receive also returns
   immediately (the channel is closed, not signalled) and no double-close panic
   occurs when publication and shutdown race under `-race`.
4. Given `wr manager start -f` on a DB with prior jobs, then the "wr manager ...
   started on" line and the web-interface URL are printed only after the
   listener is bound.
5. Given `clienttesting.Serve(t, config)` against a DB holding 3 prior
   incomplete jobs, when it returns, then `jobqueue.Connect` to the manager port
   succeeds on the first attempt: no retry, no `ErrNoServer`. The helper is
   exported, so this post-condition is the only thing an out-of-repo caller has
   to rely on.
6. Given `go vet ./jobqueue/` and `make lint`, then both are clean, `Serve`'s
   doc comment no longer says it is listening for connections by the time it
   returns, and `jobqueue/doc.go`'s Server example waits on `Serving()` before
   it uses the returned server. A doc and lint check, not a Go test, in the
   shape B3 acceptance test 1 uses.

### E3: Shutdown during the window

As an operator following the documented rollback procedure, I want `wr manager
stop` to work during the startup window, because that procedure is "stop the
manager and restore a pre-upgrade DB copy".

`wr manager stop` reads the pid file and SIGTERMs it, and the daemonized child
writes its pid file before `Serve`, so stop **does** reach a manager in the
window. Three things break there today:

- `shutdownHTTPServer` (`server.go:6970`) dereferences `s.httpServer` unguarded,
  and it is set only inside `serveWebInterface`. A SIGTERM before the web
  interface starts panics. Add a nil guard.
- `closeServerCommsAndDB` (`server.go:6841`) does `close(s.stopClientHandling)`
  then `waitForClientHandling`, which waits `ServerShutdownWaitTime` for a
  `clientHandlingDone` that is never closed because `serveClients` never ran.
  Record whether the readers started and skip both when they did not.
- Scheduler messages raised during the window are silently discarded, since
  `SetMessageCallBack` / `SetBadServerCallBack` and the casters now start late.
  This is harmless mechanically - the openstack implementation nil-checks both
  callbacks, `caster.Broadcasting` is a no-op and `Send` with no members drops -
  but say so rather than losing them by accident.

A fourth thing does not break but does gain a new input state, and it is worth
naming because it is easy to walk into: **the manager may never have bound its
ports.** `shutdown` calls `waitForPortsClosed` unconditionally
(`server.go:6826`), and `s.ServerInfo.Port` and `WebPort` are filled from the
config in the `Server` literal (`server.go:3529-3530`), not by the bind, so the
loop (`:6987-6998`) probes them either way. Inside the window that is benign:
nothing is listening, both dials fail, and the loop returns on its first
iteration, which is why no other E1 or E3 test discovers it and why no
production change is needed. It bites only when something **else** holds one of
those ports, because the loop has neither a deadline nor a context check and
spins until the port goes quiet. Production reaches that on the RPC port only
if a foreign process holds it when a SIGTERM arrives mid-window, since a manager
that loses the bind race leaves through `publishExit` (E1). It can happen on the
web port, where something else holding `WebPort` makes the loop spin today and
this work changes nothing about that. What a test can force is the RPC case, by
holding the manager port, which is why E1 acceptance test 5 closes its own
listener once it has seen `publishExit` and before it stops the server.

**Package:** `jobqueue/`
**File:** `jobqueue/server.go`
**Test file:** `jobqueue/depgranularity_startup_test.go`

**Acceptance tests:**

1. Given a paused-recovery server and a goroutine that releases the pause hook
   once `Stop(ctx, true)` has been entered, then `Stop` returns without
   panicking within **2 s** of that release, and the DB file lock is released
   (a second `initDB` on the same file succeeds). Two details make or break
   this test. The bound must be well under `ServerShutdownWaitTime`, which is
   exactly 5 s (`server.go:183`), or it passes whether or not
   `waitForClientHandling` was skipped and so never fails pre-fix. And the
   release is required: `stopBackgroundStartupTasks` waits on `bgWG`, and that
   wait does not time out (its `ServerShutdownWaitTime` argument only sets when
   unfinished tasks are logged), so a `Stop` with recovery parked at the hook
   never returns at all.
2. Given a paused-recovery server released the same way, when SIGTERM is
   delivered through the same path `handleSignals` uses, then no nil-pointer
   panic occurs and the process shuts down cleanly.
3. Given a fully published server, when `Stop` is called, then the existing
   shutdown behaviour is unchanged: `clientHandlingDone` closes, the HTTP server
   shuts down, and no "timed out waiting for client handling" warning is logged.

### E4: The sidecar is the primary operator channel during startup

As an operator watching `wr manager start`, I want progress reported
out-of-band, because for however many minutes recovery takes it is the only way
to tell a slow start from a hang.

Recovery observability **moves** from the request surface to the file sidecar;
it does not disappear. `waitForLiveManagerStartupWith` (`cmd/manager.go:760`)
already loops indefinitely, tells a dead daemon from a slow one via the child
process handle, polls with a short per-attempt connect deadline, and reports
progress by reading `internal.ReadDBUpgradeStatus`
(`internal.DBUpgradeStatusPath(dbFile)` = `dbFile + ".upgrade"`). It needs no
new mechanism, only new phases.

Three things about the sidecar must change before it can be the primary
channel:

- `keepPostUpgradeStartupStatus` (`server.go:3221`) writes the file **only** `if
  upgradedOnOpen`. Drop that condition: write it on every start.
- Its remover is deferred in `Serve` (`server.go:3467`), so the file is removed
  exactly when `Serve` returns - the instant recovery begins and the sidecar
  becomes the only channel. Move the removal to publication (E1 step 5) and to
  the shutdown path, so it also does not outlive a manager that died in the
  window.
- Moving it off that defer uncovers `Serve`'s **error** path, where neither
  publication nor shutdown runs: a `prepareListener`, socket, `createQueue` or
  `startPriorStateRecovery` failure would leave the file behind for the next
  reader. Keep an error-only removal on the defer, in the existing
  `closeOnError` idiom (`server.go:3245`) that the db and socket closes already
  use: remove the sidecar when `Serve` returns non-nil, leave it alone when it
  returns nil. The PID checks in `currentManagerDBUpgradeStatus`
  (`cmd/manager.go:883`) - `status.PID` against the child handle, and
  `managerDBUpgradeProcessRunning(status.PID)` - are the backstop for the case
  no code path can cover, a process killed without returning, but they are a
  backstop and not a licence to leave the file.

Phases written, using the existing `internal.DBUpgradeStatus` shape plus one
additive field:

    // internal/db_upgrade_status.go
    type DBUpgradeStatus struct {
        State     string    `json:"state"`
        Detail    string    `json:"detail"`
        Processed int       `json:"processed,omitempty"`
        Total     int       `json:"total,omitempty"` // NEW
        PID       int       `json:"pid"`
        StartedAt time.Time `json:"started_at"`
        UpdatedAt time.Time `json:"updated_at"`
    }

    // New states, alongside DBUpgradePostStartupState:
    DBStartupPrepareState   = "prepare to serve"
    DBStartupDecodeState    = "decode live jobs"
    DBStartupDepGroupState  = "build dependency-group state"
    DBStartupRecoveryState  = "recover prior state"

`DBStartupPrepareState` covers the span from `initDB` returning to
`startPriorStateRecovery` being called: socket creation, `prepareListener`'s
keypair and CA load, `currentServerIP`/`fqdn`, `scheduler.New` (which shells out
under LSF) and `createQueue`. **It was added during implementation, when the
original three states left that span with no sidecar at all** - so on a start
that performed no DB upgrade, `wr manager status` died "non-responsive" for its
whole duration, which is the defect E5 exists to remove. The post-upgrade state
must therefore be written only when an upgrade really ran (`upgradedOnOpen`),
rather than on every start.

Still uncovered, and outside this section's scope: `initDB` itself, which runs
before any sidecar can be written and whose production cost is the one phase the
measurements do not bound (it mmaps a 15 GB file). `wr manager status` remains
"non-responsive" for that span; `wr manager start` is unaffected, since it
reports elapsed time without a phase name.

`DBStartupRecoveryState` is written as the **last synchronous statement of
`startPriorStateRecovery`** (`server.go:1433`): after `setRecoveryTotal`, before
the `go s.recoverInBackground(...)`. Written inside `recoverInBackground`
instead, it would usually land before `Serve` returns and occasionally not,
which turns acceptance test 2 into a manufactured flake.

`Total` is `omitempty`, so files written when it is unset are byte-identical to
today's and an older reader ignores it. The recovery phase is refreshed on the
existing `recoveryHeartbeatInterval` (1 minute) tick that already logs "still
recovering prior state", plus once on each phase change, so no new timer is
introduced. This overlaps `.docs/bugfixes/260825-3.md` item 2, which makes the
same reporter's **log** lines visible; keep the two consistent and do not
restructure the reporter in a way that would make either awkward.

**`Processed` cannot climb during the recovery phase, and the sidecar must not
imply that it can.** `recoverPriorJobs` enqueues in a single `AddMany` batch,
so `noteRecovered` is called once with the full total. `recoveryProgress`' own
doc says `restored` reads 0 until that batch completes and then jumps straight
to `total`, and `startRecoveryHeartbeat` reports elapsed time precisely because
"there is no per-job progress to report". A `Processed` fed from `restored`
would therefore read 0 for the whole multi-minute window: an operator watching
0/150472 would read a hang.

So the recovery phase reports **elapsed time** in `Detail`, refreshed with
`UpdatedAt` on each heartbeat tick - the signal that does distinguish a slow
start from a hang. `Total` is still written, because the size of the wait is
worth knowing. `Processed` is written only where a real count exists: it keeps
its current meaning on the DB-upgrade phases and is left unset on the recovery
phase. If the enqueue is ever split into batches, `noteRecovered` is already
additive and `Processed` can start carrying `restored` then.

Both committed sidecar tests in `jobqueue/server_startup_test.go` are pinned to
the behaviour this story changes and must be reframed, not deleted.
`TestServeReportsPostUpgradeStartupUntilTokenReady` (`:70`) holds a FIFO-backed
token file so that `persistToken` blocks inside `Serve`, and asserts the sidecar
is gone once `Serve` returns; with `persistToken` moved to the publication tail,
the FIFO now holds **publication** instead, so the assertion moves from "gone
when `Serve` returns" to "gone when `<-server.Serving()` returns".
`TestServeDoesNotReportPostUpgradeStartupForBrandNewDB` (`:254`) asserts that a
brand-new DB gets no sidecar and that the TLS web port is up shortly after the
token is written; both premises change, since the sidecar is now written on
every start and the web port comes up only at publication. Its reframing is
that a brand-new DB's sidecar reports a non-upgrade startup phase during the
window and is removed at publication.

The reframed test must build its window with `recoveryPauseHookForTest`, not
with the token FIFO it uses today. The FIFO (`prepareFIFOBackedToken`,
`server_startup_test.go:260`, helper at `:149-175`) blocks at `persistToken`,
which is publication step 2, by which time step 1 has already brought the web
port up - so a FIFO window cannot assert that the web port is still closed. The
pause hook works even for a brand-new DB: it fires in
`recoverPriorJobsWithHeartbeat` (`server.go:1526-1532`), before
`recoverPriorJobs` reaches its `len(priorJobs) == 0` early return
(`server.go:3941-3944`). The test therefore drops the FIFO and its token
round-trip, which `TestServeReportsPostUpgradeStartupUntilTokenReady` still
covers.

**Package:** `internal/`, `jobqueue/`
**File:** `internal/db_upgrade_status.go`, `jobqueue/server.go`
**Test file:** `internal/db_upgrade_status_test.go`,
  `jobqueue/depgranularity_startup_test.go`

**Acceptance tests:**

1. Given a `DBUpgradeStatus` with `Total: 0`, when written and read back, then
   the JSON contains no `total` key and the round-tripped `State`, `Detail`,
   `Processed` and `Total` equal the original's; with `Total: 150472` the JSON
   contains `"total": 150472`. The equality is scoped to those four payload
   fields because `WriteDBUpgradeStatus` (`internal/db_upgrade_status.go:80`)
   overwrites `PID` and `UpdatedAt` and fills a zero `StartedAt`, so no
   whole-struct comparison can hold.
2. Given a server started on a DB needing no upgrade (`db.upgradedOnOpen ==
   false`) with a paused recovery hook, when `Serve` returns, then
   `internal.ReadDBUpgradeStatus(dbFile)` succeeds and reports `State ==
   DBStartupRecoveryState` with the correct `Total`. That is deterministic only
   because the phase is written synchronously in `startPriorStateRecovery`,
   above.
3. Given the same server, when the hook is released and `<-server.Serving()`
   returns, then `internal.ReadDBUpgradeStatus(dbFile)` returns an
   `os.IsNotExist` error (the sidecar was removed at publication).
4. Given a paused-recovery server on a DB needing no upgrade, when the sidecar
   is first asserted present with `State == DBStartupRecoveryState` (test 2's
   setup) and the server is then stopped the way E3 acceptance test 1 stops one
   (release the hook once `Stop` has been entered, since `Stop` cannot complete
   while recovery is parked at the hook), then the sidecar is also removed. The
   present-first half is what makes this test discriminate: pre-change,
   `keepPostUpgradeStartupStatus` (`server.go:3221-3224`) writes nothing at all
   for a DB needing no upgrade, so a bare "removed" assertion passes
   vacuously.
5. Given a server recovering 3 prior jobs, paused at `recoveryPauseHookForTest`
   and with the heartbeat interval lowered to well under the test's sampling
   window, when two samples are taken during the window, then both report
   `State == DBStartupRecoveryState` and `Total == 3`; the second's `UpdatedAt`
   is after the first's and its `Detail` reports a strictly larger elapsed
   time; and neither sample carries a non-zero `Processed`. The pause is what
   makes two samples possible: with only 3 prior jobs, an unpaused recovery
   finishes before the first tick. It does not cost the heartbeat, because
   `startRecoveryHeartbeat` runs before the hook fires (`server.go:1527-1531`),
   so the ticker keeps updating the sidecar while recovery is parked. A
   "between 0 and 3" assertion would be satisfied by a constant 0 and would
   test nothing, which is exactly what a `restored`-fed `Processed` would
   produce.
6. Given a `Serve` that fails after the sidecar is written - the mismatched
   `certFile`/`keyFile` pair of E1 acceptance test 7, whose
   `tls.LoadX509KeyPair` error is raised in `prepareListener`, after `initDB` -
   when `Serve` returns that error, then
   `internal.ReadDBUpgradeStatus(dbFile)` returns an `os.IsNotExist` error: the
   sidecar does not outlive a failed start.
7. `TestServeReportsPostUpgradeStartupUntilTokenReady`, reframed, passes: with
   the token FIFO unread the sidecar reports `State == DBStartupRecoveryState`,
   `PID == os.Getpid()` and a zero-size token file; after the FIFO is read and
   `<-server.Serving()` returns, the sidecar is gone and the token read from the
   FIFO equals `Serve`'s returned token. It cannot assert
   `postUpgradeStartupDetail` in that window: the FIFO blocks at `persistToken`,
   which is publication step 2, and by then this story's own phase writes have
   moved the sidecar on three times, the last of them to
   `DBStartupRecoveryState`. That is the same reason test 8 drops the FIFO for
   `recoveryPauseHookForTest`. Nor is there any seam between `initDB` and
   `startPriorStateRecovery` at which the upgrade detail is guaranteed to still
   be readable. The test keeps its name because `prepareDBNeedingStartupUpgrade`
   is still its fixture; what it asserts is the startup phase rather than the
   upgrade detail. `waitForDBUpgradeStatusDetail` (`server_startup_test.go:186`)
   matches on `Detail`, and the recovery phase's `Detail` is an elapsed time
   that changes on every heartbeat tick, so this test needs a state-matching
   sibling of that helper rather than the helper itself.
8. `TestServeDoesNotReportPostUpgradeStartupForBrandNewDB`, reframed onto
   `recoveryPauseHookForTest`, passes: for a brand-new DB the sidecar exists
   during the window with a state other than `DBUpgradePostStartupState`,
   neither the TLS web port nor the RPC port is connectable before
   `<-server.Serving()` returns, and after it returns the sidecar is gone and
   both ports answer.

### E5: `wr manager status` reads the sidecar

As an operator, I want `wr manager status` to say the manager is starting rather
than lie about it, in both daemonized and foreground modes.

Today, during the window: the daemonized path finds a pid file, fails to
connect, and `die()`s with "supposed to be running with pid N, but is
non-responsive" (`cmd/manager.go:511`); the `-f` path has no pid file at all and
prints "stopped". Both are wrong, and inconsistent with each other.

In `managerStatusCmd`, before either of those outcomes, read the sidecar via the
existing `currentManagerDBUpgradeStatus` (`cmd/manager.go:883`), which combines
`internal.ReadDBUpgradeStatus(config.ManagerDBFile)` with three rejections:
`status.PID != processPID` (skipped when `processPID <= 0`),
`!statusTime.After(preStart)`, and
`!managerDBUpgradeProcessRunning(status.PID)`. `wr manager status` has neither
a `preStart` nor a child process handle, so it passes the zero `time.Time` and
`0`, leaving the recorded PID's liveness as the whole test; passing
`time.Now()` would instead reject every sidecar, since the file always predates
the call. `managerDBUpgradeStatusFresh` (`cmd/manager.go:108`) is not part of
this helper - it only extends `waitForLiveManagerStartupWith`'s deadline, in
`extendDeadline` (`:853`).

If the helper reports a live startup phase, print that phase and exit 0, with
`Processed`/`Total` when both are set, `Total` and the phase's elapsed time
otherwise. In production the recovery phase is the second case (E4: `restored`
is all-or-nothing, so it carries no `Processed`), and the DB-upgrade phases are
the first. Build the line in a second helper,
`managerStartupStatusMessage(status internal.DBUpgradeStatus) string`, so the
wording is testable without running the command.

**Package:** `cmd/`
**File:** `cmd/manager.go`
**Test file:** `cmd/manager_test.go`. `managerStatusCmd` itself `die()`s and has
no in-process harness, so the tests below drive the helper it calls, in the
pattern that file already uses (`:274-308`, `:680-706`): swap the package
`config` for a `t.TempDir()` one, write a sidecar with
`writeDBUpgradeStatusForTest`, and call the helper directly. The wiring in
`managerStatusCmd` - this story's one production change - is not reachable that
way, so F3 step 5 runs `wr manager status` against a real manager inside a real
window and records the outcome.

**Acceptance tests:**

1. Given a sidecar naming a live PID and `State == DBStartupRecoveryState`,
   `Total: 150472`, no `Processed`, and a `Detail` carrying an elapsed time,
   when `currentManagerDBUpgradeStatus(time.Time{}, 0)` is called, then it
   reports the status as live, and `managerStartupStatusMessage` names the
   phase, the 150472 total and that elapsed time, saying "starting" rather than
   "stopped" or "non-responsive".
2. Given a sidecar naming a live PID with `Processed: 9000` and no `Total`,
   then the message contains "9000 so far". **This, not the combined form, is
   what wr can currently produce:** the two writers are disjoint - the startup
   reporter never sets `Processed`, and `dbUpgradeReporter.writeStatus` never
   sets `Total`, because totalling `rebuildJobLookupEntries` would need a second
   full `ForEach` over three buckets during the slowest phase of an already-slow
   start, which rule 6 forbids. A `Processed: 9000, Total: 150472` sidecar
   yielding "9000/150472" is retained as a forward-looking case, reachable if
   the enqueue is ever split into batches (`noteRecovered` is already additive,
   so `Processed` could then carry `restored`).
3. Given a sidecar whose recorded PID is not running, then
   `currentManagerDBUpgradeStatus(time.Time{}, 0)` reports nothing live and the
   existing pid-file/connect logic decides the outcome unchanged.
4. Given no sidecar file at all, then the helper reports nothing, so `wr
   manager status` falls through to today's pid-file and connect logic
   unchanged.

### E6: The recovery-window RPC machinery is retained as defence in depth

As a maintainer, I want a deliberate decision on the `ErrRecovering` machinery
rather than dead code by accident.

**Decision: keep it.** `ErrRecovering` (`server.go:101`), its two returns in
`getij` (`serverCLI.go:2057`) and `getijForReport` (`serverCLI.go:2004`), and
the `!s.isRecovering()` scheduling gate (`server.go:4618`) all stay. Reasons:
the scheduling gate is still load-bearing, because `buildSchedulerGroups` still
runs during the window and no `bsub` must happen before serving begins; the
sub-millisecond publish-before-`finishRecovering` window in E1 keeps the `getij`
branches reachable, so they are not dead; and `.docs/reliable2/spec.md` H2 is a
binding written contract that a reconnecting runner gets a retryable
`ErrRecovering` rather than a terminal `ErrBadJob`.

What becomes unreachable is specifically the **server-request** pathway in
production: no client can send a request before recovery finishes, so H2's
contract is satisfied vacuously rather than actively.

The two committed tests that assert it connect a client while recovery is
deliberately paused - `reliable2_dbcompat_test.go:193` (H2 acceptance test 1)
and `:240` (H2 acceptance test 2) - and can no longer connect at all. Reframe
them as server-state assertions using the in-package server, which needs no
listener.

**Package:** `jobqueue/`
**File:** none (no production change)
**Test file:** `jobqueue/reliable2_dbcompat_test.go`

**Acceptance tests:**

1. `TestReliable2RecoveryWindowReturnsRecovering`: given a paused-recovery
   server, when `s.getij(cr, true)` is called in-package with a `clientRequest`
   naming an incomplete fixture job's key, then the returned error string is
   `ErrRecovering`, and contains neither `ErrBadJob` nor `ErrBadRequest`. The
   `isRecovering()` and `recoveryProgress() == (0, dbcompatIncompleteCount)`
   assertions are unchanged; the `Connect`/`Touch` pair is replaced.
2. `TestReliable2RecoveryRestoresIncompleteJobs`: given a paused-recovery
   server, then `s.q.Stats().Items == 0` (nothing reservable during the window,
   asserted server-side instead of via a client `Reserve`); after `release()`,
   `waitUntilRecovered(server)` and `<-server.Serving()`, a client connects and
   reserves all `dbcompatIncompleteCount` jobs, each with
   `dbcompatIncompleteRepGroup`.
3. Given a live grep of the tree, then `ErrRecovering`, `getijForReport`'s
   recovering branch and the `!s.isRecovering()` scheduling gate are all still
   present and referenced (they must not be deleted as dead code).

### E7: A double-started manager fails cleanly and never touches the winner's DB

As an operator whose environment has a cron that restarts the manager, I want a
second manager launched during the window to fail cleanly, because extending the
unreachable window extends the window in which the cron may conclude the manager
is dead.

Neither of the two guards that exist stops it.

`manager start`'s own up-check is the first, and this change is what disarms
it: before daemonizing, it does `jq := connect(1*time.Second, true)`
(`cmd/manager.go:188`) and dies with "wr manager on port %s is already running
(pid %d)" if that connects. Today it connects, because the listener is bound
early. Under E1 the listener is not bound until publication, so throughout the
window the up-check gets a fast `ErrNoServer` - the same answer a genuinely
dead manager gives - and proceeds. The token file does not save it either:
`generateToken` reuses an existing one, so `connect` gets past its token check
and fails at the dial. That follows from E1 making slow-starting and down
indistinguishable on purpose, but it means the up-check cannot be counted as a
backstop here.

The pid-file lock is the second, and it does not stop it either: `reborn`
(`cmd/root.go:272`) deletes the locked pid file and retries, which succeeds
against a new inode, so the imposter spawns and its pid replaces the live
manager's. It then blocks in `bolt.Open`
**forever**, because `initDB` passes no `Timeout` and bbolt retries the flock
every 50 ms indefinitely. The winner's DB is untouched, but the hung imposter
acquires it the instant the winner exits - which collides directly with the
documented rollback procedure, since it would start writing to the file being
restored. With `--foreground` there is no pid file and no flock at all: the `-f`
branch calls `startJQ` directly and never calls `daemonize`.

**Decision: bound `bolt.Open` with a timeout, do not change `reborn`, and the
bound MUST NOT reach the restore-from-backup branch.**

That last clause is the whole point, and the hazard it avoids is worse than the
one being fixed. `initDB`'s `openedExistingDB` branch treats **any** `bolt.Open`
error as "corrupt (?) db file": if `db_bk` exists and opens, it does
`os.Remove(dbFile)`, copies the backup over it, and opens the new file. Add
`Options.Timeout` naively and a second manager started during the window gets
`bolt.ErrTimeout`, **unlinks the live DB out from under the running manager**
(which keeps writing to a deleted inode and loses everything at exit), and comes
up as a **second live manager** on a stale backup, on a fresh inode so the flock
now protects nothing. Two managers both submitting to LSF, and the winner's DB
destroyed, including mid-rollback.

So:

- Add `Timeout: managerDBOpenTimeout` (30 s) to every `bolt.Open` in `initDB`.
  Declare it near the `offlineDBOpenTimeout = 10 * time.Second` const rather
  than inside that block, as a package **var** carrying the
  `//nolint:gochecknoglobals` comment the house style uses for a test-tunable
  knob: `dependencyResolutionChunkSize` (`dependency.go:69`) and
  `recoveryHeartbeatInterval` (`server.go:246`). As a const, acceptance
  tests 1-3 each really wait the full 30 s and each harness deadline sits at
  35 s, so the three together add ~90 s to `make test`. As a var, a test lowers
  it and the deadline shrinks with it, since the deadline is written as
  `managerDBOpenTimeout + 5*time.Second`. `offlineDBOpenTimeout` is safe
  precedent for the timeout itself only because its caller merely returns the
  error. Its doc comment (`db.go:86-87`) ends "The running manager's own initDB
  opens intentionally omit this and block/wait", which this story falsifies, so
  amend it in the same change: no linter reads comment semantics, so F4's sweep
  cannot catch it going stale.
- In the `openedExistingDB` else-branch, **before** the restore-from-backup
  block, return immediately when `errors.Is(err, bolt.ErrTimeout)`, wrapping a
  new sentinel `ErrDBLocked` ("another wr manager holds this database") and
  naming the file. Never enter the restore path on a lock timeout.
- The open timeout is the only mechanism that works uniformly: it does not care
  about daemon mode, pid files, or who started what. Hardening `reborn` instead
  would produce a guard that silently does not apply under `-f`, and hardening
  the `manager start` up-check cannot work at all, since during the window
  there is deliberately nothing for it to connect to.

**What the bound costs, and why 30 s.** The imposter case is not the only one it
changes. Today a `manager start` issued while the previous manager is still
closing the database blocks in `bolt.Open` for as long as that takes and then
succeeds; with the bound it fails with `ErrDBLocked` after 30 s. The two cases
are the same flock and cannot be told apart, so the cost is real: a restart that
overlaps a shutdown can now fail and need retrying. 30 s is not derived from
`ServerShutdownWaitTime` (5 s, `server.go:183`), because the lock is held well
past it - `db.close` runs `finaliseBackup` first, which drains the writers and
then copies the **whole** database to `db_bk` before `closeBolt`, and at
production's 7 GB on NFS that copy alone can exceed 30 s. It is chosen instead
to sit between the bounded waits inside a shutdown and the 120 s `wr manager
stop` itself allows (`daemonStopGiveupS`, `cmd/root.go:70`): long enough that a
start racing a prompt shutdown still wins the lock, short enough that a start
racing a slow one fails with a message naming the file rather than lurking until
the winner exits. The documented sequences do not overlap - `wr manager stop`
polls the pid until the process is gone and warns if it is not - so the exposure
is an operator or a cron starting a manager while a large shutdown is still
writing its final backup, and the remedy there is to wait and start again.

The loser then fails the way any failed start does: `Serve` returns the
`ErrDBLocked` error, the daemonized child exits, `waitForManagerStartup`'s child
handle reports it promptly, and `manager start` prints the bad log lines and
dies. Under `-f` the same error is printed directly. Either way the winner keeps
serving and its DB file is untouched, which acceptance tests 1-3 assert at the
`initDB` level.

Known behaviour to record rather than fix here: under `-f` there is no pid-file
guard (which is why, after the 2026-08-25 11:59 `-f` start, `pid` still named
the dead 09:22 process); `wr manager stop` cannot find an `-f` manager by pid
file, so stopping one during the window depends on the RPC, leaving
kill-by-hand; `wr manager status` on an `-f` manager reports "stopped" during
the window, which E5 fixes.

**How tests 1-3 are proven RED.** Not by running them at the pre-fix commit:
they name `managerDBOpenTimeout` and `ErrDBLocked`, neither of which exists
there, so the tree would not compile. The RED run is against the fixed tree
with both new symbols declared and the two production behaviours withheld - no
`Options.Timeout` on `initDB`'s `bolt.Open` calls, and no `ErrDBLocked`
short-circuit before the restore-from-backup block. Even then they fail by
hanging, not by asserting. Without `Options.Timeout`, bbolt's `flock` retries
every 50 ms (`flockRetryTimeout`, `bbolt@v1.4.3/db.go:19`) for as long as the
lock is held (`bolt_unix.go:30-45` returns `ErrTimeout` only when `timeout !=
0`), and each of the three would block until `go test`'s 10-minute panic took
the whole package down with a stack dump rather than a named failure. So each
must call `initDB` on a goroutine and `select` between its result and a
`time.After(managerDBOpenTimeout + 5*time.Second)` deadline, failing on the
deadline branch rather than waiting on it. That harness is what makes the RED
run report a per-test "initDB did not return", and it is the same bound the
tests assert once the behaviours are restored, so it is not scaffolding to
remove afterwards. The goroutine must close whatever `*db` it eventually
receives, and the test's release of the held lock must be deferred, so a
goroutine left blocked by the RED run unblocks and lets the file go rather than
holding it for the rest of the package run.

**Package:** `jobqueue/`
**File:** `jobqueue/db.go`
**Test file:** `jobqueue/depgranularity_dblock_test.go`

**Acceptance tests:**

1. Given a DB file already held open by a `bolt.Open` in the test process, and a
   `db_bk` backup file present and valid, when `initDB` is called on that DB
   file, then it returns an error satisfying `errors.Is(err, ErrDBLocked)`
   within `managerDBOpenTimeout + 5s`, and the DB file still exists with the
   same size and modification time and the same inode as before the call.
2. Given the same setup, then the `db_bk` file is unmodified and no new
   `bolt.DB` was created (the holder's subsequent writes and reads still
   succeed).
3. Given a DB file held open and **no** `db_bk` present, then `initDB` still
   returns `ErrDBLocked` (not a bare `bolt.ErrTimeout`) and the DB file is
   untouched.
4. Given a genuinely corrupt DB file (truncated mid-page) with a valid `db_bk`
   and nothing holding a lock, then the existing restore-from-backup behaviour
   is preserved: `initDB` succeeds and the recovered DB contains the backup's
   jobs.
5. Given an unlocked, healthy DB, then `initDB` opens it with no measurable
   delay (the timeout is not a sleep).
6. Given a DB file held open by the test and released after 1 s, when `initDB`
   runs concurrently, then it waits and succeeds rather than failing fast: the
   bound is a wait, so a restart that overlaps a prompt shutdown still opens the
   database.

### E8: A long absence does not lose a reconnecting runner

As a runner that was connected before the manager died, I want to survive a
20-40 minute startup window, because the whole justification for E1 rests on it.

`ClientRetryTime` being a constant is not the same as proving a runner survives
a long absence and resumes correctly. Pin it.

**Package:** `jobqueue/`
**File:** none (no production change expected)
**Test file:** `jobqueue/depgranularity_startup_test.go`

**Acceptance tests:**

1. Given a client holding a reserved job, when the server is stopped, the client
   attempts an `Archive` that must retry, the server is restarted on the same DB
   with a recovery window longer than one client retry interval, and publication
   then happens, then the `Archive` eventually succeeds, the job ends `complete`
   exactly once, and the client never returned a terminal error. Drive the
   timings from `ClientRetryWait` and a shortened `retryTime` rather than
   waiting 24 hours.
2. Given the same scenario, then the recovered job is not run twice and the
   final queue holds exactly one item for its key.

### E9: Measure and state the startup window

As an operator sizing the outage this change introduces, I want the window's
duration measured and its scaling stated, because this decision converts that
duration into total unavailability.

Measure, on a synthetic DB, the wall time of each published phase separately:
`initDB` (open plus mmap), the live-bucket decode (`db.recoverIncompleteJobs`),
the dependency-group state build (`registerDepGroupMembers`), the dependency
resolution pass, and `enqueueItems`. Record the numbers at three live-job counts
(for example 10k, 50k, 150k) in `.docs/dep-granularity/` and state the resulting
ceiling and its scaling with live-job count. Do **not** quote the 37 s and 51 s
production figures as the decode or build cost: they are
process-start-to-post-scan and include mmapping a 7 GB file.

**Package:** `jobqueue/`
**File:** none (measurement, plus a log line reporting each phase's duration)
**Test file:** `jobqueue/depgranularity_startup_test.go` (acceptance test 1),
  `jobqueue/depgranularity_scale_test.go` (acceptance test 2, build tag
  `reliability_repro`)

**Acceptance tests:**

1. Given a server started on a DB with 100 live jobs, then the manager log
   carries one line per startup phase with an `elapsed` field, at warn level so
   it appears at the default log level (as `bf53de0`'s recovery lines do). 100
   is enough to produce every phase line and small enough to leave `make test`
   at its baseline.
2. Given the phase measurement at 10k and at 50k live jobs of the same shape,
   then the recorded decode and build durations are within 2x of a linear
   relationship in live-job count (recorded, and asserted only as "not
   superlinear by more than 2x", so the test is not a wall-clock flake on a
   loaded host). This one sits behind the `reliability_repro` tag, in the file
   F3's fixture generator already owns: two full `serve` recoveries over 10k
   and 50k live-job DBs would materially lengthen `make test`, which F4 item 4
   gates at the branch-point baseline. The 150k point E9 asks for is measured by
   hand through the same tagged entry point, not in `make test`.
3. `.docs/dep-granularity/` contains the recorded numbers and the stated ceiling
   before merge.

---

## F. Proof that gates the merge

### F1: Memory is linear in the work

As a maintainer, I want the retained bytes per recovered job asserted to be
independent of dep-group size, because that is the property whose absence killed
production five times, and memory is faithfully reproducible on this host.

Two assertions, one exact and one bounded. The exact one is primary because it
can never flake: the total number of dependency keys retained across all
recovered items must equal the number of declared group edges plus live essence
edges, and must not change when the group's membership grows. The bounded one
measures actual retained heap, following go-conventions' memory-bounded pattern
and the in-tree `memoMallocsDuring` helper
(`jobqueue/reliable4_snapshot_memo_test.go:355`): `runtime.GC()` plus
`runtime.ReadMemStats` before and after, with the resolved result held live
across the second read so the GC cannot collect what is being measured, and
unsigned-underflow guarded.

**Package:** `jobqueue/`
**File:** none (test-only)
**Test file:** `jobqueue/depgranularity_memory_test.go`

**Acceptance tests:**

1. Given a DB with one group of M live members and W = 2,000 live waiters on it,
   when `db.resolveDependencies` runs over all live jobs, then
   `sum(len(rj.deps))` equals `W` for M = 200 and equals `W` for M = 2,000 (the
   retained key count does not scale with M at all).
2. Given the same two fixtures, when retained heap growth across
   `resolveDependencies` is measured with the result held live, then growth
   divided by the number of resolved jobs is below 2 KB in both cases, and the M
   = 2,000 figure is no more than 1.5x the M = 200 figure.
3. Given the M = 2,000 fixture, when a full `serve` recovery runs, then
   `s.depGroups.memberships()` equals the fixture's total (group, member) pair
   count, and the sum of `len(item.Dependencies())` over all queue items equals
   the fixture's total declared edge count.
4. Given the assertions above, when the whole test is run against a build with
   `dependencyKeys` reverted to expanding group members (a mutation check the
   implementor performs and records, not a committed test), then tests 1 and 2
   both fail.

### F2: Transaction counts

As a maintainer, I want the recovery dependency pass's transaction cost pinned
with bbolt's own counter, so that neither a per-job regression nor a whole-pass
regression can land.

Both directions are already pinned by `jobqueue/reliable4_dependency_tx_test.go`
and both must still hold after this work: one read transaction **per chunk** of
`dependencyResolutionChunkSize` jobs, never one per job, and never one for the
whole pass. That file's three dependency-resolving tests are rewritten onto the
new API (C2), so the counts have to survive a rewrite rather than an untouched
file. `db.bolt.Stats().TxN` still needs no new instrumentation.

**Package:** `jobqueue/`
**File:** none (test-only)
**Test file:** `jobqueue/reliable4_dependency_tx_test.go`,
  `jobqueue/depgranularity_add_test.go`

**Acceptance tests:**

1. C2's five acceptance tests pass.
2. Given a group with M live members and one job added to it, then the add
   path's `TxN` delta is the same (within 1.25x) for M = 200 and M = 2,000 (D1
   acceptance test 2, a regression guard that already holds pre-change).
3. A regression guard that already holds pre-change, not a pre-fix failure:
   given recovery of N live jobs at the production chunk size of 1,000, then
   the dependency pass's `TxN` grows by exactly `ceil(N/1000)`. Every read the
   pass makes - each essence `checkIfLive`, each `bucketDepGroups` seen check -
   goes through `txDepReader` (`dependency.go:85-103`) on the chunk's own
   transaction, so neither adds a transaction. That was already true at
   `c96dcbf`, which chunks at `dependencyResolutionChunkSize = 1000`
   (`dependency.go:69`), so tightening the bound from a range to exactly
   `ceil(N/1000)` does not make it fail pre-fix. Keep it anyway: a bound that
   allowed one transaction per essence dependency or one per group would be
   loose enough to hide a regression to per-job reads inside a chunk.

### F3: `developers/wrdev.sh dep-granularity-check`

As a maintainer, I want a prod-shaped scale gate that fails before the fix and
passes after, because three false-PASS gates were caught in an earlier batch.

Add mode `dep-granularity-check [waiters] [members] [groups]` to
`developers/wrdev.sh`, defaulting to 30,000 waiters, 3,000 members in one group,
and 6,300 groups total. It must cover the **add path** as well as recovery.
Scale to prod's shape (150,000 waiters, 19,000 members) on a large host via
`WRDEV_DEPGRAN_*` env knobs; the defaults are chosen so the pre-fix run fails
without exhausting the dev host.

Procedure:

1. Build the fixture DB through `db.storeNewJobs` in a Go test
   (`TestDepGranularityFixture`, tag `reliability_repro`), **not** by writing
   `bucketJobsLive` directly, so `bucketRDTK` **and** `bucketDepGroups` are
   populated the way the real add path populates them. A fixture that populates
   only `bucketJobsLive` makes the pre-fix run resolve no member keys, never
   allocate quadratically, and false-PASS - the same failure mode as the
   `pristine10` history fixture.

   **`bucketDTK` is the exception: the fixture writes it directly.** A3 drops
   `dgLookups`, so after this work `storeNewJobs` writes no `bucketDTK` entry
   at all and a fixture built through it leaves the bucket empty. The pre-fix
   binary resolves dep-group dependencies only through
   `retrieveIncompleteJobKeysByDepGroupTx` (`db.go:3050`), which cursors that
   bucket, so an empty one makes every seen group look satisfied, nothing
   quadratic allocates, and the pre-fix run PASSes - the false-PASS this step
   exists to stop - while acceptance test 4 could never hold. Write those
   entries as the pre-upgrade data they are: one `depGroup + dbDelimiter +
   jobKey` key per (group, live member) pair, the shape `db.generateLookupKey`
   produces (`db.go:2397`) and exactly what pre-fix `storeNewJobs` wrote, in
   the `replaceLookupRebuildTestBucket` pattern (`db_test.go:1499`) A3
   acceptance test 3 already uses. That is the DB shape the upgrade meets in
   any case, since every live job in production was added by a pre-fix binary.
2. Store the group's **members before** the waiters. `storeNewJobs` ->
   `retrieveDependentJobs` scans the waiters of the new job's own `DepGroups`,
   so waiters added last (having no `DepGroups` of their own) trigger no scan,
   whereas members added last would each decode all the waiters.
3. Copy the fixture to `$WRDEV_ROOT` (`local work=...`, `rm -f` in cleanup, pass
   `WRDEV_ROOT="$WRDEV_ROOT"` on the inline `go test` prefix since it is not
   exported) and start an isolated production-mode manager on the copy.
4. Delimit the window with two log lines that exist in **both** trees:
   `bf53de0`'s `recovering prior state` (`server.go:1497`) and `recovering:
   prior state recovered` (`server.go:1515`), both at warn so they appear at the
   default log level. The gap between them is `recoverySec`. Open the window on
   the **first** match of the start line, not the last: the heartbeat's
   progress line `recovering: still recovering prior state` (`server.go:1565`)
   contains that text, exists in both trees and is kept by E4, so matching the
   last would shorten both `recoverySec` and the sampling window and could hide
   a threshold breach. The finish line is unambiguous either way, since
   `recovering: prior state recovered` does not contain the start line's text.
   Poll `VmHWM` from `/proc/<pid>/status` once a second from process start
   until the finish line (or until the process dies), keeping the largest value
   seen; that is `peakRssMb`.

   **Do not key the window on the manager becoming reachable, or on
   publication.** At the pre-fix commit there is no publication step:
   `configureAndListen` binds at `server.go:3478` and `serveClients` starts at
   `:3615`, both before `startPriorStateRecovery` at `:3624`. A "wait until it
   answers" check therefore returns at t = 0, the window collapses, `VmHWM` is
   read before recovery has allocated anything, and the gate PASSes pre-fix -
   the same false-PASS class step 1 warns about, and a direct contradiction of
   acceptance test 1, which needs a recorded pre-fix `peakRssMb` at least 4x the
   post-fix figure. Sampling `VmHWM` throughout rather than once at the end also
   survives the pre-fix run being OOM-killed mid-recovery, when
   `/proc/<pid>/status` vanishes.
5. As soon as the `recovering prior state` line appears, and before the finish
   line does, run `wr manager status --deployment production` once against the
   isolated manager and record `statusInWindow=` as one of: `starting`, exit 0
   naming a startup phase; `up`, exit 0 reporting a running manager;
   `nonresponsive`, a non-zero exit; or `-`, the window closed before the
   sample. This is the only automated exercise of E5's one production change,
   the new branch in `managerStatusCmd` - E5's own tests drive the helpers that
   branch calls, not the command. It does not discriminate pre-fix, where the
   manager answers from t = 0 and the answer is therefore `up`, so acceptance
   test 1 does not rest on it.

   On `-` the run has measured nothing about E5, so do not settle for it. When
   the run otherwise completed - both delimiter lines appeared - redo it once
   with the waiter count doubled, which lengthens recovery and so widens the
   window, and record the retry in `errors=`. Widen once only: a second `-` is
   a FAIL. Do not widen a run that already FAILs for another reason, since a
   longer one teaches nothing.
6. Once the finish line has appeared and the manager answers, `wr add` one job
   into the big group and time it as `addSec`.
7. Print one `DEPGRAN-SUMMARY` line with `waiters=`, `members=`, `peakRssMb=`,
   `recoverySec=`, `statusInWindow=`, `addSec=` and `errors=`, writing `-` for
   any metric the run never reached.

Gate shape, following `archive-rate`:

- Capture the pipeline exit status (`|| rc=$?` ... `return "$rc"`) so an
  appended cleanup `rm` cannot swallow a FAIL.
- A hard `FAIL (NOT MEASURED)` branch when the summary line is absent, when
  `peakRssMb` is missing, zero or unparseable, when the `recovering prior
  state` line never appeared - the manager never reached recovery, so nothing
  was measured - or, in a run that did reach the finish line, when
  `recoverySec` or `addSec` is missing or unparseable. Step 7 writes `-` for a
  metric the run never reached and no numeric threshold can evaluate that, so a
  run whose `wr add` failed reports `addSec=-` and would otherwise sail past
  every threshold below. A gate that passes when the measurement is absent is
  worse than no gate. The next bullet takes precedence when it is the finish
  line that is missing: that is a plain FAIL, not this branch.
- Plain FAIL, not `FAIL (NOT MEASURED)`, when the start line appeared but
  `recovering: prior state recovered` never did, whether because the run hit the
  timeout or because the manager died. That is the pre-fix outcome the gate
  exists to catch, `peakRssMb` is still reported from the samples taken before
  the death, and `errors=` names it.
- FAIL when `peakRssMb` exceeds `WRDEV_DEPGRAN_MAX_RSS_MB`, `recoverySec`
  exceeds `WRDEV_DEPGRAN_MAX_RECOVERY_SEC`, or `addSec` exceeds
  `WRDEV_DEPGRAN_MAX_ADD_SEC`.
- FAIL when `statusInWindow=nonresponsive`: `wr manager status` died against a
  manager that was starting, which is exactly what E5 exists to prevent. FAIL
  when it is `-` after procedure step 5's doubled-waiter retry, because a run
  that never samples leaves E5's production change with no automated coverage
  at all. `up` is not a FAIL: it is the pre-fix answer, and acceptance test 1
  does not rest on this metric.

**Where those three thresholds come from.** Each is an env-overridable shell
default, in the pattern `archive-rate` uses for `WRDEV_ARCHRATE_MAX_MEAN_MS`.
Set each to **2x the post-fix figure** acceptance test 1 records at the default
shape, rounded up to a round number, then check the result is at most **half
the recorded pre-fix figure** - a threshold the pre-fix run would pass is not a
gate. Acceptance test 1 already records both figures and already requires
pre-fix `peakRssMb` to be at least 4x post-fix, so the memory threshold meets
both conditions by construction. If a metric's pre-fix and post-fix figures
turn out closer than 4x, that metric does not discriminate at this shape: say
so in the recorded numbers and gate on the other two rather than picking a
value between them. Write the chosen values and the figures they came from into
`.docs/dep-granularity/` beside the gate, so a later run can tell a real
regression from a re-tuned threshold.

**How the pre-fix run gets a fixture.** Neither the `dep-granularity-check`
mode nor `TestDepGranularityFixture` exists at the pre-fix commit, and copying
`jobqueue/depgranularity_scale_test.go` across is not a safe way to get the
generator: E9 acceptance test 2 shares that file and measures a startup phase
absent there, so the pre-fix worktree would not compile it. The mode therefore
takes a pre-built fixture DB through a `WRDEV_DEPGRAN_DB` knob that skips
generation and measures the supplied file, on the precedent of `WR_ARCHRATE_DB`
(`developers/wrdev.sh:410-419`). Build the fixture once in the fixed tree and
give each run its own copy of it, exactly as step 3 already copies one. This
works only because step 1's fixture writes `bucketDTK` itself: leave those
entries to `storeNewJobs` and the pre-fix run has nothing to expand and hands
back a PASS. `wrdev.sh` resolves `REPO` from its own path
(`developers/wrdev.sh:37`), so the pre-fix worktree's copy of the script builds
and measures the pre-fix binary - pristine means the product code under test,
not the harness, and both runs then measure the same bytes. Record how it was
arranged beside the numbers.

**Package:** n/a
**File:** `developers/wrdev.sh`
**Test file:** `jobqueue/depgranularity_scale_test.go` (build tag
  `reliability_repro`)

**Acceptance tests:**

1. Given a pristine `git worktree` at the pre-fix commit, and the fixture DB
   built in the fixed tree and supplied through `WRDEV_DEPGRAN_DB` (above,
   since the generator does not exist at that commit), when
   `developers/wrdev.sh dep-granularity-check` runs at the default shape, then
   it prints FAIL, and the recorded `peakRssMb` is at least 4x the post-fix
   figure. Record the pre-fix and post-fix numbers in `.docs/dep-granularity/`.
2. Given the fixed tree, when the same command runs, then it prints PASS with
   all three metrics measured and non-zero.
3. Given a deliberately broken invocation (fixture DB path removed), then the
   gate prints `FAIL (NOT MEASURED)` and exits non-zero.
4. Given the fixture generator, when the produced DB is inspected, then
   `bucketDTK`, `bucketRDTK` and `bucketDepGroups` are all non-empty and
   `bucketDTK` holds `members` entries for the big group.
5. Given the fixed tree, when the same command runs, then the summary carries
   `statusInWindow=starting` and never `nonresponsive` or `-`, taking procedure
   step 5's doubled-waiter retry if the first attempt samples nothing. This is
   the only thing that covers E5's command wiring, so a run that ends at `-` is
   a FAIL rather than an accepted outcome.

### F4: Regression guards

As a maintainer, I want the suites that cover dependencies, recovery and startup
to stay green, because this change touches all three.

**Acceptance tests:**

1. `queue` package: `TestQueue` (including `queue_test.go:1478-1501`) and the
   rest of `queue/queue_test.go` pass.
2. `jobqueue`, `cmd`, `client` and `client/testing`, unchanged and green. In
   `jobqueue`: `TestJobqueueExecutionAndDependencyScenarios`,
   `TestReliable4RecoveryDependencyState`, `TestDBUpgradeProgress`,
   `TestGetIncompleteWaitingForDepGroups`,
   `TestReliable2FastStartupNoHistoryScan` and
   `TestReliable2CLIStatusCountStaysAScan`. In `cmd`, `client` and
   `client/testing`: every test that starts a server through the helpers E2
   changes - `startStatusTestServer` (14 call sites),
   `startQueueCommandTestServer` (1) and the exported `client/testing.Serve`
   (9). Not one of their assertions changes; only the helpers do.
3. The nine tests this work deliberately rewrites each keep asserting the same
   property against the new shape: `TestDBReverseLookupIndex` and
   `TestDBDepGroups` (A3), `TestReliable4DependencyResolutionUnchanged`,
   `TestReliable4DependencyFreeTxCost` and
   `TestReliable4RecoveryDependencyPass` (C2),
   `TestReliable2RecoveryWindowReturnsRecovering` and
   `TestReliable2RecoveryRestoresIncompleteJobs` (E6), and
   `TestServeReportsPostUpgradeStartupUntilTokenReady` and
   `TestServeDoesNotReportPostUpgradeStartupForBrandNewDB` (E4). None is
   deleted, and none has an assertion weakened to make it pass.
   `TestDBDepGroups` is in this list, not in item 2's unchanged-and-green list,
   because A3 stops `storeNewJobs` writing `bucketDTK` and its second `Convey`
   builds its rebuild fixture that way; its assertions are unchanged, only how
   the fixture's `bucketDTK` entries get there. The two transaction-cost tests
   are here for the same kind of reason: their counts are unchanged, but every
   `incompleteJobKeys` and `resolveDependencies` call in them has to change, and
   `perJobTx > len(jobs)` is replaced outright (C2).
4. `make lint` 0 issues; `make test` at the branch-point baseline or better,
   `cmd`, `client` and `client/testing` included, since E2's helper changes are
   the only thing keeping those three green; `make race` with 0 data races.
   Measure that baseline at the branch point, `8b9ba00`, rather than taking the
   older 413 passed / 9 skipped / 29 packages figure recorded at `bf53de0`:
   `.docs/bugfixes/260825-3.md` records 421 passed / 9 skipped / 29 packages
   two commits later. Record which figure was used. Record `uptime` next to
   every result too: this is a shared node at load average 85-120 on 8 cores,
   and `TestJobqueueExecutionAndDependencyScenarios`, `TestJobqueueProduction`,
   `TestServerWebI` and `TestSubscriptionReconnectResync` are known
   load-dependent victims. Never accept a "known flake" attribution without a
   load-matched A/B against a pristine worktree. A whole-package `ErrNoServer`
   in `cmd` or `client` is the opposite of a flake, though: it is a helper that
   never got its `Serving()` wait (E2), so check the helpers before spending a
   load-matched A/B on it.
5. `developers/wrdev.sh idle-backlog-cpu 50000 25` and `developers/wrdev.sh
   backlog-rescan-check 2000 50000` still pass.

---

## G. Out of scope, recorded as follow-ups

### G1: The add path's all-time waiter scan

`retrieveDependentJobs` decodes every all-time waiter of a new job's dep groups,
live and archived, transitively, and resurrects archived ones for re-run. It is
arguably DEVELOPERS.md rule 6's "no history scan on a control path", but it is
O(all-time waiters) rather than O(waiters x members), so it is not the OOM.
Preserve it byte-for-byte here to protect the resurrect-and-rerun semantics.

### G2: No offline live-job-reduction tool

Production stays down until this fix lands and restarts on it. Do not spend
spec, implementation or review time on offline surgery.

### G3: Reporting all unsatisfied dep groups

The new model could populate `WaitingForDepGroups` with every unsatisfied group
for free, but that changes `wr status --missing_deps`, the `waiting-deps`
display state, and the REST and web payloads. A separate feature.

### G4: Re-enabling `wr mod --dep_grps`

The flag wiring in `cmd/mod.go` is commented out because member-key rewriting
was "complex" - precisely what this design removes. Tempting, but it widens a
change that is blocking production.

---

## Implementation Order

Sections A through E are one delivery and must land together. A recovery-only
fix leaves the manager dying on the first `wr add` into a large dep group; the
`queue` package changes are what make the group key work at all; and the startup
reorder is what makes the add path correct, since a group the in-memory state
has not yet learned looks satisfied. The phases below are review units within
that single delivery, each building on tested foundations from the prior one.

1. **The `queue` package (section B).** B1 (`SatisfyDependency`), B2 (the two
   existence proxies plus the new no-backing-item `Kick` pinning test), B3
   (docs). Depends on nothing; unblocks everything. Reviewable in isolation, and
   its tests are pure `queue` tests.

2. **Per-group state and resolution (section A).** A1 (`depGroupMembers`), A2
   (`dependencyKeys` and the deletion of the sentinel machinery), A3
   (`bucketDTK` retired, plus the `TestDBReverseLookupIndex` and
   `TestDBDepGroups` updates). Depends on phase 1 for `SatisfyDependency`. A1
   and A2 must land together, since A2's resolution rule needs A1's
   `hasMembers`.

3. **Recovery (section C).** C1 (build the state before resolving; keep the
   per-chunk transaction), C2 (rewrite the committed transaction-cost tests onto
   the new API, keeping their counts). Depends on phase 2.

4. **The add, modify, remove and kick paths (section D).** D1 (add), D2
   (modify), D3 (`wr remove`'s guard), D4 (the `Kick` pinning test at the
   jobqueue level). Depends on phase 2; independent of phase 3 in code but
   shares the same hooks, so review them after it.

5. **Startup (section E).** E1 (publish from the recovery tail, and the
   `configureAndListen` split), E2 (`Serving()` and its callers, including the
   `serve` test-helper split), E3 (shutdown during the window), E4 (the
   sidecar), E5 (`wr manager status`), E6 (retain and reframe the
   `ErrRecovering` machinery), E7 (bound `bolt.Open` without the restore
   branch), E8 (24-hour reconnect survival), E9 (measure the window). E1 must
   land before E2-E6, E8 and E9: every one of them describes, survives or
   measures a consequence of it. E7 is the only story here independent of E1,
   and may land in parallel. This phase is a substantial part of the work, not a
   footnote: it reverses `.docs/reliable/spec.md` B1's user story.

6. **Proof (section F).** F1 (memory), F2 (transactions), F3 (the `wrdev.sh`
   scale gate, proven to FAIL from a pristine pre-fix worktree), F4 (the
   regression sweep). F1 and F2 can be written as failing tests before phases
   2-4 and used to drive them, except F2 acceptance tests 2 and 3, which are
   regression guards that already hold pre-change. Test 2 is D1 acceptance test
   2. Test 3's `ceil(N/1000)` bound already holds at `c96dcbf`, because
   `resolveDependencies` chunks at `dependencyResolutionChunkSize = 1000`
   (`dependency.go:69`, `:450-457`) and every read the pass makes rides
   `txDepReader` on the chunk's own transaction; tightening the bound from a
   range to exactly `ceil(N/1000)` does not make it fail pre-fix. F3 must be
   proven RED at the pre-fix commit before it is trusted, and its window must be
   keyed on the two recovery log lines rather than on reachability, or it PASSes
   pre-fix. F4 runs after every phase.

Then restart production on the result. That restart is the final evidence; no
real-LSF Tier-B run is required.

---

## Appendix: Key Decisions

- **Sets at group granularity, not counts.** Memory no longer decides it: one
  membership stored once is ~250k entries at prod's shape, about 1/6000th of
  today's cost. The failure mode decides it. A lost or duplicated decrement on a
  counter releases a waiter before its parents have finished, which is silent
  wrong-order execution in the user's pipeline - the worst failure class
  available here. A set makes re-add, key rename and `wr remove` idempotent. A
  boolean is insufficient because dependencies are satisfied incrementally and
  the design must know when the *last* one clears.

- **One opaque synthetic key, not a new `queue` concept.** `queue` keeps its
  documented "depend on opaque keys, resolved when an item with that key is
  removed" contract and gains one additive capability, resolving a dependency
  key with no backing item - which `promoteDependants` already does internally
  from `removeItem`. That reuses `Queue.dependants` unchanged as the per-group
  waiter set, reduces `Item.remainingDeps` to one entry per declared group,
  makes the never-seen sentinel machinery redundant, and avoids one queue-mutex
  acquisition per waiter when a group clears, which DEVELOPERS.md rule 2
  disfavours.

- **Essence dependencies stay job-key edges.** They are per-job identity,
  bounded by what the user declared, and not the quadratic part.

- **All three areas in one delivery.** Sharing resolved slices cannot make
  prod's heap fit, because the quadratic state is also in the queue itself
  (~2.9e9 map entries with or without sharing), and the add path is quadratic in
  the same way. The interim slice-sharing idea is abandoned; no phased-by-area
  delivery.

- **No new bucket, no migration, no startup history scan.** `bucketRDTK` already
  holds the (group, waiter) pairs, membership and waiters come from jobs already
  decoded, and only "has this group ever been seen" needs the database - one
  `bucketDepGroups` get per distinct group with no live member.

- **`bucketDTK` left in place, unwritten.** Deleting it is the more dangerous
  option: it repairs nothing (the rebuild is gated on `bucketDepGroups`, and
  builds *from* `bucketDTK`), it would make an old binary see every seen group
  as satisfied and run everything immediately, and it would break the modify
  path on the ~150k pre-upgrade jobs through `deleteLookupEntriesForJobKey`. A
  hard anti-downgrade marker was also rejected. Rollback is an operator step -
  stop the manager, restore a pre-upgrade DB copy - and `db_bk` is not that
  copy.

- **Invisible until fully ready.** Nothing externally observable comes up until
  recovery has finished, because there is no way to serve the add path correctly
  before the group state exists and REST reaches that path before the RPC
  readers even start. The gate is the **listener**, not the token file:
  `generateToken` reuses an existing token file and `deleteToken` removes it
  only on a clean stop, so after an OOM kill `client.token` is already on disk
  and every token-based readiness check is satisfied from the first instant; and
  deleting the token for the window is not a safe substitute, because a second
  kill mid-window would make the next start generate a new token and lock out
  every 24h-retrying runner. Within publication the token file is written just
  before the bind (E1 steps 2 and 3), so on a fresh start, where there is no
  reused token, `manager start`'s poll - which checks the token file before it
  dials (`connectIfManagerTokenReady`, `cmd/manager.go:731-737`) - can never
  meet a bound listener whose token file is still missing.

- **Publication is a tail statement, before `finishRecovering`.** A defer
  registered after `LogPanic`'s would publish a listener microseconds before
  `os.Exit(1)`. The cost is a sub-millisecond window with the listener up while
  `isRecovering()` is true; the benefit is that `isRecovering() == false`
  implies a bound listener, which keeps every `waitUntilRecovered`-gated test
  race-free.

- **Publish on recovery ending, not succeeding**, so a failed recovery does not
  leave a manager that is up, holds the DB lock and is invisible forever.

- **The bound on `bolt.Open` must not reach the restore-from-backup branch.**
  That branch treats any open error as corruption and unlinks the live DB. A
  naive `Options.Timeout` would let a second manager destroy the winner's
  database and come up on a stale backup with the flock protecting nothing.
  `ErrDBLocked` short-circuits before it. `reborn` is left alone, because
  hardening it would produce a guard that silently does not apply under `-f`.

- **Testing strategy.** Deterministic in-package GoConvey tests using the
  existing `serve`/`Connect` harness, the `recoveryPauseHook` seam and
  `t.TempDir()` fixtures; exact count assertions as the primary memory evidence
  with a bounded retained-heap assertion beside them; `db.bolt.Stats().TxN` for
  transaction counts, with no new instrumentation; inert counters
  (`memberships()`) in the accepted house style of `db.archivedDecodes`,
  `Job.derivations` and `db.archiveTxObserver`; and one `wrdev.sh` scale gate
  proven RED from a pristine pre-fix worktree. An acceptance test that cannot
  fail at the pre-fix commit says so where it appears, and section E carries one
  note covering its own, since every E story but E7 is proved against a tree
  with E1 landed rather than against that commit. Do not hunt for a pre-fix
  failure behind a test labelled a regression guard; do demand one for every
  test that is not. Where such a test names a symbol this work introduces -
  among them E1 acceptance tests 2-5, C1 test 8, C2 tests 3-4, E7 tests 1-3 and
  every A1 and A2 test - the pre-fix tree does not compile, so its RED run is
  inside the fixed tree with the behaviour withheld, exactly as E7 spells out.
  Never put `So()` inside a loop of more than 20 iterations - count and assert
  the final count. Follow `go-implementor` (write the failing acceptance test
  first) and `go-reviewer` (verify every acceptance test genuinely fails before
  the change and passes after), both referencing `go-conventions`.
