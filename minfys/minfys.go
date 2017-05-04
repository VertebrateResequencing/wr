// Copyright © 2017 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
// The target parsing code in this file is based on code in
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

It allows "multiplexing": you can mount multiple different buckets (or sub
directories of the same bucket) on the same local directory. This makes commands
you want to run against the files in your buckets much simpler, eg. instead of
mounting s3://publicbucket, s3://myinputbucket and s3://myoutputbucket to
separate mount points and running:
$ myexe -ref /mnt/publicbucket/refs/human/ref.fa -i /mnt/myinputbucket/xyz/123/
  input.file > /mnt/myoutputbucket/xyz/123/output.file
You could multiplex the 3 buckets (at the desired paths) on to the directory you
will work from and just run:
$ myexe -ref ref.fa -i input.file > output.file

It is a "filey" system ('fys' instead of 'fs') in that it cares about
performance first, and POSIX second. It is designed around a particular use-
case:

Non-interactively read a small handful of files who's paths you already know,
probably a few times for small files and only once for large files, then upload
a few large files. Eg. we want to mount S3 buckets that contain thousands of
unchanging cache files, and a few big input files that we process using those
cache files, and finally generate some results.

In particular this means we hold on to directory and file attributes forever and
assume they don't change externally. Permissions are ignored and only you get
read/write access.

When using minfys, you 1) mount, 2) do something that needs the files in your S3
bucket(s), 3) unmount. Then repeat 1-3 for other things that need data in your
S3 buckets.

# Provenance

There are many ways of accessing data in S3 buckets. Common tools include s3cmd
for direct up/download of particular files, and s3fs for fuse-mounting a bucket.
But these are not written in Go.

Amazon provide aws-sdk-go for interacting with S3, but this does not work with
(my) Ceph Object Gateway and possibly other implementations of S3.

minio-go is an alternative Go library that provides good compatibility with a
wide variety of S3-like systems.

There are at least 3 Go libraries for creating fuse-mounted file-systems.
github.com/jacobsa/fuse was based on bazil.org/fuse, claiming higher
performance. Also claiming high performance is github.com/hanwen/go-fuse.

There are at least 2 projects that implement fuse-mounting of S3 buckets:

  * github.com/minio/minfs is implemented using minio-go and bazil, but in my
    hands was very slow. It is designed to be run as root, requiring file-based
    configuration.
  * github.com/kahing/goofys is implemented using aws-sdk-go and jacobsa/fuse,
    making it incompatible with (my) Ceph Object Gateway.

Both are designed to be run as daemons as opposed to being used in-process.

minfys is implemented using minio-go for compatibility, and hanwen/go-fuse for
speed. (In my testing, hanwen/go-fuse and jacobsa/fuse did not have noticeably
difference performance characteristics, but go-fuse was easier to write for.)
However, some of its read code is inspired by goofys. Thanks to minimising
remote calls to the remote S3 system, and only implementing what S3 is generally
capable of, it shares and adds to goofys' non-POSIX behaviours:

  * writes are only supported in cached mode
  * does not store file mode/owner/group
  * does not support hardlinks
  * symlinks are only supported temporarily in a cached writeable mount: they
    can be created and used, but do not get uploaded
  * `atime` (and typically `ctime`) is always the same as `mtime`
  * `mtime` of files is not stored remotely (remote file mtimes are of their
    upload time, and minfys only guarantees that files are uploaded in the order
    of their mtimes)
  * does not upload empty directories, can't rename remote directories
  * `fsync` is ignored, files are only flushed on `close`

# Performance

To get a basic sense of performance, a 1GB file in a Ceph Object Gateway S3
bucket was read, twice in a row for tools with caching, using the methods that
worked for me (I had to hack minfs to get it to work); units are seconds
(average of 3 attempts) needed to read the whole file:

