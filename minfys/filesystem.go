// Copyright © 2017 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
// The StatFs() code in this file is based on code in
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
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

// GetRemotePath combines any base path initially configured in Target with the
// current path, to get the real complete remote path.
func (fs *MinFys) GetRemotePath(relPath string) string {
	return filepath.Join(fs.basePath, relPath)
}

// GetLocalPath gets the path to the local cached file when configured with
// CacheData. You must supply the complete remote path (ie. the return value of
// GetRemotePath). Returns empty string if not in CacheData mode.
func (fs *MinFys) GetLocalPath(remotePath string) string {
	if fs.cacheData {
		return filepath.Join(fs.cacheDir, fs.bucket, remotePath)
	}
	return ""
}

// GetAttr finds out about a given object, returning information from a
// permanent cache if possible. context is not currently used.
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

	// sequentially check if name is a file or directory. Checking
	// simultaneously doesn't really help since the remote system may queue the
	// requests serially anyway, and it's better to try and minimise requests.
	// We'll use a simple heuristic that if the name contains a '.', it's more
	// likely to be a file.
	if strings.Contains(name, ".") {
		attr, status = fs.maybeFile(name)
		if status != fuse.OK {
			attr, status = fs.maybeDir(name)
		}
	} else {
		attr, status = fs.maybeDir(name)
		if status != fuse.OK {
			attr, status = fs.maybeFile(name)
		}
	}
	return
}

// maybeDir simply calls openDir() and returns the directory attributes if
// 'name' was actually a directory.
func (fs *MinFys) maybeDir(name string) (attr *fuse.Attr, status fuse.Status) {
	_, status = fs.openDir(name)
	if status == fuse.OK {
		attr = fs.dirAttr
	}
	return
}

// maybeFile calls openDir() on the putative file's parent directory, then
// checks to see if that resulted in a file named 'name' being cached.
func (fs *MinFys) maybeFile(name string) (attr *fuse.Attr, status fuse.Status) {
	// rather than call StatObject on name to see if its a file, it's more
	// efficient to try and open it's parent directory and see if that resulted
	// in us caching the file as one of the dir's entries
	parent := filepath.Dir(name)
	if parent == "/" {
		parent = ""
	}
	if _, cached := fs.dirContents[name]; !cached {
		fs.openDir(parent)
		attr, _ = fs.files[name]
	}

	if attr != nil {
		status = fuse.OK
	} else {
		status = fuse.ENOENT
	}
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
	fullPath := fs.GetRemotePath(name)
	if fullPath != "" {
		fullPath += "/"
	}
	doneCh := make(chan struct{})

	var isDir bool
	attempts := 0
	fs.clientBackoff.Reset()
	start := time.Now()
ATTEMPTS:
	for {
		attempts++
		objectCh := fs.client.ListObjectsV2(fs.bucket, fullPath, false, doneCh)

		for object := range objectCh {
			if object.Err != nil {
				if attempts < fs.maxAttempts {
					<-time.After(fs.clientBackoff.Duration())
					continue ATTEMPTS
				}
				fs.debug("error: ListObjectsV2(%s, %s) call for openDir failed after %d retries and %s: %s", fs.bucket, fullPath, attempts-1, time.Since(start), object.Err)
				status = fuse.EIO
				return
			}
			if object.Key == name {
				continue
			}

			d := fuse.DirEntry{
				Name: object.Key[len(fullPath):],
			}

			fs.mutex.Lock()
			if strings.HasSuffix(d.Name, "/") {
				d.Mode = uint32(fuse.S_IFDIR)
				d.Name = d.Name[0 : len(d.Name)-1]
				fs.dirs[filepath.Join(name, d.Name)] = true
			} else {
				d.Mode = uint32(fuse.S_IFREG)
				thisPath := filepath.Join(name, d.Name)
				mTime := uint64(object.LastModified.Unix())
				attr := &fuse.Attr{
					Mode:  fuse.S_IFREG | fs.fileMode,
					Size:  uint64(object.Size),
					Mtime: mTime,
					Atime: mTime,
					Ctime: mTime,
				}
				fs.files[thisPath] = attr
			}
			fs.mutex.Unlock()

			entries = append(entries, d)
			isDir = true

			// for efficiency, instead of breaking here, we'll keep looping and
			// cache all the dir contents; this does mean we'll never see new
			// entries for this dir in the future
		}
		break
	}
	status = fuse.OK
	fs.debug("info: ListObjectsV2(%s, %s) call for openDir took %s", fs.bucket, fullPath, time.Since(start))

	if isDir {
		fs.mutex.Lock()
		fs.dirs[name] = true
		fs.dirContents[name] = entries
		fs.mutex.Unlock()
	} else {
		entries = nil
		status = fuse.ENOENT
	}

	return
}

