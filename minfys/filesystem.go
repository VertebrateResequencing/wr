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

// OnMount prepares MinFys for use once Mount() has been called.
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

	// rather than call StatObject on name to see if its a file, it's more
	// efficient to try and open it's parent directory and see if that resulted
	// in us caching name as one of the parent's contents
	parent := filepath.Dir(name)
	if parent == "/" || parent == "." {
		parent = ""
	}
	if _, cached := fs.dirContents[parent]; !cached {
		fs.OpenDir(parent, context)
		if _, isDir := fs.dirs[name]; isDir {
			attr = fs.dirAttr
			status = fuse.OK
			return
		}

		if attr, cached = fs.files[name]; cached {
			status = fuse.OK
			return
		}
	}
	return nil, fuse.ENOENT
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

	objects, status := r.findObjects(remotePath)
	if status != fuse.OK {
		return
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
	checkWritable := false
	if int(flags)&os.O_WRONLY != 0 || int(flags)&os.O_RDWR != 0 || int(flags)&os.O_APPEND != 0 || int(flags)&os.O_CREATE != 0 || int(flags)&os.O_TRUNC != 0 {
		checkWritable = true
	}
	attr, r, status := fs.fileDetails(name, checkWritable)
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

// openCached defers all subsequent read/write operations to a CachedFile for
// that local file. NB: there is currently no locking, so this should only be
// called by one process at a time (for the same configured CacheDir).
func (fs *MinFys) openCached(r *remote, name string, flags uint32, context *fuse.Context, attr *fuse.Attr) (nodefs.File, fuse.Status) {
	remotePath := r.getRemotePath(name)

	// *** will need to do locking to avoid downloading the same file multiple
	// times simultaneously, including by a completely separate process using
	// the same cache dir

	localPath := r.getLocalPath(remotePath)
	localStats, err := os.Stat(localPath)
	var create bool
	if err != nil {
		os.Remove(localPath)
		create = true
	} else {
		// check the file is the right size
		if localStats.Size() != int64(attr.Size) {
			fs.debug("warning: openCached(%s) cached sizes differ: %d local vs %d remote", name, localStats.Size(), attr.Size)
			os.Remove(localPath)
			create = true
		}
	}

	if create {
		parent := filepath.Dir(localPath)
		err = os.MkdirAll(parent, dirMode)
		if err != nil {
			return nil, fuse.ToStatus(err)
		}

		if int(flags)&os.O_APPEND != 0 {
			// download whole remote object to disk before user appends anything
			// to it
			if status := r.downloadFile(remotePath, localPath); status != fuse.OK {
				return nil, status
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
			fs.downloaded[localPath] = true
		} else {
			// this is our first time opening this remote file, create a sparse
			// file that Read() operations will cache in to
			f, err := os.Create(localPath)
			if err != nil {
				return nil, fuse.ToStatus(err)
			}
			if err := f.Truncate(int64(attr.Size)); err != nil {
				return nil, fuse.ToStatus(err)
			}
			f.Close()
		}
	}

	// if the flags suggest any kind of write-ability, treat it like we created
	// the file
	if int(flags)&os.O_WRONLY != 0 || int(flags)&os.O_RDWR != 0 || int(flags)&os.O_APPEND != 0 || int(flags)&os.O_CREATE != 0 || int(flags)&os.O_TRUNC != 0 {
		return fs.Create(name, flags, uint32(fileMode), context)
	}

	return NewCachedFile(r, remotePath, localPath, attr, flags), fuse.OK
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

// SetXAttr is ignored.
func (fs *MinFys) SetXAttr(name string, attr string, data []byte, flags int, context *fuse.Context) fuse.Status {
	_, _, status := fs.fileDetails(name, true)
	return status
}

// Utimens only functions when configured with CacheData and the file is already
// in the cache; otherwise ignored. This only gets called by direct operations
// like os.Chtimes() (that don't first Open()/Create() the file). context is not
// currently used.
func (fs *MinFys) Utimens(name string, Atime *time.Time, Mtime *time.Time, context *fuse.Context) (status fuse.Status) {
	attr, r, status := fs.fileDetails(name, true)
	if status != fuse.OK || !r.cacheData {
		return
	}

	localPath := r.getLocalPath(r.getRemotePath(name))
	if _, err := os.Stat(localPath); err == nil {
		err = os.Chtimes(localPath, *Atime, *Mtime)
		if err == nil {
			attr.Atime = uint64(Atime.Unix())
			attr.Mtime = uint64(Mtime.Unix())
		}
		status = fuse.ToStatus(err)
	}

	return
}

// Truncate truncates any local cached copy of the file. Only currently
// implemented for when configured with CacheData; the results of the Truncate
// are only uploaded at Unmount() time. If offset is > size of file, does
// nothing and returns OK. context is not currently used.
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
				object, status := r.getObject(remotePath, 0)
				if status != fuse.OK {
					return status
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

// Mkdir for a directory that doesn't exist yet only works whilst mounted in
// CacheData mode. neither mode nor context are currently used.
func (fs *MinFys) Mkdir(name string, mode uint32, context *fuse.Context) fuse.Status {
	if fs.writeRemote == nil {
		return fuse.EPERM
	}

	if _, isDir := fs.dirs[name]; isDir {
		return fuse.OK
	}

	// it's parent directory must already exist
	parent := filepath.Dir(name)
	if parent == "." {
		parent = ""
	}
	if _, exists := fs.dirs[parent]; !exists {
		return fuse.ENOENT
	}

	remotePath := fs.writeRemote.getRemotePath(name)
	if fs.writeRemote.cacheData {
		localPath := fs.writeRemote.getLocalPath(remotePath)

		// make all the parent directories. *** we use our dirMode constant here
		// instead of the supplied mode because of strange permission problems
		// using the latter, and because it doesn't matter what permissions the
		// user wants for the dir - this is for a user-only cache
		var err error
		if err = os.MkdirAll(filepath.Dir(localPath), os.FileMode(dirMode)); err == nil {
			// make the desired directory
			if err = os.Mkdir(localPath, os.FileMode(dirMode)); err == nil {
				fs.mutex.Lock()
				fs.dirs[name] = append(fs.dirs[name], fs.writeRemote)
				if _, exists := fs.dirContents[name]; !exists {
					fs.dirContents[name] = []fuse.DirEntry{}
				}
				fs.createdDirs[name] = true
				fs.mutex.Unlock()
				fs.addNewEntryToItsDir(name, fuse.S_IFDIR)
			}
		}
		return fuse.ToStatus(err)
	}
	return fuse.ENOSYS
}

// Rmdir only works for directories you have created whilst mounted in
// CacheData mode. context is not currently used.
func (fs *MinFys) Rmdir(name string, context *fuse.Context) fuse.Status {
	if fs.writeRemote == nil {
		return fuse.EPERM
	}

	if _, isDir := fs.dirs[name]; !isDir {
		return fuse.ENOENT
	} else if _, created := fs.createdDirs[name]; !created {
		return fuse.ENOSYS
	}

	remotePath := fs.writeRemote.getRemotePath(name)
	if fs.writeRemote.cacheData {
		err := syscall.Rmdir(fs.writeRemote.getLocalPath(remotePath))
		if err == nil {
			fs.mutex.Lock()
			delete(fs.dirs, name)
			delete(fs.createdDirs, name)
			delete(fs.dirContents, name)
			fs.mutex.Unlock()
			fs.rmEntryFromItsDir(name)
		}
		return fuse.ToStatus(err)
	}
	return fuse.ENOSYS
}

// Rename only works where oldPath is found in the writeable remote. For files,
// first remotely copies oldPath to newPath (ignoring any local changes to
// oldPath), renames any local cached (and possibly modified) copy of oldPath to
// newPath, and finally deletes the remote oldPath; if oldPath had been
// modified, its changes will only be uploaded to newPath at Unmount() time. For
// directories, is only capable of renaming directories you have created whilst
// mounted. context is not currently used.
func (fs *MinFys) Rename(oldPath string, newPath string, context *fuse.Context) fuse.Status {
	if fs.writeRemote == nil {
		return fuse.EPERM
	}

	var isDir bool
	if _, isDir = fs.dirs[oldPath]; !isDir {
		if _, isFile := fs.fileToRemote[oldPath]; !isFile {
			return fuse.ENOENT
		}
	} else if _, created := fs.createdDirs[oldPath]; !created {
		return fuse.ENOSYS
	} else {
		// the directory's new parent dir must exist
		parent := filepath.Dir(newPath)
		if parent == "." {
			parent = ""
		}
		if _, exists := fs.dirs[parent]; !exists {
			return fuse.ENOENT
		}
	}

	remotePathOld := fs.writeRemote.getRemotePath(oldPath)
	remotePathNew := fs.writeRemote.getRemotePath(newPath)
	if isDir {
		if fs.writeRemote.cacheData {
			// first create the newPaths's cached parent dir
			localPathNew := fs.writeRemote.getLocalPath(remotePathNew)
			var err error
			if err = os.MkdirAll(filepath.Dir(localPathNew), os.FileMode(dirMode)); err == nil {
				// now try and rename the cached dir
				if err = os.Rename(fs.writeRemote.getLocalPath(remotePathOld), localPathNew); err == nil {
					// update our knowledge of what dirs we have
					fs.mutex.Lock()
					fs.dirs[newPath] = fs.dirs[oldPath]
					fs.dirContents[newPath] = fs.dirContents[oldPath]
					fs.createdDirs[newPath] = true
					delete(fs.dirs, oldPath)
					delete(fs.createdDirs, oldPath)
					delete(fs.dirContents, oldPath)
					fs.mutex.Unlock()
					fs.rmEntryFromItsDir(oldPath)
					fs.addNewEntryToItsDir(newPath, fuse.S_IFDIR)
				}
			}
			return fuse.ToStatus(err)
		}
	} else {
		// first trigger a remote copy of oldPath to newPath
		status := fs.writeRemote.copyObject(remotePathOld, remotePathNew)
		if status != fuse.OK {
			return status
		}

		if fs.writeRemote.cacheData {
			// if we've cached oldPath, move to new cached file
			os.Rename(fs.writeRemote.getLocalPath(remotePathOld), fs.writeRemote.getLocalPath(remotePathNew))
			fs.mutex.Lock()
			fs.downloaded[fs.writeRemote.getLocalPath(remotePathNew)] = fs.downloaded[fs.writeRemote.getLocalPath(remotePathOld)]
			fs.mutex.Unlock()
		}

		// cache the existence of the new file
		fs.mutex.Lock()
		fs.files[newPath] = fs.files[oldPath]
		fs.fileToRemote[newPath] = fs.fileToRemote[oldPath]
		if _, created := fs.createdFiles[oldPath]; created {
			fs.createdFiles[newPath] = true
			delete(fs.createdFiles, oldPath)
		}
		fs.mutex.Unlock()
		fs.addNewEntryToItsDir(newPath, fuse.S_IFREG)

		// finally unlink oldPath remotely
		fs.Unlink(oldPath, context)

		return fuse.OK
	}
	return fuse.ENOSYS
}

// Unlink deletes a file from the remote S3 bucket, as well as any locally
// cached copy. context is not currently used.
func (fs *MinFys) Unlink(name string, context *fuse.Context) fuse.Status {
	_, r, status := fs.fileDetails(name, true)
	if status != fuse.OK {
		return status
	}

	remotePath := r.getRemotePath(name)
	if r.cacheData {
		syscall.Unlink(r.getLocalPath(remotePath))
	}

	status = r.deleteFile(remotePath)
	if status != fuse.OK {
		return status
	}

	fs.mutex.Lock()
	delete(fs.files, name)
	delete(fs.fileToRemote, name)
	delete(fs.createdFiles, name)
	delete(fs.downloaded, name)
	fs.mutex.Unlock()

	// remove the directory entry as well
	fs.rmEntryFromItsDir(name)

	return fuse.OK
}

// Access is ignored.
func (fs *MinFys) Access(name string, mode uint32, context *fuse.Context) fuse.Status {
	return fuse.OK
}

// Create creates a new file. mode and context are not currently used. Only
// currently implemented for when configured with CacheData; the contents of the
// created file are only uploaded at Unmount() time.
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

		fs.mutex.Lock()
		attr, existed := fs.files[name]
		mTime := uint64(time.Now().Unix())
		if !existed {
			// add to our directory entries for this file's dir
			fs.mutex.Unlock()
			fs.addNewEntryToItsDir(name, fuse.S_IFREG)
			fs.mutex.Lock()

			attr = &fuse.Attr{
				Mode:  fuse.S_IFREG | uint32(fileMode),
				Size:  uint64(0),
				Mtime: mTime,
				Atime: mTime,
				Ctime: mTime,
			}
			fs.files[name] = attr
			fs.fileToRemote[name] = r
		} else {
			attr.Mtime = mTime
			attr.Atime = mTime
		}
		fs.createdFiles[name] = true
		fs.mutex.Unlock()

		return NewCachedFile(r, remotePath, localPath, attr, uint32(int(flags)|os.O_CREATE)), fuse.ToStatus(err)
	}
	return nil, fuse.ENOSYS
}

// addNewEntryToItsDir adds a DirEntry for the file/dir named name to that
// object's containing directory entries. mode should be fuse.S_IFREG or
// fuse.S_IFDIR
func (fs *MinFys) addNewEntryToItsDir(name string, mode int) {
	d := fuse.DirEntry{
		Name: filepath.Base(name),
		Mode: uint32(mode),
	}
	parent := filepath.Dir(name)
	if parent == "." {
		parent = ""
	}
	if _, exists := fs.dirContents[parent]; !exists {
		// we must populate the contents of parent first
		fs.OpenDir(parent, &fuse.Context{})
	}
	fs.mutex.Lock()
	fs.dirContents[parent] = append(fs.dirContents[parent], d)
	fs.mutex.Unlock()
}

// rmEntryFromItsDir removes a DirEntry for the file/dir named name from that
// object's containing directory entries.
func (fs *MinFys) rmEntryFromItsDir(name string) {
	parent := filepath.Dir(name)
	if parent == "." {
		parent = ""
	}
	baseName := filepath.Base(name)
	fs.mutex.Lock()
	defer fs.mutex.Unlock()
	if dentries, exists := fs.dirContents[parent]; exists {
		for i, entry := range dentries {
			if entry.Name == baseName {
				// delete without preserving order and avoiding memory leak
				dentries[i] = dentries[len(dentries)-1]
				dentries[len(dentries)-1] = fuse.DirEntry{}
				dentries = dentries[:len(dentries)-1]
				fs.dirContents[parent] = dentries
				break
			}
		}
	}
}
