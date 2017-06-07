# Change Log
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](http://keepachangelog.com/) and this
project adheres to [Semantic Versioning](http://semver.org/).


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
