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

package muxfys

// This file contains the implementation of remote struct and its configuration
// etc.

import (
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hanwen/go-fuse/fuse"
	"github.com/inconshreveable/log15"
	"github.com/jpillora/backoff"
	"github.com/mitchellh/go-homedir"
)

const downRemoteWaitTime = 10 * time.Minute

// RemoteConfig struct is how you configure what you want to mount, and how you
// want to cache.
type RemoteConfig struct {
	// Accessor is the RemoteAccessor for your desired remote file system type.
	// Currently there is only one implemented choice: an S3Accessor. When you
	// make a new one of these (by calling NewS3Accessor()), you will provide
	// all the connection details for accessing your remote file system.
	Accessor RemoteAccessor

	// CacheData enables caching of remote files that you read locally on disk.
	// Writes will also be staged on local disk prior to upload.
	CacheData bool

	// CacheDir is the directory used to cache data if CacheData is true.
	// (muxfys will try to create this if it doesn't exist). If not supplied
	// when CacheData is true, muxfys will create a unique temporary directory
	// in MuxFys' CacheBase directory (these get automatically deleted on
	// Unmount() - specified CacheDirs do not). Defining this makes CacheData be
	// treated as true.
	CacheDir string

	// Write enables write operations in the mount. Only set true if you know
	// you really need to write.
	Write bool
}

// RemoteAttr struct describes the attributes of a remote file or directory.
// Directories should have their Name property suffixed with a forward slash.
type RemoteAttr struct {
	Name  string    // Name of the file, including its full path
	Size  int64     // Size of the file in bytes
	MTime time.Time // Time the file was last modified
	MD5   string    // MD5 checksum of the file (if known)
}

// RemoteAccessor is the interface used by remote to actually communicate with
// the remote file system or object store. All of the methods that return an
// error may be called multiple times if there's a problem, so they should be
// idempotent.
type RemoteAccessor interface {
	// DownloadFile downloads the remote source file to the local dest path.
	DownloadFile(source, dest string) error

	// UploadFile uploads the local source path to the remote dest path,
	// recording the given contentType if possible.
	UploadFile(source, dest, contentType string) error

	// UploadData uploads a data stream in real time to the remote dest path.
	// The reader is what the remote file system or object store reads from to
	// get the data it should write to the object at dest.
	UploadData(data io.Reader, dest string) error

	// ListEntries returns a slice of all the files and directories in the given
	// remote directory (or for object stores, all files and directories with a
	// prefix of dir but excluding those that have an additional forward slash).
	ListEntries(dir string) ([]RemoteAttr, error)

	// OpenFile opens a remote file ready for reading.
	OpenFile(path string, offset int64) (io.ReadCloser, error)

	// Seek should take an object returned by OpenFile() (from the same
	// RemoteAccessor implementation) and seek to the given offset from the
	// beginning of the file.
	Seek(path string, rc io.ReadCloser, offset int64) (io.ReadCloser, error)

	// CopyFile should do a remote copy of source to dest without involving the
	// the local file system.
	CopyFile(source, dest string) error

	// DeleteFile should delete the remote file at the given path.
	DeleteFile(path string) error

	// DeleteIncompleteUpload is like DeleteFile, but only called after a failed
	// Upload*() attempt.
	DeleteIncompleteUpload(path string) error

	// ErrorIsNotExists should return true if the supplied error (retrieved from
	// any of the above methods called on the same RemoteAccessor
	// implementation) indicates a file not existing.
	ErrorIsNotExists(err error) bool

	// ErrorIsNoQuota should return true if the supplied error (retrieved from
	// any of the above methods called on the same RemoteAccessor
	// implementation) indicates insufficient quota to write some data.
	ErrorIsNoQuota(err error) bool

	// Target should return a string describing the complete location details of
	// what the accessor has been configured to access. Eg. it might be a url.
	// It is only used for logging purposes, to distinguish this Accessor from
	// others.
	Target() string

	// RemotePath should return the absolute remote path given a path relative
	// to the target point the Accessor was originally configured with.
	RemotePath(relPath string) (absPath string)

	// LocalPath should return a stable non-conflicting absolute path relative
	// to the given local path for the given absolute remote path. It should
	// include directories that ensure that different targets with the same
	// directory structure and files get different local paths. The local path
	// returned from here will be used to decide where to cache files.
	LocalPath(baseDir, remotePath string) (localPath string)
}