| method         | fresh | cached |
|----------------|-------|--------|
| s3cmd          | 5.9   | n/a    |
| mc             | 7.9   | n/a    |
| minfs          | 40    | n/a    |
| s3fs           | 12.1  | n/a    |
| s3fs caching   | 12.2  | 1.0    |
| minfys         | 5.7   | n/a    |
| minfys caching | 5.8   | 0.7    |

Ie. minfs is very slow, and minfys is about 2x faster than s3fs, with no
noticeable performance penalty for fuse mounting vs simply downloading the files
you need to local disk. (You also get the benefit of being able to seek and read
only small parts of the remote file, without having to download the whole
thing.)

The same story holds true when performing the above test 100 times
~simultaneously; while some reads take much longer due to Ceph/network overload,
minfys remains on average twice as fast as s3fs. The only significant change is
that s3cmd starts to fail.

For a real-world test, some data processing and analysis was done with samtools,
a tool that can end up reading small parts of very large files.
www.htslib.org/workflow was partially followed to map fastqs with 441 read pairs
(extracted from an old human chr20 mapping). Mapping, sorting and calling was
carried out, in addition to creating and viewing a cram. The different caching
strategies used were: cup == reference-related files cached, fastq files
uncached, working in a normal POSIX directory; cuf == as cup, but working in a
fuse mounted writable directory; uuf == as cuf, but with no caching for the
reference-related files. The local(mc) method involved downloading all files
with mc first, with the cached result being the maximum possible performance:
that of running bwa and samtools when all required files are accessed from the
local POSIX filesystem. Units are seconds (average of 3 attempts):

| method     | fresh | cached |
|------------|-------|--------|
| local(mc)  | 157   | 40     |
| s3fs.cup   | 175   | 50     |
| minfys.cup | 80    | 45     |
| minfys.cuf | 79    | 44     |
| minfys.uuf | 88    | n/a    |