// Open is what is called when any request to read a file is made. The file must
// already have been stat'ed (eg. with a GetAttr() call), or we report the file
// doesn't exist. context is not currently used. If CacheData has been
// configured, we defer to openCached(). Otherwise the real implementation is in
// S3File.
func (fs *MinFys) Open(name string, flags uint32, context *fuse.Context) (file nodefs.File, status fuse.Status) {
	info, exists := fs.files[name]
	if !exists {
		return nil, fuse.ENOENT
	}

	if fs.cacheData {
		file, status = fs.openCached(name, flags, context, info)
	} else {
		file = NewS3File(fs, fs.GetRemotePath(name), info.Size)
		status = fuse.OK
	}

	if fs.readOnly || (int(flags)&os.O_WRONLY == 0 && int(flags)&os.O_RDWR == 0) {
		file = nodefs.NewReadOnlyFile(file)
	}

	return
}

// openCached downloads the remotePath to the configured CacheDir, then all
// subsequent read/write operations are deferred to the *os.File for that local
// file. Any writes are currently lost because they're not uploaded! NB: there
// is currently no locking, so this should only be called by one process at a
// time (for the same configured CacheDir).
func (fs *MinFys) openCached(name string, flags uint32, context *fuse.Context, info *fuse.Attr) (nodefs.File, fuse.Status) {
	remotePath := fs.GetRemotePath(name)

	// *** will need to do locking to avoid downloading the same file multiple
	// times simultaneously, including by a completely separate process using
	// the same cache dir

	// check cache file doesn't already exist
	var download bool
	localPath := fs.GetLocalPath(remotePath)
	localStats, err := os.Stat(localPath)
	if err != nil { // don't bother checking os.IsNotExist(err); we'll download based on any error
		os.Remove(localPath)
		download = true
	} else {
		// check the file is the right size
		if localStats.Size() != int64(info.Size) {
			fs.debug("warning: openCached(%s) cached sizes differ: %d local vs %d remote", name, localStats.Size(), info.Size)
			os.Remove(localPath)
			download = true
		}
	}

	if download {
		// download whole remote object, with automatic retries
		attempts := 0
		fs.clientBackoff.Reset()
		start := time.Now()
	ATTEMPTS:
		for {
			attempts++
			var err error
			err = fs.client.FGetObject(fs.bucket, remotePath, localPath)
			if err != nil {
				if attempts < fs.maxAttempts {
					<-time.After(fs.clientBackoff.Duration())
					continue ATTEMPTS
				}
				fs.debug("error: FGetObject(%s, %s) call for openCached failed after %d retries and %s: %s", fs.bucket, remotePath, attempts-1, time.Since(start), err)
				return nil, fuse.EIO
			}
			fs.debug("info: FGetObject(%s, %s) call for openCached took %s", fs.bucket, remotePath, time.Since(start))
			break
		}

		// check size ok
		localStats, err := os.Stat(localPath)
		if err != nil {
			fs.debug("error: FGetObject(%s, %s) call worked, but the downloaded file had error: %s", fs.bucket, remotePath, err)
			os.Remove(localPath)
			return nil, fuse.ToStatus(err)
		} else {
			if localStats.Size() != int64(info.Size) {
				os.Remove(localPath)
				fs.debug("error: FGetObject(%s, %s) call for openCached worked, but download/remote sizes differ: %d vs %d", fs.bucket, remotePath, localStats.Size(), info.Size)
				return nil, fuse.EIO
			}
		}
	}

	// open for appending, treating it like we created the file
	if int(flags)&os.O_APPEND != 0 {
		return fs.Create(name, flags, fs.fileMode, context)
	}

	// open for reading
	localFile, err := os.Open(localPath)
	if err != nil {
		fs.debug("error: openCached(%s) could not open %s: %s", name, localPath, err)
		return nil, fuse.ToStatus(err)
	}

	return nodefs.NewLoopbackFile(localFile), fuse.OK
}

