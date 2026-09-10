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

- [x] 4. NOT in the original record, found while fixing item 2 and escalated by
  its reviewer: **a one-typo command line crash-loops a scheduler group.** An
  environment entry that is a bare NAME with no "=" makes the runner panic.
    - `strings.Split("PATH", "=")` has length 1, and both
      `jobEnvOverrider.overridesFor` (`cmd/runner.go`), `Job.Getenv`
      (`jobqueue/job.go`) and `prependedPath` (`jobqueue/client.go`, on the
      bsub path) index `pair[1]`. CORRECTION: this item named only the first 2
      when filed; the 3rd was found by the implementor and confirmed by the
      reviewer, and fixing only the named 2 would have left the bsub path
      crash-looping:

      ```
      panic: runtime error: index out of range [1] with length 1
        cmd.(*jobEnvOverrider).overridesFor(...) cmd/runner.go
      ```

    - PRE-EXISTING, not introduced by item 2's extraction. Proved by running
      HEAD's own loop body, copied out of `git show HEAD:cmd/runner.go`, over
      the identical env: same panic, same line, same input.
    - Reachable from ordinary user input by 4 unvalidated routes, not the 1
      first reported (this item said 3 when filed; the implementor found a
      4th). Both `compressEnv(strings.Split(...))` sites take the value
      verbatim:
      * `wr add --env PATH` -> `JobDefaults.Env` ->
        `jobqueue/serverREST.go`'s `compressEnv(strings.Split(jd.Env, ","))`.
        This is the likeliest trigger and the one the implementor missed: the
        flag help says "comma-separated list of key=value environment
        variables", so a user writing `--env PATH` to mean "pass PATH through"
        poisons the job at ADD time.
      * `wr mod --env PATH` -> `JobModifier.SetEnvOverride` ->
        `compressEnv(strings.Split(newVal, ","))`.
      * `wr add` with a JSON line `{"cmd":..., "env":["PATH"]}`, and REST
        `POST /rest/v1/jobs` with the same body -> `JobViaJSON.resolveEnvOverride`.
        This is a SEPARATE route from `JobDefaults.Env` above, and is the 4th.
      * REST `PATCH /rest/v1/jobs/<ids>` with `env: ["PATH"]` ->
        `JobModifier.setEnvOverrideValues`. CORRECTION: this item said `PUT`
        when filed; `restJobs` dispatches PATCH to `restJobsModifyResponse`
        and rejects PUT as unsupported.
    - `Job.Env()` -> `applyEnvOverrides` -> `envOverride` then faithfully
      substitutes the bare `PATH` for the job's real `PATH=...` entry, or
      appends it if absent, and the runner walks into `pair[1]`.
    - Blast radius, which is why this is worth doing now. There is no
      `recover()` anywhere in `cmd`. The runner dies with exit 2. The deferred
      `jq.Disconnect()` closes the socket, so the reserved job is never
      touched, times out on `ServerTimings.ItemTTR` and returns to the queue as
      lost with `numrun` still 0 - so the manager spawns another runner, which
      reserves the same poisoned job and dies the same way. Each dying runner
      also takes down every other job it would have gone on to execute in its
      sequence.
    - Validation at entry is necessary but NOT sufficient, and this is the part
      that decides the shape of the fix: it does nothing for a job ALREADY
      STORED with a bare entry. Those exist in any database where someone has
      already typed it, and they would keep crash-looping after an upgrade. So
      both halves are needed:
      * reject an env element with no "=" at every write route, with an error
        that names the offending element and says what the format is; and
      * make the 3 `pair[1]` reads non-panicking, so a stored bad entry
        degrades instead of killing the runner.
    - The second half is NOT the symptom-suppression `implementation-principles`
      warns against, precisely because the first half is also being done. A
      guard alone would leave the malformed entry in place, leave the user with
      a silently broken `PATH` and no diagnostic, and leave `Job.Getenv`
      panicking. A read accessor that panics on stored data is its own defect.

## Item 4, as fixed

- Half A: one new `compressUserEnv` in `jobqueue/utils.go`, beside `envName`
  and `envOverride` which already own what an environment entry looks like. It
  validates then delegates to `compressEnv`. `SetEnvOverride` was reduced to
  call `setEnvOverrideValues`, which was its duplicate, so 4 routes are 3 call
  sites of 1 implementation. Each route was proved individually load-bearing:
  reverting any one of them to plain `compressEnv` reddens only that route.
