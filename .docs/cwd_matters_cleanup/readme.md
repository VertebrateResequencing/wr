# The cwd_matters cleanup data loss

**What this is.** The story of PR #558 (branch
`fix-cwd-matters-cleanup-deletes-parent`): a real user's scripts directory was
recursively deleted by wr's own default `on_exit` behaviour, and fixing it grew
into a rebuild of how wr decides which directories it may delete, and where it
may run a `run` behaviour's command.

**Where the evidence is.** Everything asserted here was measured, and the
measurements, red commands, probe outputs and per-round mutation verdicts live
in [`../bugfixes/260828-3.md`](../bugfixes/260828-3.md) (1823 lines, the
per-layer checklist) and [`../bugfixes/260829-2.md`](../bugfixes/260829-2.md)
(the review-comment batch). This file is the curated version, pointing into
those two for proof. The code is
[`jobqueue/workspace.go`](../../jobqueue/workspace.go) plus the path helpers in
`jobqueue/utils.go`.

## The incident

A user ran a batch of `wr add --cwd_matters` jobs (the reported incident
involved 19632 of them) with `Cwd` set to a directory of their own on Lustre, of
the shape `/lustre/scratch124/pam/projects/.../wr_RunCisEQTL`. The **parent of
that `Cwd` was recursively deleted**: their entire scripts directory,
`/lustre/.../scripts_ciseqtl`, with every `.R`, `.sh`, `.README` and `.rds` file
in it and a results directory beside them. `ensureCwdExists`
(client.go:1340-1346) then recreated the bare `Cwd`, so the job carried on
running in an empty directory.

The mechanism, in four steps:

