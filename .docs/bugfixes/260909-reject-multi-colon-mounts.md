# Bugfixes 2026-09-09

reject-multi-colon-mounts

- [ ] A `ContainerMounts` spec with 2 or more colons is accepted and then
  silently means something the user did not ask for.
  `container.MountSpecPaths` (`container/run.go`) splits on ":" and returns
  `parts[0], parts[1]` ONLY when there are exactly 2 parts; for anything else it
  returns `parts[0], parts[0]`. So `/data/a:b:/mnt` yields source AND target
  both `/data/a`, and the `/mnt` the user asked for is dropped.
    - The validation `#587` added does not catch it. `containerMountsMessage`
      (`jobqueue/job.go`) checks that both returned paths are absolute, but
      for a 3-part spec both ARE `parts[0]`, so it inspects the same path twice,
      never sees the in-container one, and accepts the spec. The job then runs
      with a bind mount the user never asked for.
    - The behaviour is already recorded as a known open question in
      `MountSpecPaths`'s own doc comment: "What a 3-part spec should mean is a
      user-facing format question, so the behaviour is left as it was."
    - The repo owner has DECIDED: reject multi-colon mount specs. That decision
      is settled and is not to be re-litigated.
    - One place covers every path. `containerMountsMessage` is reached from all
      4 submission and modification routes: `jobqueue/serverREST.go`,
      `jobqueue/job.go`, and the modify routes at `jobqueue/job.go`
      and `:2317`.
    - Note for whoever writes the message: docker's real syntax allows a third
      field for options, such as `/a:/b:ro`. wr has never supported it - that
      spec is exactly the case that silently collapses to `("/a", "/a")` today -
      so rejecting it is not a removal of a working feature. The message should
      say what the supported format IS, not only that the value is wrong.
    - Fixed in `containerMountsMessage` (`jobqueue/job.go`), which is the one
      function all 4 routes reach, verified by the implementor rather than
      taken from the item: `jobqueue/serverREST.go` (REST add),
      `jobqueue/job.go` (`malformedAddJobMessage`, reached from
      `addValidationError` in `client.go` and from `serverCLI.go`),
      `job.go` (`modifiedContainerMountsMessage`) and `job.go`
      (`validationMessage`). A grep for every caller of both the method and the
      function confirms there is no fifth.
    - The check goes FIRST in the loop, before the absolute-path check,
      because `MountSpecPaths` answers a multi-colon spec with its local path
      twice, which would otherwise look like a pair of perfectly absolute
      paths.
    - Message, and it names the offending mount separately from the whole
      value, which matters when commas made the value multi-mount:

      ```
      ContainerMounts "/data/opt:/opt:ro" is invalid: mount "/data/opt:/opt:ro"
      has more than one colon; each mount must be /outside/container or
      /outside/container:/inside/container, and mount options such as ":ro" are
      not supported
      ```

      Traced to where a user sees it, not assumed: `cmd/add.go`/`:573`
      `die("%s", err)` on the Add error, and `Error.Error()`
      (`jobqueue/server.go`) renders it as
      `jobqueue add(<item>): bad request`.
    - Red command: a new case in `malformedAddTests()`
      (`jobqueue/client_payload_test.go`), the table whose doc comment says it
      holds one case per check `malformedAddJobMessage` makes.
      `TestClientAddErrorNamesTheProblem` runs every case through all 4 public
      Add entry points. Red before the fix at `Line 987`,
      `So(errors.As(err, &jqErr), ShouldBeTrue)` `Expected: true Actual: false`
      x4 - once per Add variant, because `err` was nil and the spec was
      accepted.
    - `MountSpecPaths` itself is UNCHANGED, deliberately, with 3 reasons:
      hardening it would centralise nothing, since `singularityMounts` never
      calls it and shell-quotes the raw spec straight into `-B`; rejection
      cannot be expressed in a 2-string return without breaking an exported
      signature that `dockerMounts` and `DockerRunCmd` depend on; and changing
      the >2-part return to `parts[0], parts[1]` would silently RELOCATE the
      bind mount of a job already stored with such a spec, which still loads
      and runs, while still dropping the option they asked for.
    - The residual gap is named rather than papered over: a Go API caller who
      builds a `Job` and calls `container.DockerRunCmd` directly, bypassing
      jobqueue validation, still gets the silent collapse. `MountSpecPaths`'s
      doc comment now records that as the deliberate contract, replacing the
      text that recorded it as an open question.
    - Bounds proved: a 1-part spec still works (`container/run_test.go`
      and `:196` both drive `/foo/car` with no colon, and
      `containerMountsWellFormed = "/data/set:/data,/other"` is the value every
      "is accepted" assertion uses); a valid 2-part spec is unchanged;
      `TestKeyByteIdentity` and `TestJob` pass, and the key path concatenates
      `ContainerMounts` verbatim and never calls `MountSpecPaths`; and jobs
      already stored with a multi-colon spec still load, since
      `db.recoverIncompleteJobs` and `server.go` do no validation and
      `modify_validation_test.go` already exercises that path.

