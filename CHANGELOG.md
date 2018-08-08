# Change Log
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](http://keepachangelog.com/) and this
project adheres to [Semantic Versioning](http://semver.org/).


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
