# Change Log
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](http://keepachangelog.com/) and this
project adheres to [Semantic Versioning](http://semver.org/).


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