- [x] **For the repo owner, before merging: this is a small capability
  REMOVAL for singularity users, not purely a fix.** The item above assumed a
  multi-colon spec is always the silent-collapse case. That is true only on
  the docker path, which goes through `MountSpecPaths`.
  `singularityMounts` (`container/run.go`) never calls it - it shell-quotes
  the whole spec and emits `-B '/a:/b:ro'`.
    - Checked against the real tool on this box rather than by reading code.
      `singularity exec --help`, singularity-ce 4.1.1:

      ```
      -B, --bind strings   a user-bind path specification. spec
                           has the format src[:dest[:opts]],
                           where src and dest are outside and
                           inside paths.
      ```

      So `/a:/b:ro` is a valid, WORKING singularity bind spec today, and this
      change rejects it at submission. Already-queued jobs keep running;
      only new submissions are refused.
    - Implemented as decided rather than narrowed to docker, because
      `ContainerMounts` is one field and its meaning should not depend on
      which image field is set: supporting options only for singularity is its
      own trap, and a user moving a job from singularity to docker would
      silently lose them. It is also why the message says "mount options such
      as \":ro\" are not supported" rather than only "invalid" - the
      singularity user is the one who will be surprised.
    - If the owner would rather reject only on the docker path, that is a
      small change to the same one function.
    - The reviewer did not stop at singularity's help text. It built a busybox
      sandbox and ran singularity-ce 4.1.1 for real:

      ```
      $ singularity exec -B $SRC:/mnt/x:ro sbox sh -c 'echo nope > /mnt/x/w.txt'
      sh: can't create /mnt/x/w.txt: Read-only file system   exit=1
      $ singularity exec -B $SRC:/mnt/x    sbox sh -c 'echo yes  > /mnt/x/w.txt'
      exit=0
      ```

      So `:ro` is honoured today on wr's singularity path. The removal is real,
      not theoretical.
    - CORRECTION to the argument this item first gave. It justified rejecting
      for singularity partly by "a user moving a job from singularity to docker
      would silently lose them". That trap is already covered EITHER way:
      `modifiedContainerMountsMessage` previews the modification, so
      `wr mod --with_docker` on a `:ro` singularity job would be rejected by a
      docker-only check too. The case for rejecting both runtimes therefore
      rests on 2 other grounds, which the reviewer independently reached and
      which are stronger:
      * the published contract is already 2-field only.
        `Job.ContainerMounts`'s own doc says
        `/outside/container/path[:/inside/container/path]`, and both
        `--container_mounts` helps say only "mount additional locations". The
        singularity `:ro` support is undocumented and accidental - it works
        solely because `singularityMounts` passes the raw string through. So
        this aligns behaviour with the published contract rather than removing
        a promised feature.
      * one field, one meaning: making acceptance depend on which image flag
        is set is its own trap.
    - Follow-up worth raising separately, and the honest resolution of the
      tension: support `:ro` PROPERLY on both runtimes - singularity `:ro`,
      docker `--mount ...,readonly`. That turns a capability removal into a
      capability everyone gets.

- [x] Selection deliberately does NOT reject a multi-colon value, and that
  asymmetry with `wr add` is correct rather than an oversight. Recorded
  because nothing else says so.
    - #588 gave `status`, `kill`, `remove`, `retry`, `suspend` and `resume` a
      `--container_mounts` SELECTION flag, and its
      `validateSelectionContainerFlags` does not call
      `containerMountsMessage`. The value flows verbatim into
      `JobEssence.ContainerMounts` and `keyForCwd` concatenates it verbatim;
      `MountSpecPaths` is never involved. Run for real:

      ```
      $ wr kill -l 'sleep 300' --with_docker img --container_mounts /a:/b:ro
      EROR  No matching jobs found
      ```

    - It must stay that way: selection is the only command-line route to a
      LEGACY job that was stored with such a spec plus an image, and rejecting
      it would leave those jobs reachable only by rep group or internal id. A
      value matching nothing already fails loudly.

