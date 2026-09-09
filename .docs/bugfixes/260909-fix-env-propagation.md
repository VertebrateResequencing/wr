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

- [ ] 1. `envOverride` (`jobqueue/utils.go`) overrides only the FIRST
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

- [ ] 2. `cmd/runner.go` accumulates `envOverrides` ACROSS reserve-loop
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
