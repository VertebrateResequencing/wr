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
	"github.com/hanwen/go-fuse/fuse/pathfs"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// fileDetails checks the file is known and returns its attributes and the
// remote the file came from. If not known, returns ENOENT (which should never
// happen).
func (fs *MinFys) fileDetails(name string, shouldBeWritable bool) (attr *fuse.Attr, r *remote, status fuse.Status) {
	attr, exists := fs.files[name]
	if !exists {
		return nil, nil, fuse.ENOENT
	}
	r = fs.fileToRemote[name]
	status = fuse.OK

	if shouldBeWritable && !r.write {
		status = fuse.EPERM
	}

	return
}

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

func (fs *MinFys) OnMount(nodeFs *pathfs.PathNodeFs) {
	// we need to establish that the root directory is a directory; the next
	// attempt by the user to get it's contents will actually do the remote call
	// to get the directory entries
	fs.dirs[""] = fs.remotes
}

// GetAttr finds out about a given object, returning information from a
// permanent cache if possible. context is not currently used.
func (fs *MinFys) GetAttr(name string, context *fuse.Context) (attr *fuse.Attr, status fuse.Status) {
	if _, isDir := fs.dirs[name]; isDir {
		attr = fs.dirAttr
		status = fuse.OK
		return
	}

	var cached bool
	if attr, cached = fs.files[name]; cached {
		status = fuse.OK
		return
	}

	// sequentially check if name is a file or directory in each of our remotes.
	// We take the remotes in turn instead of simultaneously, because if its a
	// file, we want to only consider the first user supplied target to have
	// that file, even if multiple of them do.
	// Within a remote, checking simultaneously doesn't really help since the
	// remote system may queue the requests serially anyway, and it's better to
	// try and minimise requests. We'll use a simple heuristic that if the name
	// contains a '.', it's more likely to be a file.
	isDir := false
	for _, r := range fs.remotes {
		if strings.Contains(name, ".") {
			attr, status = fs.maybeFile(r, name)
			if status != fuse.OK {
				attr, status = fs.maybeDir(r, name)
			} else {
				// it's a file, stop checking remotes
				break
			}
		} else {
			attr, status = fs.maybeDir(r, name)
			if status != fuse.OK {
				attr, status = fs.maybeFile(r, name)
				if status == fuse.OK {
					// it's a file, stop checking remotes
					break
				}
			}
		}

		if status == fuse.OK {
			// it's a directory, but we'll keep checking the other remotes in
			// case they also have this directory, in which case we'll combine
			// their contents
			isDir = true
		}
	}

	if isDir {
		// we may have found the dir in one remote, but then not found it in the
		// next, so we need to restore
		attr = fs.dirAttr
		status = fuse.OK
	}

	return
}

// maybeDir simply calls openDir() and returns the directory attributes if
// 'name' was actually a directory.
func (fs *MinFys) maybeDir(r *remote, name string) (attr *fuse.Attr, status fuse.Status) {
	status = fs.openDir(r, name)
	if status == fuse.OK {
		attr = fs.dirAttr
	}
	return
}