- [x] Follow-ups from the review, both applied.
    - `MountSpecPaths`'s doc comment overclaimed. It said jobqueue rejects a
      multi-colon spec "as a job is added or modified, so only a job stored
      before that check existed can still carry one". Two holes, and the
      implementor refined the second one further than the review had:
      * `(*Job).containerMountsMessage()` (`jobqueue/job.go`) returns ""
        when the Job has no container image, and all 3 add routes reach that
        METHOD rather than the free function, so a job ADDED today with no
        image keeps its spec unchecked. Harmless, since mounts are inert
        without an image, and setting one later IS caught by
        `modifiedContainerMountsMessage`.
      * but MODIFY is guarded regardless of image, which the review had not
        separated out: `JobModifier.validationMessage()` (`job.go`) calls
        the FREE `containerMountsMessage` with no image gate whenever
        `ContainerMountsSet`, so `wr mod --container_mounts /a:/b:ro` on an
        image-less job is refused. So the only real holes are add-with-no-image
        and a Go caller of the `container` package, which jobqueue cannot
        guard at all.
      * The comment now says exactly that, in the same space.
    - Not closed, deliberately: the add-with-no-image hole. Closing it is a
      one-line change (drop the image guard from the method, or call the free
      function from `malformedAddJobMessage`), but the existing guard is
      deliberate and an image-less job's mounts are inert, so changing it is a
      behaviour decision for the owner rather than part of this fix.
    - CHANGELOG entry added under BOTH `### Changed` and `### Fixed`, because
      the change is genuinely both and the audiences differ: singularity users
      LOSE a working capability, which belongs in Changed where someone
      scanning for what might break them will look, and docker users get a
      silent wrong-mount defect fixed, which belongs in Fixed.

- [x] Copilot review of PR #591, both findings valid and both taken.
    - The CHANGELOG entry claimed "`wr add` and `wr mod` now refuse" a
      multi-colon value outright. `wr add` does NOT, when no image flag is
      given: all 3 add routes reach `(*Job).containerMountsMessage()`, which
      returns "" with no image. Release notes that overstate a rejection are
      worse than none, so the entry now says exactly which command refuses
      when: add with an image flag, mod whenever `--container_mounts` is set,
      and mod refusing to add an image to a job whose existing mounts have the
      problem.
    - The write-up carried hard-coded `file.go:NNN` references that were
      already stale, because this branch has been rebased twice - onto #588
      and #589, then onto #590 - and every rebase moved them. All 11 have been
      reduced to bare file names rather than re-pinned, since the next rebase
      would stale them again. Function and identifier names are what make a
      reference findable.

- [x] Sighting only, provably NOT caused by this branch:
  `TestReliable4RacBoundedBySchedulable`
  (`jobqueue/reliable4_rac_bound_test.go`, the
  `So(scanWork, ShouldEqual, limit)` at the "only the schedulable (limit) jobs
  incur the expensive prepareReadyJob work" Convey) failed on CI for
  `ec0966ca`: `Expected: 5 Actual: 19`. CI run 34342089929.
    - Decisively not this commit's: `git diff --name-only 5fc08d00 ec0966ca`
      is 2 `.md` files and nothing else, so the compiled code is BYTE-IDENTICAL
      to `5fc08d00`, on which the same CI `test` job had already passed. A
      commit that changes no code cannot change a test result.
    - Not reproducible on this box: 20/20 green plain, and 12/12 green under 6
      spinning CPU hogs. `make test` had also passed twice on this exact code
      locally, at `640 passed · 13 skipped`.
    - What the assertion means: the test pins that the scheduler does the
      expensive `prepareReadyJob` work for EXACTLY the number of schedulable
      jobs, which is the bound `.docs/bugfixes/260725-2.md` introduced the test
      to protect. CI measured 19 against a limit of 5, so more of the backlog
      was scanned than the bound allows.
    - So it is either a genuine bound violation that only a slower, contended
      machine exposes, or an assertion that is too tight to hold under
      scheduling jitter. Worth the repo owner deciding which, because the 2
      answers are very different: the first is a real performance regression
      hiding in a flake, the second is a test to loosen. `ShouldEqual` on a
      work COUNT is the kind of assertion that is exact by design here - the
      whole point of `260725-2` was that the count used to be the whole
      backlog - so it should not simply be relaxed without establishing which
      it is.
    - Remedy applied here: re-ran the CI job on the unchanged commit, as the
      repo does for its other load-sensitive tests. It PASSED, which confirms
      the failure is intermittent on CI rather than deterministic - but note
      that this does not distinguish the 2 explanations above, since a real
      bound violation exposed only under contention would also pass on a
      quieter runner. This is a NEW name for
      that family; it is not on the known list and no prior bugfix doc records
      it as flaky, only as a test that was added and passed.