// remote struct is used by MuxFys to interact with some remote file system or
// object store. It embeds a CacheTracker and a RemoteAccessor to do its work.
type remote struct {
	*CacheTracker
	accessor      RemoteAccessor
	cacheData     bool
	cacheDir      string
	cacheIsTmp    bool
	maxAttempts   int
	write         bool
	clientBackoff *backoff.Backoff
	hasWorked     bool
	cbMutex       sync.Mutex
	log15.Logger
}

// newRemote creates a remote for use inside MuxFys.
func newRemote(accessor RemoteAccessor, cacheData bool, cacheDir string, cacheBase string, write bool, maxAttempts int, logger log15.Logger) (*remote, error) {
	// handle cacheData option, creating cache dir if necessary
	if !cacheData && cacheDir != "" {
		cacheData = true
	}

	if cacheDir != "" {
		var err error
		cacheDir, err = homedir.Expand(cacheDir)
		if err != nil {
			return nil, err
		}
		cacheDir, err = filepath.Abs(cacheDir)
		if err != nil {
			return nil, err
		}
		err = os.MkdirAll(cacheDir, os.FileMode(dirMode))
		if err != nil {
			return nil, err
		}
	}

	cacheIsTmp := false
	if cacheData && cacheDir == "" {
		// decide on our own cache directory
		var err error
		cacheDir, err = ioutil.TempDir(cacheBase, ".muxfys_cache")
		if err != nil {
			return nil, err
		}
		cacheIsTmp = true
	}

	return &remote{
		CacheTracker: NewCacheTracker(),
		accessor:     accessor,
		cacheData:    cacheData,
		cacheDir:     cacheDir,
		cacheIsTmp:   cacheIsTmp,
		maxAttempts:  maxAttempts,
		write:        write,
		clientBackoff: &backoff.Backoff{
			Min:    100 * time.Millisecond,
			Max:    10 * time.Second,
			Factor: 3,
			Jitter: true,
		},
		Logger: logger.New("target", accessor.Target()),
	}, nil
}

// retryFunc is used as an argument to remote.retry() - the function is retried
// until it no longer returns an error. The function should be idempotent.
type retryFunc func() error

// retry attempts to run the given func a number of times until it completes
// without error. While a RemoteAccessor implementation may do retries
// internally, it may not do retries in all circumstances, whereas we want to.
// It logs errors itself. Does not bother retrying when the error indicates a
// requested file does not exist or the quota is exceeded. "Connection reset by
// peer" errors are retried (with backoff) for at least 10mins if any remote
// calls had previously succeeded, potentially exceeding desired number of
// attempts.
func (r *remote) retry(clientMethod string, path string, rf retryFunc) fuse.Status {
	attempts := 0
	start := time.Now()
	var lastError error
ATTEMPTS:
	for {
		attempts++
		err := rf()
		if err != nil {
			lastError = err

			// return immediately if key not found or quota exceeded
			if r.accessor.ErrorIsNotExists(err) {
				r.Warn("File doesn't exist", "call", clientMethod, "path", path, "walltime", time.Since(start))
				return fuse.ENOENT
			}
			if r.accessor.ErrorIsNoQuota(err) {
				r.Warn("Quota Exceeded", "call", clientMethod, "path", path, "walltime", time.Since(start))
				return fuse.ENODATA
			}

			if strings.Contains(err.Error(), "reset by peer") {
				// special-case peer resets which could indicate a temporary but
				// multi-minute downtime
				r.cbMutex.Lock()
				if r.hasWorked && time.Since(start) < downRemoteWaitTime {
					r.Warn("Connection problem, will retry", "call", clientMethod, "path", path, "retries", attempts-1, "walltime", time.Since(start), "err", err)
					dur := r.clientBackoff.Duration()
					r.cbMutex.Unlock()
					<-time.After(dur)
					continue ATTEMPTS
				} else {
					r.cbMutex.Unlock()
				}
			}

			// otherwise blindly retry for maxAttempts times
			if attempts < r.maxAttempts {
				r.cbMutex.Lock()
				dur := r.clientBackoff.Duration()
				r.cbMutex.Unlock()
				<-time.After(dur)
				continue ATTEMPTS
			}
			r.Error("Remote call failed", "call", clientMethod, "path", path, "retries", attempts-1, "walltime", time.Since(start), "err", err)
			return fuse.EIO
		}
		if attempts-1 > 0 {
			r.Info("Remote call succeeded", "call", clientMethod, "path", path, "walltime", time.Since(start), "retries", attempts-1, "walltime", time.Since(start), "previous_err", lastError)
		} else {
			r.Info("Remote call succeeded", "call", clientMethod, "path", path, "walltime", time.Since(start))
		}
		r.cbMutex.Lock()
		r.clientBackoff.Reset()
		r.hasWorked = true
		r.cbMutex.Unlock()
		return fuse.OK
	}
}

