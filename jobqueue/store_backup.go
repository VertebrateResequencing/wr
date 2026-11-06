/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

package jobqueue

// This file holds the log store's self-contained backup (spec E5), and where a
// store's backup is kept: a local path, or objects under a key in an S3
// bucket.

import (
	"archive/tar"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/muxfys/v5"
	"github.com/VertebrateResequencing/wr/internal"
)

// logBackupWALHook, a test seam nil outside tests, is called by every backup
// once it has copied history.bolt, before it seals and copies the wal files.
var logBackupWALHook atomic.Pointer[func()] //nolint:gochecknoglobals // a test seam

// newS3Objects returns the objects in the S3 bucket path (bucket and any
// directories within it) reached with the credentials of profile. Tests
// replace it with a fake.
var newS3Objects = func(profile, path string) (s3Objects, error) { //nolint:gochecknoglobals
	config, err := muxfys.S3ConfigFromEnvironment(profile, path)
	if err != nil {
		return nil, err
	}

	accessor, err := muxfys.NewS3Accessor(config)
	if err != nil {
		return nil, err
	}

	return accessor, nil
}

// s3Objects is the part of muxfys.S3Accessor that backups use, so tests can
// stand a fake in for S3 (see newS3Objects).
type s3Objects interface {
	DownloadFile(source, dest string) error
	UploadFile(source, dest, contentType string) error
	DeleteFile(path string) error
	ListEntries(dir string) ([]muxfys.RemoteAttr, error)
}

// s3BackupLocation returns the objects of the bucket an S3 dbBkFile url
// (s3://[profile@]bucket/path/file) names, and the key of the backup in it. In
// development deployment the key gets "." + deployment appended, so a
// development manager never touches a production backup.
func s3BackupLocation(dbBkFile, deployment string) (s3Objects, string, error) {
	if deployment == internal.Development {
		dbBkFile += "." + deployment
	}

	path := strings.TrimPrefix(dbBkFile, internal.S3Prefix)
	pp := strings.Split(path, "@")

	profile := "default"
	if len(pp) == s3ProfilePathParts {
		profile = pp[0]
		path = pp[1]
	}

	dir := filepath.Dir(path)

	objects, err := newS3Objects(profile, dir)
	if err != nil {
		return nil, "", err
	}

	key, err := stripBucketFromS3Path(filepath.Join(dir, filepath.Base(path)))
	if err != nil {
		return nil, "", err
	}

	return objects, key, nil
}

// backupTarget is where a store's backup is kept. Later items add the
// operations that write and read a backup (spec E3).
type backupTarget interface {
	// removeAll deletes the backup and everything the DB keeps beside it.
	removeAll() error
}

// backupTargetAt returns the backupTarget of dbBkFile, a local path or an S3
// url (see s3BackupLocation) for a manager of the given deployment.
func backupTargetAt(dbBkFile, deployment string) (backupTarget, error) {
	if !internal.InS3(dbBkFile) {
		return dirTarget{path: dbBkFile}, nil
	}

	objects, key, err := s3BackupLocation(dbBkFile, deployment)
	if err != nil {
		return nil, err
	}

	return s3Target{objects: objects, key: key}, nil
}

// s3Target is a backupTarget for the backup at key in a bucket.
type s3Target struct {
	objects s3Objects
	key     string
}

// removeAll deletes the objects of the backup at t.key, as dirTarget's does
// the files: a store backup under key + "/", an old bbolt backup at key, and
// the bbolt files E1 keeps beside it at key + ".bolt-pre-" + v. Other objects
// whose keys merely start with key are left.
func (t s3Target) removeAll() error {
	for _, prefix := range []string{t.key + "/", t.key + storeBoltPreInfix} {
		if err := t.removeUnder(prefix); err != nil {
			return err
		}
	}

	return t.objects.DeleteFile(t.key)
}

// removeUnder deletes every object whose key starts with prefix, descending
// into the common prefixes S3 lists in place of deeper keys.
func (t s3Target) removeUnder(prefix string) error {
	entries, err := t.objects.ListEntries(prefix)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name, "/") && entry.Name != prefix {
			err = t.removeUnder(entry.Name)
		} else {
			err = t.objects.DeleteFile(entry.Name)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

// dirTarget is a backupTarget at a local path.
type dirTarget struct {
	path string
}

// removeAll removes the backup at t.path, file or directory, and the bbolt
// files E1 keeps beside it at path + ".bolt-pre-" + v.
func (t dirTarget) removeAll() error {
	return removeOwned(t.path)
}

// tarFile writes the file at path, which must not change while it is copied,
// to tw as one member named for its base name.
func tarFile(tw *tar.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}

	info, err := f.Stat()
	if err == nil {
		err = tw.WriteHeader(backupTarHeader(filepath.Base(path), info.Size()))
	}

	if err == nil {
		_, err = io.CopyN(tw, f, info.Size())
	}

	return errors.Join(err, f.Close())
}

// backupTarHeader returns the tar header of a self-contained backup's member
// called name holding size bytes.
func backupTarHeader(name string, size int64) *tar.Header {
	return &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     name,
		Size:     size,
		Mode:     dbFilePermission,
		ModTime:  time.Now(),
	}
}

// backup writes a self-contained backup of the store to w (spec E5): a tar of
// VERSION, the newest checkpoint, history.bolt and the wal files from the
// checkpoint's walFrom, or the copied history's folded position's file if
// that is earlier, through one it seals once history.bolt is copied, so that
// the backup holds every write acknowledged before backup was called. A store
// with no checkpoint gets one first.
//
// It holds ckptMu throughout, so no checkpoint deletes a file it copies, at
// the cost of delaying a checkpoint due meanwhile until it ends. A close
// meanwhile waits for it (see close).
func (s *logStore) backup(w io.Writer) error {
	if !s.startBackup() {
		return errDBClosed
	}

	defer s.backups.Done()

	s.ckptMu.Lock()
	defer s.ckptMu.Unlock()

	if s.ckptFrom == 0 {
		if err := s.checkpointLocked(); err != nil {
			return err
		}
	}

	tw := tar.NewWriter(w)

	if err := s.writeBackup(tw); err != nil {
		return err
	}

	return tw.Close()
}

// startBackup joins backups, so that close waits for the backup, and reports
// whether it did: it does not once the store is closed.
func (s *logStore) startBackup() bool {
	s.closeMu.RLock()
	defer s.closeMu.RUnlock()

	if s.closed {
		return false
	}

	s.backups.Add(1)

	return true
}

// writeBackup writes backup's tar members to tw. The caller holds ckptMu.
func (s *logStore) writeBackup(tw *tar.Writer) error {
	for _, path := range []string{filepath.Join(s.dir, storeVersionFile), checkpointPath(s.dir, s.ckptFrom)} {
		if err := tarFile(tw, path); err != nil {
			return err
		}
	}

	folded, err := s.history.backup(tw)
	if err != nil {
		return err
	}

	callHook(&logBackupWALHook)

	return s.tarWALFiles(tw, min(s.ckptFrom, folded.file))
}

// tarWALFiles seals the current wal file and writes to tw each wal file
// starting at number from and ending at the sealed one. The caller holds
// ckptMu.
func (s *logStore) tarWALFiles(tw *tar.Writer, from uint32) error {
	sealed, err := s.rotate()
	if err != nil {
		return err
	}

	files, err := walFileNumbers(s.dir)
	if err != nil {
		return err
	}

	for _, n := range files {
		if n < from || n > sealed {
			continue
		}

		if err = tarFile(tw, walFilePath(s.dir, n)); err != nil {
			return err
		}
	}

	return nil
}
