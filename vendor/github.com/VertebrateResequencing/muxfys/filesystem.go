// Copyright © 2017, 2018 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
// The StatFs() code in this file is based on code in
// https://github.com/kahing/goofys Copyright 2015-2017 Ka-Hing Cheung,
// licensed under the Apache License, Version 2.0 (the "License"), stating:
// "You may not use this file except in compliance with the License. You may
// obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0"
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

// This file implements pathfs.FileSystem methods.

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/alexflint/go-filemutex"
	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/fuse/nodefs"
	"github.com/hanwen/go-fuse/fuse/pathfs"
)

const (
	blockSize   = uint64(4096)
	totalBlocks = uint64(274877906944) // 1PB / blockSize
	inodes      = uint64(1000000000)
	ioSize      = uint32(1048576) // 1MB
)

// fileDetails checks the file is known and returns its attributes and the
// remote the file came from. If not known, returns ENOENT (which should never
// happen).
func (fs *MuxFys) fileDetails(name string, shouldBeWritable bool) (*fuse.Attr, *remote, fuse.Status) {
	fs.mapMutex.RLock()
	defer fs.mapMutex.RUnlock()
	attr, exists := fs.files[name]
	if !exists {
		return nil, nil, fuse.ENOENT
	}

	r := fs.fileToRemote[name]
	status := fuse.OK
	if shouldBeWritable && !r.write {
		status = fuse.EPERM
	}

	return attr, r, status
}

// StatFs returns a constant (faked) set of details describing a very large
// file system.
func (fs *MuxFys) StatFs(name string) *fuse.StatfsOut {
	return &fuse.StatfsOut{
		Blocks: blockSize,
		Bfree:  totalBlocks,
		Bavail: totalBlocks,
		Files:  inodes,
		Ffree:  inodes,
		Bsize:  ioSize,
		// NameLen uint32
		// Frsize  uint32
		// Padding uint32
		// Spare   [6]uint32
	}
}

// OnMount prepares MuxFys for use once Mount() has been called.
func (fs *MuxFys) OnMount(nodeFs *pathfs.PathNodeFs) {
	fs.mapMutex.Lock()
	defer fs.mapMutex.Unlock()
	// we need to establish that the root directory is a directory; the next
	// attempt by the user to get it's contents will actually do the remote call
	// to get the directory entries
	fs.dirs[""] = fs.remotes
}

// GetAttr finds out about a given object, returning information from a
// permanent cache if possible. context is not currently used.
func (fs *MuxFys) GetAttr(name string, context *fuse.Context) (*fuse.Attr, fuse.Status) {
	fs.mapMutex.Lock()
	defer fs.mapMutex.Unlock()

	if _, isDir := fs.dirs[name]; isDir {
		return fs.dirAttr, fuse.OK
	}

	if attr, cached := fs.files[name]; cached {
		return attr, fuse.OK
	}

	// rather than call StatObject on name to see if its a file, it's more
	// efficient to try and open it's parent directory and see if that resulted
	// in us caching name as one of the parent's contents
	parent := filepath.Dir(name)
	if parent == "/" || parent == "." {
		parent = ""
	}
	if _, cached := fs.dirContents[parent]; !cached {
		// we must populate the contents of parent first, doing the essential
		// part of OpenDir()
		if remotes, exists := fs.dirs[parent]; exists {
			for _, r := range remotes {
				status := fs.openDir(r, parent)
				if status != fuse.OK {
					fs.Warn("GetAttr openDir failed", "path", parent, "status", status)
				}
			}
		}

		if _, isDir := fs.dirs[name]; isDir {
			return fs.dirAttr, fuse.OK
		}

		if attr, cached := fs.files[name]; cached {
			return attr, fuse.OK
		}
	}
	return nil, fuse.ENOENT
}

// OpenDir gets the contents of the given directory for eg. `ls` purposes. It
// also caches the attributes of all the files within. context is not currently
// used.
func (fs *MuxFys) OpenDir(name string, context *fuse.Context) ([]fuse.DirEntry, fuse.Status) {
	fs.mapMutex.Lock()
	defer fs.mapMutex.Unlock()

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
		status := fs.openDir(r, name)
		if status != fuse.OK {
			fs.Warn("GetAttr openDir failed", "path", name, "status", status)
		}
	}

	entries, cached = fs.dirContents[name]
	if cached {
		return entries, fuse.OK
	}
	return nil, fuse.ENOENT
}

