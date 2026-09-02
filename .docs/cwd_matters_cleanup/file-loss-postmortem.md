# Postmortem: `wr` deleted a user's scripts directory

**Status:** fixed on branch `fix-cwd-matters-cleanup-deletes-parent` (PR #558).
**Affected releases:** v0.37.0 and v0.37.1.
**Introduced by:** commit `5993460`, "Solve #98: add live job introspection (#530)", 2026-06-24.

Throughout, statements are marked **[FACT]** where they are established from the
code, the git history, or a measurement I ran, and **[CONJECTURE]** where I am
reasoning about intent or causes I cannot observe. The distinction matters most
in section 5, which is largely conjecture by nature.

---

## 1. What happened

**[FACT]** A user ran, in
`/lustre/scratch124/pam/projects/.../scripts_ciseqtl/wr_RunCisEQTL`:

```
wr add -f wr_input_cmds_RunCisEQTL.txt -i RunCisEQTL_maineffects -r 0 \
       --cwd_matters --queue "normal" --memory "8G" -o 2
```

19,632 jobs. Part way through the run, the **parent** directory
`scripts_ciseqtl` was recursively deleted: every `.R`, `.sh` and `.README` file,
a 1.2 MB `.rds`, and the sibling results directory `wr_eqtl_chr1_varyinggexpcs1to50`.
The directory itself still existed and contained only an empty `wr_RunCisEQTL`,
both stamped with a fresh mtime — which is why it looked like a partial deletion
rather than a total one.

**[FACT]** The user did nothing wrong. The only `rm` in their whole session
removed a stray commands file in a different directory. `05_RunCisEQTL.R` — the
script every job was running — was among the deleted files, which is why the
remaining jobs then failed.

---

## 2. The mechanism

**[FACT]** `Job.ActualCwd` is the field that licenses deletion. Its meaning:

- empty → wr created no working directory for this job; delete nothing;
- non-empty → wr created this unique directory below `Cwd`, so **its parent** is
  a disposable workspace containing `cwd`, `tmp` and mount caches.

`Behaviour.cleanup` acted on exactly that:

```go
if j.ActualCwd == "" {
    return nil
}
workSpace := filepath.Dir(j.ActualCwd)   // the PARENT
cleanupWorkSpace(j, workSpace)           // os.RemoveAll
```

**[FACT]** For a `--cwd_matters` job there is no wr-created directory: the job
runs directly in the user's own `Cwd`, so `ActualCwd` must stay empty. The
contract is documented on `JobEndState.Cwd` (`jobqueue/client.go:2586-2593`):

> The cwd you supply should be the actual working directory used, which may be
> different to the Job's Cwd property; **if not, supply empty string.**

**[FACT]** #530 added live job introspection so the web UI could show a running
job's working directory. It built each touch snapshot from `cmd.Dir`:

```go
liveState := newExecuteLiveState(cmd.Dir, liveStdout, liveStderr)   // client.go:1466
```

For a `--cwd_matters` job, `resolveWorkingDir` sets `cmd.Dir = job.Cwd` and
returns `actualCwd == ""`. So every touch — roughly every 30 seconds, for every
running job — shipped `Cwd` in a field whose contract says to send empty. The
server copied it in (`applyLiveSnapshot`, `serverCLI.go:355-362`), and from that
moment `filepath.Dir(ActualCwd)` was the user's parent directory.

**[FACT]** The trigger fired **server-side, on the first attempt**: when a job is
declared lost and confirmed dead, `killLostJobAndTriggerBehaviours`
(`server.go:4594-4623`) runs the behaviours against the server's own — now
poisoned — job object. At 19,632 jobs on LSF, one lost job is close to certain,
and one is enough. Afterwards `ensureCwdExists` recreated the bare `cwd`, which
explains the fresh mtimes.

**[FACT]** The behaviour that did it is the default. `--on_exit` defaults to
`[{"cleanup":true}]`, and `cmd/add.go` documented it as having *"no effect when
cwd_matters is true"*. The documentation was correct about the intent and wrong
about the code.

---

## 3. The gaps in the code

These are the conditions that turned one wrong assignment into data loss.
**[FACT]** for each.

**3.1 A load-bearing invariant with no owner.** `ActualCwd` was a plain exported
string. Four separate places derived it from a `JobEndState`, each with its own
copy of `if jes.Cwd != "" { j.ActualCwd = jes.Cwd }`. Nothing enforced "empty iff
`CwdMatters`". The invariant lived in prose, ~1,100 lines from the call site that
broke it.

**3.2 A sentinel encoding a *permission*.** `""` did not mean "unknown"; it meant
"you may not delete anything". A `string` cannot express that, and nothing at the
assignment site hinted at it.

**3.3 Deletion derived from a path rather than proven.** `cleanup` computed
`filepath.Dir(ActualCwd)` and deleted it, with no check that the result was
inside `Cwd`. A single wrong field value therefore relocated a recursive delete
anywhere on the filesystem.

**3.4 The dangerous default was armed everywhere.** Every job carried
`cleanup`, including the jobs where it was documented as inert. The loaded gun
was in the room for no benefit.

**3.5 The display path wanted the same field.** `wr status` and the web UI need
"where is this job running", which for a cwd-matters job *is* `Cwd`. That created
real pressure to populate `ActualCwd` for display — a legitimate need pushing
against a safety invariant, with nothing marking the conflict.

---

## 4. Why nothing caught it

**[FACT]** The change passed CI, review, and two releases.

- **The tests encoded the bug.** `liveExecuteJob` in `client_payload_test.go`
  sets `CwdMatters: true`, and the accompanying test asserted
  `So(states[0].Cwd, ShouldEqual, cwd)` under the name *"Execute sends stdout
  tails once per touch **from the actual cwd**"*. For a cwd-matters job there is
  no actual cwd — so the test asserted precisely the invariant violation, while
  reading like a correctness check.
- **No test exercised the combination.** cwd-matters **and** a lost job **and**
  the default cleanup. Each ingredient was individually well-tested.
- **The blast radius was invisible locally.** From `newExecuteLiveState(cmd.Dir)`
  to `os.RemoveAll` is three hops across three files. Nothing at the call site
  suggests a deletion is downstream.
- **The symptom was silent.** Cleanup returned `nil`. The only evidence was the
  user's missing files.

---

## 5. How the buggy change came to be written

This section is **mostly [CONJECTURE]**. I can read the diff and the surrounding
code; I cannot observe what the author was thinking. I have marked the few facts.

**[FACT]** The correct value was in scope on the line immediately above:
`resolveWorkingDir` returns `actualCwd` at `client.go:1454`, and `cmd.Dir` was
used at `:1466`. The fix is a one-word change.

**[CONJECTURE] The task framing selected the wrong variable.** The feature was
"show the working directory of a running job". `cmd.Dir` *is* the working
directory — it is the honest answer to the question as posed. `actualCwd` answers
a different and less obvious question: "which directory did wr create for this
job, if any". An implementer optimising for the stated feature reaches for the
first. Nothing about `cmd.Dir` announces that a neighbouring variable carries a
safety meaning.

**[CONJECTURE] An existing transport was reused because its field name matched.**
`JobEndState` already travelled from runner to server on every touch and already
had a `Cwd` field. Reusing it is good engineering by every visible signal — no
new type, no new plumbing. The trap is that `JobEndState.Cwd` is not "a cwd"; it
is "the wr-created cwd, or empty". Field-name similarity concealed a semantic
difference, and the doc comment recording it was on the type, far from the use.

**[CONJECTURE] The invariant was invisible at the point of decision.** To know
the assignment was wrong you must hold three things at once: that `cmd.Dir` is
`job.Cwd` when `CwdMatters`; that `JobEndState.Cwd` must be empty in that case;
and that the server copies it into a field that authorises deletion. Each is
documented somewhere; none is visible where the choice is made. This is the
classic shape of a defect that survives review — every individual step looks
right.

**[CONJECTURE] The tests were written to confirm the new behaviour, not to
question it.** Having decided the snapshot carries the cwd, asserting
`states[0].Cwd == cwd` is the natural test, and it passes. That the fixture was
`CwdMatters: true` was, I suspect, invisible — the helper was pre-existing and
its name (`liveExecuteJob`) does not mention it. The test then locked the bug in
and would have failed a correct implementation.

**[CONJECTURE, but supported] This is not a "careless LLM" story.** The same
failure modes recurred repeatedly *during the fix*, including in my own work,
which is evidence that they are structural rather than a lapse:

- **[FACT]** I deferred the TOCTOU hardening saying it needed "Go 1.26" — while
  `go.mod` already said `go 1.26.3`. I asserted a fact I had not checked.
- **[FACT]** I described the mount defect as "silently useless". The user
  challenged it; on inspection it breaks a documented guarantee and can reach a
  user's remote filesystem. I had asserted severity without measuring it.
- **[FACT]** My own fix for one escape *introduced* another: `resolvedDirBelow`
  returned the **resolved** path, so a symlink pointing inside `Cwd` redirected a
  recursive delete onto `Cwd/userdata`. That is the identical class of error as
  the original bug — a value that looks like the path you asked about but isn't.
- **[FACT]** A first attempt at bounding a timeout **doubled** the hang it was
  meant to remove, because it capped with the client's *connect* timeout (120 s)
  rather than the socket's floor (60 s). The tests could not catch it: at the
  200 ms budget they use, the buggy term is masked.
- **[FACT]** I blamed a CI failure on our own commit from a two-head
  correlation. Counting the actual managers refuted it; the real cause was a
  pre-existing port-checker defect that held 7,120 sockets to find a range of 4.
- **[FACT]** Chasing a flaky concurrency assertion, I concluded its 500 ms poll
  was undercounting a parallelism that really was occurring, and rewrote the
  measurement to be exact. The exact measurement **disproved my own hypothesis**:
  the peak really was 3 on a 4-core box. The test was racing the runner spawn
  rate, not mismeasuring it. I had inferred the cause from the shape of the code
  rather than from data, and only found out because the fix I chose happened to
  produce the number that refuted it.
- **[FACT]** From a single CI sample I fitted a per-job cost to a startup time
  and concluded the server had a real O(history) term. Three local runs put the
  ratio at ~1.0. A two-point fit through one noisy measurement is not evidence,
  and I had presented it as though it were.

The pattern in all eight: **a plausible local inference, unverified, in a place
where the cost of being wrong is not visible from the code you are looking at.**

The last two are the most instructive, because they cost nothing and were caught
the same way: by measuring the thing rather than reasoning about it. Both times
the reasoning was sound given what was visible, and both times it was wrong. That
is the same epistemic position the author of `newExecuteLiveState(cmd.Dir, ...)`
was in — the difference is only that a wrong guess about a test's timing gets
refuted by the next test run, while a wrong guess about which variable holds a
deletion permission does not get refuted until a user loses their files.

---

## 6. What the fix changes

**[FACT]**, summarised; details in `.docs/bugfixes/260828-3.md`.

1. **One writer.** `Job.setActualCwd` is now the sole `JobEndState`-derived
   writer and refuses any value when `CwdMatters`. It deleted four copies of the
   old conditional, so the mistake cannot be made by copying a neighbour.
2. **Deletion is proven, not derived.** `realDirBelow` proves the path is a real
   directory strictly inside `Cwd`; a `provenDirs` type is the *only* way to
   reach the deletion helpers, which no longer accept loose strings.
3. **Deletion is pinned.** Removals go through `os.Root` handles held from the
   descent, so a path swapped after the check cannot redirect them — this also
   removed the walk's repeated path resolution (18 → 8 `openat`s per cleanup).
4. **The gun is unloaded.** Cleanup behaviours are no longer stored on
   cwd-matters jobs at all — on add, on modify, and on DB load, so jobs already
   poisoned by v0.37.x are sanitised when read.
5. **The tests that encoded the bug were corrected**, and the combination that
   caused the incident is now covered directly, with a 44-case probe asserting
   nothing outside a job's own workspace is ever deleted.

---

## 7. Recommendations

1. **Prefer types over prose for invariants that authorise destruction.** The
   `provenDirs` type is the load-bearing change; the doc comment it replaced had
   been correct and ignored for two releases.
2. **Treat "no effect in case X" in documentation as a smell.** If a behaviour is
   inert in a configuration, don't store it. The gap between "inert" and
   "catastrophic" was one wrong field.
3. **Test the combination, not the ingredients.** cwd-matters, lost jobs and
   cleanup were each well covered. Nothing crossed them.
4. **Be suspicious of a test whose name asserts something the fixture
   contradicts.** *"from the actual cwd"* on a `CwdMatters: true` fixture was the
   bug, stated aloud, in English, and reviewed twice.
5. **For AI-written changes specifically:** require that any change touching a
   field consumed by a destructive path names the consumer in its description.
   The author cannot see three hops downstream, and neither, in practice, can the
   reviewer.
6. **Verify severity claims before recording them.** Several of the deferrals in
   this work — including two of mine — were wrong on facts that took one command
   to check.
