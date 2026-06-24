## [0.37.0] - 2026-06-24
### Added
- New `wr suspend` and `wr resume` commands, and a "suspended" job state, to
  stop and restart scheduling of non-running commands (replacing the
  limit-group-to-0 workaround). Suspended commands are shown and filterable
  (`wr status --suspended`) in both the CLI and the web UI.
- You can now modify non-running incomplete commands from the status web page: a
  new "Modify" button lets you edit any displayed field (requirements,
  environment, priority, behaviours, etc.), with invalid edits reported in a
  popup. The same modification is also available via a new REST endpoint,
  `PATCH /rest/v1/jobs/...`.
- The status web page now shows, live, the peak RAM and CPU and the latest
  STDOUT/STDERR of running commands, along with an ssh command to reach the host
  a command is running on.
- The status web page now has a "Rerun" button on completed commands.
- New `wr status -o table` (`-o t`) output mode, with columns configurable via
  the `WR_STATUS_FORMAT` environment variable.
- Log rotation for the manager and runner logs, configurable with the
  `WR_LOGSMAXSIZEMB`, `WR_LOGSMAXBACKUPS`, `WR_LOGSMAXAGEDAYS` and
  `WR_LOGSCOMPRESS` options.
- `wr status` now reports scheduler issues and bad servers (the same problems the
  web UI surfaces) as a footer.
- New `wr add --head N` option to add only the first N commands from your input,
  for quickly testing a command file.
- New `--remote_same_as_local` option for `wr add` (also settable via the
  `managerremotesameaslocal` config option / `WR_MANAGERREMOTESAMEASLOCAL`) to
  make adds to a remote manager default to your current working directory and
  environment, the same as adds to a local manager.

### Changed
- `wr status -o c` and `-o summary` are now much faster on managers with very
  many completed commands, as the counts are computed without retrieving every
  matching job.
- `wr mod` is now much faster when modifying large numbers of commands.

### Fixed
- A command added with `--rerun` that has incomplete dependencies now waits for
  them, instead of running immediately or being skipped as a duplicate.
- A command that failed for some other reason but happened to exceed its memory
  estimate is no longer misreported as having been killed for using too much
  RAM; the real failure reason is now given (with the high memory use noted), and
  the expected memory is still raised for the retry.
- Quick, sub-second commands no longer report identical start and end times.
- The status web page now reconnects and resynchronises automatically if it
  loses contact with the manager, instead of needing a manual refresh.
- Fixed a reserved-quota leak in cloud/OpenStack mode when the configured OS
  image was unavailable, which could exhaust quota and eventually lock up the
  manager.