// statusFromErr is for when you get an error from trying to use something you
// you get back from a remote, such an object from getObject. It returns the
// appropriate status and logs any error.
func (r *remote) statusFromErr(clientMethod string, err error) fuse.Status {
	if err != nil {
		if r.accessor.ErrorIsNotExists(err) {
			r.Warn("File doesn't exist", "call", clientMethod)
			return fuse.ENOENT
		}
		if r.accessor.ErrorIsNoQuota(err) {
			r.Warn("Quota Exceeded", "call", clientMethod)
			return fuse.ENODATA
		}
		r.Error("Remote call failed", "call", clientMethod, "err", err)
		return fuse.EIO
	}
	return fuse.OK
}

// getRemotePath gets the real complete remote path given the path relative to
// the configured remote mount point.
func (r *remote) getRemotePath(relPath string) string {
	return r.accessor.RemotePath(relPath)
}

// getLocalPath gets the path to the local cached file when configured with
// CacheData. You must supply the complete remote path (ie. the return value of
// getRemotePath). Returns empty string if not in CacheData mode.
func (r *remote) getLocalPath(remotePath string) string {
	if r.cacheData {
		return r.accessor.LocalPath(r.cacheDir, remotePath)
	}
	return ""
}

// uploadFile uploads the given local file to the given remote path, with
// automatic retries on failure.
func (r *remote) uploadFile(localPath, remotePath string) fuse.Status {
	// get the file's content type
	file, err := os.Open(localPath)
	if err != nil {
		r.Error("Could not open local file", "method", "uploadFile", "path", localPath, "err", err)
		return fuse.EIO
	}
	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		r.Error("Could not read local file", "method", "uploadFile", "path", localPath, "err", err)
		logClose(r.Logger, file, "upload file", "path", localPath)
		return fuse.EIO
	}
	contentType := http.DetectContentType(buffer[:n])
	logClose(r.Logger, file, "upload file", "path", localPath)

	// upload, with automatic retries
	rf := func() error {
		return r.accessor.UploadFile(localPath, remotePath, contentType)
	}
	status := r.retry("UploadFile", remotePath, rf)
	if status != fuse.OK {
		errd := r.accessor.DeleteIncompleteUpload(remotePath)
		if errd != nil && !os.IsNotExist(errd) {
			r.Warn("Deletion of incomplete upload failed", "err", errd)
		}
	}
	return status
}