// Chmod is ignored.
func (fs *MinFys) Chmod(name string, mode uint32, context *fuse.Context) fuse.Status {
	if fs.readOnly {
		return fuse.EPERM
	}
	return fuse.OK
}

// Chown is ignored.
func (fs *MinFys) Chown(name string, uid uint32, gid uint32, context *fuse.Context) fuse.Status {
	if fs.readOnly {
		return fuse.EPERM
	}
	return fuse.OK
}

// Truncate truncates any local cached copy of the file. Only currently
// implemented for when configured with CacheData; the results of the Truncate
// are only uploaded at Unmount() time. If offset is > size of file, does
// nothing and returns OK.
func (fs *MinFys) Truncate(name string, offset uint64, context *fuse.Context) fuse.Status {
	if fs.readOnly {
		return fuse.EPERM
	}

	attr, existed := fs.files[name]
	if !existed {
		return fuse.ENOENT
	}

	if offset > attr.Size {
		return fuse.OK
	}

	remotePath := fs.GetRemotePath(name)
	if fs.cacheData {
		localPath := fs.GetLocalPath(remotePath)

		// *** as per openCached, we need locking of this file globally...

		if _, err := os.Stat(localPath); err == nil {
			// truncate local cached copy
			err := os.Truncate(localPath, int64(offset))
			if err != nil {
				return fuse.ToStatus(err)
			}
		} else {
			// create a new empty file
			dir := filepath.Dir(localPath)
			err := os.MkdirAll(dir, os.FileMode(fs.dirMode))
			if err != nil {
				fs.debug("error: Truncate(%s) could not make cache parent directory %s: %s", name, dir, err)
				return fuse.EIO
			}

			localFile, err := os.Create(localPath)
			if err != nil {
				fs.debug("error: Truncate(%s) could not create %s: %s", name, localPath, err)
				return fuse.EIO
			}

			if offset == 0 {
				localFile.Close()
			} else {
				// download offset bytes of remote file
				var object *minio.Object
				attempts := 0
				fs.clientBackoff.Reset()
				start := time.Now()
			ATTEMPTS:
				for {
					attempts++
					var err error
					object, err = fs.client.GetObject(fs.bucket, remotePath)
					if err != nil {
						if attempts < fs.maxAttempts {
							<-time.After(fs.clientBackoff.Duration())
							continue ATTEMPTS
						}
						fs.debug("error: GetObject(%s, %s) call for Truncate failed after %d retries and %s: %s", fs.bucket, remotePath, attempts-1, time.Since(start), err)
						localFile.Close()
						syscall.Unlink(localPath)
						return fuse.EIO
					}
					fs.debug("info: GetObject(%s, %s) call for Truncate took %s", fs.bucket, remotePath, time.Since(start))
					break
				}

				written, err := io.CopyN(localFile, object, int64(offset))
				if err != nil {
					fs.debug("error: Truncate(%s) failed to copy %d bytes from %s: %s", name, offset, remotePath, err)
					localFile.Close()
					syscall.Unlink(localPath)
					return fuse.EIO
				}
				if written != int64(offset) {
					fs.debug("error: Truncate(%s) failed to copy all %d bytes from %s", name, offset, remotePath)
					localFile.Close()
					syscall.Unlink(localPath)
					return fuse.EIO
				}

				localFile.Close()
				object.Close()
			}
		}

		// update attr and claim we created this file
		attr.Size = offset
		attr.Mtime = uint64(time.Now().Unix())
		fs.createdFiles[name] = true

		return fuse.OK
	}
	return fuse.ENOSYS
}

