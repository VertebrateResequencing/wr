// Copyright © 2017, 2018 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
// Some of the read code in this file is inspired by the work of Ka-Hing Cheung
// in https://github.com/kahing/goofys
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

// This file implements pathfs.File methods for remote and cached files.

import (
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/fuse/nodefs"
	"github.com/inconshreveable/log15"
)

// remoteFile struct is muxfys' implementation of pathfs.File for reading data
// directly from a remote file system or object store.
type remoteFile struct {
	nodefs.File
	r             *remote
	path          string
	mutex         sync.Mutex
	attr          *fuse.Attr
	readOffset    int64
	readWorked    bool
	reader        io.ReadCloser
	rpipe         *io.PipeReader
	wpipe         *io.PipeWriter
	writeOffset   int64
	writeComplete chan bool
	skips         map[int64][]byte
	log15.Logger
}

// newRemoteFile creates a new RemoteFile. For all the methods not yet
// implemented, fuse will get a not yet implemented error.
func newRemoteFile(r *remote, path string, attr *fuse.Attr, create bool, logger log15.Logger) nodefs.File {
	f := &remoteFile{
		File:   nodefs.NewDefaultFile(),
		r:      r,
		path:   path,
		attr:   attr,
		skips:  make(map[int64][]byte),
		Logger: logger.New("path", path),
	}

	if create {
		f.rpipe, f.wpipe = io.Pipe()
		ready, finished := r.uploadData(f.rpipe, path)
		<-ready
		f.writeComplete = finished
	}

	return f
}

// Read supports random reading of data from the file. This gets called as many
// times as are needed to get through all the desired data len(buf) bytes at a
// time.
func (f *remoteFile) Read(buf []byte, offset int64) (fuse.ReadResult, fuse.Status) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	if uint64(offset) >= f.attr.Size {
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
			if offset > f.readOffset && (offset-f.readOffset) < int64((len(buf)*6)) {
				// read from reader until we get to the correct position,
				// storing what we skipped
				skippedPos := f.readOffset
				skipSize := offset - f.readOffset
				skipped := make([]byte, skipSize)
				status := f.fillBuffer(skipped, f.readOffset)
				if status != fuse.OK {
					return nil, status
				}
				lb := int64(len(buf))
				if skipSize <= lb {
					f.skips[skippedPos] = skipped
				} else {
					var o int64
					for p := skippedPos; p < offset; p += lb {
						if o+lb > skipSize {
							f.skips[p] = skipped[o:]
							break
						}
						f.skips[p] = skipped[o : o+lb]
						o += lb
					}
				}
			} else if skipped, existed := f.skips[offset]; existed && len(buf) == len(skipped) {
				// service the request from the bytes we previously skipped
				copy(buf, skipped)
				delete(f.skips, offset)
				return fuse.ReadResultData(buf), fuse.OK
			} else {
				// we'll have to seek and wipe our skips
				var status fuse.Status
				f.reader, status = f.r.seek(f.reader, offset, f.path)
				if status != fuse.OK {
					return nil, status
				}
				f.skips = make(map[int64][]byte)
			}
		}
		f.readOffset = offset
	}

	// if opened previously, read from existing reader and return
	if f.reader != nil {
		status := f.fillBuffer(buf, offset)
		return fuse.ReadResultData(buf), status
	}

	// otherwise open remote object (if it doesn't exist, we only get an error
	// when we try to fillBuffer, but that's OK)
	reader, status := f.r.getObject(f.path, offset)
	if status != fuse.OK {
		return fuse.ReadResultData([]byte{}), status
	}

	// store the reader to read from later
	f.reader = reader

	status = f.fillBuffer(buf, offset)
	if status != fuse.OK {
		return fuse.ReadResultData([]byte{}), status
	}
	return fuse.ReadResultData(buf), status
}