Ie. minfys is about 2x faster than just downloading all required files manually,
and over 2x faster than using s3fs. There isn't much performance loss when the
data is cached vs maximum possible performance. There's no noticeable penalty
(indeed it's a little faster) for working directly in a minfys-mounted
directory.

Finally, to compare to a highly optimised tool written in C that has built-in
support (via libcurl) for reading from S3, samtools was once again used, this
time to read 100bp (the equivalent of a few lines) from an 8GB indexed cram
file. The builtin(mc) method involved downloading the single required cram cache
file from S3 first using mc, then relying on samtools' built-in S3 support by
giving it the s3:// path to the cram file; the cached result involves samtools
reading this cache file and the cram's index files from the local POSIX
filesystem, but it still reads cram data itself from the remote S3 system. The
other methods used samtools normally, giving it paths within the fuse mount(s)
created. The different caching strategies used were: cu == reference-related
files cached, cram-related files uncached; cc == everything cached; uu ==
nothing cached. Units are seconds (average of 3 attempts):

| method      | fresh | cached |
|-------------|-------|--------|
| builtin(mc) | 1.3   | 0.5    |
| s3fs.cu     | 4.3   | 1.7    |
| s3fs.cc     | 4.4   | 0.5    |
| s3fs.uu     | 4.4   | 2.2    |
| minfys.cu   | 0.3   | 0.1    |
| minfys.cc   | 0.3   | 0.06   |
| minfys.uu   | 0.3   | 0.1    |

Ie. minfys is much faster than s3fs (more than 2x faster probably due to much
faster and more efficient stating of files), and using it also gives a
significant benefit over using a tools' built-in support for S3.

# Status

In cached mode, random reads and writes have been implemented.

In non-cached mode, only random reads have been implemented so far.

Coming soon: serial writes in non-cached mode.

# Guidance

`CacheData: true` will usually give you the best performance. Not setting an
explicit CacheDir will also give the best performance, as if you read a small
part of a large file, only the part you read will be downloaded and cached in
the unique CacheDir.

Only turn on `Write` mode if you have to write.

Use `CacheData: false` if you will read more data than can be stored on local
disk in the CacheDir.

If you know that you will definitely end up reading the same data multiple times
(either during a mount, or from different mounts) on the same machine, and have
sufficient local disk space, use `CacheData: true` and set an explicit CacheDir
(with a constant absolute path, eg. starting in /tmp). Doing this results in any
file read downloading the whole remote file to cache it, which can be wasteful
if you only need to read a small part of a large file. (But this is the only way
that minfys can coordinate the cache amongst independent processes.)

# Usage

    import "github.com/VertebrateResequencing/wr/minfys"

    // fully manual target configuration
    target1 := &minfys.Target{
        Target:     "https://s3.amazonaws.com/mybucket/subdir",
        Region:     "us-east-1",
        AccessKey:  os.Getenv("AWS_ACCESS_KEY_ID"),
        SecretKey:  os.Getenv("AWS_SECRET_ACCESS_KEY"),
        CacheDir:   "/tmp/minfys/cache",
        Write:      true,
    }

    // or read some configuration from standard AWS S3 config files and
    // environment variables
    target2 := &minfys.Target{
        CacheData: true,
    }
    target2.ReadEnvironment("default", "myotherbucket/another/subdir")

    cfg := &minfys.Config{
        Mount: "/tmp/minfys/mount",
        CacheBase: "/tmp",
        Retries:    3,
        Verbose:    true,
        Quiet:      true,
        Targets:    []*minfys.Target{target, target2},
    }

    fs, err := minfys.New(cfg)
    if err != nil {
        log.Fatalf("bad configuration: %s\n", err)
    }

    err = fs.Mount()
    if err != nil {
        log.Fatalf("could not mount: %s\n", err)
    }
    fs.UnmountOnDeath()

    // read from & write to files in /tmp/minfys/mount, which contains the
    // contents of mybucket/subdir and myotherbucket/another/subdir; writes will
    // get uploaded to mybucket/subdir when you Unmount()

    err = fs.Unmount()
    if err != nil {
        log.Fatalf("could not unmount: %s\n", err)
    }

    logs := fs.Logs()
*/
package minfys

import (
	"bufio"
	"fmt"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/go-ini/ini"
	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/fuse/nodefs"
	"github.com/hanwen/go-fuse/fuse/pathfs"
	"github.com/jpillora/backoff"
	"github.com/minio/minio-go"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"os/signal"
	"os/user"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const defaultDomain = "s3.amazonaws.com"
const dirMode = 0700
const fileMode = 0600
const dirSize = uint64(4096)
const symlinkSize = uint64(7)

// Config struct provides the configuration of a MinFys.
type Config struct {
	// Mount is the local directory to mount on top of (minfys will try to
	// create this if it doesn't exist). If not supplied, defaults to the
	// subdirectory "mnt" in the current working directory. Note that mounting
	// will only succeed if the Mount directory either doesn't exist or is
	// empty.
	Mount string

	// Retries is the number of times to automatically retry failed remote S3
	// system requests. The default of 0 means don't retry; at least 3 is
	// recommended.
	Retries int

	// CacheBase is the base directory that will be used to create any Target
	// cache directories, when those Targets have CacheData true but CacheDir
	// undefined. Defaults to the current working directory.
	CacheBase string

	// Verbose turns on logging of every remote request. Errors are always
	// logged.
	Verbose bool

	// Quiet means that no messages get printed to the logger, though errors
	// (and informational messages if Verbose is on) are still accessible via
	// MinFys.Logs().
	Quiet bool

	// Targets is a slice of Target, describing what you want to mount and
	// allowing you to multiplex more than one bucket/ sub directory on to
	// Mount. Only 1 of these Target can be writeable.
	Targets []*Target
}

// Target struct provides details of the remote target (S3 bucket) you wish to
// mount, and particulars about caching and writing for this target.
type Target struct {
	// The full URL of your bucket and possible sub-path, eg.
	// https://cog.domain.com/bucket/subpath. For performance reasons, you
	// should specify the deepest subpath that holds all your files. This will
	// be set for you by a call to ReadEnvironment().
	Target string

	// Region is optional if you need to use a specific region. This can be set
	// for you by a call to ReadEnvironment().
	Region string

	// AccessKey and SecretKey can be set for you by calling ReadEnvironment().
	AccessKey string
	SecretKey string

	// CacheData enables caching of remote files that you read locally on disk.
	// Writes will also be staged on local disk prior to upload.
	CacheData bool

	// CacheDir is the directory used to cache data if CacheData is true.
	// (minfys will try to create this if it doesn't exist). If not supplied
	// when CacheData is true, minfys will create a unique temporary directory
	// in the CacheBase directory of the containing Config (these get
	// automatically deleted on Unmount() - specified CacheDirs do not).
	// Defining this makes CacheData be treated as true.
	CacheDir string

	// Write enables write operations in the mount. Only set true if you know
	// you really need to write. Since writing currently requires caching of
	// data, CacheData will be treated as true.
	Write bool
}

// ReadEnvironment sets Target, AccessKey and SecretKey and possibly Region. It
// determines these by looking primarily at the given profile section of
// ~/.s3cfg (s3cmd's config file). If profile is an empty string, it comes from
// $AWS_DEFAULT_PROFILE or $AWS_PROFILE or defaults to "default". If ~/.s3cfg
// doesn't exist or isn't fully specified, missing values will be taken from the
// file pointed to by $AWS_SHARED_CREDENTIALS_FILE, or ~/.aws/credentials (in
// the AWS CLI format) if that is not set. If this file also doesn't exist,
// ~/.awssecret (in the format used by s3fs) is used instead. AccessKey and
// SecretKey values will always preferably come from $AWS_ACCESS_KEY_ID and
// $AWS_SECRET_ACCESS_KEY respectively, if those are set. If no config file
// specified host_base, the default domain used is s3.amazonaws.com. Region is
// set by the $AWS_DEFAULT_REGION environment variable, or if that is not set,
// by checking the file pointed to by $AWS_CONFIG_FILE (~/.aws/config if unset).
// To allow the use of a single configuration file, users can create a non-
// standard file that specifies all relevant options: use_https, host_base,
// region, access_key (or aws_access_key_id) and secret_key (or
// aws_secret_access_key) (saved in any of the files except ~/.awssecret). The
// path argument should at least be the bucket name, but ideally should also
// specify the deepest subpath that holds all the files that need to be
// accessed. Because reading from a public s3.amazonaws.com bucket requires no
// credentials, no error is raised on failure to find any values in the
// environment when profile is supplied as an empty string.
func (t *Target) ReadEnvironment(profile, path string) error {
	if path == "" {
		return fmt.Errorf("minfys ReadEnvironment() requires a path")
	}

	profileSpecified := true
	if profile == "" {
		if profile = os.Getenv("AWS_DEFAULT_PROFILE"); profile == "" {
			if profile = os.Getenv("AWS_PROFILE"); profile == "" {
				profile = "default"
				profileSpecified = false
			}
		}
	}

	aws, err := ini.LooseLoad(
		internal.TildaToHome("~/.s3cfg"),
		internal.TildaToHome(os.Getenv("AWS_SHARED_CREDENTIALS_FILE")),
		internal.TildaToHome("~/.aws/credentials"),
		internal.TildaToHome(os.Getenv("AWS_CONFIG_FILE")),
		internal.TildaToHome("~/.aws/config"),
	)
	if err != nil {
		return fmt.Errorf("minfys ReadEnvironment() loose loading of config files failed: %s", err)
	}

	var domain, key, secret, region string
	var https bool
	section, err := aws.GetSection(profile)
	if err == nil {
		https = section.Key("use_https").MustBool(false)
		domain = section.Key("host_base").String()
		region = section.Key("region").String()
		key = section.Key("access_key").MustString(section.Key("aws_access_key_id").MustString(os.Getenv("AWS_ACCESS_KEY_ID")))
		secret = section.Key("secret_key").MustString(section.Key("aws_secret_access_key").MustString(os.Getenv("AWS_SECRET_ACCESS_KEY")))
	} else if profileSpecified {
		return fmt.Errorf("minfys ReadEnvironment(%s) called, but no config files defined that profile", profile)
	}

	if key == "" && secret == "" {
		// last resort, check ~/.awssecret
		awsSec := internal.TildaToHome("~/.awssecret")
		if file, err := os.Open(awsSec); err == nil {
			defer file.Close()

			scanner := bufio.NewScanner(file)
			if scanner.Scan() {
				line := scanner.Text()
				if line != "" {
					line = strings.TrimSuffix(line, "\n")
					ks := strings.Split(line, ":")
					if len(ks) == 2 {
						key = ks[0]
						secret = ks[1]
					}
				}
			}
		}
	}

	if os.Getenv("AWS_ACCESS_KEY_ID") != "" {
		key = os.Getenv("AWS_ACCESS_KEY_ID")
	}
	if os.Getenv("AWS_SECRET_ACCESS_KEY") != "" {
		secret = os.Getenv("AWS_SECRET_ACCESS_KEY")
	}
	t.AccessKey = key
	t.SecretKey = secret

	if domain == "" {
		domain = defaultDomain
	}

	scheme := "http"
	if https {
		scheme += "s"
	}
	u := &url.URL{
		Scheme: scheme,
		Host:   domain,
		Path:   path,
	}
	t.Target = u.String()

	if os.Getenv("AWS_DEFAULT_REGION") != "" {
		t.Region = os.Getenv("AWS_DEFAULT_REGION")
	} else if region != "" {
		t.Region = region
	}

	return nil
}

// createRemote uses the configured details of the Target to create a *remote,
// used internally by MinFys.New().
func (t *Target) createRemote(fs *MinFys) (r *remote, err error) {
	// parse the target to get secure, host, bucket and basePath
	if t.Target == "" {
		return nil, fmt.Errorf("no Target defined")
	}

	u, err := url.Parse(t.Target)
	if err != nil {
		return
	}

	var secure bool
	if strings.HasPrefix(t.Target, "https") {
		secure = true
	}

	host := u.Host
	var bucket, basePath string
	if len(u.Path) > 1 {
		parts := strings.Split(u.Path[1:], "/")
		if len(parts) >= 0 {
			bucket = parts[0]
		}
		if len(parts) >= 1 {
			basePath = path.Join(parts[1:]...)
		}
	}

	if bucket == "" {
		return nil, fmt.Errorf("no bucket could be determined from [%s]", t.Target)
	}

	// handle CacheData option, creating cache dir if necessary
	var cacheData bool
	if t.CacheData || t.CacheDir != "" || t.Write {
		cacheData = true
	}

	cacheDir := t.CacheDir
	if cacheDir != "" {
		cacheDir, err = filepath.Abs(internal.TildaToHome(cacheDir))
		if err != nil {
			return
		}
		err = os.MkdirAll(cacheDir, os.FileMode(dirMode))
		if err != nil {
			return
		}
	}

	deleteCache := false
	if cacheData && cacheDir == "" {
		// decide on our own cache directory
		cacheDir, err = ioutil.TempDir(fs.cacheBase, ".minfys_cache")
		if err != nil {
			return
		}
		deleteCache = true
	}

	r = &remote{
		host:        host,
		bucket:      bucket,
		basePath:    basePath,
		cacheData:   cacheData,
		cacheDir:    cacheDir,
		deleteCache: deleteCache,
		write:       t.Write,
		fs:          fs,
	}

	// create a client for interacting with S3 (we do this here instead of
	// as-needed inside remote because there's large overhead in creating these)
	if t.Region != "" {
		r.client, err = minio.NewWithRegion(host, t.AccessKey, t.SecretKey, secure, t.Region)
	} else {
		r.client, err = minio.New(host, t.AccessKey, t.SecretKey, secure)
	}
	return
}

// MinFys struct is the main filey system object.
type MinFys struct {
	pathfs.FileSystem
	mountPoint      string
	maxAttempts     int
	verbose         bool
	quiet           bool
	dirAttr         *fuse.Attr
	server          *fuse.Server
	mutex           sync.Mutex
	dirs            map[string][]*remote
	dirContents     map[string][]fuse.DirEntry
	files           map[string]*fuse.Attr
	fileToRemote    map[string]*remote
	createdFiles    map[string]bool
	createdDirs     map[string]bool
	downloaded      map[string]Intervals
	mounted         bool
	loggedMsgs      []string
	handlingSignals bool
	deathSignals    chan os.Signal
	ignoreSignals   chan bool
	clientBackoff   *backoff.Backoff
	cacheBase       string
	remotes         []*remote
	writeRemote     *remote
}

// New, given a configuration, returns a MinFys that you'll use to Mount() your
// S3 bucket(s), ensure you un-mount if killed by calling UnmountOnDeath(), then
// Unmount() when you're done. If configured with Quiet you might check Logs()
// afterwards. The other methods of MinFys can be ignored in most cases.
func New(config *Config) (fs *MinFys, err error) {
	if len(config.Targets) == 0 {
		return nil, fmt.Errorf("no targets provided")
	}

	mountPoint := config.Mount
	if mountPoint == "" {
		mountPoint = "mnt"
	}
	mountPoint, err = filepath.Abs(internal.TildaToHome(mountPoint))
	if err != nil {
		return
	}

	// create mount point if necessary
	err = os.MkdirAll(mountPoint, os.FileMode(dirMode))
	if err != nil {
		return
	}

	// check that it's empty
	entries, err := ioutil.ReadDir(mountPoint)
	if err != nil {
		return
	}
	if len(entries) > 0 {
		return nil, fmt.Errorf("Mount directory %s was not empty", mountPoint)
	}

	cacheBase := config.CacheBase
	if cacheBase == "" {
		cacheBase, err = os.Getwd()
		if err != nil {
			return
		}
	}

	// initialize ourselves
	fs = &MinFys{
		FileSystem:   pathfs.NewDefaultFileSystem(),
		mountPoint:   mountPoint,
		dirs:         make(map[string][]*remote),
		dirContents:  make(map[string][]fuse.DirEntry),
		files:        make(map[string]*fuse.Attr),
		fileToRemote: make(map[string]*remote),
		createdFiles: make(map[string]bool),
		createdDirs:  make(map[string]bool),
		downloaded:   make(map[string]Intervals),
		maxAttempts:  config.Retries + 1,
		cacheBase:    cacheBase,
		verbose:      config.Verbose,
		quiet:        config.Quiet,
	}

	fs.clientBackoff = &backoff.Backoff{
		Min:    100 * time.Millisecond,
		Max:    10 * time.Second,
		Factor: 3,
		Jitter: true,
	}

	// create a remote for every Target
	for _, t := range config.Targets {
		var r *remote
		r, err = t.createRemote(fs)
		if err != nil {
			return
		}

		fs.remotes = append(fs.remotes, r)
		if r.write {
			if fs.writeRemote != nil {
				return nil, fmt.Errorf("you can't have more than one writeable target")
			}
			fs.writeRemote = r
		}
	}

	// cheats for s3-like filesystems
	mTime := uint64(time.Now().Unix())
	fs.dirAttr = &fuse.Attr{
		Size:  dirSize,
		Mode:  fuse.S_IFDIR | uint32(dirMode),
		Mtime: mTime,
		Atime: mTime,
		Ctime: mTime,
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

// UnmountOnDeath captures SIGINT (ctrl-c) and SIGTERM (kill) signals, then
// calls Unmount() before calling os.Exit(1 if the unmount worked, 2 otherwise)
// to terminate your program. Manually calling Unmount() after this cancels the
// signal capture. This does NOT block.
func (fs *MinFys) UnmountOnDeath() {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()
	if !fs.mounted || fs.handlingSignals {
		return
	}

	fs.deathSignals = make(chan os.Signal, 2)
	signal.Notify(fs.deathSignals, os.Interrupt, syscall.SIGTERM)
	fs.handlingSignals = true
	fs.ignoreSignals = make(chan bool)

	go func() {
		select {
		case <-fs.ignoreSignals:
			signal.Stop(fs.deathSignals)
			fs.mutex.Lock()
			fs.handlingSignals = false
			fs.mutex.Unlock()
			return
		case <-fs.deathSignals:
			fs.mutex.Lock()
			fs.handlingSignals = false
			fs.mutex.Unlock()
			err := fs.Unmount()
			if err != nil {
				fs.debug("error: failed to unmount on death: %s", err)
				os.Exit(2)
			}
			os.Exit(1)
		}
	}()
}

// Unmount must be called when you're done reading from/ writing to your bucket.
// Be sure to close any open filehandles before hand! It's a good idea to defer
// this after calling Mount(), and possibly also call UnmountOnDeath(). In
// CacheData mode, it is only at Unmount() that any files you created or altered
// get uploaded, so this may take some time. You can optionally supply a bool
// which if true prevents any uploads. If a target was not configured with a
// specific CacheDir but CacheData was true, the CacheDir will be deleted.
func (fs *MinFys) Unmount(doNotUpload ...bool) (err error) {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()

	if fs.handlingSignals {
		fs.ignoreSignals <- true
	}

	if fs.mounted {
		err = fs.server.Unmount()
		if err == nil {
			fs.mounted = false
		}
	}

	if !(len(doNotUpload) > 0 && doNotUpload[0]) {
		// upload files that got opened for writing
		uerr := fs.uploadCreated()
		if uerr != nil {
			if err == nil {
				err = uerr
			} else {
				err = fmt.Errorf("%s; %s", err.Error(), uerr.Error())
			}
		}
	}

	// delete any cachedirs we created
	for _, remote := range fs.remotes {
		if remote.deleteCache {
			os.RemoveAll(remote.cacheDir)
		}
	}

	// clean out our caches; one reason to unmount is to force recognition of
	// new files when we re-mount
	fs.dirs = make(map[string][]*remote)
	fs.dirContents = make(map[string][]fuse.DirEntry)
	fs.files = make(map[string]*fuse.Attr)
	fs.fileToRemote = make(map[string]*remote)
	fs.createdFiles = make(map[string]bool)
	fs.createdDirs = make(map[string]bool)
	fs.downloaded = make(map[string]Intervals)

	return
}

// uploadCreated uploads any files that previously got created. Only functions
// in CacheData mode.
func (fs *MinFys) uploadCreated() error {
	if fs.writeRemote != nil && fs.writeRemote.cacheData {
		fails := 0

		// since mtimes in S3 are stored as the upload time, we sort our created
		// files by their mtime to at least upload them in the correct order
		var createdFiles []string
		for name := range fs.createdFiles {
			createdFiles = append(createdFiles, name)
		}
		if len(createdFiles) > 1 {
			sort.Slice(createdFiles, func(i, j int) bool {
				return fs.files[createdFiles[i]].Mtime < fs.files[createdFiles[j]].Mtime
			})
		}

		for _, name := range createdFiles {
			remotePath := fs.writeRemote.getRemotePath(name)
			localPath := fs.writeRemote.getLocalPath(remotePath)

			// upload file
			status := fs.writeRemote.uploadFile(localPath, remotePath)
			if status != fuse.OK {
				fails++
				continue
			}

			delete(fs.createdFiles, name)
		}

		if fails > 0 {
			return fmt.Errorf("failed to upload %d files\n", fails)
		}
	}
	return nil
}

// Logs returns messages generated while mounted; you might call it after
// Unmount() to see how things went. By default these will only be errors that
// occurred, but if minfys was configured with Verbose on, it will also contain
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
