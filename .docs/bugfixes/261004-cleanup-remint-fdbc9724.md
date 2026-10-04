# Cleanup refuses a hashed level another run re-made

- Branch: `cleanup-remint-fdbc9724`, worktree `../wr-cleanupremint`
- Base: `origin/develop` at `55cc2565`
- Queue owner: this branch owns this item; it is queue item 1 in
  `checklist-tidy-8e94211c`'s `.docs/bugfixes/261004-checklist-tidy-8e94211c.md`

Quality gates, with `nice -n 19`, `GOFLAGS=-p=2` and `GOCACHE` outside the
home directory: `golangci-lint run ./jobqueue/`, `cleanorder -min-diff` on
edited Go files, and the jobqueue workspace, behaviour and cleanup tests below,
plain and `-race`. The caller runs `make test` and `make race`.

- [x] When a Job's cleanup runs (`jobWorkSpace.cleanup`), `provenDirs.openChain`
  re-opens each intermediate hashed level and requires it to be the inode the
  proof lstat'ed. With two runs of one key, one run's cleanup can remove a
  shared empty level and the other run's `mkHashedDir` re-create it with a new
  inode (always on NFS; on ext4 whenever any other mkdir happens in between).
  The first cleanup then returns `errNotBelowBaseDir` ("... is no longer the dir
  that was checked"), the cleanup Behaviour reports a failure, and the empty
  hashed levels and `<AppName>_cwd` base are left. No data loss.
  - Source: `.docs/bugfixes/260904-4.md`, "A production finding for a separate
    PR, NOT fixed here". Owner-approved fix: end the descent silently.
  - Red: `go test -tags netgo -count 1 ./jobqueue/ -run
    'TestCleanupLevelRemintedByAnotherRun'` exited 1, every case failing only
    its error assertion, e.g. `workspace_test.go` `Expected: nil`, `Actual:
    'dir is not below the base dir: /tmp/.../001/jobqueue_cwd/b/b/8 is no longer
    the dir that was checked'`, and the same for the `jobqueue_cwd` base.
  - Cause: confirmed. `openChain` ended the descent only for `os.IsNotExist`; a
    level re-made with a new inode fell through to the `proveSameDir` refusal,
    and `cleanup` returned it before `empty` and `removeUpward`.
  - Fix (`jobqueue/utils.go`): `proveSameDir` also wraps a new `errDirChanged`
    sentinel (message unchanged). `openChain` ends the descent when
    `levelRemade` says the open failed with `errDirChanged` and an lstat of the
    name through the parent handle finds a real directory. An incomplete chain
    opens no workspace and walks up nothing, so nothing at or above the
    re-made level is deleted. A symlink at the level is not a re-made level and
    stays a refusal; a non-directory, an escape, and the leaf and working
    directory checks are unchanged.
  - Tests: new `TestCleanupLevelRemintedByAnotherRun` (`jobqueue/workspace_test.go`),
    for both the hashed level above the workspace and the `<AppName>_cwd` base,
    using `cleanupProvenHook`: the level is renamed aside so its inode is held,
    then another run's `mkHashedDir` re-makes it, asserted by `os.SameFile`.
    Cases: this run's workspace already gone (cleanup returns nil, the other
    run's output, working dir, tmp, the re-made level by identity, base and
    user files all survive); this run's workspace moved aside with the level
    (its file untouched, nothing made at its old path); an empty directory of
    the user's renamed onto the level (survives by identity).
  - Changed prior expectation: `.docs/bugfixes/260904-4.md` Finding 4 made the
    two hashed-level rows of `TestProbeWorkSpaceChangedInTheWindow` assert
    `errNotBelowBaseDir`. That expectation is what this fix changes, by owner
    decision, so both rows now expect nil; their survivor lists (the other run's
    files and the re-made level, or the user's tree and the level) are kept and
    still asserted first, and the row comment is rewritten. All other
    `errNotBelowBaseDir` expectations from cleanup are unchanged and pass: the
    workspace and working-directory swaps are the leaf or below it, the
    already-gone-at-proof cases take `openChain`'s other early return, and the
    symlink swaps in `TestBehaviourCleanupSafety` stay refusals.
  - Mutants, each killed:
    - `levelRemade` always false (the error again): exit 1,
      `TestCleanupLevelRemintedByAnotherRun` 6 cases and both rewritten
      `TestProbeWorkSpaceChangedInTheWindow` rows red.
    - descend on through a re-made level without verifying it: exit 1, the
      empty user directory case red (`lstat .../jobqueue_cwd/b/b/8: no such
      file or directory`).
    - accept a symlink as a re-made level: exit 1,
      `TestBehaviourCleanupSafety` red (`Expected '<nil>' to NOT be nil`).
  - Gates: `-run 'Workspace|WorkSpace|Behaviour|Cleanup|Hashed|provenDirs|Probe|RmEmpty|Remove|Lost|OpenVerified|Unmount|RmMuxfys|RelIs|RunDir|JobKey|Kill|Reserved|Released|Minted|Pin|ASlow|CreatedCwd'`
    passed plain (exit 0) and with `-race` (exit 0); the two tests passed 30 of
    30 at `-count 30`; `golangci-lint` 0 issues; `cleanorder -min-diff` clean.
  - Files: `jobqueue/utils.go`, `jobqueue/workspace_test.go`,
    `jobqueue/deletion_probes_test.go`, `CHANGELOG.md`.

## Review (261004)

PASS on safety, error wrapping, tests and mutants (4 more killed in review:
levelRemade accepting a symlink, skipping its Lstat, accepting any error, and
proveSameDir not wrapping errNotBelowBaseDir). Findings addressed:

- The CHANGELOG entry said the empty directories were no longer left. They
  never were this run's to remove: before and after the fix nothing at or
  above the changed level is deleted; the run that re-made them removes them
  when it finishes. The user-visible fix is that the spurious cleanup failure
  ("Behaviour problems" in the job's stderr) is gone. Entry reworded. The
  Source note above ("are left") describes the pre-fix report and stays.
- Also affected, and still safe: `rmEmptyMountDirs` (via `rmEmptyDirsIn`)
  now stops silently, rather than refusing, when a level above a mount point
  is a different real directory; it deletes nothing in that case either.

