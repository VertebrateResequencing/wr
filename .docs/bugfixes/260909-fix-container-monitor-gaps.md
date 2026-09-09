# Bugfixes 2026-09-09

fix-container-monitor-gaps

The remaining items from `origin/record-container-monitor-gaps`
(`.docs/bugfixes/260903-8.md`, commit `bdd17d4`). Items 1, 2 and 3 of that
record are already fixed and merged, as #589, #587 and #590 respectively.

FOUR items remain, not the two that were expected: 6 and 7 were open too. Each
was re-verified against `develop` at `0cc79218` before this branch started.

No `file.go:NNN` references below. PR #591 shipped with stale ones after three
rebases and Copilot caught it; this file names functions instead.

- [x] 4. A container that outlives its `docker run` keeps the job key as its
  name, and then every retry of that job fails immediately.
    - `containerRunCmd` (`jobqueue/job.go`) passes `j.Key()` as `--name`.
      `--rm` covers the normal case, but a container that outlives its client
      keeps the name: the runner SIGKILLed, the host lost, the daemon restarted
      mid-run.
    - wr only removes a container via `KillContainer` from `killCmd`, which
      needs a live runner that has already identified it. Verified still true:
      a repo-wide grep for `ContainerRemove` / `RemoveContainer` outside tests
      finds nothing.
    - So after a lost run, every retry dies at `docker run` with "The container
      name ... is already in use", and the message names an opaque
      32-character key rather than the job. The job is effectively unrunnable
      until someone removes the container by hand.
    - Two halves to weigh: making the retry work, and making the failure
      legible if it still happens.
    - Constraint: #589 gave the container a
      `--label uk.ac.sanger.wr.job-key=<key>` and its monitor matches on that
      label, so whatever is done to the NAME must keep that correlation
      working.

- [x] 5. `SingularityRunCmd` contradicts its own doc comment.
    - The comment promises "The CWD is always mounted at / in container". The
      implementation emits only `cat %s | singularity shell%s %s`, with `-B`
      for each explicitly configured mount and nothing else: no bind for the
      working directory, and no `--pwd`. Verified unchanged on `develop`.
    - So the command runs wherever singularity puts it, with whatever the
      site's `singularity.conf` binds. Typical site defaults bind the user's
      real `$HOME` read-write and NOT wr's working directory - the opposite of
      what a wr job wants. The job writes to its home instead of its workspace,
      and `--change_home` has no effect inside the container.
    - The record is careful about what it can prove, and so should the fix be:
      the singularity-default half is a claim about site configuration, not
      about this repo. The missing bind and the missing `--pwd` are in the
      code, and those are what this item owns.
    - `DockerRunCmd` already does the equivalent: `-w "$PWD"` plus
      `--mount type=bind,source="$PWD",target="$PWD"`.

- [x] 6. A cid-file glob can read a large data file once a second, and inflate
  the job's recorded PeakRAM.
    - `GetContainerByPath` falls back to `cidPathGlobToContainer` when the
      configured path is not an existing file, and each glob match goes through
      `cidPathToContainer` -> `file.GetFirstLine` -> `file.ToString` ->
      `os.ReadFile`. Verified: `GetFirstLine` reads the WHOLE file into memory
      and then trims one trailing newline.
    - `findContainerID` runs on the 1-second resource ticker for as long as no
      container has been identified, so a `--monitor_docker` glob matching a
      large output file - `*.txt`, `out*` - re-reads that file every second for
      the life of the job.
    - The record corrects its own first telling of where the cost lands, and
      the correction matters: `currentMemory(job.Pid)` measures the job's own
      process tree, not the runner, so the read does not land there. It lands
      through the runner's own footprint, which wr deliberately adds to the
      job's peak (`ourmem, _ := ownMemoryMB()` then `peakmem += ourmem`). So a
      big glob match inflates the job's recorded PeakRAM, and with it the RAM
      wr reserves for its next run.

- [ ] 7. `container/docker/docker_test.go` uses the developer's real docker.
    - `pullUbuntuImage` calls `cli.ImagePull(ctx, "ubuntu", ...)` against
      `client.FromEnv`, and the tests start containers named `container_1` and
      `container_2` in that same daemon. Verified all three still present.
    - Running the package's tests therefore pulls
      `docker.io/library/ubuntu:latest` into the developer's real docker,
      re-pointing the `ubuntu:latest` tag if they had built their own, and
      takes names that may collide with theirs.
    - A test that needs a real daemon should at least use a digest-pinned image
      and unique names.