// Mkdir is ignored.
func (fs *MinFys) Mkdir(name string, mode uint32, context *fuse.Context) fuse.Status {
	if fs.readOnly {
		return fuse.EPERM
	}
	return fuse.OK
}

// Unlink deletes a file from the remote S3 bucket, as well as any locally
// cached copy. It returns OK if the file never existed.
func (fs *MinFys) Unlink(name string, context *fuse.Context) fuse.Status {
	if fs.readOnly {
		return fuse.EPERM
	}

	remotePath := fs.GetRemotePath(name)
	if fs.cacheData {
		syscall.Unlink(fs.GetLocalPath(remotePath))
	}

	attempts := 0
	fs.clientBackoff.Reset()
	start := time.Now()
ATTEMPTS:
	for {
		attempts++
		err := fs.client.RemoveObject(fs.bucket, remotePath)
		if err != nil {
			if attempts < fs.maxAttempts {
				<-time.After(fs.clientBackoff.Duration())
				continue ATTEMPTS
			}
			fs.debug("error: RemoveObject(%s, %s) call for Unlink failed after %d retries and %s: %s", fs.bucket, remotePath, attempts-1, time.Since(start), err)
			return fuse.EIO
		}
		fs.debug("info: RemoveObject(%s, %s) call for Unlink took %s", fs.bucket, remotePath, time.Since(start))
		break
	}

	delete(fs.files, name)
	delete(fs.createdFiles, name)

	return fuse.OK
}

// Rmdir is ignored.
func (fs *MinFys) Rmdir(name string, context *fuse.Context) fuse.Status {
	if fs.readOnly {
		return fuse.EPERM
	}
	return fuse.OK
}

// Rename isn't implemented yet.
func (fs *MinFys) Rename(oldPath string, newPath string, context *fuse.Context) fuse.Status {
	if fs.readOnly {
		return fuse.EPERM
	}
	// err := os.Rename(fs.GetPath(oldPath), fs.GetPath(newPath))
	// return fuse.ToStatus(err)
	return fuse.ENOSYS
}

// Access is ignored.
func (fs *MinFys) Access(name string, mode uint32, context *fuse.Context) fuse.Status {
	return fuse.OK
}

// Create creates a new file. context is not currently used. Only currently
// implemented for when configured with CacheData; the contents of the created
// file are only uploaded at Unmount() time.
func (fs *MinFys) Create(name string, flags uint32, mode uint32, context *fuse.Context) (nodefs.File, fuse.Status) {
	if fs.readOnly {
		return nil, fuse.EPERM
	}

	remotePath := fs.GetRemotePath(name)
	if fs.cacheData {
		localPath := fs.GetLocalPath(remotePath)
		dir := filepath.Dir(localPath)
		err := os.MkdirAll(dir, os.FileMode(fs.dirMode))
		if err != nil {
			fs.debug("error: Create(%s) could not make cache parent directory %s: %s", name, dir, err)
			return nil, fuse.ToStatus(err)
		}

		localFile, err := os.OpenFile(localPath, int(flags)|os.O_CREATE, os.FileMode(mode))
		if err != nil {
			fs.debug("error: Create(%s) could not OpenFile(%s): %s", name, localPath, err)
		}

		attr, existed := fs.files[name]
		if !existed {
			mTime := uint64(time.Now().Unix())
			attr = &fuse.Attr{
				Mode:  fuse.S_IFREG | fs.fileMode,
				Size:  uint64(0),
				Mtime: mTime,
				Atime: mTime,
				Ctime: mTime,
			}
			fs.files[name] = attr
		}
		fs.createdFiles[name] = true

		return NewCachedWriteFile(nodefs.NewLoopbackFile(localFile), attr), fuse.ToStatus(err)
	}
	return nil, fuse.ENOSYS
}