// openDir gets the contents of the given name, treating it as a directory,
// caching the attributes of its contents. Must be called while you have the
// mapMutex Locked.
func (fs *MuxFys) openDir(r *remote, name string) fuse.Status {
	remotePath := r.getRemotePath(name)
	if remotePath != "" {
		remotePath += "/"
	}

	objects, status := r.findObjects(remotePath)

	if status != fuse.OK || len(objects) == 0 {
		if name == "" {
			// allow the root to be a non-existent directory
			fs.dirs[name] = append(fs.dirs[name], r)
			if _, exists := fs.dirContents[name]; !exists {
				fs.dirContents[name] = []fuse.DirEntry{}
			}
			return fuse.OK
		} else if status == fuse.OK {
			return fuse.ENOENT
		}
		return status
	}

	var isDir bool
	for _, object := range objects {
		if object.Name == name {
			continue
		}
		isDir = true

		d := fuse.DirEntry{
			Name: object.Name[len(remotePath):],
		}
		if d.Name == "" {
			continue
		}

		if strings.HasSuffix(d.Name, "/") {
			d.Mode = uint32(fuse.S_IFDIR)
			d.Name = d.Name[0 : len(d.Name)-1]
			thisPath := filepath.Join(name, d.Name)
			fs.dirs[thisPath] = append(fs.dirs[thisPath], r)
		} else {
			d.Mode = uint32(fuse.S_IFREG)
			thisPath := filepath.Join(name, d.Name)
			mTime := uint64(object.MTime.Unix())
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

		// for efficiency, instead of breaking here, we'll keep looping and
		// cache all the dir contents; this does mean we'll never see externally
		// added new entries for this dir in the future
	}

	if !isDir {
		return fuse.ENOENT
	}

	fs.dirs[name] = append(fs.dirs[name], r)
	if _, exists := fs.dirContents[name]; !exists {
		// empty dir, we must create an entry in this map
		fs.dirContents[name] = []fuse.DirEntry{}
	}
	return fuse.OK
}

// Open is what is called when any request to read a file is made. The file must
// already have been stat'ed (eg. with a GetAttr() call), or we report the file
// doesn't exist. context is not currently used. If CacheData has been
// configured, we defer to openCached(). Otherwise the real implementation is in
// remoteFile.
func (fs *MuxFys) Open(name string, flags uint32, context *fuse.Context) (nodefs.File, fuse.Status) {
	checkWritable := false
	if int(flags)&os.O_WRONLY != 0 || int(flags)&os.O_RDWR != 0 || int(flags)&os.O_APPEND != 0 || int(flags)&os.O_CREATE != 0 || int(flags)&os.O_TRUNC != 0 {
		checkWritable = true
	}
	attr, r, status := fs.fileDetails(name, checkWritable)
	var file nodefs.File
	if status != fuse.OK {
		return file, status
	}

	if r.cacheData {
		file, status = fs.openCached(r, name, flags, context, attr, checkWritable)
	} else {
		file = newRemoteFile(r, r.getRemotePath(name), attr, false, fs.Logger)
	}

	if !r.write || (int(flags)&os.O_WRONLY == 0 && int(flags)&os.O_RDWR == 0) {
		file = nodefs.NewReadOnlyFile(file)
	}

	return file, status
}

// openCached defers all subsequent read/write operations to a CachedFile for
// that local file.
func (fs *MuxFys) openCached(r *remote, name string, flags uint32, context *fuse.Context, attr *fuse.Attr, writeMode bool) (nodefs.File, fuse.Status) {
	remotePath := r.getRemotePath(name)
	localPath := r.getLocalPath(remotePath)

	fmutex, err := fs.getFileMutex(localPath)
	if err != nil {
		return nil, fuse.EIO
	}
	err = fmutex.Lock()
	if err != nil {
		fs.Error("openCached file mutex lock failed", "err", err)
	}

	localStats, err := os.Stat(localPath)
	var create bool
	if err != nil {
		err = os.Remove(localPath)
		if err != nil && !os.IsNotExist(err) {
			fs.Warn("openCached remove cache file failed", "path", localPath, "err", err)
		}
		create = true
	} else if !writeMode {
		// check the file is the right size
		if localStats.Size() != int64(attr.Size) {
			r.Warn("Cached size differs", "path", name, "localSize", localStats.Size(), "remoteSize", attr.Size)
			err = os.Remove(localPath)
			if err != nil {
				fs.Warn("openCached remove cache file failed", "path", localPath, "err", err)
			}
			create = true
			if int(flags)&os.O_WRONLY != 0 || int(flags)&os.O_RDWR != 0 || int(flags)&os.O_APPEND != 0 || int(flags)&os.O_CREATE != 0 || int(flags)&os.O_TRUNC != 0 {
				attr.Size = uint64(0)
			}
		} else if !r.cacheIsTmp {
			// if the file already exists at the correct size, but we have no
			// record of it being cached, assume another process sharing the
			// same permanent cache folder already cached the whole file
			iv := NewInterval(0, localStats.Size())
			ivs := r.Uncached(localPath, iv)
			if len(ivs) > 0 {
				r.Cached(localPath, iv)
			}

			// *** doesn't this break if two different mount processes are
			// trying to read the same file at the same time?... Maybe we'll
			// need to store cached intervals in the lock file after all...
		}
	}

	if create {
		r.CacheDelete(localPath)

		if !r.cacheIsTmp || int(flags)&os.O_APPEND != 0 {
			// download whole remote object to disk before user appends anything
			// to it; if we just append to the sparse file then on upload we
			// lose the contents of the original file. We also do this if we're
			// not deleting our cache, ie. our cache dir was chosen by the user
			// and could be in use simultaneously by other muxfys mounts
			// *** alternatively we could store Invervals in the lock file...
			if status := r.downloadFile(remotePath, localPath); status != fuse.OK {
				logClose(fs.Logger, fmutex, "openCached file mutex")
				return nil, status
			}

			// check size ok
			localStats, errs := os.Stat(localPath)
			if errs != nil {
				r.Error("Downloaded file could not be accessed", "path", localPath, "err", errs)
				errr := os.Remove(localPath)
				if errr != nil {
					fs.Warn("openCached remove cache file failed", "path", localPath, "err", errr)
				}
				logClose(fs.Logger, fmutex, "openCached file mutex")
				return nil, fuse.ToStatus(errs)
			} else if localStats.Size() != int64(attr.Size) {
				r.Error("Downloaded size is wrong", "path", remotePath, "localSize", localStats.Size(), "remoteSize", attr.Size)
				errr := os.Remove(localPath)
				if errr != nil {
					fs.Warn("openCached remove cache file failed", "path", localPath, "err", errr)
				}
				logClose(fs.Logger, fmutex, "openCached file mutex")
				return nil, fuse.EIO
			}
			r.CacheOverride(localPath, NewInterval(0, int64(attr.Size)))
		} else {
			// this is our first time opening this remote file, create a sparse
			// file that Read() operations will cache in to
			f, errc := os.Create(localPath)
			if errc != nil {
				fs.Error("openCached create cached file failed", "path", localPath, "err", errc)
				logClose(fs.Logger, fmutex, "openCached file mutex")
				return nil, fuse.ToStatus(errc)
			}
			if errt := f.Truncate(int64(attr.Size)); errt != nil {
				fs.Error("openCached truncate failed", "path", localPath, "err", errt)
				logClose(fs.Logger, fmutex, "openCached file mutex")
				return nil, fuse.ToStatus(errt)
			}
			logClose(fs.Logger, f, "openCached created file", "path", localPath)
		}
	} else if r.cacheIsTmp && int(flags)&os.O_APPEND != 0 {
		// cache everything in the file we haven't already read by reading the
		// file the way a client would
		iv := Interval{0, int64(attr.Size)}
		unread := r.Uncached(localPath, iv)
		if len(unread) > 0 {
			err = fmutex.Unlock()
			if err != nil {
				fs.Error("openCached file mutex unlock failed", "err", err)
			}
			path := filepath.Join(fs.mountPoint, name)
			reader, err := os.Open(path)
			if err != nil {
				r.Error("Could not open cached file", "path", path, "err", err)
				errl := fmutex.Lock()
				if errl != nil {
					fs.Error("openCached file mutex lock failed", "err", errl)
				}
				logClose(fs.Logger, fmutex, "openCached file mutex")
				return nil, fuse.ToStatus(err)
			}
			for _, uiv := range unread {
				_, errs := reader.Seek(uiv.Start, io.SeekStart)
				if errs != nil {
					r.Error("openCached reader seek failed", "err", errs)
				}
				br := bufio.NewReader(reader)
				b := make([]byte, 1000)
				var read int64
				for read <= uiv.Length() {
					done, rerr := br.Read(b)
					if rerr != nil {
						if rerr != io.EOF {
							err = rerr
						}
						break
					}
					read += int64(done)
				}
				if err != nil {
					r.Error("Could not read file", "path", name, "err", err)
					logClose(fs.Logger, reader, "openCached reader", "path", name)
					err = fmutex.Lock()
					if err != nil {
						fs.Error("openCached file mutex lock failed", "err", err)
					}
					logClose(fs.Logger, fmutex, "openCached file mutex")
					return nil, fuse.EIO
				}
			}
			logClose(fs.Logger, reader, "openCached reader", "path", name)
			err = fmutex.Lock()
			if err != nil {
				fs.Error("openCached file mutex lock failed", "err", err)
			}
		}
	}

	// if the flags suggest any kind of write-ability, treat it like we created
	// the file
	if writeMode {
		return fs.create(name, flags, uint32(fileMode), fmutex)
	}

	logClose(fs.Logger, fmutex, "openCached file mutex")
	return newCachedFile(r, remotePath, localPath, attr, flags, fs.Logger), fuse.OK
}

// Chmod is ignored.
func (fs *MuxFys) Chmod(name string, mode uint32, context *fuse.Context) fuse.Status {
	_, _, status := fs.fileDetails(name, true)
	if status == fuse.ENOENT {
		fs.mapMutex.RLock()
		defer fs.mapMutex.RUnlock()
		if _, exists := fs.dirs[name]; exists {
			return fuse.OK
		}
	}
	return status
}

// Chown is ignored.
func (fs *MuxFys) Chown(name string, uid uint32, gid uint32, context *fuse.Context) fuse.Status {
	_, _, status := fs.fileDetails(name, true)
	if status == fuse.ENOENT {
		fs.mapMutex.RLock()
		defer fs.mapMutex.RUnlock()
		if _, exists := fs.dirs[name]; exists {
			return fuse.OK
		}
	}
	return status
}

// Symlink creates a symbolic link. Only implemented for temporary use when
// configured with CacheData: you can create and use symlinks but they don't get
// uploaded. context is not currently used.
func (fs *MuxFys) Symlink(source string, dest string, context *fuse.Context) (status fuse.Status) {
	if fs.writeRemote == nil || !fs.writeRemote.cacheData {
		return fuse.ENOSYS
	}

	localPathDest := fs.writeRemote.getLocalPath(fs.writeRemote.getRemotePath(dest))
	fmutex, err := fs.getFileMutex(localPathDest)
	if err != nil {
		return fuse.EIO
	}
	err = fmutex.Lock()
	if err != nil {
		fs.Error("Symlink file mutex lock failed", "err", err)
	}
	defer logClose(fs.Logger, fmutex, "Symlink file mutex")

	// symlink from mount point source to cached dest file
	err = os.Symlink(source, localPathDest)
	if err != nil {
		fs.writeRemote.Error("Could not create symlink", "source", source, "dest", localPathDest, "err", err)
		return fuse.ToStatus(err)
	}

	// note the existence of dest without making it uploadable on unmount
	fs.mapMutex.Lock()
	fs.addNewEntryToItsDir(dest, fuse.S_IFLNK)
	mTime := uint64(time.Now().Unix())
	attr := &fuse.Attr{
		Mode:  fuse.S_IFLNK | uint32(fileMode),
		Size:  symlinkSize, // it doesn't matter what the actual size is (which we could get with os.Lstat(localPathDest)), this is just for presentation purposes
		Mtime: mTime,
		Atime: mTime,
		Ctime: mTime,
	}
	fs.files[dest] = attr
	fs.fileToRemote[dest] = fs.writeRemote
	fs.mapMutex.Unlock()

	return fuse.OK
}

// Readlink returns the destination of a symbolic link that was created with
// Symlink(). context is not currently used.
func (fs *MuxFys) Readlink(name string, context *fuse.Context) (string, fuse.Status) {
	_, r, status := fs.fileDetails(name, true)
	if status != fuse.OK {
		return "", status
	}
	localPath := r.getLocalPath(r.getRemotePath(name))
	out, err := os.Readlink(localPath)
	if err != nil {
		fs.Warn("Readlink failed", "path", localPath, "err", err)
	}
	return out, fuse.ToStatus(err)
}

// SetXAttr is ignored.
func (fs *MuxFys) SetXAttr(name string, attr string, data []byte, flags int, context *fuse.Context) fuse.Status {
	_, _, status := fs.fileDetails(name, true)
	if status == fuse.ENOENT {
		fs.mapMutex.RLock()
		defer fs.mapMutex.RUnlock()
		if _, exists := fs.dirs[name]; exists {
			return fuse.OK
		}
	}
	return status
}

// RemoveXAttr is ignored.
func (fs *MuxFys) RemoveXAttr(name string, attr string, context *fuse.Context) fuse.Status {
	_, _, status := fs.fileDetails(name, true)
	if status == fuse.ENOENT {
		fs.mapMutex.RLock()
		defer fs.mapMutex.RUnlock()
		if _, exists := fs.dirs[name]; exists {
			return fuse.OK
		}
	}
	return status
}

// Utimens only functions when configured with CacheData and the file is already
// in the cache; otherwise ignored. This only gets called by direct operations
// like os.Chtimes() (that don't first Open()/Create() the file). context is not
// currently used.
func (fs *MuxFys) Utimens(name string, Atime *time.Time, Mtime *time.Time, context *fuse.Context) fuse.Status {
	attr, r, status := fs.fileDetails(name, true)
	if status == fuse.ENOENT {
		fs.mapMutex.RLock()
		defer fs.mapMutex.RUnlock()
		if _, exists := fs.dirs[name]; exists {
			return fuse.OK
		}
	}
	if status != fuse.OK || !r.cacheData {
		return status
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

	return status
}

// Truncate truncates any local cached copy of the file. Only currently
// implemented for when configured with CacheData; the results of the Truncate
// are only uploaded at Unmount() time. If offset is > size of file, does
// nothing and returns OK. context is not currently used.
func (fs *MuxFys) Truncate(name string, offset uint64, context *fuse.Context) fuse.Status {
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

		fmutex, err := fs.getFileMutex(localPath)
		if err != nil {
			return fuse.EIO
		}
		err = fmutex.Lock()
		if err != nil {
			fs.Error("Truncate file mutex lock failed", "err", err)
			return fuse.EIO
		}
		defer logClose(fs.Logger, fmutex, "Trucate mutex file")

		if _, err := os.Stat(localPath); err == nil {
			// truncate local cached copy
			err = os.Truncate(localPath, int64(offset))
			if err != nil {
				fs.Error("Truncate cached file failed", "path", localPath, "err", err)
				return fuse.ToStatus(err)
			}
			r.CacheTruncate(localPath, int64(offset))
		} else {
			// create a new empty file
			localFile, err := os.Create(localPath)
			if err != nil {
				r.Error("Could not create empty file", "path", localPath, "err", err)
				return fuse.EIO
			}

			if offset == 0 {
				logClose(fs.Logger, localFile, "Trucate local file")
				r.CacheTruncate(localPath, int64(offset))
			} else {
				// download offset bytes of remote file
				object, status := r.getObject(remotePath, 0)
				if status != fuse.OK {
					return status
				}

				written, err := io.CopyN(localFile, object, int64(offset))
				if err != nil || written != int64(offset) {
					msg := "Could not copy bytes"
					if err == nil {
						msg = "Could not copy all bytes"
					}
					r.Error(msg, "size", offset, "source", remotePath, "dest", localPath, "err", err)
					logClose(fs.Logger, localFile, "Trucate local file")
					erru := syscall.Unlink(localPath)
					if erru != nil {
						fs.Error("Truncate cache file unlink failed", "err", erru)
					}
					return fuse.EIO
				}

				logClose(fs.Logger, localFile, "Trucate local file")
				logClose(fs.Logger, object, "Trucate remote object")

				r.CacheOverride(localPath, NewInterval(0, int64(offset)))
			}
		}

		// update attr and claim we created this file
		attr.Size = offset
		attr.Mtime = uint64(time.Now().Unix())
		fs.mapMutex.Lock()
		fs.createdFiles[name] = true
		fs.mapMutex.Unlock()

		return fuse.OK
	}
	return fuse.ENOSYS
}

// Mkdir for a directory that doesn't exist yet. neither mode nor context are
// currently used.
func (fs *MuxFys) Mkdir(name string, mode uint32, context *fuse.Context) fuse.Status {
	if fs.writeRemote == nil {
		return fuse.EPERM
	}

	fs.mapMutex.Lock()
	defer fs.mapMutex.Unlock()

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
	var err error
	if fs.writeRemote.cacheData {
		localPath := fs.writeRemote.getLocalPath(remotePath)

		// make all the parent directories. We use our dirMode constant here
		// instead of the supplied mode because of strange permission problems
		// using the latter, and because it doesn't matter what permissions the
		// user wants for the dir - this is for a user-only cache
		if err = os.MkdirAll(filepath.Dir(localPath), os.FileMode(dirMode)); err == nil {
			// make the desired directory
			err = os.Mkdir(localPath, os.FileMode(dirMode))
		}
		if err != nil {
			fs.Error("Mkdir failed", "path", localPath, "err", err)
			fs.mapMutex.Unlock()
			return fuse.ToStatus(err)
		}
	}

	// we mark its existence internally but don't do anything "physical"
	// to create the dir remotely (applies for cached and uncached modes)
	fs.dirs[name] = append(fs.dirs[name], fs.writeRemote)
	if _, exists := fs.dirContents[name]; !exists {
		fs.dirContents[name] = []fuse.DirEntry{}
	}
	if fs.writeRemote.cacheData {
		fs.createdDirs[name] = true
	}
	fs.addNewEntryToItsDir(name, fuse.S_IFDIR)
	return fuse.OK
}

// Rmdir only works for non-existent or empty dirs. context is not currently
// used.
func (fs *MuxFys) Rmdir(name string, context *fuse.Context) fuse.Status {
	if fs.writeRemote == nil {
		return fuse.EPERM
	}

	fs.mapMutex.Lock()
	defer fs.mapMutex.Unlock()

	if _, isDir := fs.dirs[name]; !isDir {
		return fuse.ENOENT
	} else if contents, exists := fs.dirContents[name]; exists && len(contents) > 0 {
		return fuse.ENOSYS
	}

	remotePath := fs.writeRemote.getRemotePath(name)
	var err error
	if fs.writeRemote.cacheData {
		localPath := fs.writeRemote.getLocalPath(remotePath)
		err = syscall.Rmdir(localPath)
		if err != nil {
			fs.Error("Rmdir failed", "path", localPath, "err", err)
			return fuse.ToStatus(err)
		}

	}

	delete(fs.dirs, name)
	delete(fs.createdDirs, name)
	delete(fs.dirContents, name)
	fs.rmEntryFromItsDir(name)

	return fuse.OK
}

// Rename only works where oldPath is found in the writeable remote. For files,
// first remotely copies oldPath to newPath (ignoring any local changes to
// oldPath), renames any local cached (and possibly modified) copy of oldPath to
// newPath, and finally deletes the remote oldPath; if oldPath had been
// modified, its changes will only be uploaded to newPath at Unmount() time. For
// directories, is only capable of renaming directories you have created whilst
// mounted. context is not currently used.
func (fs *MuxFys) Rename(oldPath string, newPath string, context *fuse.Context) fuse.Status {
	if fs.writeRemote == nil {
		return fuse.EPERM
	}

	fs.mapMutex.Lock()
	defer fs.mapMutex.Unlock()

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

			// *** should we try and lock the old and new directories first?

			var err error
			if err = os.MkdirAll(filepath.Dir(localPathNew), os.FileMode(dirMode)); err == nil {
				// now try and rename the cached dir
				if err = os.Rename(fs.writeRemote.getLocalPath(remotePathOld), localPathNew); err == nil {
					// update our knowledge of what dirs we have
					fs.dirs[newPath] = fs.dirs[oldPath]
					fs.dirContents[newPath] = fs.dirContents[oldPath]
					fs.createdDirs[newPath] = true
					delete(fs.dirs, oldPath)
					delete(fs.createdDirs, oldPath)
					delete(fs.dirContents, oldPath)
					fs.rmEntryFromItsDir(oldPath)
					fs.addNewEntryToItsDir(newPath, fuse.S_IFDIR)
				}
			}
			fs.Error("Rename mkdir failed", "path", localPathNew, "err", err)
			return fuse.ToStatus(err)
		}
	} else {
		// first trigger a remote copy of oldPath to newPath
		status := fs.writeRemote.copyFile(remotePathOld, remotePathNew)
		if status != fuse.OK {
			return status
		}

		if fs.writeRemote.cacheData {
			localPathOld := fs.writeRemote.getLocalPath(remotePathOld)
			localPathNew := fs.writeRemote.getLocalPath(remotePathNew)

			fmutex, err := fs.getFileMutex(localPathOld)
			if err != nil {
				return fuse.EIO
			}
			err = fmutex.Lock()
			if err != nil {
				fs.Error("Rename file mutex lock failed", "path", localPathOld, "err", err)
				return fuse.EIO
			}
			defer logClose(fs.Logger, fmutex, "Rename file mutex")
			fmutex2, err := fs.getFileMutex(localPathNew)
			if err != nil {
				return fuse.EIO
			}
			err = fmutex2.Lock()
			if err != nil {
				fs.Error("Rename file mutex lock failed", "path", localPathNew, "err", err)
				return fuse.EIO
			}
			defer logClose(fs.Logger, fmutex2, "Rename file mutex")

			// if we've cached oldPath, move to new cached file
			err = os.Rename(localPathOld, localPathNew)
			if err != nil {
				fs.Error("Rename of cached files failed", "source", localPathOld, "dest", localPathNew, "err", err)
			}
			fs.writeRemote.CacheRename(localPathOld, localPathNew)
		}

		// cache the existence of the new file
		fs.files[newPath] = fs.files[oldPath]
		fs.fileToRemote[newPath] = fs.fileToRemote[oldPath]
		if _, created := fs.createdFiles[oldPath]; created {
			fs.createdFiles[newPath] = true
			delete(fs.createdFiles, oldPath)
		}
		fs.addNewEntryToItsDir(newPath, fuse.S_IFREG)

		// finally unlink oldPath remotely
		r := fs.fileToRemote[oldPath]
		if r != nil {
			r.deleteFile(remotePathOld)
		}
		delete(fs.files, oldPath)
		delete(fs.fileToRemote, oldPath)
		delete(fs.createdFiles, oldPath)
		fs.rmEntryFromItsDir(oldPath)

		return fuse.OK
	}
	return fuse.ENOSYS
}