## Item 5, as fixed

- The code was made to match the promise, not the other way round, and the case
  is stronger than this item put it: there were TWO pre-existing promises, both
  verified by the reviewer and both predating the branch.
  * `cmd/add.go`'s `with_singularity` help: "The container is created with cwd
    mounted and set to current directory inside the container", dating to
    `f7995da` (Nov 2021), the commit that added container support.
  * `jobqueue/job.go`'s `WithSingularity` field doc: "Cwd will be mounted
    inside the container and will be the working directory in the container."
  A repo-wide grep finds exactly 4 such statements - 2 docker, already true,
  and these 2. So every user-facing and API-facing thing wr said already
  described the fixed behaviour, and fixing only the doc comment would have
  left the text users actually read still lying.
- New `singularityWorkDirArgs` mirrors `workDirMountArgs`, emitting
  `-B "$PWD" --pwd "$PWD"` before the explicit `-B` mounts. `$PWD` stays a
  double-quoted shell expansion for the reason `260907-1.md` established:
  `shellquote.Join` would emit `'$PWD'` and kill the expansion.
- CORRECTION to the mechanism the implementor gave, which the reviewer
  disproved empirically. It argued "mounted at `/`" cannot be literal because
  binding CWD over `/` would shadow the image root and leave no `/bin/sh`. Not
  so - singularity-ce 4.1.1 accepts the bind and makes it a SILENT NO-OP,
  mounting the source and then layering the rootfs over it:

  ```
  $ echo 'pwd; ls /' | singularity shell -B "$PWD/bindroot:/" docker://alpine
  bin dev environment etc home lib ... usr var
  EXIT=0
  ```

  The image root survives and `/bin/sh` runs. The conclusion is unchanged and
  actually stronger: the comment's literal reading is not implementable at all,
  so it can only ever have been the symptom written down as the mechanism -
  when a site binds nothing, singularity simply STARTS the process at `/`.
- `--pwd` rather than `--cwd`, deliberately. 4.1.1's help presents `--cwd` as
  the flag and `--pwd` as its synonym, so the naive choice is `--cwd` - but
  `--cwd` is the NEWER name (SingularityCE 3.11 / Apptainer 1.1) while `--pwd`
  goes back to Singularity 2.x and works on every release. A
  `with_singularity` job runs against whatever singularity is on the worker
  nodes.
- The existing real tests were STRUCTURALLY BLIND to this, which is why it
  survived: they build their working directory under `/tmp`, which singularity
  binds by default, so the job landed in the right place by accident. Proved by
  the reviewer running the unfixed line both ways:

  ```
  no fix, no NO_MOUNT  -> /tmp/.../home + home.file   exit 0   (the old tests' world)
  no fix, NO_MOUNT set -> /  + ls: *.file not found   exit 1   (a hostile site)
  ```

- The new `TestRunRealSingularityWorkDir` therefore sets
  `SINGULARITY_NO_MOUNT=cwd,home,tmp`, which is singularity's own `--no-mount`
  and so genuinely stands in for a site whose `singularity.conf` binds none of
  them. That `t.Setenv` is itself load-bearing: with the fix reverted AND the
  setenv removed, the test passes.
- Verified against real singularity, not asserted: with the fix the container
  prints its wr working directory and lists the file placed there; without it,
  `/` and `ls: *.file: No such file or directory`. Also driven with a working
  directory containing a space, and one containing `;rm -rf x&*'q'` - the path
  is printed verbatim, nothing executed, nothing removed.
- Known and accepted: the real test CANNOT discriminate `--pwd`. Under
  `SINGULARITY_NO_MOUNT`, `-B "$PWD"` alone already lands in the working
  directory, because singularity defaults its cwd to the host cwd once that
  path exists in the container; only the string assertions cover `--pwd`. It is
  kept because it states the contract explicitly, mirrors docker's `-w`, and
  does not depend on a default that varies by version and with `--contain`.
