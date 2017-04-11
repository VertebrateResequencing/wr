// Copyright © 2017 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
// The parseTarget() code in this file is based on code in
// https://github.com/minio/minfs Copyright 2016 Minio, Inc.
// licensed under the Apache License, Version 2.0 (the "License"), stating:
// "You may not use this file except in compliance with the License. You may
// obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0"
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

/*
Package minfys is a pure Go library that lets you in-process temporarily
fuse-mount S3-like buckets as a "filey" system.

It has high performance, and is easy to use with nothing else to install, and no
root permissions needed (except to initially install/configure fuse: on old
linux you may need to install fuse-utils, and for macOS you'll need to install
osxfuse; for both you must ensure that 'user_allow_other' is set in
/etc/fuse.conf or equivalent).

It has good compatibility, working with AWS Signature Version 4 (Amazon S3,
Minio, et al.) and AWS Signature Version 2 (Google Cloud Storage, Openstack
Swift, Ceph Object Gateway, Riak CS, et al).

It is a "filey" system ('fys' instead of 'fs') in that it cares about
performance first, and POSIX second. It is designed around a particular use-
case:

Non-interactively read a small handful of files who's paths we already know,
probably a few times for small files and only once for large files, then upload
a few large files. Ie. we want to mount S3 buckets that contain thousands of
unchanging cache files, and a few big input files that we process using those
cache files, and finally generate some results.

In particular this means we hold on to directory and file attributes forever and
assume they don't change externally.

When using minfys, you 1) mount, 2) do something that needs the files in your S3
bucket(s), 3) unmount. Then repeat 1-3 for other things that need data in your
S3 buckets.

# Provenance

There are many ways of accessing data in S3 buckets. Amazon provide s3cmd for
direct up/download of particular files, and s3fs for fuse-mounting a bucket. But
these are not written in Go.

Amazon also provide aws-sdk-go for interacting with S3, but this does not work
with (my) Ceph Object Gateway and possibly other implementations of S3.

minio-go is an alternative Go library that provides good compatibility with a
wide variety of S3-like systems.

There are at least 3 Go libraries for creating fuse-mounted file-systems.
github.com/jacobsa/fuse was based on bazil.org/fuse, claiming higher
performance. Also claiming high performance is github.com/hanwen/go-fuse.

There are at least 2 projects that implement fuse-mounting of S3 buckets:

  * github.com/minio/minfs is implemented using minio-go and bazil, but in my
    hands was very slow. It is designed to be run as root, requiring file-based
    configuration.
  * github.com/kahing/goofys is implemented using aws-dsk-go and jacobsa/fuse,
    making it incompatible with (my) Ceph Object Gateway.

Both are designed to be run as daemons as opposed to being used in-process.

minfys is implemented using minio-go for compatibility, and hanwen/go-fuse for
speed. (In my testing, hanwen/go-fuse and jacobsa/fuse did not have noticeably
difference performance characteristics, but go-fuse was easier to write for.)
However, its read/write code is inspired by goofys. It shares all of goofys'
non-POSIX behaviours:

  * only sequential writes supported
  * does not store file mode/owner/group
  * does not support symlink or hardlink
  * `ctime`, `atime` is always the same as `mtime`
  * cannot rename non-empty directories
  * `unlink` returns success even if file is not present
  * `fsync` is ignored, files are only flushed on `close`

# Performance

To get a basic sense of performance, a 1GB file in a Ceph Object Gateway S3
bucket was read, twice in a row, using the methods that worked for me (minfs had
to be hacked, and the minio result is for using it like s3cmd with no fuse-
mounting); units are seconds needed to read the whole file:

| method         | first | second |
|----------------|-------|--------|
| s3cmd          | 6.2   | 7.5    |
| minio          | 6.3   | 6.0    |
| s3fs caching   | 12.0  | 0.8    |
| minfs          | 40    | 40     |
| minfys caching | 6.5   | 0.9    |
| minfys         | 6.3   | 5.1    |

Ie. minfs is very slow, and minfys is about 2x faster than s3fs, with no
noticeable performance penalty for fuse mounting vs simply downloading the files
you need to local disk. (You also get the benefit of being able to seek and read
only small parts of the remote file, without having to download the whole
thing.)

The same story holds true when performing the above test 100 times
~simultaneously; while some reads take much longer due to Ceph/network overload,
minfys remains on average twice as fast as s3fs. The only significant change is
that s3cmd starts to fail.

# Status

In cached mode, random reads and writes have been implemented. But the same
local disk cache directory should not be used by multiple processes at once.

In non-cached mode, only random reads have been implemented so far.

Coming soon: proper local caching, serial writes in non-cached mode, and
multiplexing of buckets on the same mount point.

# Usage

    import "github.com/VertebrateResequencing/wr/minfys"

    cfg := minfys.Config{
        Target:     "https://cog.domain.com/bucket",
        MountPoint: "/tmp/minfys/bucket",
        CacheDir:   "/tmp/minfys/cache",
        AccessKey:  os.Getenv("AWS_ACCESS_KEY_ID"),
        SecretKey:  os.Getenv("AWS_SECRET_ACCESS_KEY"),
        FileMode:   os.FileMode(0644),
        DirMode:    os.FileMode(0755),
        Retries:    3,
        ReadOnly:   true,
        CacheData:  true,
        Verbose:    true,
        Quiet:      true,
    }

    fs, err := minfys.New(cfg)
    if err != nil {
        log.Fatalf("bad configuration: %s\n", err)
    }

    err = fs.Mount()
    if err != nil {
        log.Fatalf("could not mount: %s\n", err)
    }

    // read from & write to files in /tmp/minfys/bucket

    err = fs.Unmount()
    if err != nil {
        log.Fatalf("could not unmount: %s\n", err)
    }

    logs := fs.Logs()
*/
package minfys