// Unlink deletes a file from the remote system, as well as any locally cached
// copy. context is not currently used.
func (fs *MuxFys) Unlink(name string, context *fuse.Context) fuse.Status {
	_, r, status := fs.fileDetails(name, true)
	if status != fuse.OK {
		return status
	}

	remotePath := r.getRemotePath(name)
	if r.cacheData {
		localPath := r.getLocalPath(remotePath)
		// *** we could file lock here, but that is a little wasteful if
		// localPath doesn't actually exist, and we'd have to file unlock eg.
		// Rename() and anything else that calls us
		err := syscall.Unlink(localPath)
		if err != nil {
			fs.Warn("Unlink failed", "path", localPath, "err", err)
		}
		r.CacheDelete(localPath)
	}

	fs.mapMutex.Lock()
	defer fs.mapMutex.Unlock()

	delete(fs.createdFiles, name)

	status = r.deleteFile(remotePath)
	if status != fuse.OK {
		return status
	}

	delete(fs.files, name)
	delete(fs.fileToRemote, name)
	fs.rmEntryFromItsDir(name)

	return fuse.OK
}

// Access is ignored.
func (fs *MuxFys) Access(name string, mode uint32, context *fuse.Context) fuse.Status {
	return fuse.OK
}

// Create creates a new file. mode and context are not currently used. When
// configured with CacheData the contents of the created file are only uploaded
// at Unmount() time.
func (fs *MuxFys) Create(name string, flags uint32, mode uint32, context *fuse.Context) (nodefs.File, fuse.Status) {
	return fs.create(name, flags, mode)
}