// uploadData uploads the given data stream to the given remote path, with
// automatic retries on failure (of the initial connection attempt). Since we
// need to write the data that the remote system will read from, we must be
// asynchronous, so we do the real work in a goroutine and return a channel that
// will receive true a little while after we've hopefully gone through any
// initialization phase (such as creating the remote file) and are now ready
// for data to start coming in. The finished channel receives true once the
// upload actually completes. (If there are any errors they get logged and
// finished receives false.)
func (r *remote) uploadData(data io.ReadCloser, remotePath string) (ready chan bool, finished chan bool) {
	// upload, with automatic retries
	rf := func() error {
		return r.accessor.UploadData(data, remotePath)
	}

	ready = make(chan bool)
	finished = make(chan bool)
	go func() {
		sentReady := make(chan bool)
		go func() {
			<-time.After(50 * time.Millisecond)
			ready <- true
			sentReady <- true
		}()
		status := r.retry("UploadData", remotePath, rf)
		<-sentReady // in case rf completes in less than 50ms
		if status == fuse.OK {
			finished <- true
		} else {
			logClose(r.Logger, data, "upload data")
			finished <- false
			errd := r.accessor.DeleteIncompleteUpload(remotePath)
			if errd != nil {
				r.Warn("Deletion of incomplete upload failed", "err", errd)
			}
		}
	}()
	return ready, finished
}

// downloadFile downloads the given remote file to the given local path, with
// automatic retries on failure.
func (r *remote) downloadFile(remotePath, localPath string) fuse.Status {
	// upload, with automatic retries
	rf := func() error {
		return r.accessor.DownloadFile(remotePath, localPath)
	}
	return r.retry("DownloadFile", remotePath, rf)
}

// findObjects returns details of all files and directories with the same prefix
// as the given path, but without "traversing" to deeper "sub-directories". Ie.
// it's like a directory listing. Returns the details and fuse.OK if there were
// no problems getting those details.
func (r *remote) findObjects(remotePath string) ([]RemoteAttr, fuse.Status) {
	// find objects, with automatic retries
	var ras []RemoteAttr
	rf := func() error {
		var err error
		ras, err = r.accessor.ListEntries(remotePath)
		return err
	}
	status := r.retry("ListEntries", remotePath, rf)
	return ras, status
}

// getObject gets the object representing an opened remote file, ready to be
// read from. Optionally also seek within it first (to the given number of bytes
// from the start of the file).
func (r *remote) getObject(remotePath string, offset int64) (io.ReadCloser, fuse.Status) {
	// get object and seek, with automatic retries
	var reader io.ReadCloser
	rf := func() error {
		var err error
		reader, err = r.accessor.OpenFile(remotePath, offset)
		return err
	}
	status := r.retry("OpenFile", remotePath, rf)
	return reader, status
}

// seek takes the object returned by getObject and seeks it to the desired
// offset from the start of the file. This may involve creating a new object,
// which is why remotePath must be supplied, and why you get back an object.
// This might be the same object you supplied if there were no problems.
func (r *remote) seek(rc io.ReadCloser, offset int64, remotePath string) (io.ReadCloser, fuse.Status) {
	var reader io.ReadCloser
	rf := func() error {
		var err error
		reader, err = r.accessor.Seek(remotePath, rc, offset)
		return err
	}
	status := r.retry("Seek", remotePath, rf)
	return reader, status
}

// copyFile remotely copies a file to a new remote path. oldPath is treated
// as a relative path to where this remote was targeted (excluding bucket),
// while newPath is treated as an absolute path (including bucket).
func (r *remote) copyFile(oldPath, newPath string) fuse.Status {
	// copy, with automatic retries
	rf := func() error {
		return r.accessor.CopyFile(oldPath, newPath)
	}
	return r.retry("CopyFile", oldPath, rf)
}

// deleteFile deletes the given remote file.
func (r *remote) deleteFile(remotePath string) fuse.Status {
	// delete, with automatic retries
	rf := func() error {
		return r.accessor.DeleteFile(remotePath)
	}
	return r.retry("DeleteFile", remotePath, rf)
}

// deleteCache physically deletes the whole cache directory and erases our
// knowledge of what parts of what files we have cached. You'd probably call
// this when unmounting, only if cacheIsTmp was true.
func (r *remote) deleteCache() (err error) {
	err = os.RemoveAll(r.cacheDir)
	r.CacheWipe()
	return
}