- Two failure modes checked for regression risk, both benign: `--pwd` at a path
  that does not exist in the container (a site with `user bind control = no`,
  where `-B` is ignored) does not hard-fail, singularity falls back to `$HOME`;
  and a duplicate bind, where the user's own `container_mounts` also names the
  cwd, warns and proceeds.
- `jobqueue/job_test.go` asserts the same command string in 3 more places,
  which this item did not name. Updated, and every changed assertion got
  LONGER: `ShouldEqual` stayed `ShouldEqual`, `ShouldEndWith` stayed
  `ShouldEndWith` with a longer suffix, and no `ShouldContainSubstring` was
  introduced anywhere.
- CHANGELOG entry added under `### Fixed`, not `### Changed`: no documented
  contract moved, the code caught up with one that had been wrong since 2021.
- The test's red output now names the directory the container actually started
  in, so a future regression reads `output: "/\n"` rather than only
  `exit status 1`.
- Residual limitation, confirmed present in DOCKER too and therefore left for
  consistency: a working directory containing a comma breaks `-B "$PWD"`
  (`unable to add ... to mount list`), exactly as it breaks
  `--mount type=bind,source="$PWD"` (`invalid field 'me' must be a key=value
  pair`). `Job.containerMounts()` already splits `ContainerMounts` on comma, so
  the assumption is baked in upstream.

## Item 4, as fixed

- `DockerRunCmd`'s line is now prefixed by `removeStaleContainerCmd(name)`,
  which removes a container matching BOTH filters, `AND`ed by docker:
  `name=^<name>$` says it is in our way, and
  `label=uk.ac.sanger.wr.job-key=<name>` says it is ours.
  `regexp.QuoteMeta` on the name, because docker's `name` filter is a regex.
- Name and label are UNCHANGED, both still `j.Key()`, so #589's correlation is
  untouched. The reviewer proved that independently by driving the real
  `Operator` over the real docker `Interactor` in `setupDockerMonitor`'s order:
  after the stale container is removed, both `GetNewContainerByName` and
  `adoptLabelledNewContainer`'s label loop resolve to the same new container.
- Design, with the rejected options recorded because they look attractive:
  * a UNIQUE NAME per attempt fixes the retry without any destructive call,
    but leaves the orphan alive FOREVER - wr's only removal path is
    `KillContainer` from a live runner that has already identified the
    container, and there is no reaper - so orphans accumulate on worker nodes.
    It also races the retry, since `mkHashedDir` is deterministic on the key
    and both land in the same `$PWD`. And it moves the monitor surface,
    because `containerRunCmd` sets `MonitorDocker = j.Key()` and
    `findContainerID` takes the by-name path for `--with_docker` jobs.
  * DETECTING docker's error and renaming it never makes the job runnable, and
    means string-matching another tool's prose.
  * DOING IT IN GO is blocked: `Interactor.ContainerList` passes `All: false`,
    so an EXITED leftover is invisible to the Go client, and widening that
    listing is exactly what the monitor's new-container diff is built on.
- CORRECTION to that last point as first reported: "the Go client cannot see
  the leftover at all" is true only for an exited one. A RUNNING leftover IS in
  the baseline the monitor remembers - benign, and in fact helpful, since being
  remembered it can never be adopted.
- Safety, verified by the reviewer against real docker 29.1.3 across 8 cases,
  with an unrelated bystander present throughout. It could not be made to
  remove anything it should not:

  ```
  running leftover, our label                 -> removed
  exited leftover, our label                  -> removed
  our name, ANOTHER job's label               -> survives, run fails as before
  our name, no label at all                   -> survives, run fails as before
  our label, different name                   -> survives
  name wrrevF, container wrrevFextra          -> survives (the ^...$ anchors)
  job wrrev.G, container wrrevXG              -> survives (regexp.QuoteMeta)
  job wrrev.G, container really named wrrev.G -> removed
  ```

- Shell safety: 8 hostile names driven through the real line under `/bin/sh`
  (`evil; touch`, `evil$(touch)`, backticks, embedded newline, `evil*`, quote
  breakouts) created ZERO files. The failure in each case is docker's own
  "Invalid container name". This does not rely on `j.Key()` being hex.
  `260907-1.md`'s trap is avoided because the `$` here is MEANT to be literal,
  unlike `$PWD`.