// create is the implementation of Create() that also takes an optional
// filemutex that should be Lock()ed (it will be Close()d).
func (fs *MuxFys) create(name string, flags uint32, mode uint32, fmutex ...*filemutex.FileMutex) (nodefs.File, fuse.Status) {
	r := fs.writeRemote
	if r == nil {
		return nil, fuse.EPERM
	}

	remotePath := r.getRemotePath(name)
	var localPath string
	if r.cacheData {
		localPath = r.getLocalPath(remotePath)

		if len(fmutex) == 1 {
			defer logClose(fs.Logger, fmutex[0], "file mutex", "path", localPath)
		} else {
			fm, err := fs.getFileMutex(localPath)
			if err != nil {
				return nil, fuse.EIO
			}
			err = fm.Lock()
			if err != nil {
				fs.Error("file mutex lock failed", "path", localPath, "err", err)
				return nil, fuse.EIO
			}
			defer logClose(fs.Logger, fm, "file mutex", "path", localPath)
		}
	}

	fs.mapMutex.Lock()
	defer fs.mapMutex.Unlock()

	attr, existed := fs.files[name]
	mTime := uint64(time.Now().Unix())
	if !existed {
		// add to our directory entries for this file's dir
		fs.addNewEntryToItsDir(name, fuse.S_IFREG)

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

		// *** when not appending, don't we need to reset to 0? It seems to work
		// without this, and we avoid incorrect reset to 0 when something
		// opens many simultaneous non-append writes to write at different
		// offsets.
		// if int(flags)&os.O_APPEND == 0 {
		// 	r.CacheDelete(localPath)
		// 	attr.Size = uint64(0)
		// }
	}
	fs.createdFiles[name] = true

	if r.cacheData {
		return newCachedFile(r, remotePath, localPath, attr, uint32(int(flags)|os.O_CREATE), fs.Logger), fuse.OK
	}
	return newRemoteFile(r, remotePath, attr, true, fs.Logger), fuse.OK
}

