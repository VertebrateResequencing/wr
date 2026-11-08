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
// bucket. Its incremental backups (E4) are in store_backup_tick.go.

import (
	"archive/tar"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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

// errBackupNoStage is an S3 backup's put's error when it has no local
// directory to stage the file in.
var errBackupNoStage = errors.New("s3 backup has no staging directory")

// backupUploadName is the name in a store's directory of the file an S3
// backup's put stages before it uploads it; recovery deletes it, as a .tmp
// file, if a crash leaves it.
const backupUploadName = "backup-upload" + storeTmpExt

// backupPreDownloadName is the name in a store's directory that an S3
// backup's move aside downloads an existing ".bolt-pre-" object to, to
// compare; recovery deletes it, as a .tmp file, if a crash leaves it.
const backupPreDownloadName = "backup-pre" + storeTmpExt

// backupDownloadName is the name in a restore's directory that a one-file
// backup on S3 is downloaded to; recovery would delete it, as a .tmp file, if
// restore did not.
const backupDownloadName = "backup" + storeTmpExt

// backupForm is what a backupTarget holds.
type backupForm uint8

const (
	backupNone  backupForm = iota // no backup
	backupStore                   // a store's files: a directory, or objects under a prefix
	backupFile                    // one file: an old bbolt backup or a self-contained one
)

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

// backupEntry is a file of a store backup: its name, relative to the backup
// and separated by "/", and its size.
type backupEntry struct {
	name string
	size int64
}

// backupTarget is where a store's backup is kept: what reads it for a restore
// (spec E2) and what writes it, a tick at a time (spec E4).
//
//nolint:interfacebloat // the whole surface a backup location offers, as one seam over a directory and S3
type backupTarget interface {
	// form says what the backup is, if there is one.
	form() (backupForm, error)

	// list returns the files of a store backup.
	list() ([]backupEntry, error)

	// put writes the file name of a store backup, replacing any there, with
	// what write writes: locally through a .tmp file, paced as copyBackup
	// paces, then renamed into place, durably; on S3 as one object. It makes
	// the backup's directory if need be.
	put(name string, write func(w io.Writer) error) error

	// remove deletes the file name of a store backup, if it is there.
	remove(name string) error

	// sync makes the removes before it durable: locally by fsyncing the
	// backup's directory; on S3, whose deletes are, it does nothing.
	sync() error

	// moveAside renames a one-file backup holding bbolt to its location +
	// ".bolt-pre-" + v, as E1 keeps a converted bbolt file, so that a store
	// backup can take its place. Anything it downloads goes in dir. It returns
	// an error wrapping errBoltPreExists, naming that name, if it is taken,
	// and one wrapping errNotABackup if the backup is not bbolt, in either
	// case changing nothing.
	moveAside(dir string) error

	// download copies the store backup's file name to the local path local,
	// creating any directories it needs.
	download(name, local string) error

	// fetchFile returns a local path holding a one-file backup: the file
	// itself, or a download of it into dir, which done removes.
	fetchFile(dir string) (path string, done func() error, err error)

	// describe names the backup as restore messages do: "backup file <path>"
	// or "s3 backup file <key>".
	describe() string

	// location is the backup's path, or its key in its bucket.
	location() string

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

	// stage is the local directory put writes each file to before it uploads
	// it.
	stage string
}

// form lists t.key: objects under t.key + "/" are a store backup, which wins
// over an object at t.key itself, a one-file backup.
func (t s3Target) form() (backupForm, error) {
	entries, err := t.objects.ListEntries(t.key)
	if err != nil {
		return backupNone, err
	}

	form := backupNone

	for _, entry := range entries {
		switch entry.Name {
		case t.key + "/":
			return backupStore, nil
		case t.key:
			form = backupFile
		}
	}

	return form, nil
}

// list returns the objects under t.key + "/", their keys relative to it,
// leaving out folder markers.
func (t s3Target) list() ([]backupEntry, error) {
	prefix := t.key + "/"

	var entries []backupEntry

	err := t.walkUnder(prefix, func(entry muxfys.RemoteAttr) error {
		if name := strings.TrimPrefix(entry.Name, prefix); name != "" && !strings.HasSuffix(name, "/") {
			entries = append(entries, backupEntry{name: name, size: entry.Size})
		}

		return nil
	})

	return entries, err
}

// put writes what write writes to a .tmp file in t.stage, paced as dirTarget
// paces, then uploads it as the object t.key + "/" + name and removes it. It
// stages the file, as the bbolt store's S3 backup went through a local cache,
// because an upload of unknown length buffers a 537MiB part in memory.
func (t s3Target) put(name string, write func(w io.Writer) error) error {
	if t.stage == "" {
		return errBackupNoStage
	}

	path := filepath.Join(t.stage, backupUploadName)

	err := writePacedFile(path, write)
	if err == nil {
		err = t.objects.UploadFile(path, t.key+"/"+name, "")
	}

	if errr := os.Remove(path); !errors.Is(errr, fs.ErrNotExist) {
		err = errors.Join(err, errr)
	}

	return err
}

// writePacedFile writes what write writes to the file at path through a
// backupCopyWriter, pacing its writeback every backupCopySyncBytes, then
// fsyncs it. A failed write removes the file.
func writePacedFile(path string, write func(w io.Writer) error) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, dbFilePermission)
	if err != nil {
		return err
	}

	err = write(&backupCopyWriter{f: f, syncEvery: backupCopySyncBytes})
	if err == nil {
		err = f.Sync()
	}

	if err = errors.Join(err, f.Close()); err != nil {
		return errors.Join(err, os.Remove(path))
	}

	return nil
}

