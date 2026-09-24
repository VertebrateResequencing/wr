# Bugfixes 2026-09-24

fix-lint-baseline-ref

- [x] `make lint`'s result depends on the local `master` branch, which nothing
  keeps current, and when that branch is missing it silently lints the whole
  tree.

  `.golangci.yml` set `new-from-rev: master`, so `make lint` reported only the
  issues that are new since the local `master` branch. `git fetch` moves
  `origin/master` but never a local branch, so an old clone keeps whatever
  `master` pointed at when it was made. When the rev does not resolve,
  golangci-lint prints one `level=warning` line and reports every issue in the
  tree. CI was not affected, because
  `.github/workflows/golangci-lint.yml` passed
  `GOLANGCI_LINT_ARGS=--new-from-rev=origin/master`.

  Repro clone: local `master` is the stale `3caeba4d`, and `origin/master` is
  `b2f0ff97`. Base: origin/develop `41a04a26`.

  - Red commands, before the fix (`golangci-lint cache clean` before each):
    - Stale local `master` (`3caeba4d`), `make lint`: exit 2, with two
      phantom issues that are already on `origin/master`:

      ```
      jobqueue/behaviours.go:323:21: Function 'run' has too many statements (21 > 20) (funlen)
      jobqueue/modify_validation_test.go:421:1: File is not properly formatted (gci)
      2 issues:
      * funlen: 1
      * gci: 1
      ```

    - Local `master` missing (`git branch -m master lintrepro-master-aside`,
      restored afterwards), `make lint`: exit 2 with `44 issues:`. The only
      sign of the cause is the first line:

      ```
      level=warning msg="[runner] Can't process results by diff processor: can't prepare diff by revgrep: could not read git repo: error executing \"git diff --color=never --no-ext-diff --relative master --\": exit status 128: fatal: bad revision 'master'\n"
      ```

    - A rev that cannot resolve at all,
      `make lint GOLANGCI_LINT_ARGS=--new-from-rev=origin/no-such-branch`: the
      same warning for `origin/no-such-branch`, then `44 issues:`, exit 2.

  - Design:
    - `.golangci.yml` holds the only default baseline, now
      `new-from-rev: origin/master`. It stays in the config rather than moving
      to the Makefile, because agents and developers also run
      `golangci-lint run --fix` directly. Without a rev in the config, that
      command would report, and `--fix` would rewrite, untouched code across
      the tree.
    - The Makefile does not repeat the rev. `LINT_BASE_REV` reads it from the
      config with `sed`. The last `--new-from-rev=<rev>` in
      `GOLANGCI_LINT_ARGS` replaces it, because golangci-lint also gives the
      command-line flag precedence over the config. So the rev that gets
      checked is the rev golangci-lint will use. A comment on the config line
      says that the Makefile reads it.
    - The `lint` target first runs
      `git rev-parse --verify --quiet '<rev>^{commit}'`. If that fails, it
      prints to stderr which baseline is missing, says to run
      `git fetch origin` or pick another rev with `GOLANGCI_LINT_ARGS`, and
      exits 1 before golangci-lint starts. It is plain POSIX `sh`, and it does
      no network access. If the config line were deleted, the rev would be
      empty and `'^{commit}'` would fail too, so that case also fails loudly.
    - CI now runs a plain `make lint`. Its `Fetch lint base` step still fetches
      `origin/master`, and the config supplies the same rev CI used to pass, so
      the baseline has one source. If CI ever lacked the ref, the check would
      fail the job rather than lint the whole tree.
    - Limitation: only the `--new-from-rev=<rev>` form of the override is
      recognised. With the space-separated `--new-from-rev <rev>` form, the
      check verifies the config rev, while golangci-lint still uses `<rev>`.
      The failure message and the Makefile comment both give the `=` form.
  - Stale `origin/master`: accepted, with the message pointing at
    `git fetch origin`. Any `git fetch origin` updates it. A stale
    `origin/master` only widens the diff, so it can add phantom issues but
    never hides a new one. Detecting staleness would need network access,
    which `make lint` must not do.
  - Why no automated test: the repo has no harness for Makefile targets, and a
    Go test would have to shell out to `make` against crafted git states,
    which is heavier than this three-line check. The proof is the real
    `make lint` in every git state below, run against this clone.
  - Green commands, after the fix (`golangci-lint cache clean` before each
    lint run):
    - Stale local `master` (`3caeba4d`), `make lint`: `0 issues.`, exit 0.
      This is also the gate run, with the `OS_*` variables unset.
    - Local `master` missing (renamed, then restored), `make lint`:
      `0 issues.`, exit 0.
    - `make lint GOLANGCI_LINT_ARGS=--new-from-rev=HEAD~1`: `0 issues.`,
      exit 0. The override still works.
    - `make lint GOLANGCI_LINT_ARGS=--new-from-rev=origin/no-such-branch`:
      exits before linting.

      ```
      make lint: lint baseline 'origin/no-such-branch' is not a commit in this clone.
      Run 'git fetch origin', or choose a baseline with GOLANGCI_LINT_ARGS=--new-from-rev=<rev>.
      make: *** [Makefile:104: lint] Error 1
      ```

    - `origin/master` absent: a scratch
      `git clone --single-branch --branch fix-lint-baseline-ref` of this clone,
      holding the fixed `Makefile` and `.golangci.yml`, has no `origin/master`.
      `make lint` there prints the same message for `'origin/master'` and
      exits 2. The real `origin/master` ref here was never touched.
  - `DEVELOPERS.md` mentions `make lint` only to say that it ignores
    `developers/`, and it does not describe the baseline, so it is unchanged.
  - Local `master` is back at `3caeba4d`, as the repro requires.
