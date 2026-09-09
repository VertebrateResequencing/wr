# Bugfixes 2026-09-09

reject-multi-colon-mounts

- [ ] A `ContainerMounts` spec with 2 or more colons is accepted and then
  silently means something the user did not ask for.
  `container.MountSpecPaths` (`container/run.go:179`) splits on ":" and returns
  `parts[0], parts[1]` ONLY when there are exactly 2 parts; for anything else it
  returns `parts[0], parts[0]`. So `/data/a:b:/mnt` yields source AND target
  both `/data/a`, and the `/mnt` the user asked for is dropped.
    - The validation `#587` added does not catch it. `containerMountsMessage`
      (`jobqueue/job.go:1239`) checks that both returned paths are absolute, but
      for a 3-part spec both ARE `parts[0]`, so it inspects the same path twice,
      never sees the in-container one, and accepts the spec. The job then runs
      with a bind mount the user never asked for.
    - The behaviour is already recorded as a known open question in
      `MountSpecPaths`'s own doc comment: "What a 3-part spec should mean is a
      user-facing format question, so the behaviour is left as it was."
    - The repo owner has DECIDED: reject multi-colon mount specs. That decision
      is settled and is not to be re-litigated.
    - One place covers every path. `containerMountsMessage` is reached from all
      4 submission and modification routes: `jobqueue/serverREST.go:1676`,
      `jobqueue/job.go:1308`, and the modify routes at `jobqueue/job.go:2283`
      and `:2317`.
    - Note for whoever writes the message: docker's real syntax allows a third
      field for options, such as `/a:/b:ro`. wr has never supported it - that
      spec is exactly the case that silently collapses to `("/a", "/a")` today -
      so rejecting it is not a removal of a working feature. The message should
      say what the supported format IS, not only that the value is wrong.
    - Fixed in `containerMountsMessage` (`jobqueue/job.go`), which is the one
      function all 4 routes reach, verified by the implementor rather than
      taken from the item: `jobqueue/serverREST.go:1676` (REST add),
      `jobqueue/job.go:1308` (`malformedAddJobMessage`, reached from
      `addValidationError` in `client.go` and from `serverCLI.go:826`),
      `job.go:2283` (`modifiedContainerMountsMessage`) and `job.go:2317`
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

      Traced to where a user sees it, not assumed: `cmd/add.go:556`/`:573`
      `die("%s", err)` on the Add error, and `Error.Error()`
      (`jobqueue/server.go:510`) renders it as
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
    - Bounds proved: a 1-part spec still works (`container/run_test.go:169`
      and `:196` both drive `/foo/car` with no colon, and
      `containerMountsWellFormed = "/data/set:/data,/other"` is the value every
      "is accepted" assertion uses); a valid 2-part spec is unchanged;
      `TestKeyByteIdentity` and `TestJob` pass, and the key path concatenates
      `ContainerMounts` verbatim and never calls `MountSpecPaths`; and jobs
      already stored with a multi-colon spec still load, since
      `db.recoverIncompleteJobs` and `server.go:1735` do no validation and
      `modify_validation_test.go:313` already exercises that path.

- [ ] **For the repo owner, before merging: this is a small capability
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