// maybeFile calls openDir() on the putative file's parent directory, then
// checks to see if that resulted in a file named 'name' being cached.
func (fs *MinFys) maybeFile(r *remote, name string) (attr *fuse.Attr, status fuse.Status) {
	// rather than call StatObject on name to see if its a file, it's more
	// efficient to try and open it's parent directory and see if that resulted
	// in us caching the file as one of the dir's entries
	parent := filepath.Dir(name)
	if parent == "/" {
		parent = ""
	}
	if _, cached := fs.dirContents[parent]; !cached {
		fs.openDir(r, parent)
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
func (fs *MinFys) OpenDir(name string, context *fuse.Context) ([]fuse.DirEntry, fuse.Status) {
	remotes, exists := fs.dirs[name]
	if !exists {
		return nil, fuse.ENOENT
	}

	entries, cached := fs.dirContents[name]
	if cached {
		return entries, fuse.OK
	}

	// openDir in all remotes that have this dir, then return the combined dir
	// contents from the cache
	for _, r := range remotes {
		fs.openDir(r, name)
	}

	entries, cached = fs.dirContents[name]
	if cached {
		return entries, fuse.OK
	}
	return nil, fuse.ENOENT
}

// openDir gets the contents of the given name, treating it as a directory,
// caching the attributes of its contents.
func (fs *MinFys) openDir(r *remote, name string) (status fuse.Status) {
	remotePath := r.getRemotePath(name)
	if remotePath != "" {
		remotePath += "/"
	}

	objects, worked := r.findObjects(remotePath)
	if !worked {
		return fuse.EIO
	}

	var isDir bool
	for _, object := range objects {
		if object.Key == name {
			continue
		}
		isDir = true

		d := fuse.DirEntry{
			Name: object.Key[len(remotePath):],
		}
		if d.Name == "" {
			continue
		}

		fs.mutex.Lock()
		if strings.HasSuffix(d.Name, "/") {
			d.Mode = uint32(fuse.S_IFDIR)
			d.Name = d.Name[0 : len(d.Name)-1]
			thisPath := filepath.Join(name, d.Name)
			fs.dirs[thisPath] = append(fs.dirs[thisPath], r)
		} else {
			d.Mode = uint32(fuse.S_IFREG)
			thisPath := filepath.Join(name, d.Name)
			mTime := uint64(object.LastModified.Unix())
			attr := &fuse.Attr{
				Mode:  fuse.S_IFREG | uint32(fileMode),
				Size:  uint64(object.Size),
				Mtime: mTime,
				Atime: mTime,
				Ctime: mTime,
			}
			fs.files[thisPath] = attr
			fs.fileToRemote[thisPath] = r
		}
		fs.dirContents[name] = append(fs.dirContents[name], d)
		fs.mutex.Unlock()

		// for efficiency, instead of breaking here, we'll keep looping and
		// cache all the dir contents; this does mean we'll never see new
		// entries for this dir in the future
	}
	status = fuse.OK

	if isDir {
		fs.mutex.Lock()
		fs.dirs[name] = append(fs.dirs[name], r)
		if _, exists := fs.dirContents[name]; !exists {
			// empty dir, we must create an entry in this map
			fs.dirContents[name] = []fuse.DirEntry{}
		}
		fs.mutex.Unlock()
	} else {
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
	attr, r, status := fs.fileDetails(name, false)
	if status != fuse.OK {
		return
	}

	if r.cacheData {
		file, status = fs.openCached(r, name, flags, context, attr)
	} else {
		file = NewS3File(r, r.getRemotePath(name), attr.Size)
	}

	if !r.write || (int(flags)&os.O_WRONLY == 0 && int(flags)&os.O_RDWR == 0) {
		file = nodefs.NewReadOnlyFile(file)
	}

	return
}

// openCached downloads the remotePath to the configured CacheDir, then all
// subsequent read/write operations are deferred to the *os.File for that local
// file. Any writes are currently lost because they're not uploaded! NB: there
// is currently no locking, so this should only be called by one process at a
// time (for the same configured CacheDir).
func (fs *MinFys) openCached(r *remote, name string, flags uint32, context *fuse.Context, attr *fuse.Attr) (nodefs.File, fuse.Status) {
	remotePath := r.getRemotePath(name)

	// *** will need to do locking to avoid downloading the same file multiple
	// times simultaneously, including by a completely separate process using
	// the same cache dir

	// check cache file doesn't already exist
	var download bool
	localPath := r.getLocalPath(remotePath)
	localStats, err := os.Stat(localPath)
	if err != nil { // don't bother checking os.IsNotExist(err); we'll download based on any error
		os.Remove(localPath)
		download = true
	} else {
		// check the file is the right size
		if localStats.Size() != int64(attr.Size) {
			fs.debug("warning: openCached(%s) cached sizes differ: %d local vs %d remote", name, localStats.Size(), attr.Size)
			os.Remove(localPath)
			download = true
		}
	}

	if download {
		// download whole remote object, with automatic retries
		if !r.downloadFile(remotePath, localPath) {
			return nil, fuse.EIO
		}

		// check size ok
		localStats, err := os.Stat(localPath)
		if err != nil {
			fs.debug("error: FGetObject(%s, %s) call worked, but the downloaded file had error: %s", r.bucket, remotePath, err)
			os.Remove(localPath)
			return nil, fuse.ToStatus(err)
		} else {
			if localStats.Size() != int64(attr.Size) {
				os.Remove(localPath)
				fs.debug("error: FGetObject(%s, %s) call for openCached worked, but download/remote sizes differ: %d vs %d", r.bucket, remotePath, localStats.Size(), attr.Size)
				return nil, fuse.EIO
			}
		}
	}

	// open for appending, treating it like we created the file
	if int(flags)&os.O_APPEND != 0 {
		return fs.Create(name, flags, uint32(fileMode), context)
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
	_, _, status := fs.fileDetails(name, true)
	return status
}

// Chown is ignored.
func (fs *MinFys) Chown(name string, uid uint32, gid uint32, context *fuse.Context) fuse.Status {
	_, _, status := fs.fileDetails(name, true)
	return status
}

// Truncate truncates any local cached copy of the file. Only currently
// implemented for when configured with CacheData; the results of the Truncate
// are only uploaded at Unmount() time. If offset is > size of file, does
// nothing and returns OK.
func (fs *MinFys) Truncate(name string, offset uint64, context *fuse.Context) fuse.Status {
	attr, r, status := fs.fileDetails(name, true)
	if status != fuse.OK {
		return status
	}

	if offset > attr.Size {
		return fuse.OK
	}

	remotePath := r.getRemotePath(name)
	if r.cacheData {
		localPath := r.getLocalPath(remotePath)

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
			err := os.MkdirAll(dir, os.FileMode(dirMode))
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
				object, worked := r.getObject(remotePath, 0)
				if !worked {
					return fuse.EIO
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

// Mkdir is ignored ***for now...
func (fs *MinFys) Mkdir(name string, mode uint32, context *fuse.Context) fuse.Status {
	if fs.writeRemote == nil {
		return fuse.EPERM
	}
	return fuse.OK
}

// Unlink deletes a file from the remote S3 bucket, as well as any locally
// cached copy.
func (fs *MinFys) Unlink(name string, context *fuse.Context) fuse.Status {
	_, r, status := fs.fileDetails(name, true)
	if status != fuse.OK {
		return status
	}

	remotePath := r.getRemotePath(name)
	if r.cacheData {
		syscall.Unlink(r.getLocalPath(remotePath))
	}

	worked := r.deleteFile(remotePath)
	if !worked {
		return fuse.EIO
	}

	delete(fs.files, name)
	delete(fs.fileToRemote, name)
	delete(fs.createdFiles, name)

	// remove the directory entry as well
	dir := filepath.Dir(name)
	if dir == "." {
		dir = ""
	}
	baseName := filepath.Base(name)
	fs.mutex.Lock()
	defer fs.mutex.Unlock()
	if dentries, exists := fs.dirContents[dir]; exists {
		for i, entry := range dentries {
			if entry.Name == baseName {
				// delete without preserving order and avoiding memory leak
				dentries[i] = dentries[len(dentries)-1]
				dentries[len(dentries)-1] = fuse.DirEntry{}
				dentries = dentries[:len(dentries)-1]
				fs.dirContents[dir] = dentries
				break
			}
		}
	}

	return fuse.OK
}

// Rmdir is ignored.
func (fs *MinFys) Rmdir(name string, context *fuse.Context) fuse.Status {
	if fs.writeRemote == nil {
		return fuse.EPERM
	}
	return fuse.OK
}

// Rename isn't implemented yet.
func (fs *MinFys) Rename(oldPath string, newPath string, context *fuse.Context) fuse.Status {
	_, _, status := fs.fileDetails(oldPath, true)
	if status != fuse.OK {
		return status
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
	r := fs.writeRemote
	if r == nil {
		return nil, fuse.EPERM
	}

	remotePath := r.getRemotePath(name)
	if r.cacheData {
		localPath := r.getLocalPath(remotePath)
		dir := filepath.Dir(localPath)
		err := os.MkdirAll(dir, os.FileMode(dirMode))
		if err != nil {
			fs.debug("error: Create(%s) could not make cache parent directory %s: %s", name, dir, err)
			return nil, fuse.ToStatus(err)
		}

		localFile, err := os.OpenFile(localPath, int(flags)|os.O_CREATE, os.FileMode(mode))
		if err != nil {
			fs.debug("error: Create(%s) could not OpenFile(%s): %s", name, localPath, err)
		}

		fs.mutex.Lock()
		attr, existed := fs.files[name]
		if !existed {
			mTime := uint64(time.Now().Unix())
			attr = &fuse.Attr{
				Mode:  fuse.S_IFREG | uint32(fileMode),
				Size:  uint64(0),
				Mtime: mTime,
				Atime: mTime,
				Ctime: mTime,
			}
			fs.files[name] = attr
			fs.fileToRemote[name] = r

			// add to our directory entries for this file's dir
			d := fuse.DirEntry{
				Name: filepath.Base(name),
				Mode: uint32(fuse.S_IFREG),
			}
			dir := filepath.Dir(name)
			if dir == "." {
				dir = ""
			}
			if _, exists := fs.dirContents[dir]; !exists {
				// we must populate the contents of dir first
				fs.mutex.Unlock()
				fs.OpenDir(dir, &fuse.Context{})
				fs.mutex.Lock()
			}
			fs.dirContents[dir] = append(fs.dirContents[dir], d)
		}
		fs.createdFiles[name] = true
		fs.mutex.Unlock()

		return NewCachedWriteFile(nodefs.NewLoopbackFile(localFile), attr), fuse.ToStatus(err)
	}
	return nil, fuse.ENOSYS
}
