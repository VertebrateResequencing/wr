# Bugfixes 2026-09-09

fix-env-propagation

Three defects in how wr propagates an environment into a job, recorded but not
fixed by `origin/record-env-propagation` (`.docs/bugfixes/260903-7.md`, commit
`00011cf8`). All three were re-verified against `develop` at `0cc79218` before
this branch started; the record's line numbers had all moved, so this file names
functions instead.

Deliberately no `file.go:NNN` references anywhere below: PR #591 shipped with
stale ones after 3 rebases, and Copilot caught it. Function and identifier names
stay findable.

- [x] 1. `envOverride` (`jobqueue/utils.go`) overrides only the FIRST
  occurrence of a duplicated variable name, because it DELETES the map entry as
  it uses it:

  ```go
  if replace, do := override[pair[0]]; do {
      env[i] = replace

      delete(override, pair[0])
  }
  ```

  A second entry with the same name therefore keeps its original value, and no
  appended copy is added either, because the append loop at the end only walks
  what is left in the map.
    - `os.Environ()` normally has no duplicates, but a Job's stored environment
      is whatever the client sent, compressed at add time, and that CAN have
      them.
    - It matters because the overrides on this path are wr's OWN guarantees,
      not user preferences: `TMPDIR`, `HOME` for `--change_home`, the bsub
      `PATH` prepend, and `WR_MANAGER_HOST`/`PORT`.
    - Which value the command then sees is up to the child: libc `getenv`
      returns the first match, so the override wins; some shells and runtimes
      rebuild their environment last-wins, so the stale one wins. Either way wr
      has stopped controlling the answer.
    - Nothing needs the entry deleted.

- [x] 2. `cmd/runner.go` accumulates `envOverrides` ACROSS reserve-loop
  iterations. The slice is declared once, outside the loop, alongside
  `exePath`, and appended to inside it: for each job the runner reads that job's
  own `PATH` and, if it does not already contain the runner's exe directory,
  appends `PATH=<this job's PATH>:<exePath>`. Nothing resets it between jobs.
    - The victim is not the job that appends. `envOverride` builds its map
      last-wins, so a job that appends its own `PATH` gets the right one. It is
      the job that appends NOTHING: one whose `PATH` already contains `exePath`
      skips the append, and `EnvAddOverride` is then handed the PREVIOUS job's
      `PATH` line, so the command runs with an unrelated job's `PATH` and can
      resolve the wrong binaries.
    - A runner executes many jobs in sequence, so one job with a different
      `PATH` poisons every later job in that runner that would otherwise have
      needed no override. The slice also grows one `PATH=` entry per job for
      the life of the runner.
    - Fix shape: a per-iteration copy of the base overrides. The base
      (`WR_MANAGERHOST`, `WR_MANAGERPORT`, `WR_MANAGERCERTDOMAIN`) is genuinely
      loop-invariant; the `PATH` line is not.

