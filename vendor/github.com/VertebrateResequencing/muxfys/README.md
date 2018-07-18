# muxfys

[![GoDoc](https://godoc.org/github.com/VertebrateResequencing/muxfys?status.svg)](https://godoc.org/github.com/VertebrateResequencing/muxfys)
[![Go Report Card](https://goreportcard.com/badge/github.com/VertebrateResequencing/muxfys)](https://goreportcard.com/report/github.com/VertebrateResequencing/muxfys)
[![Build Status](https://travis-ci.org/VertebrateResequencing/muxfys.svg?branch=master)](https://travis-ci.org/VertebrateResequencing/muxfys)
[![Coverage Status](https://coveralls.io/repos/github/VertebrateResequencing/muxfys/badge.svg?branch=master)](https://coveralls.io/github/VertebrateResequencing/muxfys?branch=master)

    go get github.com/VertebrateResequencing/muxfys


muxfys is a pure Go library for temporarily in-process mounting multiple
different remote file systems or object stores on to the same mount point as a
"filey" system. Currently only support for S3-like systems has been implemented.

It has high performance, and is easy to use with nothing else to install, and no
root permissions needed (except to initially install/configure fuse: on old
linux you may need to install fuse-utils, and for macOS you'll need to install
osxfuse; for both you must ensure that 'user_allow_other' is set in
/etc/fuse.conf or equivalent).

It has good S3 compatibility, working with AWS Signature Version 4 (Amazon S3,
Minio, et al.) and AWS Signature Version 2 (Google Cloud Storage, Openstack
Swift, Ceph Object Gateway, Riak CS, et al).

It allows "multiplexing": you can mount multiple different S3 buckets (or sub
directories of the same bucket) on the same local directory. This makes commands
you want to run against the files in your buckets much simpler, eg. instead of
mounting s3://publicbucket, s3://myinputbucket and s3://myoutputbucket to
separate mount points and running:

    $ myexe -ref /mnt/publicbucket/refs/human/ref.fa -i /mnt/myinbucket/xyz/123/
      input.file > /mnt/myoutputbucket/xyz/123/output.file

You could multiplex the 3 buckets (at the desired paths) on to the directory you
will work from and just run:

    $ myexe -ref ref.fa -i input.file > output.file

It is a "filey" system ('fys' instead of 'fs') in that it cares about
performance and efficiency first, and POSIX second. It is designed around a
particular use-case:

Non-interactively read a small handful of files who's paths you already know,
probably a few times for small files and only once for large files, then upload
a few large files. Eg. we want to mount S3 buckets that contain thousands of
unchanging cache files, and a few big input files that we process using those
cache files, and finally generate some results.

In particular this means we hold on to directory and file attributes forever and
assume they don't change externally. Permissions are ignored and only you get
read/write access.

When using muxfys, you 1) mount, 2) do something that needs the files in your S3
bucket(s), 3) unmount. Then repeat 1-3 for other things that need data in your
S3 buckets.

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
    | muxfys         | 5.7   | n/a    |
    | muxfys caching | 5.8   | 0.7    |

Ie. minfs is very slow, and muxfys is about 2x faster than s3fs, with no
noticeable performance penalty for fuse mounting vs simply downloading the files
you need to local disk. (You also get the benefit of being able to seek and read
only small parts of the remote file, without having to download the whole
thing.)

The same story holds true when performing the above test 100 times
~simultaneously; while some reads take much longer due to Ceph/network overload,
muxfys remains on average twice as fast as s3fs. The only significant change is
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
    | muxfys.cup | 80    | 45     |
    | muxfys.cuf | 79    | 44     |
    | muxfys.uuf | 88    | n/a    |

Ie. muxfys is about 2x faster than just downloading all required files manually,
and over 2x faster than using s3fs. There isn't much performance loss when the
data is cached vs maximum possible performance. There's no noticeable penalty
(indeed it's a little faster) for working directly in a muxfys-mounted
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
    | muxfys.cu   | 0.3   | 0.1    |
    | muxfys.cc   | 0.3   | 0.06   |
    | muxfys.uu   | 0.3   | 0.1    |

Ie. muxfys is much faster than s3fs (more than 2x faster probably due to much
faster and more efficient stating of files), and using it also gives a
significant benefit over using a tools' built-in support for S3.

# Status & Limitations

The only `RemoteAccessor` implemented so far is for S3-like object stores.

In cached mode, random reads and writes have been implemented.

In non-cached mode, random reads and serial writes have been implemented.
(It is unlikely that random uncached writes will be implemented.)

Non-POSIX behaviours:

* does not store file mode/owner/group
* does not support hardlinks
* symlinks are only supported temporarily in a cached writeable mount: they
  can be created and used, but do not get uploaded
* `atime` (and typically `ctime`) is always the same as `mtime`
* `mtime` of files is not stored remotely (remote file mtimes are of their
  upload time, and muxfys only guarantees that files are uploaded in the order
  of their mtimes)
* does not upload empty directories, can't rename remote directories
* `fsync` is ignored, files are only flushed on `close`

# Guidance

`CacheData: true` will usually give you the best performance. Not setting an
explicit CacheDir will also give the best performance, as if you read a small
part of a large file, only the part you read will be downloaded and cached in
the unique CacheDir.

Only turn on `Write` mode if you have to write.

Use `CacheData: false` if you will read more data than can be stored on local
disk.

If you know that you will definitely end up reading the same data multiple times
(either during a mount, or from different mounts) on the same machine, and have
sufficient local disk space, use `CacheData: true` and set an explicit CacheDir
(with a constant absolute path, eg. starting in /tmp). Doing this results in any
file read downloading the whole remote file to cache it, which can be wasteful
if you only need to read a small part of a large file. (But this is the only way
that muxfys can coordinate the cache amongst independent processes.)

# Usage

```go
import "github.com/VertebrateResequencing/muxfys"

// fully manual S3 configuration
accessorConfig := &muxfys.S3Config{
    Target:    "https://s3.amazonaws.com/mybucket/subdir",
    Region:    "us-east-1",
    AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
    SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
}
accessor, err := muxfys.NewS3Accessor(accessorConfig)
if err != nil {
    log.Fatal(err)
}
remoteConfig1 := &muxfys.RemoteConfig{
    Accessor: accessor,
    CacheDir: "/tmp/muxfys/cache",
    Write:    true,
}

// or read configuration from standard AWS S3 config files and environment
// variables
accessorConfig, err = muxfys.S3ConfigFromEnvironment("default",
    "myotherbucket/another/subdir")
if err != nil {
    log.Fatalf("could not read config from environment: %s\n", err)
}
accessor, err = muxfys.NewS3Accessor(accessorConfig)
if err != nil {
    log.Fatal(err)
}
remoteConfig2 := &muxfys.RemoteConfig{
    Accessor:  accessor,
    CacheData: true,
}

cfg := &muxfys.Config{
    Mount:     "/tmp/muxfys/mount",
    CacheBase: "/tmp",
    Retries:   3,
    Verbose:   true,
}

fs, err := muxfys.New(cfg)
if err != nil {
    log.Fatalf("bad configuration: %s\n", err)
}

err = fs.Mount(remoteConfig, remoteConfig2)
if err != nil {
    log.Fatalf("could not mount: %s\n", err)
}
fs.UnmountOnDeath()

// read from & write to files in /tmp/muxfys/mount, which contains the
// contents of mybucket/subdir and myotherbucket/another/subdir; writes will
// get uploaded to mybucket/subdir when you Unmount()

err = fs.Unmount()
if err != nil {
    log.Fatalf("could not unmount: %s\n", err)
}

logs := fs.Logs()
```

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

muxfys is implemented using minio-go for compatibility, and hanwen/go-fuse for
speed. (In my testing, hanwen/go-fuse and jacobsa/fuse did not have noticeably
difference performance characteristics, but go-fuse was easier to write for.)
However, some of its read code is inspired by goofys. Thanks to minimising
remote calls to the remote S3 system, and only implementing what S3 is generally
capable of, it shares and adds to goofys' non-POSIX behaviours.

## Versioning

This project adheres to [Semantic Versioning](http://semver.org/). See
CHANGELOG.md for a description of changes.

If you want to rely on a stable API, vendor the library, updating within a
desired version. For example, you could use [Glide](https://glide.sh) and:

    $ glide get github.com/VertebrateResequencing/muxfys#^2.0.0
