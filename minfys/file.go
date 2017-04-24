// Copyright © 2017 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
// Some of the read code in this file is inspired by the work of Ka-Hing Cheung
// in https://github.com/kahing/goofys
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

// This file implements pathfs.File methods

import (
	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/fuse/nodefs"
	"io"
	"os"
	"sync"
	"time"
)

// S3File struct is minfys' implementation of pathfs.File, providing read/write
// operations on files in the remote bucket (when we're not doing any local
// caching of data).
type S3File struct {
	nodefs.File
	r          *remote
	path       string
	mutex      sync.Mutex
	size       uint64
	readOffset int64
	reader     io.ReadCloser
	skips      map[int64][]byte
}

// NewS3File creates a new S3File. For all the methods not yet implemented, fuse
// will get a not yet implemented error.
func NewS3File(r *remote, path string, size uint64) nodefs.File {
	return &S3File{
		File:  nodefs.NewDefaultFile(),
		r:     r,
		path:  path,
		size:  size,
		skips: make(map[int64][]byte),
	}
}

// Read supports random reading of data from the file. This gets called as many
// times as are needed to get through all the desired data len(buf) bytes at a
// time.
func (f *S3File) Read(buf []byte, offset int64) (fuse.ReadResult, fuse.Status) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	if uint64(offset) >= f.size {
		// nothing to read
		return nil, fuse.OK
	}

	// handle out-of-order reads, which happen even when the user request is a
	// serial read: we get offsets out of order
	if f.readOffset != offset {
		if f.reader != nil {
			// it's really expensive dealing with constant slightly out-of-order
			// requests, so we handle the typical case of the current expected
			// offset being skipped for the next offset, then coming back to the
			// expected one
			if offset > f.readOffset && (offset-f.readOffset) < int64((len(buf)*3)) {
				// read from reader until we get to the correct position,
				// storing what we skipped
				skippedPos := f.readOffset
				skipSize := offset - f.readOffset
				skipped := make([]byte, skipSize, skipSize)
				status := f.fillBuffer(skipped)
				if status != fuse.OK {
					return nil, status
				}
				f.skips[skippedPos] = skipped
			} else if skipped, existed := f.skips[offset]; existed && len(buf) == len(skipped) {
				// service the request from the bytes we previously skipped
				copy(buf, skipped)
				delete(f.skips, offset)
				return fuse.ReadResultData(buf), fuse.OK
			} else {
				// we'll have to start from scratch
				f.reader.Close()
				f.reader = nil
				f.skips = make(map[int64][]byte)
			}
		}
		f.readOffset = offset
	}

	// if opened previously, read from existing reader and return
	if f.reader != nil {
		status := f.fillBuffer(buf)
		return fuse.ReadResultData(buf), status
	}

	// otherwise open remote object (if it doesn't exist, we only get an error
	// when we try to fillBuffer, but that's OK)
	object, status := f.r.getObject(f.path, offset)
	if status != fuse.OK {
		return fuse.ReadResultData([]byte{}), status
	}

	// store the minio reader to read from later
	f.reader = object

	status = f.fillBuffer(buf)
	if status != fuse.OK {
		return fuse.ReadResultData([]byte{}), status
	}
	return fuse.ReadResultData(buf), status
}

// fillBuffer reads from our remote reader to the Read() buffer.
func (f *S3File) fillBuffer(buf []byte) (status fuse.Status) {
	bytesRead, err := io.ReadFull(f.reader, buf)
	if err != nil {
		f.reader.Close()
		f.reader = nil
		f.readOffset = 0
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			status = fuse.OK
		} else {
			status = f.r.statusFromErr("Read("+f.path+")", err)
		}
		return
	}
	f.readOffset += int64(bytesRead)
	return fuse.OK
}

// Release makes sure we close our readers.
func (f *S3File) Release() {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.reader != nil {
		f.reader.Close()
	}
	f.skips = make(map[int64][]byte)
}

// Fsync always returns OK as opposed to "not imlemented" so that write-sync-
// write works.
func (f *S3File) Fsync(flags int) fuse.Status {
	return fuse.OK
}

// CachedFile is used as a wrapper around a nodefs.loopbackFile, the only
// difference being that on Write it updates the given attr's Size, Mtime and
// Atime, and on Read it copies data from remote to local disk if not requested
// before.
type CachedFile struct {
	nodefs.File
	r          *remote
	remotePath string
	localPath  string
	flags      int
	attr       *fuse.Attr
	mutex      sync.Mutex
}