1. **The poison.** #530 (commit 5993460, shipped in v0.37.0 and v0.37.1) added
   live-introspection touches and built each snapshot from `cmd.Dir`:
   `newExecuteLiveState(cmd.Dir, ...)` at client.go:1466. For a `--cwd_matters`
   job, `resolveWorkingDir` sets `cmd.Dir = job.Cwd` and returns
   `actualCwd == ""`, so `cmd.Dir` **is** `job.Cwd`. Every touch shipped
   `JobEndState.Cwd == job.Cwd`, against that field's documented invariant
   (client.go:2586-2593: supply the actual working directory used, or the empty
   string if it is the Job's own `Cwd`).
2. **The copy.** `applyLiveSnapshot` (serverCLI.go:355-362) did
   `job.ActualCwd = jes.Cwd`, poisoning the server-side job every touch
   interval; `copyJobForClient` shipped it back to the runner on a re-attempt.
3. **The deletion.** `Behaviour.cleanup` (behaviours.go:238-256) treated
   `filepath.Dir(j.ActualCwd)` as a disposable workspace and `os.RemoveAll`ed
   it. With `ActualCwd == Cwd`, that is the **parent of the user's own working
   directory**.
4. **The trigger.** Server-side on the FIRST attempt, via
   `killLostJobAndTriggerBehaviours` (server.go:4594-4623) running behaviours
   against the poisoned server-side job; runner-side on any re-attempt.

`wr add` attaches the default `on_exit` behaviour `[{"cleanup":true}]`
(cmd/add.go:685) to every job, `--cwd_matters` included, and `wr status`
advertised it. Meanwhile cmd/add.go:219-224 documented `cleanup` and
`cleanup_all` as having **"no effect when cwd_matters is true"**. The
documentation was right about the intent and wrong about the code, and was
believed for two releases. That same failure — a comment promising a property
the code does not have — recurs throughout this work, which is why several later
layers are comment-only.

Two more things in the wrong-parent family came out of the same investigation.
`mountBaseDirs` branched on `ActualCwd != ""` rather than `!CwdMatters`, so
under the poison `defaultCacheBase = filepath.Dir(Cwd)`: **mount cache dirs
written into the parent**. And `Job.Unmount` -> `rmEmptyMountDirs` ->
`rmEmptyParentDirs` was a second parent-destruction route independent of
`Behaviour.cleanup`, since it started at `filepath.Dir(current)` and so walked
UPWARD past `baseDir` when leaf == base. `JobModifier.applyCmdCwd` had also
poisoned `ActualCwd` on `wr mod --cwd_matters` since that flag first existed
(commit 7b1f002), so #530 widened an older bug rather than creating all of it.

**Release note.** For a `--cwd_matters` job, an undefined mount point moves from
`Cwd` back to `Cwd/mnt` and an unspecified `CacheBase` from the parent of `Cwd`
back to `Cwd`, restoring pre-v0.37.0 behaviour; tooling built against v0.37.x
paths will notice.

Evidence: LAYERs 1 and 2 of the checklist, with the red tests that reproduced
the deletion against a fixture mirroring the incident.

## What the code does now

**One resolution point.** `jobqueue/workspace.go` is the only place a Job's
`Cwd` and `ActualCwd` are read to license a deletion or to decide where a
command runs. Its three consumers — `Behaviour.cleanup`, `Behaviour.run` and
`Job.Unmount`'s empty-dir tidy-up — consume nothing else, so they cannot
disagree about which directories are wr's.

The resolution, in order:

1. `Job.workSpaceSnapshot()` copies `CwdMatters`, `Cwd`, `ActualCwd`, `Key()`
   and `MountConfigs` under the Job's read lock, and releases it before any
   filesystem work.
2. `paths()` returns "wr created nothing here" for a `CwdMatters` Job or a blank
   `ActualCwd`; refuses either directory unless it is **already absolute**; and
   calls `createdCwdRel`, which requires the reported directory to be strictly
   inside `Cwd` and to be the path `mkHashedDir` builds **from this Job's own
   key** — `<something>_cwd/k0/k1/k2/<key[3:]><digits>/cwd`, checked by asking
   `calculateHashedDir` what it would produce, so recogniser and builder cannot
   drift.
3. `prove()` opens `Cwd` as an `os.Root`, proves the workspace (the parent of
   the working directory) is a real directory strictly inside it with no symlink
   among the components leading to it, keeping the `FileInfo` it lstat'ed at
   **each** component, then lstats the working directory as a name relative to
   that root.
4. `keptDirs()` resolves every mount point and cache location once and
   classifies each against the proven dirs: `wholeActualCwd`, `inActualCwd`,
   `workSpaceEntries`, `mountPoints`, `wholeWorkSpace`,
   `muxfysNamesWorkSpaceEntry`.

**How deletion is bounded.** Nothing is deleted by re-resolving a path string.
`provenDirs.openChain()` descends once from the `Cwd` handle, keeping one
`*os.Root` per level, each opened with `openVerifiedDir` — `os.SameFile` against
the info the proof took at that level — so no handle above `Cwd` exists and the
boundary is structural rather than a check. The sweep opens the workspace as its
own root, re-lstats the working directory as a single named entry and requires
`proveSameDir` against the proof's info, then removes entries by their bare
names through that root. The upward walk removes `names[i]` through `roots[i]`,
so it costs no further openats and cannot be redirected by a mid-walk swap.
`Behaviour.run` gets its directory **held open**, and `cmd.Dir` names the
descriptor (`/proc/self/fd/N`), which the child resolves after `fork()` while it
still has our descriptor table.

**Cost.** The lexical checks make no syscalls and fail closed; the guard is 5
`lstat`s, O(depth below `Cwd`) and independent of how deep `Cwd` itself is,
which is the Lustre-relevant direction. Measured by `strace` rather than the
clock, a full-depth cleanup's deletion phase went 18 -> 8 directory `openat`s
(the upward walk itself 10 -> 0), and 20.03 -> 13.02 per cleanup for 500 jobs
sharing a hash prefix.

**The poison itself is closed four ways.** `Job.setActualCwd` is the sole writer
of `ActualCwd` from a `JobEndState` and ignores any cwd on a `CwdMatters` Job;
`JobModifier.applyTo` clears `ActualCwd` whenever a modification changes
`Job.Key()`, and `applyCmdCwd` clears it on a `--cwd` or `--cwd_matters` change
that does not; `Job.dropImpossibleCleanups` removes `cleanup`/`cleanup_all` from
any `cwd_matters` job at `Server.prepareInputJobs`, `applyBehaviours` and
`db.decodeJob`, so a v0.37.x database is sanitised on read; and
`Job.createdCwd()` is the one expression of "the directory wr created for this
Job", so display, `ssh` and mounting cannot each get it slightly wrong.

## Why each decision is what it is

This is the section that stops a future change reintroducing a bug. Each entry
is a decision and the failure it prevents.

**Identification never keys on this process's `AppName`.** `mkHashedDir` builds
`Cwd/<AppName>_cwd/...`, so keying on `AppName` would be an exact origin check —
but it is a package var set to `"wr"` only in `cmd/runner.go`, and the manager
keeps `"jobqueue"`. Cleanup runs in the manager too, on the very path that fired
in the incident, so asking what THIS process would build refuses every real
workspace server-side and **silently disables all cleanup**. Hence a SUFFIX
check (`createdCwdBaseSuffix`, shared by builder and recogniser), and by
`HasSuffix` not `Contains`, or a user directory called `wr_cwd.old` passes — a
round-7 mutation survived until a test said so.

**`Job.Cwd` cannot be normalised at source.** It feeds `Job.Key()`, so it is job
identity and a directory name on disk. `absJobDir` refuses a non-absolute `Cwd`
or `ActualCwd` at the resolution instead. That prevents LAYER 32, the worst
finding of any round: nothing absolutised `Job.Cwd` while the proofs used
`filepath.Abs`, i.e. resolved against **the current process's** directory. With
a relative `Cwd` every proof passed, but against the MANAGER's directory: the
probe deleted a user's `proj/results/2026/runA/sampleX/align/scripts` beside the
manager, `err == nil`, while the job's real workspace was elsewhere entirely.

**There is no resolve-the-symlink fallback.** A resolved path names a symlink's
target, not the directory asked about, and the deletions descend by READING the
workspace and working directory, which follows a symlinked final component.
Learned twice: LAYER 8's escape 3 was INTRODUCED by the fix for escapes 1 and
2, when a resolved path reached the recursive delete and `Cwd/userdata` was
destroyed with a nil error; LAYER 21 then found the surviving fallback was
itself a route, since a Job's Cmd can point its mount dir at the user's own
tree. Removing the concept made `provenDirs`' contract stronger: `rel` is
always the path the caller asked about.

**`proveSameDir` is one shared check, not two.** `run`'s `openVerifiedDirFile`
and cleanup's `openVerifiedDir` each had a copy of "still the dir that was
checked", and cleanup's `actualCwdNow` asked only `IsDir()` — kind, not
identity. A DIRECTORY renamed onto `<workspace>/cwd` after the proof is a real
dir, so it passed, and the fresh info then became the identity of record for the
open that followed. A probe with a real racing goroutine and no test seam won
**20 of 400 attempts**, while `run`, asking the question properly, refused the
identical swap. One function asked by both consumers about the same stored field
means one mutation now reddens four tests.

**The origin proof is binding; only ever widening tolerance was the bug.**
Rounds 5 and 6 consulted `relIsJobCreatedCwd` in one place, purely to WIDEN
tolerance for an absent workspace, and required of every path only depth plus
leaf name. Round 8 measured what that allows: two Jobs sharing a `Cwd`, both
with real `mkHashedDir` workspaces, the attacker's `ActualCwd` set to the
victim's working directory the way `applyLiveSnapshot` sets it from the wire.
`CleanupAll` returned **nil** having deleted the victim's `results.txt`, its
working directory, its live `TMPDIR` and its whole workspace, and a `run`
behaviour returned **nil** having executed the attacker's command inside it.
Every job of a `Cwd` has the created shape at the created depth below the same
`*_cwd` base, because it IS one. So `createdCwdRel` now requires
`relIsJobCreatedCwd(rel, key)` of **every** path. Three things hold that up:

- **The prerequisite** (LAYER 47): `JobModifier.applyTo` takes the key before
  applying modifications and clears `ActualCwd` if it differs afterwards, so a
  stored path and the current key cannot disagree. Testing the key rather than
  clearing in `SetCmd`/`SetMountConfigs` also covers the container fields, and
  does not clear when a modification sets a field to the value it already had.
- **One statement of each condition**: `relIsCreatedCwd` was folded INTO
  `relIsJobCreatedCwd`, so depth, base suffix, hashed dirs, unique dir and leaf
  name are each said once. Two binding predicates at one call site is the "same
  rule stated twice" shape this work was bitten by every round.
- **The depth check is a check and a precondition.** A path one level too DEEP
  whose leaf is still `cwd` — which a Job's own Cmd can make inside the
  directory wr gave it — satisfies everything else, and would have that
  directory swept as a workspace; every index in the predicate is also fixed.
  `isMkTempName`'s digit check is load-bearing for the same reason: without it
  `<key[3:]>-mydata/cwd` is accepted (LAYER 60).

**`wholeWorkSpace` exists even though the case looks unreachable.** A mount
point at or above the workspace makes everything wr created the inside of a live
mount, and cleanup runs BEFORE `Job.Unmount`, so deleting there reads through
the mount into the user's remote filesystem. Both halves of `protect()` describe
something INSIDE the workspace, so neither can record such a point;
`wholeWorkSpace` says it, and cleanup then deletes nothing at all. Round 11
established that the only at-or-above points nameable in advance are
`<Cwd>/<AppName>_cwd` and above, and that mounting at one of those hides the
working directory `mkHashedDir` already made, so the Job's Cmd cannot start. It
is **kept anyway**: its cost falls only on jobs that cannot run, and removing a
safety net on the strength of a reachability argument is the disable-everything
failure this work has already been bitten by. Its comparison (`dirIsAtOrAbove`)
also asks the filesystem when the two strings disagree, because
`<symlink-to-Cwd>/<AppName>_cwd` and a relative `Mount: ".."` name one directory
two ways, and with only the lexical answer cleanup swept the workspace and
working directory through a live mount (LAYER 58). It is the one containment
question needing no usable path BACK, so it can afford to say yes on a spelling
it cannot name; every other comparison stays lexical, because their `rel` is
used against a handle on the *unresolved* directory.

**`removeExcept` must not gain a no-mounts fast path.** `jobWorkSpace.empty` is
the only route to the keep set, so a `len(mounts) == 0` short-circuit is the
only route that never applies the `muxfysCachePrefix` rule — the one place a
NAME rather than a path identifies a cache dir, and what let LAYER 46's attacker
job destroy the victim's un-uploaded writable-mount output. A mountless Job's
keep set is empty anyway, so it is swept just as unconditionally: the fast path
bought nothing but a hole. Contrast `removeActualCwd`'s `len(keepDirs) == 0`
fast path, kept, whose mutation is GREEN by design — it empties the working
directory instead of unlinking it, and the entry sweep deletes the emptied
directory anyway. GREEN there is proof of equivalence, not a missing test.

**The muxfys name rule is a fact about the Job's configuration, not a global
rule.** `muxfysNamesWorkSpaceEntry` is set only when one of the Job's mounts has
a `CacheBase` resolving to the workspace itself — the default for a mounting
Job, impossible for a Job with no mounts, which is where muxfys puts the
directory it names for itself. Applied to every Job, which is what removing the
fast path did in round 8, a Job's own Cmd creating `../.muxfyssquat` kept the
workspace, `removeUpward` hit `ENOTEMPTY`, and the workspace plus the whole
`<AppName>_cwd/k/k/k` chain **leaked permanently, one per job** (LAYER 53). It
is a name rule at all only because muxfys names the cache dir it makes inside a
given `CacheBase`, and deliberately does not delete a cache dir it was given
rather than chose.

**The absence rule differs between cleanup and `run`, and says so.** Cleanup
tolerates a working directory that is not there: the Job's own Cmd may have
deleted it, and cleanup runs more than once for a lost job, so refusing leaks
the workspace including `tmp` (LAYER 38's idempotence regression). `run`
refuses it, because absence has no legitimate meaning for a directory a command
is about to be executed in, and the name goes to `exec.Cmd`, so anything
creating it in between would choose where the user's command ran. It is an
`absenceRule` passed at the two call sites rather than inherited by whichever
consumer shares the function, and its test asserts the refusal is the
resolution's own (`errors.Is(err, os.ErrNotExist)` is FALSE), because a rule
that only holds because the next syscall happens to fail is a rule nobody knows
is there. Absence gets no exemption from the proof itself (LAYER 41): an early
return skipping `proveActualCwd` when the workspace was missing let
`removeEmptyParents` delete up to five levels of the user's own empty
directories, cleanup reporting success.

**A directory that cannot be shown to be the Job's is refused, never
substituted.** Cleanup deletes nothing and returns a named error
(`errNotBelowBaseDir`, `errNotACreatedCwd`); a `run` behaviour executes nothing.
A leaked workspace is recoverable and loudly reported; a deleted `Cwd/userdata`
is not. That trade decides the direction of every fix here.

**`cmd.Dir` names a descriptor, not a path.** `exec.Cmd` resolves a `Dir` name
again when the command starts, and `run` also fires in the MANAGER for a job
declared lost whose Cmd may still be alive on a node sharing the filesystem, so
racer and executor can be different machines. A racer looping
remove/symlink/remove/mkdir, with no timing effort, redirected the user's
command out of the Job's `Cwd` **11 times in 200 attempts**; naming
`/proc/self/fd/N` made it **0 of 1000**. Where that is unavailable the
resolution warns rather than refuses: there is nothing else to run the command
in, so refusing would take `run` away from every job on such a host to avoid a
race open for as long as the feature has existed.

**A non-`CwdMatters` Job with a blank `ActualCwd` runs nothing.** `paths()` is
nil for `CwdMatters` OR a blank `ActualCwd`, and only the first means the Cmd
ran in `Cwd`; a blank `ActualCwd` means only that THIS process never learned
which directory, and a manager with no web port never enables the live-snapshot
Touch that would tell it. Before LAYER 56,
`--on_exit '{"run":"rm -f *.tmp"}'` executed in the user's `Cwd`.

Three smaller decisions, each with a measured reason:

- **The resolution snapshots under `RLock` and releases before any filesystem
  work.** Cleanup read `ActualCwd` unlocked while `applyLiveSnapshot` wrote it
  under the lock, both live in the manager, confirmed under `-race`; holding a
  lock across a directory walk is what DEVELOPERS.md section 2 forbids.
- **`MountConfigs.Key()` sorts a copy, stably.** It sorted the CALLER's slice
  while `Key()` is a read at every call site: 29 `DATA RACE` reports, 2.9% of
  concurrent reads returning a key that was not the Job's, 1.6% of Jobs left
  holding a config list with an entry lost and another duplicated — and a
  dropped writable S3 mount means the Cmd writes into a plain directory cleanup
  then deletes. `SliceStable` because the key is persisted identity and a
  directory name, so configs sharing a `Mount` must not reorder if Go's sort
  changes; no test can redden that today, and it is kept anyway.
- **Impossible cleanups are dropped, not rejected.** cmd/add.go documents wr as
  discarding them, so rejecting from the Go SDK would make the API contradict
  the documented CLI and REST behaviour for identical input. `Job.Key()`
  excludes `Behaviours`, so dropping cannot change job identity or dedup.

## Measured residuals

Each with what must be true for it to bite.

**Shape and origin, not proof of provenance.** A reported `ActualCwd` must be
`Cwd/<name ending _cwd>/k0/k1/k2/<key[3:]><digits>/cwd` where the key is the
reporting Job's OWN. `k0`-`k2` are three hex characters, grindable in about 4096
tries (or one in 4096 by chance), but the component carrying the other 29 has to
be named for the rest of that same key. **Bites when** a user directory of
exactly that shape exists inside the Job's `Cwd`. For scale: when the check was
depth-and-name only, a user tree at that depth lost 13 of 13 files and its
parent was unlinked with `err == nil`.

**The same-key residual is open.** `os.MkdirTemp`'s digits are not pinned and
cannot be from a stored path alone, so a Job may name any workspace built from
its own key, including one another LIVE instance of that Job is using. Probed on
the committed tree: `cleanup err=<nil> out.txt=gone cwd=gone tmp=gone`,
`run err=<nil> ranInOther=true`. **Bites when** two live instances of one Job
share a `Cwd`: two managers, two queues, or two users running the same Cmd
there. Closing it means recording the workspace path somewhere cleanup can
trust.

**The `exec.Cmd` window is closed on Linux only.** `os/exec` has no `DirFD` and
nothing portable replaces `/proc/self/fd`. With the descriptor prefix pointed at
a path that does not exist: deterministically, with the swap between proof and
command start, **50 of 50** attempts redirected the command against **0 of 50**
pinned; blind, with a racer and no timing effort, **2 of 1200** against **0 of
1200**. **Bites when** the host has no `/proc`, which now logs a warning naming
the directory and the descriptor path it could not use.

**The handle pins identity, not location.** A working directory the Job's own
Cmd MOVES between the proof and the exec is still where the command runs, at its
new location, possibly outside `Cwd`; one it DELETES is still chdir'ed into, as
an unlinked directory. **Bites when** the Job sabotages the directory wr made
for it.

**`os.SameFile` is not an identity check across delete and recreate.** ext4
reuses a freed directory inode immediately, so a per-level proof can be
satisfied by a different directory. **Bites when** the target is created after
the proof — so a probe could redirect the descent into an attacker-made
directory, but could not get pre-existing user data into it.

**Leaks, the affordable half of every trade here.** Measured after a
`wr mod --cmd` between the run and the cleanup: `ActualCwd` is `""`,
`CleanupAll` returns nil having deleted nothing, 7 paths are left below
`Cwd/<AppName>_cwd`, and a `run` behaviour executes in `Cwd`. Also open: a
`.muxfys`-prefixed decoy made by a mounting Job's Cmd keeps that workspace alive
forever; `CacheBase: "."` makes cleanup a silent no-op; a `.hold` file left by a
killed `mkHashedDir` blocks `removeEmptyParents`; a wr-created mount dir ABOVE
the workspace is tidied by neither path.

**Known open items, none of them data loss.** `client.go:1711`'s unproven
`os.RemoveAll(tmpDir)` (same principal as the Job's own Cmd, so no privilege is
gained); `cmd/add.go` storing `--cwd` unnormalised, so a relative-`Cwd` job's
cleanup and `run` are refused at exit rather than rejected at input;
`calculateHashedDir`'s panic on a short key, unreachable while both callers pass
32-char hashes; `Behaviours.Trigger` running OnSuccess before OnExit, so
`--on_success cleanup --on_exit run` refuses; `mergeBehaviours` sharing one set
of `*Behaviour` structs across a `wr mod` batch, harmless until something
mutates a `Behaviour`; `removeAllExcept` re-resolving multi-component relative
paths without per-level handles, needing a nested mount plus a race won by the
Job's Cmd; and `Cwd == "/"` being accepted, which makes the whole filesystem the
boundary but still requires the created shape below it.

## How this was verified, and what it cost

**Thirteen adversarial rounds ran.** Rounds 1 to 12 were numbered in one
sequence with a second, unrelated bug family (see *What is not here*); round
12's findings were all in that family, so it has no section in this PR's
checklist. A thirteenth ran after the two PRs were split.

**Most rounds found their bug inside the previous round's fix.** Round 2's two
findings were both defects in round 1's fixes; round 3 broke both of round 2's;
round 4 found another defect in round 3's; round 5 found round 4's fix had a
third call site it had not converted; round 6 refuted the claim round 5's
refactor was built on (`workspace.go:40` said nothing that can delete reads
`j.Cwd` or `j.ActualCwd` again — `Behaviour.run` did); round 8 found round 7's
base-component check said only "this is *some* wr workspace"; round 9 found
round 8's mountless change had bought a permanent leak; round 11 found round
10's `wholeWorkSpace` knew one of the two spellings a directory can have; the
thirteenth found cleanup asking what KIND of thing was at a name where `run`
asked its IDENTITY.

**The "simplifications" that each traded one failure mode for another.** This is
the pattern to recognise, because each looked like a tidy-up at the time:

- Proving `ActualCwd`'s last component and resolving symlinks (LAYER 8's fix for
  escapes 1 and 2) created escape 3, by handing a resolved path to the recursive
  delete.
- Keeping a symlink-resolving fallback so a symlink staying inside base was
  still deletable (LAYER 5) left a deletion route through `Job.Unmount`'s walk;
  deleting the concept (LAYER 21) made the contract stronger and the code
  shorter.
- A leaf-name check (LAYER 22) was defeated by appending one path element and by
  tolerating a non-existent directory (LAYER 25); its residual, written up as "a
  user directory literally named `cwd` would still pass", measured as the whole
  of `Cwd/analysis` destroyed — `run.R`, `README`, `results/final/out.tsv`,
  everything (LAYER 29). Recording a residual without measuring its blast radius
  understated it.
- The "reported working directory must exist" check both missed the branch where
  the workspace was missing too (LAYER 27, plus a TOCTOU a plain racing
  goroutine won 66 of 300 runs) and broke idempotence, leaking the workspace
  including `tmp` (LAYER 38).
- Fixing "deletes too little" by feeding every resolved mount point to
  `rmEmptyDirs` (LAYER 24) deleted a user's empty directory inside their `Cwd`
  (LAYER 26), and the guard added for THAT was derived from an unproven
  `ActualCwd`, making it a no-op in exactly the poisoned case this PR exists for
  (LAYER 28).
- Removing the mountless fast path (LAYER 48) handed a name rule to the jobs it
  does not describe (LAYER 53).

**The root cause of that pattern, named by round 5's refactor.** Safety was
re-derived from the raw `j.Cwd` / `j.ActualCwd` strings at EIGHT independent
sites — `Behaviour.cleanup`, `provenWorkSpace`, `mountDirsToKeep`,
`entryLeadingTo`, `Job.createdWorkSpace`, `Job.rmEmptyMountDirs`,
`Job.mountPoints` and `mountState.resolveCacheDir` — each slightly differently:
some absolutised and some did not, some proved the components were real dirs and
some did not, some required the working directory to exist and some did not.
Patching one site at a time is what kept producing the next bug. The second
recurring shape is **the same rule stated twice**: `keptEntry`'s working-dir
clause, `relIsCreatedCwd`'s duplicate containment check, and `relIsCreatedCwd`
itself were each found by a mutation coming back GREEN, and each was deleted
rather than covered.

**Mutation-testing discipline.** Every guard must have a test that fails without
it, and **a mutation must COMPILE before its verdict counts** — `go vet` after
each — because a build failure reports zero failing assertions and looks exactly
like a guard that does nothing. That mistake was made in five separate rounds
(an unused variable in round 2, unused imports or variables in rounds 6 to 9),
which is why the rule is written down. The harness restores production files
from a snapshot taken before it starts, never from git, after a
`git checkout -- jobqueue/` in one round silently reverted uncommitted work. 157
mutations are recorded across the eight rounds that ran them (27, 23, 24, 26,
15, 15, 18 and 9), a few belonging to the other PR's half.

**Guards found load-bearing but untested — what the mutations bought.** In round
5, two guards had NO coverage at all: `createdWorkSpace`'s `isRealDirBelow` and
`provenActualCwd`'s must-exist check. The whole suite stayed green while a
user's directory was deleted. Later rounds added tests for
`relIsCreatedCwd`'s leaf-name check; `HasSuffix` versus `Contains` on the base
component; the depth check and the hashed-dir comparison, both masked by the
other conditions of the same predicate; `IsDir` for a working directory that had
become a regular FILE, masked in all three consumers; `execDir`'s nothing-held
check, without which the no-`/proc` warning fires for every ordinary Job;
`dirIsAtOrAbove` resolving the second of two spellings; and `isMkTempName`'s
digit check, whose mutation survived the entire suite. Three more were pinned
after the last recorded round: `setActualCwd`'s refusal to write `ActualCwd` on
a `CwdMatters` Job, `wr status`'s display for a job carrying the v0.37.0|1
poison, and `openLeaf`'s refusal to open a workspace that reappeared after the
proof. Nothing failed when any of the three was removed.

**Fixtures had to be rebuilt.** 17 workspace fixtures were 2 to 3 directories
deep, i.e. shapes wr cannot produce; they now go through helpers that build the
path `mkHashedDir` really builds for that Job, so the Job (its `Cmd` and
`MountConfigs`, since both reach `Key()`) has to be settled before the path.
Fixtures that could not occur in production are a large part of why these holes
survived three rounds of review, and three had to be renamed when the
base-suffix check started refusing them earlier: a fixture that no longer
reaches its guard is a test that has quietly stopped testing anything. No
regression test was weakened or deleted in any round; the ones that encoded the
bug were corrected with the reasoning recorded in place.

**Two process mistakes worth keeping.** A `make race` failure was re-run before
its output was captured, twice, losing the evidence. And a new test first
reported PASS because it was invoked with `-run TestBehaviours`, which does not
match `TestBehaviourCleanupSafety`; a deliberate always-fail probe proved the
block had never executed. A test that passes without running looks exactly like
a test that passes.

**Gates and cost.** `make test`, `make race` and `make lint` at every step, with
`make race` added to the local gate set at the first rebase because CI runs both
and a build-tag error on a sibling branch had got past `make test` + `make
lint`. The suite went from 353 tests at LAYER 1 to **391 passed / 9 skipped**,
`make lint` `0 issues.` The whole PR is 107 commits, 46 files, +9888/-797 lines,
with 1862 lines of checklist behind it.

## What is not here

**The lost-job run-identity work is on PR #562** (branch
`fix-lost-job-run-identity`, checklist `.docs/bugfixes/260831-1.md`). It is a
pre-existing family about which RUN of a job a manager's decision is carried out
on: a lost job's behaviours acting on its own live retry, a pin taken after the
decision it belongs to, a recovered run's token. In code, `runToken`,
`Job.runID`, `Job.isLostRunLocked`, `Server.mintRunToken`, the reserve-time run
reset, `Started` carrying the working directory, the pin taken at `markJobLost`,
`killLostRun`/`killRunningJob`, `releaseKilledLostRun` and
`jobqueue/lost_job_behaviours_test.go`. Rounds 9 to 12 each fixed findings from
both families at once, which is why LAYERs 51, 54, 56 (round 11's) and 57, and
the whole of round 12, are absent here. The families met only in that both read
a Job's reported working directory.

**Other work found here and shipped elsewhere.** The flaky
`TestSubscriptionReconnectResync` went to PR #560
(`.docs/bugfixes/260829-3.md`); the client-hang-on-shutdown bug (LAYER 12) and
`mergeBehaviours` keeping only the last modifier entry per trigger (LAYER 14) to
#557; and two 1-in-10 flakes (LAYERs 10 and 11), possibly artifacts of a box
that was out of disk, to the runnermode-hardening branch.

**Reading the checklist's numbers.** Every round's gate counts, test counts and
mutation tallies were measured on the combined tree before the split and left as
recorded, so they read higher than this branch's own; and round 10 and round 11
each number a LAYER 56, because the two families were numbered in one sequence.

**Two places the checklist is behind the tree.** LAYER 3's cleanup filter in
`JobViaJSON.resolveBehaviours` has since been removed, leaving
`Job.dropImpossibleCleanups` as the one authoritative rule (a POST's response is
built from the queue's own copy, so it cannot show an unfiltered job either).
And the three guard pins named above were added after the last recorded round,
taking the suite from 388 to 391.
