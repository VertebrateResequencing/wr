# Change Log
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](http://keepachangelog.com/) and this
project adheres to [Semantic Versioning](http://semver.org/).


## [3.0.2] - 2018-04-13
### Fixed
- Can now read from directories when multiplexing with non-existent ones.


## [3.0.1] - 2018-04-06
### Fixed
- Can list directory contents with >1000 entries.


## [3.0.0] - 2018-03-17
### Changed
- Backwards incompatible API changes: RemoteAccessor DeleteIncompleteUpload now
returns an error. RemoteAccessor OpenFile() now takes an offset to open and seek
in 1 call. RemoteAccessor Seek() now takes a path and returns a ReadCloser,
since the implementation may be the same as OpenFile().
- Makefile includes improved way of linting.
- Code delinted.
- Improved logging for errors.

### Fixed
- Tests work under MacOS


## [2.0.6] - 2018-01-17
## Fixed
- When using a shared cache directory, files cached by a prior process are now
  actually used, instead of reading from remote again.


## [2.0.5] - 2017-11-13
## Fixed
- Compiles against latest minio-go.
- Possible fix for "too many open files" when using cached mounts.


## [2.0.4] - 2017-09-20
## Fixed
- Compiles against latest minio-go.


## [2.0.3] - 2017-08-11
### Changed
- Remote reads that work but then stop working due to "connection reset by
  peer" now result in retries for 10mins instead of a failing instantly.

### Fixed
- Slow reads with unix tools like cat or cp.


## [2.0.2] - 2017-08-01
### Changed
- Remote servers that work but then stop working due to "connection reset by
  peer" now result in retries for 10mins instead of a fixed number of retries.
- The logs for successful calls now note if there had been problems, with a
  "previous_err" and a number of retries.

### Fixed
- Data race conditions have been eliminated.
- "Too many open files" problem after many reads/writes.
- "Cached size differs" problem while doing multithreaded writes to the same
  file.
- Written to file seeming to not exist afterwards.


## [2.0.1] - 2017-07-19
### Changed
- V2 signatures are now used for compatibility with Ceph + latest version of
  minio-go.

### Fixed
- Non-existent remote directories can now be mounted and accessed without error.


## [2.0.0] - 2017-06-29
### Added
- Serial writes when not cached are now implemented.

### Changed
- RemoteAccessor public interface gains 3 new methods that must be implemented:
  UploadData(), DeleteIncompleteUpload() and ErrorIsNoQuota().

### Fixed
- Failed uploads no longer leave behind incomplete upload parts.
- Memory used for caches is freed on Flush() to avoid other calls running out of
  memory.


## [1.0.0] - 2017-05-19
### Added
- First semver release of muxfys