// NewCachedFile makes a CachedFile that reads from remotePath only once,
// returning subsequent reads from and writing to localPath.
func NewCachedFile(r *remote, remotePath, localPath string, attr *fuse.Attr, flags uint32) nodefs.File {
	f := &CachedFile{r: r, remotePath: remotePath, localPath: localPath, flags: int(flags), attr: attr}
	f.makeLoopback()
	return f
}

func (f *CachedFile) makeLoopback() {
	localFile, err := os.OpenFile(f.localPath, f.flags, os.FileMode(fileMode))
	if err != nil {
		f.r.fs.debug("error: could not OpenFile(%s): %s", f.localPath, err)
	}
	f.File = nodefs.NewLoopbackFile(localFile)
}

// InnerFile() returns the loopbackFile that deals with local files on disk.
func (f *CachedFile) InnerFile() nodefs.File {
	return f.File
}

// Write passes the real work to our InnerFile(), also updating our cached
// attr.
func (f *CachedFile) Write(data []byte, offset int64) (uint32, fuse.Status) {
	n, s := f.InnerFile().Write(data, offset)
	f.attr.Size += uint64(n)
	mTime := uint64(time.Now().Unix())
	f.attr.Mtime = mTime
	f.attr.Atime = mTime
	f.r.fs.downloaded[f.localPath] = true
	return n, s
}

// Utimens gets called by things like `touch -d "2006-01-02 15:04:05" filename,
// and we need to update our cached attr as well as the local file.
func (f *CachedFile) Utimens(Atime *time.Time, Mtime *time.Time) (status fuse.Status) {
	status = f.InnerFile().Utimens(Atime, Mtime)
	if status == fuse.OK {
		f.attr.Atime = uint64(Atime.Unix())
		f.attr.Mtime = uint64(Mtime.Unix())
	}
	return status
}

// Read checks to see if we've previously stored these bytes in our local
// cached file, and if so just defers to our InnerFile(). If not, gets the data
// from the remote object and stores it in the cache file.
func (f *CachedFile) Read(buf []byte, offset int64) (fuse.ReadResult, fuse.Status) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	if uint64(offset) >= f.attr.Size {
		// nothing to read
		return nil, fuse.OK
	}

	// if downloaded := f.r.fs.downloaded[f.localPath]; !downloaded {
	// 	// download whole remote object, with automatic retries
	// 	if status := f.r.downloadFile(f.remotePath, f.localPath); status != fuse.OK {
	// 		return nil, status
	// 	}

	// 	// check size ok
	// 	localStats, err := os.Stat(f.localPath)
	// 	if err != nil {
	// 		f.r.fs.debug("error: FGetObject(%s, %s) call worked, but the downloaded file had error: %s", f.r.bucket, f.remotePath, err)
	// 		os.Remove(f.localPath)
	// 		return nil, fuse.ToStatus(err)
	// 	} else {
	// 		if localStats.Size() != int64(f.attr.Size) {
	// 			os.Remove(f.localPath)
	// 			f.r.fs.debug("error: FGetObject(%s, %s) call for openCached worked, but download/remote sizes differ: %d vs %d", f.r.bucket, f.remotePath, localStats.Size(), f.attr.Size)
	// 			return nil, fuse.EIO
	// 		}
	// 	}
	// 	f.r.fs.downloaded[f.localPath] = true
	// 	f.makeLoopback()
	// }

	// we need to know if the requested range of bytes has already been stored
	// in our cache file by a previous Read() call. We store prior Read()
	// intervals, and there are many ways we could query to see if this new
	// interval overlaps with any of them, eg. having an interval tree, or
	// sorting the ranges and merging. But we expect that most of the time we
	// will only have between 1 and 3 prior intervals if we merge every time,
	// and we expect that we will merge the majority of the time. And in this
	// case, it seems to be fastest and simplest to just check against all prior
	// intervals with a nested loop. I did actually benchmark a number of
	// possibilities, and this really is fastest!
	// For ease of implementation, we only bother to read from our cache file if
	// the requested interval is entirely contained within a prior interval; if
	// it goes even a single byte into unread territory, we will fill all of buf
	// using a remote read.

	return f.InnerFile().Read(buf, offset)
}