// fillBuffer reads from our remote reader to the Read() buffer.
func (f *remoteFile) fillBuffer(buf []byte, offset int64) (status fuse.Status) {
	// io.ReadFull throws away errors if enough bytes were read; implement our
	// own just in case weird stuff happens. It's also annoying in converting
	// EOF errors to ErrUnexpectedEOF, which we don't do here
	var bytesRead int
	min := len(buf)
	var err error
	for bytesRead < min && err == nil {
		var nn int
		nn, err = f.reader.Read(buf[bytesRead:])
		bytesRead += nn
	}

	if err != nil {
		errc := f.reader.Close()
		if errc != nil {
			f.Warn("fillBuffer reader close failed", "err", errc)
		}
		f.reader = nil
		if err == io.EOF {
			f.Info("fillBuffer read reached eof")
			status = fuse.OK
		} else {
			f.Error("fillBuffer read failed", "err", err)
			if f.readWorked && strings.Contains(err.Error(), "reset by peer") {
				// if connection reset by peer and a read previously worked
				// we try getting a new object before trying again, to cope with
				// temporary networking issues
				reader, goStatus := f.r.getObject(f.path, offset)
				if goStatus == fuse.OK {
					f.Info("fillBuffer retry got the object")
					f.reader = reader
					f.readWorked = false
					return f.fillBuffer(buf, offset)
				}
				f.Error("fillBuffer retry failed to get the object")
			}
			status = f.r.statusFromErr("Read("+f.path+")", err)
		}
		f.readOffset = 0
		return
	}
	f.readWorked = true
	f.readOffset += int64(bytesRead)
	return fuse.OK
}

// Write supports serial writes of data directly to a remote file, where
// remoteFile was made with newRemoteFile() with the create boolean set to true.
func (f *remoteFile) Write(data []byte, offset int64) (uint32, fuse.Status) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	if len(data) == 0 {
		// ignore zero-length writes that come in for some reason
		return uint32(0), fuse.OK
	}

	if offset != f.writeOffset {
		// we can't handle non-serial writes
		f.Warn("Write can't handle non-serial writes")
		return uint32(0), fuse.EIO
	}

	if f.wpipe == nil {
		// shouldn't happen: trying to write after close (Flush()), or without
		// making the remoteFile with create true.
		f.Warn("Write when wipipe nil")
		return uint32(0), fuse.EIO
	}

	n, err := f.wpipe.Write(data)

	f.writeOffset += int64(n)
	f.attr.Size += uint64(n)
	mTime := uint64(time.Now().Unix())
	f.attr.Mtime = mTime
	f.attr.Atime = mTime

	return uint32(n), fuse.ToStatus(err)
}

// Flush, despite the name, is called for close() calls on file descriptors. It
// may be called more than once at the end, and may be called at the start,
// however.
func (f *remoteFile) Flush() fuse.Status {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	if f.readOffset > 0 && f.reader != nil {
		errc := f.reader.Close()
		if errc != nil {
			f.Warn("Flush reader close failed", "err", errc)
		}
		f.reader = nil
	}

	if f.writeOffset > 0 && f.wpipe != nil {
		errc := f.wpipe.Close()
		if errc != nil {
			f.Warn("Flush wpipe close failed", "err", errc)
		}
		f.writeOffset = 0
		worked := <-f.writeComplete
		if worked {
			errc = f.rpipe.Close()
			if errc != nil {
				f.Warn("Flush rpipe close failed", "err", errc)
			}
		}
		f.wpipe = nil
		f.rpipe = nil
	}

	return fuse.OK
}

// Release is called before the file handle is forgotten, so we do final
// cleanup not done in Flush().
func (f *remoteFile) Release() {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.skips = make(map[int64][]byte)
}

// Fsync always returns OK as opposed to "not implemented" so that write-sync-
// write works.
func (f *remoteFile) Fsync(flags int) fuse.Status {
	return fuse.OK
}

// Truncate could be called as a prelude to writing and in alternative to
// making this remoteFile in create mode.
func (f *remoteFile) Truncate(size uint64) fuse.Status {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.attr.Size = size
	if f.wpipe == nil {
		f.rpipe, f.wpipe = io.Pipe()
		ready, finished := f.r.uploadData(f.rpipe, f.path)
		<-ready
		f.writeComplete = finished
	}
	return fuse.OK
}

// cachedFile is used as a wrapper around a nodefs.loopbackFile, the only
// difference being that on Write it updates the given attr's Size, Mtime and
// Atime, and on Read it copies data from remote to local disk if not requested
// before.
type cachedFile struct {
	nodefs.File
	r          *remote
	remotePath string
	localPath  string
	flags      int
	attr       *fuse.Attr
	remoteFile *remoteFile
	openedRW   bool
	mutex      sync.Mutex
	log15.Logger
}

