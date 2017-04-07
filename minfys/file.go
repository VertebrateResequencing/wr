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
	"github.com/minio/minio-go"
	"io"
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
	reader     io.ReadCloser
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

	// otherwise open remote object
	var object *minio.Object
	attempts := 0
	f.fs.clientBackoff.Reset()
	start := time.Now()
ATTEMPTS:
	for {
		attempts++
		var err error
		object, err = f.fs.client.GetObject(f.fs.bucket, f.path)
		if err != nil {
			if attempts < f.fs.maxAttempts {
				<-time.After(f.fs.clientBackoff.Duration())
				continue ATTEMPTS
			}
			f.fs.debug("error: GetObject(%s, %s) call for Read failed after %d retries and %s: %s", f.fs.bucket, f.path, attempts-1, time.Since(start), err)
			return fuse.ReadResultData([]byte{}), fuse.EIO
		}
		f.fs.debug("info: GetObject(%s, %s) call for Read took %s", f.fs.bucket, f.path, time.Since(start))
		break
	}

	// seek if desired
	if offset != 0 {
		attempts = 0
		f.fs.clientBackoff.Reset()
	SEEKATTEMPTS:
		for {
			attempts++
			_, err := object.Seek(offset, io.SeekStart)
			if err != nil {
				if attempts < f.fs.maxAttempts {
					<-time.After(f.fs.clientBackoff.Duration())
					continue SEEKATTEMPTS
				}
				f.fs.debug("error: Seek() call on object(%s, %s) for Read failed after %d retries: %s", f.fs.bucket, f.path, attempts-1, err)
				return fuse.ReadResultData([]byte{}), fuse.EIO
			}
			break
		}
	}

	// store the minio reader to read from later
	f.reader = object

	status := f.fillBuffer(buf)
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
			f.fs.debug("error: fillBuffer() ReadFull for %s failed: %s", f.path, err)
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

// cachedWriteFile is used as a wrapper around a nodefs.loopbackFile, the only
// difference being that on Write it updates the given attr's Size.
type cachedWriteFile struct {
	nodefs.File
	attr *fuse.Attr
}

// NewCachedWriteFile is for use with a nodefs.loopbackFile for a locally
// created file you want to write to while updating the Size of the given attr.
// Used to implement MinFys.Create().
func NewCachedWriteFile(f nodefs.File, attr *fuse.Attr) nodefs.File {
	return &cachedWriteFile{File: f, attr: attr}
}

func (f *cachedWriteFile) InnerFile() nodefs.File {
	return f.File
}

func (f *cachedWriteFile) Write(data []byte, off int64) (uint32, fuse.Status) {
	n, s := f.InnerFile().Write(data, off)
	f.attr.Size += uint64(n)
	return n, s
}
