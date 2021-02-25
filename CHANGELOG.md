# Change Log
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](http://keepachangelog.com/) and this
project adheres to [Semantic Versioning](http://semver.org/).


## [0.23.3] - 2021-02-26
### Added
- `wr status` now has a `--hosts` option to filter for jobs that ran on the
  given host.
- `wr status` now has a --running` option to only show jobs that are currently
  running.
- `wr remove` now has a `--buried` flag, to only remove buried jobs.

### Changed
- Container interaction code refactored. No change in behaviour, but will allow
  for possible future features such as native container support and singularity
  support.


## [0.23.2] - 2020-10-29
### Changed
- OpenStack worker instances that are spawned now attach all the networks
  that were attached to the manager's instance, not just the one that matches
  the configured CIDR (but that is still the only one with wr's security groups
  applied).

### Fixed
- Major performance reversion when using LSF fixed.
- Increased TCP timeout for ssh to newly spawned OpenStack instances, to allow
  slower systems to work.


## [0.23.1] - 2020-10-05
### Fixed
- If a cloud deploy fails, you are asked to hit return after investigating,
  before it tears down. An extraneous warning message is no longer displayed
  when you do this.
- When checking for reminaing disk space, if the filesystem returns 0, it is
  now re-checked a few times before killing the job.


## [0.23.0] - 2020-07-14
### Changed
- When using the OpenStack scheduler, `wr manager start --max_cores 0` (or
  `wr cloud deploy --max_local_cores 0`) now allows 0-cpu jobs to run on the
  manager's instance. To fully disable all jobs from running on the manager's
  instance, use `--max_ram 0`.
- When using the OpenStack scheduler, if servers fail to spawn, the error
  backoff now results in a maximum wait time of 1min between spawn attempts,
  intead of 20mins. These waits are now also logged.
- Improved logging when an OpenStack server fails to spawn, to include the
  flavor attempted.

### Fixed
- It was possible in some rare cases where jobs wrote to a distributed
  filesystem in OpenStack, that writes of the last job before the server was
  scaled down would not persist to disk. Now wr will attempt to sync all
  filysystems and remount them read-only before destroying servers.


## [0.22.0] - 2020-06-02
### Added
- New `wr conf` command that shows current configuration.

### Changed
- Additional and improved logging or normal operation and errors.
- API change: jobqueue.Scheduler has new method Scheduled().

### Fixed
- Critical fix for database lockups resulting in state not being saved or backed
  up.
- Critical fix for jobs getting stuck pending.
- LSF queue picking heuristics fixed for systems with nested and duplicated
  host definitions.
- OpenStack resources file getting corrupt.
- No longer crashes when trying to terminate many OpenStack servers at once, and
  OpenStack is non-responsive.
- Fixed various edge-case concurrency bugs.
- Jobs were failing when they detect / has run out disk space; now they check
  the correct volume the working directory is mounted to instead.


## [0.21.0] - 2020-03-20
### Added
- The OpenStack scheduler can now spawn multiple servers at once, increasing the
  rate of scale up. New cloudspawns config option (defaults to 10).
- `wr add` now has an -s option which will make it output the internal id of the
  added job(s).
- `wr status` now has a "plain" output mode (-o p) that reports just the current
  state of each job, listed by internal id.

### Changed
- When OpenStack flavors are picked for you, they are now picked from your
  --flavor_sets in the order you specify them, meaning you can prefer certain
  hardware is used and filled up before other hardware.
- To avoid overloading machines in local or OpenStack mode, there is now a limit
  of how many zero core jobs will run at once: 2 x physical cores. This is in
  addition to whatever non-zero core jobs are using. (As before, zero core jobs
  are also limited by the other requirements such as RAM, so zero core jobs were
  never unlimited.)
- Improved logging for certain kinds of errors that result in jobs getting stuck
  pending
- Improved logging for OpenStack servers that fail to become ACTIVE.

### Fixed
- A number of cases of the OpenStack scheduler failing to schedule jobs
  correctly.


## [0.20.0] - 2019-11-07
### Added
- New `--misc` option for `wr add` to pass options to LSF scheduler.
- New `--queue` option for `wr add` to specify a queue to submit to when using
  the LSF scheduler (it used to be picked for you, and still will if not
  specified).

### Changed
- Always try scheduling again in the case that something went wrong with
  scheduling.
- Runners wait not just for new jobs to be added to the queue before exiting,
  but also for limited jobs to become available.
- Runners under the local and cloud schedulers now exit after running for at
  least 15mins, after a job ends, allowing better scheduling.
- The resource-requirments based bin packing used by the local and cloud
  scheduler now considers user-specified job priority, which combined with the
  prior change should mean high priority jobs run as soon as possible, even if
  they are small and all hardware is in use. Small higher priority jobs will run
  before the next larger lower priority job. However large high priority jobs
  still have to wait for all smaller jobs to complete and free up enough space.
- Requires go >v1.13 to build.

### Fixed
- `wr mod` no longer resumes a paused manager.
- `wr mod` no longer allows you to change the command line of 1 job to match
  that of another, which would violate job uniqueness.
- Critical fix for OpenStack scheduler to avoid edge-case where servers could
  endlessly be spawned but then deleted before use.
- If S3 mounting takes a long time, this no longer causes a problem with jobs
  getting stuck in an endless cycle of retrying without ever starting.
- Retrieval of complete jobs with `wr status` that completed very quickly and
  had hundreds running in parallel.
- No more limit exhaustion if jobs get reserved but don't start, or if they get
  buried without exiting.
- No more delay when handling jobs with different resource requirements but the
  same limit group.
- Fixed various data races.


## [0.19.0] - 2019-08-01
### Added
- New --cloud_auto_confirm_dead option to `wr manager start` (and similar
  option for `wr cloud deploy`, and new config option) that allows dead
  OpenStack servers to be automatically confirmed dead (destroying them, and
  confirming the jobs running on them as dead as well) after, by default,
  30mins. NB: for the old behaviour where dead servers were left alone until
  you manually investigated, set this option to 0.

### Changed
- OpenStack scheduler reimplemented for new fixes and behaviour:
  1. Avoids a lock-up issue where jobs would stop getting scheduled under
     certain circumstances.
  2. Quota warnings no longer appear until quota is physically used up.
  3. The flavor of server to spawn and the jobs to run on them are reassessed
     after every spawn, job completion and schedule, allowing bin-packing to do
     the expected thing as new jobs are scheduled over time.
  4. Spawned servers that are no longer needed are abandoned right away,
     allowing any other still-desired server to come up straight away.
  5. Servers for different kinds of job (according to having different resource
     requirements) are now spawned fully simultaneously, while servers for the
     same kind of job are now spawned fully sequentially. (The old behaviour was
     servers for all kinds of jobs were spawned sequentially, but with some
     overlap.)
- Cloud scripts (being the scripts that run after an OpenStack server boots up)
  now have a time limit of 15 mins, so that scripts that fail to exit do not
  cause servers to be created that are never used and never destroyed.
- For developers of wr, the linting method has changed to golangci-lint. See
  comment in Makefile for installation instructions.

### Fixed
- Theoretical edge-case bugs fixed, alongside potential general stability
  improvements.
- Memory leak associated with running jobs that write empty files to uncached S3
  mounts.
- Runners now wait the appropriate time for new jobs to run, and start running
  them the moment they're added.
- Edge-case where an OpenStack server could scale down while running a command.


## [0.18.1] - 2019-05-23
### Added
- New `--manager_flavor` option to `wr cloud deploy` (defaulting to new
  cloudflavormanager config option), to be able to set a different flavor for
  the deployed manager compared to workers created by the manager.

### Changed
- At least go v1.12 is needed to compile wr.
- cloudos config option (and --os option to `wr cloud deploy` or --cloud_os
  option to `wr manager start`) has always picked a random image that had a
  matching prefix; now an image with an exactly matching name will be picked in
  preference.
- Default cloudos changed from "Ubuntu Xenial" to "bionic-server".

### Fixed
- Fixed a situation where OpenStack servers failed to scale down and became
  unused when adding lots of quickly completing jobs.


## [0.18.0] - 2019-04-08
### Added
- New limit group property on Jobs, to allow you to limit the number of jobs
  that run at once in a group.
- New `wr limit` command to set and change limits on limit groups.
- New `wr mod` command to modify most aspects of incomplete jobs added to the
  queue.
- REST API now supports DELETE of jobs to cancel them.

### Changed
- REST API GET on jobs can now also filter on "deletable" state.
- Backwards-incompatible internal API changes.

### Fixed
- Can now build on latest versions of go, and requires at least 1.11.5. You may
  have to `go clean -modcache` if you encounter problems.
- managerumask config opion now applies to jobs run on remote OpenStack servers
  when using the OpenStack scheduler.
- Fixed a case where it was possible for a job with dependencies to start
  running immediately if its dependencies were re-added to the queue, before
  they finished running.
- The managerdir (~/.wr_production by default) is no longer created until you
  try to start the manager; other commands like `wr version` do not create it.


## [0.17.0] - 2019-01-14
### Added
- New `wr manager pause` and `wr manager resume` commands, allowing you to
  temporarily pause the running of further commands, while letting existing
  commands continue to run and queuing newly added commands.
- New cloudflavorsets config option and related manager and cloud arguments.
  This allows you to define which cloud flavors are backed by different
  hardware, which results in the cloud scheduler retrying a flavor in a
  different set when the hardware backing one set is full.
- When you start the manager, it now logs its version to the log file.
- New --runner_debug option to `wr manager start` that makes runners log
  warnings and errors to syslog on the machine they find themselves on.
  `wr cloud deploy --debug` turns this on.

### Changed
- Cloud resources have been renamed from having a wr-[production|development]
  prefix, to having a wr-[prod|dev] prefix, increasing `wr cloud deploy
  --resource_name` length limit from 11 to 18. `wr manager start
  --local_username` now also shares this length restriction, to avoid
  hostname truncation issues as intended. Be sure to teardown existing clouds
  _before_ updating to this version.
- `wr cloud servers --confirmdead`, in addition to deleting dead servers, now
  also confirms dead any jobs that were running on those servers.
- `wr add --monitor_docker` now takes wildcards, to specify variable filenames.

### Fixed
- Adding jobs with an override of 2 now works with the disk resource when you
  specify 0 GB, using both the CLI and the REST API. (Not specifying it
  continues to use learned values, since override only applies to specified
  resources.)
- If a cloud server has a hostname that is a truncation of its name in the
  cloud, `wr status` is still able to look up and report its ID.
- If a manager crashes or is killed, and then is restarted, it now ignores old
  client messages still cached in the socket.
- If authentication with OpenStack fails, re-authentication no longer gets
  disabled.
- Fix for 1 case of jobs pending for a long time when using the OpenStack
  scheduler in certain circumstances.


## [0.16.0] - 2018-11-15
### Added
- New `wr k8s` commands to self-deploy to kubernetes clusters. See:
  https://github.com/VertebrateResequencing/wr/wiki/Kubernetes
- Complete recovery of state is now possible following the loss of the manager,
  even when jobs are still running. See the wiki for details:
  https://github.com/VertebrateResequencing/wr/wiki/Recovery
- New /info/ and /rest/version endpoints for the REST API, to get manager status
  and REST API version respectively.
- New `--max_local_cores` option to `wr cloud deploy`, and `--max_cores` option
  to `wr manager start` (and likewise for ram), lets you control how much of the
  machine that the manager is running on is also used for running jobs.
- The --cloud_cidr option to `wr manager start` now not only determines which
  network to get an IP address from, but also which network to spawn new servers
  in.
- New --cloud_disable_security_groups option to `wr manager start` allows new
  cloud servers to be spawned in networks that do not allow security groups.
- For the dismissible scheduler warnings that can appear on the web interface,
  there is now a "Dismiss all" button.
- New `--confirmdead` option to `wr kill` to provide a way of confirming jobs
  in "lost contact" state are dead, using the CLI.
- New `wr cloud servers` command to view and confirm dead cloud servers that
  have become unresponsive, using the CLI.

### Changed
- Switched to using go modules for dependency management, so requiring go
  v1.11+. See the README.md for new build instructions. If not working on any
  other go projects, recommend deleting all existing go-related files, unsetting
  go environment variables, and starting from scratch.
- Manager's token file is now only automatically deleted on graceful stop. If
  the token file is present on manager start, it will be reused, allowing
  existing runners to reconnect to the new manager. See the wiki for notes on
  this: https://github.com/VertebrateResequencing/wr/wiki/Security
- If a keypair was created by a manager instance in the cloud, then it will be
  deleted on manager stop. (Previously keys would remain on the assumption they
  might be necessary to access the manager's cloud instance.)
- Various breaking API changes in most sub-packages.

### Fixed
- Buried jobs stay buried after a manager restart.
- Deleting jobs properly clears out the scheduler in all cases, so that
  schedulers don't endlessly try and schedule runners when there are no jobs to
  run.
- Fixed a case of the local scheduler overcommiting on resources, running too
  many jobs at once.
- Adding very long commands used to silently fail. Now commands longer than
  typical shells can cope with can be added, and incredibly long commands result
  in a failure to add.
- Starting many jobs at once on cloud servers should no longer result in that
  server being considered "dead".
- OpenStack scheduler no longer fails to start up if there are existing servers
  in OpenStack with flavors that no longer exist.


## [0.15.0] - 2018-09-03
### Added
- New rerun option when adding jobs using the REST API.

### Changed
- You can now add jobs with fractional and 0 CPU requirements. NB: this makes
  the default CPU requirement when using the REST API 0!
- When adding jobs using the REST API, rep_grp now defaults to 'manually_added'.
- The manager now fails to start if your db backup location is in S3 and your
  credentials are incorrect.

### Fixed
- Critical fix for potential read errors when using the built-in S3 mounting on
  overloaded Ceph.
- `wr status` crash when given non-existant internal IDs.
- You can no longer add jobs with less than 0 CPU requirements.
- PATH environment variable is no longer ignored when adding new jobs.


## [0.14.0] - 2018-08-07
### Added
- Added commands will now be killed if the disk fills up.
- Certain kinds of jobs (non --cwd_matters, no or uniquely-cached mounts) now
  have their disk usage tracked and reported.
- The local scheduler can now be limited in its CPU and memory usage.

### Changed
- `wr add --report_grp` renamed `--rep_grp` for consistency with the JSON arg.
- `wr add -o 1|2` now only override the supplied resource types, using learned
  values for other types.
- `wr status -o summary` now adds a line summarising all displayed rep groups
  along with an overall start and end time. It also now shows disk usage.
- `wr manager` help text related to start moved to the `start` sub-command.

### Fixed
- OpenStack servers are no longer failed as bad if more than MaxSessions SSH
  sessions are opened on them.
- `wr cloud deploy --resource_name` limited to 11 characters, since hostnames
  have a maximum length.
- Fix a case of a manager panic leading to unexpected death.


## [0.13.1] - 2018-05-30
### Added
- `wr cloud deploy` has new --resource_name option, allowing for a single user
  to have multiple named deployments. See the
  [wiki](https://github.com/VertebrateResequencing/wr/wiki/Multiple-Cloud-Deployments)
  for details.


## [0.13.0] - 2018-05-21
### Added
- Minimual LSF client (bsub, bjobs, bkill) emulation, for using wr as the
  backend scheduler and runner for legacy or alternate workflow systems, such as
  Nextflow or Martian.
- `wr add` has new --monitor_docker option to get accurate memory and cpu usage
  stats for jobs that run docker, and to kill those dockers when you kill the
  job.
- `wr add` has new --cloud_shared option, for turning on a simple NFS shared
  disk when using the OpenStack scheduler with Ubuntu.
- `wr status`, `retry`, `kill` and `remove` take new -z and -y modifiers to
  treat -i as a repgroup substr (show status of jobs in multiple repgroups) or
  as an internal job identifier (which are now displayed by `status`).
- `wr status` has new -o option to define the output format, including new json
  and summary formats (shows mean resource usage across a repgroup).

### Changed
- Jobs are now only killed if they both use more than expected memory and more
  than 90% of total physical memory.
- Local scheduler (and by extension some behaviour of the OpenStack scheduler)
  now does bin packing, trying to run as many jobs as possible in parallel by
  filling in "gaps" in resource usage. Commands that use more resources will be
  scheduled to run before other commands. Job priority only decides the order
  that jobs of equal resource usage run in.
- Trying to start the manager in OpenStack mode outside of OpenStack now
  immediately returns an error.
- `wr manager start` now shows error/crit lines from the log, on failure to
  start.
- Backwards incompatible changes to cloud API.

### Fixed
- `wr manager start` no longer logs its authentication token.
- Race condition where an OpenStack server could be destroyed yet be considered
  usable.
- `wr` client commands now obey managerhost config option when not running on
  the same host as the manager.
- OpenStack scheduler no longer ignores requested job memory when non-default
  OS disk set.
- Reported peak memory usage of jobs fixed to consider usage of all child
  processes of the initial command (even if they change their process group and
  fork).
- Reported CPU time of jobs fixed to include user time, not just system time.


## [0.12.0] - 2018-04-27
### Added
- All communications to the manager are now via TLS, and authentication is
  required using a token. See notes on the
  [wiki](https://github.com/VertebrateResequencing/wr/wiki/Security).
- You can now add jobs with a chosen cloud flavor, instead of having wr pick a
  a flavor for you.
- New wr sub-commands `retry`, `kill` and `remove`, so that you can do
  everything you can do with the web interface on the command line. `wr remove`
  can also remove whole dependency trees in one go.
- `wr cloud teardown` now has a --debug option to see teardown details.
- `wr cloud deploy` can now run an arbitrary script --on_success, and
  --set_domain_ip, when using your own TLS certificate and infoblox.
- `wr add` can now take a --cloud_config_files option, for per-job config files.
- New upload endpoint for REST API, allowing you to add jobs with job-specific
  cloud_scripts or cloud_config_files.

### Changed
- The -f option of `wr status` and the new `retry`, `kill` and `remove` commands
  now takes the same format file as does `wr add`, so you easily get the status
  of, kill, retry or remove jobs you just added.
- `wr cloud teardown` now attempts subnet removal from a router multiple times
  on failure, before giving up.
- Web interface and REST API now tell you about the "other" resource reqs of
  jobs.
- Web interface and REST API can once again show environment variables of jobs.
- Backwards incompatible changes to jobqueue and cloud APIs.

### Fixed
- `wr add --cwd` now has an effect.
- Scheduler no longer tries to run jobs that were deleted using the web
  interface, or that can never start.
- Openstack scheduler now correctly estimates how many jobs can be run, when you
  are running jobs with particular script, config file and flavor requirements.
- Deleted jobs no longer reappear when restoring from a backed up database.
- Backups include the latest transactions.
- S3 mounts can cope with large (>1000 files) directories.
- S3 mounts work correctly when multiplexing in non-existent directories.
- Runners no longer hang when their job is killed.


## [0.11.0] - 2018-03-19
### Added
- OpenStack scheduler now supports recent versions of OpenStack ("Pike").
- Significant performance increases when working on 1000s of ~instant complete
  jobs. See new "Performance considerations" section of README.md.
- Both cloud and LSF jobs can successfully call `wr add` to add more jobs to the
  queue in dynamic workflows.
- `wr status` shows the status of jobs in dependent state.
- `wr manager start` now takes a --timeout option (also passed through from `wr
  cloud deploy`) so that you can start when eg. your OpenStack system is being
  very slow to authenticate.

### Changed
- `wr cloud deploy` and `teardown` now display errors that occur with the
  manager. Manager problems during deploy now wait for you to debug before
  tearing everything down.
- wr commands now output INFO etc. lines in a new format to STDERR instead of
  STDOUT.
- Improved logging of all kinds of errors and potential issues.
- `wr manager` --cloud_debug renamed --debug, which now also shows non-cloud
  debug messages.
- Cloud post creation scripts can now rely on config files having been copied
  over before they are run.
- `wr cloud deploy --os` prefix can be of the image's id as well as name.
- There is now a 30s gap between sequential automated database backups.
- Improved efficiency of interactions between runner and manager: fewer smaller
  calls to increase performance and scaling.
- JobQueue Server now has a single hard-coded queue for jobs, for performance
  increase.
- Default timeouts for interactions with the manager have been increased.
- Cloud deployments now delete the transferred environment file after the
  manager starts, for improved security.
- Backwards incompatible changes to many API methods to support the new logging.

### Fixed
- Wrong quota remaining after failed OpenStack server spawns.
- Makefile now works with GOPATH unset, and correctly builds a static executable
  if CGO_ENABLED is unset.
- File read errors when using uncached S3 mounts.
- `wr add --cwd_matters` no longer ignored when running remotely with a
  defaulted --cwd.
- Data races in various places.
- `wr manager` can now be started with the OpenStack scheduler on an OpenStack
  server with any character in its name.
- Performance reversion when dealing with many ~instant complete jobs.
- Schedulers no longer request unnecessary jobs be run when jobs are completing
  too quickly. OpenStack scheduler does a better job of keeping track of how
  many servers it can and should create.
- `wr cloud deploy -h` help text now correctly specifies what OpenStack
  environment variables are required.
- `wr cloud deploy` now reconnects to an existing remote manager properly.
- New servers that a created by the cloud scheduler now actually wait for the
  new servers to be fully ready before trying to run commands on them.
- Outstanding database operations are allowed to complete, and everything else
  cleans up more completely before manager shutdown.
- OpenStack scheduler and `wr cloud deploy` no longer get stuck indefinitely
  waiting for SSH to a server to work, timing out instead.


## [0.10.0] - 2017-10-27
### Added
- New REST API. See https://github.com/VertebrateResequencing/wr/wiki/REST-API
- Automatic database backups now occur. Manual ones can be taken. The backup
  location can be in S3. This allows for state to be retained across cloud
  deployments.
- OpenStack scheduler now continuously monitors the servers it creates, and
  sends a warning to the user via the web interface or REST API to let them
  know when a server might have died. The user can confirm death to have wr
  destroy the server and spawn a new one (if still needed).
- OpenStack scheduler now reports other issues it encounters to the web
  interface or REST API, so users know why they have jobs stuck pending (eg.
  they've run out of quota).
- If `wr cloud deploy` was used, and the remote `wr manager` is still running
  but your ssh connectivity to it got broken, another `wr cloud deploy` will
  reconnect.
- Running jobs can now be killed (via the web interface).

### Changed
- When the manager loses contact with a job and thinks it must be dead, the job
  is no longer automatically killed. Instead it enters "lost" state and the user
  can confirm it is dead (in order to subsequently retry it). Otherwise it will
  come back to life automatically once the (eg. connectivity) issue is resolved.
- Cloud deployments create security groups with "ALL ICMP" enabled.
- OpenStack scheduler regularly checks for changes to available flavors, instead
  of only getting a list at start-up.
- `wr mount --mounts` and similar for `add` can now take a profile name.
- Status of jobs now includes the IP address of the server the job ran on, and
  for cloud deployments, the server ID.
- OpenStack scheduler now has a minimum spawn timeout, instead of being based
  purely on the running average spawn time.
- On the status web page, if you choose to kill, retry or remove just 1 of many
  jobs in a report group, instead of acting on a random job in the report group,
  it now acts on the specific job you were looking at.

### Fixed
- LSF scheduler now works correctly when a job needs more than 1 core.
- LSF scheduler now works with LSF queues that have been configured in TB.
- `wr manager start -m` value was off by 1.
- OpenStack scheduler could sometimes think the wrong number of things were
  running.
- `wr cloud teardown` no longer hangs when the resources have already been
  destroyed (by a third party).
- OpenStack scheduler was miscalculating remaining instance quota when
  `wr cloud deploy --max_servers` was used.
- Cloud deployments and subsequent creation of servers to run jobs are now more
  likely to succeed.


## [0.9.1] - 2017-08-15
### Fixed
- Data races, for general reliability and stability improvements.
- `wr add` now defaults the cloud options to blank so they don't override
  non-default `wr cloud deploy` settings.
- Various read and write issues with the built-in S3 mounting.

### Changed
- `wr cloud deploy` now creates servers that are also in the default security
  group, increasing the chances that it work on all OpenStack systems and with
  servers that have network storage.


## [0.9.0] - 2017-07-19
### Added
- Jobs can now have mounts that are writeable and uncached.
- Web interface shows a "live" walltime for running jobs.

### Fixed
- Cloud post creation scripts that alter PATH now work as expected.
- Run behaviours with pipes now work as expected.
- `wr cloud deploy` with a default -m of 0 now gets passed through to the remote
  manager correctly.
- In cloud deployments, /etc/fuse.conf is made world readable so mounting can
  work.
- Jobs with lots of data to upload to a mount no longer time out and fail.
- Jobs with long-running behaviours no longer time out and fail.
- Immediately buried jobs are now recognised as not needing resources.
- Retried buried jobs no longer pend forever.
- Jobs with multiple mounts now correctly un-mount them all if killed.
- Long lines of STDOUT/ERR no longer hang job execution.
- Web interface now correctly displays job behaviour JSON.
- Jobs that fail due to loss of connectivity between runner and manager (eg.
  network issues, or a dead or manually terminated OpenStack instance) now obey
  the --retries option and don't retry indefinitely.
- Job unique cwd creation no longer fails if many other quick jobs are cleaning
  up after themselves in the same disk location.
- Web interface now displays correct start and end times of jobs in a list.
- OpenStack scheduler now copes with the user manually terminating wr-created
  instances, avoiding an edge case where a job could pend forever.
- For a cloud deployment, a job's --cloud_script option is now always obeyed.
- If glide is not installed and the user has a brand new Go installation, the
  makefile now ensures $GOPATH/bin exists.

### Changed
- Jobs with --mounts are now uniqued appropriately based on the mount options.
- When a cached writeable mount is used and the upload fails, jobs now get an
  exit code of -2.
- `wr cloud deploy --config_files` option now keeps the mtime of copied over
  files.
- `wr cloud deploy --config_files` option can now take a from:to form if the
  local config file is in a completely different place to where it should go on
  the cloud instance.
- OpenStack cloud scheduler now periodically checks for freed resources, in case
  you have have pending jobs because you have reached your quota and then
  terminate a non-wr instance.
- `wr add --deps` now just takes a comma separated list of dependency group
  names (ie. without the "\tgroups" suffix), and cmd-based dependencies are
  defined with new `--cmd_deps` option.
- As a stop-gap for security, the web interface no longer displays the
  environment variables used by a job.
- As a stop-gap for security, the command line clients only allow the owner of a
  manager to access it.
- OpenStack environment variables no longer get printed to screen on failure
  to start a manager in the cloud.
- `wr [add|mount] --mounts` now takes a simpler format; the old JSON format can
  be supplied to new `--mount_json` option.
- `wr add` now takes command-line options for the cloud_* and env options that
  could previously be provided in JSON only. cloud_user renamed to
  cloud_username and cloud_os_ram renamed to cloud_ram, for consistency with
  other wr commands.
- `wr add` now has a `--rerun` option to rerun added jobs if they had previously
  been added and completed. This is how it used to behave before this change.
  Now the default behaviour is to ignore added jobs that have already
  completed, making it easier to work with ongoing projects where you come up
  with commands for all your data whenever new data might have arrived, without
  having to worry about which old data you already ran the commands for.
- Backwards incompatible change to jobqueue.Client API: Add() now takes an
  ignoreComplete argument.


## [0.8.1] - 2017-06-07
### Fixed
- `make dist` fixed to work and compile to darwin once again.

## [0.8.0] - 2017-06-07
### Added
- New --debug option for `wr cloud deploy`.
- New --cwd_matters option for `wr add`: the default for new jobs is that they
  run in a unique directory, and --cwd_matters restores the old behaviour of
  running directly in --cwd. Running in a unique directory enables automatic
  cleanup (see behaviour system below) and sets $TMPDIR to a unique directory
  that will always get deleted.
- New --ChangeHome option for `wr add`: sets the $HOME environment variable to
  the unique working directory.
- Jobs now store (and display in status) their start and end time if they ran.
- `wr add` gains a new behaviour system for specifying what happens after a cmd
  exits (--on_failure, --on_success and --on_exit options).
- S3-like object stores can now be mounted just-in-time for a cmd to run using
  the new --mounts option to `wr add`, and these mounts can be tested with the
  new `wr mount` command.
- `wr add` can now be called by a command added to `wr add` when using a cloud
  deployment.


### Fixed
- Status web page 'remove' button for buried jobs was broken.
- OpenStack scheduler no longer stops spawning after a number of hours.
- OpenStack scheduler no longer miscounts how many resources have been used,
  which stopped it spawning new servers when close to your quota.
- Adding jobs with alternating resource requirements no longer results in
  unnecessarily pending jobs.
- Orders of magnitude speed improvement for runners reserving new jobs to work
  on when there are many thousands of jobs.

### Changed
- `wr cloud deploy` now waits longer for remote manager to start up.
- `wr cloud deploy --script` option now runs as a user, but lets you prefix
  commands with sudo.
- Building wr now requires Go v1.8+.
- Dependencies are now managed by glide, and include a private bug fix for
  OpenStack.
- Backwards incompatible changes to queue package API (ReserveFiltered() removed
  and replaced with Reserve("group")).


## [0.7.0] - 2017-03-09
### Added
- Status web page now lets you delete pending commands from the queue.
- Package comments (godoc documentation) are now improved with better overviews
  and examples.
- `wr cloud deploy`, on failure to start the remote manager, and
  `wr cloud teardown` now copy the remote manager's log file locally to eg.
  ~/.wr_produciton/log.openstack

### Fixed
- `wr cloud teardown` will no longer stop the remote manager if you you can't
  authenticate with the cloud provider.
- `wr status --std --limit 0` now works, getting all STDOUT/ERR for all your
  commands.
- Changed a dependency to use a fixed older version.
- Fixed a situation in which the OpenStack scheduler could fail to spawn
  servers.
- Fixed a situation in which the OpenStack scheduler could fail to terminate
  idle servers.
- `wr cloud deploy --script xyz` now ensures the script completes before wr runs
  anything on the new server.

### Changed
- Backwards incompatible changes to the cloud package API.


## [0.6.0] - 2017-02-22
### Added
- MacOS compatibility.
- Status web page now shows requested disk.
- All cloud options to `wr cloud deploy` and `wr manager start` are now config
  options, so they can be set once in your ~/.wr_config.yml file for simpler
  deployments.
- `wr status` can now take a --limit option that lets you see details of all
  commands, instead of a random one from each group.
- Cloud usage is now user-specific, allowing multiple users to have their own
  wr deployment in the same OpenStack tenant.
- There's now a wiki (see README.md for the link) that covers some gotchas.

### Fixed
- Various cases of commands getting stuck pending.
- Various cases of the OpenStack scheduler failing to spawn servers.
- OpenStack scheduler creating too many servers.
- OpenStack scheduler starting too many runners on servers.
- `wr cloud teardown` now more reliably cleans everything up if you have many
  servers that need to be terminated.
- `go get` was broken by a dependency.

### Changed
- `wr manager start -m` now allows you to specify 0 additional servers get
  spawned.
- OpenStack servers failing to spawn now results in a back-off on further
  requests.
- OpenStack servers are now created with a dynamic timeout that should avoid
  unnecessary cancellations when the system is busy.
- Status web page now shows stdout/err as pre-formatted text with progress bars
  filtered out.


## [0.5.0] - 2017-01-26
### Added
- `wr cloud deploy` now has options for setting your network CIDR, gateway IP,
  DNS name servers and minimum disk size.
- `wr add` command-specific options now allow specifying environment variable
  overrides.

### Fixed
- Manager's IP is now calculated correctly on hosts with multiple network
  interfaces.
- `wr cloud deploy` applies appropriate private permissions to the ssh key file
  it copies over to the "head" node.
- Numerous fixes made to OpenStack scheduler, allowing it to work as expected
  with multi-core flavors and not overload the system by spawning new servers
  sequentially.

### Changed
- Using `wr add` with a remote manager now no longer associates local
  environment variables with the command; commands run with the remote variables
  instead.
- Memory requirements are no longer increased by 100MB unless the resulting
  figure would be less than 1GB.
- Backwards incompatible internal API changes for the jobqueue package.


## [0.4.0] - 2017-01-19
### Added
- `wr add --disk` causes the creation of suitable sized temporary volumes when
  using the OpenStack scheduler, if necessary.
- New `wr add` command-specific options now allow specifying which cloud OS
  image to use to run that command.

### Fixed
- Improved error message if an invalid OS image name prefix is supplied.

### Changed
- Format of file taken by `wr add -f` has changed completely; read the help.
- Backwards incompatible internal API changes for the cloud package.


## [0.3.1] - 2016-12-16
### Fixed
- `wr cloud deploy` now creates working ssh tunnels for typical ssh configs.


## [0.3.0] - 2016-12-12
### Added
- Added commands can now be dependent on previously added commands, using
  arbitrary and multiple dependency groups for "live" dependencies.
- Status web page now shows dependency information.

### Fixed
- Updated help text to note what is not implemented yet.

### Changed
- Format of file taken by `wr add -f` now has an additional column for
  specifying dependency group-based dependencies, which changes the column
  order. Old files that had dependencies specified are no longer compatible!


## [0.2.0] - 2016-12-06
### Added
- Added commands can now be dependent on previously added commands.
- Status web page now has a favicon.
- `wr cloud deploy` (and `wr manager start`) now take options to limit what
  cloud flavors can be used, and to specify a post-creation script.
- Manager now has improved logging of a variety of errors.

### Fixed
- Status web page now correctly sorts the RepGroups.
- Status web page progress bars no longer get stuck in 'ready' state, even when
  complete.
- Status web page progress bars no longer flicker.
- Manager works with latest versions of OpenStack (which do not generate DER
  format SSH private keys).
- Manager running on OpenStack no longer stops spawning new servers when it
  should.

### Changed
- Format of file taken by `wr add -f` now had additional columns for specifying
  dependencies.
- Status web page no longer shows the unimplemented search box.
- Status web page now show deleted commands as deleted instead of complete.


## [0.1.1] - 2016-10-21
### Added
- `wr cloud deploy -h` now includes additional help text to explain OpenStack
  usage.
- `wr add` now has a `--retries` option, letting you choose how many automatic
  retries failed commands undergo before user action is required.
- Makefile now specifies a 'report' action to do most of goreportcard.

### Fixed
- `wr cloud deploy` now uses the --os_ram value for the initial "head" node.
- `wr status` now shows the status of currently running commands.
- Mistakes in typing a `wr` command no longer duplicate the error message.
- Test timings relaxed to hopefully pass in Travis more reliably.

### Changed
- The release zip uploaded to github has a better architecture name (x86-64
  instead of amd64).
- Removed the unused deploypidfile config option.
- Removed the unused ssh package.


## [0.1.0] - 2016-10-14
### Added
- First release of wr
- No workflow implementation
- Adding and automated running of commands
- Run commands on local machine, via LSF, or via OpenStack