- Failure modes of the prefix are all clean no-ops with the exit status still
  `docker run`'s, checked against the bare line: filter matches nothing (bash
  and dash), docker absent from PATH, daemon down, `docker ps` failing,
  `docker ps` printing a non-id. It introduces no `|`, so
  `buildExecCmd`'s `set -o pipefail` trigger is unchanged.
- The still-running case is deliberate, and the justification was verified
  against code rather than accepted: `wr retry` only touches buried jobs;
  `jobConfirmedDead` re-runs only when the command pid and the runner pid are
  both confirmed dead, and the command pid IS the shell running `docker run`;
  `backstopKillWedgedRunner` kills the runner and command but NOT the
  container, which is what manufactures the leftover; and
  `killLostJobAndTriggerBehaviours` latches, so one manager never has 2 live
  runners for a key.
- The LIMIT of that, now stated in the code rather than asserted away:
  `Job.Key()` is derived from Cwd, Cmd, mounts and image and names no manager,
  so 2 managers on one docker host running the identical command in the
  identical Cwd share a key, and either could remove a container the other has
  running. Before this change the second merely failed loudly. Mitigating: that
  pair already shares the same deterministic `mkHashedDir` working directory
  and is already corrupting each other. A second additive label carrying a
  manager id would close it without disturbing #589, and is NOT done here.
- That overstatement had reached 4 places. All 4 corrected:
  `removeStaleContainerCmd`'s comment, `WithDocker`'s field doc, `CmdLine`'s
  doc bullet, and `cmd/add.go`'s terminal help - the last being the only copy a
  wr USER sees, so it keeps the caveat in the user's own vocabulary, with no
  mention of the label or the key. The CHANGELOG's trailing "cannot prove is
  its own" became the literal consequence of the 2 filters.
- Blocking test defect found by the reviewer and fixed, and it would have
  broken CI rather than this box: `startTestContainer` read the container id
  from `CombinedOutput()`, but `docker run --detach` writes the id to STDOUT
  and any image pull to STDERR, so on a machine without `alpine:latest` cached
  the "id" is the pull progress and the assertion compares against
  `"Unable to fi"`. `realTestSetup` never pre-pulls, so that is a fresh
  runner's normal state. Proved by `docker save`, `docker rmi`, run, then
  `docker load` - and the fix proved the same way, with the image id and repo
  digest verified byte-identical afterwards.
- `regexp.QuoteMeta` had NO test: removing it left `./container/...` and
  `TestJob` green, on the most destructive change here. Closed with an exact
  string case for a name containing a metacharacter, which also pins the
  asymmetry that makes the 2 filters do different jobs - the name filter
  escapes, the label filter carries the name raw.
- The co-tenant Convey, honestly flagged by its author as green before and
  after, IS load-bearing: it goes red under the mutation that drops the label
  filter, which is precisely the mutation that would make the change
  destructive.
- Legibility: the item claimed the failure "names an opaque key rather than the
  job". Only docker's half does - wr's `loggableCmd()` already puts the user's
  own command line in front of it, and `wr status` prints `StdErr:` alongside
  the `Cmd`. So no error sniffing was added. When wr does destroy something it
  says so on the job's own stderr:
  `wr: removing container 08a4951e32a5, left behind by a lost run of this same command`.
- Left alone deliberately, and worth its own item: exit 125 is classified
  `FailReasonExit`, "command exited non-zero", though the command never ran.
  Special-casing 125 would misclassify a real job that legitimately exits 125,
  and telling the 2 apart needs the stderr sniffing that was avoided.

## Item 6, as fixed

- A FUNCTIONAL BUG the record did not spot, and the more user-visible half:
  `GetFirstLine` never returned a first line. For any multi-line file it
  returned everything with ONE trailing newline trimmed, so
  `"id1\nsome other output\n"` came back as `"id1\nsome other output"` and
  never matched a container id. A cidfile with any trailing content therefore
  left the job unmonitored, silently.
- Its consequence, confirmed at the monitor level by the reviewer: a cidfile
  containing `jobs\ntrailing junk\n` now yields `containerID="jobs"` and that
  container's memory is charged to the job - AND the container is SIGKILLed
  with the job, inside `killCmd`. So the fix widens what wr kills, which is why
  it earned its own CHANGELOG entry rather than being folded into the memory
  one.