- The check is deliberately NOT inside `compressEnv`, and this is the crux of
  the design. `Job.EnvAddOverride` re-compresses an environment ALREADY STORED
  on a job, and the runner calls it on every job it reserves. Validating there
  would make a legacy bad job fail `EnvAddOverride`, whose handler is
  `jq.Release(job, nil, "failed to add env var overrides")` then `break` - so
  the job returns to the queue and the runner exits, and the manager starts
  another. That converts half B's degradation into a GRACEFUL crash-loop: the
  same bug in a nicer coat.
- The reviewer proved that rather than accepting it, by moving the check into
  `compressEnv` and driving a legacy job through: `EnvAddOverride` returned
  `environment variable is not in key=value format: "PATH"`, where the shipped
  design returns nil.
- It also proved the loop is genuinely INFINITE, which this item asserted
  without proof. `Client.Release` passes `attempted=false`, and the server's
  `releaseSpendsARetry` is `rep.attempted || !job.StartTime.IsZero()`. A job
  released before execution has a zero `StartTime`, so no retry is spent,
  `UntilBuried` never reaches 0, and the job is never buried. `Client.Release`'s
  own doc says as much.
- End-to-end proof on 2 real managers. Pre-fix build, job added with
  `--env PATH`:

  ```
  lvl=eror msg="runCmd wait" cmd="... wr_head runner -s '1024:60:1:0:456997...'" err="exit status 2"
  lvl=eror msg="runCmd wait" cmd="... wr_head runner -s '200:30:1:0:456997...'"  err="exit status 2"
  poisoned : complete=0 running=1 ... buried=0
  ```

  Two runner deaths 2 minutes apart, job stuck running, never buried. Fixed
  build, same job: `complete=1`, exit code 0, the bare entry STILL stored, and
  the shell supplying its own default PATH.
- Half B: 3 reads, not the 2 this item named. `prependedPath`
  (`jobqueue/client.go`, reached from `Client.Execute` -> `addBsubEnv`) has the
  same `pair[1]`, so a `--bsub` job crash-looped identically and fixing only
  the named 2 would have left it alive. All 3 now use `strings.Cut` and skip an
  entry with no "=", matching what `getenv(3)` does with one.
- Degradations chosen: `overridesFor` skips it and keeps looking, so such a job
  behaves exactly like a job with no PATH - it does NOT synthesise
  `PATH=<exeDir>`, which would leave the command with a one-directory PATH,
  worse than the shell's fallback. It is also strictly better than before on
  `["PATH", "PATH=/real"]`, where the old code panicked on the first entry and
  the new one walks past it to the real one. `Getenv` returns blank, its
  existing answer for an absent variable. `prependedPath` falls through to its
  no-PATH branch.
- A SECOND pre-existing bug the same `Cut` fixes: `Split(...)[1]` truncated any
  value containing a further "=". CORRECTION to how this was first described -
  it was called a live bug on the grounds that `Getenv`'s only production
  caller reads JSON (`WR_BSUB_CONFIG`). The reviewer marshalled the actual
  struct: for a plain bsub job it is 1081 bytes containing NO "=" at all,
  because every `[]byte` field is nil and the string fields are empty. It bites
  only when a field carries one - `Requirements.Other["cloud_script"]` holding
  something like `export FOO=bar` is the realistic case, and then the JSON
  truncates mid-value, `json.Unmarshal` fails, `mountCouldFail` stays false,
  and a bsub child job is buried on a mount failure it should have tolerated.
  Reachable, not routine.
- Empty elements are rejected too, so `wr add --env "A=1,"` now errors. That is
  not merely tidiness: an empty element reaches `containerEnv` as the empty
  name and `dockerEnv` emits `-e ''`, which docker 29.1.3 rejects with
  `invalid argument "" for "-e, --env" flag`, exit 125. The old behaviour
  silently stored a value that made any `--with_docker` job fail to start.
  `--env "A=1,2"` likewise now errors rather than storing a bare "2".
- `=value` is rejected as well, on the reviewer's recommendation and verified
  rather than taken: docker gives the same exit 125 for it, and on the
  non-container path `os/exec` passes it to a child that has no name to look it
  up by, so it defines nothing either way. Item 3's own record above had
  already nominated this exact home for the check.
