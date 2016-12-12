# Change Log
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](http://keepachangelog.com/) and this
project adheres to [Semantic Versioning](http://semver.org/).


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
