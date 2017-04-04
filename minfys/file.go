// Copyright © 2017 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
// Much of the critical read/write code in this file is heavily based on code in
// https://github.com/kahing/goofys Copyright 2015-2017 Ka-Hing Cheung,
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

package minfys

// This file implements pathfs.File methods

import (
	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/fuse/nodefs"
	"io"
	"sync"
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
	reader     io.ReadCloser
	readOffset int64
}

// NewS3File creates a new S3File. For all the methods not yet implemented, fuse
// will get a not yet implemented error.
func NewS3File(fs *MinFys, path string, size uint64) nodefs.File {
	return &S3File{
		File: nodefs.NewDefaultFile(),
		fs:   fs,
		path: path,
		size: size,
	}
}

// Read currently supports the serial reading of data from the file. You can
// initially seek to any point though. This gets called as many times as are
// needed to get through all the data len(buf) bytes at a time.
func (f *S3File) Read(buf []byte, offset int64) (fuse.ReadResult, fuse.Status) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	if uint64(offset) >= f.size {
		// nothing to read
		return nil, fuse.OK
	}

	if f.readOffset != offset {
		// out of order read, start fresh *** this happens even when the user
		// request is a serial read: we get offsets out of order
		if f.reader != nil {
			f.reader.Close()
			f.reader = nil
		}
		f.readOffset = offset
	}

	// if opened previously, read from existing object and return
	status := f.fillBuffer(buf)
	if status != fuse.ENODATA {
		f.fs.debug("using opened object for %s", f.path)
		return fuse.ReadResultData(buf), status
	}

	// otherwise open remote object
	object, err := f.fs.client.GetObject(f.fs.bucket, f.path)
	f.fs.debug("made a GetObject call for %s", f.path)
	if err != nil {
		f.fs.debug("GetObject call failed: %s", err)
		return fuse.ReadResultData([]byte{}), fuse.EIO
	}

	// seek if desired
	if offset != 0 {
		_, err = object.Seek(offset, io.SeekStart)
		if err != nil {
			f.fs.debug("GetObject seek failed: %s", err)
			return fuse.ReadResultData([]byte{}), fuse.EIO
		}
	}

	// store the opened object and read
	f.reader = object
	status = f.fillBuffer(buf)
	return fuse.ReadResultData(buf), status
}

// fillBuffer does the actual reading of data from the opened minio object
// representing the remote data, to the local buffer. Returns ENODATA if we
// haven't opened the object yet.
func (f *S3File) fillBuffer(buf []byte) (status fuse.Status) {
	if f.reader != nil {
		toRead := len(buf)
		bytesRead := 0
		for toRead > 0 {
			buf := buf[bytesRead : bytesRead+int(toRead)]

			nread, err := f.reader.Read(buf)
			bytesRead += nread
			toRead -= nread

			if err != nil {
				f.reader.Close()
				f.reader = nil
				f.readOffset = 0
				if err == io.EOF {
					status = fuse.OK
				} else {
					f.fs.debug("fillBuffer read failed: %s", err)
					status = fuse.EIO
				}
				return status
			}
		}
		f.readOffset += int64(bytesRead)
		return fuse.OK
	}
	return fuse.ENODATA
}

// Release makes sure we close any opened minio object for the remote data.
func (f *S3File) Release() {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.reader != nil {
		f.reader.Close()
	}
}

// Fsync always returns OK as opposed to "not imlemented" so that write-sync-
// write works.
func (f *S3File) Fsync(flags int) fuse.Status {
	return fuse.OK
}