- [x] 3. `containerEnv` (`jobqueue/job.go`) splits `K=V` on ":" and truncates
  the value. It means to reduce the job's overridden environment to the NAMES
  of the variables, so `DockerRunCmd` can emit `-e NAME` and let docker copy
  the value in from the environment. It splits on the wrong character:

  ```go
  parts := strings.Split(envvar, ":")
  names[i] = parts[0]
  ```

    - Entries are `K=V`, so for `PATH=/usr/local/bin:/usr/bin` the "name"
      becomes `PATH=/usr/local/bin`, and `dockerEnv` emits
      `-e PATH=/usr/local/bin`. Docker treats `-e K=V` as an ASSIGNMENT, so a
      `--with_docker` job runs with its `PATH` truncated at the first colon,
      and likewise for any other colon-bearing value: `LD_LIBRARY_PATH`,
      `MANPATH`, `PYTHONPATH`.
    - A value with no colon is passed as a full `K=V` assignment too, which
      happens to be harmless only by accident.
    - User experience: a containerised job cannot find binaries that are on its
      `PATH` outside the container, and the "command not found" says nothing
      about the environment.
    - Splitting on "=" and taking `parts[0]` is what the function's own doc
      comment describes.
    - Fixed with `name, _, _ := strings.Cut(envvar, "=")`. `strings.SplitN(envvar,
      "=", 2)` was tried first and `make lint` rejected it: `Magic number: 2, in
      <argument> detected (mnd)`. `Cut` is the better answer anyway - it is the
      idiomatic form for "split on the first separator", allocates no slice, and
      needs no named constant.
    - An existing test had CODIFIED the bug and had to be corrected, which is
      the part worth knowing about. `That can include additional mounts and env
      vars` asserted `-e FOO=bar -e OOF=rab`. Both fixtures are colon-free, so
      the old `Split(envvar, ":")[0]` returned the whole `K=V` string and the
      test locked that in. `git log -S` shows the split and the assertion
      arrived together in `f7995da3` (Nov 2021), and the doc comment written in
      that SAME commit already said the function returns variable NAMES - so
      the code contradicted its own doc from day one and the test froze the
      contradiction. Three later commits reshuffled the assertion without
      re-examining it. Corrected to `-e FOO -e OOF`; it still asserts the same
      2 variables, the same arg form, the same position in the full command and
      the same order-agnostic check, losing only the value text, which was the
      wrong part.
    - The correction is a real behaviour change for COLON-FREE values, from an
      explicit `-e K=V` assignment to `-e K` copy-from-environment, so it was
      checked empirically rather than argued. The reviewer traced
      `Client.Execute` -> `prepareCommand`/`buildExecCmd` -> `cmd.Env =
      job.Env()`, where `Job.Env()` runs the stored env through
      `applyEnvOverrides` -> `envOverride`, so the shell that runs `docker run`
      already holds the overridden environment that `-e K` copies from. It then
      replicated those exact steps in a throwaway test against real docker
      29.1.3 and `alpine:latest`:

      ```
      with the fix:      -e PATH -e COLONFREE      IN_PATH=[/opt/wrtest/bin:/opt/go/bin:...:/bin]  IN_COLONFREE=[plainvalue]
      with it reverted:  -e PATH=/opt/wrtest/bin   IN_PATH=[/opt/wrtest/bin]                       IN_COLONFREE=[plainvalue]
      ```

      So the colon-free value survives BOTH forms, and only the colon-bearing
      one was being truncated. No override is dropped. `container/run.go`'s own
      doc comments already stated this contract; the fix restores documented
      behaviour rather than inventing it.
    - Red proved by mutation: reverting to the ":" split turns 2 assertions red,
      the new one at the `-e PATH=/usr/local/sbin` symptom and the corrected one
      at `-e FOO=bar -e OOF=rab`.
    - Edge cases, each run rather than reasoned: `FOO=a=b` -> `FOO`; `FOO=` ->
      `FOO`; `NOEQUALS` -> the whole string, unchanged from before, and docker
      silently omits a name it cannot resolve (exit 0, verified); `=value` ->
      the empty name, which docker rejects with exit 125. No guard added for
      the last: the old code was equally fatal there, so it is not a
      regression, and the right home for that check is input validation in
      `SetEnvOverride`, not here.
    - Singularity is untouched: `containerRunCmd` returns on the
      `WithDocker == ""` branch BEFORE reaching `containerEnv`, and a repo-wide
      grep including tests shows `containerEnv` has exactly one caller.
      `SingularityRunCmd` takes no env argument at all.
    - Interaction to keep in mind while doing item 1, found by the reviewer:
      `-e K` is now sensitive to item 1's bug. If a job's stored env has a
      duplicated name and an override targets it, `envOverride` replaces only
      the first and leaves the stale second, and `os/exec` dedups `Cmd.Env`
      LAST-WINS (verified: `[FOO=new, BAR=x, FOO=stale]` yields `stale`). So
      docker would copy the stale value, where the old `-e K=V` forced the
      override's. It needs a duplicated stored env to bite, and item 1 fixes
      the cause - but item 1 now protects this path too, not just its own.
    - Pre-existing typo noted, not fixed here: "envionrment" in
      `containerEnv`'s doc comment. Worth sweeping when that comment is next
      touched.

## Item 1, as fixed

- Stopping the delete was not enough on its own. The trailing append loop USED
  the delete as its signal for "this override went unused", so removing it
  alone would have appended a second copy of every override already applied,
  turning a stale-value bug into a duplicate-entry bug. That signal is now an
  explicit `applied map[string]bool`, marked in the replacement loop.
- The append loop now ranges the caller's `over` slice rather than the map,
  reading each value back by name. Two gains in one change: `applied` already
  provides the skip test, so iterating the map bought nothing, and iterating
  the input makes the appended ORDER deterministic - caller order, where it
  used to be Go's randomised map order. Marking `applied` as it appends stops a
  name given twice in `over` being appended twice, while `override[name]`
  keeps the existing last-wins value.
- `envName(envvar string) string` extracted using `strings.Cut`, now that name
  extraction happens 3 times in the function. Verified behaviourally identical
  to the `strings.Split(envvar, "=")[0]` it replaces across `"" "=" "=v" "A"
  "A=" "A=1" "A=1=2" "==" "a b=c"` and a NUL-containing name: zero mismatches.
