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

package minfys

// This file contains the implementation of remote struct: all the code that
// interacts with the remote S3 system.

import (
	"fmt"
	"github.com/minio/minio-go"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// remote struct holds the details of each of the user's Targets. These are
// contained in a *MinFys, but also contain a reference to that *MinFys,
// primarily so that we can do easy logging and coordination amongst multiple
// remotes.
type remote struct {
	client      *minio.Client
	host        string
	bucket      string
	basePath    string
	cacheData   bool
	cacheDir    string
	deleteCache bool
	write       bool
	fs          *MinFys
}

type retryFunc func() error

// retry attempts to run the given func a number of times until it completes
// without error. While minio internally does retries, it only does them when
// it considers the failure to be retryable, whereas we always want to retry to
// handle more kinds of errors. Since it logs errors, it just returns a bool:
// true if rf ran without error.
func (r *remote) retry(clientMethod string, rf retryFunc) bool {
	attempts := 0
	start := time.Now()
ATTEMPTS:
	for {
		attempts++
		err := rf()
		if err != nil {
			if attempts < r.fs.maxAttempts {
				<-time.After(r.fs.clientBackoff.Duration())
				continue ATTEMPTS
			}
			r.fs.debug("error: %s call failed after %d retries and %s: %s", clientMethod, attempts-1, time.Since(start), err)
			return false
		}
		r.fs.debug("info: %s call took %s", clientMethod, time.Since(start))
		r.fs.clientBackoff.Reset()
		return true
	}
}

// getRemotePath combines any base path initially configured in Target with the
// current path, to get the real complete remote path.
func (r *remote) getRemotePath(relPath string) string {
	return filepath.Join(r.basePath, relPath)
}

// getLocalPath gets the path to the local cached file when configured with
// CacheData. You must supply the complete remote path (ie. the return value of
// getRemotePath). Returns empty string if not in CacheData mode.
func (r *remote) getLocalPath(remotePath string) string {
	if r.cacheData {
		return filepath.Join(r.cacheDir, r.host, r.bucket, remotePath)
	}
	return ""
}

// uploadFile uploads the given local file to the given remote path, with
// automatic retries on failure. Returns true if the upload was successful.
func (r *remote) uploadFile(localPath, remotePath string) bool {
	// get the file's content type *** don't know if this is important, or if we
	// can just fake it
	file, err := os.Open(localPath)
	if err != nil {
		r.fs.debug("error: uploadFile could not open %s: %s", localPath, err)
		return false
	}
	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		r.fs.debug("error: uploadFile could not read from %s: %s", localPath, err)
		file.Close()
		return false
	}
	contentType := http.DetectContentType(buffer[:n])
	file.Close()

	// upload, with automatic retries
	rf := func() error {
		_, err := r.client.FPutObject(r.bucket, remotePath, localPath, contentType)
		return err
	}
	return r.retry(fmt.Sprintf("FPutObject(%s, %s, %s)", r.bucket, remotePath, localPath), rf)
}

// downloadFile downloads the given remote file to the given local path, with
// automatic retries on failure. Returns true if the download was successful.
func (r *remote) downloadFile(remotePath, localPath string) bool {
	// upload, with automatic retries
	rf := func() error {
		return r.client.FGetObject(r.bucket, remotePath, localPath)
	}
	return r.retry(fmt.Sprintf("FGetObject(%s, %s)", r.bucket, remotePath), rf)
}

// findObjects returns details of all objects with the same prefix as the given
// path, but without "traversing" to deeper "sub-directories". Ie. it's like a
// directory listing. Returns the details and true if there were no problems
// getting those details.
func (r *remote) findObjects(remotePath string) (objects []minio.ObjectInfo, worked bool) {
	// find objects, with automatic retries
	rf := func() error {
		doneCh := make(chan struct{})
		objectCh := r.client.ListObjectsV2(r.bucket, remotePath, false, doneCh)
		for object := range objectCh {
			if object.Err != nil {
				close(doneCh)
				objects = nil
				return object.Err
			}
			objects = append(objects, object)
		}
		return nil
	}
	worked = r.retry(fmt.Sprintf("ListObjectsV2(%s, %s)", r.bucket, remotePath), rf)
	return
}

// getObject gets the object representing a remote file, ready to be read from.
// Optionally also seek within it first (to the given number of bytes from the
// start of the file). Returns the object and true if there were no problems.
func (r *remote) getObject(remotePath string, offset int64) (object *minio.Object, worked bool) {
	// get object and seek, with automatic retries
	rf := func() error {
		var err error
		object, err = r.client.GetObject(r.bucket, remotePath)
		if err != nil {
			return err
		}

		if offset > 0 {
			_, err = object.Seek(offset, io.SeekStart)
			if err != nil {
				return err
			}
		}

		return nil
	}
	worked = r.retry(fmt.Sprintf("GetObject(%s, %s) and/or Seek()", r.bucket, remotePath), rf)
	return
}

// copyObject remotely copies an object to a new remote path. Returns true if
// there were no problems. If there were, the new remote path won't exist.
func (r *remote) copyObject(oldPath, newPath string) bool {
	// copy, with automatic retries
	rf := func() error {
		return r.client.CopyObject(r.bucket, newPath, r.bucket+"/"+oldPath, minio.CopyConditions{})
	}
	return r.retry(fmt.Sprintf("CopyObject(%s, %s, %s)", r.bucket, newPath, r.bucket+"/"+oldPath), rf)
}

// deleteFile deletes the given remote file. Returns true if the deletion was
// successful.
func (r *remote) deleteFile(remotePath string) bool {
	// delete, with automatic retries
	rf := func() error {
		return r.client.RemoveObject(r.bucket, remotePath)
	}
	return r.retry(fmt.Sprintf("RemoveObject(%s, %s)", r.bucket, remotePath), rf)
}
