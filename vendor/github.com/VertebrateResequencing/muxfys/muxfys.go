// Copyright © 2017, 2018 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of muxfys.
//
//  muxfys is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  muxfys is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with muxfys. If not, see <http://www.gnu.org/licenses/>.

/*
Package muxfys is a pure Go library that lets you in-process temporarily
fuse-mount remote file systems or object stores as a "filey" system. Currently
only support for S3-like systems has been implemented.

It has high performance, and is easy to use with nothing else to install, and no
root permissions needed (except to initially install/configure fuse: on old
linux you may need to install fuse-utils, and for macOS you'll need to install
osxfuse; for both you must ensure that 'user_allow_other' is set in
/etc/fuse.conf or equivalent).

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

When using muxfys, you 1) mount, 2) do something that needs the files in your S3
bucket(s), 3) unmount. Then repeat 1-3 for other things that need data in your
S3 buckets.

# Usage

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

# Extending

To add support for a new kind of remote file system or object store, simply
implement the RemoteAccessor interface and supply an instance of that to
RemoteConfig.
*/
package muxfys

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/fuse/nodefs"
	"github.com/hanwen/go-fuse/fuse/pathfs"
	"github.com/inconshreveable/log15"
	"github.com/mitchellh/go-homedir"
	"github.com/sb10/l15h"
)

const (
	dirMode     = 0700
	fileMode    = 0600
	dirSize     = uint64(4096)
	symlinkSize = uint64(7)
)

var (
	logHandlerSetter = l15h.NewChanger(log15.DiscardHandler())
	pkgLogger        = log15.New("pkg", "muxfys")
	exitFunc         = os.Exit
	deathSignals     = []os.Signal{os.Interrupt, syscall.SIGTERM}
)

func init() {
	pkgLogger.SetHandler(l15h.ChangeableHandler(logHandlerSetter))
}

// Config struct provides the configuration of a MuxFys.
type Config struct {
	// Mount is the local directory to mount on top of (muxfys will try to
	// create this if it doesn't exist). If not supplied, defaults to the
	// subdirectory "mnt" in the current working directory. Note that mounting
	// will only succeed if the Mount directory either doesn't exist or is
	// empty.
	Mount string

	// Retries is the number of times to automatically retry failed remote
	// system requests. The default of 0 means don't retry; at least 3 is
	// recommended.
	Retries int

	// CacheBase is the base directory that will be used to create cache
	// directories when a RemoteConfig that you Mount() has CacheData true but
	// CacheDir undefined. Defaults to the current working directory.
	CacheBase string

	// Verbose results in every remote request getting an entry in the output of
	// Logs(). Errors always appear there.
	Verbose bool
}

// MuxFys struct is the main filey system object.
type MuxFys struct {
	pathfs.FileSystem
	mountPoint      string
	cacheBase       string
	dirAttr         *fuse.Attr
	server          *fuse.Server
	mutex           sync.Mutex
	mapMutex        sync.RWMutex
	dirs            map[string][]*remote
	dirContents     map[string][]fuse.DirEntry
	files           map[string]*fuse.Attr
	fileToRemote    map[string]*remote
	createdFiles    map[string]bool
	createdDirs     map[string]bool
	mounted         bool
	handlingSignals bool
	deathSignals    chan os.Signal
	ignoreSignals   chan bool
	remotes         []*remote
	writeRemote     *remote
	maxAttempts     int
	logStore        *l15h.Store
	log15.Logger
}