- 3 sentinels, not 1, because each input has a different remedy and a message
  carrying a remedy must not be reused for an input that remedy does not fit:

  ```
  "PATH"   -> "PATH": environment variable is not in key=value format; to pass a variable through, write NAME=$NAME
  ""       -> environment variable is empty; check for a stray or doubled comma
  "=value" -> "=value": environment variable has no name before the =
  ```

  The hint names the generic `NAME=$NAME` rather than interpolating the
  element, since the element is arbitrary user text and "write foo bar=$foo
  bar" would be nonsense; the element is already quoted at the front of the
  same line.
- Well-formed values are untouched, asserted across all 4 routes: `A=1`,
  `PATH=/usr/bin`, a value containing "=" (`WR_ENV_EQUALS=a=b`), a value
  containing ":" (`WR_ENV_COLON=/usr/bin:/bin`) and a legitimately empty value
  (`D=`).
- Gap named rather than papered over: all 4 checks are client-side, and the
  server stores whatever compressed `EnvOverride` bytes a client sends, so an
  OLDER wr client against an upgraded manager can still poison a job. Half B is
  what covers that. Validating server-side would mean decompressing every job's
  override on add.
- Not done, raised as its own item rather than folded in: `--env` is now the
  only comma-separated `wr add` flag that hard-errors on a trailing comma.
  `--limit_grps "a,"`, `--modules "x,"` and `--queues_avoid "z,"` all accept
  one silently (`cmd/add.go` does a bare `strings.Split` with no element
  validation). The asymmetry tracks blast radius rather than taste - an empty
  env entry crash-looped a scheduler group, an empty limit group appears to be
  inert - and removing it either way is a user-visible change to 3 unrelated
  flags. Anyone taking it should first measure what an empty limit group and an
  empty module name actually DO, since the right answer may differ per flag.
- Also left, non-blocking: nothing is logged when a read skips a malformed
  stored entry, so a legacy job degrades silently. A `warn` in the runner would
  close the last "no diagnostic" case this item complained about.

- [x] Copilot review of PR #592, both findings valid and both taken.
    - `envOverride`'s doc said it "returns the new slice", hiding that it
      assigns `env := orig` and mutates the caller's backing array in place -
      and this branch's fix makes it write to MORE indices than before. The
      implementor verified the behaviour rather than describing it from the
      code, running the real function over 5 shapes:
      * every replaced index is written through to `orig`, at EVERY duplicate
        index now;
      * when `cap(orig) > len(orig)` the append writes into `orig`'s own array
        past its length, overwriting what another slice over that array sees;
      * when the append must grow, the result is a fresh array and `orig` keeps
        the replacements but not the appends - so whether a caller sees
        appended entries is CAPACITY-dependent, and not something to rely on
        either way;
      * two results derived from the same over-capacity `orig` alias each
        other, which is precisely the class of bug item 2's `slices.Clone`
        guards against in `cmd/runner.go`.
      The doc now says a caller holding `orig` can assume its length is
      unchanged and nothing else.
    - A test comment documented behaviour THIS PR removed, which is the sharper
      finding: `TestEnvOverrideDuplicates`'s third Convey justified itself by
      citing `cmd/runner.go` accumulating overrides across the reserve loop -
      and item 2, 2 commits later on this same branch, deleted exactly that.
      Stale on arrival.
    - The correct remaining rationale was established, not invented, and the
      duplicate case is NOT dead: `wr mod --env "A=1,A=2"` is accepted. Item
      4's `compressUserEnv` rejects 3 shapes - empty, no "=", no name before
      the "=" - and says nothing about a name repeating, and all 4 write routes
      funnel through it. Proved by driving the exact call `cmd/mod.go` makes:
      `SetEnvOverride("A=1,A=2")` returns nil and the stored override decodes
      to `[A=1 A=2]`. Both of `envOverride`'s arguments can carry duplicates -
      `over` via `applyEnvOverrides`, and `orig` via `EnvAddOverride`, which
      the runner calls on every job it reserves.
    - CORRECTION to this item's own opening claim, and it is worth having
      exactly right: it said "`os.Environ()` NORMALLY has no duplicates". It
      can never have them. Go's `syscall.copyenv` blanks every duplicate key -
      the source comment is literally `// Clear duplicate keys.` - so it is a
      language-level guarantee, not a convention. Confirmed by `syscall.Exec`ing
      a binary with an `envp` containing the same key twice: the child's
      `os.Environ()` reported only the first. So the justification for item 1
      rests entirely on environments that came from a CLIENT, never on the
      ambient one, and `wr add`'s own `addEnvVars` returns `os.Environ()` and
      so contributes no duplicate either.
    - Checked and NOT stale, having been suspected: `cmd/runner_env_test.go`'s
      header describes the accumulation in the PAST tense, as the bug this PR
      fixed, which is accurate. A repo-wide grep plus a scan of every comment
      line added by this branch found no other instance in Go code.

