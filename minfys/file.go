// Copyright © 2017 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
// Some of the read/write code in this file is inspired by the work of Ka-Hing
// Cheung in https://github.com/kahing/goofys
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
	"github.com/klauspost/readahead"
	"github.com/minio/minio-go"
	"io"
	"strings"
	"sync"
	"time"
)

// S3File struct is minfys' implementation of pathfs.File, providing read/write
// operations on files in the remote bucket (when we're not doing any local
// caching of data).
type S3File struct {
	nodefs.File
	fs         *MinFys
	path       string
	mutex      sync.Mutex
	size       uint64
	readOffset int64
	minioObj   io.ReadCloser
	readahead  io.ReadCloser
	skips      map[int64][]byte
}

// NewS3File creates a new S3File. For all the methods not yet implemented, fuse
// will get a not yet implemented error.
func NewS3File(fs *MinFys, path string, size uint64) nodefs.File {
	return &S3File{
		File:  nodefs.NewDefaultFile(),
		fs:    fs,
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
		if f.readahead != nil {
			// it's really expensive dealing with constant slightly out-of-order
			// requests, so we handle the typical case of the current expected
			// offset being skipped for the next offset, then coming back to the
			// expected one
			if offset > f.readOffset && (offset-f.readOffset) < int64((len(buf)*3)) {
				// read from readahead until we get to the correct position,
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
				f.minioObj.Close()
				f.readahead.Close()
				f.readahead = nil
				f.skips = nil
			}
		}
		f.readOffset = offset
	}

	// if opened previously, read from existing readahead buffer and return
	if f.readahead != nil {
		status := f.fillBuffer(buf)
		return fuse.ReadResultData(buf), status
	}

	// otherwise open remote object
	retries := 0
	var object *minio.Object
ATTEMPTS:
	for {
		var err error
		object, err = f.fs.client.GetObject(f.fs.bucket, f.path)
		f.fs.debug("made a GetObject call for %s", f.path)
		if err != nil {
			f.fs.debug("GetObject call failed: %s", err)
			if strings.Contains(err.Error(), "timeout") {
				retries++
				if retries <= 10 {
					<-time.After(10 * time.Millisecond)
					f.fs.debug(" - retrying")
					continue ATTEMPTS
				}
			}
			return fuse.ReadResultData([]byte{}), fuse.EIO
		}
		break
	}

	// seek if desired
	if offset != 0 {
		retries = 0
	SEEKATTEMPTS:
		for {
			_, err := object.Seek(offset, io.SeekStart)
			if err != nil {
				f.fs.debug("GetObject seek failed: %s", err)
				if strings.Contains(err.Error(), "timeout") {
					retries++
					if retries <= 10 {
						<-time.After(10 * time.Millisecond)
						f.fs.debug(" - retrying")
						continue SEEKATTEMPTS
					}
				}
				f.fs.debug("GetObject seek failed and giving up")
				return fuse.ReadResultData([]byte{}), fuse.EIO
			}
			break
		}
	}

	// send the minio reader object to a readahead buffer and store that to read
	// from in the future (we store the minioObj just so we can close it
	// properly)
	f.minioObj = object
	f.readahead = readahead.NewReader(object)

	status := f.fillBuffer(buf)
	return fuse.ReadResultData(buf), status
}

// fillBuffer reads from our readahead buffer in to the Read() buffer.
func (f *S3File) fillBuffer(buf []byte) (status fuse.Status) {
	bytesRead, err := io.ReadFull(f.readahead, buf)
	if err != nil {
		f.readahead.Close()
		f.readahead = nil
		f.readOffset = 0
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			status = fuse.OK
		} else {
			f.fs.debug("fillBuffer read failed: %s", err)
			status = fuse.EIO
		}
		return status
	}
	f.readOffset += int64(bytesRead)
	return fuse.OK
}

// Release makes sure we close our readers.
func (f *S3File) Release() {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.readahead != nil {
		f.minioObj.Close()
		f.readahead.Close()
	}
	f.skips = nil
}

// Fsync always returns OK as opposed to "not imlemented" so that write-sync-
// write works.
func (f *S3File) Fsync(flags int) fuse.Status {
	return fuse.OK
}