// New returns a MuxFys that you'll use to Mount() your remote file systems or
// object stores, ensure you un-mount if killed by calling UnmountOnDeath(),
// then Unmount() when you're done. You might check Logs() afterwards. The other
// methods of MuxFys can be ignored in most cases.
func New(config *Config) (*MuxFys, error) {
	mountPoint := config.Mount
	if mountPoint == "" {
		mountPoint = "mnt"
	}
	mountPoint, err := homedir.Expand(mountPoint)
	if err != nil {
		return nil, err
	}
	mountPoint, err = filepath.Abs(mountPoint)
	if err != nil {
		return nil, err
	}

	// create mount point if necessary
	err = os.MkdirAll(mountPoint, os.FileMode(dirMode))
	if err != nil {
		return nil, err
	}

	// check that it's empty
	entries, err := ioutil.ReadDir(mountPoint)
	if err != nil {
		return nil, err
	}
	if len(entries) > 0 {
		return nil, fmt.Errorf("Mount directory %s was not empty", mountPoint)
	}

	cacheBase := config.CacheBase
	if cacheBase == "" {
		cacheBase, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}

	// make a logger with context for us, that will store log messages in memory
	// but is also capable of logging anywhere the user wants via
	// SetLogHandler()
	logger := pkgLogger.New("mount", mountPoint)
	store := l15h.NewStore()
	logLevel := log15.LvlError
	if config.Verbose {
		logLevel = log15.LvlInfo
	}
	l15h.AddHandler(logger, log15.LvlFilterHandler(logLevel, l15h.CallerInfoHandler(l15h.StoreHandler(store, log15.LogfmtFormat()))))

	// initialize ourselves
	fs := &MuxFys{
		FileSystem:   pathfs.NewDefaultFileSystem(),
		mountPoint:   mountPoint,
		cacheBase:    cacheBase,
		dirs:         make(map[string][]*remote),
		dirContents:  make(map[string][]fuse.DirEntry),
		files:        make(map[string]*fuse.Attr),
		fileToRemote: make(map[string]*remote),
		createdFiles: make(map[string]bool),
		createdDirs:  make(map[string]bool),
		maxAttempts:  config.Retries + 1,
		logStore:     store,
		Logger:       logger,
	}

	// we'll always use the same attributes for our directories
	mTime := uint64(time.Now().Unix())
	fs.dirAttr = &fuse.Attr{
		Size:  dirSize,
		Mode:  fuse.S_IFDIR | uint32(dirMode),
		Mtime: mTime,
		Atime: mTime,
		Ctime: mTime,
	}

	return fs, err
}

// Mount carries out the mounting of your supplied RemoteConfigs to your
// configured mount point. On return, the files in your remote(s) will be
// accessible.
//
// Once mounted, you can't mount again until you Unmount().
//
// If more than 1 RemoteConfig is supplied, the remotes will become multiplexed:
// your mount point will show the combined contents of all your remote systems.
// If multiple remotes have a directory with the same name, that directory's
// contents will in in turn show the contents of all those directories. If
// multiple remotes have a file with the same name in the same directory, reads
// will come from the first remote you configured that has that file.
func (fs *MuxFys) Mount(rcs ...*RemoteConfig) error {
	if len(rcs) == 0 {
		return fmt.Errorf("At least one RemoteConfig must be supplied")
	}

	fs.mutex.Lock()
	defer fs.mutex.Unlock()
	if fs.mounted {
		return fmt.Errorf("Can't mount more that once at a time")
	}

	// create a remote for every RemoteConfig
	for _, c := range rcs {
		r, err := newRemote(c.Accessor, c.CacheData, c.CacheDir, fs.cacheBase, c.Write, fs.maxAttempts, fs.Logger)
		if err != nil {
			return err
		}

		fs.remotes = append(fs.remotes, r)
		if r.write {
			if fs.writeRemote != nil {
				return fmt.Errorf("You can't have more than one writeable remote")
			}
			fs.writeRemote = r
		}
	}

	uid, gid, err := userAndGroup()
	if err != nil {
		return err
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
	pathFsOpts := &pathfs.PathNodeFsOptions{ClientInodes: false} // false means we can't hardlink, but our inodes are stable *** does it matter if they're unstable?
	pathFs := pathfs.NewPathNodeFs(fs, pathFsOpts)
	conn := nodefs.NewFileSystemConnector(pathFs.Root(), opts)
	mOpts := &fuse.MountOptions{
		AllowOther:           true,
		FsName:               "MuxFys",
		Name:                 "MuxFys",
		RememberInodes:       true,
		DisableXAttrs:        true,
		IgnoreSecurityLabels: true,
		Debug:                false,
	}
	fs.server, err = fuse.NewServer(conn.RawFS(), fs.mountPoint, mOpts)
	if err != nil {
		return err
	}

	go fs.server.Serve()
	err = fs.server.WaitMount()
	if err != nil {
		return err
	}

	fs.mounted = true
	return err
}

// userAndGroup returns the current uid and gid; we only ever mount with dir and
// file permissions for the current user.
func userAndGroup() (uid uint32, gid uint32, err error) {
	user, err := user.Current()
	if err != nil {
		return uid, gid, err
	}

	uid64, err := strconv.ParseInt(user.Uid, 10, 32)
	if err != nil {
		return uid, gid, err
	}

	gid64, err := strconv.ParseInt(user.Gid, 10, 32)
	if err != nil {
		return uid, gid, err
	}

	return uint32(uid64), uint32(gid64), err
}

// UnmountOnDeath captures SIGINT (ctrl-c) and SIGTERM (kill) signals, then
// calls Unmount() before calling os.Exit(1 if the unmount worked, 2 otherwise)
// to terminate your program. Manually calling Unmount() after this cancels the
// signal capture. This does NOT block.
func (fs *MuxFys) UnmountOnDeath() {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()
	if !fs.mounted || fs.handlingSignals {
		return
	}

	fs.deathSignals = make(chan os.Signal, 2)
	signal.Notify(fs.deathSignals, deathSignals...)
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
				fs.Error("Failed to unmount on death", "err", err)
				exitFunc(2)
				return
			}
			exitFunc(1)
			return
		}
	}()
}