// newCachedFile makes a CachedFile that reads each byte from remotePath only
// once, returning subsequent reads from and writing to localPath.
func newCachedFile(r *remote, remotePath, localPath string, attr *fuse.Attr, flags uint32, logger log15.Logger) nodefs.File {
	f := &cachedFile{
		r:          r,
		remotePath: remotePath,
		localPath:  localPath,
		flags:      int(flags),
		attr:       attr,
		Logger:     logger.New("rpath", remotePath, "lpath", localPath),
	}
	f.makeLoopback()
	f.remoteFile = newRemoteFile(r, remotePath, attr, false, logger).(*remoteFile)
	return f
}

func (f *cachedFile) makeLoopback() {
	localFile, err := os.OpenFile(f.localPath, f.flags, os.FileMode(fileMode))
	if err != nil {
		f.Error("Could not open file", "err", err)
	}

	if f.flags&os.O_RDWR != 0 {
		f.openedRW = true
	} else {
		f.openedRW = false
	}

	f.File = nodefs.NewLoopbackFile(localFile)
}

// InnerFile returns the loopbackFile that deals with local files on disk.
func (f *cachedFile) InnerFile() nodefs.File {
	return f.File
}

// Write passes the real work to our InnerFile(), also updating our cached
// attr.
func (f *cachedFile) Write(data []byte, offset int64) (uint32, fuse.Status) {
	n, s := f.InnerFile().Write(data, offset)
	size := uint64(offset) + uint64(n)
	if size > f.attr.Size {
		f.attr.Size = size // instead of += n, since offsets could come out of order
	}
	mTime := uint64(time.Now().Unix())
	f.attr.Mtime = mTime
	f.attr.Atime = mTime
	f.r.Cached(f.localPath, NewInterval(offset, int64(n)))
	return n, s
}

// Utimens gets called by things like `touch -d "2006-01-02 15:04:05" filename`,
// and we need to update our cached attr as well as the local file.
func (f *cachedFile) Utimens(Atime *time.Time, Mtime *time.Time) (status fuse.Status) {
	status = f.InnerFile().Utimens(Atime, Mtime)
	if status == fuse.OK {
		f.attr.Atime = uint64(Atime.Unix())
		f.attr.Mtime = uint64(Mtime.Unix())
	}
	return status
}

// Read checks to see if we've previously stored these bytes in our local
// cached file, and if so just defers to our InnerFile(). If not, gets the data
// from the remote file and stores it in the cache file.
func (f *cachedFile) Read(buf []byte, offset int64) (fuse.ReadResult, fuse.Status) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	if uint64(offset) >= f.attr.Size {
		// nothing to read
		return nil, fuse.OK
	}

	// find which bytes we haven't previously read
	request := NewInterval(offset, int64(len(buf)))
	if request.End >= int64(f.attr.Size-1) {
		request.End = int64(f.attr.Size - 1)
	}
	newIvs := f.r.Uncached(f.localPath, request)

	// *** have tried using a single RemoteFile per remote, and also trying to
	// combine sets of reads on the same file, but performance is best just
	// letting different reads on the same file interleave

	// read remote data and store in cache file for the previously unread parts
	for _, iv := range newIvs {
		ivBuf := make([]byte, iv.Length())
		_, status := f.remoteFile.Read(ivBuf, iv.Start)
		if status != fuse.OK {
			// we warn instead of error because this is a "normal" situation
			// when trying to read from non-existent files
			f.Warn("Read failed", "status", status)
			return nil, status
		}

		// write the data to our cache file
		if !f.openedRW {
			f.flags = f.flags | os.O_RDWR
			f.makeLoopback()
		}
		n, s := f.InnerFile().Write(ivBuf, iv.Start)
		if s == fuse.OK && int64(n) == iv.Length() {
			f.r.Cached(f.localPath, iv)
		} else {
			f.Error("Failed to write bytes to cache file", "read", iv.Length(), "wrote", n, "status", s)
			return nil, s
		}
	}

	// read the whole region from the cache file and return
	return f.InnerFile().Read(buf, offset)
}
