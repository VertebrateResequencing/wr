## [0.37.0] - 2026-06-24
### Added
- New `wr suspend` and `wr resume` commands, and a "suspended" job state, to
  stop and restart scheduling of non-running commands (replacing the
  limit-group-to-0 workaround). Suspended commands are shown and filterable
  (`wr status --suspended`) in both the CLI and the web UI, and the web UI has a
  "Resume" action for suspended commands.
- You can now modify non-running incomplete commands from the status web page: a
  new "Modify" button lets you edit any displayed field (requirements,
  environment, priority, behaviours, etc.), with invalid edits reported in a
  popup. The same modification is also available via a new REST endpoint,
  `PATCH /rest/v1/jobs/...`.
- The status web page and `wr status -o details` now show live peak RAM, disk
  and CPU plus the latest STDOUT/STDERR for running commands. The web page also
  shows an ssh command to reach the host a command is running on.
- The status web page now has a "Rerun" button on completed commands.
- New `wr status -o table` (`-o t`) output mode, with columns configurable via
  the `WR_STATUS_FORMAT` environment variable.
- New `wr status --recent <duration>` mode to show successful jobs that finished
  within the last duration, across all report groups. It accepts Go duration
  units plus `d` (days) and `w` (weeks), and still honours `--limit`, `--env`
  and `--host`.
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
- The manager can expose an opt-in Go profiling endpoint by setting
  `WR_PPROF_ADDR`, useful when diagnosing production performance issues.
- The manager database batch window and size are now configurable with
  `managerdbbatchdelay` / `WR_MANAGERDBBATCHDELAY` and
  `managerdbbatchsize` / `WR_MANAGERDBBATCHSIZE`, for sites that need to tune
  throughput on high-latency storage.

### Changed
- `wr status -o c` and `-o summary` are now much faster on managers with very
  many completed commands, as the counts are computed without retrieving every
  matching job.
- `wr mod` is now much faster when modifying large numbers of commands.
- High-volume short jobs are faster again, especially on busy machines, by
  avoiding an unnecessary per-job process-tree memory scan after each command
  exits.
- `make test` and `make race` now use a Go test-suite runner that keeps passing
  output concise, shows clearer failure context, reports package-level
  pass/skip summaries, and shows live progress in an interactive terminal.

### Fixed
- A command added with `--rerun` that has incomplete dependencies now waits for
  them, instead of running immediately or being skipped as a duplicate.
- A command that failed for some other reason but happened to exceed its memory
  estimate is no longer misreported as having been killed for using too much
  RAM; the real failure reason is now given (with the high memory use noted), and
  the expected memory is still raised for later retries when automatic resource
  learning is allowed.
- Bulk adds of many dependent commands no longer miscount persisted live commands
  as duplicates; the missing commands are requeued and counted as newly added,
  and status details show which dependency group they are waiting for.
- Quick, sub-second commands no longer report identical start and end times.
- The status web page now reconnects and resynchronises automatically if it
  loses contact with the manager, instead of needing a manual refresh.
- The status web page now keeps live counts authoritative during high update
  rates: rows no longer flicker to zero, overcount, disappear incorrectly, or
  bring removed commands back after a refresh.
- Fixed a reserved-quota leak in cloud/OpenStack mode when the configured OS
  image was unavailable, which could exhaust quota and eventually lock up the
  manager.
- `wr cloud deploy` works again with common OpenStack RC files that set
  user/project domain variables rather than the generic domain variables.
- `wr cloud deploy --debug` no longer passes the removed `--runner_debug` flag to
  remote managers, cleans up SSH forwarding processes after failed deploys,
  pauses for debugging on manager-start/connect failures, retries transient
  manager readiness failures, and prints the locally forwarded web URL.
- OpenStack cloud mode now resolves runner executables on the remote host before
  trying to upload local paths, handles leading environment assignments in runner
  commands, and is more robust to transient server/port visibility and cleanup
  races.