// remove deletes the object t.key + "/" + name.
func (t s3Target) remove(name string) error {
	return t.objects.DeleteFile(t.key + "/" + name)
}

// sync does nothing, as an S3 delete is durable once it returns.
func (t s3Target) sync() error {
	return nil
}

// moveAside copies the bbolt object at t.key to t.key + ".bolt-pre-" + v
// through a download into dir, then deletes it. If that name is taken by a
// copy of it, as a crash between the copy and the delete leaves, it finishes
// the move (see finishMoveAside).
func (t s3Target) moveAside(dir string) error {
	pre := t.key + storeBoltPreInfix + convertVersion()

	entries, err := t.objects.ListEntries(pre)
	if err != nil {
		return err
	}

	if i := slices.IndexFunc(entries, func(e muxfys.RemoteAttr) bool { return e.Name == pre }); i >= 0 {
		return t.finishMoveAside(dir, pre, entries[i].Size)
	}

	path, done, err := t.fetchFile(dir)
	if err != nil {
		return err
	}

	err = uploadBolt(t.objects, path, pre, t.key)

	return errors.Join(err, done())
}

// uploadBolt uploads the bbolt file at path to the object pre, then deletes
// the object key it was downloaded from. A file that is not bbolt is
// errNotABackup.
func uploadBolt(objects s3Objects, path, pre, key string) error {
	isBolt, err := hasBoltMagic(path)
	if err != nil {
		return err
	}

	if !isBolt {
		return fmt.Errorf("%w: s3 backup file %s is not a bbolt file, so it has been left in place", errNotABackup,
			key)
	}

	if err = objects.UploadFile(path, pre, ""); err != nil {
		return err
	}

	return objects.DeleteFile(key)
}

// finishMoveAside deletes the object t.key if the object pre, of size bytes,
// holds the same bytes, comparing downloads of both into dir only when their
// sizes match. Otherwise it returns an error wrapping errBoltPreExists, naming
// pre, changing nothing.
func (t s3Target) finishMoveAside(dir, pre string, size int64) error {
	taken := fmt.Errorf("%w: s3 backup file %s", errBoltPreExists, pre)

	entries, err := t.objects.ListEntries(t.key)
	if err != nil {
		return err
	}

	if !slices.ContainsFunc(entries, func(e muxfys.RemoteAttr) bool { return e.Name == t.key && e.Size == size }) {
		return taken
	}

	same, err := t.sameObjects(dir, pre)
	if err != nil {
		return err
	}

	if !same {
		return taken
	}

	return t.objects.DeleteFile(t.key)
}

// sameObjects reports whether the objects t.key and pre hold the same bytes,
// downloading both into dir, then removing the downloads.
func (t s3Target) sameObjects(dir, pre string) (bool, error) {
	path, done, err := t.fetchFile(dir)
	if err != nil {
		return false, err
	}

	prePath := filepath.Join(dir, backupPreDownloadName)

	err = t.objects.DownloadFile(pre, prePath)

	var same bool

	if err == nil {
		same, err = sameFileContent(path, prePath)
	}

	if errr := os.Remove(prePath); !errors.Is(errr, fs.ErrNotExist) {
		err = errors.Join(err, errr)
	}

	return same, errors.Join(err, done())
}

// sameFileContent reports whether the files at a and b hold the same bytes.
func sameFileContent(a, b string) (bool, error) {
	sumA, err := fileSum(a)
	if err != nil {
		return false, err
	}

	sumB, err := fileSum(b)

	return err == nil && sumA == sumB, err
}

// download downloads the object t.key + "/" + name to local. The object store
// creates local's directories, and stages the object beside local, so a
// restore downloads only inside the directory it removes on failure.
func (t s3Target) download(name, local string) error {
	return t.objects.DownloadFile(t.key+"/"+name, local)
}

// fetchFile downloads the object at t.key into dir.
func (t s3Target) fetchFile(dir string) (string, func() error, error) {
	path := filepath.Join(dir, backupDownloadName)
	if err := t.objects.DownloadFile(t.key, path); err != nil {
		return "", nil, err
	}

	return path, func() error { return os.Remove(path) }, nil
}