- The widening is bounded, checked rather than assumed: only a container that
  appeared AFTER `newDockerMonitor` remembered the baseline can be adopted, so
  #589's guard is untouched; the first line must equal a container id exactly,
  so a longer line can never match; and `GetContainerByPath`'s documented
  contract already said "if the first line contains the ID of a container".
- All 3 links of the memory chain verified. The harm was MEASURED: a 500 MB
  file read once a second took `ownMemoryMB()` from 2 MB to 957 MB, and the
  fixed version stays at 2. The reviewer independently confirmed the mechanism
  at 32 MB, 67 MB allocated for a 4-byte answer - about 2x the file, because
  `os.ReadFile` grows its buffer and `string()` copies it.
- CORRECTION to the record's implication that the inflation is permanent: Go's
  scavenger reclaims it after roughly 2 to 5 minutes. It does not matter,
  because `ownMemoryMB()` is called once after `cmd.Wait()`, within a second of
  the last 1-second tick, deep inside the inflated window.
- `GetFirstLine` itself was changed rather than only its caller, because no
  caller wants the old behaviour: it has exactly ONE non-test caller,
  `cidPathToContainer`. `ToString` is byte-identical and has NO non-test
  callers at all.
- Bound is a named `maxFirstLineBytes = 4096`, 64x the 64 hex characters a cid
  actually is, so no real cidfile can trip it. Past the bound it returns
  `ErrLineTooLong` rather than a truncated prefix, because a 4096-byte prefix
  is not the first line and a caller cannot tell it from one. Zero `//nolint`
  in the diff - `mnd` was satisfied by naming the constants, not silenced.
- A deliberate behaviour change, flagged by its author and kept: on the
  EXACT-PATH route a too-long first line is now counted as a failure, and after
  3 ticks wr warns and stops monitoring, where before it re-read for ever in
  silence. The job is unaffected - `resolveContainerMem` returns no error by
  design, so no `on_failure` behaviour fires, and `killCmd` is gated on a
  non-empty container id so nothing is killed.
- The asymmetry with the GLOB route, which skips a bad match silently, is
  correct rather than an oversight: a glob is EXPECTED to match non-cidfiles,
  while an exact path names one file the user asserted is a cidfile. 3 ticks is
  right because the condition is not transient - the same file gives the same
  answer every tick - and the latch is what stops one warning per second.
- Tests measure ALLOCATION, which is the harm itself rather than a proxy for
  it, via `TotalAlloc` deltas after a `runtime.GC()`. Deliberately not wall
  clock. Proved deterministic: 25 in-process runs plus 20 separate processes,
  zero failures, with a 60-200x margin below budget and a 3,300x gap from the
  broken value. Files are made 32 MB instantly with a sparse `Truncate`, so the
  tests cost nothing.
- 4 mutations, each red for its own reason: the old `GetFirstLine` body; the
  length guard removed; the length check reordered before the newline search
  (which pins the exact boundary, a 4096-byte line returned and 4097 rejected);
  and `cidPathToContainer` swallowing the error.
- 2 GoConvey traps hit and worth passing on: the default `FailureHalts` meant
  an allocation assertion silently never ran until it was moved first, so the
  first "red" proved only the wrong half; and asserting equality on a 32 MB
  string dumped 128 MB into the failure output, fixed with a `shorten()`
  helper.
- `PathReadError` gained an `Unwrap` so `errors.Is` works on it. Its sibling
  `OperatorError` already had one, so this is consistency rather than a new
  pattern, and no existing caller does `errors.Is`/`errors.As` on it.
- CHANGELOG failed review twice over and now says the truth: the old entry
  claimed wr "ignores" a too-long match, which is true only of the glob loop,
  and was framed as "a path WITH A GLOB IN IT", which excluded the exact-path
  route entirely. It is now 2 entries - the memory one covering both routes and
  saying the job itself is unaffected, and a separate one for the recognition
  fix that ends on the kill, which is the sentence a user needs.
- Caveat, contrived and non-blocking: `--monitor_docker mycontainer` resolves
  to `<cmdDir>/mycontainer`, so if the job's own working directory happens to
  hold a large file of that name with no newline in its first 4096 bytes, the
  latch now disables the BY-NAME lookup too. That setup was already inflating
  PeakRAM every second before this change.