import (
	"fmt"
	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/fuse/nodefs"
	"github.com/hanwen/go-fuse/fuse/pathfs"
	"github.com/jpillora/backoff"
	"github.com/minio/minio-go"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Config struct provides the configuration of a MinFys.
type Config struct {
	// Local directory to mount on top of (minfys will try to create this if it
	// doesn't exist).
	MountPoint string

	// The full URL of your bucket and possible sub-path, eg.
	// https://cog.domain.com/bucket/subpath. For performance reasons, you
	// should specify the deepest subpath that holds all your files.
	Target string

	// Do you want remote files cached locally on disk?
	CacheData bool

	// If CacheData is true, where should files be stored? (minfys will try to
	// create this if it doesn't exist).
	CacheDir string

	// If a request to the remote S3 system fails, how many times should it be
	// automatically retried? The default of 0 means don't retry; at least 3 is
	// recommended.
	Retries int

	AccessKey string
	SecretKey string
	FileMode  os.FileMode
	DirMode   os.FileMode
	ReadOnly  bool

	// Errors are always logged; turning on Verbose also logs informational
	// timings on all remote requests.
	Verbose bool

	// Turning on Quiet mode means that no messages get printed to the logger,
	// though errors (and informational messages if Verbose is on) are still
	// accessible via Logs().
	Quiet bool
}

// MinFys struct is the main filey system object.
type MinFys struct {
	pathfs.FileSystem
	client        *minio.Client
	clientBackoff *backoff.Backoff
	maxAttempts   int
	target        string
	secure        bool
	host          string
	bucket        string
	basePath      string
	mountPoint    string
	readOnly      bool
	cacheDir      string
	fileMode      uint32
	dirMode       uint32
	dirAttr       *fuse.Attr
	server        *fuse.Server
	dirs          map[string]bool
	dirContents   map[string][]fuse.DirEntry
	files         map[string]*fuse.Attr
	createdFiles  map[string]bool
	cacheData     bool
	mutex         sync.Mutex
	mounted       bool
	verbose       bool
	quiet         bool
	loggedMsgs    []string
}

// New, given a configuration, returns a MinFys that you'll use to Mount() your
// S3 bucket, then Unmount() when you're done. The other methods of MinFys can
// be ignored in most cases.
func New(config Config) (fs *MinFys, err error) {
	// create mount point and cachedir if necessary
	err = os.MkdirAll(config.MountPoint, config.DirMode)
	if err != nil {
		return
	}
	if config.CacheData {
		err = os.MkdirAll(config.CacheDir, config.DirMode)
		if err != nil {
			return
		}
	}

	// initialize ourselves
	fs = &MinFys{
		FileSystem:   pathfs.NewDefaultFileSystem(),
		mountPoint:   config.MountPoint,
		readOnly:     config.ReadOnly,
		cacheDir:     config.CacheDir,
		fileMode:     uint32(config.FileMode),
		dirMode:      uint32(config.DirMode),
		dirs:         make(map[string]bool),
		dirContents:  make(map[string][]fuse.DirEntry),
		files:        make(map[string]*fuse.Attr),
		createdFiles: make(map[string]bool),
		cacheData:    config.CacheData,
		verbose:      config.Verbose,
		quiet:        config.Quiet,
		maxAttempts:  config.Retries + 1,
	}
	err = fs.parseTarget(config.Target)
	if err != nil {
		return
	}

	// create our client for interacting with S3
	client, err := minio.New(fs.host, config.AccessKey, config.SecretKey, fs.secure)
	if err != nil {
		return
	}
	fs.client = client
	fs.clientBackoff = &backoff.Backoff{
		Min:    100 * time.Millisecond,
		Max:    10 * time.Second,
		Factor: 3,
		Jitter: true,
	}

	// cheats for s3-like filesystems
	mTime := uint64(time.Now().Unix())
	fs.dirAttr = &fuse.Attr{
		Size:  uint64(4096),
		Mode:  fuse.S_IFDIR | fs.dirMode,
		Mtime: mTime,
		Atime: mTime,
		Ctime: mTime,
	}

	fs.dirs[""] = true

	return
}

// parseTarget turns eg. https://cog.domain.com/bucket/path into knowledge of
// its secure (yes), then determines the host ('cog.domain.com'), bucket
// ('bucket'), and any base path we're mounting ('path').
func (fs *MinFys) parseTarget(target string) (err error) {
	u, err := url.Parse(target)
	if err != nil {
		return
	}

	if strings.HasPrefix(target, "https") {
		fs.secure = true
	}

	fs.target = u.String()
	fs.host = u.Host

	if len(u.Path) > 1 {
		parts := strings.Split(u.Path[1:], "/")
		if len(parts) >= 0 {
			fs.bucket = parts[0]
		}
		if len(parts) >= 1 {
			fs.basePath = path.Join(parts[1:]...)
		}
	}
	return
}

// Mount carries out the mounting of your configured S3 bucket to your
// configured mount point. On return, the files in your bucket will be
// accessible. Once mounted, you can't mount again until you Unmount().
func (fs *MinFys) Mount() (err error) {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()
	if fs.mounted {
		err = fmt.Errorf("Can't mount more that once at a time\n")
		return
	}

	uid, gid, err := userAndGroup()
	if err != nil {
		return
	}

	opts := &nodefs.Options{
		NegativeTimeout: time.Second,
		AttrTimeout:     time.Second,
		EntryTimeout:    time.Second,
		Owner: &fuse.Owner{
			Uid: uid,
			Gid: gid,
		},
		Debug: false,
	}
	pathFsOpts := &pathfs.PathNodeFsOptions{ClientInodes: false}
	pathFs := pathfs.NewPathNodeFs(fs, pathFsOpts)
	conn := nodefs.NewFileSystemConnector(pathFs.Root(), opts)
	mOpts := &fuse.MountOptions{
		AllowOther:           true,
		FsName:               "MinFys",
		Name:                 "MinFys",
		RememberInodes:       true,
		DisableXAttrs:        true,
		IgnoreSecurityLabels: true,
		Debug:                false,
	}
	server, err := fuse.NewServer(conn.RawFS(), fs.mountPoint, mOpts)
	if err != nil {
		return
	}

	fs.server = server
	go server.Serve()

	fs.mounted = true
	return
}

// userAndGroup returns the current uid and gid; we only ever mount with dir and
// file permissions for the current user.
func userAndGroup() (uid uint32, gid uint32, err error) {
	user, err := user.Current()
	if err != nil {
		return
	}

	uid64, err := strconv.ParseInt(user.Uid, 10, 32)
	if err != nil {
		return
	}

	gid64, err := strconv.ParseInt(user.Gid, 10, 32)
	if err != nil {
		return
	}

	uid = uint32(uid64)
	gid = uint32(gid64)

	return
}

// Unmount must be called when you're done reading from/ writing to your bucket.
// Be sure to close any open filehandles before hand! It's a good idea to defer
// this after calling Mount(), and possibly also set up a signal trap to
// Unmount() if your process receives an os.Interrupt or syscall.SIGTERM. In
// CacheData mode, it is only at Unmount() that any files you created or altered
// get uploaded, so this may take some time.
func (fs *MinFys) Unmount() (err error) {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()
	if fs.mounted {
		err = fs.server.Unmount()
		if err == nil {
			fs.mounted = false
		}
	}

	// upload created files and delete them from the local cache
	uerr := fs.uploadCreated()
	if uerr != nil {
		if err == nil {
			err = uerr
		} else {
			err = fmt.Errorf("%s; %s", err.Error(), uerr.Error())
		}
	}

	return
}

// uploadCreated uploads any files that previously got created, then deletes
// them from the local cache. Only functions in CacheData mode.
func (fs *MinFys) uploadCreated() error {
	if fs.cacheData && !fs.readOnly {
		fails := 0
	FILES:
		for name := range fs.createdFiles {
			remotePath := fs.GetRemotePath(name)
			localPath := filepath.Join(fs.cacheDir, fs.bucket, remotePath)

			// upload file
			worked := fs.uploadFile(localPath, remotePath)
			if !worked {
				fails++
				continue FILES
			}

			// delete local copy
			syscall.Unlink(localPath)

			delete(fs.createdFiles, name)
		}

		if fails > 0 {
			return fmt.Errorf("failed to upload %d files\n", fails)
		}
	}
	return nil
}

// uploadFile uploads the given local file to the given remote path, with
// automatic retries on failure. Returns true if the upload was successful.
func (fs *MinFys) uploadFile(localPath, remotePath string) bool {
	// get the file's content type *** don't know if this is important, or if we
	// can just fake it
	file, err := os.Open(localPath)
	if err != nil {
		fs.debug("error: uploadFile could not open %s: %s", localPath, err)
		return false
	}
	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		fs.debug("error: uploadFile could not read from %s: %s", localPath, err)
		file.Close()
		return false
	}
	contentType := http.DetectContentType(buffer[:n])
	file.Close()

	// upload, with automatic retries
	attempts := 0
	fs.clientBackoff.Reset()
	start := time.Now()
ATTEMPTS:
	for {
		attempts++
		_, err = fs.client.FPutObject(fs.bucket, remotePath, localPath, contentType)
		if err != nil {
			if attempts < fs.maxAttempts {
				<-time.After(fs.clientBackoff.Duration())
				continue ATTEMPTS
			}
			fs.debug("error: FPutObject(%s, %s, %s) call for uploadCreated failed after %d retries and %s: %s", fs.bucket, remotePath, localPath, attempts-1, time.Since(start), err)
			return false
		}
		fs.debug("info: FPutObject(%s, %s, %s) call for uploadCreated took %s", fs.bucket, remotePath, localPath, time.Since(start))
		break
	}
	return true
}

// Logs returns messages generated while mounted; you might call it after
// Unmount() to see how things went. By default these will only be errors that
// occurred, but if minfys was configured with Debug on, it will also contain
// informational and warning messages. If minfys was configured with Quiet off,
// these same messages would have been printed to the logger as they occurred.
func (fs *MinFys) Logs() []string {
	return fs.loggedMsgs[:]
}

// debug is our simplistic way of logging messages. When Quiet mode is off the
// messages get printed to STDERR; to get these in to a file, just call
// log.SetOutput() from the log package. Regardless of Quiet mode, these
// messages are accessible via Logs() afterwards.
func (fs *MinFys) debug(msg string, a ...interface{}) {
	if fs.verbose || strings.HasPrefix(msg, "error") {
		logMsg := fmt.Sprintf("minfys %s", fmt.Sprintf(msg, a...))
		if !fs.quiet {
			log.Println(logMsg)
		}
		fs.loggedMsgs = append(fs.loggedMsgs, logMsg)
	}
}