// describe returns "s3 backup file <key>".
func (t s3Target) describe() string {
	return "s3 backup file " + t.key
}

// location returns t.key.
func (t s3Target) location() string {
	return t.key
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

// removeUnder deletes every object whose key starts with prefix.
func (t s3Target) removeUnder(prefix string) error {
	return t.walkUnder(prefix, func(entry muxfys.RemoteAttr) error { return t.objects.DeleteFile(entry.Name) })
}

// walkUnder calls fn with every object whose key starts with prefix,
// descending into the common prefixes S3 lists in place of deeper keys.
func (t s3Target) walkUnder(prefix string, fn func(entry muxfys.RemoteAttr) error) error {
	entries, err := t.objects.ListEntries(prefix)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name, "/") && entry.Name != prefix {
			err = t.walkUnder(entry.Name, fn)
		} else {
			err = fn(entry)
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

// form stats t.path: a directory is a store backup and a regular file a
// one-file backup. An empty path holds no backup.
func (t dirTarget) form() (backupForm, error) {
	if t.path == "" {
		return backupNone, nil
	}

	info, err := os.Stat(t.path)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return backupNone, nil
	case err != nil:
		return backupNone, err
	case info.IsDir():
		return backupStore, nil
	case info.Mode().IsRegular():
		return backupFile, nil
	default:
		return backupNone, fmt.Errorf("%w: backup %s is neither a directory nor a regular file",
			errNotABackup, t.path)
	}
}

// list returns the regular files under t.path, their paths relative to it.
func (t dirTarget) list() ([]backupEntry, error) {
	var entries []backupEntry

	err := fs.WalkDir(os.DirFS(t.path), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}

		info, err := entry.Info()
		if err == nil {
			entries = append(entries, backupEntry{name: path, size: info.Size()})
		}

		return err
	})

	return entries, err
}

// put writes name under t.path through name + ".tmp", paced by a
// backupCopyWriter, then fsyncs it, renames it into place and fsyncs the
// directory. A failed write removes the .tmp file.
func (t dirTarget) put(name string, write func(w io.Writer) error) error {
	if err := t.makeDir(); err != nil {
		return err
	}

	path := filepath.Join(t.path, name)
	tmp := path + storeTmpExt

	if err := writePacedFile(tmp, write); err != nil {
		return err
	}

	if err := os.Rename(tmp, path); err != nil {
		return errors.Join(err, os.Remove(tmp))
	}

	return syncDir(t.path)
}

// makeDir makes t.path a directory, durably, if it is not there.
func (t dirTarget) makeDir() error {
	_, err := os.Stat(t.path)
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	if err = os.MkdirAll(t.path, logStoreDirPerm); err != nil {
		return err
	}

	return syncDir(filepath.Dir(t.path))
}

// remove deletes name under t.path, if it is there.
func (t dirTarget) remove(name string) error {
	err := os.Remove(filepath.Join(t.path, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	return err
}

// sync fsyncs t.path, so that the removes made in it are durable.
func (t dirTarget) sync() error {
	return syncDir(t.path)
}

// moveAside renames the bbolt file at t.path to t.path + ".bolt-pre-" + v,
// durably.
func (t dirTarget) moveAside(string) error {
	pre := t.path + storeBoltPreInfix + convertVersion()

	_, err := os.Lstat(pre)

	switch {
	case err == nil:
		return fmt.Errorf("%w: backup file %s", errBoltPreExists, pre)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}

	isBolt, err := hasBoltMagic(t.path)
	if err != nil {
		return err
	}

	if !isBolt {
		return fmt.Errorf("%w: backup file %s is not a bbolt file, so it has been left in place", errNotABackup,
			t.path)
	}

	if err = os.Rename(t.path, pre); err != nil {
		return err
	}

	return syncDir(filepath.Dir(t.path))
}

// download copies the file name under t.path to local.
func (t dirTarget) download(name, local string) error {
	if err := os.MkdirAll(filepath.Dir(local), logStoreDirPerm); err != nil {
		return err
	}

	return copyFile(filepath.Join(t.path, filepath.FromSlash(name)), local)
}

// fetchFile returns t.path itself, which it never changes.
func (t dirTarget) fetchFile(string) (string, func() error, error) {
	return t.path, func() error { return nil }, nil
}

// describe returns "backup file <path>".
func (t dirTarget) describe() string {
	return "backup file " + t.path
}

// location returns t.path.
func (t dirTarget) location() string {
	return t.path
}

// fileSum returns the sha256 of the file at path.
func fileSum(path string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte

	f, err := os.Open(path)
	if err != nil {
		return sum, err
	}

	h := sha256.New()
	_, err = io.Copy(h, f)
	h.Sum(sum[:0])

	return sum, errors.Join(err, f.Close())
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