- [x] Copilot review of the rebased head `cd99fc4a` (PR #592): `containerEnv`
  is a FOURTH read path and item 4 missed it.
    - Item 4 established that validation at the write routes cannot help a job
      ALREADY STORED with a malformed entry, so the reads must degrade, and it
      hardened 3 of them: `jobEnvOverrider.overridesFor`, `Job.Getenv` and
      `prependedPath`. `containerEnv` was not touched, so a legacy job whose
      stored overrides hold `""` or `=value` still produced
      `docker run -e ''`, which docker rejects outright.
    - This is a gap in the reasoning, not only the code: the principle was
      stated correctly and then applied to 3 of 4 places.
    - Proved end to end against real docker 29.1.3, through the actual
      `Job.CmdLine()` output:

      ```
      pre-fix   ... -e '' -e FOO -e '' -i alpine /bin/sh
                exit 125: invalid argument "" for "-e, --env" flag
      post-fix  ... -e FOO -i alpine /bin/sh
                exit 0: FOO is [bar]
      ```

    - One detail nearly let the bug survive its own fix: the slice was
      `make([]string, len(overrideEs))`, so skipping an entry would have left a
      trailing `""` - the same `-e ''`, at the end instead of the middle. It
      needed `make([]string, 0, len(...))` with `append`, which is also the
      shape `prealloc` wants.
    - Skips SILENTLY, matching all 3 siblings, rather than logging. Item 4
      recorded that silence as a known gap and named the runner as the single
      right place to close it for every read at once; a log here alone would be
      a 4th style, in a pure accessor with no logger, leaving the other 3
      silent.
    - One deliberate difference from the siblings, in the TEST rather than the
      treatment: they skip on "no `=`", this skips on an empty NAME. A bare
      `NOEQUALS` is a legitimate copy-from-environment argument - `-e NAME` is
      exactly the form this function exists to emit - and was re-verified at
      29.1.3 to give exit 0 with the variable unset. Skipping it would be a
      behaviour change with no bug behind it. `""` and `=value` both yield the
      empty name and are both covered.
    - Took 2 tidies the earlier records had nominated: `containerEnv` now uses
      the `envName` helper item 1 extracted, which item 3's write-up had
      already flagged as the 4th inlined copy, and the `envionrment` typo in
      its doc comment is fixed.

- [x] The branch owed a CHANGELOG entry and had written NONE, which the
  implementor caught rather than the orchestrator: `git log 098b2227..HEAD --
  CHANGELOG.md` was empty, for 4 user-visible fixes and 1 behaviour change.
    - Now 1 bullet under `### Changed` and 4 under `### Fixed`, grouped by what
      a USER would recognise as one thing rather than by item number.
    - Item 4 is deliberately SPLIT across both sections: the fix half ("you
      were bitten and now you are not") and the validation half ("your working
      command line may now error") go to different readers, and putting both in
      Fixed would hide a breaking change inside a list of repairs.
    - Items 4-fix and 5 are MERGED, because a user recognises one thing: an env
      entry that names no variable no longer breaks their job.
    - Items 1 and 2 are kept apart despite both being "wrong environment",
      because the triggers differ - item 1 needs a repeated name in your own
      environment, item 2 needs nothing from you beyond sharing a runner - so
      someone hitting one would not recognise the other's description.
    - The behaviour change leads the section, enumerates all 3 now-rejected
      shapes with a literal example of each, gives the remedy for the likeliest
      (`--env PATH=$PATH`), names all the affected routes including both REST
      verbs, and warns explicitly about a script that joins an `--env` list and
      leaves a trailing comma.

- [x] Tooling hazard found while doing this, worth knowing:
  `golangci-lint run --fix` silently rewrites a doubled apostrophe `''` inside
  a Go DOC COMMENT into a typographic close-quote, because gofmt's doc-comment
  reformatter treats it as a quotation. It then reports `0 issues.` having
  mangled the text. Anyone writing a shell-quoting example in a Go comment will
  hit it; reword to avoid `''` rather than keeping the mangled character.