// addNewEntryToItsDir adds a DirEntry for the file/dir named name to that
// object's containing directory entries. mode should be fuse.S_IFREG or
// fuse.S_IFDIR. Must be called while you have the mapMutex Locked.
func (fs *MuxFys) addNewEntryToItsDir(name string, mode int) {
	d := fuse.DirEntry{
		Name: filepath.Base(name),
		Mode: uint32(mode),
	}
	parent := filepath.Dir(name)
	if parent == "." {
		parent = ""
	}

	if _, exists := fs.dirContents[parent]; !exists {
		// we must populate the contents of parent first, doing the essential
		// part of OpenDir()
		if remotes, exists := fs.dirs[parent]; exists {
			for _, r := range remotes {
				status := fs.openDir(r, parent)
				if status != fuse.OK {
					fs.Warn("addNewEntryToItsDir openDir failed", "path", parent, "status", status)
				}
			}
		}
	}
	fs.dirContents[parent] = append(fs.dirContents[parent], d)
}

// rmEntryFromItsDir removes a DirEntry for the file/dir named name from that
// object's containing directory entries. Must be called while you have the
// mapMutex Locked.
func (fs *MuxFys) rmEntryFromItsDir(name string) {
	parent := filepath.Dir(name)
	if parent == "." {
		parent = ""
	}
	baseName := filepath.Base(name)

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

// getFileMutex prepares a lock file for the given local path (in that path's
// directory, creating the directory first if necessary), and returns a mutex
// that you should Lock() and Close().
func (fs *MuxFys) getFileMutex(localPath string) (*filemutex.FileMutex, error) {
	parent := filepath.Dir(localPath)
	if _, err := os.Stat(parent); err != nil && os.IsNotExist(err) {
		err = os.MkdirAll(parent, dirMode)
		if err != nil {
			fs.Error("Could not create parent directory", "path", localPath, "err", err)
			return nil, err
		}
	}
	mutex, err := filemutex.New(filepath.Join(parent, ".muxfys_lock."+filepath.Base(localPath)))
	if err != nil {
		fs.Error("Could not create lock file", "path", localPath, "err", err)
	}
	return mutex, err
}