// Unmount must be called when you're done reading from/ writing to your
// remotes. Be sure to close any open filehandles before hand!
//
// It's a good idea to defer this after calling Mount(), and possibly also call
// UnmountOnDeath().
//
// In CacheData mode, it is only at Unmount() that any files you created or
// altered get uploaded, so this may take some time. You can optionally supply a
// bool which if true prevents any uploads.
//
// If a remote was not configured with a specific CacheDir but CacheData was
// true, the CacheDir will be deleted.
func (fs *MuxFys) Unmount(doNotUpload ...bool) error {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()

	if fs.handlingSignals {
		fs.ignoreSignals <- true
	}

	var err error
	if fs.mounted {
		err = fs.server.Unmount()
		if err == nil {
			fs.mounted = false
		}
		// <-time.After(10 * time.Second)
	}

	if !(len(doNotUpload) == 1 && doNotUpload[0]) {
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
		if remote.cacheIsTmp {
			errd := remote.deleteCache()
			if errd != nil {
				remote.Warn("Unmount cache deletion failed", "err", errd)
				// *** this can fail on nfs due to "device or resource busy",
				// but retrying doesn't help. Waiting 10s immediately before or
				// after a failure also doesn't help; you have to always wait
				// 10s after fs.server.Unmount() to be able to delete the cache!
			}
		}
	}

	// clean out our caches; one reason to unmount is to force recognition of
	// new files when we re-mount
	fs.mapMutex.Lock()
	fs.dirs = make(map[string][]*remote)
	fs.dirContents = make(map[string][]fuse.DirEntry)
	fs.files = make(map[string]*fuse.Attr)
	fs.fileToRemote = make(map[string]*remote)
	fs.createdFiles = make(map[string]bool)
	fs.createdDirs = make(map[string]bool)
	fs.mapMutex.Unlock()

	// forget our remotes so we can be remounted with other remotes
	fs.remotes = nil
	fs.writeRemote = nil

	return err
}

// uploadCreated uploads any files that previously got created. Only functions
// in CacheData mode.
func (fs *MuxFys) uploadCreated() error {
	if fs.writeRemote != nil && fs.writeRemote.cacheData {
		fails := 0

		// since mtimes in S3 are stored as the upload time, we sort our created
		// files by their mtime to at least upload them in the correct order
		var createdFiles []string
		fs.mapMutex.Lock()
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
		fs.mapMutex.Unlock()

		if fails > 0 {
			return fmt.Errorf("failed to upload %d files", fails)
		}
	}
	return nil
}

// Logs returns messages generated while mounted; you might call it after
// Unmount() to see how things went.
//
// By default these will only be errors that occurred, but if this MuxFys was
// configured with Verbose on, it will also contain informational and warning
// messages.
//
// If the muxfys package was configured with a log Handler (see
// SetLogHandler()), these same messages would have been logged as they
// occurred.
func (fs *MuxFys) Logs() []string {
	return fs.logStore.Logs()
}

// SetLogHandler defines how log messages (globally for this package) are
// logged. Logs are always retrievable as strings from individual MuxFys
// instances using MuxFys.Logs(), but otherwise by default are discarded.
//
// To have them logged somewhere as they are emitted, supply a
// github.com/inconshreveable/log15.Handler. For example, supplying
// log15.StderrHandler would log everything to STDERR.
func SetLogHandler(h log15.Handler) {
	logHandlerSetter.SetHandler(h)
}

// logClose is for use to Close() an object during a defer when you don't care
// if the Close() returns an error, but do want non-EOF errors logged. Extra
// args are passed as additional context for the logger.
func logClose(logger log15.Logger, obj io.Closer, msg string, extra ...interface{}) {
	err := obj.Close()
	if err != nil && err.Error() != "EOF" {
		extra = append(extra, "err", err)
		logger.Warn("failed to close "+msg, extra...)
	}
}