- Output length is IDENTICAL to the old implementation's, not merely no larger.
  The reviewer ran both side by side over 13 shapes; every one reported
  SAME-LEN. The old code appended exactly the names never matched in `orig`
  (the un-deleted map remainder); the new code appends exactly the names not in
  `applied`. Same set.
- Regression test `TestEnvOverrideDuplicates` (`jobqueue/utils_test.go`) drives
  `Job.Env()`, not `envOverride`. That is the exported call whose result
  becomes `cmd.Env` in `Client.Execute`, and it is the only seam where the 2
  halves of the bug meet: a stored environment that came from a client, which
  CAN hold duplicates unlike `os.Environ()`, and an override applied to it.
- Red proved 25/25, deterministic in both directions:
  `Expected ["WR_DUP=fresh" "WR_OTHER=other" "WR_DUP=fresh"]` against
  `Actual [... "WR_DUP=staler"]`. Post-fix, 25/25 green.
- CORRECTION to the implementor's own report: it described the SECOND Convey as
  intermittently red "five passes and one failure in six". The reviewer
  measured 4 failures in 25, about 16%. The Convey that actually guards the bug
  is the first, and that one is fully deterministic. Recorded because an
  intermittent red is only an intermittent guard, and the distinction between
  the 2 Conveys matters.
- A line had NO test behind it, found by the reviewer deleting it: the append
  loop's `applied[name] = true`. The whole gate stayed green without it, yet
  its absence makes the result GAIN an entry and breaks the length invariant
  above. Not hypothetical either - `cmd/runner.go` accumulates `envOverrides`
  across its reserve loop (item 2, still open), so a repeated name in `over`
  happens in production today. Closed with a third Convey, proved red under
  that exact mutation:
  `Actual [... "WR_NEW=two", "WR_NEW=two"]`.
- The interaction with item 3 was settled against real docker 29.1.3, not
  argued. A Job whose stored env names `WR_DUP` twice, with an override for it,
  run through the same wiring `Client.Execute` uses:

  ```
  with this fix:      job.Env() = [WR_DUP=fresh, WR_OTHER=other, WR_DUP=fresh]   container prints "fresh"
  utils.go reverted:  job.Env() = [WR_DUP=fresh, WR_OTHER=other, WR_DUP=staler]  container prints "staler"
  ```

  So item 3's `-e NAME` form really was handing the stale duplicate to the
  container, and this fixes it. The `os/exec` last-wins dedup was confirmed in
  the same run by the host's own `printenv`.
- Aliasing checked, pre-existing and harmless: `env := orig` then `env[i] = ...`
  mutates the caller's backing array, and the fix now writes to MORE indices
  than before. Every call site was traced - `applyEnvOverrides`,
  `EnvAddOverride`, `envWithRunDirs`, `addBsubEnv` - and each passes a slice it
  already owns.
- No existing test asserted the old behaviour; the test diff is insertions
  only.
- `goconst` rejected the first test draft for 3 occurrences of a literal, the
  same class of surprise as item 3's `mnd` rejection. Fixed with a
  function-local `const` block.
- Tooling problem, pre-existing and NOT caused by this branch, confirmed
  independently by 2 agents and by running it on `HEAD`'s copy:
  `cleanorder -min-diff jobqueue/utils_test.go` hoists `const testJobKey`,
  `const mkHashedDirSweepTries` and 2 interface assertions to the top of the
  file, stranding `TestCreatedCwdDepthMatchesMkHashedDir`'s doc comment
  hundreds of lines from its function. Both agents reverted it and hand-placed
  their additions. `make lint` is `0 issues.` either way, so nothing enforces
  cleanorder's preference here. Someone should decide whether that file takes
  the reorder as its own commit or whether cleanorder is simply wrong about
  test files that document a constant through the test above it.
- Tidies noted, both out of scope for this item: `Job.containerEnv()` in
  `jobqueue/job.go` still inlines a 4th copy of the name extraction that
  `envName` now owns, and `jobqueue/job_test.go`'s either-order hedge for 2 env
  vars is now dead code, since the append order is deterministic.

## Item 2, as fixed

- `cmd/runner.go` gained `jobEnvOverrider{base, exePath}` with one method,
  `overridesFor(env) []string`, owning exactly the 2 things previously declared
  outside the reserve loop. The PATH-scanning loop and its append moved into the
  method verbatim. Each call returns `slices.Clone(j.base)` plus that job's own
  `PATH` line, so nothing survives into the next job.
