// Copyright © 2017 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
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
assume they don't change (though new files will be detected).

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
However, it steals much of its read/write code from goofys (which unfortunately
has this code locked away in its internal package). It shares all of goofys'
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

Only serial reads have been implemented so far (when not using caching), and
data caching to the same local disk cache directory should not be used by
multiple processes at once.

Coming soon: proper local caching, random reads and serial writes (uploads).

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
        ReadOnly:   true,
        CacheData:  false,
    }

    fs, err := minfys.New(cfg)
    if err != nil {
        log.Fatalf("bad configuration: %s\n", err)
    }

    err = fs.Mount()
    if err != nil {
        log.Fatalf("could not mount: %s\n", err)
    }

    // read from files in /tmp/minfys/bucket

    err = fs.Unmount()
    if err != nil {
        log.Fatalf("could not unmount: %s\n", err)
    }
*/
package minfys

import (
	"fmt"
	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/fuse/nodefs"
	"github.com/hanwen/go-fuse/fuse/pathfs"
	"github.com/minio/minio-go"
	"log"
	"net/url"
	"os"
	"os/user"
	"path"
	"strconv"
	"strings"
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

	AccessKey string
	SecretKey string
	FileMode  os.FileMode
	DirMode   os.FileMode
	ReadOnly  bool
	Debug     bool
}

// MinFys struct is the main filey system object.
type MinFys struct {
	pathfs.FileSystem
	client      *minio.Client
	target      string
	secure      bool
	host        string
	bucket      string
	basePath    string
	mountPoint  string
	cacheDir    string
	fileMode    uint32
	dirMode     uint32
	dirAttr     *fuse.Attr
	server      *fuse.Server
	dirs        map[string]bool
	dirContents map[string][]fuse.DirEntry
	files       map[string]*fuse.Attr
	cacheData   bool
	debugging   bool
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
		FileSystem:  pathfs.NewDefaultFileSystem(),
		mountPoint:  config.MountPoint,
		cacheDir:    config.CacheDir,
		fileMode:    uint32(config.FileMode),
		dirMode:     uint32(config.DirMode),
		dirs:        make(map[string]bool),
		dirContents: make(map[string][]fuse.DirEntry),
		files:       make(map[string]*fuse.Attr),
		cacheData:   config.CacheData,
		debugging:   config.Debug,
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
// accessible. Do NOT call Mount() more than once on the same MinFys! *** lock and prevent multi-mounting
func (fs *MinFys) Mount() (err error) {
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
		fmt.Printf("Mount fail: %v\n", err)
		os.Exit(1)
	}

	fs.server = server
	go server.Serve()
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
// Unmount() if your process receives an os.Interrupt or syscall.SIGTERM.
func (fs *MinFys) Unmount() (err error) {
	err = fs.server.Unmount()
	return
}

// debug is our simplistic way of logging debugging messages. To get these in to
// a file, just call log.SetOutput() from the log package.
func (fs *MinFys) debug(msg string, a ...interface{}) {
	if fs.debugging {
		log.Printf("debug: %s\n", fmt.Sprintf(msg, a...))
	}
}
