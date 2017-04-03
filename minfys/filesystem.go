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

// This file implements pathfs.FileSystem methods

import (
	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/fuse/nodefs"
	"github.com/minio/minio-go"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StatFS returns a constant (faked) set of details describing a very large
// file system.
func (fs *MinFys) StatFs(name string) *fuse.StatfsOut {
	const BLOCK_SIZE = uint64(4096)
	const TOTAL_SPACE = uint64(1 * 1024 * 1024 * 1024 * 1024 * 1024) // 1PB
	const TOTAL_BLOCKS = uint64(TOTAL_SPACE / BLOCK_SIZE)
	const INODES = uint64(1 * 1000 * 1000 * 1000) // 1 billion
	const IOSIZE = uint32(1 * 1024 * 1024)        // 1MB
	return &fuse.StatfsOut{
		Blocks: BLOCK_SIZE,
		Bfree:  TOTAL_BLOCKS,
		Bavail: TOTAL_BLOCKS,
		Files:  INODES,
		Ffree:  INODES,
		Bsize:  IOSIZE,
		// NameLen uint32
		// Frsize  uint32
		// Padding uint32
		// Spare   [6]uint32
	}
}

// GetPath combines any base path initially configured in Target with the
// current path, to get the real complete remote path.
func (fs *MinFys) GetPath(relPath string) string {
	return filepath.Join(fs.basePath, relPath)
}

// GetAttr finds out about a given object. Since only the name is received, if
// we haven't already cached this object from prior calls to this or OpenDir(),
// we must check if it's a file or a directory, which we do with parallel calls.
// The context is not currently used.
func (fs *MinFys) GetAttr(name string, context *fuse.Context) (attr *fuse.Attr, status fuse.Status) {
	if fs.dirs[name] {
		attr = fs.dirAttr
		status = fuse.OK
		return
	}

	var cached bool
	if attr, cached = fs.files[name]; cached {
		status = fuse.OK
		return
	}

	// simultaneously check if it's a directory or file
	fileAttrChan := make(chan *fuse.Attr, 1)
	fileStatusChan := make(chan fuse.Status, 1)
	dirAttrChan := make(chan *fuse.Attr, 1)
	dirStatusChan := make(chan fuse.Status, 1)
	go func() {
		at, st := fs.PathInfo(name, false)
		if st == fuse.OK {
			fileAttrChan <- at
		} else {
			fileStatusChan <- st
		}
	}()
	go func() {
		at, st := fs.PathInfo(name, true)
		if st == fuse.OK {
			dirAttrChan <- at
		} else {
			dirStatusChan <- st
		}
	}()

	checking := 2
	var checkStatus [2]fuse.Status
	for {
		select {
		case attr = <-fileAttrChan:
			status = fuse.OK
			return
		case st := <-fileStatusChan:
			checking--
			checkStatus[0] = st
		case attr = <-dirAttrChan:
			status = fuse.OK
			return
		case st := <-dirStatusChan:
			checking--
			checkStatus[1] = st
		}

		if checking == 0 {
			for _, st := range checkStatus {
				if st != fuse.ENOENT {
					status = st
					return
				}
			}
			status = fuse.ENOENT
			return
		}
	}
}

// PathInfo gets you the attributes of a file or directory given a full path. If
// you say it's a directory when its actually a file (or vice versa), you will
// be told the object doesn't exist. It caches any positive results, including
// the contents of the directory if dir is true and it's a directory.
func (fs *MinFys) PathInfo(name string, dir bool) (attr *fuse.Attr, status fuse.Status) {
	if dir {
		_, status = fs.openDir(name)
		if status == fuse.OK {
			attr = fs.dirAttr
		}
		return
	}

	start := time.Now()
	fullPath := fs.GetPath(name)
	info, err := fs.client.StatObject(fs.bucket, fullPath)
	fs.debug("made a StatObject(%s, %s) call during PathInfo which took %s and gave error %s", fs.bucket, fullPath, time.Since(start), err)
	if err != nil {
		if er, ok := err.(minio.ErrorResponse); ok && er.Code == "NoSuchKey" {
			status = fuse.ENOENT
		} else {
			status = fuse.EIO
		}
	} else {
		status = fuse.OK
	}

	if status == fuse.OK {
		attr = fs.cacheFile(name, info)
	}

	return
}

// cacheFile converts minio.ObjectInfo into fuse Attr and caches (permanently)
// the latter against the file's full path so we won't have to look up the file
// in S3 again.
func (fs *MinFys) cacheFile(name string, info minio.ObjectInfo) (attr *fuse.Attr) {
	mTime := uint64(info.LastModified.Unix())
	attr = &fuse.Attr{
		Mode:  fuse.S_IFREG | fs.fileMode,
		Size:  uint64(info.Size),
		Mtime: mTime,
		Atime: mTime,
		Ctime: mTime,
	}
	fs.files[name] = attr
	return
}

// OpenDir gets the contents of the given directory for eg. `ls` purposes. It
// also caches the attributes of all the files within. context is not currently
// used.
func (fs *MinFys) OpenDir(name string, context *fuse.Context) (entries []fuse.DirEntry, status fuse.Status) {
	_, exists := fs.dirs[name]
	if !exists {
		return nil, fuse.ENOENT
	}

	entries, cached := fs.dirContents[name]
	if cached {
		return entries, fuse.OK
	}

	return fs.openDir(name)
}

// openDir gets the contents of the given name, treating it as a directory,
// caching the attributes of its contents.
func (fs *MinFys) openDir(name string) (entries []fuse.DirEntry, status fuse.Status) {
	fullPath := fs.GetPath(name)
	doneCh := make(chan struct{})

	start := time.Now()
	defer func() {
		fs.debug("ListObjectsV2(%s, %s/) call for openDir took %s", fs.bucket, fullPath, time.Since(start))
	}()

	objectCh := fs.client.ListObjectsV2(fs.bucket, fullPath+"/", false, doneCh)
	fs.debug("made a ListObjectsV2(%s, %s/) call during openDir", fs.bucket, fullPath)

	var isDir bool
	for object := range objectCh {
		if object.Err != nil {
			fs.debug("openDir object in listing had error: %s", object.Err)
			status = fuse.EIO
			return
		}
		if object.Key == name {
			continue
		}

		d := fuse.DirEntry{
			Name: object.Key[len(fullPath)+1:],
		}

		if strings.HasSuffix(d.Name, "/") {
			d.Mode = uint32(fuse.S_IFDIR)
			d.Name = d.Name[0 : len(d.Name)-1]
			fs.dirs[filepath.Join(name, d.Name)] = true
			fs.debug(" - cached %s as a dir", filepath.Join(name, d.Name))
		} else {
			d.Mode = uint32(fuse.S_IFREG)
			thisPath := filepath.Join(name, d.Name)
			fs.cacheFile(thisPath, object)
			fs.debug(" - cached file %s", thisPath)
		}

		entries = append(entries, d)
		isDir = true

		// for efficiency, instead of breaking here, we'll keep looping and
		// cache all the dir contents; this does mean we'll never see new
		// entries for this dir in the future
	}
	status = fuse.OK

	if isDir {
		fs.dirContents[name] = entries
	} else {
		entries = nil
		status = fuse.ENOENT
	}

	return
}

// Open is what is called when any request to read a file is made. The file must
// already have been stat'ed (eg. with a GetAttr() call), or we report the file
// doesn't exist. Neither flags nor context are currently used. If CacheData has
// been configured, we defer to openCached(). Otherwise the real implementation
// is in S3File.
func (fs *MinFys) Open(name string, flags uint32, context *fuse.Context) (nodefs.File, fuse.Status) {
	start := time.Now()
	defer func() {
		fs.debug("Open for %s took %s", name, time.Since(start))
	}()

	info, exists := fs.files[name]
	if !exists {
		return nil, fuse.ENOENT
	}

	if fs.cacheData {
		return fs.openCached(name, flags, context, info)
	}

	return NewS3File(fs, fs.GetPath(name), info.Size), fuse.OK
}

// openCached downloads the remotePath to the configure CacheDir, then all
// subsequent read/write operations are deferred to the *os.File for that local
// file. Any writes are currently lost because they're not uploaded! NB: there
// is currently no locking, so this should only be called by one process at a
// time (for the same configured CacheDir).
func (fs *MinFys) openCached(name string, flags uint32, context *fuse.Context, info *fuse.Attr) (nodefs.File, fuse.Status) {
	remotePath := fs.GetPath(name)

	// *** will need to do locking to avoid downloading the same file multiple
	// times simultaneously, including by a completely separate process using
	// the same cache dir

	// check cache file doesn't already exist
	var download bool
	dst := filepath.Join(fs.cacheDir, remotePath)
	dstStats, err := os.Stat(dst)
	if err != nil { // don't bother checking os.IsNotExist(err); we'll download based on any error
		os.Remove(dst)
		download = true
	} else {
		// check the file is the right size
		if dstStats.Size() != int64(info.Size) {
			fs.debug("openCached sizes differ: %d vs %d", dstStats.Size(), info.Size)
			os.Remove(dst)
			download = true
		} else {
			fs.debug("using cache of %s", remotePath)
		}
	}

	if download {
		s := time.Now()
		err = fs.client.FGetObject(fs.bucket, remotePath, dst)
		fs.debug("made a FGetObject(%s, %s, ...) call", fs.bucket, remotePath)
		if err != nil {
			return nil, fuse.EIO
		}
		dstStats, err := os.Stat(dst)
		if err != nil {
			os.Remove(dst)
			return nil, fuse.ToStatus(err)
		} else {
			if dstStats.Size() != int64(info.Size) {
				os.Remove(dst)
				fs.debug("openCached download vs remote sizes differ: %d vs %d", dstStats.Size(), info.Size)
				return nil, fuse.EIO
			}
		}
		fs.debug("download of %s completed fine in %s", remotePath, time.Since(s))
	}

	localFile, err := os.Open(dst)
	if err != nil {
		return nil, fuse.ToStatus(err)
	}

	return nodefs.NewLoopbackFile(localFile), fuse.OK
}