- Why a struct and not a plain function, which is the interesting design point:
  a pure `jobEnvOverrides(base, exePath, env)` CANNOT go red for this bug.
  Extracting one silently fixes it, because the accumulation lives in the
  caller's `envOverrides = append(...)`, and that line sits in a cobra `Run`
  closure needing a live manager, scheduler and `jq.Execute` to reach. The
  state that persists across jobs has to be inside the seam for a test to prove
  it gone. The reviewer checked this reasoning and agreed, while noting the
  implementor's "could NOT go red" was too absolute: a pure function CAN be
  red-tested for "the base you hand me is not mutated", which is a different
  half of the property.
- Behaviour preservation verified line by line by the reviewer: the guard
  `len(overrider.base) > 0` is equivalent to the old `len(envOverrides) > 0`
  (`base` is non-empty iff `rserver != ""`, and the old in-loop append was
  itself gated by that same condition so could never be what first made the
  slice non-empty); both release paths, both `warn` texts and both exit reasons
  are byte-identical; `job.Env()` and `job.EnvAddOverride` happen at the same
  points.
- Red proved, and reproduced independently by the reviewer: 3 of 5 Conveys go
  red under the accumulating mutation.

  ```
  Line  99  Expected "/opt/wrtest/bin:/jobtwo/bin"   Actual "/jobone/bin:/usr/bin:/opt/wrtest/bin"
  Line 112  Actual [...base..., "PATH=/jobone/bin:/usr/bin:/opt/wrtest/bin"]
  Line 120  Actual [...base..., "PATH=...", "PATH=...", "PATH=..."]
  ```

  Line 99 is the poisoning, line 112 is the no-PATH victim, line 120 is the
  growth: 3 jobs, 3 entries.
- The growth is fully fixed, not just the wrong value. `overrider.base` is only
  appended to during setup; after 3 jobs a 4th still gets exactly the 3 base
  entries.
- A test gap the reviewer PROVED and we closed: the test was blind to the
  `slices.Clone`. Replacing it with `overrides := j.base` left the whole `cmd`
  package green. The mechanical reason is capacity, and it is worth knowing:

  ```
  production-shaped base len/cap: 3 4   (var base; 3 appends onto nil -> cap 1,2,4)
  test-literal base len/cap:      3 3   ([]string{a,b,c})
  ```

  In production `cap > len`, so a no-clone `append` writes into the shared
  backing array and 2 successive results alias. The test's composite literal
  had `cap == len`, so every append reallocated and the aliasing could not
  occur AT ALL. Doubly blind, since `EnvAddOverride` also compresses each
  result before the next call.
- Closed by building the fixture the way the runner builds it - appending to
  the field one at a time - and asserting one job's overrides still hold their
  own values after a later job's call. Exactly one leaf goes red under
  `overrides := j.base`, with
  `Expected "PATH=/jobone/bin:..." Actual "PATH=/jobthree/bin:..."`, and the
  other 5 stay green, which is itself the proof that they were blind.
- A linter rule would have re-created the blindness, which is worth recording:
  `prealloc` rejects `var base []string` followed by appends, suggesting
  `make([]string, 0, 3)` - and that gives `cap == len`, the very shape that
  hides the bug. Sidestepped by appending to the struct field instead, which
  `prealloc` does not inspect and which matches production more closely anyway.
- Tidies taken: the assertion on the private `overrider.base` field was dropped
  as implementation-detail coupling, with no coverage lost because the
  following behavioural assertion fails just the same on a grown base; and the
  poisoned-run Convey was renamed to say that it depends on an earlier job.
- The `break` in the PATH search is right and item 1 is what makes it right. It
  takes the first `PATH` entry, matching libc `getenv`; with a duplicated
  `PATH`, item 1 now replaces every copy with the same value, so the
  first-match read and `os/exec`'s last-wins dedup agree. They could disagree
  before item 1.
- Item 2 removes the production source of repeated names in `over` that item
  1's write-up cited when justifying `applied[name] = true`. That guard is
  still correct and still needed, since a user can repeat a name through
  `wr mod --env`, and item 1's third Convey still covers it - it simply no
  longer has this caller behind it.
- Pre-existing quirks found and deliberately left, neither a regression:
  `strings.Contains(pair[1], exePath)` is a substring test rather than a
  path-element one, so `PATH=/opt/wrtest/binx` counts as already containing
  `/opt/wrtest/bin`; and with `PATH=A` (already containing exePath) followed by
  `PATH=B` (not), the `break` skips the append and exec's last-wins gives `B`
  without exePath.
