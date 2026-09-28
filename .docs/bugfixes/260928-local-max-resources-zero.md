# 260928: `--max_ram 0 --max_cores 0` still runs jobs locally

Branch `fix-local-max-resources-zero`, based on `origin/develop` at `8cb5ff5`
(#633).

- [x] `wr manager start --max_ram 0 --max_cores 0` still ran jobs locally. The
  help text says 0 prevents local jobs, but jobqueue/scheduler/local.go
  `detectResources` treats values ≤ 0 as "no cap". (Found in a reviewer's real
  manager run.)
  - Red: `go test ./cmd -run TestManagerStartLocalLimitHelp -count=1` exited 1:
    `Expected 'maximum number of local cores to use to run cmds; -1 means
    unlimited, 0 allows only 0-core jobs' to contain substring 'local
    scheduler, 0 or -1 mean' (but it didn't)!`
  - Decision: the help text was wrong, not the local scheduler.
    - The flag defaults are `runtime.NumCPU()` and the machine's memory, so the
      CLI alone could tell 0 from unset. `jqs.ConfigLocal`, which the flags feed,
      cannot. Its `MaxCores` and `MaxRAM` are plain ints, and since they were
      added in `09176c3e` (2018, #122) they have been documented as "Values
      below 1 are treated as default". Every library and test caller builds
      `&ConfigLocal{Shell: "bash"}` and relies on the zero value meaning the
      whole machine. That includes client/testing, jobqueue/doc.go, the
      jobqueue tests and the dbcompat generator. Making 0 mean "none" would
      stop all of them running anything.
    - `09176c3e` gave the flags the help "for local scheduler, maximum number
      of cores to use; 0 means unlimited". `1dc08d74` (2020, "Allow 0 core jobs
      to run when --max_cores 0 is used") changed only cloud/server.go,
      cmd/cloud.go and jobqueue/scheduler/openstack.go. It rewrote the shared
      manager help to describe the openstack meaning of 0. The 0.23.0
      CHANGELOG entry for that change starts "When using the OpenStack
      scheduler", which confirms the change was only meant for openstack.
    - The openstack scheduler honours 0 through `MaxLocalCores` and
      `MaxLocalRAM`, which are `*int` so that 0 can be told from unset
      (`clampLocalLimit` in jobqueue/scheduler/openstack.go). `wr cloud deploy`
      defaults `--max_local_cores` and `--max_local_ram` to -1 and passes them
      on as `--max_cores` and `--max_ram`. That path was correct and has not
      changed.
    - The repo's own tooling already works on the real behaviour.
      developers/wrdev.sh starts a manager that must run nothing with
      `-s local --max_cores 1 --max_ram 1`, not 0.
  - Fix: cmd/manager.go `--max_cores` and `--max_ram` help now states the
    local meaning (0 or -1 mean the whole machine) and the openstack meaning
    separately. The `manager start` long help says how to keep commands off
    the machine: use lsf, use openstack with `--max_ram 0`, or run
    `wr manager pause` to stop new commands starting. The `ConfigLocal` field
    docs now say that 0 cannot be used to stop local jobs. No behaviour
    changed, and default configs run local jobs as before.
  - Tests: `TestManagerStartLocalLimitHelp` (cmd/manager_test.go, red to
    green) checks both flags' help. `TestLocalResourceLimits`
    (jobqueue/scheduler/scheduler_test.go) pins the local behaviour this help
    now describes: `MaxCores` and `MaxRAM` of 0 and -1 give the machine's full
    `NumCPU` and memory, and 1 caps both. It passes before and after the fix,
    so the default `ConfigLocal` keeps running jobs.
  - CHANGELOG: Unreleased, Fixed.
